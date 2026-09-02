package postgres_test

import (
	"context"
	"database/sql"
	"strconv"
	"syscall"
	"testing"

	// 管理用の接続もテスト用 DB の作成に pgx を使う。
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// 🔴 接続情報は compose.yaml・.github/workflows/ci.yml・.env.example と
// 同一の固定値である。4箇所が同じでなければならない。
//
// 🔴 ポートが 5433 なのは 5432 をネイティブ PostgreSQL 14 が占有しているため。
// 標準ポートに戻すとネイティブ側へ繋がり、「コンテナは healthy なのに
// SASL 認証失敗」という辿りにくい壊れ方をする。詳細は compose.yaml のコメント。
//
// 環境変数から読まない。理由は2つある。
//   - internal/store で os を触ると depguard の env-is-read-in-config-only に落ちる。
//     定数にすることが規約準拠の解でもある（ARC-005）
//   - ローカルと CI が同じ DSN で同じテストを走らせることが QLT-003 の要求であり、
//     env で分岐できる余地を作ると「CI でしか落ちない」失敗が生まれる
const (
	// dsnHead / dsnTail は DSN の「DB 名以外」である。可変なのは DB 名だけで、
	// ホスト・ポート・認証情報は adminDSN とテスト用 DSN で同一でなければならない。
	dsnHead = "postgres://recall:recall@localhost:5433/"
	dsnTail = "?sslmode=disable"

	adminDSN = dsnHead + "recall" + dsnTail

	// testDBPrefix は接尾辞（プロセス ID）の前に付く固定部分。
	testDBPrefix = "recall_test_"
)

// testDBName は自プロセス専用のテスト用 DB 名を返す。
//
// 🔴 固定名 recall_test を使わないのは、同じ Postgres に対して make check が
// 2本同時に走ると壊れるからである。2026-09-02 に実測: 両方の go test が
// `duplicate key value violates unique constraint "pg_database_datname_index"` で
// 全滅した（片方の CREATE DATABASE と、もう片方の DROP → CREATE が競る）。
// 設計→実装のループでは複数の作業木が並走するので、テストが互いを壊さないことが要る。
//
// 接尾辞はテストバイナリのプロセス ID。同じパッケージのテストは1プロセスで走るので
// 1プロセス＝1 DB になり、名前は実行中ずっと同じで、他プロセスとは決して衝突しない。
//
// 🔴 os ではなく syscall を使っている。ARC-005（docs/coding-rules.md）が禁じるのは
// 「環境変数・シグナル・標準入出力」で、自分のプロセス ID はそのどれでもない。
// ただし機械強制の depguard は os パッケージ全体を拒むため、粒度が規則より粗い。
// os.Getpid の実体は syscall.Getpid であり、ここで読んでいるのは環境ではなく
// 自分自身の識別子である。環境変数を読みたくなったら config 経由にすること。
func testDBName() string {
	return testDBPrefix + strconv.Itoa(syscall.Getpid())
}

// testDSN は指定したテスト用 DB への接続文字列を組み立てる。
func testDSN(dbName string) string {
	return dsnHead + dbName + dsnTail
}

// testStore はテスト対象のストアと、検証のための素の接続。
type testStore struct {
	store *postgres.Store
	db    *sql.DB
}

// newTestStore はテスト用 DB を作り直し、移行済みで空のストアを返す。
//
// 🔴 TestMain を使わず、テストごとに作り直している。TestMain に戻さないこと。
//
// TestMain には *testing.T が無いので、準備の失敗を Skip として報告するには
// 結果を可変のパッケージ変数に置くしかない。それは GO-007（可変グローバル禁止）に
// 正面から反し、実際に gochecknoglobals が発火した。規則が設計を正した場面であって、
// 抑制で通す場面ではない。
//
// テストごとに用意すれば報告先の t が常に手元にあり、隔離もテスト1件単位に強くなる。
// 対価は DB の作り直しを毎回行う実行時間だけで、実測 1.9 秒（テスト全体）である。
// 「TestMain のほうが速い」を理由に戻すと、可変グローバルが復活する。
func newTestStore(t *testing.T, e embed.Embedder) *testStore {
	t.Helper()

	return newTestStoreWith(t, defaultStoreSpec(e))
}

// storeSpec はテスト用ストアの組み立て指定。
//
// 引数を4つ以下に保つための入れ物 (GO-011)。フィールドを足したときに
// 呼び出し側が全部見直されるよう、exhaustruct が全項目の明示を強制する。
type storeSpec struct {
	embedder  embed.Embedder
	tokenizer lexical.Tokenizer
	fusion    postgres.Fusion
}

// defaultStoreSpec は既定の指定を返す。
//
// 融合方式の既定は加重和である（既定を変えるのは実測を見て ADR を書いてから）。
func defaultStoreSpec(e embed.Embedder) storeSpec {
	return storeSpec{
		embedder:  e,
		tokenizer: newFakeTokenizer("fake-tokenizer:1"),
		fusion:    postgres.FusionWeightedSum,
	}
}

// newTestStoreWith は分割器と融合方式も指定してテスト用ストアを作る。
//
// 既定から外したいのは、tokenizer_id の不一致検知・実物の分割器を使う
// 往復同一性テスト・融合方式の比較だけである。それ以外は newTestStore でよい。
func newTestStoreWith(t *testing.T, spec storeSpec) *testStore {
	t.Helper()

	recreateTestDatabase(t)

	db, err := postgres.Open(t.Context(), testDSN(testDBName()))
	if err != nil {
		t.Fatalf("テスト用 DB へ接続できない: %v", err)
	}

	store, err := postgres.New(db, spec.embedder, spec.tokenizer, spec.fusion)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	if err := store.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	return &testStore{store: store, db: db}
}

// recreateTestDatabase は開発用の recall とは別のテスト用 DB を作り直す。
//
// 別 DB にするのは開発中のデータを壊さないため。作り直すのは、前回の残骸に
// 依存したテストが「たまたま通る」状態を作らないため。
//
// 🔴 触るのは自分の recall_test_<pid> だけである。recall_test_% をまとめて
// 掃除しないこと——その名前の DB は今まさに別プロセスのテストが使っている
// 可能性があり、それは今回直した障害そのものになる。
func recreateTestDatabase(t *testing.T) {
	t.Helper()

	name := testDBName()

	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("管理接続を開けない: %v", err)
	}

	defer func() {
		if err := admin.Close(); err != nil {
			t.Errorf("管理接続を閉じられない: %v", err)
		}
	}()

	if err := admin.PingContext(t.Context()); err != nil {
		t.Skipf("Postgres へ接続できない。`docker compose up -d` で起動すること: %v", err)
	}

	// FORCE は残った接続を切ってから落とす（PostgreSQL 13 以降）。
	// 前のテストが返し損ねた接続で DROP が失敗するのを防ぐ。
	if _, err := admin.ExecContext(t.Context(),
		`DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		t.Fatalf("テスト用 DB を落とせない: %v", err)
	}

	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+name); err != nil {
		t.Fatalf("テスト用 DB を作れない: %v", err)
	}

	// 作った DB は必ず片付ける。名前がプロセスごとに変わるようになったので、
	// 残すと実行のたびに recall_test_<pid> が増え続ける。
	//
	// 🔴 TestMain に置いていない。TestMain には *testing.T が無く、
	// 後片付けの失敗を報告する手段が（GO-014 が log/fmt の直書きを禁じている以上）
	// 無くなるためである。t.Cleanup なら報告先の t が手元にあり、この関数を
	// 呼んだテストだけが自分の後始末を持つ。
	//
	// ⚠️ ここを通らない終わり方（kill・パニックでのプロセス毎死）では DB が残る。
	// 残骸は手で落とすこと:
	//   psql "postgres://recall:recall@localhost:5433/recall" \
	//     -c 'DROP DATABASE recall_test_12345 WITH (FORCE)'
	t.Cleanup(func() { dropTestDatabase(t, name) })
}

// dropTestDatabase はテスト用 DB を落とす。
//
// 管理接続を開き直しているのは、recreateTestDatabase の接続が defer で
// 既に閉じているため。片付けは失敗しても後続のテストを止めないので Errorf で報告する。
func dropTestDatabase(t *testing.T, name string) {
	t.Helper()

	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Errorf("片付けの管理接続を開けない: %v", err)

		return
	}

	defer func() {
		if err := admin.Close(); err != nil {
			t.Errorf("片付けの管理接続を閉じられない: %v", err)
		}
	}()

	// FORCE は返し損ねた接続を切ってから落とす。テストが失敗して Store を
	// 閉じられなかった場合でも、DB を残さない。
	//
	// 🔴 t.Context() をそのまま渡さない。t.Context() は Cleanup が呼ばれる
	// 直前に取り消されるので（Go 1.24 以降）、そのまま使うと DROP が
	// context canceled で必ず失敗し、DB が残る。取り消しだけ外して使う。
	if _, err := admin.ExecContext(context.WithoutCancel(t.Context()),
		`DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		t.Errorf("テスト用 DB を片付けられない: %v", err)
	}
}

// attachStore は既にあるテスト用 DB に、別の Embedder でもう1つ Store を繋ぐ。
//
// モデルを切り替えた状況を作るために要る。newTestStore は DB を作り直すので、
// 同じデータに対して別の Embedder を当てるにはこちらを使う。
func attachStore(t *testing.T, e embed.Embedder) *postgres.Store {
	t.Helper()

	return attachStoreWith(t, defaultStoreSpec(e))
}

// attachStoreWith は指定を明示して、既にあるテスト用 DB へ別の Store を繋ぐ。
func attachStoreWith(t *testing.T, spec storeSpec) *postgres.Store {
	t.Helper()

	db, err := postgres.Open(t.Context(), testDSN(testDBName()))
	if err != nil {
		t.Fatalf("テスト用 DB へ接続できない: %v", err)
	}

	store, err := postgres.New(db, spec.embedder, spec.tokenizer, spec.fusion)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return store
}
