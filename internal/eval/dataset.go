// Package eval は検索品質を計測する。
//
// 🔴 このパッケージは LLM を1回も呼ばない。recall@k と MRR は正解セットとの
// 突き合わせであり、純粋な集合演算で計算できる。ragas の faithfulness のような
// LLM 審査員は「生成の評価」であって検索の評価ではなく、生成は Recall の
// スコープ外である (docs/adr/0009-retrieval-evaluation-is-in-scope.md)。
// この性質のおかげで、最も価値の高い検証が最も費用のかからない方法で行える。
// 将来 LLM を持ち込む変更は ADR 0009 と
// docs/adr/0013-evaluation-harness-design.md を supersede する形でしか入れられない。
//
// 🔴 正解セットは chunks.id を持たない。id は取り込みのたびに変わる採番なので、
// 書いた瞬間にその正解セットは1回しか再現しなくなる。正解は評価セット側の
// 安定キー eval_key で持ち、採番 id への写像は index.Writer.Put が返す
// 「入力と同じ順の id」から実行時に組み立てる。写像は永続化しない
// (docs/adr/0013-evaluation-harness-design.md)。
//
// 依存は internal/index・internal/chunk・internal/org と標準ライブラリだけに保つ。
// 具体ストアも具体 Embedder も知らない。os も触らない（ARC-005 の depguard が拒否する）。
package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

// maxLineBytes は JSONL の1行の上限。
//
// bufio.Scanner の既定は 64KB で、長い本文のチャンクが混ざると
// 「途中で切れた JSON」という読みにくい失敗になる。上限に達したことは
// エラーとして表面化させる。
const maxLineBytes = 1 << 20

// Passage は評価コーパスの1件。検索対象になるチャンク1つに対応する。
//
// 🔴 chunks.id を持たない。Key が唯一の安定した参照先である。
//
// Source は取り込み元の文書名（例 "adr-0007"）。document_id / source_id は
// この名前の初出順に採番するので、注釈を書く側は数値の id を一切書かない。
type Passage struct {
	// Key は正解注釈が指す安定キー。例 "adr-0007#003"。
	Key string `json:"eval_key"`
	// Source は取り込み元の文書名。同じ Source のチャンクは並び順に chunk_index を持つ。
	Source string `json:"source"`
	// Content は本文。埋め込みの入力そのものである。
	Content string `json:"content"`
}

// Query は評価クエリ1件と、その正解注釈。
//
// Note を必須にしているのは、「なぜこれが正解なのか」が失われると、
// 後から注釈を見直すときに判断をやり直せなくなるためである。
// 評価セットで最も高価な資産は人手の注釈であり、その根拠も資産に含まれる。
type Query struct {
	// ID はクエリの識別子。例 "q-012"。
	ID string `json:"query_id"`
	// Text は検索語。
	Text string `json:"text"`
	// Relevant は正解チャンクの eval_key。1件以上を要求する。
	Relevant []string `json:"relevant"`
	// Tags はクエリの分類。タグ別 recall@10 の集計単位になる。1件以上を要求する。
	Tags []string `json:"tags"`
	// Note はこの注釈の根拠。空を許さない。
	Note string `json:"note"`
}

// Dataset は評価セット全体。
type Dataset struct {
	// Passages は検索対象のコーパス。正解にならない紛れ込み（distractor）を含んでよい。
	Passages []Passage
	// Queries は評価クエリ。
	Queries []Query
}

// jsonlLine は JSONL の1行と、その行番号。
//
// 行番号を持ち回るのは、注釈の誤りを直すのが人だからである。
// 「どこかが壊れている」ではなく「何行目が壊れている」と言えないと直せない。
type jsonlLine struct {
	number int
	raw    []byte
}

// LoadPassages は corpus.jsonl を読む。
//
// eval_key の重複はエラーにする。重複を許すと、正解注釈がどちらを指すのか
// 決まらないまま計測が通ってしまう。
func LoadPassages(r io.Reader) ([]Passage, error) {
	lines, err := splitJSONL(r)
	if err != nil {
		return nil, err
	}

	passages := make([]Passage, 0, len(lines))
	seen := make(map[string]int, len(lines))

	for _, line := range lines {
		p, err := decodeLine[Passage](line)
		if err != nil {
			return nil, err
		}

		if err := p.validate(line.number); err != nil {
			return nil, err
		}

		if first, dup := seen[p.Key]; dup {
			return nil, fmt.Errorf("%w: line %d: eval_key %q is already defined on line %d",
				ErrInvalidDataset, line.number, p.Key, first)
		}

		seen[p.Key] = line.number
		passages = append(passages, p)
	}

	return passages, nil
}

// LoadQueries は queries.jsonl を読む。
func LoadQueries(r io.Reader) ([]Query, error) {
	lines, err := splitJSONL(r)
	if err != nil {
		return nil, err
	}

	queries := make([]Query, 0, len(lines))
	seen := make(map[string]int, len(lines))

	for _, line := range lines {
		q, err := decodeLine[Query](line)
		if err != nil {
			return nil, err
		}

		if err := q.validate(line.number); err != nil {
			return nil, err
		}

		if first, dup := seen[q.ID]; dup {
			return nil, fmt.Errorf("%w: line %d: query_id %q is already defined on line %d",
				ErrInvalidDataset, line.number, q.ID, first)
		}

		seen[q.ID] = line.number
		queries = append(queries, q)
	}

	return queries, nil
}

// validate は1件のコーパス項目に必要な値が揃っているかを見る。
func (p Passage) validate(line int) error {
	switch {
	case p.Key == "":
		return fmt.Errorf("%w: line %d: eval_key is required", ErrInvalidDataset, line)
	case p.Source == "":
		return fmt.Errorf("%w: line %d: source is required (%q)", ErrInvalidDataset, line, p.Key)
	case p.Content == "":
		return fmt.Errorf("%w: line %d: content is required (%q)", ErrInvalidDataset, line, p.Key)
	}

	return nil
}

// validate は1件のクエリに必要な値が揃っているかを見る。
//
// Relevant の重複を拒否するのは、recall@k の分母が |relevant| だからである。
// 同じキーを2回書くと分母だけが増え、上限 1.0 に届かない指標になる。
// これは「静かに低い数字が出る」形の壊れ方で、気づくのが難しい。
func (q Query) validate(line int) error {
	switch {
	case q.ID == "":
		return fmt.Errorf("%w: line %d: query_id is required", ErrInvalidDataset, line)
	case q.Text == "":
		return fmt.Errorf("%w: line %d: text is required (%q)", ErrInvalidDataset, line, q.ID)
	case len(q.Relevant) == 0:
		return fmt.Errorf("%w: line %d: relevant must have at least one eval_key (%q)",
			ErrInvalidDataset, line, q.ID)
	case len(q.Tags) == 0:
		return fmt.Errorf("%w: line %d: tags must have at least one tag (%q)",
			ErrInvalidDataset, line, q.ID)
	case q.Note == "":
		return fmt.Errorf("%w: line %d: note is required (%q)", ErrInvalidDataset, line, q.ID)
	}

	return duplicateIn(q.Relevant, line, q.ID)
}

// duplicateIn は正解キーの重複を1つ見つけたら報告する。
func duplicateIn(keys []string, line int, queryID string) error {
	seen := make(map[string]bool, len(keys))

	for _, key := range keys {
		if seen[key] {
			return fmt.Errorf("%w: line %d: %q lists relevant key %q twice",
				ErrInvalidDataset, line, queryID, key)
		}

		seen[key] = true
	}

	return nil
}

// ValidateDataset は2つのファイルにまたがる整合性を確かめる。
//
// 🔴 ここが「評価セットを壊すコミットは CI で落ちる」の中身である
// (docs/adr/0013-evaluation-harness-design.md)。計測は CI で走らせないが、
// この検査は真偽が決まるので走らせる。
//
// 見つかった問題をすべて返す。1件ずつ直させると、注釈の修正が往復になる。
func ValidateDataset(ds Dataset, allowedTags []string) error {
	if len(allowedTags) == 0 {
		return fmt.Errorf("%w: the allowed tag vocabulary is empty", ErrInvalidDataset)
	}

	keys := make(map[string]bool, len(ds.Passages))
	for _, p := range ds.Passages {
		keys[p.Key] = true
	}

	var problems []error

	for _, q := range ds.Queries {
		problems = append(problems, danglingRelevant(q, keys)...)
		problems = append(problems, unknownTags(q, allowedTags)...)
	}

	return errors.Join(problems...)
}

// danglingRelevant は corpus に存在しない正解キーを報告する。
//
// 🔴 これが評価セットで最も起きやすい壊れ方である。コーパスを作り直すと
// eval_key が変わりうるが、注釈は古いキーを指したまま残る。検査が無ければ
// 症状は「recall が下がった」であって、原因が注釈だとは分からない。
func danglingRelevant(q Query, keys map[string]bool) []error {
	var problems []error

	for _, key := range q.Relevant {
		if !keys[key] {
			problems = append(problems, fmt.Errorf(
				"%w: query %q refers to eval_key %q, which is not in the corpus",
				ErrInvalidDataset, q.ID, key))
		}
	}

	return problems
}

// unknownTags は語彙に無いタグを報告する。
//
// タグ別 recall@10 の集計単位なので、綴りの揺れがそのまま「別のカテゴリ」になる。
// 語彙を宣言してから使わせることで、揺れを検査で潰す。
func unknownTags(q Query, allowedTags []string) []error {
	var problems []error

	for _, tag := range q.Tags {
		if !slices.Contains(allowedTags, tag) {
			problems = append(problems, fmt.Errorf(
				"%w: query %q uses tag %q, which is not in the declared vocabulary",
				ErrInvalidDataset, q.ID, tag))
		}
	}

	return problems
}

// splitJSONL は入力を行に割る。空行と行頭 # のコメント行は読み飛ばす。
//
// コメントを許すのは、注釈ファイルに由来や区切りを書けるようにするため。
// JSON の仕様外なので、読み飛ばしはここ1箇所に閉じる。
func splitJSONL(r io.Reader) ([]jsonlLine, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxLineBytes)

	var lines []jsonlLine

	number := 0

	for scanner.Scan() {
		number++

		text := bytes.TrimSpace(scanner.Bytes())
		if len(text) == 0 || text[0] == '#' {
			continue
		}

		// Bytes() が返すのは次の Scan で上書きされる領域なので、必ず複製する。
		lines = append(lines, jsonlLine{number: number, raw: bytes.Clone(text)})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: read line %d: %w", ErrInvalidDataset, number+1, err)
	}

	return lines, nil
}

// decodeLine は1行を T に読む。
//
// 🔴 未知のフィールドを拒否する。"relevent" のような綴り誤りを黙って無視すると、
// 正解注釈が空のまま計測が通り、recall が 0 になる。原因が注釈の綴りだとは
// まず分からない。境界で落とすのが最も安い。
func decodeLine[T any](line jsonlLine) (T, error) {
	var value T

	dec := json.NewDecoder(bytes.NewReader(line.raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&value); err != nil {
		return value, fmt.Errorf("%w: line %d: %w", ErrInvalidDataset, line.number, err)
	}

	// 1行に2つ目の JSON 値が続いていたら、書き手の意図と読み手の解釈がずれている。
	if dec.More() {
		return value, fmt.Errorf("%w: line %d: trailing content after the json object",
			ErrInvalidDataset, line.number)
	}

	return value, nil
}
