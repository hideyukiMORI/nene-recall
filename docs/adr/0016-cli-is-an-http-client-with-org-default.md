# ADR 0016: CLI は HTTP API の薄いクライアントとし、`org_id` の既定値は CLI だけが持つ

## Status

accepted (2026-09-02)

## Context

Phase 1 項目 9「CLI — 個人利用の入口」を作る。要件定義 §2.1 のとおり Recall は施主個人の必要から
始まっており、HTTP API を `curl` で叩くのは日常の入口として不便である。

一方で [ADR 0003](0003-org-id-is-mandatory.md) は **サーバ側のどこにも `org_id` の既定値を置かない**と定め、
[ADR 0010](0010-strictness-is-mechanically-enforced.md) の `CNF-001`/`CNF-002` と `TestOrgIDIsMandatory` が
それを機械的に守っている。個人利用では毎回 `org_id` を打ちたくない。**この2つの要求は、
既定値をどこに置くかで折り合う**——CLAUDE.md はすでに「既定値は CLI 側に置く。サーバ側には置かない」と
方針を書いている。本 ADR はそれを設計として確定し、CLI の形を決める。

## Decision

### 1. CLI は別バイナリ `recallctl`（`cmd/recallctl`）で、**HTTP API だけを叩く**

ストア（PostgreSQL）や埋め込み（Ollama）に CLI から直接触れない。CLI が知っているのはサーバの URL だけである。

理由: `org_id` の必須化・入力検証・埋め込みモデルの整合チェック（ADR 0005）は HTTP 層に1箇所で
実装されている。CLI が DB を直接読むと、その検査を**二重に持つか、素通しするか**のどちらかになる。
素通しは ADR 0003 の抜け穴になり、二重化は片方だけ直される。

### 2. `org_id` の解決順は `--org` フラグ → 環境変数 `RECALL_ORG_ID` → **CLI の定数 `1`**

既定値 `1` は `cmd/recallctl` の中に**定数として1箇所**だけ置く。サーバ側には引き続き置かない。
値は `org.ParseID` を通す（`org.ID(1)` の直接変換は `CNF-001` が禁じている）。
検索結果の表示には、どの `org_id` で問い合わせたかを**必ず出す**（stderr の1行）。既定値が
黙って効いていると、別 org のデータを見ているつもりで自分のデータを見ている、という取り違えが起きる。

### 3. 入出力は JSONL と表。生の応答は `--json` で出す

- `put` は **ChunkInput（OpenAPI）を1行1件の JSONL** で標準入力またはファイルから読む
- `search` は既定で人が読む表（順位・`score`・`vector_score`・`lexical_score`・`document_id`・`source_id`・本文の先頭）、
  `--json` でサーバ応答をそのまま出す
- サーバの `Error` 応答は `code` と `message` を stderr に出し、終了コードで区別する
  （1 = 使い方の誤り、2 = サーバが 4xx/5xx、3 = 接続失敗）

### 4. チャンク分割器は CLI に**まだ**持たない

`put` は分割済みのチャンクを受け取る。文書を Recall 側で分割する機能は別の設計判断である——
[ADR 0014](0014-lexical-search-is-tsvector-over-bigram.md) の実測で**末尾の句点1文字がコサイン類似度を
0.016 動かす**ことが分かっており、分割規則は検索品質に効く。当て推量で入れない。

## 却下した選択肢

| 選択肢 | 却下の理由 |
| --- | --- |
| **CLI がストアと Embedder を直接使う** | 検査の二重化か素通しになる（Decision 1）。加えて CLI に DB 接続情報と Ollama の設定が要り、「サーバの URL だけ知っていればよい」単純さを失う |
| **`recall` バイナリにサブコマンド（`recall serve` / `recall search`）を足す** | サーバとクライアントの依存集合が違う。サーバは `pgx` と Ollama クライアントを持ち、CLI は `net/http` だけでよい。1つにすると CLI が DB ドライバを抱えて配布サイズと攻撃面が増える。`make run` の形も変わる |
| **`org_id` を設定ファイル（`~/.config/recall/...`）で持つ** | 個人利用の第一歩には過剰。フラグと環境変数で足りる。必要になったら**CLI 側に**足す（サーバ側には足さない） |
| **既定値を持たず、`--org` を必須にする** | ADR 0003 の精神には最も忠実だが、個人利用の入口としては毎回打つことになり CLI の存在理由が薄れる。既定値を**CLI の1箇所に閉じ、表示で可視化する**ことで折り合う |
| **`cobra` 等の CLI フレームワーク** | ARC-004 により依存の追加は ADR を要する。サブコマンド5つに `flag` 標準パッケージで足りる。依存ゼロを保つ |

## Consequences

- `make build` が `./cmd/recallctl` も作る（CLI は成果物である。`cmd/eval` を含めない方針とは矛盾しない）
- 認証は無い（要件定義 Q-6 は未決）。Q-6 が決まったら CLI は**トークンをヘッダで送るだけ**で追随できる
- 追従作業: 分割器（Decision 4）は別 ADR。Corpus 由来の `chunk_id` 指定は Phase 2 の ADR 待ちで、
  CLI は Phase 1 の契約（`chunk_id` を送らない）に従う

## Related

- CLAUDE.md「Phase 1 の残作業」項目 9
- 関連 ADR: [0003](0003-org-id-is-mandatory.md)・[0005](0005-embedding-provider-is-pluggable.md)・
  [0010](0010-strictness-is-mechanically-enforced.md)・[0014](0014-lexical-search-is-tsvector-over-bigram.md)
- OpenAPI: `docs/openapi/openapi.yaml`
- Supersedes: none
- Superseded by: none
