# 評価 2026-09-02 — 10万件規模・索引つき候補モードの実測（HNSW / GIN の after）

[ADR 0007](../adr/0007-pgvector-over-brute-force.md) の手順 3（「測ってから HNSW を入れる。
before/after の数値を並べて記録する」）の実行結果である。[ADR 0022](../adr/0022-indexed-candidate-search.md)
が実装した候補モード（`RECALL_SEARCH_MODE=candidates`・HNSW + GIN）を、
before と**同じ評価セット・同じ紛れ込み**（10万件）で測った。

before は [`2026-09-02-eval-100k-before-index.md`](2026-09-02-eval-100k-before-index.md)。

🔴 **本書も結論を書かない。** 既定を `candidates` へ移すかどうかは ADR 0023 の仕事であり、
その ADR は本書の数字を読んでから書かれる。**本書は測っただけである。**

生データ（すべて `docs/benchmarks/data/` にコミットしてある）:

| ラベル | 構成 | ファイル |
| --- | --- | --- |
| `259-cand-k100-ef100-08` | **紛れ込み無し**・候補 K=100 / ef=100 | [`data/2026-09-02-eval-259-cand-k100-ef100-08.json`](data/2026-09-02-eval-259-cand-k100-ef100-08.json) |
| `259-exh-08-s4` | **紛れ込み無し**・全探索（整合の相手） | [`data/2026-09-02-eval-259-exh-08-s4.json`](data/2026-09-02-eval-259-exh-08-s4.json) |
| `100k-cand-k100-ef100-08` | 10万件・K=100 / ef=100 / `alpha=0.8`（**既定候補の after**） | [`data/2026-09-02-eval-100k-cand-k100-ef100-08.json`](data/2026-09-02-eval-100k-cand-k100-ef100-08.json) |
| `100k-cand-k50-ef100-08` | K=50 / ef=100 | [`data/2026-09-02-eval-100k-cand-k50-ef100-08.json`](data/2026-09-02-eval-100k-cand-k50-ef100-08.json) |
| `100k-cand-k200-ef200-08` | K=200 / ef=200 | [`data/2026-09-02-eval-100k-cand-k200-ef200-08.json`](data/2026-09-02-eval-100k-cand-k200-ef200-08.json) |
| `100k-cand-k100-ef400-08` | K=100 / ef=400 | [`data/2026-09-02-eval-100k-cand-k100-ef400-08.json`](data/2026-09-02-eval-100k-cand-k100-ef400-08.json) |
| `100k-cand-k100-ef100-{05,07,09,10,00}` | `alpha` の再掃引 | `data/2026-09-02-eval-100k-cand-k100-ef100-{05,07,09,10,00}.json` |
| `100k-cand-k100-ef100-rrf` | 順位融合（`alpha` は無視される） | [`data/2026-09-02-eval-100k-cand-k100-ef100-rrf.json`](data/2026-09-02-eval-100k-cand-k100-ef100-rrf.json) |

🔴 **測れなかった条件が 3 つある**（`k40-ef40`・`k40-ef100`・`noidx`）。理由と原因は §9 に書いた。
指示書の `100k-cand-k100-ef40-08` は `K > ef_search` が構築時に拒否されるため成立せず、
`100k-cand-k40-ef40-08`（K=40 / ef=40）に置き換えて測ろうとした。

---

## 1. 条件

| 項目 | 値 |
| --- | --- |
| git revision | `33893eb`（**10本とも変更なし**・`git_modified: false`） |
| レポートの様式 | `nene-recall/eval-report/v7`（候補の作り方を条件に記録する版） |
| 評価セット | 259チャンク / 58クエリ / 正解 延べ236件。**before と同一**（`corpus` sha256 `955607918e38…` / `queries` `f9e8e766…` / `tags` `389ad28c…`） |
| 紛れ込み | `distractors-100k.jsonl`・**100,000 件**・sha256 `514739b7f396b204182a1b166808be596849432758c0ed2d2107197b85a53556`（**before と同一ファイル**） |
| 投入後の行数 | **100,259**（DB で実測） |
| 埋め込み | `bge-m3:1024` / Ollama 0.33.2 / digest `790764642607…` |
| 埋め込みキャッシュ | 使用。**成功した10本すべてで hits 100,259 / misses 0**。クエリ側はキャッシュしない |
| ストア | PostgreSQL 17.11 + pgvector 0.8.6・**索引あり**（後述 §1.1） |
| 語彙 | Go 側 bigram（`bigram:nfkc-lower:v1`）→ `tsvector('simple')`・`ts_rank` 正規化 **0** |
| 検索 | `search_mode: candidates`・`limit=10` / `k ∈ {1,5,10}` / **5ラウンド＋ウォームアップ1周** / 1系統あたり **290サンプル** |
| パーセンタイル | `nearest-rank: sorted[ceil(p/100*n)-1]`・1-indexed・補間なし |
| GPU | RTX 3090・**他アプリと共有**（測定開始時 4.9GB / 24GB・GPU-Util 26%・Ollama の `llama-server` 常駐） |
| 評価用 DB | `recall_eval_lane18`（`-eval-db`。共有の `recall_eval` を壊さないため分けた） |

⚠️ **計測は日をまたいでいる。** セッションは 2026-09-02 23:30 に始まり 2026-09-03 03:48 に終わった（最後の補助測定を含む）。
文書名とレポートのファイル名は before と揃えて `2026-09-02` にしてあるが、**各 JSON の
`measured_at` が正である**（例 `100k-cand-k100-ef100-rrf` は `2026-09-03T02:51:38+09:00`）。

🔴 **正解セット（`testdata/eval/`）は1バイトも変えていない。** 3ファイルの sha256 は
before と一致する。紛れ込みも同じファイル（sha256 一致）である。

⚠️ レポートの `conditions.distractors.path` は before（`bin/wikidistract/distractors-100k.jsonl`）と
違う絶対パスになっている。**複数のレーンで同じファイルを共有するためにリポジトリの外へ置いたためで、
中身は同一である**——`sha256` と `count` が一致していることがその証拠である。

### 1.1 索引が在ることの確認

```
recall_eval_lane18=# \di chunks*
 public | chunks_embedder_idx        | index | recall | chunks
 public | chunks_embedding_hnsw      | index | recall | chunks
 public | chunks_lexemes_gin         | index | recall | chunks
 public | chunks_org_document_idx    | index | recall | chunks
 public | chunks_org_external_id_key | index | recall | chunks
 public | chunks_org_source_idx      | index | recall | chunks
 public | chunks_pkey                | index | recall | chunks
 public | chunks_tokenizer_idx       | index | recall | chunks
```

before の 6本（すべて btree）に 2本増えている。アクセスメソッドと演算子クラスを実測した。

| 索引 | `amname` | `opcname` |
| --- | --- | --- |
| `chunks_embedding_hnsw` | **`hnsw`** | **`vector_ip_ops`** |
| `chunks_lexemes_gin` | **`gin`** | `tsvector_ops` |

🔑 演算子クラスが `vector_ip_ops` であることは、検索の `<#>`（負の内積）と対になっている
ことの確認である。食い違うと索引は作られるのに**黙って使われない**
（`migrations/0004_add_search_indexes.sql` の赤字）。§7 の `EXPLAIN` が実際に
`Index Scan using chunks_embedding_hnsw` を選んでいることも別途確かめた。

### 1.2 データの大きさ — before との差は 857 MB

| 項目 | before（索引なし） | **after（索引あり）** |
| --- | ---: | ---: |
| `pg_database_size` | 823 MB | **1,680 MB**（1,761,203,891 バイト） |
| `pg_relation_size('chunks')`（ヒープ） | 145 MB | 145 MB |
| TOAST | 655 MB | 654 MB |
| **索引の合計** | **6.2 MB**（btree 6本） | **863 MB** |
| `pg_total_relation_size('chunks')` | 815 MB | 1,672 MB |

索引の内訳（after）:

| 索引 | 大きさ |
| --- | ---: |
| `chunks_embedding_hnsw` | **783 MB**（821,043,200 バイト） |
| `chunks_lexemes_gin` | 73 MB（76,922,880 バイト） |
| btree 6本の合計 | 6.2 MB |

🔑 **HNSW 索引 783 MB は `shared_buffers`（128 MB）の 6.1 倍である。**
ヒープ 145 MB と TOAST 654 MB を足すと、この DB は共有バッファの 13 倍になる。
§7 の `EXPLAIN` に現れる `Buffers: shared read=…` の大きさはこの条件のもとの値である。

### 1.3 PostgreSQL の設定（before と同じ・既定のまま）

| 項目 | 値 |
| --- | ---: |
| `shared_buffers` | 128 MB |
| `work_mem` | 4 MB |
| `maintenance_work_mem` | 64 MB |
| `max_parallel_workers_per_gather` | 2 |
| `effective_cache_size` | 4 GB |
| `random_page_cost` | 4 |
| `jit` | on |

**チューニングしていない。** before と同じ設定である。

---

## 2. 259 件の整合 — 候補モードは全探索と1件も違わなかった

ADR 0022 Consequences が「**259 件の `recall@10` が `exhaustive` と一致するかが最初の検査**」
と書いている。指示書はさらに強く「**上位 10 件の並びが全クエリで一致するか**」を求めた。

| | `259-cand-k100-ef100-08` | `259-exh-08-s4` |
| --- | ---: | ---: |
| `search_mode` | `candidates`（K=100 / ef=100） | `exhaustive` |
| `recall@1` | 0.2782430213464696 | 0.2782430213464696 |
| `recall@5` | 0.6100985221674877 | 0.6100985221674877 |
| **`recall@10`** | **0.724076354679803** | **0.724076354679803** |
| **`MRR`** | **0.8071839080459772** | **0.8071839080459772** |
| micro | 157/236 = 0.6653 | 157/236 = 0.6653 |
| gold ≤520字 / >520字 | 128/180 / 29/56 | 128/180 / 29/56 |

🔑 **上位10件の並びは 58 クエリすべてで完全に一致した**（`eval_key` の列を要素順で比較。
不一致 0 件）。指標も最後の桁まで同じで、既存の 259 件の正本
（[`2026-09-02-eval-hybrid-latency.md`](2026-09-02-eval-hybrid-latency.md)）とも一致する。

⇒ **259 件（K=100 が母集団の 39%）の範囲では、候補生成も HNSW の近似も順位を1つも動かしていない。**
⚠️ これは「10万件でも動かない」という意味ではない。10万件での比較は §3 にある。

latency（参考・259 件）:

| 構成 | 系統 | min | p50 | p95 | max |
| --- | --- | ---: | ---: | ---: | ---: |
| `candidates` K=100 / ef=100 | 含む | 45.79 | 60.08 | 81.06 | 112.22 |
| `candidates` K=100 / ef=100 | 除く | 3.66 | **9.78** | **16.76** | 27.22 |
| `exhaustive` | 含む | 46.08 | 60.39 | 75.46 | 94.06 |
| `exhaustive` | 除く | 4.11 | **9.76** | **14.82** | 24.08 |

⚠️ **259 件では候補モードのほうが速くない。** 索引を引く手間が全探索の節約を上回る規模である。
差（p95 で 1.9ms）は
[`2026-09-02-eval-morph-vs-bigram.md`](2026-09-02-eval-morph-vs-bigram.md) §6 が記録する
モードの割れ（259 件で 9.00ms と 14.45ms に分かれる）と同じ大きさなので、**本測定はこの差を差と呼ばない。**

---

## 3. 10万件 — before（全探索）と after（候補モード）の品質

🔴 **before は取り直していない。** 5.8 秒 × 6周 × 58 クエリ × 2系統 は 40 分近くかかるうえ、
before は既に `dde0b12` で測って文書になっている。ここでは
[`2026-09-02-eval-100k-before-index.md`](2026-09-02-eval-100k-before-index.md)（revision `dde0b12`・
2026-09-02 測定）の値を**引用**する。⇒ **before と after は別セッションである。**

⚠️ 2つのセッションの間に候補モードの実装（`33893eb`）が入っているが、
**`exhaustive` の SQL・語彙スコア・分割器・埋め込みは変わっていない**（レポートの
`conditions.ranking` で確認できる。before は `bigram:nfkc-lower:v1`・`ts_rank_normalization: 0`、
after も同じ）。before の 259 件統制が正本と完全一致していた（before §2.1）ことも、
セッション間で順位付けが動いていないことの傍証である。

### 3.1 総合

| ラベル | mode | K | ef | `alpha` | `recall@1` | `recall@5` | **`recall@10`** | **`MRR`** | micro |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| **before** `100k-hybrid-08-r5` | `exhaustive` | – | – | 0.8 | 0.2335 | 0.4950 | **0.5863** | **0.6771** | 117/236 |
| **after** `100k-cand-k100-ef100-08` | `candidates` | 100 | 100 | 0.8 | 0.2335 | 0.4993 | **0.5863** | **0.6771** | 117/236 |
| `100k-cand-k50-ef100-08` | `candidates` | 50 | 100 | 0.8 | 0.2335 | 0.4950 | 0.5888 | 0.6785 | 118/236 |
| `100k-cand-k200-ef200-08` | `candidates` | 200 | 200 | 0.8 | 0.2335 | 0.4950 | 0.5863 | 0.6771 | 117/236 |
| `100k-cand-k100-ef400-08` | `candidates` | 100 | 400 | 0.8 | 0.2335 | 0.4993 | 0.5863 | 0.6771 | 117/236 |
| before `alpha=1.0` | `exhaustive` | – | – | 1.0 | 0.1473 | 0.3323 | 0.4057 | 0.5157 | 85/236 |
| after `alpha=1.0` | `candidates` | 100 | 100 | 1.0 | 0.1473 | 0.3323 | 0.4057 | 0.5157 | 85/236 |
| before `alpha=0.0` | `exhaustive` | – | – | 0.0 | 0.1732 | 0.3996 | 0.4355 | 0.4867 | 79/236 |
| after `alpha=0.0` | `candidates` | 100 | 100 | 0.0 | 0.1732 | 0.3996 | 0.4355 | 0.4867 | 79/236 |

🔑 **`alpha` 0.0・0.8・1.0 のどれでも、候補モードは全探索と同じ `recall@10` と `MRR` を返した。**
`alpha=0.0` と `1.0` は `recall@1` / `recall@5` / micro まで完全に一致し、`alpha=0.8` だけ
`recall@5` が 0.4950 → 0.4993 と動いている（差の中身は §3.3）。

### 3.2 タグ別 `recall@10`

| tag | n | 259件（候補） | before 10万件 | after K=100/ef=100 | K=50 | K=200 | ef=400 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `compound` | 7 | 0.929 | 0.762 | 0.762 | 0.762 | 0.762 | 0.762 |
| `exact-term` | 8 | 0.982 | 0.857 | 0.857 | 0.857 | 0.857 | 0.857 |
| `negation` | 7 | 0.760 | 0.760 | 0.760 | 0.760 | 0.760 | 0.760 |
| `numeric` | 8 | 0.737 | 0.604 | 0.604 | 0.604 | 0.604 | 0.604 |
| `orthography` | 7 | 0.588 | 0.395 | 0.395 | **0.430** | 0.395 | 0.395 |
| `paraphrase` | 7 | 0.626 | 0.366 | 0.366 | 0.366 | 0.366 | 0.366 |
| `particle` | 8 | 0.625 | 0.625 | 0.625 | **0.594** | 0.625 | 0.625 |
| `synonym` | 6 | 0.489 | 0.224 | 0.224 | **0.248** | 0.224 | 0.224 |

**K=100 以上では、8タグすべてが before と小数第3位まで一致している。**
動いているのは K=50 の3タグだけで、いずれも 1クエリぶん（タグ n=6〜8 なので 0.02〜0.04）である。

gold の長さ別（micro）:

| 区分 | before 10万件 | after K=100/ef=100 | K=50 |
| --- | ---: | ---: | ---: |
| gold ≤520字 | 94/180 | 94/180 | 95/180 |
| gold >520字 | 23/56 | 23/56 | 23/56 |
| 名指しの長文3件 | 2/15 | 2/15 | 2/15 |

### 3.3 **HNSW の近似で落ちた正解はあったか** — per-query の差分

before（`exhaustive`）と各 after を、クエリごとに `recall@10` と上位10件の並びで突き合わせた。

| after の条件 | 上位10件の**並び**が違うクエリ | `recall@10` が違うクエリ | 正解が圏外に出たクエリ |
| --- | ---: | ---: | ---: |
| K=200 / ef=200 | **1 / 58** | 0 | **0** |
| K=100 / ef=100 | **6 / 58** | 0 | **0** |
| K=100 / ef=400 | 6 / 58 | 0 | **0** |
| K=50 / ef=100 | 15 / 58 | 3 | **1**（`q-029`） |

**K=100 / ef=100 の 6 件は、いずれも正解の出入りではない。** `recall@10` は 6 件とも同値で、
唯一動いたのは `q-054`「一度付けた正解が入れ直したとき別の場所を指してしまうのを防ぐには」の
`recall@5` である。

| `query_id` | tag | 正解の順位（before） | 正解の順位（after K=100/ef=100） |
| --- | --- | --- | --- |
| `q-054` | `paraphrase` | `adr-0013#005` 1位 / `claude-md#025` 4位 / **`adr-0013#003` 6位** / `adr-0013#004` 圏外 | 1位 / 4位 / **5位** / 圏外 |

⇒ 正解1件が 6位から 5位へ1つ上がり、`recall@5` が 0.500 → 0.750（そのクエリで）動いた。
総合では +0.0043 である。

**K=50 だけが正解を落とした。**

| `query_id` | tag | 差 | 落ちた正解 |
| --- | --- | ---: | --- |
| `q-029` | `particle` | `recall@10` 0.500 → **0.250** | `requirements#012` |
| `q-037` | `orthography` | 0.500 → 0.750 | （拾った） |
| `q-048` | `synonym` | 0.143 → 0.286 | （拾った） |

⇒ **候補の切り捨てが効き始めるのは K=50 である。** K=100 以上では 1件も落ちていない。
⚠️ K=50 は落とした 1 件より拾った 2 件のほうが多く、総合 `recall@10` は逆に +0.0025 になっている。
**総合値だけを見ると「K を下げたら良くなった」と読めてしまうが、中身は 3クエリの入れ替わりであり、
`testdata/eval/README.md` の「±2〜4ポイントの差は判断材料にしない」の範囲内である。**

---

## 4. `alpha` の再掃引（候補モード）

ADR 0022 Consequences は「候補集合内の最大値で正規化」が `alpha` の意味を変えるので
「既定 0.8 を**そのまま持ち込まない**」と書いた。候補モードで測り直した。

| `alpha` | 0.0 | 0.5 | 0.7 | **0.8** | 0.9 | 1.0 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `recall@1` | 0.1732 | 0.1923 | 0.2210 | **0.2335** | 0.1880 | 0.1473 |
| `recall@5` | 0.3996 | 0.4569 | 0.4743 | **0.4993** | 0.4697 | 0.3323 |
| **`recall@10`** | 0.4355 | 0.5089 | 0.5569 | **0.5863** | 0.5580 | 0.4057 |
| **`MRR`** | 0.4867 | 0.5696 | 0.6412 | **0.6771** | 0.6124 | 0.5157 |
| micro | 79/236 | 100/236 | 112/236 | **117/236** | 110/236 | 85/236 |

（K=100 / ef=100・10万件。0.3 と 0.95 は測っていない。）

観察できること:

- **最大は 0.8 で、最大から −0.02 以内に入るのは 0.8 だけである**（0.7 は −0.029、0.9 は −0.028）。
  ADR 0015 が `exhaustive`・259 件で見た「0.7〜0.9 のプラトー」に対し、**10万件の候補モードでは
  同じ 3 点の差が広がっている**（259 件では 0.708 / 0.724 / 0.721 で幅 0.016、
  ここでは 0.5569 / 0.5863 / 0.5580 で幅 0.029）
- ⚠️ **幅 0.029 は 1.7 クエリぶんである。** `testdata/eval/README.md` は「±2〜4ポイントは
  判断材料にしない」と定めており、この差はその境界にある。**本測定は「プラトーが狭くなった」と
  主張しない**——言えるのは「3点の値がこうだった」だけである
- **両端（0.0 と 1.0）は before の `exhaustive` と完全に同値**（§3.1）。⇒ `alpha` の掃引で
  動いているのは合成だけで、候補集合の作り方は両端の値を変えていない

## 5. RRF との比較

ADR 0015 は RRF を却下したとき「**候補集合を絞る構成では再評価の余地がある**」と留保した。
その再評価である。

| 方式 | `recall@1` | `recall@5` | **`recall@10`** | **`MRR`** | micro |
| --- | ---: | ---: | ---: | ---: | ---: |
| 加重和 `alpha=0.8` | 0.2335 | 0.4993 | **0.5863** | **0.6771** | 117/236 |
| **RRF（`k=60`）** | 0.1908 | 0.4144 | **0.5260** | **0.5818** | 106/236 |

どちらも `candidates` K=100 / ef=100・10万件・同一セッション。
⚠️ RRF のレポートにも `conditions.alpha: 0.7` が刻まれているが、**順位融合では `alpha` は
使われない**（`data/README.md`）。

per-query では **58 クエリ全部で上位10件の並びが変わり**、`recall@10` が動いたのは 15 件
（下がった 10 件・上がった 5 件）。

| 下がった（大きい順） | tag | 差 | | 上がった | tag | 差 |
| --- | --- | ---: | --- | --- | --- | ---: |
| `q-001` `pgvector 0.8.6` | `exact-term` | **−1.000** | | `q-051` 新しく入った人が規約を… | `paraphrase` | +0.333 |
| `q-003` `RECALL_STORE` | `exact-term` | **−1.000** | | `q-048` `0.7` という数字に裏付けは… | `synonym` | +0.286 |
| `q-031` 配線点 | `compound` | −0.833 | | `q-047` 速さと当たり具合の… | `synonym` | +0.250 |
| `q-029` 本文の正はどちら側に… | `particle` | −0.500 | | `q-037` 状態を持たない推論サーバー… | `orthography` | +0.250 |
| `q-026` HNSW を入れると recall は… | `particle` | −0.333 | | `q-043` 埋込みモデルを変えたら… | `orthography` | +0.167 |
| `q-049` 本文を2箇所に持つと… | `synonym` | −0.333 | | | | |

タグ別 `recall@10` では `exact-term` 0.857 → **0.607**、`compound` 0.762 → 0.643 と落ち、
`orthography` 0.395 → 0.454、`paraphrase` 0.366 → 0.378 と上がっている。

🔑 **落ちた側にカテゴリの偏りがある。** `q-001`・`q-003` はどちらも識別子そのものを引く
`exact-term` で、加重和では 1位だった正解が RRF では圏外に出た。
⚠️ ただし `exact-term` は n=8 で、2クエリの入れ替わりで 0.25 動く大きさである。

---

## 6. latency — p50 / p95 を 2系統 × 全構成

🔴 **2系統は別々に実測した値である。引き算で出していない**（`CLAUDE.md` 地雷10）。
系統2 は `Store.SearchVector` で直接測っている。単位は ms・各セル 290サンプル。

| 構成 | 系統 | min | **p50** | **p95** | max |
| --- | --- | ---: | ---: | ---: | ---: |
| **before**（全探索・別セッション `dde0b12`） | 含む | 1800.99 | **3905.03** | **5758.34** | 6694.80 |
| **before**（全探索・別セッション `dde0b12`） | 除く | 1736.84 | **3829.66** | **5765.83** | 6535.88 |
| **after** K=100 / ef=100 / `α`=0.8 | 含む | 299.27 | **1049.20** | **2298.21** | 3051.62 |
| **after** K=100 / ef=100 / `α`=0.8 | 除く | 241.71 | **961.54** | **2160.35** | 2997.97 |
| K=50 / ef=100 | 含む | 310.43 | 1087.12 | 2305.30 | 3013.01 |
| K=50 / ef=100 | 除く | 246.29 | 960.46 | 2207.22 | 2971.02 |
| K=200 / ef=200 | 含む | 318.64 | 1184.42 | 2420.87 | 3267.99 |
| K=200 / ef=200 | 除く | 260.78 | 1100.16 | 2319.30 | 3187.54 |
| K=100 / ef=400 | 含む | 296.15 | 958.57 | 2123.64 | 3011.00 |
| K=100 / ef=400 | 除く | 243.92 | 923.14 | 2083.76 | 2917.32 |
| K=100 / ef=100 / `α`=0.0 | 除く | 217.11 | 815.13 | 1930.14 | 2621.16 |
| K=100 / ef=100 / `α`=0.5 | 除く | 227.58 | 857.78 | 1968.06 | 2770.66 |
| K=100 / ef=100 / `α`=0.7 | 除く | 226.51 | 830.24 | 1996.42 | 2687.89 |
| K=100 / ef=100 / `α`=0.9 | 除く | 224.50 | 864.41 | 1976.93 | 2635.11 |
| K=100 / ef=100 / `α`=1.0 | 除く | 218.65 | 831.53 | 1944.65 | 2650.81 |
| K=100 / ef=100 / RRF | 除く | 217.97 | 846.18 | 1933.94 | 2634.87 |

### 6.1 同一条件の 7 反復が、K と ef の差より大きい

`alpha` は候補モードの latency に効かない——候補集合の作り方も両スコアの計算も `alpha` に
依存せず、変わるのは最後の加重だけである。RRF も同じ候補集合を使う。
⇒ **上の表の下 7 行（K=100 / ef=100 の `α`×6 と RRF）は、latency については同一条件の 7 反復である。**

| | 系統2 p50 | 系統2 p95 |
| --- | ---: | ---: |
| 7 反復の最小 | 815.13 | 1930.14 |
| 7 反復の最大 | **961.54** | **2160.35** |
| 開き | **+17.9%** | **+11.9%** |

この幅（p95 で 1930〜2160ms）に対し、K と ef を振った 3 本は 2084（ef=400）・2207（K=50）・
2319（K=200）である。

🔴 **⇒ 本測定は K と `ef_search` の latency への効きを分離できていない。**
K=200 / ef=200 の 2319ms だけが 7 反復の上限を 7% 超えるが、7 点しかない反復から
「7.4% 外側なら差」と言える根拠は本測定には無い。`ef_search` を 100 → 400 に上げた本
（2084ms）は反復の**中**に入っており、**「ef を 4倍にしても遅くならなかった」とも
「速くなった」とも言えない。**

### 6.2 品質は K・ef に対してほぼ平らだった

§3.1・§3.2 のとおり、K=100 / 200 と ef=100 / 400 は `recall@10` も `MRR` もタグ別も同値で、
K=50 でだけ 3 クエリが入れ替わった。⇒ **本測定の範囲（K 50〜200・ef 100〜400）では、
品質は K と ef のどちらにもほとんど反応していない。**

### 6.3 要件定義 §8 の予算に対する位置

要件定義 §8 の性能要件は **「1万チャンクに対し検索 p95 < 200ms（埋め込み往復を除く）」**。

| | 値 |
| --- | ---: |
| after 既定候補の系統2 p95（**100,259チャンク**・実測） | **2,160.35 ms** |
| before 同上（**100,259チャンク**・実測） | 5,765.83 ms |
| §8 の予算（**1万チャンク**での要件） | 200 ms |

🔴 **本測定は 100,259 チャンクで、要件が課された規模の 約10倍である。**
[latency 正本](2026-09-02-eval-hybrid-latency.md) が「1万」と「10万」を混ぜるなと明記しており、
混ぜていない。**1万件は測っていない。**

🔴 **「予算の 10 倍規模で予算内に入った」とは書けない。** 2,160ms は 200ms の 10.8 倍である。
before から 2.67 倍速くなったが、桁は埋まっていない。
⚠️ 比例で 1万件相当に縮める計算（2160 ÷ 10.03 ≈ 215ms）は before 文書 §3.4 が挙げた 4 つの
理由でここでも成り立たない。**それは測定値ではない。**

---

## 7. `EXPLAIN (ANALYZE, BUFFERS)` — 代表クエリ1本

代表は before と同じ `q-014`「検索の応答時間の目標は何ミリ秒か」（`numeric`・bigram 15トークン）。

### 7.1 再現の正しさ

`EXPLAIN` に渡した SQL は `internal/store/postgres/searcher.go` の
`candidateSelectionCTE` + `weightedSumTail` を**そのまま書き写した**もので、
`hnsw.ef_search` は `set_config('hnsw.ef_search','100',true)` をトランザクション内で先に実行した。

上位10件は3つのスコアとも**小数6桁までレポートの `ranked_keys` と一致**し、
**before 文書 §4.1 の表とも同じ**である（`bench-embed#007` 0.670190 … `distractor:9010185#11` 0.554826）。

### 7.2 候補モードの計画（索引あり）

```
 Limit  (actual time=113.562..113.567 rows=10 loops=1)
   Buffers: shared hit=34194 read=18474
   ->  Sort  (Sort Method: top-N heapsort  Memory: 41kB)
         ->  WindowAgg  (actual time=111.230..113.508 rows=185 loops=1)
               ->  Nested Loop  (actual time=109.634..110.199 rows=185 loops=1)
                     ->  Unique  (actual time=109.616..109.678 rows=185)
                           ->  Merge Append  (rows=200)
                                 ->  Sort  (actual time=8.667..8.674 rows=100)
                                       ->  Limit  (actual time=7.628..8.643 rows=100)
                                             ->  Index Scan using chunks_embedding_hnsw on chunks
                                                   Order By: (embedding <#> '[...]'::vector)
                                                   Filter: (org_id = 1)
                                                   Buffers: shared hit=685 read=2022
                                 ->  Sort  (actual time=100.946..100.953 rows=100)
                                       ->  Limit  (actual time=100.918..100.925 rows=100)
                                             ->  Sort  (Sort Method: top-N heapsort  Memory: 29kB)
                                                   ->  Bitmap Heap Scan on chunks (actual time=5.304..99.133 rows=9929)
                                                         Heap Blocks: exact=7109
                                                         ->  Bitmap Index Scan on chunks_lexemes_gin
                                                               (actual time=4.598..4.598 rows=9929)
                     ->  Index Scan using chunks_pkey on chunks c (loops=185)
 Planning Time: 1.283 ms
 Execution Time: 113.751 ms
```

読み取れること:

1. ✅ **`Index Scan using chunks_embedding_hnsw` が出ている。** ベクトル側 top-100 は **8.6ms**。
   before の `Seq Scan` 100,259 行（走査だけで 44ms・距離計算まで入れて 224ms）が消えた
2. ✅ **`Bitmap Index Scan on chunks_lexemes_gin` が出ている。** 索引の走査自体は **4.6ms**
3. 🔴 **一時ファイルが消えた。** `Buffers:` に `temp read/written` が1つも無い。
   before は `temp read=16633 written=8493`（**読み 130MB・書き 66MB**）だった。
   窓関数 `MAX() OVER ()` が見る行が 100,259 行から **185 行**（候補の和集合）になったためである
4. 🔴 **並列化されていない**（`Workers Launched` の行が無い）。before と同じく窓関数を含むためだが、
   before と違って並列化しなくても困らない行数になっている
5. 🔴 **残りのコストは語彙側にある。** 語彙側 top-100 は **100.9ms** で、実行時間 113.8ms の 89% である

### 7.3 語彙側だけを取り出した計画 — GIN は絞り込めていない

```
 Limit  (actual time=107.674..107.683 rows=100 loops=1)
   ->  Sort  (Sort Method: top-N heapsort  Memory: 29kB)
         ->  Bitmap Heap Scan on chunks  (actual time=5.252..105.832 rows=9929 loops=1)
               Recheck Cond: (lexemes @@ '検索 | 索の | の応 | …')
               Filter: (org_id = 1)
               Heap Blocks: exact=7109
               Buffers: shared hit=29124 read=16620
               ->  Bitmap Index Scan on chunks_lexemes_gin  (actual time=4.503..4.504 rows=9929)
                     Buffers: shared hit=23 read=176
 Execution Time: 107.774 ms
```

| | 値 |
| --- | ---: |
| `Bitmap Index Scan` の時間 | **4.5ms** |
| `@@` に当たった行数（`q-014`・15トークン） | **9,929 / 100,259 = 9.9%** |
| ヒープを触ったブロック | 7,109 |
| `Bitmap Heap Scan` 全体の時間 | **105.8ms** |

🔑 **GIN は 4.5ms で候補を出すが、`ts_rank` は当たった 9,929 行すべてに計算される。**
候補モードの語彙側は「`@@` で絞ってから `ts_rank`」という形になっているが、
**bigram を `|`（OR）で並べた `tsquery` は当たる行が多い**ので、絞り込みが効きにくい。

別のクエリではもっと極端になる。`q-010`「文脈長は何トークンまで入るのか」（14トークン）は
**`@@` に 28,760 行（28.7%）が当たる**。

⚠️ **1クエリ1本の `EXPLAIN` である。** `ts_rank` のコストはクエリのトークン数と当たる行数に
依存するので、**他のクエリでは内訳の比が変わる。**

### 7.4 参考: ベクトル側だけ

```
 Limit  (actual time=7.412..8.368 rows=100 loops=1)
   ->  Index Scan using chunks_embedding_hnsw on chunks
         Order By: (embedding <#> '[...]'::vector)
         Filter: (org_id = 1)
         Buffers: shared hit=798 read=1914
 Execution Time: 8.413 ms
```

`Buffers` は 2,712 ブロック（約 21MB）である。783MB の索引のうち、この探索が触ったのはその 2.7% だった。

---

## 8. 索引を使わない候補モード — 候補生成の効果と索引の効果を分ける

ADR 0022 Decision 3 は「分離したいなら候補モードで索引を落として測る
（`enable_indexscan=off`）」と書いた。

🔴 **`make eval` ではこの条件を測れなかった**（§9）。ここでは **psql 直叩きの別測定**で
同じ SQL を索引あり／なしで比べる。

### 8.1 別測定であることの明示

| | `make eval`（§6） | 本節（psql 直叩き） |
| --- | --- | --- |
| SQL | `internal/store/postgres/searcher.go` を pgx が**パラメータつき**で実行 | 同じ SQL を**値を埋め込んだリテラル**で実行 |
| 事前検査 | `assertSameEmbedderAndTokenizer`（`chunks` の全走査）を毎回通る | 通らない |
| 接続 | `database/sql` のプール | 1本の psql セッション |
| クエリベクトル | 実行のたびに Ollama から取る／計測の外で1回取る | **計測の外で1回だけ取り、両条件で同じものを使う** |
| DB の状態 | **投入直後**（100k 行を書いた直後） | 何度も走査したあと（バッファが温まっている） |

🔴 **絶対値を §6 の表と並べてはならない。** 本節で意味があるのは**同じ条件どうしの比**だけである。

### 8.2 結果（58クエリ × 5ラウンド = 290サンプル・ウォームアップ1周は除外）

| 条件 | min | **p50** | **p95** | max |
| --- | ---: | ---: | ---: | ---: |
| **索引あり**（既定） | 5.94 | **255.06** | **353.98** | 446.76 |
| **索引なし**（`enable_indexscan` / `enable_indexonlyscan` / `enable_bitmapscan` = off） | 411.75 | **492.11** | **645.84** | 776.79 |

同じ SQL・同じ候補の作り方・同じベクトル。⇒ **索引を落とすと p50 は 1.93 倍、p95 は 1.82 倍になった。**

### 8.3 索引を落とした計画（同じ `q-014`）

```
 Limit  (actual time=738.865..738.938 rows=10 loops=1)
   Buffers: shared hit=682762 read=221904
   ->  … Hash Join …
         ->  Seq Scan on chunks c  (actual time=0.012..25.809 rows=100259)
         ->  … Merge Append …
               ->  Sort (vec) (actual time=199.753..199.825 rows=100)
                     ->  Gather Merge  (Workers Planned: 2  Workers Launched: 2)
                           ->  Parallel Seq Scan on chunks (actual time=0.103..192.020 rows=33420 loops=3)
               ->  Sort (lex) (actual time=502.810..502.814 rows=100)
                     ->  Seq Scan on chunks chunks_1 (actual time=0.034..500.868 rows=9929)
                           Rows Removed by Filter: 90330
 Planning Time: 1.207 ms
 Execution Time: 739.102 ms
```

| | 索引あり | 索引なし |
| --- | ---: | ---: |
| ベクトル側 top-100 | **8.6ms**（`Index Scan using chunks_embedding_hnsw`） | **199.8ms**（`Parallel Seq Scan` 2ワーカー） |
| 語彙側 top-100 | **100.9ms**（`Bitmap Index Scan` + `Bitmap Heap Scan`） | **502.8ms**（`Seq Scan`・90,330 行を Filter で捨てる） |
| 全体 `Execution Time` | **113.8ms** | **739.1ms** |
| 一時ファイル | なし | なし |

🔑 **候補生成の形にしただけで一時ファイルは消えている**（索引なしの計画にも `temp` が無い）。
before の 66MB / 130MB の溢れは、窓関数が 100,259 行を見ていたことによるもので、
**索引ではなく候補生成が消した**と読める。

⚠️ **索引なしの `Seq Scan` は before の `Seq Scan` と同じものではない。** before は
全行に**両方の**スコアを計算して合成式で並べていた。ここでは片側ずつ top-K を取っている。

---

## 9. 測れなかった 3 条件と、その原因

`k40-ef40`（2回）・`k40-ef100`・`noidx`（2回）の**計 5 回の実行が、いずれも
`eval: the two search paths returned different rankings`（`eval.ErrRankingDiverged`）で
途中終了した。** 落ちたのは 5 回とも同じクエリ `q-010`「文脈長は何トークンまで入るのか」で、
落ちたラウンドは 1・1・2・4・4 とばらついている。

| 実行 | 条件 | 落ちたラウンド |
| --- | --- | ---: |
| `100k-cand-k40-ef40-08` | K=40 / ef=40 | 4 |
| `100k-cand-k40-ef40-08-retry` | K=40 / ef=40 | 2 |
| `100k-cand-k40-ef100-08` | K=40 / ef=100 | 1 |
| `100k-cand-k100-ef100-08-noidx` | K=100 / ef=100・索引を使わない | 4 |
| `100k-cand-k100-ef100-08-noidx-retry` | 同上 | 1 |

### 9.1 HNSW の近似が原因ではない

🔴 **`noidx` の 2 回は `enable_indexscan` を落とした状態で落ちている。**
索引を使わない経路にはそもそも近似が無い。⇒ **`ef_search` を下げたことが原因ではない。**

### 9.2 原因は語彙側 top-K に同点の切り方が無いことだった

`q-010` を固定して（同じベクトル・同じ `tsquery`）、候補モードの各段を 15 回ずつ実行した。

| 何を 15 回実行したか | 返った id 集合の種類 |
| --- | ---: |
| ベクトル側 top-100（`vec`） | **1 種類**（15回とも同じ） |
| **語彙側 top-100（`lex`）** | **15 種類（毎回違う）** |
| 候補モード全体の上位10件 | 2 種類（11回 / 4回） |

境界の `ts_rank` を数えると:

| `ts_rank` | 同点の行数 |
| ---: | ---: |
| **0.015198178** | **36** |
| 0.01568066 | 10 |
| 0.015952054 | 4 |

`q-010` は `lexemes @@ tsquery` に **28,760 行**が当たり、上位100件の**境界に 36 行が同点で並ぶ**。
`lex` の `ORDER BY ts_rank(...) DESC LIMIT K` には第2ソートキーが無いので、
**そのうちどの行が候補に入るかは実行ごとに決まらない。**

⇒ 候補集合が変わる ⇒ `MAX(lexical_score) OVER ()`（候補集合内の最大値）が変わりうる ⇒
最終順位の 7〜10 位が入れ替わる ⇒ 2系統の順位が一致せず、評価ハーネスが止まる。

⚠️ **これは 10万件で初めて出た症状である。** 259 件では同点が積み上がらず、§2 のとおり
58 クエリすべてで並びが一致した。

### 9.3 埋め込みの再現性は確かめた（原因ではない）

系統1 は検索のたびに Ollama へ問い合わせ、系統2 は計測の外で1回だけ埋め込んだベクトルを使う。
「Ollama が毎回違うベクトルを返しているのでは」を潰すため、同じ本文を 6 回埋め込んで
バイト単位で比べた。**6 本とも完全に同一**だった（sha256 が1種類）。

### 9.4 何が測れていないか

- **K=40 の品質と latency**（`ef_search` の既定 40 と対にした点を含む）
- **`make eval` の経路での `noidx`**（§8 の psql 直叩きで代替した。同じ数字ではない）

🔴 **本書はこの症状の直し方を提案しない。** 観測の記録である。

---

## 10. プリペアド文の計画が 6 回目で切り替わる

§6 の系統2 p95（2,160ms）と §7 の `EXPLAIN`（113.8ms）は 19 倍離れている。
その差がどこから来るかを調べた結果を、**観測として**記録する。

`internal/store/postgres` は `database/sql`（pgx stdlib）を使うので、検索 SQL は
**パラメータつきのプリペアド文**として実行される。PostgreSQL は同じプリペアド文を
6 回目から**汎用計画**へ切り替えることがある。同じ SQL・同じ引数で、
`plan_cache_mode` を変えて 8 回ずつ測った（ms・psql 直叩き・バッファは温まっている）。

| クエリ | `plan_cache_mode` | 1 | 2 | 3 | 4 | 5 | **6** | 7 | 8 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `q-001` `pgvector 0.8.6` | `auto`（既定） | 13 | 5 | 5 | 4 | 4 | 5 | 5 | 5 |
| | `force_custom_plan` | 12 | 4 | 4 | 4 | 4 | 4 | 4 | 4 |
| | `force_generic_plan` | 3504 | 3470 | 3371 | 3547 | 3345 | 3443 | 3507 | 3522 |
| `q-014` 検索の応答時間の目標は… | **`auto`（既定）** | 119 | 91 | 90 | 93 | 90 | **3752** | **3625** | **3606** |
| | `force_custom_plan` | 120 | 89 | 90 | 89 | 89 | 89 | 93 | 95 |
| | `force_generic_plan` | 3755 | 3757 | 3559 | 3894 | 3678 | 3580 | 3818 | 3608 |
| `q-002` `` `<#>` 演算子は… `` | **`auto`（既定）** | 265 | 257 | 257 | 253 | 258 | **4223** | **4253** | **4203** |
| | `force_custom_plan` | 284 | 280 | 272 | 285 | 284 | 263 | 283 | 276 |
| | `force_generic_plan` | 4268 | 4275 | 4299 | 4251 | 4361 | 4204 | 4212 | 4254 |

🔑 **`q-014` と `q-002` は 6 回目に 40 倍遅くなる。`q-001` は切り替わらない。**
（PostgreSQL は汎用計画の見積もりコストが custom 計画の平均を上回るときは custom を使い続ける。
`q-001` はそちらに落ちている。）

汎用計画を強制して `EXPLAIN (ANALYZE)` を取ると、**ベクトル側が HNSW を使わなくなっている**:

```
 ->  Gather Merge  (Workers Planned: 2  Workers Launched: 2)
       ->  Sort  Sort Key: ((chunks.embedding <#> ($2)::vector))
             ->  Parallel Seq Scan on chunks  (actual time=0.199..3495.901 rows=33420 loops=3)
                   Filter: ((org_id = $1) AND …)
 Execution Time: 3755.102 ms   （語彙側は GIN のまま 206ms）
```

⚠️ **これは「§6 の数字がこの現象で説明できる」ことまでは示していない。**
`make eval` のラウンド別 p50（944.8 / 1007.6 / 985.8 / 947.3 / 981.5 ms）には段差が無く、
**ウォームアップ1周（58クエリ × 2系統）が既に閾値を越えているので、
段差はラウンド1より前で起きたはずである**——本測定はそれを直接観測していない。

🔴 **本書はここでも直し方を書かない。**

---

## 11. 投入時間 — HNSW の挿入コスト

### 11.1 `make eval` の投入（実測・行数を2秒間隔で数えた）

`100k-cand-k100-ef100-08` の投入を、`SELECT count(*) FROM chunks` を 2 秒ごとに読んで記録した。

| | 値 |
| --- | ---: |
| 開始（259 → 1,259 に動いた時刻） | 23:32:30 |
| 完了（100,259） | 23:39:46 |
| **所要** | **438 秒（7分18秒）** |
| 平均 | **228 行/秒** |

1万行ごとの平均速度（累積）:

| 累積行数 | 経過秒 | 累積 行/秒 |
| ---: | ---: | ---: |
| 10,000 | 33 | 303 |
| 30,000 | 109 | 275 |
| 50,000 | 193 | 259 |
| 70,000 | 288 | 243 |
| 90,000 | 387 | 233 |
| 100,000 | 438 | 228 |

🔑 **後半ほど遅くなる。** 最初の1万行が 33 秒（303 行/秒）なのに対し、最後の1万行は 51 秒
（196 行/秒）である。HNSW はグラフが育つほど1本の挿入が重くなる。

⚠️ **before の投入時間は精密に記録されていない。** before 文書 §7 は
「初回は約23分（埋め込み込み）、以後キャッシュがあれば**約1分**」と書いているだけで、
秒単位の実測は残っていない。⇒ **「7分18秒 対 約1分」という比較は、before 側が概算である。**

### 11.2 索引の維持コストだけを分けた実験（SQL レベル）

before の投入時間が概算なので、**同じ 100,259 行**を索引つきの表と索引なしの表へ
`INSERT ... SELECT` して、索引の維持コストだけを分けた。

```sql
CREATE TABLE ins_noidx (LIKE chunks INCLUDING GENERATED INCLUDING DEFAULTS);
CREATE TABLE ins_idx   (LIKE chunks INCLUDING GENERATED INCLUDING DEFAULTS);
CREATE INDEX ins_idx_embedding_hnsw ON ins_idx USING hnsw (embedding vector_ip_ops);
CREATE INDEX ins_idx_lexemes_gin    ON ins_idx USING gin (lexemes);
INSERT INTO ins_noidx (…) SELECT … FROM chunks;   -- 索引なし
INSERT INTO ins_idx   (…) SELECT … FROM chunks;   -- HNSW + GIN あり
```

| 投入先 | 100,259 行の `INSERT ... SELECT` |
| --- | ---: |
| 索引なし | **12.76 秒** |
| **HNSW + GIN あり** | **290.05 秒**（4分50秒） |
| 比 | **22.7 倍** |

できあがった索引の大きさは `chunks` のものと一致した（`ins_idx_embedding_hnsw` 783 MB /
`ins_idx_lexemes_gin` 71 MB）。⇒ **同じ索引を同じ行数ぶん作っている。**

⚠️ **これは `make eval` の投入経路そのものではない。** `make eval` は 1000 行ずつの多値 INSERT で、
埋め込みはディスクのキャッシュから読む。ここでは 1 本の `INSERT ... SELECT` である。
**測っているのは「同じ行数を入れるとき索引の維持にどれだけ余分にかかるか」だけである。**
⚠️ また `maintenance_work_mem` は 64MB のままで、**索引を後から一括で作る経路
（`CREATE INDEX` を投入後に打つ）は測っていない。**

---

## 12. 実行の記録（静穏の確認）

10 本の成功と 5 本の失敗はすべて**同一セッション・同一マシン・連続実行**である。

| ラベル | 開始 | 終了 | 結果 |
| --- | --- | --- | --- |
| `259-cand-k100-ef100-08` | 23:30:38 | 23:31:13 | ✅ |
| `259-exh-08-s4` | 23:31:13 | 23:31:44 | ✅ |
| `100k-cand-k100-ef100-08` | 23:32:28 | 23:51:41 | ✅ |
| `100k-cand-k50-ef100-08` | 23:52:40 | 00:13:26 | ✅ |
| `100k-cand-k200-ef200-08` | 00:13:26 | 00:35:50 | ✅ |
| `100k-cand-k40-ef40-08` | 00:35:50 | 00:46:05 | 🔴 `ErrRankingDiverged` |
| `100k-cand-k100-ef400-08` | 00:46:05 | 01:07:18 | ✅ |
| `100k-cand-k100-ef100-05` | 01:07:18 | 01:25:54 | ✅ |
| `100k-cand-k100-ef100-07` | 01:25:54 | 01:43:28 | ✅ |
| `100k-cand-k100-ef100-09` | 01:43:28 | 02:00:30 | ✅ |
| `100k-cand-k100-ef100-10` | 02:00:30 | 02:17:28 | ✅ |
| `100k-cand-k100-ef100-00` | 02:17:28 | 02:34:32 | ✅ |
| `100k-cand-k100-ef100-rrf` | 02:34:32 | 02:51:38 | ✅ |
| `100k-cand-k100-ef100-08-noidx` | 02:51:45 | 02:59:59 | 🔴 `ErrRankingDiverged` |
| `100k-cand-k40-ef40-08-retry` | 02:59:59 | 03:07:02 | 🔴 |
| `100k-cand-k40-ef100-08` | 03:07:02 | 03:13:59 | 🔴 |
| `100k-cand-k100-ef100-08-noidx-retry` | 03:14:06 | 03:22:19 | 🔴 |

（`259-*` は 2026-09-02、`100k-cand-k50-*` 以降は 2026-09-03。）

### 12.1 他プロセスのサンプリング（1秒間隔）

Go のビルド／テスト／lint のプロセス（`go` / `compile` / `link` / `vet` / `golangci-lint` / `gopls`）を
1秒間隔で 23:30 から記録した。**セッション全体で1件も検出しなかった**
（サンプラーは検出時のみ書き出す実装で、出力ファイルが作られなかった）。

⇒ **before の `100k-vector-only` にあったような他レーンの `make check` の混入は、本測定には無い。**

### 12.2 評価用 DB を分けた

before 文書の最後は「`recall_eval` は共有で、あとから走ったほうが 10万件を消す」という事故を
記録している。本測定は ADR 0022 で入った `-eval-db` を使い、**`recall_eval_lane18`** を
専用に作り直した。開始前に `ps -eo comm | grep -c '^eval$'` が 0 であることを確かめている。

### 12.3 索引を落とすための GUC は role 単位で被せ、必ず戻した

`make eval` は評価用 DB を毎回 `DROP` して作り直すので、`ALTER DATABASE … SET` は残らない。
`ALTER ROLE recall SET enable_indexscan = off` 等を実行の直前に被せ、
終了時（`trap`）に `RESET` した。**`pg_roles.rolconfig` が空であることを事後に確認済み**である。

---

## 13. この数字の限界

- 🔴 **本書は after であって、既定を切り替える判断ではない。** `RECALL_SEARCH_MODE` の既定を
  `candidates` にするかどうかは ADR 0023 の仕事である。**本書は結論を書いていない**
- 🔴 **before と after は別セッションである**（`dde0b12` と `33893eb`）。品質指標は決定的なので
  比較できるが、latency の絶対値には日をまたいだ差が乗りうる。⚠️ ただし**同一セッション内の
  7 反復ですら p95 が 11.9% ばらついている**（§6.1）ので、セッション間の差はそれ以上でありうる
- 🔴 **`recall` の差を「索引を入れたから」と読まないこと。** `exhaustive` は索引が張られていても
  索引を使わない（`ORDER BY` が合成式）。before と after の差には**索引の効果と候補生成の効果が
  分離できない形で混ざる**（ADR 0022 Decision 3）。§8 が psql 直叩きで一部を分けたが、
  それは `make eval` とは別の測定である
- **K と `ef_search` の latency への効きは分離できていない**（§6.1）。7 反復のばらつきより小さい
- **`alpha` の掃引は 6 点しかない**（0.0 / 0.5 / 0.7 / 0.8 / 0.9 / 1.0）。ADR 0015 が使った
  0.3 と 0.95 は測っていないので、**プラトーの端を本書は決めていない**
- **3 条件が測れていない**（§9）。K=40 の数字は本書に無い
- **58クエリ・タグ n=6〜8。** 総合 `recall@10` は1クエリで 0.017、タグ別は1クエリで 0.13〜0.17 動く
- **`EXPLAIN` は 1クエリ1本。** `q-014` は bigram 15トークンで `@@` に 9.9% が当たるが、
  `q-010` では 28.7% が当たる。**内訳の比はクエリで変わる**
- **PostgreSQL は既定設定。** `shared_buffers` 128MB に対し HNSW 索引が 783MB ある（§1.2）。
  §7 の `Buffers` はこの条件に条件付きの観察であって、索引がメモリに収まる環境の数字ではない
- **単一プロセス・WSL・共有 GPU。** 占有ベンチではない
- **1万件は測っていない**（§6.3）。要件定義 §8 の規模の数字が要るなら、その規模で測ること
- **紛れ込みは日本語 Wikipedia である。** 「10万件の実文書」一般の話に一般化しないこと（ADR 0019）

---

## 14. 再現手順

```bash
docker compose up -d
set -a; . ./.env; set +a

# 紛れ込み（sha256 514739b7… と一致すること。tools/wikidistract/README.md）
DIST=bin/wikidistract/distractors-100k.jsonl

# 259 件の整合（数分）
make eval EVAL_LABEL=259-cand-k100-ef100-08 EVAL_ALPHA=0.8 EVAL_ROUNDS=5 \
     EVAL_MODE=candidates EVAL_CANDIDATE_K=100 EVAL_EF_SEARCH=100 \
     EVAL_DB_NAME=recall_eval_lane18 GPU_NOTE="占有状況を書く"
make eval EVAL_LABEL=259-exh-08-s4 EVAL_ALPHA=0.8 EVAL_ROUNDS=5 \
     EVAL_DB_NAME=recall_eval_lane18 GPU_NOTE="占有状況を書く"

# 10万件（1本あたり投入 7分 + 計測 10〜13分）
for x in "100k-cand-k100-ef100-08 100 100 0.8" \
         "100k-cand-k50-ef100-08   50 100 0.8" \
         "100k-cand-k200-ef200-08 200 200 0.8" \
         "100k-cand-k100-ef400-08 100 400 0.8" \
         "100k-cand-k100-ef100-05 100 100 0.5" \
         "100k-cand-k100-ef100-07 100 100 0.7" \
         "100k-cand-k100-ef100-09 100 100 0.9" \
         "100k-cand-k100-ef100-10 100 100 1.0" \
         "100k-cand-k100-ef100-00 100 100 0"; do
  set -- $x
  make eval EVAL_LABEL=$1 EVAL_MODE=candidates EVAL_CANDIDATE_K=$2 EVAL_EF_SEARCH=$3 \
       EVAL_ALPHA=$4 EVAL_ROUNDS=5 EVAL_DISTRACTORS=$DIST EVAL_EMBED_CACHE=bin/embed-cache \
       EVAL_DB_NAME=recall_eval_lane18 GPU_NOTE="占有状況を書く"
done

# RRF（alpha は無視される）
make eval EVAL_LABEL=100k-cand-k100-ef100-rrf EVAL_MODE=candidates \
     EVAL_CANDIDATE_K=100 EVAL_EF_SEARCH=100 EVAL_FUSION=rrf EVAL_ROUNDS=5 \
     EVAL_DISTRACTORS=$DIST EVAL_EMBED_CACHE=bin/embed-cache \
     EVAL_DB_NAME=recall_eval_lane18 GPU_NOTE="占有状況を書く"
```

索引を落として測るとき（§8）は、`make eval` が DB を作り直すので `ALTER DATABASE` は残らない。
role に被せて、**終わったら必ず戻す**:

```sql
ALTER ROLE recall SET enable_indexscan = off;
ALTER ROLE recall SET enable_indexonlyscan = off;
ALTER ROLE recall SET enable_bitmapscan = off;
-- 測る --
ALTER ROLE recall RESET enable_indexscan;
ALTER ROLE recall RESET enable_indexonlyscan;
ALTER ROLE recall RESET enable_bitmapscan;
SELECT rolname, rolconfig FROM pg_roles WHERE rolname = 'recall';  -- 空であること
```

⚠️ `make eval` は `make check` に含まれない（ADR 0013）。

🔑 **`git_modified: false` で測ること。** 本測定は 10 本すべて `EVAL_OUT` をリポジトリ外へ向けて
走らせ、全部終わってから `data/` へ移した。

🔴 **他のレーンと `make eval` を同時に走らせないこと。** `EVAL_DB_NAME` を分けても、
Postgres・Ollama・GPU は共有である。
