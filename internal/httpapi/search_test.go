package httpapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
)

// errUnmapped は写像表に載っていない失敗を表すテスト用の sentinel。
//
// 内部情報を含む文字列にしてあるのは、500 の応答へ漏れないことを同時に見るため。
var errUnmapped = errors.New("postgres: dial tcp 10.0.0.1:5433: connect: refused")

// errLeaky は DSN と SQL を含む失敗を表すテスト用の sentinel。
var errLeaky = errors.New("postgres://recall:hunter2@10.0.0.1:5433/recall: syntax error at SELECT")

// sampleResult は応答の形を確かめるための1件。
func sampleResult(t *testing.T) index.Result {
	t.Helper()

	page := 3
	label := "第2章"

	return index.Result{
		Chunk: chunk.Chunk{
			ID:           42,
			OrgID:        mustOrg(t, 1),
			DocumentID:   7,
			SourceID:     70,
			ChunkIndex:   2,
			Content:      "本文",
			PageNumber:   &page,
			SectionLabel: &label,
		},
		Score:        0.5,
		VectorScore:  0.8,
		LexicalScore: 0,
	}
}

// TestSearchResponseMatchesOpenAPI は応答の形を OpenAPI と突き合わせる。
//
// 🔴 org_id が応答に無いことをここで縛る。出さないのは「出す必要が無い」からでは
// なく、出せば org を跨いだ取り違えが利用者側へ伝播しうるからである (ADR 0003)。
func TestSearchResponseMatchesOpenAPI(t *testing.T) {
	searcher, writer := newFakes()
	searcher.results = []index.Result{sampleResult(t)}
	srv := newTestServerWith(t, testConfig(), newDeps(searcher, writer))

	rec := post(t, srv, "/v1/search", `{"org_id":1,"query":"問い"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	if strings.Contains(rec.Body.String(), "org_id") {
		t.Errorf("🔴 応答に org_id が含まれている: %s", rec.Body.String())
	}

	var body struct {
		Results []struct {
			ChunkID      int64    `json:"chunk_id"`
			Content      string   `json:"content"`
			PageNumber   *int     `json:"page_number"`
			SectionLabel *string  `json:"section_label"`
			Score        float32  `json:"score"`
			VectorScore  *float32 `json:"vector_score"`
			LexicalScore *float32 `json:"lexical_score"`
		} `json:"results"`
		EmbedderID string `json:"embedder_id"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("応答が JSON ではない: %v", err)
	}

	if len(body.Results) != 1 {
		t.Fatalf("results = %d 件, want 1", len(body.Results))
	}

	got := body.Results[0]
	if got.ChunkID != 42 || got.Content != "本文" {
		t.Errorf("chunk_id/content = %d/%q", got.ChunkID, got.Content)
	}

	// 🔴 内訳を必ず返す。合成値だけでは、検索が外したときにベクトル側と語彙側の
	// どちらが原因かを切り分けられない。
	if got.VectorScore == nil || got.LexicalScore == nil {
		t.Errorf("vector_score / lexical_score が応答に無い: %s", rec.Body.String())
	}

	if body.EmbedderID != "bge-m3:1024" {
		t.Errorf("embedder_id = %q", body.EmbedderID)
	}
}

// TestSearchAppliesDefaultsAndFilters は境界での変換を確かめる。
func TestSearchAppliesDefaultsAndFilters(t *testing.T) {
	searcher, writer := newFakes()
	srv := newTestServerWith(t, testConfig(), newDeps(searcher, writer))

	body := `{"org_id":1,"query":"問い","filters":{"document_ids":[2,3],"source_ids":[9]}}`
	if rec := post(t, srv, "/v1/search", body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}

	got := searcher.lastQuery
	if got.Limit != 10 {
		t.Errorf("limit の既定 = %d, want 10", got.Limit)
	}

	// 既定の alpha は設定由来。この値が「調整済み」であることを主張するものではない。
	if got.Alpha != testConfig().DefaultAlpha {
		t.Errorf("alpha の既定 = %v, want %v", got.Alpha, testConfig().DefaultAlpha)
	}

	if len(got.DocumentIDs) != 2 || len(got.SourceIDs) != 1 {
		t.Errorf("filters が渡っていない: %+v", got)
	}
}

// TestSearchRejectsOutOfRange は範囲外の値を 400 にすることを確かめる。
//
// 0 と「キーが無い」を区別できていないと、範囲外の 0 が既定値に化けて通る。
func TestSearchRejectsOutOfRange(t *testing.T) {
	srv := newTestServer(t)

	cases := map[string]string{
		"limit が 0":   `{"org_id":1,"query":"x","limit":0}`,
		"limit が 101": `{"org_id":1,"query":"x","limit":101}`,
		"alpha が負":    `{"org_id":1,"query":"x","alpha":-0.1}`,
		"alpha が 1 超": `{"org_id":1,"query":"x","alpha":1.5}`,
	}

	wantCodes := map[string]string{
		"limit が 0":   "limit_out_of_range",
		"limit が 101": "limit_out_of_range",
		"alpha が負":    "alpha_out_of_range",
		"alpha が 1 超": "alpha_out_of_range",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(t, srv, "/v1/search", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}

			if code := errorCode(t, rec); code != wantCodes[name] {
				t.Errorf("code = %q, want %q", code, wantCodes[name])
			}
		})
	}
}

// TestFailureMappingTable は error から HTTP への写像を1行ずつ固定する。
//
// 🔴 この表がずれると、運用者が見る症状が変わる。たとえば埋め込み不可用が
// 500 になると「Ollama を起動し忘れた」が「サーバの不具合」に見える。
func TestFailureMappingTable(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "検索要求が契約を満たさない",
			err:        fmt.Errorf("store: %w", index.ErrInvalidQuery),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_query",
		},
		{
			name:       "保存済みベクトルのモデルが違う",
			err:        fmt.Errorf("store: %w", index.ErrEmbedderMismatch),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "embedder_mismatch",
		},
		{
			name:       "埋め込みプロバイダが落ちている",
			err:        fmt.Errorf("store: %w", embed.ErrProviderUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "embedder_unavailable",
		},
		{
			name:       "表に無い失敗",
			err:        errUnmapped,
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			searcher, writer := newFakes()
			searcher.err = tc.err
			srv := newTestServerWith(t, testConfig(), newDeps(searcher, writer))

			rec := post(t, srv, "/v1/search", `{"org_id":1,"query":"x"}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if code := errorCode(t, rec); code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// TestInternalErrorHidesDetails は 500 の応答が内部情報を漏らさないことを見る。
//
// 🔴 DSN・SQL・接続先は応答に出さない。詳細は slog にだけ残す。
func TestInternalErrorHidesDetails(t *testing.T) {
	searcher, writer := newFakes()
	searcher.err = errLeaky
	srv := newTestServerWith(t, testConfig(), newDeps(searcher, writer))

	rec := post(t, srv, "/v1/search", `{"org_id":1,"query":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	for _, secret := range []string{"hunter2", "10.0.0.1", "SELECT"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("🔴 500 の応答に内部情報 %q が漏れている: %s", secret, rec.Body.String())
		}
	}
}
