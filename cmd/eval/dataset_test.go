package main_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// evalDir は評価セットの置き場（リポジトリルートからの相対）。
//
// cmd/eval から2つ上がる。テストは常にそのパッケージのディレクトリで走るので、
// この相対パスは実行場所に依存しない。
const evalDir = "../../testdata/eval"

// TestEvaluationDatasetIsConsistent は評価セットが壊れていないことを見る。
//
// 🔑 これが「評価セットを壊すコミットは CI で落ちる」の実体である
// (docs/adr/0013-evaluation-harness-design.md)。
//
// 計測そのもの（make eval）は CI で走らせない——Ollama も GPU も無いし、
// recall@10 = 0.83 は真でも偽でもないので自動 fail の閾値を切れない。
// 一方この検査は真偽が決まる。「注釈がコーパスに存在しないキーを指している」は
// 数字の揺らぎではなく、間違いである。
//
// 🔴 この検査を cmd に置いているのは、testdata の読み取りに os が要るためである。
// internal/eval は depguard の env-is-read-in-config-only により os を import
// できず、cmd はその除外対象になっている (ARC-005)。
//
// ⚠️ 現在 testdata/eval に入っているのはダミーで、実データではない
// （評価セットの中身は施主の判断待ち）。実データが入れば、この検査は
// そのまま実データに効く。
func TestEvaluationDatasetIsConsistent(t *testing.T) {
	// 読めれば整合している。LoadDataset が検査まで済ませて返す。
	loadEvalDataset(t)
}

// TestEvaluationDatasetHasDistractors は、正解にならないチャンクが
// コーパスに入っていることを見る。
//
// 🔴 全部が正解のコーパスは、順位付けを何も試していないのと同じである。
// 紛れ込みが無ければ recall は自明に 1.0 になり、指標が何も語らない。
func TestEvaluationDatasetHasDistractors(t *testing.T) {
	loaded := loadEvalDataset(t)

	relevant := map[string]bool{}

	for _, q := range loaded.Dataset.Queries {
		for _, key := range q.Relevant {
			relevant[key] = true
		}
	}

	for _, p := range loaded.Dataset.Passages {
		if !relevant[p.Key] {
			return // 紛れ込みが1件でもあればよい
		}
	}

	t.Error("コーパスの全チャンクがどれかのクエリの正解になっている。" +
		"正解にならないチャンク（distractor）を入れること")
}

// TestEvaluationQueriesFitWithinTheLimit は、正解の件数が limit を
// 超えていないことを見る。
//
// ⚠️ 正解が limit 件を超えるクエリは recall@10 が決して 1.0 に届かない。
// 定義どおりの挙動だが、注釈を書く側が踏む罠なので検査で気づけるようにする。
// 超える注釈が必要になったら limit と k を再判断すること (ADR 0013 Decision 9)。
func TestEvaluationQueriesFitWithinTheLimit(t *testing.T) {
	loaded := loadEvalDataset(t)

	for _, q := range loaded.Dataset.Queries {
		if len(q.Relevant) > eval.DefaultLimit {
			t.Errorf("%s の正解が %d 件で limit %d を超えている。"+
				"recall@%d が 1.0 に届かなくなる",
				q.ID, len(q.Relevant), eval.DefaultLimit, eval.DefaultLimit)
		}
	}
}

// loadEvalDataset は testdata/eval の3ファイルを読む。
//
// eval.LoadDataset が整合性の検査まで済ませて返すので、error が返ることは
// 「評価セットが壊れている」ことを意味する。
func loadEvalDataset(t *testing.T) eval.LoadedDataset {
	t.Helper()

	loaded, err := eval.LoadDataset(
		readEvalFile(t, "corpus.jsonl"),
		readEvalFile(t, "queries.jsonl"),
		readEvalFile(t, "tags.json"),
	)
	if err != nil {
		t.Fatalf("評価セットが整合していない:\n%v", err)
	}

	return loaded
}

// readEvalFile は評価セットのファイルを読む。
func readEvalFile(t *testing.T, name string) eval.SourceFile {
	t.Helper()

	path := filepath.Join(evalDir, name)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s を読めない: %v", name, err)
	}

	return eval.SourceFile{Path: path, Content: raw}
}
