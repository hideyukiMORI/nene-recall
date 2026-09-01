package bigram_test

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
)

// tsqueryMetaCharacters は lexical.Tokenizer の契約が禁じている文字。
//
// PostgreSQL の tsquery でこれらは演算子・引用符として意味を持つ。トークンが
// 検索式の被演算子としてそのまま置かれるので、混ざると構文が壊れる。
const tsqueryMetaCharacters = `&|!():*<>'"\`

// TestTokenizeIdentifiers は ASCII の識別子を砕かないことを確かめる。
//
// 🔴 このテストがこの分割器の存在理由そのものである。基準線でベクトル検索が
// 1件も拾えなかったクエリのうち2件（`pgvector 0.8.6` と `RECALL_STORE`）は
// ASCII の識別子で、語彙検索がまさに救うべき対象だった。文字 bigram に砕けば
// 偽ヒットで埋まり、救うどころか悪化する。
func TestTokenizeIdentifiers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "下線は語を繋ぐ",
			text: "RECALL_STORE",
			want: []string{"recall_store"},
		},
		{
			name: "版番号の点は語を繋ぐ",
			text: "pgvector 0.8.6",
			want: []string{"pgvector", "0.8.6"},
		},
		{
			name: "拡張子の点は語を繋ぐ",
			text: "PdoChunkSearchRepository.php",
			want: []string{"pdochunksearchrepository.php"},
		},
		{
			name: "ハイフンは語を繋ぐ",
			text: "bge-m3 と golangci-lint",
			want: []string{"bge-m3", "と", "golangci-lint"},
		},
		{
			name: "斜線は語を繋ぐ",
			text: "POST /v1/search",
			want: []string{"post", "v1/search"},
		},
		{
			name: "末尾の連結子は落とす",
			text: "end. next",
			want: []string{"end", "next"},
		},
		{
			name: "連結子だけの並びはトークンにならない",
			text: "-- __ ..",
			want: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := bigram.New().Tokenize(tc.text)
			if !slices.Equal(got, tc.want) {
				t.Errorf("Tokenize(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestTokenizeCJK は日本語を2文字ずつ重ねて切り出すことを確かめる。
func TestTokenizeCJK(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "連続部を重ねて切る",
			text: "検索対象",
			want: []string{"検索", "索対", "対象"},
		},
		{
			name: "1文字の連続部はその1文字",
			text: "本 を 読む",
			want: []string{"本", "を", "読む"},
		},
		{
			name: "長音符は CJK として扱う",
			text: "サーバー",
			want: []string{"サー", "ーバ", "バー"},
		},
		{
			name: "表記ゆれは bigram を部分的に共有する",
			text: "サーバ",
			want: []string{"サー", "ーバ"},
		},
		{
			name: "句読点は区切りになる",
			text: "検索する。取得する",
			want: []string{"検索", "索す", "する", "取得", "得す", "する"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := bigram.New().Tokenize(tc.text)
			if !slices.Equal(got, tc.want) {
				t.Errorf("Tokenize(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestTokenizeMixed は英数と CJK が混ざった文を確かめる。
//
// 🔑 英数トークンと CJK 連続部の境界を跨ぐ bigram は作らない。したがって
// 「Recall が」と「Corpus が」は語彙側では区別できない。この性質は
// docs/benchmarks/2026-09-02-lexical-prediction.md に予想として登録してある。
// テストはその性質を固定するためにある（望ましいと主張しているのではない）。
func TestTokenizeMixed(t *testing.T) {
	t.Parallel()

	got := bigram.New().Tokenize("Recall が落ちたら Corpus は")
	want := []string{"recall", "が落", "落ち", "ちた", "たら", "corpus", "は"}

	if !slices.Equal(got, want) {
		t.Errorf("Tokenize = %v, want %v", got, want)
	}
}

// TestTokenizeIsBlindToArgumentOrder は最小対のトークン集合が一致することを固定する。
//
// 🔴 これは「壊れている」ことのテストではなく、分割器の能力の限界を明示する
// テストである。q-057 / q-058 は助詞の対で正解集合が交わらないが、この分割器の
// 出力は完全に一致するので、語彙スコアは両者で同じ値になる。撃ち分けが
// 改善したという観測が出たら、それは語彙側の働きではなく合成の副作用である。
func TestTokenizeIsBlindToArgumentOrder(t *testing.T) {
	t.Parallel()

	tokenizer := bigram.New()
	forward := tokenizer.Tokenize("Recall が落ちたら Corpus は動き続けるか")
	reverse := tokenizer.Tokenize("Corpus が落ちたら Recall は動き続けるか")

	slices.Sort(forward)
	slices.Sort(reverse)

	if !slices.Equal(forward, reverse) {
		t.Errorf("最小対のトークン集合が違う: %v / %v", forward, reverse)
	}
}

// TestTokenizeNormalizes は NFKC 正規化と小文字化を確かめる。
//
// 🔴 正規化は分割より前に行う。全角の "ＲＥＣＡＬＬ" は正規化前には英数では
// ないので、順序を入れ替えると CJK 側の bigram に落ちる。
func TestTokenizeNormalizes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "全角英数は半角になる",
			text: "ＲＥＣＡＬＬ＿ＳＴＯＲＥ",
			want: []string{"recall_store"},
		},
		{
			name: "半角カナは全角カナに合成される",
			text: "ｻｰﾊﾞ",
			want: []string{"サー", "ーバ"},
		},
		{
			name: "大文字と小文字は同じトークンになる",
			text: "PgVector",
			want: []string{"pgvector"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := bigram.New().Tokenize(tc.text)
			if !slices.Equal(got, tc.want) {
				t.Errorf("Tokenize(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestTokenizeReturnsEmptyForSymbolsOnly は分割できる語が無い入力を確かめる。
//
// これはエラーではない。呼び出し側は語彙スコア 0 として扱い、合成は
// alpha*vector に縮退する。
func TestTokenizeReturnsEmptyForSymbolsOnly(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"", "🔴🔑", "   ", "!!! ??? <#>"} {
		got := bigram.New().Tokenize(text)
		if len(got) != 0 {
			t.Errorf("Tokenize(%q) = %v, want empty", text, got)
		}
	}
}

// TestTokensSatisfyTheContract は lexical.Tokenizer の契約を機械で確かめる。
//
// 🔴 空白と tsquery のメタ文字を含まないことは、ストアが検索式を組み立てる
// 前提になっている。破ると SQL の構文エラーか、DB 内で1トークンが2つの
// レキシームに割れるという検出しにくい壊れ方になる。分割器を書き換える人が
// この性質を落とさないよう、意地の悪い入力で縛る。
func TestTokensSatisfyTheContract(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"a & b | c ! d ( e ) f : g * h < i > j",
		`引用符 ' と " と \ を含む文`,
		"RECALL_STORE|DROP TABLE chunks",
		"全角記号　＆｜！（）：＊＜＞",
		"混在 mixed テキスト text 0.8.6 bge-m3",
		"🔴 絵文字と組版記号 ―─※〜",
	}

	for _, text := range inputs {
		for _, token := range bigram.New().Tokenize(text) {
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
func TestTokenizeIsDeterministic(t *testing.T) {
	t.Parallel()

	const text = "ハイブリッド検索は alpha*vector + (1-alpha)*lexical で合成する"

	first := bigram.New().Tokenize(text)
	for range 3 {
		if got := bigram.New().Tokenize(text); !slices.Equal(got, first) {
			t.Fatalf("出力が揺れた: %v / %v", first, got)
		}
	}
}

// TestID は識別子が固定されていることを確かめる。
//
// 🔴 この値を変えるのは分割規則を変えたときだけである。テストを「実装に
// 合わせて」書き換えるのではなく、規則を変えたなら値も変える、という順序を
// 守るためにリテラルで縛る。保存済みトークン列との照合がこの値に依存している。
func TestID(t *testing.T) {
	t.Parallel()

	if got := bigram.New().ID(); got != "bigram:nfkc-lower:v1" {
		t.Errorf("ID() = %q", got)
	}
}
