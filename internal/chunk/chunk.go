// Package chunk は検索対象の最小単位を表す。
//
// フィールド名は NeNe Corpus の Chunk エンティティに一対一で写せるよう揃えている
// (nene-corpus/src/Chunk/Chunk.php)。Phase 2 の統合時に変換層を薄く保つため。
package chunk

// Chunk は文書を分割した一片。
type Chunk struct {
	ID           int64   `json:"chunk_id"`
	OrgID        int64   `json:"-"` // 応答には出さない。分離はサーバ側の責務
	DocumentID   int64   `json:"document_id"`
	SourceID     int64   `json:"source_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Content      string  `json:"content"`
	PageNumber   *int    `json:"page_number"`
	SectionLabel *string `json:"section_label"`
}
