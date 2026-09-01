// Package httpapi は HTTP のルーティングと入出力の変換を担う。
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/hideyukiMORI/nene-recall/internal/config"
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

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

// requireOrgID はクエリ文字列から org_id を取り出す。
//
// 欠落・空・0・非数値はいずれも 400 とし、既定値へフォールバックしない。
// ここを緩めると、あるテナントの検索が別テナントの文書を返す
// (docs/adr/0003-org-id-is-mandatory.md)。
func requireOrgID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.URL.Query().Get("org_id")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "org_id_required", "org_id is required")
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "org_id_invalid", "org_id must be a positive integer")
		return 0, false
	}
	return id, true
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"checks":      map[string]any{},
		"embedder_id": s.cfg.EmbedderID(),
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

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")
		return
	}
	if req.OrgID == nil || *req.OrgID < 1 {
		writeError(w, http.StatusBadRequest, "org_id_required", "org_id is required and must be a positive integer")
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query_required", "query must not be empty")
		return
	}
	writeError(w, http.StatusNotImplemented, "not_implemented", "search is not implemented yet")
}

func (s *Server) handlePutChunks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrgID *int64 `json:"org_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")
		return
	}
	if req.OrgID == nil || *req.OrgID < 1 {
		writeError(w, http.StatusBadRequest, "org_id_required", "org_id is required and must be a positive integer")
		return
	}
	writeError(w, http.StatusNotImplemented, "not_implemented", "chunk ingestion is not implemented yet")
}

func (s *Server) handleDeleteChunk(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireOrgID(w, r); !ok {
		return
	}
	writeError(w, http.StatusNotImplemented, "not_implemented", "chunk deletion is not implemented yet")
}

func (s *Server) handleDeleteBySource(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireOrgID(w, r); !ok {
		return
	}
	writeError(w, http.StatusNotImplemented, "not_implemented", "bulk deletion is not implemented yet")
}
