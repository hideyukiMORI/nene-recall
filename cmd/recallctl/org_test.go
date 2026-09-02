package main_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	main "github.com/hideyukiMORI/nene-recall/cmd/recallctl"
)

// searchOKBody は検索が成功したときの最小の応答。
const searchOKBody = `{"results":[],"embedder_id":"bge-m3:1024","took_ms":7}`

// TestOrgIDResolutionOrder は --org → $RECALL_ORG_ID → 既定 の順を固定する。
//
// 🔴 この順序と「既定値が CLI にしか無い」ことが ADR 0016 Decision 2 の中身である。
// 順序を入れ替えると、環境変数を設定したまま別の org を明示したときに、
// 打った値ではないほうで検索することになる。
func TestOrgIDResolutionOrder(t *testing.T) {
	cases := []struct {
		name     string
		envValue string
		args     []string
		wantOrg  string
		wantFrom string
	}{
		{name: "flag が最優先", envValue: "9", args: []string{"-org", "7"}, wantOrg: "7", wantFrom: "flag"},
		{name: "env が次", envValue: "9", args: nil, wantOrg: "9", wantFrom: "env"},
		{name: "既定が最後", envValue: "", args: nil, wantOrg: "1", wantFrom: "default"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, url := startFake(t, http.StatusOK, searchOKBody)

			t.Setenv(envURL, "")
			t.Setenv(envOrgID, tc.envValue)

			args := append([]string{"search", "-url", url}, tc.args...)
			got := runCLIWithEnv(t, "", append(args, "問い")...)

			if got.code != 0 {
				t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
			}

			assertAnnouncedOrg(t, got.stderr, tc.wantOrg, tc.wantFrom)
			assertSentOrgID(t, fake.last(t).body, tc.wantOrg)
		})
	}
}

// TestDefaultOrgIDIsOne は CLI の既定値が 1 であることを固定する。
//
// 定数そのものを見るのは、既定値を変えたときに ADR 0016 を読み直させるためである。
func TestDefaultOrgIDIsOne(t *testing.T) {
	if got := main.DefaultOrgIDForTest(); got != 1 {
		t.Errorf("defaultOrgID = %d, want 1 (変えるなら ADR 0016 Decision 2 を直すこと)", got)
	}
}

// TestInvalidOrgIDIsRejectedBeforeSending は壊れた org_id で通信しないことを見る。
func TestInvalidOrgIDIsRejectedBeforeSending(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, searchOKBody)

	got := runCLI(t, "", "search", "-url", url, "-org", "0", "問い")

	if got.code != 1 {
		t.Errorf("exit = %d, want 1", got.code)
	}

	if len(fake.requests) != 0 {
		t.Errorf("🔴 org_id が不正なのに %d 件送っている", len(fake.requests))
	}
}

// TestHealthDoesNotAnnounceOrgID は health が org_id を表示しないことを見る。
//
// /healthz はテナントを持たない。表示すると「health も org 単位だ」と読まれる。
func TestHealthDoesNotAnnounceOrgID(t *testing.T) {
	_, url := startFake(t, http.StatusOK,
		`{"status":"ok","checks":{},"embedder_id":"bge-m3:1024"}`)

	got := runCLI(t, "", "health", "-url", url)

	if strings.Contains(got.stderr, "org_id=") {
		t.Errorf("🔴 health が org_id を表示している: %q", got.stderr)
	}
}

// assertAnnouncedOrg は stderr の `org_id=<n> (<source>)` を確かめる。
func assertAnnouncedOrg(t *testing.T, stderr, wantOrg, wantFrom string) {
	t.Helper()

	want := "org_id=" + wantOrg + " (" + wantFrom + ")"
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr に %q が無い: %q", want, stderr)
	}
}

// assertSentOrgID は送った本文に org_id が入っていることを確かめる。
func assertSentOrgID(t *testing.T, body, wantOrg string) {
	t.Helper()

	want := `"org_id":` + wantOrg
	if !strings.Contains(body, want) {
		t.Errorf("送信本文に %q が無い: %s", want, body)
	}
}

// deleteOrgID は削除系のテストで使う org_id。既定値と別の値にしてある——
// 1 のままだと「フラグが効いた」のか「既定値が効いた」のか区別できない。
const deleteOrgID = "5"

// deleteCase は削除系コマンド1つぶんの条件。
type deleteCase struct {
	name     string
	command  string
	id       string
	status   int
	body     string
	wantPath string
}

// TestDeletePassesOrgIDInQueryString は削除系が org_id をクエリ文字列で送ることを見る。
//
// OpenAPI の deleteChunk / deleteChunksBySource は org_id を in: query の必須
// パラメータとして定めており、internal/httpapi の requireOrgID も
// r.URL.Query() から読む。本文で送ると 400 になる。
func TestDeletePassesOrgIDInQueryString(t *testing.T) {
	cases := []deleteCase{
		{
			name:     "delete",
			command:  "delete",
			id:       "42",
			status:   http.StatusNoContent,
			body:     "",
			wantPath: "/v1/chunks/42",
		},
		{
			name:     "delete-source",
			command:  "delete-source",
			id:       "900",
			status:   http.StatusOK,
			body:     `{"deleted":3}`,
			wantPath: "/v1/sources/900/chunks",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runDeleteCase(t, tc) })
	}
}

// runDeleteCase は削除系コマンドを1回走らせ、送った要求を確かめる。
func runDeleteCase(t *testing.T, tc deleteCase) {
	t.Helper()

	fake, url := startFake(t, tc.status, tc.body)

	got := runCLI(t, "", tc.command, "-url", url, "-org", deleteOrgID, tc.id)
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	assertDeleteRequest(t, fake.last(t), tc.wantPath)
}

// assertDeleteRequest は削除要求の形を確かめる。
func assertDeleteRequest(t *testing.T, req recordedRequest, wantPath string) {
	t.Helper()

	if req.method != http.MethodDelete || req.path != wantPath {
		t.Errorf("%s %s, want DELETE %s", req.method, req.path, wantPath)
	}

	if want := "org_id=" + deleteOrgID; req.query != want {
		t.Errorf("query = %q, want %q", req.query, want)
	}

	if req.body != "" {
		t.Errorf("🔴 削除に本文を送っている: %q", req.body)
	}
}

// TestDeleteSourceReportsDeletedCount は delete-source の出力を固定する。
func TestDeleteSourceReportsDeletedCount(t *testing.T) {
	const deleted = 3

	_, url := startFake(t, http.StatusOK, `{"deleted":`+strconv.Itoa(deleted)+`}`)

	got := runCLI(t, "", "delete-source", "-url", url, "900")

	if want := "deleted=" + strconv.Itoa(deleted) + "\n"; got.stdout != want {
		t.Errorf("stdout = %q, want %q", got.stdout, want)
	}
}
