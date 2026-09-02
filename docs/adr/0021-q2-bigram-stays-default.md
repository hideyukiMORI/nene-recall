# ADR 0021: Q-2 の決着 — 既定の分割器は bigram のまま。kagome は選択肢として残し、次は両者の和集合を測る

## Status

accepted (2026-09-02)

## Context

[ADR 0018](0018-morphological-tokenizer-as-measured-alternative.md) で形態素分割器 kagome を比較対象として入れ、
予想を事前登録（[`2026-09-02-morph-prediction.md`](../benchmarks/2026-09-02-morph-prediction.md)・凍結）した上で、
bigram と分割器以外は同一条件で測った（[`2026-09-02-eval-morph-vs-bigram.md`](../benchmarks/2026-09-02-eval-morph-vs-bigram.md)・
Postgres・rounds=5・同一セッション）。

### 実測（語彙のみ・`alpha=0.0`）

| 指標 | bigram | kagome | 差 |
| --- | ---: | ---: | ---: |
| `recall@10` | 0.620 | 0.637 | +0.017（ちょうど 1 クエリぶん） |
| `MRR` | 0.736 | 0.714 | **−0.022（逆向き）** |
| ハイブリッド `alpha=0.8` `recall@10` | 0.724 | 0.731 | +0.007（ゆらぎ） |

タグ別で 1 クエリ（0.14）を超えて動いたのは 2 つだけで、**向きが逆**である:

| tag | bigram | kagome | 差 |
| --- | ---: | ---: | ---: |
| `paraphrase` | 0.44 | **0.62** | **+0.18** |
| `orthography` | 0.56 | **0.39** | **−0.18** |

- `paraphrase` の利得は、活用形を原形に畳むことで「速くなる／速くなった」のような言い換えが同じトークンになるためと読める。
  最大の穴（`paraphrase` 0.44）に**初めて動いたもの**であり、reranker（Q-4）以外の経路が見えた
- `orthography` の退行は、表記ゆれ（送り仮名・カタカナ長音・漢字／かな）を kagome が**別の語**として切り、bigram の
  文字列一致が持っていた頑健さを失うためと読める
- `compound` は予想（下がる）に反して ±0.00。`exact-term` は 1 件の下降で、識別子の隣接が緩む経路（ADR 0018 の実装が見つけた）は
  このコーパスでは発火しなかった
- `alpha` のプラトーは bigram 0.7〜0.9・kagome 0.8〜0.9。**0.8 は両方に残る**
- latency: `tsvector` の実バイトが約半分（1414 → 694）になり、埋め込みを除く p95 は同モード比較で kagome が低い
  （bigram 14.2〜14.7 / kagome 8.1〜8.8ms・高モード）。⚠️ 系統2 p95 が**同一条件でも 2 モードに割れる**現象が見つかった（原因未特定・§ Consequences）
- 費用: バイナリ +12.5MB（IPA 辞書）。既定が bigram でも辞書はリンクされる

## Decision

### 1. 既定の分割器は **bigram のまま**（`RECALL_TOKENIZER=bigram`）

品質はどちらの優位も示さなかった（総合値はゆらぎの中、タグ別は +0.18 と −0.18 が相殺）。優位が無いとき既定を変えるコストだけが残る:
既存の保存済み `lexeme_text` が `tokenizer_id` 不一致で全部無効になる・依存が増える・`orthography` は日本語文書で頻出のゆれであり、
そこで −0.18 退行するものを既定にはできない。

### 2. kagome は**選択肢として残す**。`RECALL_TOKENIZER=kagome` は正式にサポートされた構成

`paraphrase` +0.18 と latency の低さは実利であり、利用者が「言い換えに強く、表記ゆれの少ない文書（識別子中心の技術文書など）」を
持つなら選ぶ理由がある。README と `.env.example` に選び方の基準（`orthography` vs `paraphrase` の取捨）を書く。

### 3. 次に測るのは **和集合分割器**（bigram のトークン ∪ kagome の原形トークン）

2 つの利得は別の機構から来ている（文字列一致の頑健さ／原形への畳み込み）ので、**両方のトークンを同じ `lexeme_text` に入れれば**
`orthography` を bigram で守りつつ `paraphrase` を原形で拾える、というのが自然な仮説である。代償は `tsvector` が約 1.5 倍になること
（latency）と、`ts_rank` が IDF を持たないため bigram と形態素で二重に加点される語が出ること（`MRR` への影響は予想できない）。
**予想を事前登録してから測る**（ADR 0018 と同じ手順）。ID は `union:bigram+kagome:v1`。

### 4. Q-2 はこれで**閉じる**。再開の条件は 3 の実測結果か、一般文書（項目 7 の Wikipedia 混入）での再測定

## 却下した選択肢

| 選択肢 | 却下の理由 |
| --- | --- |
| **kagome を既定にする** | `orthography` −0.18 の退行。総合値・`MRR` に優位なし。保存済みデータの無効化と依存増のコストだけが確実 |
| **kagome を消す** | `paraphrase` に初めて効いた経路を捨てることになる。latency も低い。選択肢として残す費用はバイナリサイズだけ |
| **kagome v2（助詞を残す）を先に測る** | `particle` は +0.04 で「動かない」予想が当たっており、助詞を残す動機が消えた |
| **表層形版を測る** | `paraphrase` の利得の源が原形化である可能性が高く、表層形にすると消える見込み。和集合の方が仮説として強い |
| **総合値で決める** | `recall@10` +0.017 は 1 クエリぶん。評価セットの README が総合値 1 本での判定を禁じている |

## Consequences

- CLAUDE.md・要件定義 §9 Q-2 を「決着（ADR 0021）」に。`.env.example` の `RECALL_TOKENIZER` に選び方を書く
- 追従 1: 和集合分割器の予想登録 → 実装 → 実測（Decision 3）。既定変更はその結果を見て別 ADR
  → 実測 [`2026-09-02-eval-union-tokenizer.md`](../benchmarks/2026-09-02-eval-union-tokenizer.md)
- 追従 2: **系統2 p95 の 2 モード問題**を切り分ける（Postgres の実行計画・JIT・共有バッファ・WSL のスケジューリングが候補）。
  切り分けまでは、latency の比較は**同一セッション・同モード**で行い、別セッションの正本と直接並べない（比較文書がそう書いている）
- ADR 0015 の `alpha=0.8` は両分割器のプラトーに残るので変更なし

## Related

- 実測: [`2026-09-02-eval-morph-vs-bigram.md`](../benchmarks/2026-09-02-eval-morph-vs-bigram.md)
- 予想: [`2026-09-02-morph-prediction.md`](../benchmarks/2026-09-02-morph-prediction.md)
- ADR [0014](0014-lexical-search-is-tsvector-over-bigram.md)（Q-2 を開けたまま既定 bigram）・[0015](0015-fusion-is-weighted-sum-with-alpha-0.8.md)・[0018](0018-morphological-tokenizer-as-measured-alternative.md)
- Supersedes: ADR 0014 Decision 4（「Q-2 は閉じない」）を本 ADR が閉じる。0014 の他の Decision は有効
- Superseded by: none
