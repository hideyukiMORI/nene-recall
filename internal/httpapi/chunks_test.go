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
		Accepted    int      `json:"accepted"`
		ChunkIDs    []int64  `json:"chunk_ids"`
		ExternalIDs []*int64 `json:"external_ids"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("応答が JSON ではない: %v", err)
	}

	if got.Accepted != 2 || len(got.ChunkIDs) != 2 {
		t.Errorf("accepted = %d, chunk_ids = %v", got.Accepted, got.ChunkIDs)
	}

	assertAllExternalIDsAreNull(t, got.ExternalIDs, 2)

	// 引数の org がそのままストアへ渡ることを確かめる（ADR 0003）。
	if writer.lastOrgID != mustOrg(t, 1) {
		t.Errorf("ストアへ渡った org = %s, want 1", writer.lastOrgID)
	}

	if len(writer.lastChunks) != 2 || writer.lastChunks[0].Content != "本文" {
		t.Errorf("チャンクが渡っていない: %+v", writer.lastChunks)
	}
}

// assertAllExternalIDsAreNull は external_ids が全て null で、長さが期待どおりかを見る。
//
// 外部 id を持たない投入でも列は chunk_ids と同じ長さで返る。長さが違うと、
// 呼び出し側は chunk_ids との対応を添字で取れない (ADR 0020 Decision 1)。
func assertAllExternalIDsAreNull(t *testing.T, ids []*int64, want int) {
	t.Helper()

	if len(ids) != want {
		t.Errorf("external_ids = %d 件, want %d", len(ids), want)

		return
	}

	for i, id := range ids {
		if id != nil {
			t.Errorf("external_ids[%d] = %d, want null", i, *id)
		}
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
			name:     "external_id が 0",
			body:     `{"org_id":1,"chunks":[{"external_id":0,"document_id":1,"source_id":2,"chunk_index":0,"content":"本文"}]}`,
			wantCode: "external_id_invalid",
		},
		{
			name:     "external_id が負",
			body:     `{"org_id":1,"chunks":[{"external_id":-1,"document_id":1,"source_id":2,"chunk_index":0,"content":"本文"}]}`,
			wantCode: "external_id_invalid",
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

// TestPutChunksPassesExternalIDToStore は external_id が素通しで渡ることを見る。
//
// 🔴 HTTP 層はここで値を作らない。0 を補ったり、chunk_id から写したりしない
// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 1)。
// 応答の external_ids は chunk_ids と同じ順で返る——呼び出し側 (Corpus) は
// 自分の id と Recall の採番の対応を、この1つの応答から作る。
func TestPutChunksPassesExternalIDToStore(t *testing.T) {
	searcher, writer := newFakes()
	writer.ids = []int64{11, 12}
	srv := newTestServerWith(t, testConfig(), newDeps(searcher, writer))

	withExternal := `{"external_id":901,"document_id":1,"source_id":2,"chunk_index":0,"content":"本文"}`
	body := `{"org_id":1,"chunks":[` + withExternal + `,` + validChunk + `]}`

	rec := post(t, srv, "/v1/chunks", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	if len(writer.lastChunks) != 2 {
		t.Fatalf("ストアへ渡ったチャンク = %d 件, want 2", len(writer.lastChunks))
	}

	first := writer.lastChunks[0].ExternalID
	if first == nil || *first != 901 {
		t.Errorf("chunks[0].ExternalID = %v, want 901", first)
	}

	if got := writer.lastChunks[1].ExternalID; got != nil {
		t.Errorf("chunks[1].ExternalID = %d, want nil（省略は nil のまま渡す）", *got)
	}

	assertExternalIDsEcho(t, rec.Body.Bytes())
}

// assertExternalIDsEcho は応答の external_ids が要求と同じ順で返ることを見る。
func assertExternalIDsEcho(t *testing.T, body []byte) {
	t.Helper()

	var resp struct {
		ExternalIDs []*int64 `json:"external_ids"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("応答が JSON ではない: %v", err)
	}

	if len(resp.ExternalIDs) != 2 {
		t.Fatalf("external_ids = %d 件, want 2", len(resp.ExternalIDs))
	}

	if resp.ExternalIDs[0] == nil || *resp.ExternalIDs[0] != 901 {
		t.Errorf("external_ids[0] = %v, want 901", resp.ExternalIDs[0])
	}

	if resp.ExternalIDs[1] != nil {
		t.Errorf("external_ids[1] = %d, want null", *resp.ExternalIDs[1])
	}
}

// TestChunkIDRejectionPointsAtExternalID は 400 の案内が external_id を指すことを見る。
//
// 🔴 Phase 1 の文言（「Recall assigns ids in phase 1」）のままにしないこと。
// 外部の id を渡す正しい方法が生えたので、拒否のメッセージはそこへ導く義務がある
// (ADR 0020 Decision 1)。案内が古いと、呼び出し側は「渡す手段が無い」と読む。
func TestChunkIDRejectionPointsAtExternalID(t *testing.T) {
	srv := newTestServer(t)

	body := `{"org_id":1,"chunks":[{"chunk_id":5,"document_id":1,"source_id":2,"chunk_index":0,"content":"本文"}]}`

	rec := post(t, srv, "/v1/chunks", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	if !contains(rec.Body.String(), "external_id") {
		t.Errorf("🔴 拒否のメッセージが external_id を案内していない: %s", rec.Body.String())
	}
}

// TestDeleteByDocumentReturnsCount は文書単位の削除が件数を返すことを見る。
//
// source 単位と同じ応答の形にしてある。Corpus の削除経路が2つある以上
// (ADR 0020 Decision 2)、呼び出し側は2つを同じ形で扱えなければならない。
func TestDeleteByDocumentReturnsCount(t *testing.T) {
	searcher, writer := newFakes()
	writer.deleted = 4
	srv := newTestServerWith(t, testConfig(), newDeps(searcher, writer))

	rec := del(t, srv, "/v1/documents/9/chunks?org_id=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Deleted int `json:"deleted"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("応答が JSON ではない: %v", err)
	}

	if got.Deleted != 4 {
		t.Errorf("deleted = %d, want 4", got.Deleted)
	}

	// 引数の org がそのままストアへ渡ることを確かめる（ADR 0003）。
	if writer.lastOrgID != mustOrg(t, 1) {
		t.Errorf("ストアへ渡った org = %s, want 1", writer.lastOrgID)
	}
}

// TestDeleteRejectsNonNumericPath はパス変数の検証を確かめる。
func TestDeleteRejectsNonNumericPath(t *testing.T) {
	srv := newTestServer(t)

	cases := map[string]string{
		"chunk_id が非数値":    "/v1/chunks/abc?org_id=1",
		"source_id が非数値":   "/v1/sources/abc/chunks?org_id=1",
		"document_id が非数値": "/v1/documents/abc/chunks?org_id=1",
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
