package postgres_test

import (
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// chunkSpec はテストで作るチャンクの指定。
//
// chunk.Chunk を直に書くと exhaustruct_v5 が全フィールドの明示を要求し、
// テストの本題でない項目（ID・PageNumber・SectionLabel）が毎回並ぶ。
// 指定と組み立てを分けて、テスト側には意味のある値だけを書く。
type chunkSpec struct {
	orgID      org.ID
	documentID int64
	sourceID   int64
	chunkIndex int
	content    string
}

// newChunk は指定から chunk.Chunk を組み立てる。
//
// ID は必ずゼロ値にする。Phase 1 は明示 id を受け付けない（施主決定）ので、
// テストが誤って id 付きのチャンクを作らないよう、ここで固定する。
func newChunk(spec chunkSpec) chunk.Chunk {
	return chunk.Chunk{
		ID:           0,
		OrgID:        spec.orgID,
		DocumentID:   spec.documentID,
		SourceID:     spec.sourceID,
		ChunkIndex:   spec.chunkIndex,
		Content:      spec.content,
		PageNumber:   nil,
		SectionLabel: nil,
	}
}

// mustOrgID は org.NewID を通して ID を作る。
//
// org.ID(1) という直接変換は CNF-001 が禁じている。テストでも例外にしない。
// 生成経路を1つに保つことが ADR 0003 の実装である。
func mustOrgID(t *testing.T, v int64) org.ID {
	t.Helper()

	id, err := org.NewID(v)
	if err != nil {
		t.Fatalf("org.NewID(%d): %v", v, err)
	}

	return id
}

// countChunks は org に属する行数を数える。
func countChunks(t *testing.T, ts *testStore, orgID org.ID) int {
	t.Helper()

	var n int

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM chunks WHERE org_id = $1`, orgID.Int64()).Scan(&n)
	if err != nil {
		t.Fatalf("行数を数えられない: %v", err)
	}

	return n
}

// querySpec はテストで組む検索要求の指定。
//
// index.Query は exhaustruct_v5 の対象なので全フィールドの明示が要る。
// テストの本題でない項目を毎回書かずに済ませるための入れ物。
type querySpec struct {
	orgID       org.ID
	text        string
	limit       int
	alpha       float32
	documentIDs []int64
	sourceIDs   []int64
}

// newQuery は指定から index.Query を組み立てる。
func newQuery(spec querySpec) index.Query {
	return index.Query{
		OrgID:       spec.orgID,
		Text:        spec.text,
		Limit:       spec.limit,
		Alpha:       spec.alpha,
		DocumentIDs: spec.documentIDs,
		SourceIDs:   spec.sourceIDs,
	}
}
