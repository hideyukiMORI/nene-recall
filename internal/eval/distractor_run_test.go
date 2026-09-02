package eval_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// distractorRecord はテストで使う紛れ込みの記録。件数だけが検査される。
func distractorRecord(count int) *eval.FileInput {
	return &eval.FileInput{Path: "bin/distractors.jsonl", SHA256: "deadbeef", Count: count}
}

// datasetWith は紛れ込みを足した評価セットを返す。
func datasetWith(distractors []eval.Distractor) eval.Dataset {
	ds := smallDataset()
	ds.Distractors = distractors

	return ds
}

// optionsWith は紛れ込みの記録を足した計測条件を返す。
func optionsWith(t *testing.T, record *eval.FileInput) eval.Options {
	t.Helper()

	opts := defaultOptions(t, 1)
	opts.Distractors = record

	return opts
}

// TestMeasureIngestsDistractorsInBatches は 10万件を1回の Put に渡さないことを見る。
//
// 🔴 Store.Put は1回の呼び出しが1トランザクションで、全件の埋め込みを済ませて
// から挿入する。10万件を1回で渡すと、埋め込みの約18分ぶんトランザクションを
// 握り続けることになる (docs/adr/0019-large-scale-benchmark-corpus.md)。
//
// 🔑 合計件数だけを数えない。合計は1回で全部渡す実装でも一致するので、
// 「何件ずつ渡したか」の並びで見る。
func TestMeasureIngestsDistractorsInBatches(t *testing.T) {
	const count = 2500

	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	_, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).Measure(
		t.Context(), datasetWith(distractorLines(count)), optionsWith(t, distractorRecord(count)))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	// 先頭は評価コーパス 3 件。以降が紛れ込みのバッチである。
	want := []int{3, eval.IngestBatchSize, eval.IngestBatchSize, count - 2*eval.IngestBatchSize}
	if !slices.Equal(idx.putBatches, want) {
		t.Fatalf("Put に渡した件数 = %v, want %v", idx.putBatches, want)
	}
}

// TestMeasureIngestsCorpusBeforeDistractors は投入の順序を見る。
//
// 🔴 評価コーパスが先である。先に紛れ込みを入れると、eval_key → id の写像を
// 作る Put の前に別の Put が挟まる。実ストアの採番は投入順に進むので、
// 順序が変われば写像そのものは壊れないが、「どの Put の返り値が写像か」が
// 呼び出し側の記憶に依存するようになる (ADR 0013)。
func TestMeasureIngestsCorpusBeforeDistractors(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).Measure(
		t.Context(), datasetWith(distractorLines(2)), optionsWith(t, distractorRecord(2)))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if len(idx.putBatches) != 2 || idx.putBatches[0] != 3 {
		t.Fatalf("Put に渡した件数 = %v, want [3 2]", idx.putBatches)
	}

	// 正解の写像は紛れ込みを足しても動かない。
	if got.Queries[0].RankedKeys[0].Key != "doc-a#001" {
		t.Errorf("1位 = %q, want doc-a#001", got.Queries[0].RankedKeys[0].Key)
	}
}

// TestMeasureNamesDistractorsInTheRanking は紛れ込みが上位に入っても
// 計測が止まらず、表示名で報告されることを見る。
//
// 🔴 これが押し出しの検査そのものである。rankedEntries は写像に無い id を
// 拒否するので、紛れ込みを写像へ足していない実装では「押し出しが起きた瞬間に
// 計測が落ちる」ハーネスになる (ADR 0019 Decision 2)。
func TestMeasureNamesDistractorsInTheRanking(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"紛れ込みの本文 0", "当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).Measure(
		t.Context(), datasetWith(distractorLines(2)), optionsWith(t, distractorRecord(2)))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	keys := eval.RankedKeysOf(got.Queries[0].RankedKeys)
	if len(keys) != 2 || keys[0] != "distractor:9000000#0" || keys[1] != "doc-a#001" {
		t.Fatalf("順位 = %v, want [distractor:9000000#0 doc-a#001]", keys)
	}

	// 押し出されたぶんは順位が下がるだけで、正解の判定は変わらない。
	if got.Queries[0].ReciprocalRank != 0.5 {
		t.Errorf("reciprocal_rank = %v, want 0.5", got.Queries[0].ReciprocalRank)
	}
}

// TestMeasureRequiresTheDistractorRecord は投入と記録が対であることを見る。
//
// 🔴 記録の無いレポートは、紛れ込みの無い数字と並べて読めない。recall の
// 定義は変わらないが意味は変わる (ADR 0019 Decision 2)。
func TestMeasureRequiresTheDistractorRecord(t *testing.T) {
	cases := map[string]struct {
		items  []eval.Distractor
		record *eval.FileInput
	}{
		"投入したのに記録が無い":    {items: distractorLines(2), record: nil},
		"記録があるのに投入していない": {items: nil, record: distractorRecord(2)},
		"記録の件数が実際と違う":    {items: distractorLines(2), record: distractorRecord(3)},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			idx := newFakeIndex()

			_, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).Measure(
				t.Context(), datasetWith(tc.items), optionsWith(t, tc.record))
			if !errors.Is(err, eval.ErrMeasure) {
				t.Fatalf("err = %v, want ErrMeasure", err)
			}

			if len(idx.putBatches) != 0 {
				t.Errorf("食い違いを検知する前に投入した: %v", idx.putBatches)
			}
		})
	}
}

// TestMeasureRejectsReservedEvalKeyPrefix は正解キーが紛れ込みの接頭辞を
// 使っていたら止めることを見る。
//
// 🔑 衝突すると写像が上書きされ、正解チャンクが「紛れ込みとして返ってきた」
// 形になる。症状は recall の低下だけなので気づけない。
func TestMeasureRejectsReservedEvalKeyPrefix(t *testing.T) {
	ds := smallDataset()
	ds.Passages[0].Key = eval.DistractorKeyPrefix + "9000000#0"

	_, err := newTestRunner(t, newFakeIndex(), &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), ds, defaultOptions(t, 1))
	if !errors.Is(err, eval.ErrInvalidDataset) {
		t.Fatalf("err = %v, want ErrInvalidDataset", err)
	}
}

// TestConditionsRecordDistractorsAndCache は条件表に2項目が残ることを見る。
func TestConditionsRecordDistractorsAndCache(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	opts := optionsWith(t, distractorRecord(2))
	opts.EmbedCache = true

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).Measure(
		t.Context(), datasetWith(distractorLines(2)), opts)
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if got.Conditions.Distractors == nil {
		t.Fatal("conditions.distractors が記録されていない")
	}

	if got.Conditions.Distractors.Count != 2 {
		t.Errorf("distractors.count = %d, want 2", got.Conditions.Distractors.Count)
	}

	if !got.Conditions.EmbedCache {
		t.Error("conditions.embed_cache が false になっている")
	}
}

// TestConditionsOmitDistractorsWhenAbsent は紛れ込み無しの計測で
// conditions.distractors が nil のままであることを見る。
//
// 🔴 nil だけが「紛れ込み無しで測った」を意味する。0 件の記録を入れると、
// レポートを読む側は「0 件の紛れ込みを投入した」と読む——同じことのようで、
// 「その項目を持たない古い様式」との区別がつかなくなる (GO-004)。
func TestConditionsOmitDistractorsWhenAbsent(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if got.Conditions.Distractors != nil {
		t.Errorf("conditions.distractors = %+v, want nil", got.Conditions.Distractors)
	}

	if got.Conditions.EmbedCache {
		t.Error("conditions.embed_cache が true になっている")
	}
}
