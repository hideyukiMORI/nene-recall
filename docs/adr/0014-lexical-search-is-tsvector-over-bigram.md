# ADR 0014: 語彙検索は Go 側 bigram 分割 + Postgres `tsvector` で行い、長さ正規化は掛けない

## Status

accepted (2026-09-02) — ただし **Q-2（bigram か形態素か）は本 ADR では閉じない**（§ Decision 4）

## Context

要件定義 Q-1 は「語彙検索を Postgres の全文検索（`tsvector`）で行うか、Go 側で BM25 を
実装するか」、Q-2 は「日本語の分割を bigram にするか形態素解析にするか」を未決事項としていた。
[ADR 0009](0009-retrieval-evaluation-is-in-scope.md) はどちらも**評価セットで決着させる**と定め、
[ADR 0013](0013-evaluation-harness-design.md) がそのための `make eval` を用意した。

本 ADR は **2026-09-02 の実測**
（[`docs/benchmarks/2026-09-02-eval-lexical-hybrid.md`](../benchmarks/2026-09-02-eval-lexical-hybrid.md)）
を見てから書いている。予想は実装前に
[`2026-09-02-lexical-prediction.md`](../benchmarks/2026-09-02-lexical-prediction.md) に事前登録され、
測定後に書き換えていない。**ADR を先に書いて数字を後から合わせる**順序を避けるために、
実装（PR #7）と本 ADR は意図的に別の PR に分けてある。

### 実測が示したこと

| 構成 | `recall@10` | `MRR` |
| --- | ---: | ---: |
| ベクトルのみ（基準線） | 0.596 | 0.705 |
| **語彙のみ（bigram・`ts_rank` 正規化 `0`）** | **0.620** | **0.736** |
| 語彙のみ（`ts_rank` 長さ正規化 `2\|32`） | 0.544 | 0.496 |

- 語彙検索は**単体でベクトル検索を上回った**。ただし総合値の差 +0.024 は 58 クエリのゆらぎ
  （1クエリ＝0.017）と区別できない。決め手は総合値ではなくタグ別で、
  `exact-term` +0.25・`compound` +0.24 と `synonym` −0.17・`numeric` −0.09 が**同時に**起きていた。
  ⇒ ベクトルと語彙は**相補的**であり、語彙検索を持つ価値はここにある。
- `ts_rank` の長さ正規化は**有害**だった（0.544 vs 0.620・MRR は 0.496 vs 0.736）。
  第二注釈者が警告した「正規化なしは長文を優遇するだけではないか」という交絡は、
  長文 gold（>520字）の recall がベクトル 0.267・正規化なし 0.200・長さ正規化 0.067 で
  **正規化なしのほうが長文に弱い**ことから否定された。
- `paraphrase` 0.44 は ±0.00 で**動かなかった**（予想どおり）。語彙検索は最大の穴を埋めない。

### 制約

- **有料サービス・cgo・独自 Docker イメージを既定構成に持ち込まない**（CLAUDE.md・ADR 0007・ARC-004）
- テナント分離は Go 側の責任であり、検索の WHERE 句に `org_id` が**1箇所で**入る形を保つ（ADR 0003）
- 取り込みと検索は同じ分割器を通す。分割器の識別子（`Tokenizer.ID()`）を行ごとに記録し、
  不一致はエラーにする（ADR 0005 と同じ形）

## Decision

### 1. 語彙スコアは Postgres の `to_tsvector('simple', …)` と `ts_rank` で計算する（Q-1）

日本語の分割は Postgres に任せず、**Go 側の `lexical.Tokenizer` がトークン列を作り**、空白区切りの
1本の文字列（`lexeme_text`）として保存する。DB 側はそれを**生成列**
`lexemes tsvector GENERATED ALWAYS AS (to_tsvector('simple', lexeme_text)) STORED` に直す。
検索側も同じ分割器を通したトークンを `to_tsquery('simple', …)` に渡す。

`'simple'` 辞書を使うのは、Postgres 側に**一切の言語処理をさせない**ためである。分割規則は
Go 側に1つだけ存在し、`Tokenizer.ID()` がその版を名乗る。

### 2. `ts_rank` の正規化フラグは `0`（長さ正規化なし）

実測で長さ正規化（`2`）は有害だった。フラグ `32`（`rank/(rank+1)` の有界化）も掛けない。
有界化は加重和のために入れたものだったが、**有界であることと比較可能な尺度であることは違う**
（rank が小さいと `rank/(rank+1)` はほぼ恒等写像で、スケールの3桁の差を埋めない）。
スケール合わせは合成側の責任とし、[ADR 0015](0015-fusion-is-weighted-sum-with-alpha-0.8.md) で扱う。

### 3. 分割器の既定は `internal/lexical/bigram`（NFKC → 小文字化 → CJK は2文字重ね・英数は1語）

依存ゼロで、辞書ファイルを抱えない。`exact-term`・`compound` の改善はこの分割で得られている。

### 4. Q-2（形態素解析）は**本 ADR で閉じない**

bigram 側しか測っていないので、形態素との優劣は語れない。形態素解析器（kagome 等）は
ARC-004 により依存の追加に ADR を要し、要件定義 §9 の却下表が「**この段取りは施主に諮る事項**」と
明記している。⇒ 形態素を測るかどうかは施主の判断を待ち、判断が出たら**別の ADR** で扱う。
本 ADR が決めるのは「決着するまでの既定が bigram である」ことだけである。

## 却下した選択肢

| 選択肢 | 却下の理由 |
| --- | --- |
| **Go 側 BM25**（Q-1 の対案） | 品質の数字で負けたのではない——**測っていない**。却下の理由は構造にある。Go 側で BM25 を持つと、語彙の候補集合とベクトルの候補集合を Go でマージすることになり、(a) `org_id` の絞り込みが SQL と Go の**2箇所**に分かれる（ADR 0003 が最も嫌う形）、(b) 転置索引という**第二の状態**を Go 側に持ち、DB との整合を自前で保つ必要が生じる。`tsvector` は1本の SQL で `org_id` の WHERE・ベクトル距離・語彙スコア・合成を同時に計算でき、状態は DB に1つしかない。⚠️ **BM25 の数字は Phase 1 項目 8（SQLite ストア）で必然的に出る**——SQLite には `ts_rank` が無いので、そこで実装する語彙スコア（FTS5 の `bm25()` または Go 側）との比較が、事後的に Q-1 の品質面の答えになる。→ 実測は [`docs/benchmarks/2026-09-02-eval-store-comparison.md`](../benchmarks/2026-09-02-eval-store-comparison.md)（層2）を参照 |
| **`pg_bigm` / `PGroonga`** | `pgvector/pgvector:pg17` に入っておらず、独自イメージを作ると `compose.yaml` と CI の「ゲートの定義」を変えることになる。得られるのは主に**索引による高速化**で、索引は「測ってから入れる」領域（ADR 0007）。品質面では、Go 側 bigram + `'simple'` で同じ分割が既に得られている |
| **`ts_rank` の長さ正規化（`2`）** | 実測で有害（0.544 vs 0.620）。長文交絡の説明も実測で否定された |
| **`ts_rank_cd`（被覆密度）** | 測っていない。トークンの近接を使う方式で、bigram は隣接情報を既に持つため利得の見込みが薄い。将来 `particle`（−0.08）の改善を狙うときの候補として残す |
| **Postgres 側の日本語辞書（`japanese` 等）** | 標準の Postgres に日本語の ts 辞書は無い。入れるなら拡張で、上の `pg_bigm` と同じ理由で却下 |
| **形態素解析器を今入れること** | 二段構えの段取り（まず bigram で語彙の余地を数字で見る）を守った。次段に進むかは施主判断（§ Decision 4） |

## Consequences

- **利得**: ハイブリッド化の土台ができ、ADR 0015 と合わせて `recall@10` 0.596 → 0.724 を得た
- **費用**: 全行に `ts_rank` を計算する全探索であり、`@@` で絞っていない。10万件規模の p95 は
  項目 7（HNSW とあわせた実測）で見る。索引が要るなら `GIN (lexemes)` が候補だが、
  **ベクトル索引と同じく測ってから入れる**
- **地雷**: `lexeme_text` を DB 側の `to_tsvector` が**さらに**パースする。トークンに
  tsquery のメタ文字や空白が含まれると、Go 側の1トークンが DB 側で割れる。
  `lexical.Tokenizer` の契約コメントがこれを禁じ、ストアが契約違反をエラーにする
- **追従作業**:
  - `paraphrase` 0.44 は語彙で埋まらない。Q-4（reranker）と埋め込みモデルの選択が候補だが、
    **2026-09-02 時点では着手しない**（施主判断待ちの保留事項）
  - Q-2 の形態素は施主判断が出たら別 ADR
  - 項目 8（SQLite）で BM25 系の数字が出たら、本 ADR の「却下した選択肢」の Go 側 BM25 の行に追記する

## Related

- 要件定義: §9 Q-1 / Q-2、§9 「Q-1 / Q-2 で却下した選択肢」
- 実測: [`docs/benchmarks/2026-09-02-eval-lexical-hybrid.md`](../benchmarks/2026-09-02-eval-lexical-hybrid.md)
- 予想の事前登録: [`docs/benchmarks/2026-09-02-lexical-prediction.md`](../benchmarks/2026-09-02-lexical-prediction.md)
- 実装 PR: `#7`
- 関連 ADR: [0005](0005-embedding-provider-is-pluggable.md)（識別子で不一致を検知する形の原型）・
  [0007](0007-pgvector-over-brute-force.md)（測ってから索引）・[0009](0009-retrieval-evaluation-is-in-scope.md)・
  [0012](0012-embedding-implementations-live-in-subpackages.md)（契約と実装をサブパッケージで分ける形）・
  [0015](0015-fusion-is-weighted-sum-with-alpha-0.8.md)
- Supersedes: none
- Superseded by: none
