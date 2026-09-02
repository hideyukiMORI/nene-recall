package main_test

import (
	"net/http"
	"strings"
	"testing"
)

// Bearer トークンの受け渡しと、外部 id を扱う経路の検査。
//
// 判断の正本は docs/adr/0020-phase2-corpus-integration-contract.md。

// searchExternalBody は external_id を持つ行と持たない行を1件ずつ返す応答。
const searchExternalBody = `{"results":[
	{"chunk_id":11,"external_id":901,"document_id":1,"source_id":2,"chunk_index":0,
	 "content":"外部 id を持つ本文","score":0.9,"vector_score":0.9,"lexical_score":0},
	{"chunk_id":12,"external_id":null,"document_id":1,"source_id":2,"chunk_index":1,
	 "content":"持たない本文","score":0.5,"vector_score":0.5,"lexical_score":0}
],"embedder_id":"bge-m3:1024","took_ms":12}`

// TestTokenFlagSetsAuthorizationHeader は -token が Bearer ヘッダになることを見る。
func TestTokenFlagSetsAuthorizationHeader(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, searchExternalBody)

	got := runCLI(t, "", "search", "-url", url, "-token", "s3cret", "問い")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	if want := "Bearer s3cret"; fake.last(t).authorization != want {
		t.Errorf("Authorization = %q, want %q", fake.last(t).authorization, want)
	}
}

// TestTokenFallsBackToEnvironment は $RECALL_TOKEN から取れることを見る。
func TestTokenFallsBackToEnvironment(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, searchExternalBody)

	t.Setenv(envURL, "")
	t.Setenv(envOrgID, "")
	t.Setenv(envToken, "from-env")

	got := runCLIWithEnv(t, "", "search", "-url", url, "問い")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	if want := "Bearer from-env"; fake.last(t).authorization != want {
		t.Errorf("Authorization = %q, want %q", fake.last(t).authorization, want)
	}
}

// TestNoTokenMeansNoHeader は未指定ならヘッダを付けないことを見る。
//
// 🔴 空の "Bearer " を送らない。サーバは「トークンを出したが違った」として
// 401 を返す。付けなければ、認証を要求していないサーバでは普通に通る。
func TestNoTokenMeansNoHeader(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, searchExternalBody)

	got := runCLI(t, "", "search", "-url", url, "問い")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	if fake.last(t).authorization != "" {
		t.Errorf("🔴 未指定なのに Authorization が付いた: %q", fake.last(t).authorization)
	}
}

// TestTokenNeverAppearsInDiagnostics はトークンが診断行に出ないことを見る。
//
// 🔴 recallctl は org_id を必ず stderr に出す（既定値が黙って効くのを防ぐため）。
// その流儀のままトークンも出すと、`recallctl search ... 2>&1 | tee log` のような
// 普通の使い方で秘密がファイルに落ちる。org_id と違い、トークンは取り違えても
// 他人のデータが見えるわけではなく、出す利益が無い。
func TestTokenNeverAppearsInDiagnostics(t *testing.T) {
	_, url := startFake(t, http.StatusOK, searchExternalBody)

	got := runCLI(t, "", "search", "-url", url, "-token", "s3cret-token", "問い")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	for _, stream := range []string{got.stderr, got.stdout} {
		if strings.Contains(stream, "s3cret-token") {
			t.Errorf("🔴 出力にトークンが含まれている: %q", stream)
		}
	}
}

// TestUnauthorizedIsAServerError は 401 が終了コード 2 になることを見る。
//
// 「サーバが拒否した」であって「使い方の誤り」でも「届かなかった」でもない。
// 再試行してよいのは 3（接続失敗）だけ、という区別を保つ (ADR 0016 Decision 3)。
func TestUnauthorizedIsAServerError(t *testing.T) {
	_, url := startFake(t, http.StatusUnauthorized,
		`{"error":{"code":"unauthorized","message":"a valid bearer token is required"}}`)

	got := runCLI(t, "", "search", "-url", url, "問い")
	if got.code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr=%s)", got.code, got.stderr)
	}

	if !strings.Contains(got.stderr, "unauthorized") {
		t.Errorf("error.code をそのまま見せていない: %q", got.stderr)
	}
}

// TestSearchTableShowsExternalID は表に ext 列が出ることを見る。
//
// null は "-" で書く。0 と書くと「外部 id が 0 番」と読めてしまう。
func TestSearchTableShowsExternalID(t *testing.T) {
	_, url := startFake(t, http.StatusOK, searchExternalBody)

	got := runCLI(t, "", "search", "-url", url, "問い")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	lines := strings.Split(strings.TrimRight(got.stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("行数 = %d, want 3（見出し + 2件）: %q", len(lines), got.stdout)
	}

	if !strings.Contains(lines[0], "ext") {
		t.Errorf("見出しに ext が無い: %q", lines[0])
	}

	if !strings.Contains(lines[1], "901") {
		t.Errorf("external_id が出ていない: %q", lines[1])
	}

	if !strings.Contains(lines[2], "-") {
		t.Errorf("🔴 external_id が null の行に - が出ていない: %q", lines[2])
	}

	if strings.Contains(lines[2], " 0 ") {
		t.Errorf("🔴 null を 0 として書いている: %q", lines[2])
	}
}

// TestPutSendsExternalID は JSONL の external_id をそのまま送ることを見る。
//
// 🔴 CLI が値を作らない。持たない行にはキーごと付けない——0 を補うと、
// サーバ側で「外部 id が 0 番」として置き換えの鍵になる。
func TestPutSendsExternalID(t *testing.T) {
	fake, url := startFake(t, http.StatusOK,
		`{"accepted":2,"chunk_ids":[11,12],"external_ids":[901,null]}`)

	const input = `{"external_id":901,"document_id":900,"source_id":900,"chunk_index":0,"content":"一つ目"}
{"document_id":900,"source_id":900,"chunk_index":1,"content":"二つ目"}
`

	got := runCLI(t, input, "put", "-url", url)
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	body := fake.last(t).body
	if !strings.Contains(body, `"external_id":901`) {
		t.Errorf("external_id を送っていない: %s", body)
	}

	if strings.Count(body, `"external_id"`) != 1 {
		t.Errorf("🔴 external_id を持たない行にも付けている: %s", body)
	}

	if !strings.Contains(got.stdout, "external_ids") {
		t.Errorf("応答の external_ids を表示していない: %q", got.stdout)
	}
}

// TestPutRejectsNonPositiveExternalID は 0 と負値を送る前に落とすことを見る。
func TestPutRejectsNonPositiveExternalID(t *testing.T) {
	for name, value := range map[string]string{"0": "0", "負値": "-1"} {
		t.Run(name, func(t *testing.T) {
			fake, url := startFake(t, http.StatusOK, putOKBody)

			input := `{"external_id":` + value +
				`,"document_id":900,"source_id":900,"chunk_index":0,"content":"本文"}` + "\n"

			got := runCLI(t, input, "put", "-url", url)
			if got.code != 1 {
				t.Fatalf("exit = %d, want 1 (stderr=%s)", got.code, got.stderr)
			}

			if len(fake.requests) != 0 {
				t.Errorf("🔴 不正な external_id を %d 件送っている", len(fake.requests))
			}

			if !strings.Contains(got.stderr, "line 1") {
				t.Errorf("何行目かを言っていない: %q", got.stderr)
			}
		})
	}
}

// TestChunkIDRejectionPointsAtExternalID は拒否の案内が external_id を指すことを見る。
//
// 🔴 Phase 1 の文言のままにしない。外部の id を渡す正しい方法が生えたので、
// 拒否のメッセージはそこへ導く義務がある（ADR 0020 Decision 1）。
func TestChunkIDRejectionPointsAtExternalID(t *testing.T) {
	_, url := startFake(t, http.StatusOK, putOKBody)

	input := `{"chunk_id":5,"document_id":900,"source_id":900,"chunk_index":0,"content":"本文"}` + "\n"

	got := runCLI(t, input, "put", "-url", url)
	if got.code != 1 {
		t.Fatalf("exit = %d, want 1", got.code)
	}

	if !strings.Contains(got.stderr, "external_id") {
		t.Errorf("🔴 案内が external_id を指していない: %q", got.stderr)
	}
}

// TestDeleteDocumentCallsTheDocumentEndpoint は文書単位の削除の宛先を固定する。
//
// 🔴 source 単位と取り違えると、消えるべきでないものが消える。しかもどちらも
// 200 と件数を返すので、宛先を間違えても応答の形からは気づけない。
func TestDeleteDocumentCallsTheDocumentEndpoint(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, `{"deleted":3}`)

	got := runCLI(t, "", "delete-document", "-url", url, "55")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	req := fake.last(t)
	if req.method != http.MethodDelete || req.path != "/v1/documents/55/chunks" {
		t.Errorf("%s %s, want DELETE /v1/documents/55/chunks", req.method, req.path)
	}

	if req.query != "org_id=1" {
		t.Errorf("query = %q, want org_id=1", req.query)
	}

	if !strings.Contains(got.stdout, "deleted=3") {
		t.Errorf("stdout = %q, want deleted=3", got.stdout)
	}
}

// TestDeleteDocumentRejectsNonNumericID は id の検証を見る。
func TestDeleteDocumentRejectsNonNumericID(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, `{"deleted":0}`)

	got := runCLI(t, "", "delete-document", "-url", url, "abc")
	if got.code != 1 {
		t.Errorf("exit = %d, want 1 (stderr=%s)", got.code, got.stderr)
	}

	if len(fake.requests) != 0 {
		t.Errorf("🔴 id が非数値なのに %d 件送っている", len(fake.requests))
	}
}
