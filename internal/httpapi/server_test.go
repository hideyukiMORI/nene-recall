package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/config"
)

func newTestServer() http.Handler {
	cfg := config.Config{
		Addr:            ":0",
		Embedder:        "voyage",
		EmbedModel:      "voyage-4",
		EmbedDimensions: 1024,
		DefaultAlpha:    0.7,
	}
	return New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes()
}

// TestOrgIDIsMandatory は ADR 0003 の受け入れ条件を固定する。
//
// org_id の欠落・0・負値・非数値がいずれも 400 になり、既定 org へ
// フォールバックしないことを保証する。ここが緩むと、あるテナントの検索が
// 別テナントの文書を返す。単一テナントで開発している限り症状が出ないため、
// テストで縛る以外に検知手段がない。
func TestOrgIDIsMandatory(t *testing.T) {
	srv := newTestServer()

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"search: org_id 欠落", http.MethodPost, "/v1/search", `{"query":"x"}`},
		{"search: org_id が null", http.MethodPost, "/v1/search", `{"org_id":null,"query":"x"}`},
		{"search: org_id が 0", http.MethodPost, "/v1/search", `{"org_id":0,"query":"x"}`},
		{"search: org_id が負", http.MethodPost, "/v1/search", `{"org_id":-1,"query":"x"}`},
		{"chunks: org_id 欠落", http.MethodPost, "/v1/chunks", `{"chunks":[]}`},
		{"chunks: org_id が 0", http.MethodPost, "/v1/chunks", `{"org_id":0}`},
		{"delete chunk: org_id 欠落", http.MethodDelete, "/v1/chunks/1", ""},
		{"delete chunk: org_id が 0", http.MethodDelete, "/v1/chunks/1?org_id=0", ""},
		{"delete chunk: org_id が非数値", http.MethodDelete, "/v1/chunks/1?org_id=abc", ""},
		{"delete by source: org_id 欠落", http.MethodDelete, "/v1/sources/1/chunks", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (org_id must never fall back to a default)", rec.Code, http.StatusBadRequest)
			}

			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not valid JSON: %v", err)
			}
			if !strings.HasPrefix(body.Error.Code, "org_id_") {
				t.Fatalf("error code = %q, want an org_id_* code", body.Error.Code)
			}
		})
	}
}

// TestSearchRejectsEmptyQuery は org_id が正しくても空クエリを弾くことを確認する。
func TestSearchRejectsEmptyQuery(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(`{"org_id":1,"query":""}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHealthzReportsEmbedderID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Status     string `json:"status"`
		EmbedderID string `json:"embedder_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.EmbedderID != "voyage-4:1024" {
		t.Fatalf("embedder_id = %q, want %q", body.EmbedderID, "voyage-4:1024")
	}
}
