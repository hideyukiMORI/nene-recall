# ADR 0018: 形態素分割器（kagome）を**比較対象として**導入する。既定は bigram のまま

## Status

accepted (2026-09-02) — 施主判断（2026-09-02「進めて」）により Q-2 の第二段に着手

## Context

[ADR 0014](0014-lexical-search-is-tsvector-over-bigram.md) Decision 4 は Q-2（bigram か形態素か）を閉じず、
「決着するまでの既定が bigram」とだけ決めた。形態素解析器は辞書という依存を抱えるため、要件定義 §9 の
却下表が「この段取りは施主に諮る事項」と留保していた。施主が第二段（形態素を測る）へ進むと判断したので、
依存の追加（ARC-004）を本 ADR で根拠づける。

bigram の実測（[比較文書](../benchmarks/2026-09-02-eval-lexical-hybrid.md)）で語彙検索の余地は見えている:
`exact-term` +0.25・`compound` +0.24 は bigram が取り、`particle` −0.08・`synonym` −0.17 は取れていない。
形態素で変わりうるのは、**語境界を跨いだ偶然一致**（「の索」「を張」のような bigram）が消えることと、
**活用形の吸収**（原形に畳む）である。総合値が動くとは予想していない（予想は測定前に別文書へ事前登録）。

### 制約

- cgo なし・辞書ファイルの外部配置なし（バイナリに埋め込む純 Go 実装であること）
- `lexical.Tokenizer` の契約（決定的・空白を含まない・tsquery メタ文字を含まない）を満たす
- 取り込みと検索で同じ分割器。`Tokenizer.ID()` を行ごとに記録し不一致はエラー（既存の仕組み）
- **既定は変えない。** 既定の変更は実測を見て別 ADR で行う（ADR 0015 Decision 3: 分割器を変えたら `alpha` も測り直す）

## Decision

### 1. `github.com/ikawaha/kagome/v2` + `github.com/ikawaha/kagome-dict/ipa` を `internal/lexical/kagome` に置く

純 Go・MIT・辞書はバイナリ埋め込み（IPA 辞書）。契約パッケージ `internal/lexical` は触らない（ADR 0012 と同じ形）。

### 2. 分割規則 v1（ID `kagome:ipadic:ascii-words:v1`）

1. NFKC → 小文字化（bigram と同じ前処理。`orthography` の条件を揃える）
2. **ASCII の語（英数字＋連結子 `_.-/`）は bigram と同じ規則で1語1トークン**にし、kagome には渡さない。
   `pgvector`・`0.8.6`・`org_id` のような識別子は形態素解析の対象ではなく、`exact-term` の強さを bigram と揃えるため
3. それ以外の連続部を kagome（通常モード）で分割し、**原形**（`BaseForm`。無ければ表層形）をトークンにする
4. 品詞が **記号・助詞・助動詞** のトークンは捨てる。`ts_rank` は IDF を持たないので、高頻度の機能語を残すと
   長文ほど加点され、ADR 0014 が実測で退けた長さ正規化の問題を裏口から呼び戻す
5. 空白・tsquery メタ文字を含むトークンは捨てる（契約）

### 3. 選択は配線点で行う。`RECALL_TOKENIZER=bigram|kagome`（既定 `bigram`）、`make eval EVAL_TOKENIZER=…`

`internal/eval` は分割器を知らない（ARC-001）。レポートの `conditions` に `tokenizer_id` を記録する。

## 却下した選択肢

| 選択肢 | 却下の理由 |
| --- | --- |
| **MeCab（cgo）/ Sudachi（Java）** | cgo・別ランタイム。地雷 5 |
| **Lindera（Rust）を CLI 経由** | サブプロセス依存。決定性とクロスコンパイルの前提が崩れる |
| **kagome の UniDic** | 辞書がさらに大きく、短単位で切るため bigram に近づく。まず IPA で測る |
| **表層形をトークンにする** | 活用形が別トークンになり、形態素の利点（原形への畳み込み）を捨てる。表層形との比較は v2 の候補 |
| **助詞を残す** | `particle` タグの撃ち分けに効きそうに見えるが、IDF の無い `ts_rank` では機能語が長文を持ち上げる害の方が大きいと予想。**測っていない却下**なので、v1 の結果で `particle` が動かなければ v2 で試す |
| **形態素を既定にする** | 測っていない。既定の変更は別 ADR |

## Consequences

- バイナリが IPA 辞書ぶん大きくなる（数十 MB）。既定が bigram でも辞書はリンクされる。
  配布サイズが問題になるなら build tag で切る判断を別途行う
- 分割器を変えると保存済みの `lexeme_text` と噛み合わない。`tokenizer_id` の不一致チェックが守る（既存）。
  評価は DB を作り直すので影響しない
- 追従: 実測（bigram vs kagome・rounds=5・タグ別・`alpha` 掃引）→ Q-2 を閉じる ADR 0020（番号は着地順）

## Related

- 要件定義 §9 Q-2・「Q-1 / Q-2 で却下した選択肢」
- 予想の事前登録: `docs/benchmarks/2026-09-02-morph-prediction.md`（本 ADR と同じ PR で凍結）
- 関連 ADR: [0012](0012-embedding-implementations-live-in-subpackages.md)・[0014](0014-lexical-search-is-tsvector-over-bigram.md)・[0015](0015-fusion-is-weighted-sum-with-alpha-0.8.md)
- Supersedes: none / Superseded by: none
