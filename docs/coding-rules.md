# Go コーディング規約 — NeNe Recall

> Status: normative（規範）/ 2026-09-01 初版
> 判断の根拠は [ADR 0010](adr/0010-strictness-is-mechanically-enforced.md)。
> 本書は Go を本リポジトリで承認された部分集合に狭めるためのものであり、
> ここが沈黙している領域は公式の Go スタイル（Effective Go / Go Code Review Comments）に従う。

---

## 0. この文書の読み方

**すべての規則は「機械強制」の状態を持つ。**

| 表記 | 意味 |
| --- | --- |
| **active** | 違反すると `make check` が落ちる。人の記憶に依存しない |
| **planned** | 規範だが、まだ機械が見ていない。**この行に触れる変更は PR で明示的に自己レビューすること** |
| **不採用** | 検討して採らなかった。理由を必ず併記する（再提案は同じ理由への反論から始める） |

🔴 **planned を active と書き換えないこと。** 未実装の強制を実装済みに見せるのは、
規約全体の信頼を壊す唯一の行為である。実装してから書き換える。

強制の実体は3層ある。

| 層 | 実体 | 守るもの |
| --- | --- | --- |
| コンパイラ | `internal/`・`CGO_ENABLED=0`・Go 本体 | 層境界・cgo 排除・未使用宣言 |
| 汎用静的解析 | `.golangci.yml` | Go として危ういこと |
| 規約検査 | `tools/conformance`（標準ライブラリのみ） | **NeNe Recall として守るべきこと** |

---

## 1. 型と状態（GO-0xx）

### GO-001 — 境界でのプリミティブ執着を禁じる

識別子・スコア・重み・次元数のように**単位や不変条件を持つ値**は、名前付き型にする。

現在確立している型: `org.ID`（テナント識別子）・`config.Store`・`config.EmbedProvider`。

🔴 **名前付き型は Go では防護として不完全である。** 実測:

```go
Search(rawInt64)   // ❌ コンパイルエラー
Search(1)          // ✅ 通ってしまう（未型付き定数の暗黙変換）
```

したがって **`org.ID` への直接変換は `CNF-001` が禁止する**。生成は `org.NewID` / `org.ParseID` のみ。

- 機械強制: **active**（`org.ID` について `CNF-001` / `CNF-002`）
- 機械強制: **planned**（他の値型への一般化）

### GO-002 — 閉じた選択肢は型で表す

モードや状態機械を bool の組み合わせ・マジック整数・裸の文字列で表さない。
選択肢が閉じているなら named type + 定数か、未公開メソッドで封印したインタフェースにする。

`switch` は網羅する。`default` があることを網羅の証明として扱わない
（`exhaustive` の `default-signifies-exhaustive: false`）。

- 機械強制: **active**（`exhaustive`・`gochecksumtype`・`iotamixing`）

### GO-003 — ゼロ値を有効値として使わない

🔴 **Go の最大の穴。** `var id org.ID` は必ず書けてしまい、言語では防げない。

したがって次を規範とする。

- 不変条件を持つ型は「ゼロ値は無効値」を doc コメントで宣言する
- 境界（HTTP・SQL・環境変数）で必ず検証してから内側へ渡す
- **`if id == 0 { id = defaultOrg }` の形を書かない。** これが ADR 0003 の禁止事項そのもの
- 部分構築が事故になる型は `exhaustruct_v5` の対象に加える（現在 `config.Config`・`index.Query`）

- 機械強制: **active**（部分構築のみ・`exhaustruct_v5`）
- 機械強制: **不能**（ゼロ値の生成そのもの。言語仕様上、検査で塞げない）

### GO-004 — nil の意味は一つ

`nil` は「省略可能な値が無い」だけを意味する。無効・未読込・失敗・未知・削除済みを表さない。

- `(nil, nil)` を返さない
- `err != nil` なのに nil を返さない
- 型アサーションの `ok` を捨てない
- 名前付き戻り値の裸 `return` を書かない

- 機械強制: **active**（`nilnil`・`nilerr`・`nilnesserr`・`forcetypeassert`・`nonamedreturns`）

### GO-005 — 期待される失敗は error 値。panic を使わない

- 検証エラー・見つからない・拒否・非互換は **error 値**で返す。panic / recover を使わない
- **動的 error を作らない。** sentinel を宣言し `%w` で包む（`err113`）
- **パッケージ境界を越える error は必ず包む**（`wrapcheck`）。設定側に例外を作っていない
- error を捨てない。`_ =` での破棄も禁止（`errcheck` の `check-blank`）

```go
// ✅ 呼び出し側が errors.Is で分岐できる
var ErrInvalid = errors.New("org: invalid organization id")
return fmt.Errorf("%w: must be a positive integer, got %d", ErrInvalid, v)
```

- 機械強制: **active**（`errcheck`・`errorlint`・`err113`・`wrapcheck`・`forbidigo`）

### GO-006 — 汎用データバッグを禁じる

`any` / `map[string]any` / 意味を持つ `Pair` 相当・文字列キーのメタデータで型を代用しない。
名前付きの型を作る。

**唯一の例外は JSON 符号化ヘルパ**（`httpapi.writeJSON` の `body any`）。境界の1関数に閉じる。

- 機械強制: **planned**（`CNF-005` として実装予定。現在は目視）

---

## 2. 構築と可視性

### GO-007 — 可変グローバルと init() を禁じる

パッケージ変数は sentinel error のような不変値のみ。`init()` を書かない。
初期化は `main` の配線点で明示的に行う。

- 機械強制: **active**（`gochecknoglobals`・`gochecknoinits`・`reassign`）

### GO-008 — 可視性は最小

- 既定は未公開。公開するのは他パッケージが実際に使うものだけ
- 公開宣言には doc コメントを付ける
- テストのためだけの公開は `export_test.go` に閉じる。**本体側で export しない**

- 機械強制: **active**（`revive: exported`・`testpackage` の既定除外）

### GO-009 — 言語マジックを禁じる

`reflect`・`unsafe`・cgo を使わない。

- 機械強制: **active**（`depguard`・`CGO_ENABLED=0`）

### GO-010 — 名前が役割を語る

常に禁止する型名の語尾: `Manager` / `Helper` / `Util` / `Utils` / `Common`。
常に禁止するパッケージ名: `utils` / `helpers` / `managers` / `misc` / `common`。

`Processor` や `Data` のように**文脈次第で妥当な語は機械では拒否しない**。
機械が拒否してよいのは「常に禁止」だけで、判断が要る語はレビューの仕事である。

- 機械強制: **active**（`CNF-003`）

### GO-011 — 複雑度に上限を置く

| 指標 | 上限 |
| --- | --- |
| 認知的複雑度（関数） | 10 |
| 循環的複雑度（関数） | 10 |
| 循環的複雑度（パッケージ平均） | 5.0 |
| 関数の長さ | 60 行 / 40 文 |
| ネストの深さ | 3 |
| 引数の数 | 4 |
| bool の制御引数 | 禁止 |

閾値を満たすためだけに意味のある処理を割るのは目的に反する。超える必要があるときは
**測定可能な理由**を添えて ADR にする。

- 機械強制: **active**（`gocognit`・`cyclop`・`funlen`・`nestif`・`maintidx`・`revive`）

### GO-012 — 1ファイル1主要宣言

ファイルは1つの主要な型とその周辺に閉じる。`utils.go` のような寄せ集めを作らない。

- 機械強制: **planned**（`CNF-006`）

---

## 3. 実行時の規律

### GO-013 — context を持ち回る

I/O を行う関数は `context.Context` を第1引数で受ける。構造体に隠さない。
`time.Sleep` で同期しない。

- 機械強制: **active**（`noctx`・`contextcheck`・`containedctx`・`fatcontext`・`forbidigo`）

### GO-014 — ログの書き方を一つに固定する

- `log/slog` のみ。標準 `log`・`fmt.Print*`・組み込み `print` を使わない
- **グローバルロガーを使わない。** ロガーは注入する
- **属性スタイルのみ**（`slog.String("addr", addr)`）。裸の可変長 key-value を書かない
- メッセージは定数。可変部は属性へ
- キーは snake_case

🔴 **`config.Config` を丸ごとログに渡さないこと。** `String()` を実装していないので
`%v` や `slog.Any` に渡すと `VoyageAPIKey` がそのまま出る。出してよいフィールドを1つずつ選ぶ。

- 機械強制: **active**（`sloglint`・`forbidigo`）
- 機械強制: **planned**（設定構造体をログ引数に渡すことの禁止＝`CNF-007`）

### GO-015 — SQL の取り扱い

- `SELECT *` を書かない（列の増減で壊れる）
- `rows` / `stmt` を必ず閉じ、`rows.Err()` を見る
- `QueryContext` 系のみ使う

- 機械強制: **active**（`unqueryvet`・`sqlclosecheck`・`rowserrcheck`・`noctx`）
  ※ Phase 1 でストアを書くまで対象コードが存在しない

---

## 4. アーキテクチャ（ARC-0xx）

### ARC-001 — 依存の方向

```
cmd/recall ──▶ internal/httpapi ──▶ internal/index (契約) ◀── internal/store/*
                    │                      │
                    └──▶ internal/config   └──▶ internal/chunk ──▶ internal/org
```

- 具体ストアを import してよいのは `cmd`（配線点）と `internal/store` 自身のみ
- **具体 Embedder（`internal/embed/ollama`）も同じ扱い。** import してよいのは
  `cmd` と実装自身だけで、他の層は `embed.Embedder` 越しに使う（ADR 0012）
- `internal/` は Go のコンパイラが外部からの import を拒否する

- 機械強制: **active**（`depguard` の `store-is-wired-only-in-cmd`・`embedder-is-wired-only-in-cmd`・コンパイラ）

### ARC-002 — 中核は純粋に保つ

`internal/chunk` / `internal/index` / `internal/embed` は
`time`・`math/rand`・`os`・`net/http`・`database/sql`・`log`・`log/slog` を import しない。
時刻・乱数・環境は値として渡す。決定性が壊れるとテストが書けなくなる。

**適用範囲は契約パッケージ本体のみである。** 上の3つは「契約」を置く場所であり、
その配下の**実装サブパッケージは pure-core の対象外**とする。

| パッケージ | 位置づけ | pure-core | 代わりに受ける制限 |
| --- | --- | --- | --- |
| `internal/index` | 契約 | 対象 | — |
| `internal/store/postgres` | 実装 | 対象外（`database/sql` 可） | `store-is-wired-only-in-cmd` |
| `internal/embed` | 契約 | 対象 | — |
| `internal/embed/ollama` | 実装 | 対象外（`net/http`・`time` 可） | `embedder-is-wired-only-in-cmd` |

実装が中核の制約を免れるのではない。**制約の種類が変わる**——純粋性の代わりに
「具体実装を知ってよいのは配線点（`cmd`）だけ」という依存方向の制限を受ける。
判断の根拠は [ADR 0012](adr/0012-embedding-implementations-live-in-subpackages.md)。

🔴 `depguard` の `pure-core` の files glob（`**/internal/embed/*.go` 等）で `*` は `/` を
跨がないため、サブパッケージは対象に入らない。**この glob を `**` へ「揃える」変更は
上の設計を壊す。** 変えるなら実装の置き場所を同時に決め直すこと。

- 機械強制: **active**（`depguard` の `pure-core`・`store-is-wired-only-in-cmd`・`embedder-is-wired-only-in-cmd`）

### ARC-003 — cgo を持ち込まない

`CGO_ENABLED=0` でビルドする。cgo を要求する依存が入った瞬間にビルドが落ちる。

- 機械強制: **active**（`Makefile` の `build`・`depguard`）

### ARC-004 — 外部依存は許可制

明示的に禁止している依存: `lib/pq`（非推奨）・`mattn/go-sqlite3` と
`sqlite-vec-go-bindings`（cgo）・`pkg/errors`（標準 `errors` に統一）・
`sirupsen/logrus`（`log/slog` に統一）・
`stretchr/testify`（アサーションを標準 `testing` の1つに保つ）。

現在の直接依存と、それを認めた判断:

| 依存 | 用途 | 根拠 |
| --- | --- | --- |
| `github.com/jackc/pgx/v5` | PostgreSQL ドライバ（`database/sql` 経由） | [ADR 0011](adr/0011-pgx-stdlib-driver.md) |
| `golang.org/x/text` | NFKC 正規化（`internal/lexical/bigram`） | PR #7（語彙検索の導入） |
| `modernc.org/sqlite` | SQLite ドライバ（純 Go・比較実測用のストア） | [ADR 0017](adr/0017-sqlite-store-for-comparison.md) |

新しい依存を足すときは ADR を1本立て、この表に行を足す。

🔴 `.golangci.yml` の `gomodguard_v2` は**禁止リストであって許可リストではない**
（`allowed` を書いていないので、挙げていない依存は lint を通る）。つまり
機械が止めるのは「一度禁止したものの再導入」だけで、**新しい依存の追加そのものは
止まらない**。「lint が通ったから許可された」と読まないこと。許可制を実際に
支えているのは、この表と ADR とレビューである。

- 機械強制: **active**（`gomodguard_v2`。ただし止めるのは禁止済みの依存の
  再導入だけで、新規依存の追加は止めない。上の🔴を参照）

### ARC-005 — プロセス環境に触るのは配線点だけ

環境変数・シグナル・標準入出力を触ってよいのは `internal/config` と `cmd`、
およびリポジトリ検査の道具（`tools/`）のみ。

- 機械強制: **active**（`depguard` の `env-is-read-in-config-only`）

---

## 5. ゲートの健全性（QLT-0xx）

### QLT-001 — 警告はエラー

`gofmt` 差分・`go vet`・`staticcheck`・全 linter の指摘は CI を落とす。

### QLT-002 — baseline を持たない

既存違反を凍結する仕組み（`new-from-rev`・既定の除外プリセット）を使わない。
違反ゼロから始めたので、増やさないことだけを守ればよい。

### QLT-003 — ローカルと CI は同一

`make check` が唯一の入口。CI は `make check` を呼ぶだけで、CI 側にしか無い検査を作らない。
検査を足したくなったら、まず `Makefile` に足す。

### QLT-004 — 生成物の差分は失敗

`go mod tidy -diff` に差分が出る状態でコミットしない。

### QLT-005 — ゲートを弱める変更は ADR

閾値の緩和・除外の追加・linter の削除・規則の降格が対象。
CI は `.golangci.yml` / `Makefile` / `ci.yml` / 本書の差分を PR に通知する。

### QLT-006 — テストと抑制の規律

- テストは外部テストパッケージ（`package foo_test`）から公開 API 越しに書く。
  内部にどうしても触る必要があるものは `export_test.go` に閉じる
- `//nolint` は **linter 名と理由の両方**が必須。使われていない `//nolint` も失敗
- 検査器を書いたら、**意図的な違反で発火することを証明するテスト**を併せて書く
  （検査器の最大の失敗は、見逃したまま常に緑を返すことで、本物のコードを見ている限り発覚しない）

### QLT-007 — カバレッジの下限

`Makefile` の `MIN_COVERAGE`。**上げる方向にしか動かさない。** 下げる変更は ADR を要する。
2026-09-01 時点の実測は 79.4%（下限 75.0%）。

### QLT-008 — 性能の主張は実測を伴う

「速い」は測定結果を伴わなければ主張しない。記録先は `docs/benchmarks/`。
索引の追加は before/after を並べて残す（ADR 0007）。

---

## 6. 規約検査（CNF-0xx）— `tools/conformance`

golangci-lint が見ないもの、つまり**このリポジトリ固有の規約**を検査する。
標準ライブラリのみで書かれており、`go test ./...` に含まれるので常に走る。

| ID | 内容 | 状態 |
| --- | --- | --- |
| `CNF-001` | `org.ID` への直接変換を `internal/org` の外で禁止（生成は `NewID`/`ParseID` のみ） | **active** |
| `CNF-002` | `org_id` を名乗るフィールド・引数の型は `org.ID`（`*Request` 型の DTO は除く） | **active** |
| `CNF-003` | 役割を語らない型名の語尾・パッケージ名を禁止 | **active** |
| `CNF-004` | コメント・文書中の ADR 参照が実在すること | **active** |
| `CNF-005` | `any` / `map[string]any` をドメイン・アプリ層で禁止 | planned |
| `CNF-006` | 1ファイル1主要宣言 | planned |
| `CNF-007` | 設定構造体をログ引数に渡すことを禁止 | planned |

---

## 7. 採用しなかった検査と、その理由

**「有効にしなかった」ことも決定である。** 再提案するときは、ここに書かれた理由への反論から始めること。

| 検査 | 不採用の理由 |
| --- | --- |
| `noinlineerr` | `if err := f(); err != nil` を禁止するが、これは Go の標準的な書き方であり、かつ **err のスコープを狭める安全な形式**である。統一のために安全性を下げる取引になる |
| `varnamelen` | `w http.ResponseWriter, r *http.Request` のような Go の確立した短縮名と正面から衝突する。防いだ事故より生む摩擦のほうが大きい |
| `wsl` / `nlreturn` | 空行の位置を強制する。`gofmt` が整形を canonical にしている以上、追加の様式は判断の負荷だけを増やす |
| `godot` | コメント末尾のピリオドを要求するが、本リポジトリのコメントは日本語で「。」で終わる。全行が誤検知になる |
| `paralleltest` | `t.Setenv` と併用できない（`t.Parallel` と排他）。設定テストが env 駆動である以上、恒常的な waiver が必要になり、waiver を常態化させるほうが有害 |
| `dupl` | テーブル駆動テストの構造的な重複を検出してしまう |
| `enable-all: true` | 相互に矛盾する linter が同時に入り、規則ではなく道具の機嫌に従うことになる |

---

## 8. 規約に違反したくなったとき

1. **まず、規約が間違っている可能性を検討する。** 規約は実装より新しくない
2. 規約が正しいなら、設計を変える。抑制で通さない
3. どうしても抑制が要るなら、`//nolint:<linter> // <理由>` を書く。理由が無いと `nolintlint` が落とす
4. **規則そのものを緩めるなら ADR を書く**（QLT-005）。
   旧規則のどの部分が今も有効かを明記すること
