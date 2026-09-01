package postgres_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// TestMigrateIsIdempotent は起動のたびに呼ばれる前提を確かめる。
//
// 適用済みを飛ばせないと、2回目の起動で CREATE TABLE が失敗して立ち上がらない。
func TestMigrateIsIdempotent(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	// newTestStore の中で1回適用されている。もう一度呼んでも失敗しない。
	if err := ts.store.Migrate(t.Context()); err != nil {
		t.Fatalf("2回目の Migrate が失敗した: %v", err)
	}

	var applied int
	if err := ts.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("schema_migrations を読めない: %v", err)
	}

	if applied != 1 {
		t.Errorf("適用記録が %d 件。二重適用または記録漏れ (want 1)", applied)
	}
}

// TestMigrateCreatesChunksTable は列がスキーマどおりに存在することを見る。
func TestMigrateCreatesChunksTable(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	const stmt = `SELECT count(*) FROM information_schema.columns
	              WHERE table_name = 'chunks' AND column_name = $1`

	for _, column := range []string{
		"id", "org_id", "document_id", "source_id", "chunk_index",
		"content", "page_number", "section_label", "embedder_id",
		"embedding", "created_at",
	} {
		var found int
		if err := ts.db.QueryRowContext(t.Context(), stmt, column).Scan(&found); err != nil {
			t.Fatalf("information_schema を読めない: %v", err)
		}

		if found != 1 {
			t.Errorf("列 %q が chunks に無い", column)
		}
	}
}

// TestNoVectorIndexExists は ADR 0007 の手順を機械で守る。
//
// 🔴 このテストは「索引を最初から作らない」をコメントではなく検査にするためにある。
// ADR 0007 の成果物は pgvector を選んだことではなく「測ってから索引を入れた経路」で、
// 最初から張ると before の数字が取れず、なぜ入れたのかを数字で語れなくなる。
// 索引を入れるのは 10万件規模の p95 と recall を docs/benchmarks/ に記録した後で、
// そのときにこのテストを before/after の記録と一緒に更新すること。
func TestNoVectorIndexExists(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	const stmt = `SELECT count(*)
	              FROM pg_index i
	              JOIN pg_class c ON c.oid = i.indexrelid
	              JOIN pg_am am   ON am.oid = c.relam
	              WHERE am.amname IN ('hnsw', 'ivfflat')`

	var indexes int
	if err := ts.db.QueryRowContext(t.Context(), stmt).Scan(&indexes); err != nil {
		t.Fatalf("pg_index を読めない: %v", err)
	}

	if indexes != 0 {
		t.Errorf("ベクトル索引が %d 個ある。ADR 0007 は実測前の索引作成を禁じている", indexes)
	}
}

// TestNewRejectsDimensionMismatch は次元の食い違いを構築時に落とすことを確かめる。
//
// db に nil を渡しているのは、New が DB に触れてはならないことの表明でもある。
// 触れていれば、このテストは panic して気づける。
func TestNewRejectsDimensionMismatch(t *testing.T) {
	e := newFakeEmbedder("fake:768")
	e.dims = 768

	_, err := postgres.New(nil, e)
	if !errors.Is(err, postgres.ErrEmbedderDimensions()) {
		t.Fatalf("次元 768 の Embedder が受け入れられた: %v", err)
	}
}

// TestNewRejectsEmptyEmbedderID は空の識別子を構築時に落とすことを確かめる。
//
// 空のまま進むと embedder_id 列の CHECK 違反という分かりにくい形で落ちる。
func TestNewRejectsEmptyEmbedderID(t *testing.T) {
	_, err := postgres.New(nil, newFakeEmbedder(""))
	if !errors.Is(err, postgres.ErrEmbedderID()) {
		t.Fatalf("空の識別子が受け入れられた: %v", err)
	}
}
