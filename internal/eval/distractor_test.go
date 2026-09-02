package eval_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// validDistractorLine は読み込みに通る1行。
const validDistractorLine = `{"document_id":9000005,"source_id":9000000,` +
	`"chunk_index":0,"content":"アンパサンドは並立助詞を意味する記号である。"}`

// TestLoadDistractorsReadsTheLines は正常な JSONL が読めることを見る。
func TestLoadDistractorsReadsTheLines(t *testing.T) {
	got, err := eval.LoadDistractors(strings.NewReader(validDistractorLine + "\n"))
	if err != nil {
		t.Fatalf("LoadDistractors: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("件数 = %d, want 1", len(got))
	}

	want := eval.Distractor{
		DocumentID: 9000005,
		SourceID:   9000000,
		ChunkIndex: 0,
		Content:    "アンパサンドは並立助詞を意味する記号である。",
	}
	if got[0] != want {
		t.Errorf("読み込み結果 = %+v, want %+v", got[0], want)
	}
}

// TestDistractorKeyIsDerivedFromIDs は表示名が id から機械的に作られることを見る。
//
// 🔑 人が付ける eval_key と衝突しないことが要点である。衝突すると写像が
// 上書きされ、正解チャンクが紛れ込みとして返ってきた形になる。
func TestDistractorKeyIsDerivedFromIDs(t *testing.T) {
	d := eval.Distractor{
		DocumentID: 9000005, SourceID: 9000000, ChunkIndex: 3, Content: "本文",
	}

	if got, want := d.Key(), "distractor:9000005#3"; got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}

	if !strings.HasPrefix(d.Key(), eval.DistractorKeyPrefix) {
		t.Errorf("Key() = %q は接頭辞 %q で始まっていない", d.Key(), eval.DistractorKeyPrefix)
	}
}

// TestLoadDistractorsRejectsEvalKey は eval_key を書いた行が拒否されることを見る。
//
// 🔴 紛れ込みは「どのクエリの正解にもならない」行である (ADR 0019)。
// 正解キーを与える経路を境界で塞いでおかないと、注釈が指していないのに
// 「正解になりうる行」が生まれ、評価セットの意味が静かに変わる。
func TestLoadDistractorsRejectsEvalKey(t *testing.T) {
	line := `{"eval_key":"adr-0007#003","document_id":9000005,` +
		`"source_id":9000000,"chunk_index":0,"content":"本文"}`

	_, err := eval.LoadDistractors(strings.NewReader(line + "\n"))
	if !errors.Is(err, eval.ErrInvalidDataset) {
		t.Fatalf("err = %v, want ErrInvalidDataset", err)
	}
}

// TestLoadDistractorsRejectsBrokenLines は欠けた値と重複を拒否することを見る。
func TestLoadDistractorsRejectsBrokenLines(t *testing.T) {
	cases := map[string]string{
		"本文が空": `{"document_id":1,"source_id":1,"chunk_index":0,"content":""}`,
		"document_id が 0": `{"document_id":0,"source_id":1,` +
			`"chunk_index":0,"content":"本文"}`,
		"source_id が 0": `{"document_id":1,"source_id":0,` +
			`"chunk_index":0,"content":"本文"}`,
		"chunk_index が負": `{"document_id":1,"source_id":1,` +
			`"chunk_index":-1,"content":"本文"}`,
		"同じ document_id と chunk_index が2度": `{"document_id":1,"source_id":1,` +
			`"chunk_index":0,"content":"本文"}` + "\n" +
			`{"document_id":1,"source_id":1,"chunk_index":0,"content":"別の本文"}`,
	}

	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := eval.LoadDistractors(strings.NewReader(input + "\n")); err == nil {
				t.Fatal("読めてしまった。境界で落とすこと")
			}
		})
	}
}

// TestLoadDistractorFileRecordsIdentity は件数と sha256 が記録されることを見る。
//
// 🔴 記録の無いレポートは 259 件の数字と並べて読めない (ADR 0019 Decision 2)。
func TestLoadDistractorFileRecordsIdentity(t *testing.T) {
	content := []byte(validDistractorLine + "\n")

	got, input, err := eval.LoadDistractorFile(
		eval.SourceFile{Path: "bin/distractors.jsonl", Content: content})
	if err != nil {
		t.Fatalf("LoadDistractorFile: %v", err)
	}

	if len(got) != 1 || input.Count != 1 {
		t.Fatalf("件数 = %d / 記録 = %d, want 1 / 1", len(got), input.Count)
	}

	if input.Path != "bin/distractors.jsonl" {
		t.Errorf("Path = %q", input.Path)
	}

	sum, err := eval.SHA256(strings.NewReader(string(content)))
	if err != nil {
		t.Fatalf("SHA256: %v", err)
	}

	if input.SHA256 != sum {
		t.Errorf("SHA256 = %q, want %q", input.SHA256, sum)
	}
}

// TestLoadDistractorFileRejectsEmpty は空のファイルを拒否することを見る。
//
// 🔑 0 件の記録を作らせない。レポートの distractors は nil のときだけ
// 「紛れ込み無しで測った」を意味する (GO-004: nil の意味は一つ)。
func TestLoadDistractorFileRejectsEmpty(t *testing.T) {
	_, _, err := eval.LoadDistractorFile(
		eval.SourceFile{Path: "bin/empty.jsonl", Content: []byte("\n# コメントだけ\n")})
	if !errors.Is(err, eval.ErrInvalidDataset) {
		t.Fatalf("err = %v, want ErrInvalidDataset", err)
	}
}

// TestEncodeDistractorRoundTrips は書き出した行がそのまま読み戻せることを見る。
//
// 🔑 生成側 (tools/wikidistract) と読み込み側が同じ型を共有していることの検査で
// ある。10万件はリポジトリに入らないので (ADR 0019)、様式のずれを捕まえる
// 手立てはこれとコンパイルしかない。
func TestEncodeDistractorRoundTrips(t *testing.T) {
	want := eval.Distractor{
		DocumentID: 9012345, SourceID: 9000000, ChunkIndex: 7,
		Content: "記号 & を含む本文。",
	}

	line, err := eval.EncodeDistractor(want)
	if err != nil {
		t.Fatalf("EncodeDistractor: %v", err)
	}

	got, err := eval.LoadDistractors(strings.NewReader(string(line) + "\n"))
	if err != nil {
		t.Fatalf("LoadDistractors: %v", err)
	}

	if len(got) != 1 || got[0] != want {
		t.Fatalf("往復した値 = %+v, want %+v", got, want)
	}

	// 1行に収まっていること。JSONL は行が単位である。
	if strings.Contains(string(line), "\n") {
		t.Errorf("書き出しに改行が混ざった: %q", line)
	}
}

// TestEncodeDistractorHasNoEvalKey は書き出しに eval_key が現れないことを見る。
func TestEncodeDistractorHasNoEvalKey(t *testing.T) {
	line, err := eval.EncodeDistractor(eval.Distractor{
		DocumentID: 1, SourceID: 1, ChunkIndex: 0, Content: "本文",
	})
	if err != nil {
		t.Fatalf("EncodeDistractor: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if _, found := fields["eval_key"]; found {
		t.Errorf("eval_key が書き出された: %s", line)
	}
}

// distractorLines は n 件ぶんの JSONL を作る。
func distractorLines(n int) []eval.Distractor {
	out := make([]eval.Distractor, 0, n)
	for i := range n {
		out = append(out, eval.Distractor{
			DocumentID: int64(9000000 + i), SourceID: 9000000, ChunkIndex: 0,
			Content: fmt.Sprintf("紛れ込みの本文 %d", i),
		})
	}

	return out
}
