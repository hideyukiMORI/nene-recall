package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// TagDefinition はタグ1つの宣言。
//
// 🔴 Description を必須にする。タグは「クエリをどう分類したか」であり、
// 分類の基準が書かれていなければ、次に注釈を書く人は別の基準で同じタグを使う。
// タグ別 recall@10 は分類が一貫していて初めて診断情報になる。
type TagDefinition struct {
	// Name はタグ名。クエリの tags に書く値。
	Name string `json:"name"`
	// Description はこのタグをどんなクエリに付けるか。
	Description string `json:"description"`
}

// TagVocabulary は tags.json の中身。
//
// 🔑 語彙をハーネスに焼き付けず評価セット側に置くのは、どんなカテゴリで
// 検索品質を見るかが評価セットの中身の判断だからである
// (docs/adr/0013-evaluation-harness-design.md)。宣言してから使わせることで、
// 綴りの揺れが「別のカテゴリ」になるのを検査で潰す。
type TagVocabulary struct {
	// Tags は宣言されたタグの一覧。
	Tags []TagDefinition `json:"tags"`
}

// LoadTagVocabulary は tags.json を読む。
func LoadTagVocabulary(r io.Reader) (TagVocabulary, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return TagVocabulary{}, fmt.Errorf("%w: read tag vocabulary: %w", ErrInvalidDataset, err)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	// 未知のフィールドを拒否するのは JSONL のローダと同じ理由である。
	// 綴り誤りを黙って無視すると、語彙が空のまま「読めた」ことになる。
	dec.DisallowUnknownFields()

	var vocabulary TagVocabulary
	if err := dec.Decode(&vocabulary); err != nil {
		return TagVocabulary{}, fmt.Errorf("%w: decode tag vocabulary: %w", ErrInvalidDataset, err)
	}

	if err := vocabulary.validate(); err != nil {
		return TagVocabulary{}, err
	}

	return vocabulary, nil
}

// validate は語彙の中身を確かめる。
func (v TagVocabulary) validate() error {
	if len(v.Tags) == 0 {
		return fmt.Errorf("%w: the tag vocabulary declares no tags", ErrInvalidDataset)
	}

	seen := make(map[string]bool, len(v.Tags))

	for i, tag := range v.Tags {
		switch {
		case tag.Name == "":
			return fmt.Errorf("%w: tags[%d]: name is required", ErrInvalidDataset, i)
		case tag.Description == "":
			return fmt.Errorf("%w: tags[%d] (%q): description is required",
				ErrInvalidDataset, i, tag.Name)
		case seen[tag.Name]:
			return fmt.Errorf("%w: tags[%d]: %q is declared twice", ErrInvalidDataset, i, tag.Name)
		}

		seen[tag.Name] = true
	}

	return nil
}

// Names は宣言されたタグ名だけを返す。ValidateDataset に渡す形である。
func (v TagVocabulary) Names() []string {
	names := make([]string, 0, len(v.Tags))
	for _, tag := range v.Tags {
		names = append(names, tag.Name)
	}

	return names
}
