package eval_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// TestLoadTagVocabularyReadsTheDeclaration は語彙の読み取りを見る。
func TestLoadTagVocabularyReadsTheDeclaration(t *testing.T) {
	const in = `{"tags":[
		{"name":"語彙一致","description":"本文の語がそのまま出るクエリ"},
		{"name":"言い換え","description":"本文と語が重ならないクエリ"}
	]}`

	got, err := eval.LoadTagVocabulary(strings.NewReader(in))
	if err != nil {
		t.Fatalf("LoadTagVocabulary: %v", err)
	}

	names := got.Names()
	if len(names) != 2 || names[0] != "語彙一致" || names[1] != "言い換え" {
		t.Errorf("Names = %v", names)
	}
}

// TestLoadTagVocabularyRejectsBrokenDeclarations は語彙の壊れ方を縛る。
//
// description の必須がここに含まれる。分類の基準が書かれていなければ、
// 次に注釈を書く人は別の基準で同じタグを使い、タグ別の集計が意味を失う。
func TestLoadTagVocabularyRejectsBrokenDeclarations(t *testing.T) {
	cases := map[string]string{
		"タグが空":     `{"tags":[]}`,
		"name が空":  `{"tags":[{"name":"","description":"d"}]}`,
		"説明が空":     `{"tags":[{"name":"n","description":""}]}`,
		"名前の重複":    `{"tags":[{"name":"n","description":"d"},{"name":"n","description":"e"}]}`,
		"未知のフィールド": `{"tags":[{"name":"n","description":"d"}],"tag":[]}`,
		"壊れた JSON": `{"tags":`,
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := eval.LoadTagVocabulary(strings.NewReader(in))
			if !errors.Is(err, eval.ErrInvalidDataset) {
				t.Errorf("err = %v, want ErrInvalidDataset", err)
			}
		})
	}
}
