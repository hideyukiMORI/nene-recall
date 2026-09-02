package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// 🔑 postgres 側と違って、このテスト群は外部プロセスを必要としない。
// SQLite は t.TempDir() の下のファイルで完結するので、DB が無いときの Skip も
// 要らない。⇒ CI で必ず走る。ADR 0017 が Consequences に書いた「カバレッジの
// 底が上がる」はこの性質を指している。
//
// 🔴 ファイル名は postgres 側のテストと揃えてある。2つのストアは比較のために
// 並べて読まれるので、同じ観点のテストが同じ名前のファイルにあること自体が
// 「どちらかにしか無い観点」を見つけやすくする。

// testStore はテスト対象のストアと、検証のための素の接続。
type testStore struct {
	store *sqlite.Store
	db    *sql.DB
	path  string
}

// newTestStore はテスト用のファイルを作り、移行済みで空のストアを返す。
func newTestStore(t *testing.T, e embed.Embedder) *testStore {
	t.Helper()

	return newTestStoreWith(t, defaultStoreSpec(e))
}

// storeSpec はテスト用ストアの組み立て指定。
//
// 引数を4つ以下に保つための入れ物 (GO-011)。フィールドを足したときに
// 呼び出し側が全部見直されるようにするための形でもある。
type storeSpec struct {
	embedder  embed.Embedder
	tokenizer lexical.Tokenizer
}

// defaultStoreSpec は既定の指定を返す。
func defaultStoreSpec(e embed.Embedder) storeSpec {
	return storeSpec{embedder: e, tokenizer: newFakeTokenizer("fake-tokenizer:1")}
}

// newTestStoreWith は分割器も指定してテスト用ストアを作る。
//
// 既定から外したいのは、tokenizer_id の不一致検知と、実物の分割器を使う
// 往復同一性テストだけである。それ以外は newTestStore でよい。
func newTestStoreWith(t *testing.T, spec storeSpec) *testStore {
	t.Helper()

	// 🔴 t.TempDir() を使う。固定のパスにすると、並行して走るテストが同じ
	// ファイルを掴んで「たまに落ちる」ものになる。t.TempDir はテストごとに
	// 別のディレクトリを作り、後始末も自動で行う (usetesting)。
	path := filepath.Join(t.TempDir(), "recall.db")

	return attachTestStore(t, spec, path)
}

// attachTestStore は指定のファイルにストアを繋いで移行する。
func attachTestStore(t *testing.T, spec storeSpec, path string) *testStore {
	t.Helper()

	db, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("テスト用 DB へ接続できない: %v", err)
	}

	store, err := sqlite.New(db, spec.embedder, spec.tokenizer)
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

	return &testStore{store: store, db: db, path: path}
}

// attachStore は既にあるテスト用ファイルに、別の Embedder でもう1つ Store を繋ぐ。
//
// モデルを切り替えた状況を作るために要る。newTestStore は新しいファイルを作るので、
// 同じデータに対して別の Embedder を当てるにはこちらを使う。
func attachStore(t *testing.T, ts *testStore, spec storeSpec) *sqlite.Store {
	t.Helper()

	return attachTestStore(t, spec, ts.path).store
}
