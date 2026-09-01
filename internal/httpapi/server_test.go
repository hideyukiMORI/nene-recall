package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/config"
)

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

	srv := newTestServer(t)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, srv, request{method: tc.method, path: tc.path, body: tc.body})

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (org_id must never fall back to a default)",
					rec.Code, http.StatusBadRequest)
			}

			if code := errorCode(t, rec); !strings.HasPrefix(code, "org_id_") {
				t.Fatalf("error code = %q, want an org_id_* code", code)
			}
		})
	}
}

// TestSearchRejectsEmptyQuery は org_id が正しくても空クエリを弾くことを確認する。
func TestSearchRejectsEmptyQuery(t *testing.T) {
	srv := newTestServer(t)
	rec := post(t, srv, "/v1/search", `{"org_id":1,"query":""}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHealthzReportsEmbedderID は、保存済みベクトルとの互換判定に使う識別子が
// 外から見えることを確認する（ADR 0005 の罠の検知手段）。
func TestHealthzReportsEmbedderID(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/healthz")

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
	cfg := testConfig()
	cfg.DatabaseURL = "postgres://user:pw@localhost/recall"
	cfg.EmbedProvider = config.EmbedProviderVoyage
	cfg.VoyageAPIKey = "super-secret-key"

	searcher, writer := newFakes()
	srv := newTestServerWith(t, cfg, newDeps(searcher, writer))

	rec := post(t, srv, "/v1/search", `{"query":"x"}`)

	for _, secret := range []string{"super-secret-key", "pw"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("エラー応答に秘匿値 %q が含まれている: %s", secret, rec.Body.String())
		}
	}
}
