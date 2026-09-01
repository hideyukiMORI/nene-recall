# CLAUDE.md — NeNe Recall

Claude Code / AI エージェント向け実行ガイド。このファイルだけで作業を開始できる状態を保つ。

---

## 🔴 このリポジトリはフリート統治の射程外

**NeNe Recall は施主個人の必要から始まったプロジェクトであり、以下の対象に含めない:**

- 週次監査・棚卸し・フリート横断施策
- `_work/board.txt` の横断 TODO（このリポの TODO はこのリポ内で持つ）
- 統合リナの指揮対象（`nene-trace` と同じ「木の中にあるが射程外」の扱い）

将来 Corpus と統合しても、この扱いは施主が明示的に変えるまで維持する。
根拠: 2026-09-01 の施主判断。前例は `nene-trace`・reito 系。

---

## プロダクト一言要約

**Corpus が知識を蓄え、Recall が引き出す。**

Go 製の自己ホスト型 検索・取得サービス。チャンクを取り込み、ベクトル類似度と語彙一致の
ハイブリッドで検索し、引用可能な chunk を JSON で返す HTTP API。
**完全ローカル・費用0円。**

```
┌─ Windows ────────────┐    ┌─ WSL ──────────────────────┐
│ Ollama + bge-m3      │◀───┤ nene-recall (Go)           │
│ RTX 3090             │HTTP│  ├ HTTP API                │
│ ※変換のみ・状態なし   │───▶│  ├ ハイブリッド検索         │
└──────────────────────┘    │  └ PostgreSQL + pgvector   │
                            └────────────────────────────┘
```

**Ollama はベクトル DB ではない。** テキスト→ベクトルの変換だけを行う状態のないサーバ。
保存も検索も Recall（Go）の中。

---

## 現在の状況

| フェーズ | 状態 |
| --- | --- |
| Phase 0 骨組み・設計判断 | ✅ 完了（2026-09-01） |
| Phase 1 ローカル完結の検索 API | 🚧 着手（項目 1・3 完了） |
| Phase 2 Corpus 統合 | 🔲 未着手 |

**動くもの:** `/healthz`・`/readyz`・全エンドポイントの `org_id` 検証・設定の検証・graceful shutdown。
`internal/store/postgres` の投入・削除・ベクトル検索（実 Postgres に対する統合テスト付き）

🔴 **動かないもの:** `/v1/search` と `/v1/chunks` 系は依然 `501 Not Implemented`。
ストアは実装済みだが **`cmd` の配線と `httpapi` の差し替えがまだ**なので、HTTP からは届かない。
ストアが動くこととエンドポイントが動くことは別である。

### Phase 1 の残作業（着手順）

1. ~~**Postgres ストア**~~ — ✅ 完了。`internal/store/postgres`。`jackc/pgx` を
   `pgx/v5/stdlib` 経由で `database/sql` として使う（ADR 0011）。DDL・自前の
   マイグレーションランナー・`vector(1024)` 列・Writer。**ベクトル索引は作っていない**
   （ADR 0007。`TestNoVectorIndexExists` が機械的に守っている）
2. **Ollama 埋め込みクライアント** — `internal/embed/ollama.go`。`Embedder` を実装。
   🔴 **バッチで送れる形にすること**。230字のチャンクで **1本ずつ 11.8 件/秒 → 32本まとめて 87.8 件/秒＝8倍**
   （実測 `docs/benchmarks/2026-09-01-baseline.md`）。ナイーブに1チャンク1リクエストで書くと、
   10万件の取り込みが 18分から**2時間21分**になる。32本前後で頭打ちなので、それ以上大きくしても無駄
3. ~~**ベクトル検索**~~ — ✅ 完了。索引なしの全探索。演算子は **`<#>`（負の内積）** を採用した。
   前提（入力が常に正規化されている）は黙って置かず、`Embedder` の契約・`validateVector` の
   実行時検査・違反時の即エラー化の3つで支えている。詳細は `internal/store/postgres/searcher.go`
4. **語彙検索** — 未決。Postgres の `tsvector` か Go 側 BM25 か（要件定義 Q-1）
5. **ハイブリッド合成** — `alpha*vector + (1-alpha)*lexical`
6. **評価** — `make eval`。`testdata/eval/` に日本語の正解セット。recall@k・MRR・p95（ADR 0009）
7. **実測と HNSW** — ベンチを取り `docs/benchmarks/` に記録してから索引を入れる。before/after を残す
8. **SQLite ストア**（比較用）— `RECALL_STORE=sqlite`。同一データでの比較が成果物になる
9. **CLI** — 個人利用の入口。`org_id` の既定値は**CLI 側に置く。サーバ側には置かない**

---

## 開発コマンド

```bash
docker compose up -d      # PostgreSQL + pgvector (pgvector/pgvector:pg17)
ollama pull bge-m3        # Windows 側で実行

make check                # 🔴 提出前に必ず通す唯一のゲート。CI もこれを呼ぶ
make run                  # .env が要る
```

🔴 **`make check` は Postgres の起動を前提にする**（2026-09-01 施主承認）。
ストアのテストはモック SQL ではなく実 Postgres に対して走る。先に `docker compose up -d`
を実行すること。DB が無いとテストは Skip し、カバレッジ下限を割って `make check` が落ちる。

🔴 **DB のホスト側ポートは 5432 ではなく 5433。標準ポートに戻さないこと。**
施主の WSL では 5432 を systemd 管理のネイティブ PostgreSQL 14 が占有しており
（NENE2 のテスト DB `nene2_test` が入っているので止められない）、Docker の転送が
5432 を bind できない。5432 に戻すとネイティブ側へ繋がり、
**「コンテナは healthy なのに SASL 認証失敗」**という辿りにくい壊れ方をする。
理由の正本は `compose.yaml` のコメント。変更するときは `compose.yaml`・
`.github/workflows/ci.yml`・`.env.example`・統合テストの DSN 定数の**4箇所を必ず同時に**直すこと。

`make check` の中身は fmt-check → vet → lint → conformance → test → cover-check →
tidy-check → build。個別に走らせたいときだけ `make lint` `make test` 等を使う。
**CI 側にしか無い検査は作らない**（QLT-003）。初回は `make tools` が
`golangci-lint`（バージョン固定）を導入する。

Go は `/usr/local/go/bin`（1.27.0・公式 tarball から導入）。
`/etc/profile.d/go.sh` と各 `.bashrc` で PATH に入っている。

---

## 🔴 触る前に読む地雷

### 1. `org_id` に既定値を置かない

サーバ側のどこにも「org_id が無かったら 1 を使う」を書かないこと。
Corpus では分離条件が SQL の WHERE 句に埋まっていた（`PdoChunkSearchRepository.php:73`）が、
検索を外出しした結果、**分離の責任が Go 側へ移っている**。

ここを緩めると、あるテナントの検索が別テナントの文書を返す。
そして**単一テナントで開発・テストしている限り、この欠陥は一切症状を出さない**。

**2026-09-01 から、この規約はテストだけでなく型と検査器が守っている**（ADR 0010）:

- `org.ID` 型（`internal/org`）。生成経路は `org.NewID` / `org.ParseID` の2つだけ
- `CNF-001` が `org.ID(1)` のような**直接変換を禁止**する。
  Go には private constructor が無いので、この禁止は型ではなく `tools/conformance` が担う
- `CNF-002` が `org_id` を名乗るフィールド・引数の型が `int64` に退化していないか全ファイルで見る
- `TestOrgIDIsMandatory` が10ケースで HTTP 層を縛る（**ケースを減らす変更を入れないこと**）

正本は ADR 0003。機械強制の設計は ADR 0010。

### 2. 埋め込みモデルを変えたら保存済みベクトルは無効

次元が一致していても異なるモデルのベクトルは比較できない。
**エラーにならないまま無意味なスコアが返る。**
`Embedder.ID()`（例 `bge-m3:1024`）を保存済みベクトルのメタデータに記録し、
不一致を検知して拒否すること。ADR 0005。

ローカル実行はモデルの切替が容易なぶん、**この罠を踏みやすくなっている**。

### 3. `Kind` はプロバイダごとに意味が違う。呼び出し側は常に渡す

| プロバイダ | `Kind` の扱い |
| --- | --- |
| `bge-m3` | 接頭辞もパラメータも**不要**。無視してよい |
| `multilingual-e5` | **`query: ` / `passage: ` の接頭辞が必須**。付け忘れで品質低下 |
| Voyage | `input_type` として送る。省略は品質低下（公式 FAQ 明記） |

差異を実装側に閉じ込めるのがインタフェースの役割。
**呼び出し側は実装が使うかに関わらず必ず `Kind` を渡す。** ADR 0008。

### 4. Anthropic に埋め込み API は無い。サブスクは API ではない

- 「Claude で埋め込みを取る」は**できない**。公式が
  `Anthropic does not offer its own embedding model.` と明記している。探しに行かないこと
- **ChatGPT / Grok のサブスクリプションは API アクセス権ではない。** 別課金
- **`codex` をサブプロセスで叩いて埋め込みを得ることはできない。**
  生成モデルに「ベクトルを出せ」と頼んでも、それらしい数値を作文するだけで
  意味的な距離が保存されない。埋め込みは専用に訓練された別種のモデル

### 5. cgo を持ち込まない

ドライバは純 Go の `jackc/pgx` を使う。`lib/pq` は非推奨、`sqlite-vec` や
`mattn/go-sqlite3` は cgo を要求する。クロスコンパイル可能という前提が壊れる。
SQLite ストア（比較用）も純 Go の `modernc.org/sqlite` を使う。

### 6. 🔑 HNSW 索引を最初から作らない

ADR 0007 の要点は「pgvector を選んだこと」ではなく「**測ってから索引を入れた経路**」。
最初から索引を張ると、**なぜ入れたかを数字で語れなくなり、ADR 0007 の価値が消える**。

手順は固定:
1. 索引なしで Phase 1 を完成させる
2. 10万件規模で p95 と recall を測り `docs/benchmarks/` に残す
3. `CREATE INDEX ... USING hnsw` を入れて before/after を並べて記録する

### 7. `alpha` の 0.7 に根拠は無い

現状ただの当て推量（要件定義 Q-3）。ADR 0009 の評価セットで最適値を決めるまで、
この値を「調整済み」であるかのように文書やコメントに書かないこと。

### 8. 検索の評価に LLM を使わない

`recall@k` と `MRR` は正解セットとの突き合わせで、純粋な集合演算。LLM を1回も呼ばない。
`ragas` の faithfulness のような LLM 審査員は**生成の評価**であって検索の評価ではなく、
生成は Recall のスコープ外（要件定義 §3.3）。

**結果として、最も価値の高い検証が最も費用のかからない方法で行える。** この性質を壊さないこと。

---

## ✅ Corpus 側への先行依頼 — 完了（2026-09-01）

`ChunkSearchResult::$score` の `int` → `float` 拡大は **`nene-corpus` PR #385 で着地済み**
（MERGED 2026-09-01T08:35:33Z / SHA `8befe998`）。値は変わっていない。

⇒ **Phase 2 の差し替えは、純粋にバックエンドの交換だけになった。** 型の壁はもう無い。
経緯と実測は ADR 0006 の「追記」節。

**Phase 2 に着手するとき、Corpus 側の score が int だと思って読まないこと。**

---

## 規約

- **ライセンス**: MIT（フリート標準）
- **費用**: **既定構成に有料サービス・有料ツールを持ち込まない。**
  外部 API を既定にする変更は ADR で明示的に判断すること
- **公開リポの docs**: 要件定義・ADR・OpenAPI・ベンチマーク・**コーディング規約**のみを正とする。
  運用ログ・日報の類はここに置かない
- 🔴 **厳格性は文章ではなく機械で強制する**（ADR 0010）。規則の正本は
  [`docs/coding-rules.md`](docs/coding-rules.md)。**すべての規則が active / planned / 不採用の
  状態を持つ**ので、planned を active と書き換えないこと。未実装の強制を実装済みに見せると、
  規約全体が信用できなくなる
- 🔴 **ゲートを弱める変更は ADR を要する**（QLT-005）。閾値の緩和・除外の追加・linter の削除が対象。
  `//nolint` は linter 名と理由の両方が必須（理由が無いと落ちる）
- **設計判断は必ず ADR にする**。`docs/adr/0000-template.md` を複製して書く。
  **「なぜそうしなかったか」（却下した選択肢とその理由）を必ず残す。**
  supersede するときは、旧 ADR のどの部分が今も有効かを明記すること
  （ADR 0004 → 0007 が実例: 性能分析は有効、判断軸が変わっただけ）
- **コメントは「なぜ」を書く**。「何を」はコードが語る。
  特に上の地雷に関わる箇所は、次に読む人が緩めないよう理由を残す
- **エラー応答に API キーを混ぜない**。`config.Config` は `String()` を実装していないので、
  構造体ごと `%v` でログに出すと `VoyageAPIKey` が漏れる。個別フィールドを選んでログする
