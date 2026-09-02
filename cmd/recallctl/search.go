package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// previewRunes は表に出す本文の長さ（文字数）。
//
// 順位が正しいかを目で確かめられれば足りるので短く切る。全文が要るなら -json。
const previewRunes = 40

// searchFilters は OpenAPI の SearchFilters。
type searchFilters struct {
	DocumentIDs []int64 `json:"document_ids,omitempty"`
	SourceIDs   []int64 `json:"source_ids,omitempty"`
}

// searchRequest は POST /v1/search の本文。OpenAPI の SearchRequest と対応する。
//
// Limit・Alpha・Filters をポインタ + omitempty にするのは、未指定のときに
// キーごと送らないためである。サーバ側は「キーが無い」を既定値、「0 が来た」を
// 指定された値として扱う (internal/httpapi/search.go の resolveLimit / resolveAlpha)。
// CLI が 0 を送ると、未指定のつもりが limit_out_of_range になる。
type searchRequest struct {
	OrgID   int64          `json:"org_id"`
	Query   string         `json:"query"`
	Limit   *int           `json:"limit,omitempty"`
	Alpha   *float64       `json:"alpha,omitempty"`
	Filters *searchFilters `json:"filters,omitempty"`
}

// searchResult は応答1件。OpenAPI の SearchResult と対応する。
//
// 🔴 org_id のフィールドを作らない。サーバも返さない
// (docs/adr/0003-org-id-is-mandatory.md)。
type searchResult struct {
	ChunkID      int64   `json:"chunk_id"`
	DocumentID   int64   `json:"document_id"`
	SourceID     int64   `json:"source_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Content      string  `json:"content"`
	Score        float64 `json:"score"`
	VectorScore  float64 `json:"vector_score"`
	LexicalScore float64 `json:"lexical_score"`
}

// searchResponse は POST /v1/search の応答。
type searchResponse struct {
	Results    []searchResult `json:"results"`
	EmbedderID string         `json:"embedder_id"`
	TookMS     int64          `json:"took_ms"`
}

// searchOptions は search 固有の指定。ポインタの nil は「指定なし」を表す。
type searchOptions struct {
	limit     *int
	alpha     *float64
	documents []int64
	sources   []int64
}

// cmdSearch は POST /v1/search を叩く。
func cmdSearch(ctx context.Context, args []string, s streams) error {
	fs := newFlagSet("search", s.err)
	common := registerCommon(fs)

	var (
		limit              optionalInt
		alpha              optionalFloat
		documents, sources int64List
	)

	fs.Var(&limit, "limit", "返す件数 [1,100]（未指定ならサーバの既定に任せる）")
	fs.Var(&alpha, "alpha", "合成の重み [0,1]（未指定ならサーバの既定に任せる）")
	fs.Var(&documents, "document", "document_id で絞る（複数指定可）")
	fs.Var(&sources, "source", "source_id で絞る（複数指定可）")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("search: %w", err)
	}

	query := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("%w: search requires a query", errUsage)
	}

	opts, err := common.resolve()
	if err != nil {
		return err
	}

	announceOrg(s.err, opts)

	req := buildSearchRequest(opts.orgID, query, searchOptions{
		limit:     limit.ptr(),
		alpha:     alpha.ptr(),
		documents: documents,
		sources:   sources,
	})

	return sendSearch(ctx, opts, req, s)
}

// buildSearchRequest は検索要求を組み立てる。
func buildSearchRequest(orgID org.ID, query string, o searchOptions) searchRequest {
	var filters *searchFilters

	if len(o.documents) > 0 || len(o.sources) > 0 {
		filters = &searchFilters{DocumentIDs: o.documents, SourceIDs: o.sources}
	}

	return searchRequest{
		OrgID:   orgID.Int64(),
		Query:   query,
		Limit:   o.limit,
		Alpha:   o.alpha,
		Filters: filters,
	}
}

// sendSearch は検索要求を送って結果を書く。
func sendSearch(ctx context.Context, opts options, req searchRequest, s streams) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%w: encode search request: %w", errUsage, err)
	}

	raw, err := newClient(opts).do(ctx, request{
		method:         http.MethodPost,
		path:           "/v1/search",
		body:           body,
		tolerateStatus: 0,
	})
	if err != nil {
		return err
	}

	if opts.asJSON {
		return writeRaw(s.out, raw)
	}

	var resp searchResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("recallctl: decode search response: %w", err)
	}

	return renderSearch(s, resp)
}

// renderSearch は既定の表を書く。
//
// 診断（embedder_id・took_ms）を標準エラーへ回すのは、標準出力を結果だけに
// 保つためである。`recallctl search ... > results.txt` が表だけを残す。
func renderSearch(s streams, resp searchResponse) error {
	tw := tabwriter.NewWriter(s.out, 0, 0, 2, ' ', 0)
	out := newTextWriter(tw)

	out.printf("#\tscore\tvector\tlexical\tdoc\tsrc\tidx\tcontent\n")

	for i, r := range resp.Results {
		out.printf("%d\t%.4f\t%.4f\t%.4f\t%d\t%d\t%d\t%s\n",
			i+1, r.Score, r.VectorScore, r.LexicalScore,
			r.DocumentID, r.SourceID, r.ChunkIndex, preview(r.Content))
	}

	if err := out.Err(); err != nil {
		return err
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("recallctl: flush output: %w", err)
	}

	newTextWriter(s.err).printf("embedder_id=%s took_ms=%d\n", resp.EmbedderID, resp.TookMS)

	return nil
}

// preview は本文を表に収まる長さへ切り、改行を空白に潰す。
//
// rune 単位で切るのは、日本語の本文をバイト数で切ると文字の途中で切れるためである。
func preview(content string) string {
	flat := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(content)

	runes := []rune(flat)
	if len(runes) <= previewRunes {
		return flat
	}

	return string(runes[:previewRunes]) + "…"
}

// writeRaw はサーバ応答をそのまま標準出力へ出す（-json）。
//
// 整形しないのは、生の応答を jq へ渡す使い方を壊さないためである。
func writeRaw(w io.Writer, raw []byte) error {
	out := newTextWriter(w)
	out.write(string(raw))

	if !strings.HasSuffix(string(raw), "\n") {
		out.write("\n")
	}

	return out.Err()
}
