package main

import (
	"strings"
	"unicode/utf8"
)

// MinRunes / MaxRunes は採用する段落の長さ（文字数）。
//
// 🔑 100〜500 字は評価セットのチャンクと同じ桁である
// (testdata/eval/README.md: 平均 327字)。distractor が極端に短い／長いと、
// 押し出しの起きやすさが長さの偏りで決まってしまい、10万件を足した効果と
// 見分けられなくなる (docs/adr/0019-large-scale-benchmark-corpus.md)。
const (
	MinRunes = 100
	MaxRunes = 500
)

// residues は段落に残っていたら捨てる記法の断片。
//
// 🔴 Stripper を通しても記法が残ることはある。残った段落を投入すると、
// distractor の分布が「現実の日本語」から離れる。粗い除去の取りこぼしを
// ここで受け止めるのが、パーサを書かずに済ませている理由である。
//
// 関数で返すのは可変のパッケージ変数を作らないため (GO-007)。
func residues() []string { return []string{"[[", "{{", "<"} }

// nonProsePrefixes は行頭に来たら散文ではないと判断する記号。
//
// 見出し (=)・箇条書き (*#:;)・表の断片 (|!{}) ・整形済みブロック (空白始まり) は
// 本文の段落ではない。
func nonProsePrefixes() []string {
	return []string{"=", "*", "#", ":", ";", "|", "!", "{", "}", " ", "\t", "-"}
}

// Selector は素の文から distractor にする段落を選ぶ。
//
// 🔴 規則は決定的である。同じ入力から必ず同じ並びの同じ段落が出る。
// 出力の sha256 を README に記録して再現性を担保する設計なので
// (docs/adr/0019-large-scale-benchmark-corpus.md)、乱択も並列順も持ち込まない。
type Selector struct {
	// Min は採用する下限の文字数（含む）。
	Min int
	// Max は採用する上限の文字数（含む）。
	Max int
}

// NewSelector は既定の長さで Selector を作る。
func NewSelector() Selector { return Selector{Min: MinRunes, Max: MaxRunes} }

// Paragraphs は素の文を行に割り、採用する段落だけを先頭から順に返す。
//
// 1行を1段落として扱う。wikitext では段落の区切りが空行であり、段落の中で
// 改行しない書き方が標準なので、行と段落はほぼ一致する。連結して数えると、
// 隣り合う短い行が偶然 100 字を超えて採用される——長さの規則が「段落の長さ」
// ではなく「行の並び方」を測ることになる。
func (s Selector) Paragraphs(text string) []string {
	var out []string

	for _, line := range strings.Split(text, "\n") {
		candidate, ok := s.accept(line)
		if !ok {
			continue
		}

		out = append(out, candidate)
	}

	return out
}

// accept は1行を見て、採用するなら整形した本文を返す。
func (s Selector) accept(line string) (string, bool) {
	// 🔴 前後の空白を落とす**前に**行頭を見る。wikitext では行頭の空白が
	// 整形済みブロックを意味するので、先に落とすとその印が消えて、
	// 表組みやソースコードが散文として投入される。
	for _, prefix := range nonProsePrefixes() {
		if strings.HasPrefix(line, prefix) {
			return "", false
		}
	}

	candidate := strings.TrimSpace(line)
	if candidate == "" {
		return "", false
	}

	for _, residue := range residues() {
		if strings.Contains(candidate, residue) {
			return "", false
		}
	}

	// 🔴 バイト数ではなく文字数で数える。日本語は1文字3バイトなので、
	// バイトで測ると 100〜500 の範囲が別物になる。評価セットの README が
	// 「平均 327字」と書いているのと同じ数え方にそろえる。
	count := utf8.RuneCountInString(candidate)

	return candidate, count >= s.Min && count <= s.Max
}
