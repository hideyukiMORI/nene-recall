# ADR 0011: pgx を database/sql (pgx/v5/stdlib) 経由で使い、pgvector 値は自前でテキスト符号化する

## Status

accepted (2026-09-01)

## Context

Phase 1 の 1番目「Postgres ストア」に着手するにあたり、ドライバの選定と、
pgvector の `vector` 型をどう Go から書き込むかを決める必要がある。

制約は3つある。

1. **cgo を持ち込めない**（ARC-003・`CGO_ENABLED=0` でビルドが落ちる）。
   `lib/pq` は非推奨で `gomodguard_v2` が既にブロックしており、推奨代替として
   `github.com/jackc/pgx/v5` が記名されている。
2. **機械強制の網の中に留まりたい**（ADR 0010）。`docs/coding-rules.md` の GO-015 は
   `sqlclosecheck` / `rowserrcheck` / `unqueryvet` / `noctx` の4つで強制されているが、
   ADR 0010 が「有効化済みだが、まだ一度も対象コードを見ていない」と記録しているとおり、
   **この規則が実際に効くのは Phase 1 で初めてである。**
3. **Phase 1 の 8番目に SQLite ストア（比較用）が控えている**（ADR 0007 が
   「同一データでの比較実測がそのまま成果物になる」と位置づけたもの）。
   こちらは純 Go の `modernc.org/sqlite` を使う。

pgvector の値の受け渡しについては、公式に `pgvector-go` というヘルパが存在する。
一方で本ストアがベクトル列に対して行う操作は、**書き込みと SQL 側での距離計算だけ**である。
スコアは `<#>` 等の演算子で Postgres が計算して数値列として返すので、
**ベクトルを Go 側へ読み戻す経路が無い。**

## Decision

**`github.com/jackc/pgx/v5` を採用し、`pgx/v5/stdlib` 経由で `database/sql` の
インタフェースとして使う。pgx ネイティブ API（`pgx.Conn` / `pgxpool`）は使わない。**

**pgvector の値はテキスト表記（`'[0.1,0.2,...]'::vector`）で自前に符号化し、
`pgvector-go` は依存に入れない。**

理由は次の3点である。

1. **cgo を避けつつ非推奨ドライバも避ける唯一の経路が pgx である。** pgx は純 Go で、
   `CGO_ENABLED=0` と両立する。`gomodguard_v2` のブロックリストとも矛盾しない
   （`lib/pq` の推奨代替として既に記名されている）。
2. **`database/sql` を通すのは、機械強制の網の中に留まるためと、対称性のためである。**
   - (a) GO-015 を守る linter 群（`sqlclosecheck` / `rowserrcheck` / `unqueryvet` / `noctx`）は
     `database/sql` の呼び出し形（`*sql.Rows` を閉じたか・`rows.Err()` を見たか・
     `QueryContext` を使ったか）を対象に設計されている。ネイティブ API を選ぶと、
     規約は文章として残るが**検査は何も見なくなる**。ADR 0010 が退けたのは
     まさにその状態である。
   - (b) Phase 1 項目8 の SQLite ストアも `database/sql` ドライバ（`modernc.org/sqlite`）である。
     同じインタフェースの上に2実装が並ぶので、`internal/store/postgres` と
     `internal/store/sqlite` の構造が対称になり、比較実測の差分がストアの本質だけになる。
3. **`pgvector-go` が要る場面が本ストアには無い。** ベクトル列は**書き込むだけで読み戻さない**
   （距離計算は SQL 側で行い、返るのはスコアの数値列）。したがって必要なのは
   `[]float32` → `[0.1,0.2,...]` という**一方向のテキスト符号化 約20行**だけである。
   依存1本と、その依存を正当化する ADR 1本を、20行のために払わない。

### 却下した選択肢

| 選択肢 | 却下の理由 |
| --- | --- |
| **pgx ネイティブ API**（`pgx.Conn` / `pgxpool`） | バイナリプロトコルと `CopyFrom` による一括投入で `database/sql` 経由より速い。しかし 10万件の取り込み時間の**支配項は埋め込み**であり、実測で約18分（32〜128本バッチ・`docs/benchmarks/2026-09-01-baseline.md`）である。DB 書き込みを速くしても、この支配項に対して測定可能な利得が無い。QLT-008 は「速さは実測を伴わなければ主張しない」と定めており、**利得を測れないうちに linter 網の外へ出る取引は成立しない**。将来ベンチで DB 書き込みがボトルネックだと判明したら、そのときの数字を根拠に本 ADR を supersede する |
| **`pgvector-go`** | 上記3のとおり。読み戻しが無いので、得られるのは符号化 約20行の節約だけ。依存を1本増やす対価に見合わない |
| **依存ゼロの維持（PostgreSQL wire protocol の自作）** | 現在 go.mod の依存はゼロで、その状態自体には価値がある。しかし SCRAM 認証と型変換の再実装は本プロジェクトの目的（検索の品質と、索引を入れる判断の経験）と無関係であり、自作したぶんだけ検証されていないコードが増える |
| **`lib/pq`** | 非推奨。`gomodguard_v2` が既にブロックしている（ARC-004） |

## Consequences

**得るもの**

- GO-015 の4つの linter が初めて実際の対象コードを得る。ADR 0010 が「まだ一度も対象コードを
  見ていない」と記録した状態が解消される
- Postgres ストアと SQLite ストアが同じインタフェースの上に対称に並ぶ

**払うもの**

- **go.mod の依存がゼロでなくなる。** pgx と、その間接依存（`jackc/pgpassfile`・
  `jackc/pgservicefile`・`golang.org/x/crypto` 等）が入り、`make vuln` の監視対象が増える
- `database/sql` の抽象を挟むぶん、pgx 固有の機能（`CopyFrom`・通知・型プラグイン）は
  素直には使えない。使いたくなった時点が本 ADR を見直す時点である
- ベクトルの符号化を自分で持つので、その正しさは自分のテストで担保する必要がある
  （`internal/store/postgres/encode.go` とその外部テスト）

**正直に記録しておくこと**

- **本 ADR は性能を放棄する判断ではなく、性能差を「まだ測っていない」と認める判断である。**
  ネイティブ API のほうが速いこと自体は疑っていない。棄却したのは速さではなく、
  「支配項でない箇所を、測らずに、検査の外へ出てまで速くすること」である

## Related

- Issue: なし
- PR: なし
- Supersedes: none
- Superseded by: none
- 関連: ADR 0007（pgvector の採用と、索引を後から入れる経路）、
  ADR 0010（機械強制。本 ADR の理由2はこの ADR の帰結）、
  ADR 0005 / ADR 0008（`Embedder` の契約。符号化するベクトルの出どころ）
