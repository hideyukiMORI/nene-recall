// Package bigram は文字クラスで走査を分ける日本語向けの分割器を実装する。
//
// 🔴 CJK は2文字ずつ重ねた bigram に、英数は1語1トークンに割る。この非対称が
// この分割器の要点である。英数まで文字 bigram にすると RECALL_STORE が
// "re" "ec" "ca" ... に砕け、"record" や "call" を含む無関係なチャンクに
// 当たる。基準線（docs/benchmarks/2026-09-02-eval-vector-only-baseline.md）で
// ベクトル検索が1件も拾えなかったクエリのうち2件（`pgvector 0.8.6` と
// `RECALL_STORE`）はどちらも ASCII の識別子であり、語彙検索がまさに
// 救うべき対象である。そこを偽ヒットで埋めては意味が無い。
//
// 逆に日本語には語の区切りが無いので、文字 bigram にしないと「検索対象」が
// 丸ごと1トークンになり「対象」で引けない。形態素解析を使えば語で割れるが、
// 辞書を抱えることになる。どちらが良いかは要件定義 Q-2 の未決事項であり、
// ADR 0009 の評価で決着させる。この分割器は依存の少ない側の実測値を出すために作った。
package bigram

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/hideyukiMORI/nene-recall/internal/lexical"
)

// Tokenizer が契約を満たしていることをコンパイル時に確かめる。
var _ lexical.Tokenizer = Tokenizer{}

// id は保存済みトークン列との照合に使う識別子。
//
// 🔴 分割規則を1つでも変えたらこの値を変えること。保存済みのトークン列は
// 古い規則で作られており、新しい規則で分割したクエリとは噛み合わない。
// 値を据え置いたまま規則を変えると、ストアの不一致検知が発火しないまま
// 語彙スコアだけが静かに劣化する。
//
// 書式は <方式>:<前処理>:<版>。方式が同じでも前処理が違えば別物なので、
// NFKC と小文字化を名前に入れてある。
const id = "bigram:nfkc-lower:v1"

// cjkRunLength は CJK 連続部から何文字ずつ切り出すか。bigram なので 2。
const cjkRunLength = 2

// connectors は英数トークンの内側で語を繋ぐ文字。
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
const connectors = "_.-/"

// Tokenizer は NFKC 正規化と文字クラスに基づく分割器。
//
// 🔑 状態を持たないのでゼロ値が有効である。GO-003 が禁じているのは
// 「ゼロ値だと壊れる型をゼロ値で作れること」であって、状態の無い型ではない。
// それでも New を用意してあるのは、将来この分割器が設定を持ったときに
// 呼び出し側の書き換えが要らないようにするため。
type Tokenizer struct{}

// New は分割器を返す。
func New() Tokenizer { return Tokenizer{} }

// ID は保存済みトークン列との照合に使う識別子を返す。
func (Tokenizer) ID() string { return id }

// Tokenize は text をトークン列に分割する。
//
// 手順は固定する。
//  1. NFKC 正規化 → 小文字化。全角英数・半角カナ・互換文字を1つの形に寄せる
//  2. 文字クラスで走査を分ける。CJK 連続部は2文字ずつ重ねた bigram、
//     それ以外の語（英数・ラテン文字など）の連続部は1語1トークン
//  3. どちらでもない文字（記号・空白・句読点）は区切りとして捨てる
//
// 🔴 1 と 2 の順序を入れ替えないこと。全角の "ＲＥＣＡＬＬ＿ＳＴＯＲＥ" は
// NFKC を通す前は「英数」ではないので、正規化を後に回すと CJK 側の bigram に
// 落ちる。表記ゆれの吸収は分割より前に済ませる。
//
// 分割できる語が1つも無ければ空を返す。これは正常な入力（絵文字だけのクエリなど）
// であり、エラーではない。呼び出し側は語彙スコア 0 として扱う。
func (Tokenizer) Tokenize(text string) []string {
	runes := []rune(strings.ToLower(norm.NFKC.String(text)))
	tokens := []string{}

	for i := 0; i < len(runes); {
		switch {
		case isCJK(runes[i]):
			end := runEnd(runes, i, isCJK)
			tokens = appendBigrams(tokens, runes[i:end])
			i = end
		case isWord(runes[i]):
			end := wordEnd(runes, i)
			tokens = append(tokens, string(runes[i:end]))
			i = end
		default:
			// 区切り文字。トークンにしない。
			i++
		}
	}

	return tokens
}

// appendBigrams は CJK 連続部を2文字ずつ重ねて切り出す。
//
// 1文字だけの連続部はその1文字をトークンにする。落とすと「本」や「表」のような
// 単漢字のクエリが語彙側で一切引けなくなる。
//
// ⚠️ 単漢字・単かなのトークンは頻出語（「の」「を」）にも当たるので、雑音の源に
// なりうる。ts_rank は IDF（コーパス全体での希少さ）を見ないので、この雑音は
// スコアで自動的には減衰しない。落とすかどうかは実測で判断する事項であり、
// 予想は docs/benchmarks/2026-09-02-lexical-prediction.md に登録してある。
func appendBigrams(tokens []string, run []rune) []string {
	if len(run) < cjkRunLength {
		return append(tokens, string(run))
	}

	for i := 0; i+cjkRunLength <= len(run); i++ {
		tokens = append(tokens, string(run[i:i+cjkRunLength]))
	}

	return tokens
}

// runEnd は同じ文字クラスが続く終端（排他）を返す。
func runEnd(runes []rune, start int, class func(rune) bool) int {
	end := start
	for end < len(runes) && class(runes[end]) {
		end++
	}

	return end
}

// wordEnd は英数トークンの終端（排他）を返す。
//
// 連結子は後ろに語構成文字が続くときだけ取り込む。"0.8.6" は1トークンになり、
// "末尾です." の末尾の "." は落ちる。
func wordEnd(runes []rune, start int) int {
	end := start

	for end < len(runes) {
		switch {
		case isWord(runes[end]):
			end++
		case isConnector(runes[end]) && end+1 < len(runes) && isWord(runes[end+1]):
			end++ // 連結子を取り込む。次の語構成文字は次の周回が取り込む
		default:
			return end
		}
	}

	return end
}

// isCJK は漢字・ひらがな・カタカナかを判定する。
//
// 🔴 Unicode の Script だけでは足りない。長音符 "ー" (U+30FC) と "〆" (U+3006) は
// Script が Common で、Han にも Hiragana にも Katakana にも属さない（実測）。
// これらを CJK から外すと「サーバー」が「サ」「ー」「バ」「ー」に割れ、
// 表記ゆれ（`orthography`）を測るはずのクエリが分割器の欠陥を測ることになる。
//
// 半角カナの長音 "ｰ" (U+FF70) は NFKC が U+30FC に寄せるので、ここには要らない。
// この関数が正規化後の文字だけを見ることが前提である。
func isCJK(r rune) bool {
	if r == 'ー' || r == '〆' {
		return true
	}

	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r)
}

// isWord は語を構成する文字（CJK 以外の文字と数字）かを判定する。
//
// CJK を先に除くのは、ひらがなも漢字も unicode.IsLetter が真を返すためである。
// 判定の順序がそのまま分割の規則になっている。
func isWord(r rune) bool {
	if isCJK(r) {
		return false
	}

	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isConnector は英数トークンの内側で語を繋ぐ文字かを判定する。
func isConnector(r rune) bool {
	return strings.ContainsRune(connectors, r)
}
