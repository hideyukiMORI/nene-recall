package httpapi_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/config"
	"github.com/hideyukiMORI/nene-recall/internal/httpapi"
)

// newTestServer は検査対象の http.Handler を組み立てる。
//
// Config の全フィールドを明示するのは exhaustruct の要求だが、テストにとっても
// 「サーバが何を見ているか」がこの1箇所で読めるという利点がある。
func newTestServer() http.Handler {
	cfg := config.Config{
		Addr:            ":0",
		Store:           config.StorePostgres,
		DatabaseURL:     "postgres://localhost/recall",
		DBPath:          "recall.db",
		EmbedProvider:   config.EmbedProviderOllama,
		EmbedModel:      "bge-m3",
		EmbedDimensions: 1024,
		OllamaBaseURL:   "http://localhost:11434",
		VoyageAPIKey:    "",
		DefaultAlpha:    0.7,
	}

	return httpapi.New(cfg, slog.New(slog.DiscardHandler)).Routes()
}

// TestOrgIDIsMandatory は ADR 0003 の受け入れ条件を固定する。
//
// org_id の欠落・0・負値・非数値がいずれも 400 になり、既定 org へ
// フォールバックしないことを保証する。ここが緩むと、あるテナントの検索が
// 別テナントの文書を返す。単一テナントで開発している限り症状が出ないため、
// テストで縛る以外に検知手段がない。
//
// 🔴 このテストのケースを減らす変更を入れないこと。増やすのはよい。
func TestOrgIDIsMandatory(t *testing.T) {
	tests := []struct {
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

	srv := newTestServer()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (org_id must never fall back to a default)",
					rec.Code, http.StatusBadRequest)
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
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/search",
		strings.NewReader(`{"org_id":1,"query":""}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHealthzReportsEmbedderID は、保存済みベクトルとの互換判定に使う識別子が
// 外から見えることを確認する（ADR 0005 の罠の検知手段）。
func TestHealthzReportsEmbedderID(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
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

	if body.EmbedderID != "bge-m3:1024" {
		t.Fatalf("embedder_id = %q, want %q", body.EmbedderID, "bge-m3:1024")
	}
}

// TestErrorResponseNeverCarriesConfig は、エラー応答に設定値が混ざらないことを確認する。
//
// config.Config は String() を実装していないので、構造体ごと出力すると
// VoyageAPIKey が漏れる。応答の形を型で固定してあることをここで縛る。
func TestErrorResponseNeverCarriesConfig(t *testing.T) {
	cfg := config.Config{
		Addr:            ":0",
		Store:           config.StorePostgres,
		DatabaseURL:     "postgres://user:pw@localhost/recall",
		DBPath:          "recall.db",
		EmbedProvider:   config.EmbedProviderVoyage,
		EmbedModel:      "bge-m3",
		EmbedDimensions: 1024,
		OllamaBaseURL:   "http://localhost:11434",
		VoyageAPIKey:    "super-secret-key",
		DefaultAlpha:    0.7,
	}
	srv := httpapi.New(cfg, slog.New(slog.DiscardHandler)).Routes()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/search",
		strings.NewReader(`{"query":"x"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	for _, secret := range []string{"super-secret-key", "pw"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("エラー応答に秘匿値 %q が含まれている: %s", secret, rec.Body.String())
		}
	}
}
