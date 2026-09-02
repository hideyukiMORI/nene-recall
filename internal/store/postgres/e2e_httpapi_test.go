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
		APIToken:        "",
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

// TestEndToEndExternalIDRoundTrip は外部 id の往復を HTTP から実 DB まで通しで見る。
//
// 🔑 この観点は偽ストアでは確かめられない。置き換えが本当に起きたか（＝行が
// 増えていないか）は SQL の ON CONFLICT が担っており、偽 Writer は
// 「言われたとおりの id を返す」だけだからである。DTO の取り違え——要求の
// external_id を握り潰す、応答に載せ忘れる——も、両側の偽実装が辻褄を
// 合わせたまま隠しうる (docs/adr/0020-phase2-corpus-integration-contract.md)。
func TestEndToEndExternalIDRoundTrip(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["Corpus 由来の本文"] = 0
	e.angles["書き換えた本文"] = 0
	e.angles["問い"] = 0

	ts := newTestStore(t, e)
	srv := newE2EServer(t, ts)

	first := putWithExternalID(t, srv, "Corpus 由来の本文")
	second := putWithExternalID(t, srv, "書き換えた本文")

	if second.ChunkIDs[0] != first.ChunkIDs[0] {
		t.Errorf("🔴 chunk_id = %d, want %d（置き換えではなく新規採番になっている）",
			second.ChunkIDs[0], first.ChunkIDs[0])
	}

	if len(second.ExternalIDs) != 1 || second.ExternalIDs[0] == nil || *second.ExternalIDs[0] != 777 {
		t.Errorf("external_ids = %v, want [777]", second.ExternalIDs)
	}

	assertSearchCarriesExternalID(t, srv)
	assertDeleteByDocument(t, srv)
}

// putWithExternalID は external_id=777 の1件を投入して応答を返す。
func putWithExternalID(t *testing.T, srv http.Handler, content string) putResponse {
	t.Helper()

	body := `{"org_id":1,"chunks":[{"external_id":777,"document_id":55,"source_id":10,` +
		`"chunk_index":0,"content":"` + content + `"}]}`

	rec := call(t, srv, callSpec{method: http.MethodPost, path: "/v1/chunks", body: body})
	if rec.Code != http.StatusOK {
		t.Fatalf("投入 status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got putResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("投入の応答が JSON ではない: %v", err)
	}

	if got.Accepted != 1 || len(got.ChunkIDs) != 1 {
		t.Fatalf("accepted = %d, chunk_ids = %v", got.Accepted, got.ChunkIDs)
	}

	return got
}

// putResponse は POST /v1/chunks の応答。
type putResponse struct {
	Accepted    int      `json:"accepted"`
	ChunkIDs    []int64  `json:"chunk_ids"`
	ExternalIDs []*int64 `json:"external_ids"`
}

// assertSearchCarriesExternalID は検索結果に外部 id が載ることを通しで確かめる。
//
// 置き換えが本当に1行に収まったことも、ここで件数として見える。
func assertSearchCarriesExternalID(t *testing.T, srv http.Handler) {
	t.Helper()

	rec := call(t, srv, callSpec{
		method: http.MethodPost,
		path:   "/v1/search",
		body:   `{"org_id":1,"query":"問い","limit":10}`,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("検索 status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Results []struct {
			ExternalID *int64 `json:"external_id"`
			Content    string `json:"content"`
		} `json:"results"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("検索の応答が JSON ではない: %v", err)
	}

	if len(got.Results) != 1 {
		t.Fatalf("🔴 結果 = %d 件, want 1（置き換えのはずが行が増えている）: %s",
			len(got.Results), rec.Body.String())
	}

	if got.Results[0].ExternalID == nil || *got.Results[0].ExternalID != 777 {
		t.Errorf("external_id = %v, want 777", got.Results[0].ExternalID)
	}

	if got.Results[0].Content != "書き換えた本文" {
		t.Errorf("content = %q, want 書き換えた本文（更新されていない）", got.Results[0].Content)
	}
}

// assertDeleteByDocument は document 単位の削除が通しで効くことを確かめる。
func assertDeleteByDocument(t *testing.T, srv http.Handler) {
	t.Helper()

	rec := call(t, srv, callSpec{
		method: http.MethodDelete,
		path:   "/v1/documents/55/chunks?org_id=1",
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

	if got.Deleted != 1 {
		t.Errorf("deleted = %d, want 1", got.Deleted)
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
