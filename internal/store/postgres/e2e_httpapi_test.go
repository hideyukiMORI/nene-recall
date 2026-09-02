package postgres_test

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

// 🔴 このファイルが internal/store/postgres の下にあるのは意図的である。
//
// 配線（HTTP → ストア → 実 Postgres）を通しで確かめたいが、httpapi は具体ストアを
// import できない（ARC-001・depguard の store-is-wired-only-in-cmd）。
// 逆向き——ストアのテストから httpapi を使う——なら層の向きに反せず、
// 実 DB のハーネス（newTestStore・偽 Embedder）もここに揃っている。
//
// 🔑 なぜ通しのテストが要るか: 偽 Searcher/Writer と偽 Ollama は、
// **互いに辻褄が合ったまま両方間違っていられる**。httpapi のテストは偽ストアに、
// ストアのテストは偽 Embedder に対して緑になるので、DTO の取り違えや配線の
// 抜けはどちらにも現れない。実 DB を通す自動テストはこれ1本だけである。

// newE2EServer は実ストアを繋いだ HTTP ハンドラを返す。
func newE2EServer(t *testing.T, ts *testStore) http.Handler {
	t.Helper()

	cfg := config.Config{
		Addr:            ":0",
		Store:           config.StorePostgres,
		DatabaseURL:     testDSN(testDBName()),
		DBPath:          "recall.db",
		Tokenizer:       config.TokenizerBigram,
		EmbedProvider:   config.EmbedProviderOllama,
		EmbedModel:      "fake",
		EmbedDimensions: 1024,
		OllamaBaseURL:   "http://localhost:11434",
		VoyageAPIKey:    "",
		DefaultAlpha:    1,
	}

	srv, err := httpapi.New(cfg, slog.New(slog.DiscardHandler), httpapi.Dependencies{
		Searcher:   ts.store,
		Writer:     ts.store,
		EmbedderID: "fake:1024",
		Probes:     []httpapi.Probe{{Name: "database", Check: ts.store.Ping}},
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}

	return srv.Routes()
}

// call は要求を1回投げて記録器を返す。
func call(t *testing.T, srv http.Handler, spec callSpec) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), spec.method, spec.path,
		strings.NewReader(spec.body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	return rec
}

// callSpec は1回の要求。
type callSpec struct {
	method string
	path   string
	body   string
}

// TestEndToEndIngestAndSearch は HTTP から実 Postgres までを通しで確かめる。
func TestEndToEndIngestAndSearch(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["HNSW 索引は近似最近傍探索を高速化する"] = 0
	e.angles["味噌汁の出汁は昆布と鰹節でとる"] = 1.5
	e.angles["ベクトルの索引を張ると検索は速くなるか"] = 0

	ts := newTestStore(t, e)
	srv := newE2EServer(t, ts)

	body := `{"org_id":1,"chunks":[
		{"document_id":1,"source_id":10,"chunk_index":0,"content":"HNSW 索引は近似最近傍探索を高速化する"},
		{"document_id":1,"source_id":10,"chunk_index":1,"content":"味噌汁の出汁は昆布と鰹節でとる"}
	]}`

	rec := call(t, srv, callSpec{method: http.MethodPost, path: "/v1/chunks", body: body})
	if rec.Code != http.StatusOK {
		t.Fatalf("投入 status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var accepted struct {
		Accepted int     `json:"accepted"`
		ChunkIDs []int64 `json:"chunk_ids"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("投入の応答が JSON ではない: %v", err)
	}

	if accepted.Accepted != 2 || len(accepted.ChunkIDs) != 2 {
		t.Fatalf("accepted = %d, chunk_ids = %v", accepted.Accepted, accepted.ChunkIDs)
	}

	assertSearchOrder(t, srv)
	assertDeleteBySource(t, srv)
}

// assertSearchOrder は検索が意味の近い順に返すことを確かめる。
func assertSearchOrder(t *testing.T, srv http.Handler) {
	t.Helper()

	rec := call(t, srv, callSpec{
		method: http.MethodPost,
		path:   "/v1/search",
		body:   `{"org_id":1,"query":"ベクトルの索引を張ると検索は速くなるか","limit":4}`,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("検索 status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// 🔴 応答に org_id が出ないことを通しでも確かめる（ADR 0003）。
	if strings.Contains(rec.Body.String(), "org_id") {
		t.Errorf("応答に org_id が含まれている: %s", rec.Body.String())
	}

	var got struct {
		Results []struct {
			Content     string  `json:"content"`
			VectorScore float32 `json:"vector_score"`
		} `json:"results"`
		EmbedderID string `json:"embedder_id"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("検索の応答が JSON ではない: %v", err)
	}

	if len(got.Results) != 2 {
		t.Fatalf("結果 = %d 件, want 2 (%s)", len(got.Results), rec.Body.String())
	}

	if !strings.HasPrefix(got.Results[0].Content, "HNSW") {
		t.Errorf("1位 = %q, want HNSW の文", got.Results[0].Content)
	}

	if got.Results[0].VectorScore <= got.Results[1].VectorScore {
		t.Errorf("スコアが降順でない: %v", got.Results)
	}

	if got.EmbedderID != "fake:1024" {
		t.Errorf("embedder_id = %q", got.EmbedderID)
	}
}

// assertDeleteBySource は source 単位の削除が通しで効くことを確かめる。
func assertDeleteBySource(t *testing.T, srv http.Handler) {
	t.Helper()

	rec := call(t, srv, callSpec{
		method: http.MethodDelete,
		path:   "/v1/sources/10/chunks?org_id=1",
		body:   "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("削除 status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Deleted int `json:"deleted"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("削除の応答が JSON ではない: %v", err)
	}

	if got.Deleted != 2 {
		t.Errorf("deleted = %d, want 2", got.Deleted)
	}
}

// TestEndToEndHealthzUsesRealDatabase は /healthz が実 DB を見ていることを確かめる。
func TestEndToEndHealthzUsesRealDatabase(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	srv := newE2EServer(t, ts)

	rec := call(t, srv, callSpec{method: http.MethodGet, path: "/healthz", body: ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Status string `json:"status"`
		} `json:"checks"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("応答が JSON ではない: %v", err)
	}

	if got.Status != "ok" || got.Checks["database"].Status != "ok" {
		t.Errorf("healthz = %+v, want ok", got)
	}
}
