package kagome_test

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/internal/asciiword"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/kagome"
)

// tsqueryMetaCharacters は lexical.Tokenizer の契約が禁じている文字。
//
// PostgreSQL の tsquery でこれらは演算子・引用符として意味を持つ。トークンが
// 検索式の被演算子としてそのまま置かれるので、混ざると構文が壊れる。
const tsqueryMetaCharacters = `&|!():*<>'"\`

// newTokenizer は分割器を組み立てる。辞書の読み込みに失敗したら測る意味が無い。
func newTokenizer(t *testing.T) *kagome.Tokenizer {
	t.Helper()

	tokenizer, err := kagome.New()
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	return tokenizer
}

// TestTokenizeSplitsJapaneseIntoWords は日本語が語で割れることを確かめる。
//
// 🔑 bigram との差はここに出る。「ベクトルの索引を張る」は bigram では
// 「ルの」「の索」「を張」のような語境界を跨いだトークンを生むが、形態素では
// 生まない。ADR 0018 が「形態素で変わるのは、何を拾うかより何を拾わないか」と
// 書いているのがこの性質である。
func TestTokenizeSplitsJapaneseIntoWords(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "語で割れる",
			text: "検索対象",
			want: []string{"検索", "対象"},
		},
		{
			name: "語境界を跨ぐトークンを作らない",
			text: "ベクトルの索引を張ると検索は速くなるか",
			want: []string{"ベクトル", "索引", "張る", "検索", "速い", "なる"},
		},
		{
			name: "長音符を含む語は割れない",
			text: "サーバーの応答時間を測る",
			want: []string{"サーバー", "応答", "時間", "測る"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := newTokenizer(t).Tokenize(tc.text)
			if !slices.Equal(got, tc.want) {
				t.Errorf("Tokenize(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestTokenizeFoldsInflectionsToTheBaseForm は活用形が原形に畳まれることを確かめる。
//
// 🔴 これが形態素を採る理由そのものである (ADR 0018 Decision 2 の手順3)。
// 「張った」と「張る」が別トークンのままなら、活用の違いだけで語彙一致が落ちる。
// 表層形をトークンにする案は ADR 0018 が却下している。
func TestTokenizeFoldsInflectionsToTheBaseForm(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"張った":  {"張る"},
		"張らない": {"張る"},
		"測った値": {"測る", "値"},
	}

	tokenizer := newTokenizer(t)

	for text, want := range cases {
		if got := tokenizer.Tokenize(text); !slices.Equal(got, want) {
			t.Errorf("Tokenize(%q) = %v, want %v", text, got, want)
		}
	}
}

// TestTokenizeDropsFunctionWords は助詞・助動詞・記号が落ちることを確かめる。
//
// 🔴 ts_rank は IDF を持たないので、高頻度の機能語を残すと長文ほど加点される。
// ADR 0014 が実測で退けた長さ正規化の問題を裏口から呼び戻さないための規則で
// ある (ADR 0018 Decision 2 の手順4)。
//
// ⚠️ 助詞を落とす以上、`particle` タグ（「が」と「を」の撃ち分け）は原理的に
// 語彙側では扱えない。ADR 0018 はそれを承知の上で v1 の規則にしており、
// 助詞を残す v2 は測ってから判断する。このテストは「望ましい」ではなく
// 「そう決めた」を固定している。
func TestTokenizeDropsFunctionWords(t *testing.T) {
	t.Parallel()

	tokenizer := newTokenizer(t)

	// 「を」「は」は助詞、「ない」は助動詞、「。」は記号。
	got := tokenizer.Tokenize("索引を張らない。検索は遅い")

	for _, dropped := range []string{"を", "は", "ない", "。"} {
		if slices.Contains(got, dropped) {
			t.Errorf("機能語 %q が残った: %v", dropped, got)
		}
	}

	if !slices.Contains(got, "索引") {
		t.Errorf("自立語が落ちた: %v", got)
	}
}

// TestTokenizeIsBlindToArgumentOrder は最小対のトークン集合が一致することを固定する。
//
// 🔴 これは「壊れている」ことのテストではなく、分割器の能力の限界を明示する
// テストである。助詞を捨てる規則を採った以上、q-057 / q-058（助詞の対で正解集合が
// 交わらない）の撃ち分けは語彙側では原理的にできない。bigram にも同じテストが
// あり、そちらは別の理由（語をまたぐ bigram を作らない）で同じ結論になる。
// 撃ち分けが改善したという観測が出たら、それは語彙側の働きではなく合成の副作用である。
func TestTokenizeIsBlindToArgumentOrder(t *testing.T) {
	t.Parallel()

	tokenizer := newTokenizer(t)
	forward := tokenizer.Tokenize("Recall が落ちたら Corpus は動き続けるか")
	reverse := tokenizer.Tokenize("Corpus が落ちたら Recall は動き続けるか")

	slices.Sort(forward)
	slices.Sort(reverse)

	if !slices.Equal(forward, reverse) {
		t.Errorf("最小対のトークン集合が違う: %v / %v", forward, reverse)
	}
}

// TestTokenizeMatchesBigramOnASCIIWords は ASCII の語の扱いが bigram と
// 1トークンも違わないことを確かめる。
//
// 🔴 これが ADR 0018 Decision 2 の手順2 の中身である。`pgvector`・`0.8.6`・
// `RECALL_STORE` のような識別子は形態素解析の対象ではなく、bigram が
// `exact-term` で稼いでいる精度をそのまま引き継がなければならない。
// ここがずれると、2つの分割器の比較は「日本語の割り方の差」ではなく
// 「識別子の扱いの差」を測ることになり、Q-2 の判断材料にならない。
//
// 🔑 規則そのものは internal/lexical/internal/asciiword に1つだけ置いて共有して
// いる。このテストは共有が実際に効いていること（片方だけ前処理や連結子を
// 変えていないこと）を機械で見る。
func TestTokenizeMatchesBigramOnASCIIWords(t *testing.T) {
	t.Parallel()

	// ASCII と区切り記号だけで構成した入力。両者の出力は完全に一致するはずである。
	asciiOnly := []string{
		"RECALL_STORE",
		"pgvector 0.8.6",
		"PdoChunkSearchRepository.php",
		"bge-m3 golangci-lint",
		"POST /v1/search",
		"end. next",
		"-- __ ..",
		"ＲＥＣＡＬＬ＿ＳＴＯＲＥ",
		"PgVector",
	}

	tokenizer := newTokenizer(t)

	for _, text := range asciiOnly {
		got := tokenizer.Tokenize(text)
		want := bigram.New().Tokenize(text)

		if !slices.Equal(got, want) {
			t.Errorf("Tokenize(%q) = %v, bigram = %v", text, got, want)
		}
	}

	// 日本語が混ざっても、ASCII 語だけを取り出せば一致する。
	mixed := []string{
		"RECALL_STORE は postgres と sqlite を切り替える",
		"pgvector 0.8.6 で測った",
		"埋め込みは bge-m3 をローカルで動かす",
		"POST /v1/search で検索する",
	}

	for _, text := range mixed {
		got := asciiTokens(tokenizer.Tokenize(text))
		want := asciiTokens(bigram.New().Tokenize(text))

		if !slices.Equal(got, want) {
			t.Errorf("Tokenize(%q) の ASCII 語 = %v, bigram = %v", text, got, want)
		}
	}
}

// asciiTokens は CJK を1文字も含まないトークンだけを順序どおりに取り出す。
func asciiTokens(tokens []string) []string {
	kept := []string{}

	for _, token := range tokens {
		if !strings.ContainsFunc(token, asciiword.IsCJK) {
			kept = append(kept, token)
		}
	}

	return kept
}

// TestTokenizeReturnsEmptyForSymbolsOnly は分割できる語が無い入力を確かめる。
//
// これはエラーではない。呼び出し側は語彙スコア 0 として扱い、合成は
// alpha*vector に縮退する。
func TestTokenizeReturnsEmptyForSymbolsOnly(t *testing.T) {
	t.Parallel()

	tokenizer := newTokenizer(t)

	for _, text := range []string{"", "🔴🔑", "   ", "!!! ??? <#>", "。、！"} {
		if got := tokenizer.Tokenize(text); len(got) != 0 {
			t.Errorf("Tokenize(%q) = %v, want empty", text, got)
		}
	}
}

// TestTokensSatisfyTheContract は lexical.Tokenizer の契約を機械で確かめる。
//
// 🔴 空白と tsquery のメタ文字を含まないことは、ストアが検索式を組み立てる
// 前提になっている。破ると SQL の構文エラーか、DB 内で1トークンが2つの
// レキシームに割れるという検出しにくい壊れ方になる。
//
// 🔑 形態素側には bigram に無い流出経路がある——IPA 辞書は原形を持たない語に
// "*" を入れるので、原形をそのままトークンにすると tsquery のメタ文字が出る。
// 意地の悪い入力で縛るのは、その経路を塞いだままに保つためである。
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
// 🔑 分割器を作り直しても同じ出力になることまで見る。kagome の解析器は
// 内部に pool を持つので、インスタンスをまたいで結果が変わらないことは
// 自明ではない（変われば、再起動しただけで保存済みの索引が無効になる）。
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
// 🔴 bigram と違う値であること。同じ値だと、分割器を切り替えたときに
// ストアの不一致検知が発火せず、規則の違うトークン列が混ざる。
func TestID(t *testing.T) {
	t.Parallel()

	got := newTokenizer(t).ID()

	if want := "kagome:ipadic:ascii-words:v1"; got != want {
		t.Errorf("ID() = %q, want %q", got, want)
	}

	if got == bigram.New().ID() {
		t.Errorf("bigram と同じ識別子を名乗っている: %q", got)
	}
}

// TestTokenizeSplitsAtASCIIBoundaries は、ASCII を挟むと形態素解析が別々の
// 断片として走ることを固定する。
//
// 🔑 これは「望ましい」ではなく「そうなっている」を記録するテストである。
// bigram も同じ位置で切っており (TestTokenizeMixed)、切り方を揃えることで
// 2つの分割器の差に「文の切り方の差」が混ざらないようにしている。
//
// ⚠️ 代償はある。断片の先頭に来た「で」は文頭の接続詞として解析され、
// 助詞として落ちない（実測）。文全体を渡していれば格助詞になっていた。
// この性質は v1 の規則の帰結であり、変えるなら id も変える。
func TestTokenizeSplitsAtASCIIBoundaries(t *testing.T) {
	t.Parallel()

	got := newTokenizer(t).Tokenize("POST /v1/search で検索する")
	want := []string{"post", "v1/search", "で", "検索", "する"}

	if !slices.Equal(got, want) {
		t.Errorf("Tokenize = %v, want %v", got, want)
	}
}
