package asciiword_test

import (
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/lexical/internal/asciiword"
)

// 🔴 このパッケージは2つの分割器が**同じ結果を出す**ための共有部分である。
// 直接テストを持つのは、規則が片方の分割器のテスト越しにしか見えない状態を
// 作らないためである。ここが変われば両方の分割器が同時に変わるので、
// 変更の影響範囲がテストの形で読めることに意味がある。

// TestNormalizeFoldsBeforeSplitting は前処理が表記を1つの形へ寄せることを確かめる。
//
// 🔴 分割より前に行う前提がここに現れる。全角英数は半角へ、半角カナは全角へ、
// 大文字は小文字へ。この順序を崩すと、全角の識別子が CJK 側に落ちる。
func TestNormalizeFoldsBeforeSplitting(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ＲＥＣＡＬＬ＿ＳＴＯＲＥ": "recall_store",
		"PgVector":     "pgvector",
		"ｻｰﾊﾞ":         "サーバ",
		"検索対象":         "検索対象",
	}

	for text, want := range cases {
		if got := asciiword.Normalize(text); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", text, got, want)
		}
	}
}

// TestIsCJKCoversTheMarksScriptsMiss は Script では拾えない記号を確かめる。
//
// 🔴 長音符 "ー" と "〆" は Script が Common で、Han にも Hiragana にも
// Katakana にも属さない。CJK から外すと「サーバー」が1文字ずつに割れ、
// 表記ゆれを測るはずのクエリが分割器の欠陥を測ることになる。
func TestIsCJKCoversTheMarksScriptsMiss(t *testing.T) {
	t.Parallel()

	for _, r := range []rune{'ー', '〆', '検', 'あ', 'ア'} {
		if !asciiword.IsCJK(r) {
			t.Errorf("IsCJK(%q) = false", r)
		}
	}

	for _, r := range []rune{'a', '0', '_', ' ', '。', '🔴'} {
		if asciiword.IsCJK(r) {
			t.Errorf("IsCJK(%q) = true", r)
		}
	}
}

// TestIsWordExcludesCJK は CJK を語構成文字に数えないことを確かめる。
//
// ひらがなも漢字も unicode.IsLetter が真を返すので、CJK を先に除く順序が
// そのまま分割の規則になっている。
func TestIsWordExcludesCJK(t *testing.T) {
	t.Parallel()

	for _, r := range []rune{'a', 'z', '0', '9', 'é'} {
		if !asciiword.IsWord(r) {
			t.Errorf("IsWord(%q) = false", r)
		}
	}

	for _, r := range []rune{'検', 'あ', 'ア', 'ー', '_', '.', ' '} {
		if asciiword.IsWord(r) {
			t.Errorf("IsWord(%q) = true", r)
		}
	}
}

// TestIsConnectorMatchesTheDocumentedSet は連結子の集合を固定する。
//
// 🔴 tsquery のメタ文字を1つも含まないこと。トークンが検索式の被演算子として
// そのまま置かれるので、混ざると構文が壊れる。
func TestIsConnectorMatchesTheDocumentedSet(t *testing.T) {
	t.Parallel()

	for _, r := range asciiword.Connectors {
		if !asciiword.IsConnector(r) {
			t.Errorf("IsConnector(%q) = false", r)
		}
	}

	for _, r := range []rune{'&', '|', '!', '(', ')', ':', '*', '<', '>', '\'', '"', '\\'} {
		if asciiword.IsConnector(r) {
			t.Errorf("🔴 tsquery のメタ文字 %q が連結子になっている", r)
		}
	}
}

// TestEndTakesConnectorsOnlyInside は連結子を語の内側でだけ取り込むことを確かめる。
//
// "0.8.6" は1トークンになり、"末尾です." の末尾の "." は落ちる。落とさないと、
// 同じ語が文末にあるかどうかで別トークンになる。
func TestEndTakesConnectorsOnlyInside(t *testing.T) {
	t.Parallel()

	cases := []struct {
		text string
		want string
	}{
		{text: "0.8.6", want: "0.8.6"},
		{text: "recall_store", want: "recall_store"},
		{text: "v1/search", want: "v1/search"},
		{text: "end. next", want: "end"},
		{text: "end.", want: "end"},
	}

	// ⚠️ start が語構成文字を指していることが前提である。呼び出し側（2つの
	// 分割器）は IsWord で分岐してから呼ぶので、連結子から始まる呼び出しは
	// 起きない。前提を書いておかないと、"-abc" を丸ごと拾う挙動が仕様に見える。

	for _, tc := range cases {
		runes := []rune(tc.text)
		if got := string(runes[:asciiword.End(runes, 0)]); got != tc.want {
			t.Errorf("End(%q) が切り出したのは %q, want %q", tc.text, got, tc.want)
		}
	}
}

// TestRunEndStopsAtTheClassBoundary は同じ文字クラスの連続部の終端を確かめる。
func TestRunEndStopsAtTheClassBoundary(t *testing.T) {
	t.Parallel()

	runes := []rune("検索するrecall")

	if got, want := asciiword.RunEnd(runes, 0, asciiword.IsCJK), 4; got != want {
		t.Errorf("RunEnd = %d, want %d", got, want)
	}

	if got, want := asciiword.RunEnd(runes, 4, asciiword.IsCJK), 4; got != want {
		t.Errorf("CJK でない位置の RunEnd = %d, want %d（進めてはいけない）", got, want)
	}
}
