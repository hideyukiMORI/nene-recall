package eval_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// validCorpus は最小の正しいコーパス。
const validCorpus = `# 先頭のコメント行は読み飛ばす
{"eval_key":"doc-a#001","source":"doc-a","content":"一つ目の本文"}

{"eval_key":"doc-a#002","source":"doc-a","content":"二つ目の本文"}
{"eval_key":"doc-b#001","source":"doc-b","content":"別の文書の本文"}
`

// validQueries は最小の正しいクエリ集合。
const validQueries = `{"query_id":"q-1","text":"問い","relevant":["doc-a#001"],"tags":["語彙一致"],"note":"根拠"}
`

// TestLoadPassagesReadsJSONL は行の読み取りと、空行・コメント行の読み飛ばしを見る。
func TestLoadPassagesReadsJSONL(t *testing.T) {
	got, err := eval.LoadPassages(strings.NewReader(validCorpus))
	if err != nil {
		t.Fatalf("LoadPassages: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("件数 = %d, want 3 (%v)", len(got), got)
	}

	if got[0].Key != "doc-a#001" || got[0].Source != "doc-a" || got[0].Content != "一つ目の本文" {
		t.Errorf("1件目 = %+v", got[0])
	}

	if got[2].Key != "doc-b#001" {
		t.Errorf("3件目 = %+v", got[2])
	}
}

// TestLoadQueriesReadsJSONL はクエリ側の読み取りを見る。
func TestLoadQueriesReadsJSONL(t *testing.T) {
	got, err := eval.LoadQueries(strings.NewReader(validQueries))
	if err != nil {
		t.Fatalf("LoadQueries: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("件数 = %d, want 1", len(got))
	}

	q := got[0]
	if q.ID != "q-1" || q.Text != "問い" || q.Note != "根拠" {
		t.Errorf("クエリ = %+v", q)
	}

	if len(q.Relevant) != 1 || q.Relevant[0] != "doc-a#001" {
		t.Errorf("relevant = %v", q.Relevant)
	}
}

// TestLoadPassagesRejectsBrokenLines は、注釈の壊れ方を1件ずつ縛る。
//
// 🔴 未知フィールドの拒否がここに含まれる。"contnet" のような綴り誤りを
// 黙って無視すると、本文が空のまま「読めた」ことになり、症状は
// 取り込みエラーや recall の低下として遠くに出る。
func TestLoadPassagesRejectsBrokenLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{name: "eval_key が空", in: `{"eval_key":"","source":"s","content":"c"}`},
		{name: "source が空", in: `{"eval_key":"k","source":"","content":"c"}`},
		{name: "content が空", in: `{"eval_key":"k","source":"s","content":""}`},
		{name: "未知のフィールド", in: `{"eval_key":"k","source":"s","content":"c","contnet":"x"}`},
		{name: "壊れた JSON", in: `{"eval_key":"k",`},
		{name: "1行に2つの値", in: `{"eval_key":"k","source":"s","content":"c"} {"eval_key":"j","source":"s","content":"c"}`},
		{
			name: "eval_key の重複",
			in: `{"eval_key":"k","source":"s","content":"c"}` + "\n" +
				`{"eval_key":"k","source":"s","content":"d"}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := eval.LoadPassages(strings.NewReader(c.in))
			if !errors.Is(err, eval.ErrInvalidDataset) {
				t.Errorf("err = %v, want ErrInvalidDataset", err)
			}
		})
	}
}

// TestLoadQueriesRejectsBrokenLines はクエリ側の壊れ方を縛る。
//
// note と tags の必須はここで固定する。note が無い注釈は後から根拠を辿れず、
// tags が無いクエリはタグ別 recall@10 の集計から静かに抜け落ちる。
func TestLoadQueriesRejectsBrokenLines(t *testing.T) {
	const base = `"text":"t","relevant":["k"],"tags":["語彙一致"],"note":"n"`

	cases := []struct {
		name string
		in   string
	}{
		{name: "query_id が空", in: `{"query_id":"",` + base + `}`},
		{name: "text が空", in: `{"query_id":"q","text":"","relevant":["k"],"tags":["語彙一致"],"note":"n"}`},
		{name: "relevant が空", in: `{"query_id":"q","text":"t","relevant":[],"tags":["語彙一致"],"note":"n"}`},
		{name: "tags が空", in: `{"query_id":"q","text":"t","relevant":["k"],"tags":[],"note":"n"}`},
		{name: "note が空", in: `{"query_id":"q","text":"t","relevant":["k"],"tags":["語彙一致"],"note":""}`},
		{
			name: "relevant の重複",
			in:   `{"query_id":"q","text":"t","relevant":["k","k"],"tags":["語彙一致"],"note":"n"}`,
		},
		{name: "未知のフィールド", in: `{"query_id":"q",` + base + `,"relevent":["x"]}`},
		{
			name: "query_id の重複",
			in:   `{"query_id":"q",` + base + "}\n" + `{"query_id":"q",` + base + `}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := eval.LoadQueries(strings.NewReader(c.in))
			if !errors.Is(err, eval.ErrInvalidDataset) {
				t.Errorf("err = %v, want ErrInvalidDataset", err)
			}
		})
	}
}

// TestLoadPassagesReportsTheLineNumber は、誤りの場所が分かることを見る。
//
// 注釈を直すのは人なので、「どこかが壊れている」では直せない。
func TestLoadPassagesReportsTheLineNumber(t *testing.T) {
	in := `{"eval_key":"a","source":"s","content":"c"}` + "\n" +
		`{"eval_key":"b","source":"s","content":"c"}` + "\n" +
		`{"eval_key":"c","source":"s","content":""}` + "\n"

	_, err := eval.LoadPassages(strings.NewReader(in))
	if err == nil {
		t.Fatal("err = nil, want error")
	}

	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("err = %v, want 行番号 3 を含むこと", err)
	}
}

// TestLoadPassagesRejectsAnOverlongLine は、行が上限を超えたら黙って切らないことを見る。
//
// bufio.Scanner の既定は途中で切れた行をエラーにするが、そのエラーを捨てると
// 「JSON が壊れている」という遠い症状になる。
func TestLoadPassagesRejectsAnOverlongLine(t *testing.T) {
	huge := `{"eval_key":"k","source":"s","content":"` + strings.Repeat("あ", 700_000) + `"}`

	_, err := eval.LoadPassages(strings.NewReader(huge))
	if !errors.Is(err, eval.ErrInvalidDataset) {
		t.Errorf("err = %v, want ErrInvalidDataset", err)
	}
}

// TestValidateDatasetCatchesDanglingRelevantKeys は、正解注釈がコーパスに
// 存在しないキーを指している場合を見る。
//
// 🔴 これが評価セットで最も起きやすく、最も気づきにくい壊れ方である。
// 検査が無ければ症状は「recall が下がった」であり、原因が注釈だと分からない。
func TestValidateDatasetCatchesDanglingRelevantKeys(t *testing.T) {
	ds := eval.Dataset{
		Passages: []eval.Passage{{Key: "a", Source: "s", Content: "c"}},
		Queries: []eval.Query{{
			ID: "q-1", Text: "t", Relevant: []string{"a", "missing"},
			Tags: []string{"語彙一致"}, Note: "n",
		}},
	}

	err := eval.ValidateDataset(ds, []string{"語彙一致"})
	if !errors.Is(err, eval.ErrInvalidDataset) {
		t.Fatalf("err = %v, want ErrInvalidDataset", err)
	}

	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v, want 欠けているキー名を含むこと", err)
	}
}

// TestValidateDatasetCatchesUnknownTags は語彙外のタグを拒否することを見る。
func TestValidateDatasetCatchesUnknownTags(t *testing.T) {
	ds := eval.Dataset{
		Passages: []eval.Passage{{Key: "a", Source: "s", Content: "c"}},
		Queries: []eval.Query{{
			ID: "q-1", Text: "t", Relevant: []string{"a"},
			Tags: []string{"未宣言のタグ"}, Note: "n",
		}},
	}

	if err := eval.ValidateDataset(ds, []string{"語彙一致"}); !errors.Is(err, eval.ErrInvalidDataset) {
		t.Errorf("err = %v, want ErrInvalidDataset", err)
	}
}

// TestValidateDatasetReportsEveryProblemAtOnce は、問題を1件ずつではなく
// まとめて返すことを見る。1件ずつだと注釈の修正が往復になる。
func TestValidateDatasetReportsEveryProblemAtOnce(t *testing.T) {
	ds := eval.Dataset{
		Passages: []eval.Passage{{Key: "a", Source: "s", Content: "c"}},
		Queries: []eval.Query{
			{ID: "q-1", Text: "t", Relevant: []string{"x"}, Tags: []string{"語彙一致"}, Note: "n"},
			{ID: "q-2", Text: "t", Relevant: []string{"y"}, Tags: []string{"未宣言"}, Note: "n"},
		},
	}

	err := eval.ValidateDataset(ds, []string{"語彙一致"})
	if err == nil {
		t.Fatal("err = nil, want error")
	}

	for _, want := range []string{"q-1", "q-2", "x", "y", "未宣言"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err に %q が含まれていない: %v", want, err)
		}
	}
}

// TestValidateDatasetRejectsAnEmptyVocabulary は、語彙が空のときに
// 「全部のタグが未知」ではなく設定の誤りとして落ちることを見る。
func TestValidateDatasetRejectsAnEmptyVocabulary(t *testing.T) {
	ds := eval.Dataset{Passages: nil, Queries: nil}

	if err := eval.ValidateDataset(ds, nil); !errors.Is(err, eval.ErrInvalidDataset) {
		t.Errorf("err = %v, want ErrInvalidDataset", err)
	}
}

// TestValidateDatasetAcceptsAConsistentSet は正しい組が通ることを見る。
//
// コーパスに紛れ込み（どのクエリの正解でもないチャンク）があってもよい。
// 紛れ込みが無い評価セットは、順位付けを何も試していないのと同じである。
func TestValidateDatasetAcceptsAConsistentSet(t *testing.T) {
	ds := eval.Dataset{
		Passages: []eval.Passage{
			{Key: "a", Source: "s", Content: "c"},
			{Key: "distractor", Source: "s", Content: "無関係"},
		},
		Queries: []eval.Query{{
			ID: "q-1", Text: "t", Relevant: []string{"a"},
			Tags: []string{"語彙一致"}, Note: "n",
		}},
	}

	if err := eval.ValidateDataset(ds, []string{"語彙一致", "言い換え"}); err != nil {
		t.Errorf("ValidateDataset: %v", err)
	}
}
