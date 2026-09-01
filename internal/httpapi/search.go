package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/hideyukiMORI/nene-recall/internal/index"
)

// 検索要求の既定値と範囲。OpenAPI の SearchRequest と一致させること。
const (
	defaultSearchLimit = 10
	minSearchLimit     = 1
	maxSearchLimit     = 100
)

// searchFilters は任意の絞り込み。
type searchFilters struct {
	DocumentIDs []int64 `json:"document_ids"`
	SourceIDs   []int64 `json:"source_ids"`
}

// searchRequest は POST /v1/search の入力。
//
// OrgID・Limit・Alpha をポインタで受けるのは、「0 が来た」と「キーが無かった」を
// 区別するため。値型だとどちらも 0 になり、範囲外の 0 を既定値で上書きしてしまう。
type searchRequest struct {
	OrgID   *int64         `json:"org_id"`
	Query   string         `json:"query"`
	Limit   *int           `json:"limit"`
	Alpha   *float32       `json:"alpha"`
	Filters *searchFilters `json:"filters"`
}

// searchResult は応答1件。docs/openapi/openapi.yaml の SearchResult と対応する。
//
// 🔴 org_id のフィールドを作らない。応答に出さないのは「出す必要が無い」からでは
// なく、出せば org を跨いだ取り違えが利用者側に伝播しうるからである (ADR 0003)。
//
// VectorScore と LexicalScore を分けて返すのは、検索が外したときにベクトル側と
// 語彙側のどちらが原因かを切り分けるため。合成値だけでは alpha の調整が
// 当てずっぽうになる。
type searchResult struct {
	ChunkID      int64   `json:"chunk_id"`
	DocumentID   int64   `json:"document_id"`
	SourceID     int64   `json:"source_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Content      string  `json:"content"`
	PageNumber   *int    `json:"page_number"`
	SectionLabel *string `json:"section_label"`
	Score        float32 `json:"score"`
	VectorScore  float32 `json:"vector_score"`
	LexicalScore float32 `json:"lexical_score"`
}

// searchResponse は POST /v1/search の応答。
type searchResponse struct {
	Results    []searchResult `json:"results"`
	EmbedderID string         `json:"embedder_id"`
	TookMS     int64          `json:"took_ms"`
}

// toQuery は要求を検証して index.Query に変換する。
//
// defaultAlpha は設定由来の既定値。
// 🔴 この値が「調整済み」であるかのように扱わないこと。根拠はまだ無く、
// ADR 0009 の評価セットで最適値を決めるまでは暫定である（要件定義 Q-3）。
func (req searchRequest) toQuery(defaultAlpha float32) (index.Query, error) {
	orgID, err := requireBodyOrgID(req.OrgID)
	if err != nil {
		return index.Query{}, err
	}

	if req.Query == "" {
		return index.Query{}, invalid("query_required", "query must not be empty")
	}

	limit, err := resolveLimit(req.Limit)
	if err != nil {
		return index.Query{}, err
	}

	alpha, err := resolveAlpha(req.Alpha, defaultAlpha)
	if err != nil {
		return index.Query{}, err
	}

	filters := req.Filters
	if filters == nil {
		filters = &searchFilters{DocumentIDs: nil, SourceIDs: nil}
	}

	return index.Query{
		OrgID:       orgID,
		Text:        req.Query,
		Limit:       limit,
		Alpha:       alpha,
		DocumentIDs: filters.DocumentIDs,
		SourceIDs:   filters.SourceIDs,
	}, nil
}

// handleSearch はチャンクを検索する。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	query, err := req.toQuery(s.cfg.DefaultAlpha)
	if err != nil {
		s.writeRequestError(w, err)

		return
	}

	// took_ms は埋め込み API の往復を含む。検索1回の固定費は実測で 52〜85ms あり、
	// その大半が埋め込みなので、含めないと利用者から見た体感と食い違う。
	start := time.Now()

	results, err := s.deps.Searcher.Search(r.Context(), query)
	if err != nil {
		s.writeDependencyError(w, "search", err)

		return
	}

	took := time.Since(start).Milliseconds()

	// 🔴 query 本文をログに書かない。検索語は利用者の関心そのもので、
	// 運用ログに残す必要が無い（要件定義 §8）。同じ問い合わせを追跡できれば
	// 足りるので、ハッシュだけを記録する。
	s.log.Info("search completed",
		slog.String("query_hash", queryHash(query.Text)),
		slog.String("org_id", query.OrgID.String()),
		slog.Int("results", len(results)),
		slog.Int64("took_ms", took),
	)

	s.writeJSON(w, http.StatusOK, searchResponse{
		Results:    toSearchResults(results),
		EmbedderID: s.deps.EmbedderID,
		TookMS:     took,
	})
}

// toSearchResults は検索結果を応答 DTO に変換する。
func toSearchResults(results []index.Result) []searchResult {
	out := make([]searchResult, 0, len(results))

	for _, r := range results {
		out = append(out, searchResult{
			ChunkID:      r.Chunk.ID,
			DocumentID:   r.Chunk.DocumentID,
			SourceID:     r.Chunk.SourceID,
			ChunkIndex:   r.Chunk.ChunkIndex,
			Content:      r.Chunk.Content,
			PageNumber:   r.Chunk.PageNumber,
			SectionLabel: r.Chunk.SectionLabel,
			Score:        r.Score,
			VectorScore:  r.VectorScore,
			LexicalScore: r.LexicalScore,
		})
	}

	return out
}

// queryHash は検索語の追跡用ハッシュを返す。
//
// 先頭8バイトの hex に切るのは、ログの可読性のためと、全長を残す必要が
// 無いため。衝突しても困らない用途（同一問い合わせの追跡）にしか使わない。
func queryHash(text string) string {
	sum := sha256.Sum256([]byte(text))

	return hex.EncodeToString(sum[:8])
}

// resolveLimit は limit の既定値と範囲を解決する。
//
// nil は「指定なし」で既定値になる。0 は指定された値なので範囲外として弾く。
// 値型で受けるとこの2つが区別できず、範囲外の 0 が既定値に化ける。
func resolveLimit(raw *int) (int, error) {
	if raw == nil {
		return defaultSearchLimit, nil
	}

	if *raw < minSearchLimit || *raw > maxSearchLimit {
		return 0, invalid("limit_out_of_range", "limit must be between 1 and 100")
	}

	return *raw, nil
}

// resolveAlpha は alpha の既定値と範囲を解決する。
//
// 🔴 fallback は設定由来の暫定値である。根拠はまだ無く、ADR 0009 の評価セットで
// 最適値を決めるまでは暫定のままである（要件定義 Q-3）。「調整済み」として扱わない。
func resolveAlpha(raw *float32, fallback float32) (float32, error) {
	if raw == nil {
		return fallback, nil
	}

	if *raw < 0 || *raw > 1 {
		return 0, invalid("alpha_out_of_range", "alpha must be within [0,1]")
	}

	return *raw, nil
}
