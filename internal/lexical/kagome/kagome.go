// Package kagome は形態素解析で語に割る分割器を実装する。
//
// 🔴 既定ではない。既定は bigram のままで、この分割器は**比較対象**として
// 入っている (ADR 0018)。要件定義 Q-2（bigram か形態素か）は総合値の予想が
// 「動かない」であり、決着は実測に委ねられている。既定を移すのは実測を見て
// 別の ADR を書いてからである。
//
// 🔑 bigram との差は「何を拾うか」ではなく「何を拾わないか」にある。
// 語境界を跨いだ偶然一致（「の索」「を張」のような bigram）が消え、活用形が
// 原形に畳まれる。予想は docs/benchmarks/2026-09-02-morph-prediction.md に
// 測定前から凍結してある。
//
// 🔴 契約パッケージ internal/lexical には置けない。ARC-002 の pure-core が
// 掛かっており、辞書という外部依存をそこへ持ち込めないためである。実装を
// サブパッケージへ逃がす形は internal/embed/ollama と同じで、判断の根拠は
// ADR 0012 にある。
package kagome

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/ikawaha/kagome-dict/ipa"
	"github.com/ikawaha/kagome/v2/tokenizer"

	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/internal/asciiword"
)

// Tokenizer が契約を満たしていることをコンパイル時に確かめる。
var _ lexical.Tokenizer = (*Tokenizer)(nil)

// errDictionary は辞書から解析器を組み立てられなかったことを表す。
var errDictionary = errors.New("kagome: cannot build the tokenizer from the dictionary")

// id は保存済みトークン列との照合に使う識別子。
//
// 🔴 分割規則を1つでも変えたらこの値を変えること。保存済みのトークン列は
// 古い規則で作られており、新しい規則で分割したクエリとは噛み合わない。
// 値を据え置いたまま規則を変えると、ストアの不一致検知が発火しないまま
// 語彙スコアだけが静かに劣化する。
//
// 書式は <方式>:<辞書>:<英数の扱い>:<版>。辞書を名前に入れてあるのは、
// IPA と UniDic では単位が違い、同じ「形態素」でも別のトークン列になるためである
// (ADR 0018 の却下表)。
const id = "kagome:ipadic:ascii-words:v1"

// 捨てる品詞（POS の第1要素）。
//
// 🔴 ts_rank は IDF（コーパス全体での希少さ）を持たない。高頻度の機能語を
// 残すと、それを多く含む長文ほど加点され、ADR 0014 が実測で退けた長さ正規化の
// 問題を裏口から呼び戻す。だから助詞・助動詞・記号は落とす (ADR 0018 Decision 2)。
//
// ⚠️ 助詞を落とすと `particle` タグの撃ち分けは原理的にできなくなる。これは
// **測っていない却下**なので、v1 の結果で `particle` が動かなければ助詞を残す
// v2 を試す、という順序が ADR 0018 に書いてある。
const (
	posSymbol        = "記号"
	posParticle      = "助詞"
	posAuxiliaryVerb = "助動詞"
)

// baseFormUnknown は IPA 辞書が原形を持たないときの印。
//
// 素性の欄に "*" が入るので、そのままトークンにすると tsquery のメタ文字が
// 流れ出る。表層形へ倒す判定に使う。
const baseFormUnknown = "*"

// tsqueryMetaCharacters は lexical.Tokenizer の契約が禁じている文字。
//
// PostgreSQL の tsquery でこれらは演算子・引用符として意味を持つ。トークンが
// 検索式の被演算子としてそのまま置かれるので、混ざると構文が壊れる。
const tsqueryMetaCharacters = `&|!():*<>'"\`

// Tokenizer は IPA 辞書による形態素分割器。
//
// ゼロ値は無効である。必ず New を通すこと（GO-003）。解析器を持たない値で
// Tokenize を呼ぶと nil ポインタで落ちる——静かに空を返す実装にはしていない。
// 空のトークン列は「分割できる語が無い」という正常な結果であり、
// 「分割器が壊れている」をそこに混ぜると区別できなくなる。
type Tokenizer struct {
	// inner は kagome の解析器。辞書を握る。
	//
	// 🔑 複数の goroutine から同時に呼んでよい（kagome の Tokenizer は
	// 解析ごとの状態を内部の pool に持ち、自身は不変である）。HTTP の
	// ハンドラから並行に呼ばれるので、この性質に依存している。
	inner *tokenizer.Tokenizer
}

// New は IPA 辞書を読み込んで分割器を組み立てる。
//
// 🔴 bigram.New と形が違い error を返す。辞書の読み込みという失敗しうる
// 手順を含むためで、契約 (lexical.Tokenizer) の違いではない。呼び出し側は
// 配線点だけなので、この非対称は配線点1箇所に閉じる。
//
// 辞書はバイナリに埋め込まれている（純 Go・cgo なし・外部ファイルなし）。
// その代わりバイナリが数十 MB 大きくなる。既定が bigram でも辞書はリンク
// されるので、配布サイズが問題になるなら build tag で切る判断を別途行う
// (ADR 0018 の Consequences)。
func New() (*Tokenizer, error) {
	inner, err := tokenizer.New(ipa.Dict(), tokenizer.OmitBosEos())
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errDictionary, err.Error())
	}

	return &Tokenizer{inner: inner}, nil
}

// ID は保存済みトークン列との照合に使う識別子を返す。
func (t *Tokenizer) ID() string { return id }

// Tokenize は text をトークン列に分割する。
//
// 手順は ADR 0018 Decision 2 の5つ。
//  1. NFKC 正規化 → 小文字化（bigram と同じ前処理）
//  2. ASCII の語（英数＋連結子 _.-/）は **bigram と同じ規則**で1語1トークンにし、
//     形態素解析には渡さない
//  3. それ以外（CJK）の連続部を kagome の通常モードで分割し、原形をトークンにする
//  4. 記号・助詞・助動詞は捨てる
//  5. 空白・tsquery のメタ文字を含むトークンは捨てる（契約）
//
// 🔴 2 が要点である。`pgvector`・`0.8.6`・`org_id` のような識別子は形態素解析の
// 対象ではない。ここを kagome に渡すと未知語処理で語が割れ、bigram が
// `exact-term` で稼いでいる精度が落ちる。**比較したいのは日本語の割り方であって
// 識別子の扱いではない**ので、そこは規則ごと共有する
// (internal/lexical/internal/asciiword)。
//
// 分割できる語が1つも無ければ空を返す。これは正常な入力（絵文字だけのクエリなど）
// であり、エラーではない。呼び出し側は語彙スコア 0 として扱う。
func (t *Tokenizer) Tokenize(text string) []string {
	runes := []rune(asciiword.Normalize(text))
	tokens := []string{}

	for i := 0; i < len(runes); {
		switch {
		case asciiword.IsCJK(runes[i]):
			end := asciiword.RunEnd(runes, i, asciiword.IsCJK)
			tokens = t.appendMorphemes(tokens, string(runes[i:end]))
			i = end
		case asciiword.IsWord(runes[i]):
			end := asciiword.End(runes, i)
			tokens = appendToken(tokens, string(runes[i:end]))
			i = end
		default:
			// 区切り文字。トークンにしない。
			i++
		}
	}

	return tokens
}

// appendMorphemes は CJK の連続部を形態素に割って積む。
//
// 🔑 連続部ごとに解析器へ渡す。句読点や記号で区切られた断片を別々に解析する
// ことになるが、bigram も同じ位置で切っている。ここを変えると2つの分割器の
// 差に「文の切り方の差」が混ざる。
func (t *Tokenizer) appendMorphemes(tokens []string, run string) []string {
	for _, morpheme := range t.inner.Tokenize(run) {
		if isFunctionWord(morpheme.POS()) {
			continue
		}

		tokens = appendToken(tokens, lemma(morpheme))
	}

	return tokens
}

// isFunctionWord は品詞の第1要素で機能語かを判定する。
//
// 第1要素だけを見るのは、IPA 辞書の階層が「助詞,格助詞,一般,*」のように
// 大分類から並ぶためである。細分類まで見る必要があるなら、それは v2 の規則に
// なる（規則を変えたら id も変える）。
func isFunctionWord(pos []string) bool {
	if len(pos) == 0 {
		return false
	}

	return pos[0] == posSymbol || pos[0] == posParticle || pos[0] == posAuxiliaryVerb
}

// lemma は形態素の原形を返す。無ければ表層形へ倒す。
//
// 🔴 原形にするのがこの分割器の利点そのものである。「張った」と「張る」が
// 同じトークンになり、活用の違いで語彙一致を落とさない。表層形をトークンに
// する案は ADR 0018 が却下している（形態素の利点を捨てることになる）。
//
// 未知語は原形を持たない。そこで表層形へ倒すが、"*" をそのまま通すと
// tsquery のメタ文字が流れ出るので、印として明示的に弾く。
func lemma(morpheme tokenizer.Token) string {
	if base, ok := morpheme.BaseForm(); ok && base != "" && base != baseFormUnknown {
		return base
	}

	return morpheme.Surface
}

// appendToken は契約を満たすトークンだけを積む。
//
// 🔴 空白と tsquery のメタ文字を含むトークンを外へ出さない。ストアは
// トークン列を空白区切りの1本の文字列として保存するので、空白を含む
// トークンは DB の中で2つ以上のレキシームに割れ、メタ文字は検索式の構文を
// 壊す (lexical.Tokenizer の契約)。
//
// 🔑 ASCII の語がここで落ちることは無い（英数と連結子 _.-/ だけで構成され、
// どれも禁止文字ではない）。したがって bigram との同一性はこの関数を通しても
// 保たれる。守っているのは形態素側の未知語処理から出てくる文字列である。
func appendToken(tokens []string, token string) []string {
	if token == "" ||
		strings.ContainsFunc(token, unicode.IsSpace) ||
		strings.ContainsAny(token, tsqueryMetaCharacters) {
		return tokens
	}

	return append(tokens, token)
}
