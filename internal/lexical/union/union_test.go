package union_test

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/kagome"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/union"
)

// tsqueryMetaCharacters は lexical.Tokenizer の契約が禁じている文字。
//
// PostgreSQL の tsquery でこれらは演算子・引用符として意味を持つ。トークンが
// 検索式の被演算子としてそのまま置かれるので、混ざると構文が壊れる。
const tsqueryMetaCharacters = `&|!():*<>'"\`

// newTokenizer は分割器を組み立てる。辞書の読み込みに失敗したら測る意味が無い。
func newTokenizer(t *testing.T) *union.Tokenizer {
	t.Helper()

	tokenizer, err := union.New()
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	return tokenizer
}

// newMorphological は比較用の形態素分割器を組み立てる。
func newMorphological(t *testing.T) *kagome.Tokenizer {
	t.Helper()

	tokenizer, err := kagome.New()
	if err != nil {
		t.Fatalf("kagome.New(): %v", err)
	}

	return tokenizer
}

// sampleTexts は構造を確かめるための入力。日本語・ASCII・混在を含める。
func sampleTexts() []string {
	return []string{
		"検索対象",
		"ベクトルの索引を張ると検索は速くなるか",
		"RECALL_STORE は postgres と sqlite を切り替える",
		"pgvector 0.8.6 で測った",
		"POST /v1/search で検索する",
		"サーバーの応答時間を測る",
	}
}

// TestTokenizeIsBigramThenKagome は出力が2つの分割器の連結そのものであることを
// 確かめる。
//
// 🔴 順序（bigram → kagome）と「重複を除かない」ことは、この分割器の規則で
// あって実装の都合ではない。ts_rank は lexeme_text 上の位置を見るので、順序を
// 変えると保存済みデータの意味が変わる。だから id に紐づけてあり、ここで
// 機械的に固定する。緩めるなら id も上げること。
func TestTokenizeIsBigramThenKagome(t *testing.T) {
	t.Parallel()

	tokenizer := newTokenizer(t)
	morphological := newMorphological(t)

	for _, text := range sampleTexts() {
		fromBigram := bigram.New().Tokenize(text)
		fromMorphological := morphological.Tokenize(text)

		want := slices.Concat(fromBigram, fromMorphological)
		if got := tokenizer.Tokenize(text); !slices.Equal(got, want) {
			t.Errorf("Tokenize(%q) = %v, want %v", text, got, want)
		}
	}
}

// TestTokenizeKeepsBothHalvesWhole は、それぞれの分割器の出力が丸ごと残ることを
// 確かめる。
//
// 🔑 前のテストと重なって見えるが、見ているものが違う。あちらは「連結の形」、
// こちらは「片方が痩せていないこと」である。将来ここで重複除去や刈り込みを
// 入れたくなったとき（v2 の話題）、それが**規則の変更**であることをこの2本が
// 分けて示す。
func TestTokenizeKeepsBothHalvesWhole(t *testing.T) {
	t.Parallel()

	tokenizer := newTokenizer(t)
	morphological := newMorphological(t)

	for _, text := range sampleTexts() {
		got := tokenizer.Tokenize(text)
		fromBigram := bigram.New().Tokenize(text)
		fromMorphological := morphological.Tokenize(text)

		if len(got) < len(fromBigram) || !slices.Equal(got[:len(fromBigram)], fromBigram) {
			t.Errorf("Tokenize(%q) の先頭が bigram の出力ではない: %v / %v", text, got, fromBigram)
			continue
		}

		if !slices.Equal(got[len(fromBigram):], fromMorphological) {
			t.Errorf("Tokenize(%q) の後半が kagome の出力ではない: %v / %v", text, got, fromMorphological)
		}
	}
}

// TestTokenizeCarriesTheBaseFormAndTheSurfaceBigrams は、和集合が「両方の機構」を
// 実際に運んでいることを1件の具体例で示す。
//
// 🔑 これがこの分割器を作った理由そのものである。「張った」は bigram では
// 表層の文字列としてしか残らず、kagome では原形「張る」に畳まれる。ADR 0021 の
// 実測では前者が `orthography`、後者が `paraphrase` を稼いでいた。両方が同じ
// トークン列に入っていることを、抽象的な性質ではなく実物で固定する。
func TestTokenizeCarriesTheBaseFormAndTheSurfaceBigrams(t *testing.T) {
	t.Parallel()

	got := newTokenizer(t).Tokenize("索引を張った")

	// 原形。bigram だけでは決して出てこない。
	if !slices.Contains(got, "張る") {
		t.Errorf("原形が入っていない: %v", got)
	}

	// 表層の文字 bigram。kagome だけでは決して出てこない。
	for _, surface := range []string{"引を", "を張"} {
		if !slices.Contains(got, surface) {
			t.Errorf("語境界を跨ぐ bigram %q が入っていない: %v", surface, got)
		}
	}
}

// TestTokenizeRepeatsASCIIWords は ASCII の語が二重に入ることを固定する。
//
// 🔴 これは「望ましい」ではなく「そう決めた」を記録するテストである。ASCII の
// 語は両者で同じトークンになる（ADR 0018 Decision 2 が規則を共有させている）ので、
// 連結すれば必ず2回入る。ts_rank は IDF を持たないので、二重に入った語は二重に
// 加点される——予想文書がその影響を「予想できない」と書いており、**測る対象**で
// ある。重複を除くのは v2 の判断であり、除くなら id を上げること。
func TestTokenizeRepeatsASCIIWords(t *testing.T) {
	t.Parallel()

	got := newTokenizer(t).Tokenize("pgvector 0.8.6 で測った")

	for _, word := range []string{"pgvector", "0.8.6"} {
		if n := countToken(got, word); n != 2 {
			t.Errorf("%q が %d 回。二重加点の規則が変わっている: %v", word, n, got)
		}
	}
}

// countToken はトークン列に token が何回現れるかを数える。
func countToken(tokens []string, token string) int {
	n := 0

	for _, candidate := range tokens {
		if candidate == token {
			n++
		}
	}

	return n
}

// TestTokenizeReturnsEmptyForSymbolsOnly は分割できる語が無い入力を確かめる。
//
// これはエラーではない。呼び出し側は語彙スコア 0 として扱い、合成は
// alpha*vector に縮退する。
func TestTokenizeReturnsEmptyForSymbolsOnly(t *testing.T) {
	t.Parallel()

	tokenizer := newTokenizer(t)

	for _, text := range []string{"", "🔴🔑", "   ", "!!! ??? <#>"} {
		if got := tokenizer.Tokenize(text); len(got) != 0 {
			t.Errorf("Tokenize(%q) = %v, want empty", text, got)
		}
	}
}

// TestTokensSatisfyTheContract は lexical.Tokenizer の契約を機械で確かめる。
//
// 🔴 連結する側は契約を再検査していない（両者が既に満たしているため）。
// だからこそ、その前提が成り立ち続けることをここで見る。片方の分割器が契約を
// 破る変更を入れたとき、和集合側にも同じ症状が出ることを検出できる。
func TestTokensSatisfyTheContract(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"a & b | c ! d ( e ) f : g * h < i > j",
		`引用符 ' と " と \ を含む文`,
		"RECALL_STORE|DROP TABLE chunks",
		"全角記号　＆｜！（）：＊＜＞",
		"混在 mixed テキスト text 0.8.6 bge-m3",
		"🔴 絵文字と組版記号 ―─※〜",
		"ハイブリッド検索は alpha*vector + (1-alpha)*lexical で合成する",
		"ｽﾞﾎﾞﾗな未知語ｦ含む文字列",
	}

	tokenizer := newTokenizer(t)

	for _, text := range inputs {
		for _, token := range tokenizer.Tokenize(text) {
			assertTokenIsUsableAsOperand(t, text, token)
		}
	}
}

// assertTokenIsUsableAsOperand は1トークンが検索式の被演算子として置けるかを見る。
func assertTokenIsUsableAsOperand(t *testing.T, text, token string) {
	t.Helper()

	if token == "" {
		t.Errorf("Tokenize(%q) が空のトークンを返した", text)
	}

	if strings.ContainsFunc(token, unicode.IsSpace) {
		t.Errorf("Tokenize(%q) のトークン %q が空白を含む", text, token)
	}

	if strings.ContainsAny(token, tsqueryMetaCharacters) {
		t.Errorf("Tokenize(%q) のトークン %q が tsquery のメタ文字を含む", text, token)
	}
}

// TestTokenizeIsDeterministic は同じ入力から常に同じ出力が出ることを確かめる。
//
// ストアは取り込み時の出力を保存し、検索時の出力と突き合わせる。揺れる実装は
// 保存済みの索引を静かに無効にするので、契約として固定する。
//
// 🔑 分割器を作り直しても同じ出力になることまで見る。形態素側は内部に pool を
// 持つので、インスタンスをまたいで結果が変わらないことは自明ではない。
func TestTokenizeIsDeterministic(t *testing.T) {
	t.Parallel()

	const text = "ハイブリッド検索は alpha*vector + (1-alpha)*lexical で合成する"

	first := newTokenizer(t).Tokenize(text)

	tokenizer := newTokenizer(t)
	for range 3 {
		if got := tokenizer.Tokenize(text); !slices.Equal(got, first) {
			t.Fatalf("同じ分割器で出力が揺れた: %v / %v", first, got)
		}
	}

	if got := newTokenizer(t).Tokenize(text); !slices.Equal(got, first) {
		t.Fatalf("作り直した分割器で出力が揺れた: %v / %v", first, got)
	}
}

// TestID は識別子が固定されていることを確かめる。
//
// 🔴 この値を変えるのは分割規則を変えたときだけである。テストを「実装に
// 合わせて」書き換えるのではなく、規則を変えたなら値も変える、という順序を
// 守るためにリテラルで縛る。保存済みトークン列との照合がこの値に依存している。
//
// 🔴 構成要素のどちらとも違う値であること。同じ値だと、分割器を切り替えたときに
// ストアの不一致検知が発火せず、規則の違うトークン列が混ざる。
func TestID(t *testing.T) {
	t.Parallel()

	got := newTokenizer(t).ID()

	if want := "union:bigram+kagome:v1"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}

	if got == bigram.New().ID() || got == newMorphological(t).ID() {
		t.Errorf("構成要素と同じ識別子を名乗っている: %q", got)
	}
}
