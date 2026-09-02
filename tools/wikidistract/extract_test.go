package main_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
	main "github.com/hideyukiMORI/nene-recall/tools/wikidistract"
)

// dumpPage は1ページぶんの XML を組み立てる。
func dumpPage(id int64, namespace int, body string) string {
	return fmt.Sprintf(
		"<page><title>t%d</title><ns>%d</ns><id>%d</id>"+
			"<revision><id>%d</id><text xml:space=\"preserve\">%s</text></revision></page>",
		id, namespace, id, id*10, body)
}

// dump は複数ページを1つのダンプにする。
func dump(pages ...string) string {
	return "<mediawiki>" + strings.Join(pages, "") + "</mediawiki>"
}

// extract は抽出を走らせて結果と読み込んだ行を返す。
func extract(t *testing.T, xml string, count int) (main.Result, []eval.Distractor) {
	t.Helper()

	var out bytes.Buffer

	result, err := main.NewExtractor(count).Extract(strings.NewReader(xml), &out)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	items, err := eval.LoadDistractors(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("LoadDistractors: %v", err)
	}

	return result, items
}

// TestExtractNumbersIDsFromThePage は id の付け方を固定する。
//
// 🔴 評価セットと衝突しない範囲に置く。評価コーパスの document_id は
// Source 名の初出順に 1 から採番されるので、下駄を履かせないと正面衝突する
// (docs/adr/0019-large-scale-benchmark-corpus.md Decision 1)。
func TestExtractNumbersIDsFromThePage(t *testing.T) {
	body := runes(main.MinRunes) + "\n" + runes(main.MinRunes+1)

	_, items := extract(t, dump(dumpPage(42, 0, body)), 10)

	if len(items) != 2 {
		t.Fatalf("件数 = %d, want 2", len(items))
	}

	for i, item := range items {
		if item.DocumentID != main.DocumentIDOffset+42 {
			t.Errorf("items[%d].document_id = %d, want %d",
				i, item.DocumentID, main.DocumentIDOffset+42)
		}

		if item.SourceID != main.DistractorSourceID {
			t.Errorf("items[%d].source_id = %d, want %d",
				i, item.SourceID, main.DistractorSourceID)
		}

		if item.ChunkIndex != i {
			t.Errorf("items[%d].chunk_index = %d, want %d", i, item.ChunkIndex, i)
		}
	}
}

// TestExtractSkipsNonArticleNamespaces は名前空間 0 以外を読まないことを見る。
func TestExtractSkipsNonArticleNamespaces(t *testing.T) {
	body := runes(main.MinRunes)

	result, items := extract(t, dump(
		dumpPage(1, 14, body), // Category:
		dumpPage(2, 0, body),
	), 10)

	if len(items) != 1 || items[0].DocumentID != main.DocumentIDOffset+2 {
		t.Fatalf("採用された document_id = %v, want [%d]", items, main.DocumentIDOffset+2)
	}

	if result.Pages != 1 {
		t.Errorf("走査したページ = %d, want 1", result.Pages)
	}
}

// TestExtractSkipsRedirects はリダイレクトが本文にならないことを見る。
//
// 🔑 判別する項目を持たない。リダイレクトの本文は "#REDIRECT [[...]]" の
// 1行だけで、行頭 "#" を Selector が落とす。規則の在り処を1つに保っている。
func TestExtractSkipsRedirects(t *testing.T) {
	_, items := extract(t, dump(
		dumpPage(1, 0, "#REDIRECT [[別の記事]]"),
		dumpPage(2, 0, runes(main.MinRunes)),
	), 10)

	if len(items) != 1 || items[0].DocumentID != main.DocumentIDOffset+2 {
		t.Fatalf("採用された結果 = %+v", items)
	}
}

// TestExtractStopsAtTheRequestedCount は件数に達したら止まることを見る。
//
// 🔴 途中で止まるので、最後のページだけ chunk_index が途中で切れる。
// 全ページを読み切ってから切り詰める実装にすると、10万件のために 385 MB の
// ダンプを最後まで展開することになる。
func TestExtractStopsAtTheRequestedCount(t *testing.T) {
	body := strings.Repeat(runes(main.MinRunes)+"\n", 5)

	result, items := extract(t, dump(
		dumpPage(1, 0, body),
		dumpPage(2, 0, body),
		dumpPage(3, 0, body),
	), 7)

	if len(items) != 7 || result.Count != 7 {
		t.Fatalf("件数 = %d / %d, want 7", len(items), result.Count)
	}

	// 3ページ目は読まれない。
	if result.Pages != 2 || result.LastPageID != 2 {
		t.Errorf("走査したページ = %d, 最後の page_id = %d, want 2 / 2",
			result.Pages, result.LastPageID)
	}
}

// TestExtractKeepsPageOrder はページ ID 昇順（ダンプの並び順）で出ることを見る。
func TestExtractKeepsPageOrder(t *testing.T) {
	body := runes(main.MinRunes)

	_, items := extract(t, dump(
		dumpPage(3, 0, body),
		dumpPage(7, 0, body),
		dumpPage(11, 0, body),
	), 10)

	want := []int64{
		main.DocumentIDOffset + 3, main.DocumentIDOffset + 7, main.DocumentIDOffset + 11,
	}
	for i, item := range items {
		if item.DocumentID != want[i] {
			t.Fatalf("並び = %+v, want %v", items, want)
		}
	}
}

// TestExtractIsDeterministic は同じ入力から同じ出力と同じ sha256 が出ることを見る。
//
// 🔴 生成物はリポジトリに入らないので、再現性は「取得手順 ＋ チェックサム」で
// しか担保できない (ADR 0019)。決定性はこの道具の仕様である。
func TestExtractIsDeterministic(t *testing.T) {
	xml := dump(
		dumpPage(1, 0, runes(main.MinRunes)+"\n"+runes(main.MinRunes+7)),
		dumpPage(2, 0, "{{Infobox}}\n"+runes(main.MinRunes+3)),
	)

	var first, second bytes.Buffer

	a, err := main.NewExtractor(10).Extract(strings.NewReader(xml), &first)
	if err != nil {
		t.Fatalf("1回目: %v", err)
	}

	b, err := main.NewExtractor(10).Extract(strings.NewReader(xml), &second)
	if err != nil {
		t.Fatalf("2回目: %v", err)
	}

	if a.SHA256 != b.SHA256 || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("2回の出力が違う: %q / %q", a.SHA256, b.SHA256)
	}

	if a.Count == 0 {
		t.Fatal("1件も出ていない。テストが規則を確かめていない")
	}
}

// TestExtractReportsBrokenXML は壊れた入力を黙って読み飛ばさないことを見る。
func TestExtractReportsBrokenXML(t *testing.T) {
	var out bytes.Buffer

	_, err := main.NewExtractor(10).Extract(
		strings.NewReader("<mediawiki><page><ns>0</ns>"), &out)
	if err == nil {
		t.Fatal("壊れた XML が通ってしまった")
	}
}
