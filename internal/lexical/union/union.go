// Package union は文字 bigram と形態素の両方のトークンを並べる分割器を実装する。
//
// 🔴 既定ではない。既定は bigram のままで、この分割器は**比較対象**として
// 入っている。docs/adr/0021-q2-bigram-stays-default.md が Q-2 を「既定は
// bigram のまま」で閉じたうえで、次に測るものとしてこの和集合を指名した
// (Decision 3)。既定を移すのは実測を見て別の ADR を書いてからである。
//
// 🔑 なぜ和集合か。ADR 0021 の実測で bigram と kagome の利得は**別の機構から**
// 来ていることが分かった——bigram は文字列一致の頑健さで `orthography`
// （表記ゆれ）を守り、kagome は原形への畳み込みで `paraphrase`（言い換え）を
// 拾う。タグ別の差は +0.18 と −0.18 で、向きが逆だった。両方のトークンを
// 同じ lexeme_text に入れれば両取りできる、というのがこの分割器の仮説である。
// 代償の予想（tsvector が約 1.5 倍・二重加点が MRR を汚す）は測定前に
// docs/benchmarks/2026-09-02-union-prediction.md へ凍結してある。
//
// 🔴 契約パッケージ internal/lexical には置けない。ARC-002 の pure-core が
// 掛かっており、kagome の辞書も x/text の Unicode 表もそこへ持ち込めない。
// 実装をサブパッケージへ逃がす形は internal/embed/ollama と同じで、判断の
// 根拠は docs/adr/0012-embedding-implementations-live-in-subpackages.md にある。
package union

import (
	"fmt"

	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/kagome"
)

// Tokenizer が契約を満たしていることをコンパイル時に確かめる。
var _ lexical.Tokenizer = (*Tokenizer)(nil)

// id は保存済みトークン列との照合に使う識別子。
//
// 🔴 分割規則を1つでも変えたらこの値を変えること。連結の順序も重複の扱いも
// 規則である——ts_rank は lexeme_text 上の位置を見るので、並べ方を変えると
// 保存済みのトークン列とスコアの意味がずれる。値を据え置いたまま規則を変えると、
// ストアの不一致検知が発火しないまま語彙スコアだけが静かに劣化する。
//
// 書式は <方式>:<構成要素>:<版>。構成要素を名前に入れてあるのは、片方の
// 分割器の規則が変われば（bigram:…:v2 / kagome:…:v2）この分割器の出力も
// 別物になるためである。
const id = "union:bigram+kagome:v1"

// Tokenizer は bigram と形態素のトークンを連結する分割器。
//
// ゼロ値は無効である。必ず New を通すこと（GO-003）。形態素側を持たない値で
// Tokenize を呼ぶと nil ポインタで落ちる——静かに bigram だけを返す実装には
// していない。それをすると「union で測ったつもりの数字」が bigram のものに
// なり、この分割器を作った目的そのものが消える。
type Tokenizer struct {
	// characterBigrams は文字 bigram 側。状態を持たないので値で持つ。
	characterBigrams bigram.Tokenizer

	// morphological は形態素側。辞書を握る。
	//
	// 🔑 複数の goroutine から同時に呼んでよい（kagome.Tokenizer が
	// その性質を持つ）。HTTP のハンドラから並行に呼ばれるので依存している。
	morphological *kagome.Tokenizer
}

// New は両方の分割器を組み立てて連結器を返す。
//
// 🔴 error を返すのは kagome.New が辞書の読み込みを含むためで、契約
// (lexical.Tokenizer) の違いではない。bigram.New との非対称は配線点1箇所に閉じる。
func New() (*Tokenizer, error) {
	morphological, err := kagome.New()
	if err != nil {
		return nil, fmt.Errorf("build the morphological half: %w", err)
	}

	return &Tokenizer{characterBigrams: bigram.New(), morphological: morphological}, nil
}

// ID は保存済みトークン列との照合に使う識別子を返す。
func (t *Tokenizer) ID() string { return id }

// Tokenize は text を bigram のトークン列と形態素のトークン列の連結にする。
//
// 規則は2つだけで、どちらも id に紐づいている。
//  1. 順序は bigram → kagome。ts_rank は lexeme_text 上の位置を見るので、
//     順序は保存済みデータの意味の一部である
//  2. 重複を除かない。ASCII の語は両者で同じトークンになる（ADR 0018 Decision 2 が
//     規則を共有させているため）ので、`pgvector` のような識別子は必ず2回入る
//
// 🔴 2 は既知の帰結であって取りこぼしではない。ts_rank は IDF を持たないので、
// 二重に入った語は二重に加点される。これが MRR にどう出るかは予想できないと
// 予想文書が明記しており、**測る対象そのもの**である。重複を除く v2 を先に
// 作ると、この予想が検証できないまま消える。
//
// 🔑 契約（空白・tsquery のメタ文字を含まない）は両者が既に満たしているので、
// 連結しても破れない。ここで再検査しないのは、検査を2箇所に置くと「どちらが
// 正本か」が曖昧になるからである。実行時の番人はストア側の validateTokens にある。
//
// 分割できる語が1つも無ければ空を返す。これは正常な入力（絵文字だけのクエリなど）
// であり、エラーではない。呼び出し側は語彙スコア 0 として扱う。
func (t *Tokenizer) Tokenize(text string) []string {
	// bigram.Tokenize は呼び出しごとに新しいスライスを返すので、そのまま
	// 積み増してよい（他の呼び出しの結果を書き換える経路が無い）。
	return append(t.characterBigrams.Tokenize(text), t.morphological.Tokenize(text)...)
}
