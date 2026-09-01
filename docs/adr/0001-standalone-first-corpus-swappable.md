# ADR 0001: 単独動作を先に完成させ、Corpus 統合は後段に置く

## Status

accepted (2026-09-01)

## Context

NeNe Recall には二つの動機がある。

1. 施主個人の文書・ノート・コードに対する引用付き検索基盤が要る（先行する実需）
2. NeNe Corpus の検索が実質 RAG になっていないため、将来これを置き換えたい

この二つは同時に満たせるが、**どちらを設計の起点に置くか**で成果物が変わる。
Corpus 統合を起点にすると、スキーマ・認証・テナントモデルを Corpus に合わせることになり、
Corpus 側の開発レーンの進捗に律速される。逆に単独動作を起点にすると、
統合時にアダプタ層が必要になる。

## Decision

**単独で完結して動くことを Phase 1 の唯一のゴールとする。**
Corpus 統合（Phase 2）は、単独版が動いた後に**アダプタとして**足す。

Phase 1 では Corpus のスキーマに寄せない。ただし以下2点だけは統合を見越して先に守る:

- レスポンスのフィールド名を Corpus の `ChunkSearchResult` / `Chunk` に写せる語彙で揃える
- `org_id` を最初から必須にする（ADR 0003）

## Consequences

**得るもの**

- Corpus レーンの進捗に依存せず着手・完成できる
- 単独で価値が出るので、統合は「やってもよい」選択肢に降格する。
  統合が失敗しても Recall は無価値にならない

**払うもの**

- Phase 2 で Corpus 側に `HttpChunkSearchRepository` の実装が必要になる。
  ただし接合部は1メソッドのインタフェース
  （`nene-corpus/src/Search/ChunkSearchRepositoryInterface.php:9`）に既に切れており、
  DI 束縛も `SearchServiceProvider.php:23` の1箇所のみ。実装コストは小さい

**追随作業**

- Phase 2 着手時に Corpus 側へ ADR を1本立てる（driver 切替の判断）

## Related

- Issue: なし
- PR: なし
- Supersedes: none
- Superseded by: none
