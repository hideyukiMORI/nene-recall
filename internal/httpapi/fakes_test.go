package httpapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/config"
	"github.com/hideyukiMORI/nene-recall/internal/httpapi"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// fakeSearcher は index.Searcher の偽実装。
type fakeSearcher struct {
	results []index.Result
	err     error
	// lastQuery は最後に受け取った要求。境界の変換を確かめるために持つ。
	lastQuery index.Query
}

// Search は仕込まれた結果か error を返す。
func (f *fakeSearcher) Search(_ context.Context, q index.Query) ([]index.Result, error) {
	f.lastQuery = q

	return f.results, f.err
}

// fakeWriter は index.Writer の偽実装。
type fakeWriter struct {
	ids        []int64
	deleted    int
	err        error
	lastChunks []chunk.Chunk
	lastOrgID  org.ID
}

// Put は仕込まれた id か error を返す。
func (f *fakeWriter) Put(_ context.Context, orgID org.ID, chunks []chunk.Chunk) ([]int64, error) {
	f.lastOrgID = orgID
	f.lastChunks = chunks

	return f.ids, f.err
}

// Delete は仕込まれた error を返す。
func (f *fakeWriter) Delete(_ context.Context, orgID org.ID, _ int64) error {
	f.lastOrgID = orgID

	return f.err
}

// DeleteBySource は仕込まれた件数か error を返す。
func (f *fakeWriter) DeleteBySource(_ context.Context, orgID org.ID, _ int64) (int, error) {
	f.lastOrgID = orgID

	return f.deleted, f.err
}

// DeleteByDocument は仕込まれた件数か error を返す。
func (f *fakeWriter) DeleteByDocument(_ context.Context, orgID org.ID, _ int64) (int, error) {
	f.lastOrgID = orgID

	return f.deleted, f.err
}

// newFakes は正常応答を返す偽依存を作る。
//
// new() を使うのは、index.Query が exhaustruct の対象で、composite literal に
// すると全フィールドの明示を要求されるためである。ここで欲しいのはゼロ値であって、
// 「まだ呼ばれていない」ことを表すのに個々のフィールドの値は意味を持たない。
func newFakes() (*fakeSearcher, *fakeWriter) {
	return new(fakeSearcher), new(fakeWriter)
}

// testConfig は検査用の設定。
//
// 全フィールドを明示するのは exhaustruct の要求だが、テストにとっても
// 「サーバが何を見ているか」がこの1箇所で読めるという利点がある。
func testConfig() config.Config {
	return config.Config{
		Addr:            ":0",
		Store:           config.StorePostgres,
		DatabaseURL:     "postgres://localhost/recall",
		DBPath:          "recall.db",
		Tokenizer:       config.TokenizerBigram,
		EmbedProvider:   config.EmbedProviderOllama,
		EmbedModel:      "bge-m3",
		EmbedDimensions: 1024,
		OllamaBaseURL:   "http://localhost:11434",
		VoyageAPIKey:    "",
		APIToken:        "",
		DefaultAlpha:    0.8,
	}
}

// authHeader は Authorization を1つ載せた要求を投げる。
//
// header が空文字なら Authorization を付けない。「ヘッダが無い」と
// 「空のヘッダ」を分けて試せるようにするためである。
func doWithAuth(t *testing.T, srv http.Handler, r request, header string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), r.method, r.path, strings.NewReader(r.body))
	if header != "" {
		req.Header.Set("Authorization", header)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	return rec
}

// newDeps は指定の偽依存から Dependencies を作る。probe は常に正常。
func newDeps(searcher index.Searcher, writer index.Writer) httpapi.Dependencies {
	return httpapi.Dependencies{
		Searcher:   searcher,
		Writer:     writer,
		EmbedderID: "bge-m3:1024",
		Probes:     nil,
	}
}

// newTestServerWith は指定の依存でハンドラを組み立てる。
func newTestServerWith(t *testing.T, cfg config.Config, deps httpapi.Dependencies) http.Handler {
	t.Helper()

	srv, err := httpapi.New(cfg, slog.New(slog.DiscardHandler), deps)
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}

	return srv.Routes()
}

// newTestServer は既定の偽依存でハンドラを組み立てる。
func newTestServer(t *testing.T) http.Handler {
	t.Helper()

	searcher, writer := newFakes()

	return newTestServerWith(t, testConfig(), newDeps(searcher, writer))
}

// request は1回の要求。引数を4つ以下に保つための入れ物（GO-011）。
type request struct {
	method string
	path   string
	body   string
}

// do は要求を1回投げて記録器を返す。
func do(t *testing.T, srv http.Handler, r request) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), r.method, r.path, strings.NewReader(r.body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	return rec
}

// post は POST を1回投げる。
func post(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, srv, request{method: http.MethodPost, path: path, body: body})
}

// get は GET を1回投げる。
func get(t *testing.T, srv http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, srv, request{method: http.MethodGet, path: path, body: ""})
}

// del は DELETE を1回投げる。
func del(t *testing.T, srv http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, srv, request{method: http.MethodDelete, path: path, body: ""})
}

// errorCode は応答の error.code を取り出す。
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("応答が JSON ではない: %v (%s)", err, rec.Body.String())
	}

	return body.Error.Code
}

// mustOrg は org.NewID を通して ID を作る。
//
// org.ID(1) という直接変換は CNF-001 が禁じている。テストでも例外にしない。
func mustOrg(t *testing.T, v int64) org.ID {
	t.Helper()

	id, err := org.NewID(v)
	if err != nil {
		t.Fatalf("org.NewID(%d): %v", v, err)
	}

	return id
}

// discardLogger は出力を捨てるロガー。
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// contains は部分文字列を含むかを返す。
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
