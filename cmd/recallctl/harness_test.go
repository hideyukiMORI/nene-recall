package main_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	main "github.com/hideyukiMORI/nene-recall/cmd/recallctl"
)

// 環境変数の名前。CLI 側は未公開の定数で持っているので、テストは文字列で書く。
// 名前を変えたらここも落ちる（テストが env を空にできなくなり、既定値の
// 検査が開発者の環境に引きずられる）。
const (
	envURL   = "RECALL_URL"
	envOrgID = "RECALL_ORG_ID"
	envToken = "RECALL_TOKEN"
)

// closedPortURL は誰も待ち受けていない宛先。接続失敗（終了コード 3）を作る。
//
// ポート 1 を使うのは、特権ポートなので普通のプロセスが偶然掴んでいないため。
const closedPortURL = "http://127.0.0.1:1"

// recordedRequest は偽サーバが受け取った1回の要求。
type recordedRequest struct {
	method string
	path   string
	query  string
	body   string
	// authorization は受け取った Authorization ヘッダ。付いていなければ空。
	//
	// 🔴 「送ったつもりで送っていない」は、認証を要求していないサーバに対しては
	// 一切症状が出ない。届いたヘッダを記録する以外に確かめる手段が無い。
	authorization string
}

// fakeServer は Recall サーバを偽装し、受け取った要求を記録する。
//
// 🔴 CLI の正しさは「何を送ったか」で決まる。応答の見た目だけを見るテストは、
// org_id を送り忘れても通ってしまう。
type fakeServer struct {
	status   int
	body     string
	requests []recordedRequest
}

// ServeHTTP は仕込まれた応答を返し、要求を記録する。
func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	f.requests = append(f.requests, recordedRequest{
		method:        r.Method,
		path:          r.URL.Path,
		query:         r.URL.RawQuery,
		body:          string(raw),
		authorization: r.Header.Get("Authorization"),
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)

	if _, err := io.WriteString(w, f.body); err != nil {
		return
	}
}

// last は最後に受け取った要求を返す。1件も無ければテストを落とす。
func (f *fakeServer) last(t *testing.T) recordedRequest {
	t.Helper()

	if len(f.requests) == 0 {
		t.Fatal("サーバに要求が届いていない")
	}

	return f.requests[len(f.requests)-1]
}

// startFake は偽サーバを立て、その URL を返す。
func startFake(t *testing.T, status int, body string) (*fakeServer, string) {
	t.Helper()

	fake := &fakeServer{status: status, body: body, requests: nil}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	return fake, srv.URL
}

// cliResult は recallctl の1回の実行結果。
type cliResult struct {
	code   int
	stdout string
	stderr string
}

// runCLI は recallctl を1回走らせる。
//
// 環境変数を毎回空にするのは、開発者の shell に RECALL_ORG_ID が
// 設定されていると「既定値が効く」テストが黙って別の経路を通るためである。
func runCLI(t *testing.T, stdin string, args ...string) cliResult {
	t.Helper()

	t.Setenv(envURL, "")
	t.Setenv(envOrgID, "")
	t.Setenv(envToken, "")

	return runCLIWithEnv(t, stdin, args...)
}

// runCLIWithEnv は環境変数を触らずに recallctl を走らせる。
//
// 環境変数からの解決を試すテストは、自分で t.Setenv してからこちらを呼ぶ。
func runCLIWithEnv(t *testing.T, stdin string, args ...string) cliResult {
	t.Helper()

	var stdout, stderr strings.Builder

	code := main.RunForTest(args, strings.NewReader(stdin), &stdout, &stderr)

	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}
