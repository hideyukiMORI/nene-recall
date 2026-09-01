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
| 条件 | `alpha`・`limit`・`rounds`・ウォームアップ周回数・`k` 値・**パーセンタイルの定義** |
| per-query の生データ | 上位 `eval_key` 列・正解ごとの順位（圏外は `null`）・ラウンドごとの latency 2系統 |
| 集計値 | `recall@1/5/10`・`MRR`・p50/p95（2系統）・タグ別 `recall` |

`summary` は `queries` だけから計算されている。数字を疑ったら、生データから
出し直して突き合わせられる。

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
