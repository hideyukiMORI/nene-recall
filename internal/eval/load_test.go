package eval_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// validTags は最小の正しいタグ語彙。
const validTags = `{"tags":[{"name":"語彙一致","description":"本文の語がそのまま出るクエリ"}]}`

// evalSources は評価セットの3ファイル。
//
// 同じ型の値を3つ返す関数にすると呼び出し側で取り違えるので、名前付きで束ねる。
type evalSources struct {
	corpus  eval.SourceFile
	queries eval.SourceFile
	tags    eval.SourceFile
}

// validSources は整合した3ファイルを組む。
func validSources() evalSources {
	return evalSources{
		corpus:  eval.SourceFile{Path: "corpus.jsonl", Content: []byte(validCorpus)},
		queries: eval.SourceFile{Path: "queries.jsonl", Content: []byte(validQueries)},
		tags:    eval.SourceFile{Path: "tags.json", Content: []byte(validTags)},
	}
}

// load は3ファイルを読み込む。
func (s evalSources) load() (eval.LoadedDataset, error) {
	loaded, err := eval.LoadDataset(s.corpus, s.queries, s.tags)
	if err != nil {
		return eval.LoadedDataset{}, fmt.Errorf("load dataset: %w", err)
	}

	return loaded, nil
}

// TestLoadDatasetRecordsTheIdentityOfEveryInput は、3ファイルの sha256 と
// 件数がレポート用に記録されることを見る。
//
// 🔑 「同じ評価セットで測った」をファイル名や日付ではなく内容で言えることが、
// レポートが後から検証できる条件である (ADR 0013)。
func TestLoadDatasetRecordsTheIdentityOfEveryInput(t *testing.T) {
	got, err := validSources().load()
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}

	inputs := got.Inputs
	if inputs.Corpus.Count != 3 || inputs.Queries.Count != 1 || inputs.Tags.Count != 1 {
		t.Errorf("件数の記録 = %+v", inputs)
	}

	if inputs.Corpus.Path != "corpus.jsonl" {
		t.Errorf("Path = %q", inputs.Corpus.Path)
	}

	for _, in := range []eval.FileInput{inputs.Corpus, inputs.Queries, inputs.Tags} {
		if len(in.SHA256) != 64 {
			t.Errorf("%s の sha256 = %q", in.Path, in.SHA256)
		}
	}

	// 中身が違えばハッシュも違う。
	if inputs.Corpus.SHA256 == inputs.Queries.SHA256 {
		t.Error("違うファイルが同じハッシュになった")
	}
}

// TestLoadDatasetParsesEveryFile は3ファイルが解析されることを見る。
func TestLoadDatasetParsesEveryFile(t *testing.T) {
	got, err := validSources().load()
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}

	if len(got.Dataset.Passages) != 3 {
		t.Errorf("コーパス件数 = %d, want 3", len(got.Dataset.Passages))
	}

	if len(got.Dataset.Queries) != 1 {
		t.Errorf("クエリ件数 = %d, want 1", len(got.Dataset.Queries))
	}

	if names := got.Vocabulary.Names(); len(names) != 1 || names[0] != "語彙一致" {
		t.Errorf("語彙 = %v", names)
	}
}

// TestLoadDatasetValidatesBeforeReturning は、整合していない評価セットが
// 読み込みを通り抜けないことを見る。
//
// 🔴 検査を通っていない評価セットで計測できる経路を作らない。dangling な
// 正解キーを抱えたまま測ると、症状は「recall が低い」だけになる。
func TestLoadDatasetValidatesBeforeReturning(t *testing.T) {
	sources := validSources()
	sources.queries.Content = []byte(
		`{"query_id":"q-1","text":"問い","relevant":["存在しない"],` +
			`"tags":["語彙一致"],"note":"根拠"}`)

	if _, err := sources.load(); !errors.Is(err, eval.ErrInvalidDataset) {
		t.Errorf("err = %v, want ErrInvalidDataset", err)
	}
}

// TestLoadDatasetReportsWhichFileIsBroken は、どのファイルが壊れているかが
// 分かることを見る。3つ読むので、ファイル名が無いと直し先が分からない。
func TestLoadDatasetReportsWhichFileIsBroken(t *testing.T) {
	cases := map[string]func(s *evalSources){
		"corpus.jsonl":  func(s *evalSources) { s.corpus.Content = []byte(`{`) },
		"queries.jsonl": func(s *evalSources) { s.queries.Content = []byte(`{`) },
		"tags.json":     func(s *evalSources) { s.tags.Content = []byte(`{`) },
	}

	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			sources := validSources()
			corrupt(&sources)

			_, err := sources.load()
			if !errors.Is(err, eval.ErrInvalidDataset) {
				t.Fatalf("err = %v, want ErrInvalidDataset", err)
			}

			if !strings.Contains(err.Error(), name) {
				t.Errorf("err = %v, want %q を含むこと", err, name)
			}
		})
	}
}

// TestEncodeReportProducesReadableJSON は、書き出す形が git の差分として
// 読めることを見る。
func TestEncodeReportProducesReadableJSON(t *testing.T) {
	report := eval.NewReport(
		eval.Environment{
			GitRevision: "abc", GitModified: false, GoVersion: "go1.27.0",
			EmbedderID: "bge-m3:1024", OllamaVersion: "0.33.2", ModelDigest: "digest",
			PostgresVersion: "17.11", PgvectorVersion: "0.8.6", GPUNote: "",
		},
		eval.Inputs{
			Corpus:  eval.FileInput{Path: "c", SHA256: "1", Count: 1},
			Queries: eval.FileInput{Path: "q", SHA256: "2", Count: 1},
			Tags:    eval.FileInput{Path: "t", SHA256: "3", Count: 1},
		},
		eval.Measurement{
			Conditions: eval.Conditions{
				OrgID: 1, Alpha: 0.7, AlphaNote: "not tuned", Limit: 10, Rounds: 5,
				WarmupRounds: 1, KValues: eval.KValues(), PercentileMethod: eval.PercentileMethod,
			},
			Queries: nil,
			Summary: eval.Summary{
				QueryCount: 0, Recall: nil, MRR: 0,
				Latency: eval.LatencySummary{
					WithEmbedding:    eval.LatencyStats{Samples: 0, MinMS: 0, P50MS: 0, P95MS: 0, MaxMS: 0},
					WithoutEmbedding: eval.LatencyStats{Samples: 0, MinMS: 0, P50MS: 0, P95MS: 0, MaxMS: 0},
				},
				TagRecall: nil,
			},
		},
		time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	)

	encoded, err := eval.EncodeReport(report)
	if err != nil {
		t.Fatalf("EncodeReport: %v", err)
	}

	if !strings.HasSuffix(string(encoded), "}\n") {
		t.Error("末尾が改行で終わっていない。テキストファイルとして扱えること")
	}

	if !strings.Contains(string(encoded), "\n  \"schema\"") {
		t.Error("インデントされていない。git の差分として読めること")
	}

	// 書き出したものが読み戻せること。レポートは後から検証されるためにある。
	var decoded eval.Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("読み戻せない: %v", err)
	}

	if decoded.Schema != eval.ReportSchema || decoded.Environment.ModelDigest != "digest" {
		t.Errorf("読み戻した内容が違う: %+v", decoded.Environment)
	}
}

// TestRecallValueAt は集計値からの取り出しを見る。
func TestRecallValueAt(t *testing.T) {
	values := []eval.RecallAtK{{K: 1, Value: 0.25}, {K: 10, Value: 0.75}}

	if got := eval.RecallValueAt(values, 10); got != 0.75 {
		t.Errorf("RecallValueAt(10) = %v, want 0.75", got)
	}

	if got := eval.RecallValueAt(values, 5); got != 0 {
		t.Errorf("RecallValueAt(5) = %v, want 0（無い k は 0）", got)
	}
}
