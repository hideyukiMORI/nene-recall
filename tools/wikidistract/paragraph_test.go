package main_test

import (
	"strings"
	"testing"

	main "github.com/hideyukiMORI/nene-recall/tools/wikidistract"
)

// runes は n 文字の日本語を作る。長さの境界を書くために使う。
func runes(n int) string { return strings.Repeat("あ", n) }

// TestSelectorKeepsOnlyTheAllowedLengths は 100〜500 字の境界を固定する。
//
// 🔴 バイト数ではなくルーン数で数える。日本語は1文字3バイトなので、
// バイトで測ると範囲が別物になる。
func TestSelectorKeepsOnlyTheAllowedLengths(t *testing.T) {
	cases := map[string]struct {
		length int
		want   bool
	}{
		"下限の1つ手前": {length: main.MinRunes - 1, want: false},
		"ちょうど下限":  {length: main.MinRunes, want: true},
		"ちょうど上限":  {length: main.MaxRunes, want: true},
		"上限の1つ先":  {length: main.MaxRunes + 1, want: false},
	}

	selector := main.NewSelector()

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := selector.Paragraphs(runes(tc.length))
			if (len(got) == 1) != tc.want {
				t.Fatalf("%d 文字の採否 = %v, want %v", tc.length, len(got) == 1, tc.want)
			}
		})
	}
}

// TestSelectorDropsMarkupResidue は記法の残った段落を捨てることを見る。
//
// 🔴 粗い除去の取りこぼしをここで受け止める。両方が揃って初めて、投入される
// のは記法の残っていない段落だけになる
// (docs/adr/0019-large-scale-benchmark-corpus.md)。
func TestSelectorDropsMarkupResidue(t *testing.T) {
	cases := map[string]string{
		"リンクの残り":    "[[記事]]" + runes(main.MinRunes),
		"テンプレートの残り": "{{Infobox}}" + runes(main.MinRunes),
		"タグの残り":     "<div>" + runes(main.MinRunes),
	}

	selector := main.NewSelector()

	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if got := selector.Paragraphs(line); len(got) != 0 {
				t.Fatalf("採用されてしまった: %q", got)
			}
		})
	}
}

// TestSelectorDropsNonProseLines は散文でない行を捨てることを見る。
func TestSelectorDropsNonProseLines(t *testing.T) {
	body := runes(main.MinRunes)

	cases := map[string]string{
		"見出し":   "== " + body,
		"箇条書き":  "* " + body,
		"番号付き":  "# " + body,
		"定義":    "; " + body,
		"字下げ":   ": " + body,
		"表の行":   "| " + body,
		"表の見出し": "! " + body,
		"整形済み":  " " + body,
		"区切り線":  "---- " + body,
	}

	selector := main.NewSelector()

	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if got := selector.Paragraphs(line); len(got) != 0 {
				t.Fatalf("採用されてしまった: %q", got)
			}
		})
	}
}

// TestSelectorKeepsOrderAndSkipsBlanks は先頭から順に、空行を飛ばして
// 採ることを見る。
//
// 🔑 1行を1段落として扱う。連結して数えると、隣り合う短い行が偶然 100 字を
// 超えて採用され、長さの規則が「行の並び方」を測ることになる。
func TestSelectorKeepsOrderAndSkipsBlanks(t *testing.T) {
	first := runes(main.MinRunes)
	short := runes(main.MinRunes - 1)
	second := runes(main.MinRunes + 1)

	got := main.NewSelector().Paragraphs(
		strings.Join([]string{first, "", short, "  ", second}, "\n"))

	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("採用された段落の数 = %d（順序と空行の扱いを確認すること）", len(got))
	}
}
