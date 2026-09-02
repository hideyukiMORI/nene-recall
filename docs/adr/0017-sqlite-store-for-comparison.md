# ADR 0017: 比較用の SQLite ストアは純 Go の `modernc.org/sqlite` で作り、ベクトルは Go 側の総当たり・語彙は FTS5 の `bm25()` で採点する

## Status

accepted (2026-09-02)

## Context

Phase 1 項目 8「SQLite ストア（比較用）」。[ADR 0007](0007-pgvector-over-brute-force.md) は pgvector を
既定に選んだが、その判断の価値は「測ってから索引を入れた経路」にあり、比較対象として
**同一データで Go 側総当たりの SQLite を並べる**ことを成果物に数えている。`config.Store` には
`sqlite` が最初から valid な値として存在し、起動時に sentinel エラーで落ちる状態で待っていた。

[ADR 0014](0014-lexical-search-is-tsvector-over-bigram.md) は Go 側 BM25 を**測らずに**構造上の理由で
却下し、「BM25 の数字は項目 8 で必然的に出る」と書いた。本 ADR はその約束を果たす設計でもある。

### 制約

- **cgo を持ち込まない**（CLAUDE.md 地雷 5・ARC-003）。`mattn/go-sqlite3` と `sqlite-vec` は不可
- 外部依存は許可制（ARC-004）。`modernc.org/sqlite` の追加は本 ADR が根拠になる。
  `gomodguard` の許可リスト（`.golangci.yml`）の更新は「ゲートの定義」の変更で、これも本 ADR が根拠
- `org_id` の分離条件は SQL の WHERE に入れる（ADR 0003）。Go 側でフィルタしない
- `Put` は入力と同じ順で id を返し、id は再利用されない（ADR 0013。評価ハーネスが依存）
- `index.Searcher` / `index.Writer` の契約と、計測の口 `SearchVector` を Postgres と同じ形で持つ。
  `assertSameEmbedderAndTokenizer` も同じ位置で行う（省くと 2 系統の p95 の差の意味が変わる。CLAUDE.md 地雷 10）

## Decision

### 1. ドライバは `modernc.org/sqlite`（純 Go・`database/sql`）

cgo なし・クロスコンパイル可。`database/sql` 経由なので Postgres ストアとコードの形が揃う。

### 2. ベクトルは `BLOB`（float32 × 1024・リトルエンディアン）に置き、**Go 側の総当たり**で内積を取る

SQL は `WHERE org_id = ?`（＋任意の `document_id` / `source_id` 絞り込み）で候補行を返すだけで、
距離計算は Go が行う。入力が正規化されている前提は Postgres と同じ3点（`Embedder` の契約・
実行時検査・違反時の即エラー）で支える。BLOB の長さが 4096 バイトでない行は読み取り時にエラーにする。

### 3. 語彙は **FTS5**（外部コンテンツ表・`tokenize='ascii'`）に `lexeme_text` を入れ、`bm25()` で採点する

Go 側 `lexical.Tokenizer`（bigram）が作った空白区切りのトークン列を、そのまま FTS5 に入れる。
`ascii` トークナイザを選ぶのは**非 ASCII を一切分割しない**ためで、CJK の bigram トークンが
FTS5 側で再分割される経路を塞ぐ（Postgres 側の `to_tsvector('simple')` による再パースと同じ罠を避ける）。
検索式は Postgres の `encodeTsQuery` と同じ意味（トークンの論理結合）を FTS5 の MATCH 構文で組む。

`bm25()` は小さいほど良い（負値）ので符号を反転し、**Postgres と同じくクエリ内の最大値で [0,1] に正規化**
してから `alpha*vector + (1-alpha)*lexical` に入れる（ADR 0015 Decision 1 と同じ合成）。

### 4. 合成は加重和のみ。RRF は実装しない

RRF は Postgres 側で計測用に残してある実装で、既定ではない（ADR 0015）。比較は**既定同士**で行う。

### 5. id は `INTEGER PRIMARY KEY AUTOINCREMENT`

`AUTOINCREMENT` を付けるのは rowid の再利用を防ぐためである。無いと削除後の挿入が過去の id を
再び割り当て、評価ハーネスの `eval_key → id` 写像（ADR 0013）が静かに別行を指す。

### 6. 評価レポートに `store` と `lexical_scorer` を記録する

`eval.RankingSettings` に `store`（`postgres` / `sqlite`）と `lexical_scorer`（`ts_rank` / `fts5-bm25`）を足し、
様式の版を上げる。2つのストアのレポートが**条件表で見分けられない**状態を作らない。

## 却下した選択肢

| 選択肢 | 却下の理由 |
| --- | --- |
| **`sqlite-vec` / `mattn/go-sqlite3`** | cgo。CLAUDE.md 地雷 5 |
| **SQL 側にスカラ関数（`dot(blob, blob)`）を登録して `ORDER BY` を SQL で行う** | `modernc.org/sqlite` の関数登録はプロセス全域の状態で、`init()` か可変グローバルが要る（GO-007 が禁止）。Go 側総当たりで同じ結果が得られ、**「Go 側の総当たり」こそが ADR 0007 の比較対象**である |
| **語彙も Go 側で採点する（自前 tf-idf / BM25）** | 自前実装の癖が「ストアの差」として記録に混ざる。FTS5 の `bm25()` は SQLite 同梱の枯れた実装で、ADR 0014 が約束した「BM25 の数字」に正面から答える |
| **FTS5 の `unicode61` トークナイザ** | Unicode 分類で分割するので、bigram トークンに記号が含まれると再分割される。`ascii` は空白と ASCII 記号以外を分割しない |
| **Postgres と1つの `Fusion` 型を共有する** | `Fusion.statement()` が SQL を抱えており、共有には契約パッケージへの切り出しが要る。比較は加重和だけで足りるので、今は切り出さない |
| **`RECALL_STORE=sqlite` を既定にする** | 比較用である。既定は測ってから（ADR 0007） |

## Consequences

- `make check` に外部依存のないストアのテストが増える（SQLite は一時ファイルで走る）。
  Postgres 側のテストが Skip されても SQLite 側は必ず走るので、CI のカバレッジの底が上がる
- `make eval` に `EVAL_STORE=sqlite` が増え、**同一評価セット**で `recall@10`・`MRR`・p95（2系統）を
  ストア間で比較できる。この比較の実測は別のレーンで行い、`docs/benchmarks/` に残す
- `bm25()` と `ts_rank` は違う関数なので、`recall@10` の差は「ストアの差」ではなく「語彙採点関数の差」を
  含む。比較文書はこれを**分けて**読む（純ベクトル `alpha=1.0` の比較でストアの差を、語彙のみ `alpha=0.0` で
  採点関数の差を見る）
- 追従: `alpha` の最適値は正規化方式に条件付き（ADR 0015 Decision 3）。`bm25()` に対する `alpha` の掃引は
  比較レーンで行い、Postgres の 0.8 を SQLite に**そのまま当てはめない**

## 追記（2026-09-02・比較実測の結果）

正本は [`docs/benchmarks/2026-09-02-eval-store-comparison.md`](../benchmarks/2026-09-02-eval-store-comparison.md)。
同一評価セット・同一セッション・rounds=5。

| 層 | 見たもの | 結果 |
| --- | --- | --- |
| 1 純ベクトル `alpha=1.0` | ストアの差 | 品質は**完全に一致**（58クエリ全件で上位10件の並びが同一・`vector_score` の差は float32 の丸め幅）。latency は **SQLite が 10〜15 倍遅い**（埋め込みを除く p50 9.3 → 93.7ms・p95 13.5 → 207.7ms） |
| 2 語彙のみ `alpha=0.0` | `ts_rank` vs `bm25()` | `recall@10` 0.620 vs 0.649・`MRR` 0.736 vs 0.718。逆を向き、どちらもゆらぎの帯の中。判断は ADR 0014 の追記 |
| 3 ハイブリッド | SQLite の `alpha` プラトー | **0.8〜0.9**（Postgres は 0.7〜0.9）。両方に 0.8 が含まれるので、**既定 0.8 はストア共通のまま**にする。SQLite 向けに別の既定は持たない |

**帰結:**

- **SQLite は比較用のまま**（Decision の「既定にしない」を実測が裏付けた）。259 チャンクの時点で埋め込みを除く p95 が
  約 200ms で、要件 §8 の予算（1万チャンクで 200ms）を**規模が 40 分の 1 の段階で使い切っている**。Go 側総当たりの
  BLOB 読み出し＋復号が支配的で、規模に線形に伸びる。
- **`alpha` は SQLite でも latency に効かない**（Postgres と同型。`searchRows` が `alpha` に関わらず語彙を計算する）。
  「純ベクトル `alpha=1.0` が本当に純ベクトルか」を検証可能にするための設計であり、速くする余地は別の判断。
- ADR 0007 の「同一データで総当たりと pgvector を並べる」成果物は、これで揃った。ただし **pgvector 側も索引なし**なので、
  ここで見えた 10〜15 倍は「pgvector の索引の効果」ではなく「C の全探索と Go の全探索の差」である。索引の効果は項目 7 で見る。

## Related

- CLAUDE.md「Phase 1 の残作業」項目 8・地雷 5・地雷 10
- 関連 ADR: [0003](0003-org-id-is-mandatory.md)・[0007](0007-pgvector-over-brute-force.md)・
  [0013](0013-evaluation-harness-design.md)・[0014](0014-lexical-search-is-tsvector-over-bigram.md)・
  [0015](0015-fusion-is-weighted-sum-with-alpha-0.8.md)
- Supersedes: none
- Superseded by: none
