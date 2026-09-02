package main_test

import (
	"testing"

	main "github.com/hideyukiMORI/nene-recall/tools/wikidistract"
)

// TestPlainTextRemovesMarkup は粗い除去が何を落とし何を残すかを固定する。
func TestPlainTextRemovesMarkup(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"コメント":       {in: "前<!-- 消える -->後", want: "前後"},
		"出典":         {in: "本文<ref name=\"a\">出典</ref>。", want: "本文。"},
		"自己閉じの出典":    {in: "本文<ref name=\"a\" />。", want: "本文。"},
		"入れ子のテンプレート": {in: "前{{A|{{B|c}}}}後", want: "前後"},
		"表":          {in: "前\n{|\n|見出し\n|}\n後", want: "前\n\n後"},
		"リンク":        {in: "[[記事]]を見る", want: "記事を見る"},
		"パイプ付きリンク":   {in: "[[記事|表示]]を見る", want: "表示を見る"},
		"画像":         {in: "前[[ファイル:a.png|thumb|説明]]後", want: "前後"},
		"分類":         {in: "前[[Category:あ]]後", want: "前後"},
		"英語名の画像":     {in: "前[[File:a.png|thumb]]後", want: "前後"},
		"入れ子のリンク":    {in: "[[ファイル:a.png|[[別記事]]の説明]]本文", want: "本文"},
		"外部リンク":      {in: "[https://example.com 例]を見る", want: "例を見る"},
		"表示の無い外部リンク": {in: "前[https://example.com]後", want: "前後"},
		"強調":         {in: "'''強い'''と''弱い''", want: "強いと弱い"},
		"魔法語":        {in: "__NOTOC__本文", want: "本文"},
		"残ったタグ":      {in: "前<br />後", want: "前後"},
		"実体参照":       {in: "A&amp;B", want: "A&B"},
		// 🔴 実体参照のまま残すと、段落の除外規則（`<` を含めば捨てる）を
		// すり抜けて「タグの書きかけ」が本文として残る。
		"不等号の実体参照": {in: "a&lt;b", want: "a<b"},
	}

	stripper := main.NewStripper()

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := stripper.PlainText(tc.in); got != tc.want {
				t.Errorf("PlainText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestPlainTextDropsUnclosedMarkup は閉じない記法を残さないことを見る。
//
// 🔑 壊れた記法を本文として残すより、その段落ごと失うほうが安全である。
// 残すと「{{」を含む文字列が投入され、distractor の分布が現実から離れる。
func TestPlainTextDropsUnclosedMarkup(t *testing.T) {
	stripper := main.NewStripper()

	cases := map[string]string{
		"閉じないテンプレート": "前{{A|B",
		"閉じないリンク":    "前[[記事",
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got := stripper.PlainText(in); got != "前" {
				t.Errorf("PlainText(%q) = %q, want %q", in, got, "前")
			}
		})
	}
}

// TestPlainTextIsDeterministic は同じ入力から同じ出力が出ることを見る。
//
// 🔴 出力の sha256 を README に記録して再現性を担保する設計なので
// (docs/adr/0019-large-scale-benchmark-corpus.md)、決定性は仕様である。
func TestPlainTextIsDeterministic(t *testing.T) {
	const in = "{{Infobox|a=1}}'''日本語'''の[[記事|文章]]。<ref>出典</ref>"

	first := main.NewStripper().PlainText(in)
	second := main.NewStripper().PlainText(in)

	if first != second {
		t.Fatalf("2回の結果が違う: %q / %q", first, second)
	}
}
