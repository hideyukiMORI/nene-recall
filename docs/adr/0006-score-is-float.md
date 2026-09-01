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

- Corpus レーンへ「`ChunkSearchResult::$score` を float に広げる」を依頼する。
  Recall の Phase 2 着手前であればいつでもよい

## Related

- Issue: なし
- PR: なし
- Supersedes: none
- Superseded by: none
