package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// validChunk は投入できる最小のチャンク JSON。
const validChunk = `{"document_id":1,"source_id":2,"chunk_index":0,"content":"本文"}`

// TestPutChunksAcceptsAndReturnsIDs は投入の成功経路を確かめる。
func TestPutChunksAcceptsAndReturnsIDs(t *testing.T) {
	searcher, writer := newFakes()
	writer.ids = []int64{11, 12}
	srv := newTestServerWith(t, testConfig(), newDeps(searcher, writer))

	body := `{"org_id":1,"chunks":[` + validChunk + `,` + validChunk + `]}`

	rec := post(t, srv, "/v1/chunks", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Accepted int     `json:"accepted"`
		ChunkIDs []int64 `json:"chunk_ids"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("応答が JSON ではない: %v", err)
	}

	if got.Accepted != 2 || len(got.ChunkIDs) != 2 {
		t.Errorf("accepted = %d, chunk_ids = %v", got.Accepted, got.ChunkIDs)
	}

	// 引数の org がそのままストアへ渡ることを確かめる（ADR 0003）。
	if writer.lastOrgID != mustOrg(t, 1) {
		t.Errorf("ストアへ渡った org = %s, want 1", writer.lastOrgID)
	}

	if len(writer.lastChunks) != 2 || writer.lastChunks[0].Content != "本文" {
		t.Errorf("チャンクが渡っていない: %+v", writer.lastChunks)
	}
}

// TestPutChunksRejectsInvalidInput は境界検証を確かめる。
//
// 🔴 chunk_id の指定は 400 にする。Phase 1 では明示 id を受け付けない
// （施主決定）。受理してしまうと採番と衝突する。
func TestPutChunksRejectsInvalidInput(t *testing.T) {
	srv := newTestServer(t)

	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{
			name:     "chunks が空",
			body:     `{"org_id":1,"chunks":[]}`,
			wantCode: "chunks_required",
		},
		{
			name:     "content が空",
			body:     `{"org_id":1,"chunks":[{"document_id":1,"source_id":2,"chunk_index":0,"content":""}]}`,
			wantCode: "content_required",
		},
		{
			name:     "chunk_id を指定",
			body:     `{"org_id":1,"chunks":[{"chunk_id":5,"document_id":1,"source_id":2,"chunk_index":0,"content":"本文"}]}`,
			wantCode: "chunk_id_not_accepted",
		},
		{
			name:     "JSON が壊れている",
			body:     `{"org_id":1,`,
			wantCode: "invalid_json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, srv, "/v1/chunks", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}

			if code := errorCode(t, rec); code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// TestDeleteChunkReturns204 は対象の有無に関わらず 204 を返すことを確かめる。
func TestDeleteChunkReturns204(t *testing.T) {
	srv := newTestServer(t)

	rec := del(t, srv, "/v1/chunks/5?org_id=1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
}

// TestDeleteBySourceReturnsCount は削除件数を返すことを確かめる。
func TestDeleteBySourceReturnsCount(t *testing.T) {
	searcher, writer := newFakes()
	writer.deleted = 3
	srv := newTestServerWith(t, testConfig(), newDeps(searcher, writer))

	rec := del(t, srv, "/v1/sources/9/chunks?org_id=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Deleted int `json:"deleted"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("応答が JSON ではない: %v", err)
	}

	if got.Deleted != 3 {
		t.Errorf("deleted = %d, want 3", got.Deleted)
	}
}

// TestDeleteRejectsNonNumericPath はパス変数の検証を確かめる。
func TestDeleteRejectsNonNumericPath(t *testing.T) {
	srv := newTestServer(t)

	cases := map[string]string{
		"chunk_id が非数値":  "/v1/chunks/abc?org_id=1",
		"source_id が非数値": "/v1/sources/abc/chunks?org_id=1",
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			rec := del(t, srv, path)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}
