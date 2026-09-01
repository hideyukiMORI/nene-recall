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
| 環境 | git revision / 未コミット変更の有無・Go 版・`embedder_id`・**Ollama の版とモデル digest**・PostgreSQL と pgvector の版・GPU 占有の自己申告 |
| 入力の同一性 | 3ファイルの **sha256** と件数 |
| 条件 | `alpha`・`limit`・`rounds`・ウォームアップ周回数・`k` 値・**パーセンタイルの定義**・**順位付けの条件**（`ranking`: 融合方式・`ts_rank` の正規化フラグ・RRF の `k`） |
| per-query の生データ | 上位の並び（`eval_key` と**3つのスコア**）・正解ごとの順位（圏外は `null`）・ラウンドごとの latency 2系統 |
| 集計値 | `recall@1/5/10`・`MRR`・p50/p95（2系統）・タグ別 `recall`・**micro 内訳**・**gold 長さ別の内訳**・**名指しの長文チャンクの追跡** |

`summary` は `queries`（と、長さ別の内訳については `corpus.jsonl` の本文）だけから
計算されている。数字を疑ったら、生データから出し直して突き合わせられる。

## 様式の版（`schema`）

| 版 | 日付 | 変えたところ |
| --- | --- | --- |
| `nene-recall/eval-report/v1` | 2026-09-01 | 初版 |
| `nene-recall/eval-report/v2` | 2026-09-02 | 語彙検索の実装に合わせて拡張 |

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
