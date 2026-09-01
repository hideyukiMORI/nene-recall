package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SourceFile は読み込んだ評価セットのファイル1つ。
//
// 🔴 パスではなく中身を受け取る。このパッケージは os を import できない
// （ARC-005 の depguard が拒否する）ので、ファイルを開くのは配線点の仕事である。
// 一方で「解析」と「同一性の記録」はここに置く——配線点に置くとテストが書けず、
// レポートの正しさを支える部分が検査されないまま残る。
type SourceFile struct {
	// Path は読み込んだ場所。レポートにそのまま載る。
	Path string
	// Content はファイルの中身。
	//
	// 一度読んだものを使い回すこと。ハッシュと解析で別々に開くと、その間に
	// 内容が変わりうる——レポートが「この sha256 の入力で測った」と言えなくなる。
	Content []byte
}

// LoadedDataset は読み込み済みの評価セットと、その同一性の記録。
type LoadedDataset struct {
	// Dataset はコーパスとクエリ。
	Dataset Dataset
	// Vocabulary は宣言されたタグ語彙。
	Vocabulary TagVocabulary
	// Inputs は3ファイルの sha256 と件数。
	Inputs Inputs
}

// LoadDataset は3つのファイルから評価セットを組み立て、整合性まで確かめる。
//
// 🔴 整合性の検査をここに含めるのは、検査を通っていない評価セットで計測できる
// 経路を作らないためである。dangling な正解キーを抱えたまま測ると、症状は
// 「recall が低い」だけになり、原因が注釈だとは分からない (ADR 0013)。
func LoadDataset(corpus, queries, tags SourceFile) (LoadedDataset, error) {
	passages, corpusInput, err := loadPassageFile(corpus)
	if err != nil {
		return LoadedDataset{}, err
	}

	parsedQueries, queryInput, err := loadQueryFile(queries)
	if err != nil {
		return LoadedDataset{}, err
	}

	vocabulary, tagInput, err := loadTagFile(tags)
	if err != nil {
		return LoadedDataset{}, err
	}

	dataset := Dataset{Passages: passages, Queries: parsedQueries}
	if err := ValidateDataset(dataset, vocabulary.Names()); err != nil {
		return LoadedDataset{}, err
	}

	return LoadedDataset{
		Dataset:    dataset,
		Vocabulary: vocabulary,
		Inputs:     Inputs{Corpus: corpusInput, Queries: queryInput, Tags: tagInput},
	}, nil
}

// loadPassageFile は corpus.jsonl を解析する。
func loadPassageFile(file SourceFile) ([]Passage, FileInput, error) {
	input, err := file.fileInput()
	if err != nil {
		return nil, FileInput{}, err
	}

	passages, err := LoadPassages(bytes.NewReader(file.Content))
	if err != nil {
		return nil, FileInput{}, fmt.Errorf("%s: %w", file.Path, err)
	}

	input.Count = len(passages)

	return passages, input, nil
}

// loadQueryFile は queries.jsonl を解析する。
func loadQueryFile(file SourceFile) ([]Query, FileInput, error) {
	input, err := file.fileInput()
	if err != nil {
		return nil, FileInput{}, err
	}

	queries, err := LoadQueries(bytes.NewReader(file.Content))
	if err != nil {
		return nil, FileInput{}, fmt.Errorf("%s: %w", file.Path, err)
	}

	input.Count = len(queries)

	return queries, input, nil
}

// loadTagFile は tags.json を解析する。
func loadTagFile(file SourceFile) (TagVocabulary, FileInput, error) {
	input, err := file.fileInput()
	if err != nil {
		return TagVocabulary{}, FileInput{}, err
	}

	vocabulary, err := LoadTagVocabulary(bytes.NewReader(file.Content))
	if err != nil {
		return TagVocabulary{}, FileInput{}, fmt.Errorf("%s: %w", file.Path, err)
	}

	input.Count = len(vocabulary.Tags)

	return vocabulary, input, nil
}

// fileInput は内容ハッシュを取って同一性の記録にする。件数は解析後に埋める。
func (f SourceFile) fileInput() (FileInput, error) {
	sum, err := SHA256(bytes.NewReader(f.Content))
	if err != nil {
		return FileInput{}, fmt.Errorf("%s: %w", f.Path, err)
	}

	return FileInput{Path: f.Path, SHA256: sum, Count: 0}, nil
}

// EncodeReport はレポートを書き出せる JSON にする。
//
// インデント付きにするのは、git の差分として読めるようにするため。
// docs/benchmarks/data/ にコミットして前回と並べるのが使い方である。
// 末尾に改行を付けるのは、テキストファイルとして扱えるようにするため。
func EncodeReport(report Report) ([]byte, error) {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: encode report: %w", ErrInvalidDataset, err)
	}

	return append(encoded, '\n'), nil
}

// RecallValueAt は集計値から k を指定して recall を取り出す。
//
// レポートを読む側（ログ出力や比較スクリプト）が使う。見つからなければ 0 を返す。
func RecallValueAt(values []RecallAtK, k int) float64 {
	for _, v := range values {
		if v.K == k {
			return v.Value
		}
	}

	return 0
}
