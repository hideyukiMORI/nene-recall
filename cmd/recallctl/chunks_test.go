package main_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// putOKBody は投入が成功したときの応答。
const putOKBody = `{"accepted":2,"chunk_ids":[11,12]}`

// sampleJSONL は2件ぶんの入力。
const sampleJSONL = `{"document_id":900,"source_id":900,"chunk_index":0,"content":"一つ目"}

{"document_id":900,"source_id":900,"chunk_index":1,"content":"二つ目"}
`

// TestPutSendsAllLinesInOneRequest は JSONL を1リクエストにまとめることを見る。
//
// 🔴 分割送信しない。途中で失敗すると「一部だけ入った」状態が残り、
// どこまで入ったかを利用者が知る手段が無い。
func TestPutSendsAllLinesInOneRequest(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, putOKBody)

	got := runCLI(t, sampleJSONL, "put", "-url", url)
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	if len(fake.requests) != 1 {
		t.Fatalf("要求 = %d 件, want 1", len(fake.requests))
	}

	req := fake.last(t)
	if req.method != http.MethodPost || req.path != "/v1/chunks" {
		t.Errorf("%s %s, want POST /v1/chunks", req.method, req.path)
	}

	if !strings.Contains(req.body, `"org_id":1`) {
		t.Errorf("org_id が入っていない: %s", req.body)
	}

	if strings.Count(req.body, `"content"`) != 2 {
		t.Errorf("空行を含めて数え違えている: %s", req.body)
	}
}

// TestPutRejectsChunkIDBeforeSending は chunk_id を含む行を送る前に落とすことを見る。
//
// Phase 1 の契約ではサーバも 400 を返すが、CLI 側で止めるのは「何行目か」を
// 返せるのがここだけだからである (OpenAPI の ChunkInput.chunk_id)。
func TestPutRejectsChunkIDBeforeSending(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, putOKBody)

	const withChunkID = `{"document_id":900,"source_id":900,"chunk_index":0,"content":"本文"}
{"chunk_id":5,"document_id":900,"source_id":900,"chunk_index":1,"content":"本文"}
`

	got := runCLI(t, withChunkID, "put", "-url", url)

	if got.code != 1 {
		t.Errorf("exit = %d, want 1", got.code)
	}

	if len(fake.requests) != 0 {
		t.Errorf("🔴 chunk_id が入っているのに %d 件送っている", len(fake.requests))
	}

	if !strings.Contains(got.stderr, "line 2") {
		t.Errorf("何行目かを言っていない: %q", got.stderr)
	}
}

// TestPutRejectsBrokenInput は壊れた入力を通信前に落とすことを見る。
func TestPutRejectsBrokenInput(t *testing.T) {
	cases := []struct {
		name  string
		stdin string
	}{
		{name: "JSON ではない", stdin: "これは JSON ではない\n"},
		{name: "本文が空", stdin: `{"document_id":1,"source_id":1,"chunk_index":0,"content":""}` + "\n"},
		{name: "1件も無い", stdin: "\n\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, url := startFake(t, http.StatusOK, putOKBody)

			got := runCLI(t, tc.stdin, "put", "-url", url)
			if got.code != 1 {
				t.Errorf("exit = %d, want 1 (stderr=%s)", got.code, got.stderr)
			}

			if len(fake.requests) != 0 {
				t.Errorf("🔴 入力が壊れているのに %d 件送っている", len(fake.requests))
			}
		})
	}
}

// TestPutReadsFromFile はファイル引数から読めることを見る。
func TestPutReadsFromFile(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, putOKBody)

	path := filepath.Join(t.TempDir(), "chunks.jsonl")
	if err := os.WriteFile(path, []byte(sampleJSONL), 0o600); err != nil {
		t.Fatalf("入力ファイルを作れない: %v", err)
	}

	got := runCLI(t, "", "put", "-url", url, path)
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	if strings.Count(fake.last(t).body, `"content"`) != 2 {
		t.Errorf("ファイルから2件読めていない: %s", fake.last(t).body)
	}
}

// TestPutReportsMissingFile は開けないファイルを終了コード 1 で落とすことを見る。
func TestPutReportsMissingFile(t *testing.T) {
	_, url := startFake(t, http.StatusOK, putOKBody)

	got := runCLI(t, "", "put", "-url", url, filepath.Join(t.TempDir(), "無い.jsonl"))
	if got.code != 1 {
		t.Errorf("exit = %d, want 1 (stderr=%s)", got.code, got.stderr)
	}
}

// TestPutPrintsAcceptedAndChunkIDs は既定の出力を固定する。
func TestPutPrintsAcceptedAndChunkIDs(t *testing.T) {
	_, url := startFake(t, http.StatusOK, putOKBody)

	got := runCLI(t, sampleJSONL, "put", "-url", url)
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	for _, want := range []string{`"accepted": 2`, `"chunk_ids"`, "11", "12"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout に %s が無い: %q", want, got.stdout)
		}
	}
}

// TestPutJSONPassesResponseThrough は -json が応答をそのまま出すことを見る。
func TestPutJSONPassesResponseThrough(t *testing.T) {
	_, url := startFake(t, http.StatusOK, putOKBody)

	got := runCLI(t, sampleJSONL, "put", "-url", url, "-json")
	if strings.TrimRight(got.stdout, "\n") != putOKBody {
		t.Errorf("stdout = %q, want %q", got.stdout, putOKBody)
	}
}
