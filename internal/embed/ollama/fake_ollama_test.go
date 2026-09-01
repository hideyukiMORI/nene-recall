package ollama_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hideyukiMORI/nene-recall/internal/embed/ollama"
)

// testDimensions はテストで使う次元数。
//
// 本物は 1024 だが、期待値を目で読める大きさにする。次元数そのものは
// Config で受け取る値なので、小さくしても検証したい性質は変わらない。
const testDimensions = 4

// recordingOllama は要求を記録しつつ指定された応答を返す偽 Ollama。
//
// 実プロセスに依存させない。ここで確かめたいのはクライアントの振る舞い
// （分割・順序・正規化・失敗の分類）であって Ollama の挙動ではない。
type recordingOllama struct {
	// batches は /api/embed が受け取った input の並び。分割の検証に使う。
	batches [][]string
	// shows は /api/show を呼ばれた回数。
	shows int
	// respond は入力から応答ベクトルを作る。
	respond func(inputs []string) [][]float32
	// status は返す HTTP ステータス。0 なら 200。
	status int
	// rawBody は非空ならそのまま返す（壊れた応答の再現に使う）。
	rawBody string
	// version は /api/version が返すランタイムの版。
	version string
	// tags は /api/tags が返すモデル一覧。
	tags []fakeTag
}

// fakeTag は /api/tags の要素。実応答は details や size も持つが、
// クライアントが見るのは名前と digest だけなので、そこだけを模す。
type fakeTag struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Digest string `json:"digest"`
}

// newRecordingOllama は正常応答を返す偽サーバの状態を作る。
func newRecordingOllama() *recordingOllama {
	return &recordingOllama{
		batches: nil,
		shows:   0,
		respond: encodeIndexVectors,
		status:  0,
		rawBody: "",
		version: "0.33.2",
		tags: []fakeTag{
			{Name: "other:latest", Model: "other:latest", Digest: strings.Repeat("a", 64)},
			{Name: "fake-model:latest", Model: "fake-model:latest", Digest: strings.Repeat("b", 64)},
		},
	}
}

// ServeHTTP は /api/embed・/api/show・/api/version・/api/tags に答える。
func (o *recordingOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/api/show"):
		o.shows++
		// 🔴 実物の /api/show は digest を返さない（2026-09-01 実測）。
		// 偽サーバでも返さないこと。返すと、digest を /api/show から取る
		// 実装がテストだけ通ってしまう。
		o.write(w, map[string]string{"model": "fake"})
	case strings.HasSuffix(r.URL.Path, "/api/version"):
		o.write(w, map[string]string{"version": o.version})
	case strings.HasSuffix(r.URL.Path, "/api/tags"):
		o.write(w, map[string][]fakeTag{"models": o.tags})
	default:
		o.serveEmbed(w, r)
	}
}

// serveEmbed は /api/embed に答える。
func (o *recordingOllama) serveEmbed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)

		return
	}

	o.batches = append(o.batches, req.Input)
	o.write(w, map[string]any{"embeddings": o.respond(req.Input)})
}

// write は status と rawBody の指定を反映して応答する。
func (o *recordingOllama) write(w http.ResponseWriter, payload any) {
	if o.status != 0 {
		w.WriteHeader(o.status)
	}

	if o.rawBody != "" {
		if _, err := w.Write([]byte(o.rawBody)); err != nil {
			return
		}

		return
	}

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		return
	}
}

// encodeIndexVectors は入力の並び順をベクトルの比として埋め込む。
//
// 🔑 正規化を通しても消えない形にするのが要点である。長さだけを変えても
// 正規化で消えてしまうので、2成分の比で順序を表す。戻ってきたベクトルの
// v[1]/v[0] が元の番号に戻れば、分割と結合で順序が崩れていないと言える。
func encodeIndexVectors(inputs []string) [][]float32 {
	out := make([][]float32, 0, len(inputs))

	for _, text := range inputs {
		n, err := strconv.Atoi(strings.TrimPrefix(text, "t"))
		if err != nil {
			n = 0
		}

		out = append(out, []float32{1, float32(n + 1), 0, 0})
	}

	return out
}

// decodeIndex は encodeIndexVectors が埋め込んだ番号を取り出す。
func decodeIndex(t *testing.T, v []float32) int {
	t.Helper()

	if v[0] == 0 {
		t.Fatalf("先頭成分が 0 で比を取れない: %v", v)
	}

	return int(float64(v[1])/float64(v[0]) + 0.5)
}

// startFakeOllama は偽サーバを起動し、それを向いた Client を返す。
func startFakeOllama(t *testing.T, server *recordingOllama, batchSize int) *ollama.Client {
	t.Helper()

	srv := httptest.NewServer(server)
	t.Cleanup(srv.Close)

	return newClient(t, srv.URL, batchSize, srv.Client())
}

// newClient は Config を組み立てて Client を作る。
func newClient(t *testing.T, baseURL string, batchSize int, httpClient *http.Client) *ollama.Client {
	t.Helper()

	client, err := ollama.New(ollama.Config{
		BaseURL:    baseURL,
		Model:      "fake-model",
		Dimensions: testDimensions,
		BatchSize:  batchSize,
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("ollama.New: %v", err)
	}

	return client
}

// numberedTexts は "t0".."t<n-1>" を返す。
func numberedTexts(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "t"+strconv.Itoa(i))
	}

	return out
}

// stubHTTPClient は実際には使われない HTTP クライアントを返す。
//
// exhaustruct が http.Client の全フィールドを要求するのは、配線点で Timeout を
// 決め忘れないためである（ゼロ値の Timeout は「無期限」を意味する）。
// テストでは要求そのものを緩めず、明示した1つをここで使い回す。
func stubHTTPClient() *http.Client {
	return &http.Client{
		Transport:     nil, // 既定の Transport を使う
		CheckRedirect: nil, // 既定のリダイレクト方針
		Jar:           nil, // Cookie を持たない
		Timeout:       5 * time.Second,
	}
}
