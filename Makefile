# NeNe Recall のビルドと品質ゲート。
#
# 🔴 `make check` がこのリポジトリの唯一の正である（QLT-003）。
#    CI は check を呼ぶだけで、CI 側にだけ存在する検査を作らない。
#    「ローカルでは通ったのに CI で落ちた」を構造的に起こさないため。
GO ?= go
BIN := bin/recall
COVER_OUT := coverage.out

# 🔴 道具はバージョンを固定する。固定しないと、同じコードが日によって
#    通ったり落ちたりする。ゲートは再現しなければ意味がない。
GOLANGCI_VERSION := v2.13.2
GOVULNCHECK_VERSION := v1.7.0

# 🔴 カバレッジの下限。下げる変更は ADR を要する（QLT-007）。
#    2026-09-01 時点の実測は 79.4%。この値は上げる方向にしか動かさない。
MIN_COVERAGE := 75.0

.PHONY: all check build run test cover cover-check vet fmt fmt-check lint conformance tidy tidy-check vuln tools clean

# 既定は完全なゲート。部分的な確認をしたいときだけ個別ターゲットを呼ぶ。
all: check

## check — 提出前に必ず通すもの。CI もこれを呼ぶ
check: fmt-check vet lint conformance test cover-check tidy-check build

## build — 🔴 CGO_ENABLED=0 は cgo 禁止（CLAUDE.md 地雷5）のコンパイル時強制である。
## cgo を要求する依存が紛れ込んだ瞬間にここで落ちる。
build:
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/recall

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

clean:
	rm -rf bin $(COVER_OUT)
