package eval_test

import (
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// longChunkKey は名指しで追跡される長文チャンクの1つ。
//
// 🔴 リテラルで書く。eval.LongGoldKeys() から取ると、一覧が空になっても
// テストが通ってしまう。追跡の対象が実際にこのキーであることを縛る。
const longChunkKey = "readme#005"

// goldLengthDataset は長短の gold が混ざったデータセットを返す。
//
// short は 520字以下、long は 520字超になるよう本文の長さを作る。
// 内訳の境界が閾値どおりに引かれているかを見るのが目的なので、本文の中身は
// 検索結果に影響しない（偽の索引が順位を決める）。
func goldLengthDataset() eval.Dataset {
	return eval.Dataset{
		Passages: []eval.Passage{
			{Key: "short#001", Source: "short", Content: shortContentA()},
			// ちょうど閾値。定義は「520字以下が短い側」なのでこちらに入る。
			{Key: "short#002", Source: "short", Content: shortContentB()},
			{Key: longChunkKey, Source: "readme", Content: longContent()},
			{Key: "distractor#001", Source: "distractor", Content: distractorContent()},
		},
		Queries: []eval.Query{
			{
				ID: "q-1", Text: "問い1",
				Relevant: []string{"short#001", longChunkKey},
				Tags:     []string{"語彙一致"}, Note: "短い gold と長い gold を1件ずつ",
			},
			{
				ID: "q-2", Text: "問い2",
				Relevant: []string{"short#002"},
				Tags:     []string{"言い換え"}, Note: "ちょうど閾値の gold",
			},
		},
	}
}

// shortContentA は 100字の本文。
//
// 🔴 偽の索引は本文で順位を書く仕組みなので、長さの調整に使う詰め物と
// 見分けのつく先頭を持たせる。長さだけを合わせた同一文字列にすると、
// どの chunk を指しているのかテストの読み手が追えない。
func shortContentA() string { return "短い当たり" + strings.Repeat("あ", 95) }

// shortContentB はちょうど閾値（520字）の本文。
func shortContentB() string {
	return "境界ちょうど" + strings.Repeat("い", eval.GoldLengthThreshold-6)
}

// longContent は 1,136字の本文。readme#005 の実寸に合わせてある。
func longContent() string { return "長い一覧表" + strings.Repeat("う", 1131) }

// distractorContent はどのクエリの正解にもならない本文。
func distractorContent() string { return "紛れ込み" }

// TestSummarizeSplitsMicroRecallByGoldLength は長さ別の内訳を確かめる。
//
// 🔴 これが無いと Q-1 の比較が交絡する。BM25 は文書長で正規化し ts_rank は
// 既定でしないので、長文 gold が繰り返し正解になっているこの評価セットでは、
// 総合値の差が「検索品質の差」なのか「長文優遇の差」なのか切り分けられない。
func TestSummarizeSplitsMicroRecallByGoldLength(t *testing.T) {
	idx := newFakeIndex()
	// q-1: 短い gold は1位で当たり、長い gold は圏外。
	idx.ranking["問い1"] = []string{shortContentA(), distractorContent()}
	// q-2: ちょうど閾値の gold が当たる。
	idx.ranking["問い2"] = []string{shortContentB()}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), goldLengthDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if want := (eval.MicroRecall{Hits: 2, Total: 3, Cutoff: 10, Value: 2.0 / 3.0}); got.Summary.MicroRecall != want {
		t.Errorf("micro = %+v, want %+v", got.Summary.MicroRecall, want)
	}

	buckets := got.Summary.GoldLengthRecall
	if len(buckets) != 2 {
		t.Fatalf("区分の数 = %d, want 2", len(buckets))
	}

	// 短い側: short#001（当たり）と short#002（当たり）の 2/2。
	assertBucket(t, buckets[0], eval.GoldLengthBucket{
		Label: "<=520", MinRunes: 0, MaxRunes: eval.GoldLengthThreshold,
		Hits: 2, Total: 2, Value: 1,
	})
	// 長い側: readme#005（圏外）の 0/1。
	assertBucket(t, buckets[1], eval.GoldLengthBucket{
		Label: ">520", MinRunes: eval.GoldLengthThreshold + 1, MaxRunes: 0,
		Hits: 0, Total: 1, Value: 0,
	})
}

// TestSummarizeTracksNamedLongChunks は名指しの長文チャンクを追うことを確かめる。
func TestSummarizeTracksNamedLongChunks(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{shortContentA(), distractorContent()}
	idx.ranking["問い2"] = []string{shortContentB()}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), goldLengthDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	tracked := got.Summary.LongChunkRecall
	if tracked.Total != 1 || tracked.Hits != 0 {
		t.Errorf("名指しの追跡 = %d/%d, want 0/1", tracked.Hits, tracked.Total)
	}

	assertTrackedKey(t, tracked.Keys, eval.LongChunkKey{
		Key: longChunkKey, Runes: 1136, Hits: 0, Total: 1,
	})
}

// assertTrackedKey は名指しの追跡対象1件が期待どおりであることを確かめる。
func assertTrackedKey(t *testing.T, keys []eval.LongChunkKey, want eval.LongChunkKey) {
	t.Helper()

	for _, got := range keys {
		if got.Key != want.Key {
			continue
		}

		if got != want {
			t.Errorf("追跡 %s = %+v, want %+v", want.Key, got, want)
		}

		return
	}

	t.Errorf("%s が追跡対象に入っていない", want.Key)
}

// TestSummarizeReportsZeroRunesForMissingLongChunk は、名指しのキーが
// コーパスに無いことがレポートから読めることを確かめる。
//
// 🔴 評価セットを作り直すと、名指しの一覧は静かに古くなる。計測を止めない
// 代わりに、文字数 0 という形で「このキーはもう無い」を残す。止めないのは、
// 付帯情報の欠落で計測そのものを失うほうが損だからである。
func TestSummarizeReportsZeroRunesForMissingLongChunk(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	tracked := got.Summary.LongChunkRecall
	if len(tracked.Keys) != len(eval.LongGoldKeys()) {
		t.Fatalf("追跡対象の数 = %d, want %d", len(tracked.Keys), len(eval.LongGoldKeys()))
	}

	for _, key := range tracked.Keys {
		if key.Runes != 0 {
			t.Errorf("%s の文字数 = %d, want 0（コーパスに無いはず）", key.Key, key.Runes)
		}

		if key.Total != 0 {
			t.Errorf("%s が正解になったクエリ数 = %d, want 0", key.Key, key.Total)
		}
	}

	if tracked.Total != 0 || tracked.Value != 0 {
		t.Errorf("追跡の合計 = %d/%d, want 0/0", tracked.Hits, tracked.Total)
	}
}

// TestMicroRecallIsRecomputableFromRawData は、内訳が per-query の生データから
// 再計算できることを確かめる。
//
// 🔑 ADR 0013 Decision 7 の要求そのものである。集計値だけのレポートは、それが
// 正しいことを第三者が確かめられない。ここでは relevant_ranks だけを使って
// 数え直し、summary と一致することを見る。
func TestMicroRecallIsRecomputableFromRawData(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{shortContentA(), distractorContent()}
	idx.ranking["問い2"] = []string{shortContentB()}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), goldLengthDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	hits, total := 0, 0

	for _, q := range got.Queries {
		for _, rank := range q.RelevantRanks {
			total++

			if rank.Rank != nil && *rank.Rank <= got.Summary.MicroRecall.Cutoff {
				hits++
			}
		}
	}

	if hits != got.Summary.MicroRecall.Hits || total != got.Summary.MicroRecall.Total {
		t.Errorf("生データからの再計算 = %d/%d, summary = %d/%d",
			hits, total, got.Summary.MicroRecall.Hits, got.Summary.MicroRecall.Total)
	}
}

// assertBucket は長さ別内訳の1区分を確かめる。
//
// 区分の境界（MinRunes / MaxRunes）まで含めて丸ごと比べる。件数だけを見ると、
// 閾値の定義がずれても気づけない。
func assertBucket(t *testing.T, got, want eval.GoldLengthBucket) {
	t.Helper()

	if got != want {
		t.Errorf("区分 = %+v, want %+v", got, want)
	}
}
