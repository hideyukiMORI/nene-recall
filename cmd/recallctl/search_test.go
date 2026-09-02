package main_test

import (
	"net/http"
	"strings"
	"testing"

	main "github.com/hideyukiMORI/nene-recall/cmd/recallctl"
)

// searchHitBody は1件だけ返す応答。
const searchHitBody = `{"results":[{"chunk_id":42,"document_id":7,"source_id":70,` +
	`"chunk_index":2,"content":"ベクトルの索引を張ると検索は速くなる。",` +
	`"score":0.81,"vector_score":0.9,"lexical_score":0.6}],` +
	`"embedder_id":"bge-m3:1024","took_ms":62}`

// TestSearchOmitsUnspecifiedTuning は limit / alpha を指定しないとき送らないことを見る。
//
// 🔴 0 を送ってはいけない。サーバは「キーが無い」を既定値、「0 が来た」を指定
// された値として扱う (internal/httpapi/search.go)。limit=0 は範囲外で 400 になり、
// alpha=0 は純語彙検索という別の条件になる。
func TestSearchOmitsUnspecifiedTuning(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, searchOKBody)

	got := runCLI(t, "", "search", "-url", url, "ベクトル", "索引")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	body := fake.last(t).body
	for _, key := range []string{"limit", "alpha", "filters"} {
		if strings.Contains(body, `"`+key+`"`) {
			t.Errorf("🔴 未指定の %s を送っている: %s", key, body)
		}
	}

	if !strings.Contains(body, `"query":"ベクトル 索引"`) {
		t.Errorf("query が組み立てられていない: %s", body)
	}
}

// TestSearchSendsSpecifiedTuning は指定した limit / alpha / filters を送ることを見る。
//
// alpha=0 を含めるのは、ゼロ値と未指定を取り違えていないことを確かめるためである。
func TestSearchSendsSpecifiedTuning(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, searchOKBody)

	got := runCLI(t, "", "search", "-url", url, "-limit", "3", "-alpha", "0",
		"-document", "7", "-document", "8", "-source", "70", "問い")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	body := fake.last(t).body
	for _, want := range []string{
		`"limit":3`, `"alpha":0`, `"document_ids":[7,8]`, `"source_ids":[70]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("送信本文に %s が無い: %s", want, body)
		}
	}
}

// TestSearchRendersTable は既定の出力が表であることを見る。
func TestSearchRendersTable(t *testing.T) {
	_, url := startFake(t, http.StatusOK, searchHitBody)

	got := runCLI(t, "", "search", "-url", url, "索引")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	lines := strings.Split(strings.TrimRight(got.stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %d 行, want 2 行 (見出し + 1件): %q", len(lines), got.stdout)
	}

	if !strings.HasPrefix(lines[0], "#") || !strings.Contains(lines[0], "lexical") {
		t.Errorf("見出しの形が違う: %q", lines[0])
	}

	if !strings.Contains(lines[1], "0.8100") || !strings.Contains(lines[1], "索引") {
		t.Errorf("結果行の形が違う: %q", lines[1])
	}

	if !strings.Contains(got.stderr, "embedder_id=bge-m3:1024 took_ms=62") {
		t.Errorf("診断行が stderr に無い: %q", got.stderr)
	}
}

// TestSearchJSONPassesResponseThrough は -json が応答をそのまま出すことを見る。
func TestSearchJSONPassesResponseThrough(t *testing.T) {
	_, url := startFake(t, http.StatusOK, searchHitBody)

	got := runCLI(t, "", "search", "-url", url, "-json", "索引")
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
	}

	if strings.TrimRight(got.stdout, "\n") != searchHitBody {
		t.Errorf("stdout が生の応答と違う:\n got = %s\nwant = %s", got.stdout, searchHitBody)
	}
}

// TestSearchRequiresQuery は問い合わせ語が無いときに通信しないことを見る。
func TestSearchRequiresQuery(t *testing.T) {
	fake, url := startFake(t, http.StatusOK, searchOKBody)

	got := runCLI(t, "", "search", "-url", url)

	if got.code != 1 {
		t.Errorf("exit = %d, want 1 (stderr=%s)", got.code, got.stderr)
	}

	if len(fake.requests) != 0 {
		t.Errorf("🔴 query が無いのに %d 件送っている", len(fake.requests))
	}
}

// TestPreviewTruncatesByRunes は本文の切り詰めが文字単位であることを見る。
//
// バイト単位で切ると日本語が文字の途中で切れ、表が壊れる。
func TestPreviewTruncatesByRunes(t *testing.T) {
	long := strings.Repeat("あ", 50)

	got := main.PreviewForTest(long)
	if want := strings.Repeat("あ", 40) + "…"; got != want {
		t.Errorf("preview = %q, want %q", got, want)
	}

	if got := main.PreviewForTest("一行目\n二行目"); got != "一行目 二行目" {
		t.Errorf("改行が空白に潰れていない: %q", got)
	}
}
