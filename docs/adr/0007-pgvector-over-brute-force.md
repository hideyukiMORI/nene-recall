# ADR 0007: pgvector を採用し、索引なしから始めて実測後に HNSW を入れる

## Status

accepted (2026-09-01) — **supersedes ADR 0004**

## Context

ADR 0004 では「ベクトル DB も cgo も使わず、SQLite ＋ Go の総当たり内積」と決めた。
**その性能分析は今も正しい。** 裏取りした一般的な知見:

- 100万件未満では、専用ベクトル DB と pgvector の応答差は**単桁〜低二桁ミリ秒**に収まり、
  **その差は検索の手前にある埋め込み API 往復のレイテンシより小さい**
- pgvector の限界が見え始めるのは **5,000万件**、分散が要るのは10億件から
- FAISS でも全探索（`IndexFlatIP`）は一級の索引タイプであり、小規模での正解

Phase 1 の想定上限は10万件で、pgvector の限界の 1/500。
**性能を根拠に判断を変える理由は無い。**

判断が変わったのは、**別の軸が追加されたから**である。
施主はこのリポジトリをキャリア資産としても位置づけている（2026-09-01 申告）。
この軸で評価すると、総当たり方式には次の弱点がある:

- 案件票・求人票に載るのは `pgvector` `Pinecone` `Qdrant` `Weaviate` `Azure AI Search` といった**製品名**である。
  「自前でコサイン類似度を回した」はキーワード検索に一切かからない
- 総当たりだけで完結すると、**索引を入れる／入れない判断を実際に下した経験が残らない**

## Decision

**ストアを pgvector（PostgreSQL 拡張）に切り替える。ADR 0004 を supersede する。**

ただし ADR 0004 の結論を捨てるのではなく、**経路として踏む**:

1. **まず索引を作らない**（pgvector の既定は全探索）。Phase 1 はこの状態で完成させる
2. **実測する。** 10万件規模で p95 レイテンシと recall を測り、結果を `docs/benchmarks/` に残す
3. **測ってから HNSW を入れる。** `CREATE INDEX ... USING hnsw` の DDL 一発で足せる。
   before/after の数値を並べて記録する

**索引を最初から入れないことが要点である。** 入れてしまうと「なぜ入れたか」を数字で語れなくなる。

補助的な決定:

- ドライバは `jackc/pgx`（**純 Go**）。ADR 0004 の本当の制約——cgo を持ち込まず
  クロスコンパイル可能に保つ——はここで維持される
- SQLite ＋ 総当たり実装も捨てず、`RECALL_STORE=sqlite|postgres` で選べるようにする。
  **同一データでの比較実測がそのまま成果物になる**ため
- Corpus は MySQL なので、Postgres は別ストアとして自然に共存する

## Consequences

**得るもの**

- 案件票に載る技術名が実装経験として残る
- 「10万件で全探索 XXms、HNSW で YYms、recall ZZ%」という**自分で測った数字**を持てる。
  製品名を並べるより、この数字のほうが実戦での説明力が高い
- pgvector は「ただの Postgres」なので、日本のエンタープライズで承認が通りやすい。
  SaaS の Pinecone では金融・大手の稟議を通せない場面がある
- 施主の既存資産（PHP 20年・RDBMS 設計・地銀基幹系）と地続きで、新しい世界の話にならない

**払うもの**

- **「単一バイナリ＋SQLite ファイル、インフラ依存ゼロ」という性質を失う。**
  Postgres プロセスが要る。ADR 0004 が守ろうとしていた価値の一部は、ここで手放される
- 個人利用の起動コストが上がる（`docker compose up` が挟まる）。
  ただしフリート全体が既に docker compose を使っており、実質的な負担は小さい
- ストア実装が2本になり、保守対象が増える

**正直に記録しておくこと**

- **この判断は性能上の必要から出たものではない。** 10万件規模では総当たりで足りるという
  ADR 0004 の分析は有効なままである。市場価値という非技術的な軸を、
  技術的な軸より優先した判断であることを明示しておく
- **ベクトル DB の選定自体は、もう差別化にならない。** コサイン類似度とメタデータフィルタは
  2026年時点で全製品が対応済みで、差がつくのは運用コスト・既存基盤との統合性・スキルセットである。
  だからこそ本 ADR は「pgvector を選んだこと」ではなく
  「**測ってから索引を入れた経路**」を成果物と見なす。ADR 0009 が対になる

## 参照

- [You probably don't need a vector database (Encore)](https://encore.dev/blog/you-probably-dont-need-a-vector-database)
- [Vector Databases in 2026: A Systematic Performance Analysis (EFFOMA)](https://effoma.com/blog/vector-database-performance-benchmark-comparison-2026/)
- [pgvector vs Pinecone (Encore)](https://encore.dev/articles/pgvector-vs-pinecone)

## Related

- Issue: なし
- PR: なし
- Supersedes: ADR 0004
- Superseded by: none
