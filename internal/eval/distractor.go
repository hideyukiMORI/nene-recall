package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// DistractorKeyPrefix は紛れ込みに付ける表示名の接頭辞。
//
// 🔴 紛れ込みは eval_key を**持たない**。正解注釈から参照されることが無いので、
// 安定キーを人が付ける理由が無いからである
// (docs/adr/0019-large-scale-benchmark-corpus.md Decision 1)。
//
// 一方で、レポートの ranked_keys には「上位に何が入ったか」を書かなければ
// ならない。押し出しが起きたとき、押し出した側の名前が無いレポートは
// 「正解が消えた」としか読めない。そこで表示名だけを document_id と
// chunk_index から機械的に作る。接頭辞があるので、人が付ける eval_key
// （例 "adr-0007#003"）と衝突しない。
const DistractorKeyPrefix = "distractor:"

// Distractor は正解にならない紛れ込み1件。
//
// 🔴 Passage と別の型にしてある。Passage は eval_key と source（人が付ける
// 安定キーと文書名）を持ち、id は初出順に採番される。紛れ込みは逆で、
// eval_key を持たず document_id / source_id を明示する——評価セットと id が
// 衝突しない範囲（9,000,000 以降）に置く必要があるためで、採番に任せると
// 評価コーパスの document_id と重なる (ADR 0019 Decision 1)。
//
// ゼロ値は無効である。LoadDistractors を通すこと。
type Distractor struct {
	// DocumentID は取り込み元の文書。ページ ID に下駄を履かせた値。
	DocumentID int64 `json:"document_id"`
	// SourceID は取り込み元。紛れ込みは全件が同じ source に属する。
	SourceID int64 `json:"source_id"`
	// ChunkIndex は文書内の通番。
	ChunkIndex int `json:"chunk_index"`
	// Content は本文。埋め込みの入力そのものである。
	Content string `json:"content"`
}

// Key はレポートに出す表示名を返す。
//
// 正解注釈からは参照されない。DistractorKeyPrefix の doc を参照。
func (d Distractor) Key() string {
	return fmt.Sprintf("%s%d#%d", DistractorKeyPrefix, d.DocumentID, d.ChunkIndex)
}

// LoadDistractors は紛れ込みの JSONL を読む。
//
// 🔴 eval_key を書いた行はエラーになる（decodeLine が未知のフィールドを拒否する）。
// 紛れ込みに正解キーを与える経路を作らないための、境界での歯止めである。
// 与えられてしまうと、注釈が指していないのに「正解になりうる行」が生まれ、
// 評価セットの意味が静かに変わる。
func LoadDistractors(r io.Reader) ([]Distractor, error) {
	lines, err := splitJSONL(r)
	if err != nil {
		return nil, err
	}

	out := make([]Distractor, 0, len(lines))
	seen := make(map[string]int, len(lines))

	for _, line := range lines {
		d, err := decodeLine[Distractor](line)
		if err != nil {
			return nil, err
		}

		if err := d.validate(line.number); err != nil {
			return nil, err
		}

		if first, dup := seen[d.Key()]; dup {
			return nil, fmt.Errorf(
				"%w: line %d: document_id %d chunk_index %d is already defined on line %d",
				ErrInvalidDataset, line.number, d.DocumentID, d.ChunkIndex, first)
		}

		seen[d.Key()] = line.number
		out = append(out, d)
	}

	return out, nil
}

// validate は1件の紛れ込みに必要な値が揃っているかを見る。
//
// id を検査するのは、ゼロ値のまま投入すると評価コーパス側の document_id と
// 衝突しうるからである。衝突しても計測は止まらず、document_id で絞り込む
// 検索だけが静かに別のものを返すようになる。
func (d Distractor) validate(line int) error {
	switch {
	case d.Content == "":
		return fmt.Errorf("%w: line %d: content is required", ErrInvalidDataset, line)
	case d.DocumentID < 1:
		return fmt.Errorf("%w: line %d: document_id must be positive, got %d",
			ErrInvalidDataset, line, d.DocumentID)
	case d.SourceID < 1:
		return fmt.Errorf("%w: line %d: source_id must be positive, got %d",
			ErrInvalidDataset, line, d.SourceID)
	case d.ChunkIndex < 0:
		return fmt.Errorf("%w: line %d: chunk_index must not be negative, got %d",
			ErrInvalidDataset, line, d.ChunkIndex)
	}

	return nil
}

// EncodeDistractor は1件を JSONL の1行にする。
//
// 🔑 書き出す側 (tools/wikidistract) と読む側がこの1つの型を共有するので、
// 項目名がずれたらコンパイルが通らない。生成した 10万件がリポジトリに
// 入らない以上 (ADR 0019)、様式のずれを検査で捕まえる手立てはこれしかない。
func EncodeDistractor(d Distractor) ([]byte, error) {
	encoded, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("%w: encode distractor: %w", ErrInvalidDataset, err)
	}

	return encoded, nil
}

// distractorInputFor は紛れ込みの入力の同一性を組み立てる。
//
// count は解析後の件数で埋める。Passage 側の fileInput と同じ流儀である。
func distractorInputFor(file SourceFile, count int) (FileInput, error) {
	sum, err := SHA256(bytes.NewReader(file.Content))
	if err != nil {
		return FileInput{}, fmt.Errorf("%s: %w", file.Path, err)
	}

	return FileInput{Path: file.Path, SHA256: sum, Count: count}, nil
}

// LoadDistractorFile は紛れ込みのファイルを読み、同一性の記録まで作る。
//
// 🔴 記録を任意にしない。件数と sha256 の無いレポートは、259 件で測った
// 数字と並べて読めない (ADR 0019 Decision 2)。読み込みと記録を1つの関数に
// して、片方だけ行う経路を無くしてある。
func LoadDistractorFile(file SourceFile) ([]Distractor, FileInput, error) {
	distractors, err := LoadDistractors(bytes.NewReader(file.Content))
	if err != nil {
		return nil, FileInput{}, fmt.Errorf("%s: %w", file.Path, err)
	}

	if len(distractors) == 0 {
		return nil, FileInput{}, fmt.Errorf("%w: %s: the distractor file is empty",
			ErrInvalidDataset, file.Path)
	}

	input, err := distractorInputFor(file, len(distractors))
	if err != nil {
		return nil, FileInput{}, err
	}

	return distractors, input, nil
}

// validateDistractorRecord は紛れ込みの中身と条件の記録が食い違っていないかを見る。
//
// 🔴 投入したのに記録が無い（またはその逆）まま計測を通さない。レポートの
// conditions が実際の条件と違えば、それは正本になれない (ADR 0013)。
func validateDistractorRecord(distractors []Distractor, record *FileInput) error {
	switch {
	case len(distractors) > 0 && record == nil:
		return fmt.Errorf("%w: %d distractors were given without an input record",
			ErrMeasure, len(distractors))
	case len(distractors) == 0 && record != nil:
		return fmt.Errorf("%w: the conditions record distractors but none were given",
			ErrMeasure)
	case record != nil && record.Count != len(distractors):
		return fmt.Errorf("%w: the conditions record %d distractors but %d were given",
			ErrMeasure, record.Count, len(distractors))
	}

	return nil
}

// keyPrefixConflict は人が付けた eval_key が紛れ込みの接頭辞を使っていないかを見る。
//
// 衝突すると写像が上書きされ、正解チャンクが「紛れ込みとして返ってきた」形に
// なる。症状は recall の低下だけなので気づけない。
func keyPrefixConflict(passages []Passage) error {
	for _, p := range passages {
		if strings.HasPrefix(p.Key, DistractorKeyPrefix) {
			return fmt.Errorf("%w: eval_key %q uses the reserved prefix %q",
				ErrInvalidDataset, p.Key, DistractorKeyPrefix)
		}
	}

	return nil
}
