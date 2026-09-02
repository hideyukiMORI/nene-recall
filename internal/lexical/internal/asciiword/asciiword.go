// Package asciiword は「英数の語をどこで切るか」の規則を1箇所に置く。
//
// 🔴 これは寄せ集めではない。2つの分割器 (internal/lexical/bigram と
// internal/lexical/kagome) が **同じ結果を出さなければならない**部分だけを
// 持っている。ADR 0018 Decision 2 は「ASCII の語は bigram と同じ規則で
// 1語1トークンにする」と決めており、その同一性は文章では守れない——
// 片方だけ連結子を1つ足した瞬間に、`exact-term` の比較が
// 「分割器の差」ではなく「規則のずれ」を測ることになる。
// 同一性を型と関数の共有で保証するために、規則の実体をここに1つだけ置く。
//
// 🔴 internal/lexical（契約パッケージ）には置かない。契約は「テキストを
// トークン列に分割する」という意味だけを持つべきで、そこに Unicode 表の
// 依存 (golang.org/x/text) を混ぜると ARC-002 の pure-core が崩れる。
// internal/ の下に置いてあるので、import できるのは internal/lexical
// 配下のパッケージだけである（Go のコンパイラが強制する）。
package asciiword

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Connectors は英数トークンの内側で語を繋ぐ文字。
//
// 🔴 「内側」に限る。末尾の連結子は落とす。"末尾です." の "." まで拾うと、
// 同じ語が文末にあるかどうかで別トークンになってしまう。
//
// この4つを選んだ理由はコーパスにある: 下線は RECALL_STORE・CORPUS_SEARCH_DRIVER、
// 点は 0.8.6・PdoChunkSearchRepository.php、ハイフンは bge-m3・golangci-lint、
// 斜線は v1/search。いずれも「割ると別物になる」識別子である。
//
// 🔴 tsquery のメタ文字（& | ! ( ) : * < > と引用符）を1つも含まないこと。
// トークンが検索式の被演算子としてそのまま置かれるので、メタ文字が混ざると
// 構文が壊れる。lexical.Tokenizer の契約がこれを要求している。
const Connectors = "_.-/"

// Normalize は分割の前に表記を1つの形へ寄せる。NFKC 正規化 → 小文字化。
//
// 🔴 正規化は分割より前に行う。全角の "ＲＥＣＡＬＬ＿ＳＴＯＲＥ" は NFKC を
// 通す前は「英数」ではないので、順序を入れ替えると CJK 側へ落ちる。
// 半角カナの長音 "ｰ" (U+FF70) がここで U+30FC に寄るので、IsCJK は
// 正規化後の文字だけを見ればよい。
//
// 🔑 2つの分割器がこれを共有するのは、ADR 0018 が「orthography の条件を
// 揃える」ことを分割規則の第1手順に置いているためである。片方だけ前処理が
// 違えば、表記ゆれのタグは分割器ではなく前処理の差を測ることになる。
func Normalize(text string) string {
	return strings.ToLower(norm.NFKC.String(text))
}

// IsCJK は漢字・ひらがな・カタカナかを判定する。
//
// 🔴 Unicode の Script だけでは足りない。長音符 "ー" (U+30FC) と "〆" (U+3006) は
// Script が Common で、Han にも Hiragana にも Katakana にも属さない（実測）。
// これらを CJK から外すと「サーバー」が「サ」「ー」「バ」「ー」に割れ、
// 表記ゆれ（`orthography`）を測るはずのクエリが分割器の欠陥を測ることになる。
//
// 半角カナの長音 "ｰ" (U+FF70) は Normalize が U+30FC に寄せるので、ここには
// 要らない。この関数が正規化後の文字だけを見ることが前提である。
func IsCJK(r rune) bool {
	if r == 'ー' || r == '〆' {
		return true
	}

	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r)
}

// IsWord は語を構成する文字（CJK 以外の文字と数字）かを判定する。
//
// CJK を先に除くのは、ひらがなも漢字も unicode.IsLetter が真を返すためである。
// 判定の順序がそのまま分割の規則になっている。
func IsWord(r rune) bool {
	if IsCJK(r) {
		return false
	}

	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// IsConnector は英数トークンの内側で語を繋ぐ文字かを判定する。
func IsConnector(r rune) bool {
	return strings.ContainsRune(Connectors, r)
}

// End は英数トークンの終端（排他）を返す。
//
// 連結子は後ろに語構成文字が続くときだけ取り込む。"0.8.6" は1トークンになり、
// "末尾です." の末尾の "." は落ちる。
func End(runes []rune, start int) int {
	end := start

	for end < len(runes) {
		switch {
		case IsWord(runes[end]):
			end++
		case IsConnector(runes[end]) && end+1 < len(runes) && IsWord(runes[end+1]):
			end++ // 連結子を取り込む。次の語構成文字は次の周回が取り込む
		default:
			return end
		}
	}

	return end
}

// RunEnd は同じ文字クラスが続く終端（排他）を返す。
func RunEnd(runes []rune, start int, class func(rune) bool) int {
	end := start
	for end < len(runes) && class(runes[end]) {
		end++
	}

	return end
}
