# ADR 0022: 索引つきの候補生成検索を**計測モードとして**実装する（HNSW + GIN・両側 top-K の和集合を合成）。既定は測ってから

## Status

accepted (2026-09-02) — ADR 0007 手順 3 の実装。**既定の切り替えは本 ADR では行わない**（after の実測を見て別 ADR）

## Context

[ADR 0007](0007-pgvector-over-brute-force.md) の手順 2（10万件で測る）が
[`2026-09-02-eval-100k-before-index.md`](../benchmarks/2026-09-02-eval-100k-before-index.md) で終わった。

| 10万件・索引なし・既定構成 | 値 |
| --- | ---: |
| 埋め込みを除く p95 | **5,766 ms**（259 件では 9〜14 ms） |
| `recall@10` / `MRR` | 0.586 / 0.677（259 件では 0.724 / 0.807）——上位 10 件の 53% が無関係文書 |
| 実行計画 | `Seq Scan` 100,259 行・**単一プロセス**（窓関数 `MAX() OVER ()` が parallel-safe でない）・`work_mem` 4MB を溢れて一時ファイル 66MB 書き / 130MB 読み |

要件 §8 の予算（1万件で 200ms）に対し、10万件で 5.8 秒は桁が 1 つ以上足りない。原因は 3 つ重なっている:
(1) 全行に距離を計算する、(2) 全行に `ts_rank` を計算する、(3) 全行の語彙スコアを窓関数で正規化するため並列化もできず一時ファイルに落ちる。

現行の SQL は「全行に両スコアを付けて合成し ORDER BY」という形で、索引を張っても**この形のままでは索引が使われない**
（ORDER BY の式が `alpha*a + (1-alpha)*b` であり、どの索引の順序でもない）。索引を効かせるには**候補生成**の形に変える必要がある。

## Decision

### 1. 検索を 2 段にする「候補モード」を実装する（`RECALL_SEARCH_MODE=candidates`）

1. **ベクトル側 top-K**: `SELECT id, vector_score FROM chunks WHERE org_id = $1 [filters] ORDER BY embedding <#> $q LIMIT K`
   —— HNSW（`vector_ip_ops`）が効く形。
2. **語彙側 top-K**: `SELECT id, lexical_score FROM chunks WHERE org_id = $1 [filters] AND lexemes @@ $tsq ORDER BY ts_rank(...) DESC LIMIT K`
   —— GIN が `@@` を絞る。トークンが 0 個なら空集合。
3. **和集合**を候補集合とし、候補それぞれに**両スコア**を付け直す（欠けている側は候補行だけ計算。ベクトル距離は候補 ≤ 2K 行なので安い。
   語彙は候補行に `ts_rank` を計算する。`@@` に当たらなかった行は 0）。
4. 合成は ADR 0015 と同じ（語彙をクエリ内＝**候補集合内**の最大値で正規化・加重和・`alpha`）。ORDER BY・LIMIT。

`K` は `RECALL_CANDIDATE_K`（既定 **100**・両側同じ）。`Limit` より小さい K は拒否する。

### 2. 索引は migration で張る。**両方**

- `CREATE INDEX chunks_embedding_hnsw ON chunks USING hnsw (embedding vector_ip_ops)`（`m`・`ef_construction` は pgvector 既定 16 / 64。
  変えるなら after の実測で）
- `CREATE INDEX chunks_lexemes_gin ON chunks USING gin (lexemes)`
- `TestNoVectorIndexExists` は**削除ではなく反転**する: 「索引が存在し、`hnsw` であり、演算子クラスが `vector_ip_ops` である」を検査する
  （`<#>` と `vector_ip_ops` が食い違うと索引が黙って使われない）。ADR 0007 の「測ってから入れる」の**証拠は before 文書**であり、テストではない。

### 3. `exhaustive`（現行の全探索）は**計測モードとして残す**。既定はまだ `exhaustive`

- `RECALL_SEARCH_MODE=exhaustive|candidates`、既定 `exhaustive`。`make eval EVAL_MODE=`。レポート `conditions` に `search_mode` と `candidate_k`、
  HNSW の `ef_search`（`SET hnsw.ef_search`）を記録する（様式 v7）。
- 索引が張られていても `exhaustive` の SQL は使わない（ORDER BY が合成式）。⇒ **索引の効果と候補生成の効果は分離できない**ことを明記する。
  分離したいなら候補モードで索引を落として測る（`hnsw.ef_search` ではなく `enable_indexscan=off`）。after レーンの選択肢に残す。
- 既定の切り替え（`candidates` へ）は after の実測（`recall@10` の低下幅・p95）を見て別 ADR。

### 4. `hnsw.ef_search` は接続ごとの `SET LOCAL` で渡す（`RECALL_HNSW_EF_SEARCH`・既定 40 = pgvector 既定）

K ≤ ef_search でなければ HNSW は K 件返せない。`K > ef_search` は起動時に拒否する。

### 5. SQLite は本 ADR の対象外

比較用（ADR 0017）。索引の話は Postgres だけ。

## 却下した選択肢

| 選択肢 | 却下の理由 |
| --- | --- |
| **全探索の SQL のまま索引だけ張る** | ORDER BY が合成式で索引が使われない。「索引を張ったのに速くならない」という誤った before/after になる |
| **ベクトル側 top-K だけ取り、語彙は候補内で再計算（片側候補）** | 語彙でしか当たらない文書（`exact-term` の識別子）が候補に入らない。ADR 0014 の利得（`exact-term` +0.25）を捨てる |
| **RRF に切り替える** | ADR 0015 は「候補集合を絞る構成では RRF の評価が変わりうる」と留保した。after レーンで **RRF も測る**が、契約（`alpha`）は結果を見てから |
| **IVFFlat** | pgvector の HNSW が recall と速度の両面で一般に優位。IVFFlat は挿入後の再学習（`lists`）運用が要る。ADR 0007 が HNSW を名指し |
| **`work_mem` を上げて全探索を延命** | 一時ファイルは 3 原因の 1 つに過ぎず、距離と `ts_rank` の全行計算は残る。設定で隠すべき問題ではない |
| **最初から `candidates` を既定にする** | 測っていない。`recall@10` が HNSW の近似と K の切り捨てでどれだけ落ちるかを見てから（ADR 0007 の手順） |
| **GIN を後回しにする** | before の EXPLAIN で `ts_rank` の全行計算が距離計算より重い（同一計画形で 1.68 倍）。語彙側を絞らないと候補生成の意味が半分になる |

## Consequences

- **`alpha` の再測定が要る**（ADR 0015 Decision 3: 候補集合の作り方が変わった）。after レーンで K・`ef_search`・`alpha` を掃引する
- 259 件でも候補モードは動く（K=100 で 259 件中 100 件が候補）。**259 件の `recall@10` が `exhaustive` と一致するかが最初の検査**
  （一致しなければ候補生成が正解を落としている）
- 書き込みが遅くなる（HNSW の挿入コスト）。10万件の投入時間を before/after で記録する
- 「候補集合内の最大値で正規化」は全行の最大値と異なる。`alpha` の意味が変わるので、既定 0.8 を**そのまま持ち込まない**
- 追従: after の実測（Lane）→ 既定の切り替え判断（ADR 0023）→ CLAUDE.md 地雷 6「HNSW を最初から作らない」を「測って入れた」の記録に書き換える

## Related

- ADR [0007](0007-pgvector-over-brute-force.md)・[0014](0014-lexical-search-is-tsvector-over-bigram.md)・[0015](0015-fusion-is-weighted-sum-with-alpha-0.8.md)・[0019](0019-large-scale-benchmark-corpus.md)
- before: [`2026-09-02-eval-100k-before-index.md`](../benchmarks/2026-09-02-eval-100k-before-index.md)
- Supersedes: none（ADR 0007 の性能分析と手順は有効。本 ADR は手順 3 の実装） / Superseded by: none
