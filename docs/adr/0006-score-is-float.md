# ADR 0006: score は float とし、Corpus 側の int を先に広げておく

## Status

accepted (2026-09-01)

## Context

Corpus の `ChunkSearchResult` はスコアを **int** で持つ
（`nene-corpus/src/Search/ChunkSearchResult.php:13`）:

```php
public function __construct(
    public Chunk $chunk,
    public int $score,
) {}
```

現行の PDO 実装ではスコアが「ヒットした語数のカウント」なので int で辻褄が合っている。
しかしベクトル類似度は float である。Phase 2 で差し替えるとき、
この int が型の壁になる。

選択肢は二つ。

1. Recall 側が float を int にスケールして返す（例 ×10000 して丸める）
2. Corpus 側の `int` を `float` に広げる

1 は差し替え時の変更を Recall 側に閉じ込められるが、
**スコアの意味が呼び出し側から見て不透明になる**（10000 が何を指すのか型に現れない）。
また丸めによって同点が増え、順序が不安定になる。

## Decision

- **Recall の API は score を float で返す。** 合成スコアに加えて
  `vector_score` / `lexical_score` も float で個別に返す
- **Corpus 側の `ChunkSearchResult::$score` を `int` から `float` に広げる作業を、
  Phase 2 を待たずに先行して行う。**

先行する理由: 現行の PDO 実装が返すのは整数値なので、
`float` に広げても値は変わらず、既存の挙動は壊れない。
**つまり今なら無風で通せる。** Phase 2 で差し替えと同時にやると、
型変更とバックエンド交換が同じ PR に乗り、問題の切り分けが難しくなる。

## Consequences

**得るもの**

- Phase 2 の差し替え PR が純粋に「バックエンドの交換」だけになる
- `vector_score` / `lexical_score` を分けて返すので、
  検索が外したとき**ベクトル側か語彙側かを切り分けられる**。
  合成値だけでは重み `alpha` の調整が当てずっぽうになる

**払うもの**

- Corpus 側に小さな変更を1件先行投入する必要がある。
  `int` → `float` の拡大なので後方互換だが、PHPStan の型チェックには通す必要がある

**追随作業**

- ~~Corpus レーンへ「`ChunkSearchResult::$score` を float に広げる」を依頼する~~
  → **✅ Corpus 側 済（2026-09-01・PR #385）**

## 追記: Corpus 側の着地（2026-09-01）

**`nene-corpus` PR #385 MERGED**（`mergedAt = 2026-09-01T08:35:33Z` / SHA `8befe998`）。
指示書は `_work/handoff-corpus-2026-09-01-score-float-work-order.md`。

origin/main で実測確認済み:

| 箇所 | 着地後 |
| --- | --- |
| `src/Search/ChunkSearchResult.php` | `public float $score,` |
| `src/Search/PdoChunkSearchRepository.php:83` | `score: (float) $row['relevance_score'],` |
| `tests/Search/PdoChunkSearchRepositoryTest.php` | `assertSame(1.0 / 2.0 / 1.0, ...)` |
| `tests/Search/SearchChunksUseCaseTest.php:38` | `score: 1.0` |

PHPUnit 425/425（1,377 assertions）・PHPStan level8 no errors。**値は変わっていない。**

🔑 **`assertSame` のまま `1.0` に直したこと自体が、型が広がった証拠になっている。**
`1.0 === 1` は false なので、cast が効いていなければここで落ちる。
`assertEquals`（緩い比較）へ逃げると**この性質が失われ、次に float が壊れても気づけない**。
移行を「テストを緩めて通す」で済ませなかったのが、この作業の実質的な成果である。

⇒ **Phase 2 の差し替え PR は、純粋にバックエンドの交換だけになった。**

## Related

- Issue: `nene-corpus#384`
- PR: **`nene-corpus#385`（MERGED 2026-09-01・`8befe998`）**
- Supersedes: none
- Superseded by: none
