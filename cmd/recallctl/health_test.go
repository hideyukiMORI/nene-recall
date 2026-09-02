package main_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestHealthReportsOK は全依存が生きているときの出力と終了コードを見る。
func TestHealthReportsOK(t *testing.T) {
	const body = `{"status":"ok","checks":{"database":{"status":"ok"},` +
		`"embedder":{"status":"ok"}},"embedder_id":"bge-m3:1024"}`

	fake, url := startFake(t, http.StatusOK, body)

	got := runCLI(t, "", "health", "-url", url)
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	if req := fake.last(t); req.method != http.MethodGet || req.path != "/healthz" {
		t.Errorf("%s %s, want GET /healthz", req.method, req.path)
	}

	if !strings.HasPrefix(got.stdout, "status=ok\n") {
		t.Errorf("status の行が無い: %q", got.stdout)
	}

	for _, want := range []string{"database", "embedder"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("checks の表に %s が無い: %q", want, got.stdout)
		}
	}
}

// TestHealthReadsDegradedBodyFrom503 は 503 を Error として読まないことを見る。
//
// 🔴 /healthz は依存が落ちていると 503 で Health を返す。これを Error スキーマ
// として読むと「どの依存が落ちているか」が失われ、CLI は「サーバが 503 を
// 返した」としか言えなくなる。運用者は復旧の順序を決められない。
func TestHealthReadsDegradedBodyFrom503(t *testing.T) {
	const body = `{"status":"degraded","checks":{"database":{"status":"ok"},` +
		`"embedder":{"status":"down"}},"embedder_id":"bge-m3:1024"}`

	_, url := startFake(t, http.StatusServiceUnavailable, body)

	got := runCLI(t, "", "health", "-url", url)
	if got.code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", got.code, got.stderr)
	}

	if !strings.Contains(got.stdout, "status=degraded") {
		t.Errorf("degraded を読めていない: %q", got.stdout)
	}

	if !strings.Contains(got.stdout, "down") {
		t.Errorf("どの依存が落ちているかが出ていない: %q", got.stdout)
	}
}

// TestHealthChecksAreSorted は checks の行順が安定していることを見る。
//
// map の反復順は無作為なので、並べ替えないと同じ状態でも実行のたびに行が
// 入れ替わり、出力を diff で比べられない。
func TestHealthChecksAreSorted(t *testing.T) {
	const body = `{"status":"ok","checks":{"zeta":{"status":"ok"},` +
		`"alpha":{"status":"ok"},"mid":{"status":"ok"}},"embedder_id":"x"}`

	_, url := startFake(t, http.StatusOK, body)

	got := runCLI(t, "", "health", "-url", url)

	alpha := strings.Index(got.stdout, "alpha")
	mid := strings.Index(got.stdout, "mid")
	zeta := strings.Index(got.stdout, "zeta")

	if alpha < 0 || alpha >= mid || mid >= zeta {
		t.Errorf("checks が名前順になっていない: %q", got.stdout)
	}
}

// TestHealthTakesNoArguments は余計な引数を終了コード 1 で落とすことを見る。
func TestHealthTakesNoArguments(t *testing.T) {
	_, url := startFake(t, http.StatusOK, `{"status":"ok","checks":{}}`)

	got := runCLI(t, "", "health", "-url", url, "余計")
	if got.code != 1 {
		t.Errorf("exit = %d, want 1 (stderr=%s)", got.code, got.stderr)
	}
}
