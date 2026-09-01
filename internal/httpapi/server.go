// Package httpapi は HTTP のルーティングと入出力の変換を担う。
package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/hideyukiMORI/nene-recall/internal/config"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// Server は HTTP ハンドラの集合。
type Server struct {
	cfg config.Config
	log *slog.Logger
}

// New は Server を組み立てる。
func New(cfg config.Config, log *slog.Logger) *Server {
	return &Server{cfg: cfg, log: log}
}

// Routes は http.Handler を返す。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("POST /v1/search", s.handleSearch)
	mux.HandleFunc("POST /v1/chunks", s.handlePutChunks)
	mux.HandleFunc("DELETE /v1/chunks/{chunk_id}", s.handleDeleteChunk)
	mux.HandleFunc("DELETE /v1/sources/{source_id}/chunks", s.handleDeleteBySource)

	return mux
}

// apiError は Error スキーマの中身。docs/openapi/openapi.yaml と対応する。
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// errorResponse は全エラー応答の外枠。
//
// 🔴 ここに設定値や内部状態を足さないこと。config.Config を丸ごと入れると
// VoyageAPIKey が応答に出る。エラー応答に載せてよいのは code と message だけ。
type errorResponse struct {
	Error apiError `json:"error"`
}

// healthCheck は依存1件の状態。
type healthCheck struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// healthResponse は GET /healthz の応答。
//
// map[string]any ではなく型で書くのは、応答の形が OpenAPI と一致していることを
// コンパイラに見張らせるため (GO-006)。
type healthResponse struct {
	Status     string                 `json:"status"`
	Checks     map[string]healthCheck `json:"checks"`
	EmbedderID string                 `json:"embedder_id"`
}

// writeJSON は JSON 応答を書く。
//
// body が any なのは JSON 符号化の境界だからで、この例外は encoder ヘルパに閉じる
// (GO-006 の適用外はここだけ)。符号化に失敗してもヘッダは送信済みなので、
// 復帰はできない。握り潰さずに記録だけ残す。
func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log.Error("failed to encode response body",
			slog.Int("status", status),
			slog.Any("error", err),
		)
	}
}

// writeError はエラー応答を書く。
func (s *Server) writeError(w http.ResponseWriter, status int, code, msg string) {
	s.writeJSON(w, status, errorResponse{Error: apiError{Code: code, Message: msg}})
}

// writeOrgIDError は org.ID の解決に失敗したことを 400 で返す。
//
// 🔴 ここで「既定の org を使う」分岐を足さないこと。単一テナントで開発している限り
// 症状が出ないまま、別テナントの文書を返す実装になる (ADR 0003)。
func (s *Server) writeOrgIDError(w http.ResponseWriter, err error) {
	s.writeError(w, http.StatusBadRequest, "org_id_invalid", err.Error())
}

// requireOrgID はクエリ文字列から org.ID を取り出す。
//
// 生成経路を org.ParseID の1本に絞っているので、「空なら既定値」という分岐は
// このパッケージからは書けない。
func (s *Server) requireOrgID(w http.ResponseWriter, r *http.Request) (org.ID, bool) {
	id, err := org.ParseID(r.URL.Query().Get("org_id"))
	if err != nil {
		s.writeOrgIDError(w, err)

		return 0, false
	}

	return id, true
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, healthResponse{
		Status:     "ok",
		Checks:     map[string]healthCheck{},
		EmbedderID: s.cfg.EmbedderID(),
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// searchRequest は POST /v1/search の入力。
//
// OrgID をポインタで受けるのは、「0 が来た」と「キーが無かった」を区別するため。
// 値型だとどちらも 0 になり、欠落を検知できない。
type searchRequest struct {
	OrgID *int64   `json:"org_id"`
	Query string   `json:"query"`
	Limit int      `json:"limit"`
	Alpha *float32 `json:"alpha"`
}

// orgID は本文の org_id を検証済みの org.ID に変換する。
func (req searchRequest) orgID() (org.ID, error) {
	if req.OrgID == nil {
		return 0, fmt.Errorf("%w: org_id is required", org.ErrInvalid)
	}

	id, err := org.NewID(*req.OrgID)
	if err != nil {
		return 0, fmt.Errorf("search request: %w", err)
	}

	return id, nil
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")

		return
	}

	if _, err := req.orgID(); err != nil {
		s.writeError(w, http.StatusBadRequest, "org_id_required",
			"org_id is required and must be a positive integer")

		return
	}

	if req.Query == "" {
		s.writeError(w, http.StatusBadRequest, "query_required", "query must not be empty")

		return
	}

	s.writeError(w, http.StatusNotImplemented, "not_implemented", "search is not implemented yet")
}

// putChunksRequest は POST /v1/chunks の入力の、org_id 部分だけを見るための型。
type putChunksRequest struct {
	OrgID *int64 `json:"org_id"`
}

func (s *Server) handlePutChunks(w http.ResponseWriter, r *http.Request) {
	var req putChunksRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")

		return
	}

	if req.OrgID == nil {
		s.writeError(w, http.StatusBadRequest, "org_id_required",
			"org_id is required and must be a positive integer")

		return
	}

	if _, err := org.NewID(*req.OrgID); err != nil {
		s.writeError(w, http.StatusBadRequest, "org_id_required",
			"org_id is required and must be a positive integer")

		return
	}

	s.writeError(w, http.StatusNotImplemented, "not_implemented", "chunk ingestion is not implemented yet")
}

func (s *Server) handleDeleteChunk(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOrgID(w, r); !ok {
		return
	}

	s.writeError(w, http.StatusNotImplemented, "not_implemented", "chunk deletion is not implemented yet")
}

func (s *Server) handleDeleteBySource(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOrgID(w, r); !ok {
		return
	}

	s.writeError(w, http.StatusNotImplemented, "not_implemented", "bulk deletion is not implemented yet")
}
