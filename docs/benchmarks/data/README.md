# 評価レポートの置き場 — `docs/benchmarks/data/`

`make eval` が書き出す機械可読なレポート（JSON）を置く。
命名は `<日付>-eval-<ラベル>.json`（`make eval EVAL_LABEL=<ラベル>`）。

```bash
docker compose up -d                    # PostgreSQL
make eval EVAL_LABEL=baseline GPU_NOTE="他アプリが 5.7GB 使用中"
```

設計の根拠は [ADR 0009](../../adr/0009-retrieval-evaluation-is-in-scope.md) と
[ADR 0013](../../adr/0013-evaluation-harness-design.md)。

---

## なぜ JSON をコミットするのか

**集計値を第三者が生データから再計算できる状態を残すため。**

`docs/benchmarks/2026-09-01-baseline.md` の追記が記録しているとおり、モデルも
ランタイムも一致しているのに §5 の絶対値は再現しなかった。原因は、記録が
「どの式で計算したか」を欠いていたことだった。教訓は一行で書かれている——
**後から検証できない数字は正本になれない。**

レポートには次が入る。

| 区分 | 内容 |
| --- | --- |
| 環境 | git revision / 未コミット変更の有無・Go 版・`embedder_id`・**Ollama の版とモデル digest**・PostgreSQL と pgvector の版・**SQLite の版**・GPU 占有の自己申告 |
| 入力の同一性 | 3ファイルの **sha256** と件数 |
| 条件 | `alpha`（10進・v4 から `float64`）・`limit`・`rounds`・ウォームアップ周回数・`k` 値・**パーセンタイルの定義**・**順位付けの条件**（`ranking`: **バックエンド**・**語彙採点関数**・融合方式、および postgres でのみ `ts_rank` の正規化フラグと RRF の `k`） |
| per-query の生データ | 上位の並び（`eval_key` と**3つのスコア**）・正解ごとの順位（圏外は `null`）・ラウンドごとの latency 2系統 |
| 集計値 | `recall@1/5/10`・`MRR`・p50/p95（2系統）・タグ別 `recall`・**micro 内訳**・**gold 長さ別の内訳**・**名指しの長文チャンクの追跡** |

`summary` は `queries`（と、長さ別の内訳については `corpus.jsonl` の本文）だけから
計算されている。数字を疑ったら、生データから出し直して突き合わせられる。

## 様式の版（`schema`）

| 版 | 日付 | 変えたところ |
| --- | --- | --- |
| `nene-recall/eval-report/v1` | 2026-09-01 | 初版 |
| `nene-recall/eval-report/v2` | 2026-09-02 | 語彙検索の実装に合わせて拡張 |
| `nene-recall/eval-report/v3` | 2026-09-02 | 比較用の SQLite ストアに合わせて拡張 |
| `nene-recall/eval-report/v4` | 2026-09-02 | `conditions` をストア非依存で正確にした |

v2 で変えたのは3点。

1. **`ranked_keys` が文字列の配列からオブジェクトの配列になった。**
   各件が `eval_key` に加えて `score` / `vector_score` / `lexical_score` を持つ。
   🔴 名前を変えずに型だけ変えたのは、v1 を読む道具が「フィールドが無い」で
   静かに素通りするのではなく、型の不一致で落ちるようにするためである
2. **`summary` に `micro_recall` / `gold_length_recall` / `long_chunk_recall` が増えた**
3. **`conditions` に `gold_length_threshold_runes` / `long_chunk_keys` /
   `ranking` が増えた**

🔴 **`alpha` だけでは条件が決まらない。** 融合方式によって `alpha` の効き方は
変わり（順位融合 `rrf` では**無視される**）、語彙スコアの作り方も `ts_rank` の
正規化フラグで変わる。レポートを並べるときは必ず `conditions.ranking` を見ること。

```bash
make eval EVAL_LABEL=rrf EVAL_FUSION=rrf     # 順位融合（alpha は無視される）
make eval EVAL_LABEL=alpha-05 EVAL_ALPHA=0.5 # 加重和
```

`ranking` はフラグの値ではなく**ストアが実際に使った条件**を記録する。既定を
変えたときに「指定したつもりの条件」と「実際に使われた条件」がずれない。

🔴 **v1 と v2 のレポートで `ranked_keys` を直接比べないこと。** 集計値
（`recall` / `MRR` / タグ別）は定義が変わっていないので比較できる。

v3 で変えたのは2点。どちらも「**どのバックエンドで測ったか**」を記録するための
項目である（[ADR 0017](../../adr/0017-sqlite-store-for-comparison.md) Decision 6）。

1. **`conditions.ranking` に `store` と `lexical_scorer` が増えた。**
   `store` は `postgres` / `sqlite`、`lexical_scorer` は `ts_rank` / `fts5-bm25`
2. **`environment` に `sqlite_version` が増えた。**
   測っていないほうのエンジンの版は空になる（postgres で測れば `sqlite_version`
   が空、sqlite で測れば `postgres_version` と `pgvector_version` が空）

🔴 **2つのストアの `recall` の差を「ストアの差」と読まないこと。** 差には
**語彙採点関数の差**（`ts_rank` と `bm25()`）が混ざっている。分けて読むには、
純ベクトル（`alpha=1.0`）でストアの差を、語彙のみ（`alpha=0.0`）で採点関数の差を
それぞれ測る。⚠️ `alpha` の最適値は正規化方式に条件付きなので、postgres で
決めた値を SQLite にそのまま当てはめないこと。

```bash
make eval EVAL_LABEL=sqlite EVAL_STORE=sqlite  # 比較用の SQLite（Postgres は要らない）
```

v4 で変えたのは3点。どれも**レポートを読み違える経路を塞ぐ**ためで、
項目が増えたわけではない（2026-09-02 の比較計測で見つかった）。

1. **`conditions.alpha` が `float64` になった。** v3 までは `float32` を経由して
   いたので、`-alpha 0.6` が `"alpha": 0.6000000238418579` と刻まれ、機械で
   突き合わせると `== 0.6` が偽になった。検索へ渡る値が `float32` であることは
   変えていない（`index.Query.Alpha` の型は契約）。**記録するのは落とす前の10進**
2. **`conditions.ranking` の `ts_rank_normalization` と `rrf_k` が、そのストアに
   無ければ*キーごと出なくなった*。** `store: sqlite` のレポートには現れない。
   🔴 postgres の `ts_rank_normalization` は **0 が正しい値**なので、消えるのは
   sqlite のときだけである（「無い」と「0」を区別するためにポインタで持っている）
3. **`conditions.alpha_note` がストアごとに変わった。** postgres は ADR 0015 の
   条件付きの説明、sqlite は「ADR 0015 の対象外である」ことを述べる。
   文言は配線点（`cmd/eval`）が `store` の値で選ぶ

### v3 以前のレポートを読むときの注意

**過去の JSON は当時の様式のまま残してある**（書き換えていない）。並べて読むときは
次の3点を補って読むこと。

| 項目 | v3 以前の読み方 |
| --- | --- |
| `conditions.alpha` | `float32` の丸めを帯びている。`0.6000000238418579` は **`0.6` の意味**であって、掃引の別の点ではない |
| `conditions.ranking.ts_rank_normalization` / `rrf_k` | `store: sqlite` のレポートにも `0` / `60` で入っている。SQLite に `ts_rank` は無いので、これは「**フラグ 0 で測った**」ではなく「その採点関数にそのつまみが無い」の意味 |
| `conditions.alpha_note` | `store` に関わらず **Postgres 由来の文言**が入っている。sqlite のレポートの「既定 0.8 は…プラトーの中心」は、**SQLite で測った話ではない**（SQLite のプラトーは 0.8〜0.9 で、`docs/benchmarks/2026-09-02-eval-store-comparison.md` が正本） |

集計値（`recall` / `MRR` / `latency`）の定義は v3 と v4 で変わっていないので、
そのまま比較できる。

## micro 内訳と gold の長さ別内訳（v2 から）

`recall@k` は**クエリ単位のマクロ平均**で、正解が1件のクエリと8件のクエリを
同じ重みで扱う。`micro_recall` はそれとは別に、**正解チャンクを1件ずつ**数える
（基準線が「131 / 236」と書いているのがこちら）。

`gold_length_recall` はその micro を **gold チャンクの長さ 520字**で2つに割る。

> 🔴 **Q-1（`tsvector` か BM25 か）のレポートには、この内訳を必ず併記する。**
> BM25 は文書長で正規化し、Postgres の `ts_rank` は既定でしない。この評価
> セットは長い一覧表チャンクを gold として繰り返し使っている（`readme#005` は
> 1,136字で5クエリの正解）ので、併記が無いと、出た差が**長文優遇の差**なのか
> **検索品質の差**なのかを切り分けられない
> （`testdata/eval/README.md`「既知の性質」の申し送り）。

`long_chunk_recall` は名指しの3件（`readme#005`・`requirements#023`・
`requirements#008`）を個別に追う。長さの区分だけでは、この評価セットの偏りが
「特定の3つが繰り返し正解になっている」という形をしていることが見えないため。
⚠️ `runes` が 0 のキーは、評価セットを作り直して**そのキーが消えた**ことを表す。

## 読むときの注意

- **p95 は2系統ある。** `with_embedding`（利用者から見た応答時間）と
  `without_embedding`（要件定義 §8 の性能要件が指すほう）。
  🔴 **片方だけ引用しないこと**（ADR 0009 の明示要求）
- **`alpha` は調整済みの値ではない。** レポートの `alpha_note` にそう書いてある。
  根拠を与えるのがこの評価の目的であって、前提ではない
- **数十クエリの `recall` を過信しない。** ADR 0009 が「方向性を見るための道具」と
  明記している粒度である。総合値より**タグ別の内訳**のほうが診断情報として濃い
- **`git_modified: true` は未コミットの変更を含む計測**を意味する。
  比較の基準線にはしないこと
- **`model_digest` が同じでも `embedder_id` は digest を含まない。**
  同じタグで別の重みが引かれる可能性はモデル側の検知網では塞げていないので、
  レポート同士を並べるときはここを目で突き合わせる

## 索引を入れる前後（ADR 0007）

HNSW を入れるときは、**同じ評価セット（同じ sha256）で before / after を測って
両方をここに残す**。ADR 0007 の価値は「pgvector を選んだこと」ではなく
「測ってから索引を入れた経路」なので、before が無ければその価値は消える。

比較対象の **Go 側総当たり（SQLite）** も同じ評価セットで測って並べる。
ADR 0007 はこの比較そのものを成果物に数えている（Phase 1 項目8 / ADR 0017）。
