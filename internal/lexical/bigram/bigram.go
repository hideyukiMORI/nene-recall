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
//
// 🔑 前処理と英数の語の規則は internal/lexical/internal/asciiword に置いてあり、
// 形態素側 (internal/lexical/kagome) と**共有している**。ADR 0018 Decision 2 が
// 「ASCII の語は bigram と同じ規則で1語1トークンにする」と決めているので、
// 同一性はコードの共有で保証する（文章では守れない）。
package bigram

import (
	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/internal/asciiword"
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

// Tokenizer は NFKC 正規化と文字クラスに基づく分割器。
//
// 🔑 状態を持たないのでゼロ値が有効である。GO-003 が禁じているのは
// 「ゼロ値だと壊れる型をゼロ値で作れること」であって、状態の無い型ではない。
// それでも New を用意してあるのは、将来この分割器が設定を持ったときに
// 呼び出し側の書き換えが要らないようにするため。
type Tokenizer struct{}

// New は分割器を返す。
//
// ⚠️ 形態素側の kagome.New は error を返す（辞書の読み込みが失敗しうる）。
// 形が違うのは実装の事情であって契約の違いではない。lexical.Tokenizer 越しに
// 使う側からは、どちらも同じ口に見える。
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
	runes := []rune(asciiword.Normalize(text))
	tokens := []string{}

	for i := 0; i < len(runes); {
		switch {
		case asciiword.IsCJK(runes[i]):
			end := asciiword.RunEnd(runes, i, asciiword.IsCJK)
			tokens = appendBigrams(tokens, runes[i:end])
			i = end
		case asciiword.IsWord(runes[i]):
			end := asciiword.End(runes, i)
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
