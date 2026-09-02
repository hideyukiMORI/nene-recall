# NeNe Recall のビルドと品質ゲート。
#
# 🔴 `make check` がこのリポジトリの唯一の正である（QLT-003）。
#    CI は check を呼ぶだけで、CI 側にだけ存在する検査を作らない。
#    「ローカルでは通ったのに CI で落ちた」を構造的に起こさないため。
GO ?= go
BIN := bin/recall
CTL_BIN := bin/recallctl
EVAL_BIN := bin/eval
COVER_OUT := coverage.out

# 🔴 道具はバージョンを固定する。固定しないと、同じコードが日によって
#    通ったり落ちたりする。ゲートは再現しなければ意味がない。
GOLANGCI_VERSION := v2.13.2
GOVULNCHECK_VERSION := v1.7.0

# 🔴 カバレッジの下限。下げる変更は ADR を要する（QLT-007）。
#    2026-09-01 時点の実測は 79.4%。この値は上げる方向にしか動かさない。
MIN_COVERAGE := 75.0

# 評価レポートの書き出し先。EVAL_LABEL で条件に名前を付ける（alpha 掃きなど）。
EVAL_LABEL ?= baseline
EVAL_OUT ?= docs/benchmarks/data/$(shell date +%F)-eval-$(EVAL_LABEL).json

# 掃引のための条件。空なら cmd/eval の既定に任せる
# （alpha は RECALL_DEFAULT_ALPHA、rounds は eval.DefaultRounds）。
#
# 🔴 空のときにフラグ自体を渡さないこと。-alpha "" は解釈できず、
# -alpha 0 との区別もつかない。既定に任せることと 0 を指定することは別物である
# （alpha=0 は純語彙という意味のある条件で、Q-3 の掃引で実際に使う）。
EVAL_ALPHA ?=
EVAL_ROUNDS ?=
EVAL_FUSION ?=
# 🔴 EVAL_STORE は「どのバックエンドで測るか」。既定は cmd/eval の postgres。
#    sqlite は比較実測用（ADR 0017）。rrf は postgres でしか指定できない。
EVAL_STORE ?=
EVAL_SQLITE_PATH ?=
# 🔴 EVAL_TOKENIZER は「どの分割器で測るか」。既定は cmd/eval の bigram。
#    kagome（ADR 0018）と union（ADR 0021・両者の連結）は比較実測用。
#    既定を移すのは実測を見てからである。
EVAL_TOKENIZER ?=
# 🔴 EVAL_DISTRACTORS は「正解にならない紛れ込みの JSONL」（ADR 0019）。
#    testdata/eval/ は1バイトも変えず、10万件は別のファイルとして足す。
#    生成は tools/wikidistract（README に手順）。生成物はコミットしない。
# 🔴 EVAL_EMBED_CACHE は埋め込みの置き場。クエリ側はキャッシュしないので、
#    系統1（埋め込み往復を含む）の latency はキャッシュの有無で変わらない。
EVAL_DISTRACTORS ?=
EVAL_EMBED_CACHE ?=
# 🔴 EVAL_MODE は「候補集合をどう作るか」（ADR 0022）。既定は cmd/eval の exhaustive。
#    candidates は索引（HNSW / GIN）を効かせる計測モードで、既定を移すのは
#    after の実測を見て別の ADR を書いてからである。
# 🔴 EVAL_CANDIDATE_K / EVAL_EF_SEARCH は candidates のときの条件。
#    K <= ef_search でなければ HNSW は K 件を返せないので、既定の K=100 で
#    測るなら EVAL_EF_SEARCH も 100 以上へ上げること（構築時に拒否される）。
EVAL_MODE ?=
EVAL_CANDIDATE_K ?=
EVAL_EF_SEARCH ?=
# 🔴 EVAL_DB_NAME は作り直す評価用 DB の名前。既定は cmd/eval の recall_eval。
#    複数のレーンが同じ Postgres に対して同時に測るとき、共有の recall_eval を
#    互いに DROP して壊し合わないために分ける。recall_eval で始まる名前だけが
#    通る（このコマンドは指定された DB を DROP するので、任意の DB を消せる口に
#    しない）。ポートと認証は定数のままで、別の Postgres は向けられない。
EVAL_DB_NAME ?=
EVAL_FLAGS := $(if $(EVAL_ALPHA),-alpha $(EVAL_ALPHA)) \
              $(if $(EVAL_ROUNDS),-rounds $(EVAL_ROUNDS)) \
              $(if $(EVAL_FUSION),-fusion $(EVAL_FUSION)) \
              $(if $(EVAL_STORE),-store $(EVAL_STORE)) \
              $(if $(EVAL_SQLITE_PATH),-sqlite-path $(EVAL_SQLITE_PATH)) \
              $(if $(EVAL_TOKENIZER),-tokenizer $(EVAL_TOKENIZER)) \
              $(if $(EVAL_DISTRACTORS),-distractors $(EVAL_DISTRACTORS)) \
              $(if $(EVAL_EMBED_CACHE),-embed-cache $(EVAL_EMBED_CACHE)) \
              $(if $(EVAL_MODE),-mode $(EVAL_MODE)) \
              $(if $(EVAL_CANDIDATE_K),-candidate-k $(EVAL_CANDIDATE_K)) \
              $(if $(EVAL_EF_SEARCH),-ef-search $(EVAL_EF_SEARCH)) \
              $(if $(EVAL_DB_NAME),-eval-db $(EVAL_DB_NAME))

.PHONY: all check build run test cover cover-check vet fmt fmt-check lint conformance tidy tidy-check vuln tools clean eval

# 既定は完全なゲート。部分的な確認をしたいときだけ個別ターゲットを呼ぶ。
all: check

## check — 提出前に必ず通すもの。CI もこれを呼ぶ
check: fmt-check vet lint conformance test cover-check tidy-check build

## build — 🔴 CGO_ENABLED=0 は cgo 禁止（CLAUDE.md 地雷5）のコンパイル時強制である。
## cgo を要求する依存が紛れ込んだ瞬間にここで落ちる。
##
## 🔴 対象を ./cmd/eval へ広げない（施主決定・ADR 0013 Decision 10）。
## build は「配布する成果物を作る」ターゲットであり、評価ランナーは開発者の道具で
## あって成果物ではない。意味を変えないこと。
## cmd/eval のコンパイル破壊なら check が既に検知する——go vet ./... と
## go test ./... がどちらも全パッケージを通るので、埋めるべき穴が無い。
##
## 🔴 recallctl は成果物なので、こちらは build の対象に入れる。CLI は個人利用の
## 入口そのもので、配布して使うものである（docs/adr/0016-cli-is-an-http-client-with-org-default.md
## の Consequences）。評価ランナーを外す方針と矛盾しない——線は「開発者の道具か
## 利用者の道具か」で引いている。
build:
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/recall
	CGO_ENABLED=0 $(GO) build -trimpath -o $(CTL_BIN) ./cmd/recallctl

run:
	$(GO) run ./cmd/recall

## test — race 検出は常に有効。競合は「たまに壊れる」形で出るので既定で踏む
test:
	$(GO) test ./... -race -cover -count=1

cover:
	$(GO) test ./... -coverprofile=$(COVER_OUT) -covermode=atomic
	$(GO) tool cover -func=$(COVER_OUT) | tail -1

## cover-check — 下限を割ったら落とす
cover-check:
	@$(GO) test ./... -coverprofile=$(COVER_OUT) -covermode=atomic > /dev/null
	@total=$$($(GO) tool cover -func=$(COVER_OUT) | awk '/^total:/ {print $$3}' | tr -d '%'); \
	printf 'coverage: %s%% (floor %s%%)\n' "$$total" "$(MIN_COVERAGE)"; \
	awk -v t="$$total" -v m="$(MIN_COVERAGE)" 'BEGIN { exit (t+0 >= m+0) ? 0 : 1 }' \
	  || { echo "✗ カバレッジが下限を割った。テストを足すこと（下限を下げるのは ADR 事項）"; exit 1; }

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

## fmt-check — gofmt に設定は無い。整形の流儀を議論する余地がそもそもない
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  echo "✗ gofmt されていないファイル:"; echo "$$unformatted"; exit 1; \
	fi

## lint — golangci-lint。規則との対応は docs/coding-rules.md
lint: tools
	golangci-lint config verify
	golangci-lint run

## conformance — このリポジトリ固有の規約検査（tools/conformance）
conformance:
	$(GO) test ./tools/conformance/ -run TestRepositoryHasNoViolations -count=1

tidy:
	$(GO) mod tidy

## tidy-check — go.mod / go.sum に差分が出る状態でコミットさせない（QLT-004）
tidy-check:
	$(GO) mod tidy -diff

## vuln — 既知の脆弱性。依存ゼロでも標準ライブラリ自体が対象になる
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

## tools — 固定バージョンの道具を入れる。既に正しい版が入っていれば何もしない
tools:
	@if ! command -v golangci-lint > /dev/null 2>&1 \
	  || ! golangci-lint --version 2>&1 | grep -q "$(GOLANGCI_VERSION:v%=%)"; then \
	  echo "golangci-lint $(GOLANGCI_VERSION) を導入する"; \
	  $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION); \
	fi

## eval — 検索品質を計測する（ADR 0009 / ADR 0013）
##
## 🔴 check に含めない。理由は2つある（ADR 0013 の Decision 5）。
##   (1) CI に Ollama も GPU も無い。偽の Embedder で recall を測っても、
##       それは偽の Embedder の性質を測っているだけで評価にならない
##   (2) 評価は「検査」ではなく「計測」である。recall@10 = 0.83 は真でも偽でもない。
##       ADR 0009 自身が「数十クエリで測った recall を過信するな」と書く粒度に
##       自動 fail の閾値を切ると、1クエリ分のゆらぎで CI が赤くなる。
##       赤が意味を持たなくなれば、ゲート全体の信頼が落ちる（QLT-002 が baseline を
##       拒否したのと同じ理由）
##
## 🔑 ただし決定的で依存の無い部分は check に入っている。
##   - internal/eval の指標計算とローダのユニットテスト
##   - 評価セットの整合性テスト（cmd/eval/dataset_test.go）
##   ⇒ 評価セットを壊すコミットは CI で落ちる。
##
## 前提: docker compose up -d（PostgreSQL）と Windows 側の Ollama、.env の設定。
## 評価用 DB recall_eval は毎回作り直される（開発用の recall には触らない）。
##
## 🔑 EVAL_STORE=sqlite なら PostgreSQL は要らない（Ollama は要る）。評価用の
## ファイル bin/recall_eval.db を毎回作り直す。⚠️ 2つのストアの数字を比べる
## ときは、必ず同じ評価セット（同じ sha256）・同じ alpha で測ること。
## recall の差には「ストアの差」と「語彙採点関数の差」（ts_rank と bm25）が
## 混ざるので、レポートの conditions.ranking を見て分けて読む（ADR 0017）。
##
## 🔴 go run ではなく go build してから走らせる。go run はバイナリに VCS 情報を
## 埋めないので、vcs.revision が空になる（2026-09-01 実測）。どのコードで測った
## 数字か分からないレポートは、後から検証できない。
##
## 例) make eval EVAL_LABEL=alpha-05 EVAL_ALPHA=0.5 GPU_NOTE="他アプリが 5.7GB 使用中"
##     make eval EVAL_LABEL=alpha-00 EVAL_ALPHA=0   （純語彙）
##     make eval EVAL_LABEL=alpha-10 EVAL_ALPHA=1   （純ベクトル・基準線と同条件）
##     make eval EVAL_LABEL=rrf EVAL_FUSION=rrf      （順位融合・alpha は無視される）
##     make eval EVAL_LABEL=sqlite EVAL_STORE=sqlite （比較用の SQLite・ADR 0017）
##     make eval EVAL_LABEL=kagome EVAL_TOKENIZER=kagome （形態素分割・ADR 0018）
##     make eval EVAL_LABEL=union EVAL_TOKENIZER=union   （bigram と形態素の和集合・ADR 0021）
##     make eval EVAL_LABEL=candidates EVAL_MODE=candidates EVAL_EF_SEARCH=100
##                                                   （索引を効かせる候補生成・ADR 0022）
##
## 🔴 EVAL_MODE=candidates と exhaustive の差を「索引の効果」と読まないこと。
##    索引は migration 0004 で常に張られており、exhaustive の SQL は張られていても
##    索引を使わない（ORDER BY が合成式）。差には候補生成の効果が混ざる。
## ⚠️ 候補モードでは語彙スコアの正規化に使う最大値が「候補集合内の最大値」に
##    変わるので、alpha の既定 0.8（exhaustive で掃引した値）は持ち込めない。
##
## 🔴 他のレーンと同時に測るときは EVAL_DB_NAME を分けること。既定の recall_eval は
##    毎回 DROP されるので、2本が同じ名前を使うと互いの計測を壊す。
##
##     make eval EVAL_LABEL=candidates EVAL_MODE=candidates EVAL_EF_SEARCH=100 \
##       EVAL_DB_NAME=recall_eval_lane17
##
## 🔑 10万件規模の実測（ADR 0019）。紛れ込みは tools/wikidistract で生成する
##    （手順は tools/wikidistract/README.md）。初回は埋め込みに約20分かかり、
##    2回目以降はキャッシュから返るので投入が数十秒で終わる。
##
##     make eval EVAL_LABEL=100k \
##       EVAL_DISTRACTORS=bin/wikidistract/distractors-100k.jsonl \
##       EVAL_EMBED_CACHE=bin/embed-cache
##
## ⚠️ 紛れ込みを足した数字と足していない数字を同じラベルで上書きしないこと。
##    recall の定義は変わらないが意味は変わる（レポートの conditions.distractors
##    に件数と sha256 が残るので、並べるときは必ずそこを見る）。
##
## 🔴 条件を変えたら EVAL_LABEL も変えること。同じ名前で上書きすると、
## 前の条件のレポートが消えて before/after を並べられなくなる。
eval:
	CGO_ENABLED=0 $(GO) build -trimpath -o $(EVAL_BIN) ./cmd/eval
	./$(EVAL_BIN) -out $(EVAL_OUT) -gpu-note "$(GPU_NOTE)" $(EVAL_FLAGS)

clean:
	rm -rf bin $(COVER_OUT)
