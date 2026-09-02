package main_test

import (
	"os"
	"path/filepath"
	"testing"

	main "github.com/hideyukiMORI/nene-recall/cmd/eval"
	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// TestLoadDistractorsIsOptional は -distractors 未指定が「投入しない」に
// なることを見る。
//
// 🔴 nil の記録だけが「紛れ込み無しで測った」を意味する。0 件の記録を作ると、
// レポートを読む側は「0 件を投入した」と読み、その項目を持たない古い様式との
// 区別がつかなくなる (GO-004 / ADR 0019)。
func TestLoadDistractorsIsOptional(t *testing.T) {
	count, record, err := main.LoadDistractorsAt("")
	if err != nil {
		t.Fatalf("LoadDistractorsAt(\"\"): %v", err)
	}

	if count != 0 || record != nil {
		t.Fatalf("count = %d, record = %+v, want 0 / nil", count, record)
	}
}

// TestLoadDistractorsRecordsTheFile は読んだファイルの記録が付くことを見る。
func TestLoadDistractorsRecordsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "distractors.jsonl")
	content := []byte(`{"document_id":9000005,"source_id":9000000,` +
		`"chunk_index":0,"content":"本文"}` + "\n")

	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	count, record, err := main.LoadDistractorsAt(path)
	if err != nil {
		t.Fatalf("LoadDistractorsAt: %v", err)
	}

	if count != 1 || record == nil {
		t.Fatalf("count = %d, record = %+v, want 1 / 非 nil", count, record)
	}

	if record.Path != path || record.Count != 1 || record.SHA256 == "" {
		t.Errorf("記録 = %+v", record)
	}
}

// TestLoadDistractorsReportsMissingFile は無いファイルを黙って飛ばさないことを見る。
//
// 🔑 綴り誤りが「紛れ込み無しで測った」結果として記録されると、10万件の
// つもりで取った数字が 259 件の数字になる。条件表からは見分けられない。
func TestLoadDistractorsReportsMissingFile(t *testing.T) {
	_, _, err := main.LoadDistractorsAt(filepath.Join(t.TempDir(), "no-such-file.jsonl"))
	if err == nil {
		t.Fatal("無いファイルが通ってしまった")
	}
}

// TestDistractorCountReadsTheRecord は nil を 0 件と読むことを見る。
func TestDistractorCountReadsTheRecord(t *testing.T) {
	if got := main.DistractorCount(nil); got != 0 {
		t.Errorf("DistractorCount(nil) = %d, want 0", got)
	}

	record := &eval.FileInput{Path: "bin/distractors.jsonl", SHA256: "deadbeef", Count: 7}
	if got := main.DistractorCount(record); got != 7 {
		t.Errorf("DistractorCount(record) = %d, want 7", got)
	}
}
