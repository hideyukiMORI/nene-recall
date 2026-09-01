package postgres_test

import (
	"database/sql"
	"testing"

	// 管理用の接続もテスト用 DB の作成に pgx を使う。
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
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
	adminDSN   = "postgres://recall:recall@localhost:5433/recall?sslmode=disable"
	testDSN    = "postgres://recall:recall@localhost:5433/recall_test?sslmode=disable"
	testDBName = "recall_test"
)

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

	recreateTestDatabase(t)

	db, err := postgres.Open(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("テスト用 DB へ接続できない: %v", err)
	}

	store, err := postgres.New(db, e)
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
func recreateTestDatabase(t *testing.T) {
	t.Helper()

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
		`DROP DATABASE IF EXISTS `+testDBName+` WITH (FORCE)`); err != nil {
		t.Fatalf("テスト用 DB を落とせない: %v", err)
	}

	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+testDBName); err != nil {
		t.Fatalf("テスト用 DB を作れない: %v", err)
	}
}

// attachStore は既にあるテスト用 DB に、別の Embedder でもう1つ Store を繋ぐ。
//
// モデルを切り替えた状況を作るために要る。newTestStore は DB を作り直すので、
// 同じデータに対して別の Embedder を当てるにはこちらを使う。
func attachStore(t *testing.T, e embed.Embedder) *postgres.Store {
	t.Helper()

	db, err := postgres.Open(t.Context(), testDSN)
	if err != nil {
		t.Fatalf("テスト用 DB へ接続できない: %v", err)
	}

	store, err := postgres.New(db, e)
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
