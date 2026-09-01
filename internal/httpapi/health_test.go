package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/httpapi"
)

// errProbe は probe の失敗を表すテスト用の sentinel。
var errProbe = errors.New("probe failed")

// errLeakyProbe は DSN を含む probe の失敗。応答へ漏れないことを見るために使う。
var errLeakyProbe = errors.New("failed to connect to `user=recall database=recall` at 10.0.0.1:5433")

// probes は名前と成否から Probe の列を作る。
func probes(down ...string) []httpapi.Probe {
	failing := make(map[string]bool, len(down))
	for _, name := range down {
		failing[name] = true
	}

	out := make([]httpapi.Probe, 0, 2)

	for _, name := range []string{"database", "embedder"} {
		fails := failing[name]
		out = append(out, httpapi.Probe{
			Name: name,
			Check: func(_ context.Context) error {
				if fails {
					return errProbe
				}

				return nil
			},
		})
	}

	return out
}

// TestHealthzReportsPerDependencyStatus は健全性の段階を確かめる。
//
// 🔴 degraded を独立した状態にしているのは、運用者が復旧の順序を決められる
// ようにするためである。DB が生きていれば削除は動くので、埋め込みだけが
// 落ちている状態は「全部だめ」とは区別する価値がある。
func TestHealthzReportsPerDependencyStatus(t *testing.T) {
	cases := []struct {
		name       string
		down       []string
		wantStatus string
		wantCode   int
	}{
		{name: "全て正常", down: nil, wantStatus: "ok", wantCode: http.StatusOK},
		{
			name: "埋め込みだけ落ちている", down: []string{"embedder"},
			wantStatus: "degraded", wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "DB が落ちている", down: []string{"database"},
			wantStatus: "down", wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "両方落ちている", down: []string{"database", "embedder"},
			wantStatus: "down", wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertHealth(t, tc.down, tc.wantStatus, tc.wantCode)
		})
	}
}

// assertHealth は指定の probe を落とした状態で /healthz の応答を確かめる。
func assertHealth(t *testing.T, down []string, wantStatus string, wantCode int) {
	t.Helper()

	searcher, writer := newFakes()
	deps := newDeps(searcher, writer)
	deps.Probes = probes(down...)
	srv := newTestServerWith(t, testConfig(), deps)

	rec := get(t, srv, "/healthz")
	if rec.Code != wantCode {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, wantCode, rec.Body.String())
	}

	var body struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("応答が JSON ではない: %v", err)
	}

	if body.Status != wantStatus {
		t.Errorf("status = %q, want %q", body.Status, wantStatus)
	}

	for _, name := range down {
		if body.Checks[name].Status != "down" {
			t.Errorf("checks[%q].status = %q, want down", name, body.Checks[name].Status)
		}
	}
}

// TestHealthzDoesNotLeakProbeDetails は probe の error 文字列を応答に出さないことを見る。
//
// 🔴 /healthz は認証を要求しない口である。Postgres の接続失敗メッセージには
// DSN（ユーザ名・データベース名・ホスト）が、埋め込みの失敗には接続先 URL が
// 含まれる。どの依存が落ちているかは checks のキーで分かるので、診断に必要な
// 情報は失われない。
func TestHealthzDoesNotLeakProbeDetails(t *testing.T) {
	searcher, writer := newFakes()
	deps := newDeps(searcher, writer)
	deps.Probes = []httpapi.Probe{{
		Name: "database",
		Check: func(_ context.Context) error {
			return errLeakyProbe
		},
	}}
	srv := newTestServerWith(t, testConfig(), deps)

	rec := get(t, srv, "/healthz")

	for _, secret := range []string{"user=recall", "10.0.0.1"} {
		if contains(rec.Body.String(), secret) {
			t.Errorf("🔴 /healthz の応答に %q が漏れている: %s", secret, rec.Body.String())
		}
	}
}

// TestReadyzMirrorsHealthz は2つの口が同じ基準で答えることを確かめる。
//
// 別々の基準にすると「readyz は通るのに検索が 503」という説明のつかない
// 状態が生まれる。
func TestReadyzMirrorsHealthz(t *testing.T) {
	searcher, writer := newFakes()
	deps := newDeps(searcher, writer)
	deps.Probes = probes("embedder")
	srv := newTestServerWith(t, testConfig(), deps)

	if rec := get(t, srv, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz status = %d, want 503", rec.Code)
	}

	healthy := newTestServerWith(t, testConfig(), newDeps(searcher, writer))
	if rec := get(t, healthy, "/readyz"); rec.Code != http.StatusOK {
		t.Errorf("正常時の readyz status = %d, want 200", rec.Code)
	}
}

// TestNewRejectsMissingDependencies は配線の取りこぼしを構築時に落とすことを見る。
func TestNewRejectsMissingDependencies(t *testing.T) {
	searcher, writer := newFakes()

	cases := map[string]func(d *httpapi.Dependencies){
		"Searcher が nil": func(d *httpapi.Dependencies) { d.Searcher = nil },
		"Writer が nil":   func(d *httpapi.Dependencies) { d.Writer = nil },
		"EmbedderID が空":  func(d *httpapi.Dependencies) { d.EmbedderID = "" },
	}

	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			deps := newDeps(searcher, writer)
			breakIt(&deps)

			if _, err := httpapi.New(testConfig(), discardLogger(), deps); err == nil {
				t.Errorf("%s が受け入れられた", name)
			}
		})
	}
}
