package main

import (
	"regexp"
	"strings"
)

// 🔴 ここは「粗い除去」であって wikitext のパーサではない。
//
// 目的は 100〜500 字の日本語の散文を取り出すことだけで、記法を忠実に描画する
// ことではない。取りこぼしは Selector の除外規則（`[[`・`{{`・`<` を含む段落を
// 捨てる）が受け止める。つまりこの層は「大半を素の文にする」役で、「完全に
// 素の文にする」保証はしない。両方が揃って初めて、投入されるのは記法の
// 残っていない段落だけになる。
//
// 🔑 忠実なパーサを書かないのは、distractor に求められるのが「現実の日本語の
// 分布」だけだからである（docs/adr/0019-large-scale-benchmark-corpus.md）。
// 描画の正しさは 10万件の latency にも recall の希釈にも効かない。

// Stripper は wikitext を粗く素の文へ落とす。
//
// ゼロ値は無効である。必ず NewStripper を通すこと。正規表現をフィールドに
// 持つのは、可変のパッケージ変数を作らない (GO-007) 一方で、数十万ページを
// 通す間に同じ表を作り直さないためである。
type Stripper struct {
	comment      *regexp.Regexp
	refPair      *regexp.Regexp
	refSelf      *regexp.Regexp
	externalLink *regexp.Regexp
	magicWord    *regexp.Regexp
	tag          *regexp.Regexp
	entities     *strings.Replacer
}

// NewStripper は除去に使う表を組み立てる。
func NewStripper() *Stripper {
	return &Stripper{
		// HTML コメント。改行を跨ぐ。
		comment: regexp.MustCompile(`(?s)<!--.*?-->`),
		// 開閉のある <ref> ... </ref>。本文ではなく出典なので丸ごと捨てる。
		refPair: regexp.MustCompile(`(?is)<ref[^>]*>.*?</ref>`),
		// 自己閉じの <ref ... />。
		refSelf: regexp.MustCompile(`(?is)<ref[^>]*/>`),
		// [URL 表示文字列] 形式の外部リンク。表示文字列があればそれを残す。
		// 無ければ URL だけなので丸ごと捨てる——素の URL が段落に残ると、
		// 日本語の散文ではないものが混ざる。
		externalLink: regexp.MustCompile(`\[(?:https?:)?//[^\s\]]*(?:[ \t]+([^\]]*))?\]`),
		// __NOTOC__ のような魔法語。
		magicWord: regexp.MustCompile(`__[A-Z]+__`),
		// 残った HTML/XML タグ。
		//
		// 🔴 ここを通してもタグが残ることはある（`<` が比較演算子として書かれて
		// いる等）。残ったものは段落の除外規則で捨てられるので、ここで完全性を
		// 目指さない。
		tag: regexp.MustCompile(`(?s)<[^>]*>`),
		// XML 復号後に残る実体参照。ダンプ中では `&amp;nbsp;` と書かれているので、
		// encoding/xml が1段ほどいた時点で `&nbsp;` が本文に残る。HTML の実体は
		// XML の実体ではないため、復号器はここまで面倒を見ない。
		//
		// 🔴 `&lt;` を `<` に戻すのは、戻さないと段落の除外規則をすり抜けるから
		// である。素の `<` を含む段落は記法の残りとして捨てられるが、実体参照の
		// ままだと「タグの書きかけ」が本文として残る。
		entities: strings.NewReplacer(
			"&amp;", "&",
			"&lt;", "<",
			"&gt;", ">",
			"&nbsp;", " ",
			"&ndash;", "-",
			"&mdash;", "-",
			"&quot;", `"`,
			"&apos;", "'",
		),
	}
}

// mediaPrefixes は本文ではない `[[...]]` の接頭辞。
//
// 画像・分類・メディアは説明文であって記事の散文ではない。日本語版は英語の
// 名前空間名も併用するので両方を挙げる。関数で返すのは可変のパッケージ変数を
// 作らないため (GO-007)。
func mediaPrefixes() []string {
	return []string{
		"file:", "image:", "media:", "category:",
		"ファイル:", "画像:", "メディア:", "カテゴリ:",
	}
}

// PlainText は wikitext を粗く素の文へ落とす。
//
// 手順の順序には意味がある。テンプレートと表は中に `[[...]]` を持ちうるので
// 先に落とす。逆順にすると、消えるはずの画像説明文が本文として残る。
func (s *Stripper) PlainText(wikitext string) string {
	out := s.comment.ReplaceAllString(wikitext, "")
	out = s.refPair.ReplaceAllString(out, "")
	out = s.refSelf.ReplaceAllString(out, "")
	out = stripNested(out, "{|", "|}")
	out = stripNested(out, "{{", "}}")
	out = expandLinks(out)
	out = s.externalLink.ReplaceAllString(out, "$1")
	out = s.magicWord.ReplaceAllString(out, "")
	out = s.tag.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "'''''", "")
	out = strings.ReplaceAll(out, "'''", "")
	out = strings.ReplaceAll(out, "''", "")

	return s.entities.Replace(out)
}

// stripNested は opening と closing で囲まれた入れ子構造を丸ごと落とす。
//
// 🔴 正規表現で書かないこと。テンプレートは `{{A|{{B}}}}` のように入れ子に
// なるので、最短一致は内側の `}}` で閉じてしまい、外側の残骸が本文に混ざる。
// 深さを数える手続きでしか正しく落とせない。
//
// 閉じ括弧が現れないまま入力が終わったら、開き括弧以降は捨てる。壊れた記法を
// 本文として残すより、その段落ごと失うほうが安全である。
func stripNested(s, opening, closing string) string {
	var b strings.Builder

	depth := 0

	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], opening):
			depth++
			i += len(opening)
		case depth > 0 && strings.HasPrefix(s[i:], closing):
			depth--
			i += len(closing)
		case depth > 0:
			i++
		default:
			b.WriteByte(s[i])
			i++
		}
	}

	return b.String()
}

// expandLinks は `[[...]]` を表示文字列に置き換える。
//
// 画像・分類などの本文でないものは丸ごと落とす。`[[記事|表示]]` は表示側を、
// `[[記事]]` は記事名をそのまま残す。入れ子（画像の説明文の中のリンク）は
// 再帰で先に畳む。
func expandLinks(s string) string {
	var b strings.Builder

	for i := 0; i < len(s); {
		if !strings.HasPrefix(s[i:], "[[") {
			b.WriteByte(s[i])
			i++

			continue
		}

		inner, next, ok := matchNested(s, i, "[[", "]]")
		if !ok {
			// 閉じないまま終わった。壊れた記法は残さない。
			return b.String()
		}

		b.WriteString(linkText(expandLinks(inner)))

		i = next
	}

	return b.String()
}

// matchNested は s[start:] の開き括弧に対応する閉じ括弧を深さを数えて探す。
//
// 返り値は中身・閉じ括弧の直後の位置・見つかったか、の順。
func matchNested(s string, start int, opening, closing string) (string, int, bool) {
	depth := 0

	for i := start; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], opening):
			depth++
			i += len(opening)
		case strings.HasPrefix(s[i:], closing):
			depth--
			i += len(closing)

			if depth == 0 {
				return s[start+len(opening) : i-len(closing)], i, true
			}
		default:
			i++
		}
	}

	return "", 0, false
}

// linkText は `[[...]]` の中身から本文に残す文字列を決める。
func linkText(inner string) string {
	lower := strings.ToLower(strings.TrimLeft(inner, ": "))
	for _, prefix := range mediaPrefixes() {
		if strings.HasPrefix(lower, prefix) {
			return ""
		}
	}

	// パイプの後ろが表示文字列。`[[A|B|C]]` のような書き方でも最後が表示になる。
	if pipe := strings.LastIndex(inner, "|"); pipe >= 0 {
		return inner[pipe+1:]
	}

	return inner
}
