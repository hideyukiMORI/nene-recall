# ADR 0002: Recall はチャンク本文を保持し、かつ `chunk_id` を必ず返す

## Status

accepted (2026-09-01)

## Context

Recall が chunk の本文を持つべきかは、単独動作と Corpus 統合で要求が逆を向く。

| 方式 | 単独動作（Phase 1） | Corpus 統合（Phase 2） |
| --- | --- | --- |
| `chunk_id` + score のみ返す | ✗ 本文が無く単独で答えを返せない | ○ 本文の正は MySQL のまま。整合性が壊れない |
| 本文も保持する | ○ | △ MySQL と二重保管になり、内容が食い違いうる |

二重保管の危険は具体的である。Corpus 側で chunk が更新されたのに Recall への伝播が漏れると、
**Corpus の画面が Recall の古い本文を引用として表示する**。引用の正確さが売りの製品でこれは致命的になる。

## Decision

**Recall はチャンク本文を保持する。同時に、検索レスポンスに必ず `chunk_id` を含める。**

そのうえで Phase 2 の統合規約として次を定める:

> **Corpus は Recall が返した `content` を使わない。**
> `chunk_id` だけを受け取り、本文は MySQL から引き直して `Chunk` を組み立てる。
> Recall が返すのは「どの chunk が、どの順で、どのスコアか」という順序情報のみとして扱う。

## Consequences

**得るもの**

- Phase 1 は Recall 単体で完結して答えを返せる
- Phase 2 では本文の単一正本が MySQL に残る。伝播漏れが起きても、
  **順序がずれるだけで、嘘の本文は表示されない**。障害の重さが「誤情報」から「検索精度の劣化」に下がる

**払うもの**

- 本文が Recall と MySQL に二重に存在し、ストレージを余分に食う
- Corpus 統合時、返された `content` を無視するぶん通信量が無駄になる。
  必要なら `include_content=false` を後から足せる（Phase 1 では作らない）

**追随作業**

- Phase 2 のアダプタ実装時、`content` を使っていないことをテストで固定する

## Related

- Issue: なし
- PR: なし
- Supersedes: none
- Superseded by: none
