package eval_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// smallDataset は3件のコーパスと2件のクエリからなる評価セット。
//
// 正解は eval_key で書く。DB の id は一切書かない——それが ADR 0013 の中心である。
func smallDataset() eval.Dataset {
	return eval.Dataset{
		Passages: []eval.Passage{
			{Key: "doc-a#001", Source: "doc-a", Content: "当たり"},
			{Key: "doc-a#002", Source: "doc-a", Content: "はずれ"},
			{Key: "doc-b#001", Source: "doc-b", Content: "もう一つの当たり"},
		},
		Queries: []eval.Query{
			{
				ID: "q-1", Text: "問い1", Relevant: []string{"doc-a#001"},
				Tags: []string{"語彙一致"}, Note: "1位に来るはず",
			},
			{
				ID: "q-2", Text: "問い2", Relevant: []string{"doc-b#001"},
				Tags: []string{"言い換え"}, Note: "2位に来るはず",
			},
		},
	}
}

// defaultOptions はテストで使う計測条件。
func defaultOptions(t *testing.T, rounds int) eval.Options {
	t.Helper()

	id, err := org.NewID(1)
	if err != nil {
		t.Fatalf("org.NewID: %v", err)
	}

	return eval.Options{
		OrgID: id, Alpha: 1, AlphaNote: "not tuned",
		Limit: eval.DefaultLimit, Rounds: rounds,
		Ranking: testRanking(),
	}
}

// testRanking はテストで使う順位付け条件の記録。
//
// internal/eval はこの中身を解釈しないので、値そのものに意味は無い。
// 空でないことだけが要求される（条件の記録が欠けたレポートを作らせないため）。
func testRanking() eval.RankingSettings {
	return eval.RankingSettings{
		Fusion: "weighted-sum", Store: "postgres", LexicalScorer: "ts_rank",
		TsRankNormalization: intPtr(0), RRFK: intPtr(60),
	}
}

// newTestRunner は偽の索引と偽の埋め込みで Runner を組む。
func newTestRunner(t *testing.T, idx *fakeIndex, e *fakeEmbedder) *eval.Runner {
	t.Helper()

	runner, err := eval.NewRunner(eval.Dependencies{
		Writer: idx, Searcher: idx, VectorSearcher: idx, EmbedQuery: e.embed,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	return runner
}

// TestMeasureMapsResultsBackToEvalKeys は、DB の採番 id が評価セットに
// 現れないまま順位が eval_key で報告されることを見る。
//
// 🔑 これが ADR 0013 の中心である。偽索引の採番は 101 から始まるので、
// 投入順の添字をそのまま id として使う実装ではここが通らない。
func TestMeasureMapsResultsBackToEvalKeys(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり", "はずれ"}
	idx.ranking["問い2"] = []string{"はずれ", "もう一つの当たり"}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if len(got.Queries) != 2 {
		t.Fatalf("クエリ数 = %d, want 2", len(got.Queries))
	}

	wantFirst := []string{"doc-a#001", "doc-a#002"}
	if !equalStrings(eval.RankedKeysOf(got.Queries[0].RankedKeys), wantFirst) {
		t.Errorf("q-1 の順位 = %v, want %v", got.Queries[0].RankedKeys, wantFirst)
	}

	wantSecond := []string{"doc-a#002", "doc-b#001"}
	if !equalStrings(eval.RankedKeysOf(got.Queries[1].RankedKeys), wantSecond) {
		t.Errorf("q-2 の順位 = %v, want %v", got.Queries[1].RankedKeys, wantSecond)
	}
}

// TestMeasureRecordsTheRankOfEachRelevantKey は、正解ごとの順位が
// 生データに残ることを見る。圏外は null（nil）で残す。
func TestMeasureRecordsTheRankOfEachRelevantKey(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"はずれ", "当たり"} // 正解は2位
	idx.ranking["問い2"] = []string{"はずれ"}        // 正解は圏外

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	first := got.Queries[0].RelevantRanks
	if len(first) != 1 || first[0].Rank == nil || *first[0].Rank != 2 {
		t.Errorf("q-1 の正解の順位 = %+v, want 2", first)
	}

	second := got.Queries[1].RelevantRanks
	if len(second) != 1 || second[0].Rank != nil {
		t.Errorf("q-2 の正解の順位 = %+v, want nil（圏外）", second)
	}
}

// TestMeasureComputesRecallAndMRR は集計値を見る。
//
// q-1 は1位で当たり、q-2 は圏外。したがって recall@10 は 0.5、
// MRR は (1/1 + 0) / 2 = 0.5 になる。
func TestMeasureComputesRecallAndMRR(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり", "はずれ"}
	idx.ranking["問い2"] = []string{"はずれ"}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if want := 0.5; got.Summary.MRR != want {
		t.Errorf("MRR = %v, want %v", got.Summary.MRR, want)
	}

	for _, r := range got.Summary.Recall {
		if r.Value != 0.5 {
			t.Errorf("recall@%d = %v, want 0.5", r.K, r.Value)
		}
	}

	if got.Summary.QueryCount != 2 {
		t.Errorf("QueryCount = %d, want 2", got.Summary.QueryCount)
	}
}

// TestMeasureSummarizesByTag は、タグ別の内訳が総合値と別に出ることを見る。
//
// 🔑 総合値は数十クエリでは動きにくい。どのカテゴリが壊れたかが診断情報になる。
func TestMeasureSummarizesByTag(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"} // 語彙一致は当たる
	idx.ranking["問い2"] = []string{"はずれ"} // 言い換えは外す

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if len(got.Summary.TagRecall) != 2 {
		t.Fatalf("タグ数 = %d, want 2 (%+v)", len(got.Summary.TagRecall), got.Summary.TagRecall)
	}

	// 並びは安定していること（辞書順）。実行のたびに変わると差分が読めない。
	if got.Summary.TagRecall[0].Tag != "言い換え" || got.Summary.TagRecall[1].Tag != "語彙一致" {
		t.Fatalf("タグの並び = %v, %v",
			got.Summary.TagRecall[0].Tag, got.Summary.TagRecall[1].Tag)
	}

	if v := recallAt(t, got.Summary.TagRecall[0].Recall, 10); v != 0 {
		t.Errorf("言い換えの recall@10 = %v, want 0", v)
	}

	if v := recallAt(t, got.Summary.TagRecall[1].Recall, 10); v != 1 {
		t.Errorf("語彙一致の recall@10 = %v, want 1", v)
	}
}

// TestMeasureExcludesTheWarmupPass は、ウォームアップが計測に入らないことを見る。
//
// 🔴 コールドスタート（実測 18.4 秒）が1サンプル混ざるだけで p95 は壊れる。
// 呼び出し回数はクエリ数 ×（ウォームアップ1周 + ラウンド数）になるが、
// 記録される latency はラウンド数ぶんだけである。
func TestMeasureExcludesTheWarmupPass(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	const rounds = 3

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, rounds))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	const queries = 2

	wantCalls := queries * (eval.WarmupRounds + rounds)
	if idx.searchCalls != wantCalls {
		t.Errorf("Search の回数 = %d, want %d", idx.searchCalls, wantCalls)
	}

	if idx.vectorCalls != wantCalls {
		t.Errorf("SearchVector の回数 = %d, want %d", idx.vectorCalls, wantCalls)
	}

	for _, q := range got.Queries {
		if len(q.Latencies) != rounds {
			t.Errorf("%s の latency 数 = %d, want %d", q.QueryID, len(q.Latencies), rounds)
		}
	}

	if got.Summary.Latency.WithEmbedding.Samples != queries*rounds {
		t.Errorf("サンプル数 = %d, want %d",
			got.Summary.Latency.WithEmbedding.Samples, queries*rounds)
	}
}

// TestMeasureEmbedsEachQueryOnceOutsideTheLoop は、系統2 の計測から
// 埋め込み往復が本当に外れていることを見る。
//
// 🔴 ループの中で埋め込むと、除いたはずの往復が計測に混ざる。回数は
// クエリ数と一致していなければならない（ラウンド数には依存しない）。
func TestMeasureEmbedsEachQueryOnceOutsideTheLoop(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	e := &fakeEmbedder{calls: 0, err: nil}

	if _, err := newTestRunner(t, idx, e).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 5)); err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if e.calls != 2 {
		t.Errorf("埋め込みの回数 = %d, want 2（クエリ数ぶんだけ）", e.calls)
	}
}

// TestMeasureRejectsDivergentRankings は、2系統が違う順位を返したら
// 静かに続けないことを見る。
//
// 並べて比較する数字なので、片方が別のものを測っていたら比較に意味が無い。
func TestMeasureRejectsDivergentRankings(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり", "はずれ"}
	idx.ranking["問い2"] = []string{"はずれ", "もう一つの当たり"}
	idx.divergent = true

	_, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if !errors.Is(err, eval.ErrRankingDiverged) {
		t.Errorf("err = %v, want ErrRankingDiverged", err)
	}
}

// TestMeasureRejectsChunksOutsideTheCorpus は、評価コーパス以外の行が
// 混ざっていたら止まることを見る。
//
// 🔴 評価専用 DB を毎回作り直す理由がこれである。無関係な行は順位を汚染する
// が、症状は「recall が少し低い」だけなので気づけない (ADR 0013)。
func TestMeasureRejectsChunksOutsideTheCorpus(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}
	idx.foreignID = 999_999

	_, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if !errors.Is(err, eval.ErrMeasure) {
		t.Errorf("err = %v, want ErrMeasure", err)
	}
}

// TestMeasureRejectsABrokenPutContract は、Put が入力と違う数の id を
// 返したら止まることを見る。
//
// 写像がずれると「順位は正しいのに recall だけが低い」という、原因に
// 辿り着けない壊れ方をする。
func TestMeasureRejectsABrokenPutContract(t *testing.T) {
	idx := newFakeIndex()
	idx.extraIDs = true

	_, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
	if !errors.Is(err, eval.ErrMeasure) {
		t.Errorf("err = %v, want ErrMeasure", err)
	}
}

// TestMeasurePropagatesFailures は、投入・埋め込み・検索の失敗が
// 握り潰されないことを見る。
func TestMeasurePropagatesFailures(t *testing.T) {
	cases := map[string]func(idx *fakeIndex, e *fakeEmbedder){
		"投入が失敗":   func(idx *fakeIndex, _ *fakeEmbedder) { idx.putErr = errFake },
		"埋め込みが失敗": func(_ *fakeIndex, e *fakeEmbedder) { e.err = errFake },
		"検索が失敗":   func(idx *fakeIndex, _ *fakeEmbedder) { idx.searchErr = errFake },
	}

	for name, inject := range cases {
		t.Run(name, func(t *testing.T) {
			idx := newFakeIndex()
			idx.ranking["問い1"] = []string{"当たり"}
			idx.ranking["問い2"] = []string{"もう一つの当たり"}

			e := &fakeEmbedder{calls: 0, err: nil}
			inject(idx, e)

			_, err := newTestRunner(t, idx, e).
				Measure(t.Context(), smallDataset(), defaultOptions(t, 1))
			if !errors.Is(err, errFake) {
				t.Errorf("err = %v, want errFake を連鎖に含むこと", err)
			}
		})
	}
}

// TestMeasureRejectsInvalidOptions は、条件の誤りを計測の前に落とすことを見る。
func TestMeasureRejectsInvalidOptions(t *testing.T) {
	valid := defaultOptions(t, 1)

	note := "not tuned"

	cases := map[string]eval.Options{
		"org_id がゼロ値": {
			OrgID: 0, Alpha: 1, AlphaNote: note, Limit: 10, Rounds: 1, Ranking: testRanking(),
		},
		"limit が 0": {
			OrgID: valid.OrgID, Alpha: 1, AlphaNote: note, Limit: 0, Rounds: 1,
			Ranking: testRanking(),
		},
		"rounds が 0": {
			OrgID: valid.OrgID, Alpha: 1, AlphaNote: note, Limit: 10, Rounds: 0,
			Ranking: testRanking(),
		},
		"alpha が範囲外": {
			OrgID: valid.OrgID, Alpha: 1.5, AlphaNote: note, Limit: 10, Rounds: 1,
			Ranking: testRanking(),
		},
		// 🔴 条件の記録が欠けたレポートは、後から条件を特定できないので正本に
		// なれない。融合方式の記録が空なら計測そのものを止める。
		"融合方式の記録が空": {
			OrgID: valid.OrgID, Alpha: 1, AlphaNote: note, Limit: 10, Rounds: 1,
			Ranking: eval.RankingSettings{
				Fusion: "", Store: "", LexicalScorer: "",
				TsRankNormalization: nil, RRFK: nil,
			},
		},
		// 🔴 但し書きが無い alpha は「調整済みの値」として読まれる
		// (CLAUDE.md 地雷7)。文言は配線点が store に応じて選ぶので、
		// この層でできるのは「空なら止める」ことだけである。
		"alpha の但し書きが空": {
			OrgID: valid.OrgID, Alpha: 1, AlphaNote: "", Limit: 10, Rounds: 1,
			Ranking: testRanking(),
		},
	}

	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			idx := newFakeIndex()

			_, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
				Measure(t.Context(), smallDataset(), opts)
			if !errors.Is(err, eval.ErrMeasure) {
				t.Errorf("err = %v, want ErrMeasure", err)
			}
		})
	}
}

// TestMeasureRejectsAnEmptyDataset は空の入力を拒否することを見る。
func TestMeasureRejectsAnEmptyDataset(t *testing.T) {
	cases := map[string]eval.Dataset{
		"コーパスが空": {Passages: nil, Queries: smallDataset().Queries},
		"クエリが空":  {Passages: smallDataset().Passages, Queries: nil},
	}

	for name, ds := range cases {
		t.Run(name, func(t *testing.T) {
			idx := newFakeIndex()

			_, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
				Measure(t.Context(), ds, defaultOptions(t, 1))
			if !errors.Is(err, eval.ErrInvalidDataset) {
				t.Errorf("err = %v, want ErrInvalidDataset", err)
			}
		})
	}
}

// TestNewRunnerRequiresEveryDependency は、nil の口を持ったまま走らないことを見る。
//
// 計測の途中で落ちると、そこまでの所要時間が「途中で止まった条件で測られた
// 数字」として残りうる。配線の失敗は開始前に落とす。
func TestNewRunnerRequiresEveryDependency(t *testing.T) {
	idx := newFakeIndex()
	e := &fakeEmbedder{calls: 0, err: nil}

	cases := map[string]eval.Dependencies{
		"Writer が nil":         {Writer: nil, Searcher: idx, VectorSearcher: idx, EmbedQuery: e.embed},
		"Searcher が nil":       {Writer: idx, Searcher: nil, VectorSearcher: idx, EmbedQuery: e.embed},
		"VectorSearcher が nil": {Writer: idx, Searcher: idx, VectorSearcher: nil, EmbedQuery: e.embed},
		"EmbedQuery が nil":     {Writer: idx, Searcher: idx, VectorSearcher: idx, EmbedQuery: nil},
	}

	for name, deps := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := eval.NewRunner(deps); !errors.Is(err, eval.ErrMissingDependency) {
				t.Errorf("err = %v, want ErrMissingDependency", err)
			}
		})
	}
}

// TestSummaryIsRecomputableFromTheRawData は、集計値が per-query の生データから
// 再計算できることを見る。
//
// 🔑 これが「後から検証できない数字は正本になれない」への回答である。
// レポートを読んだ第三者が Queries から Summary を出し直せる状態を、
// テストとして固定しておく (ADR 0013)。
func TestSummaryIsRecomputableFromTheRawData(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"はずれ", "当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 2))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	// 生データの ranked_keys と relevant だけから指標を出し直す。
	var recalls, ranks []float64

	for _, q := range got.Queries {
		recalls = append(recalls, eval.RecallAt(eval.RankedKeysOf(q.RankedKeys), q.Relevant, 10))
		ranks = append(ranks, eval.ReciprocalRank(eval.RankedKeysOf(q.RankedKeys), q.Relevant))
	}

	if want := eval.Mean(recalls); recallAt(t, got.Summary.Recall, 10) != want {
		t.Errorf("recall@10 = %v, 生データからの再計算 = %v",
			recallAt(t, got.Summary.Recall, 10), want)
	}

	if want := eval.Mean(ranks); got.Summary.MRR != want {
		t.Errorf("MRR = %v, 生データからの再計算 = %v", got.Summary.MRR, want)
	}

	// latency も同じく生データから出し直せる。
	var samples []float64
	for _, q := range got.Queries {
		for _, l := range q.Latencies {
			samples = append(samples, l.WithEmbeddingMS)
		}
	}

	if len(samples) != got.Summary.Latency.WithEmbedding.Samples {
		t.Errorf("サンプル数 = %d, 生データの件数 = %d",
			got.Summary.Latency.WithEmbedding.Samples, len(samples))
	}
}

// TestMeasureRecordsTheConditions は、条件がレポートに残ることを見る。
//
// alpha に but 書きが付くのは意図的である。レポートは単体で読まれるので、
// 「調整済みの値ではない」を外部の文書に頼らない (CLAUDE.md 地雷7)。
func TestMeasureRecordsTheConditions(t *testing.T) {
	idx := newFakeIndex()
	idx.ranking["問い1"] = []string{"当たり"}
	idx.ranking["問い2"] = []string{"もう一つの当たり"}

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), defaultOptions(t, 3))
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	c := got.Conditions
	if c.Rounds != 3 || c.Limit != eval.DefaultLimit || c.OrgID != 1 {
		t.Errorf("条件 = %+v", c)
	}

	if c.WarmupRounds != eval.WarmupRounds {
		t.Errorf("WarmupRounds = %d, want %d", c.WarmupRounds, eval.WarmupRounds)
	}

	if c.PercentileMethod != eval.PercentileMethod {
		t.Errorf("PercentileMethod = %q, want %q", c.PercentileMethod, eval.PercentileMethod)
	}

	// 🔴 但し書きは配線点が渡したものがそのまま載る。この層が文言を持つと、
	// ストアを知らないはずの internal/eval に Postgres の事情が漏れる
	// (ARC-001)。ここで見るのは「渡したものが載る」ことだけである。
	if c.AlphaNote != defaultOptions(t, 3).AlphaNote {
		t.Errorf("AlphaNote = %q, want %q", c.AlphaNote, defaultOptions(t, 3).AlphaNote)
	}

	if !equalInts(c.KValues, eval.KValues()) {
		t.Errorf("KValues = %v, want %v", c.KValues, eval.KValues())
	}
}

// TestMeasureRecordsAlphaWithoutFloat32Rounding は、条件に刻まれる alpha が
// 検索へ渡すときの float32 の丸めを帯びないことを見る。
//
// 🔴 index.Query.Alpha は float32 という契約なので、計測ループはどこかで
// 必ず落とす。落とした値を条件へ書き戻すと 0.6 が 0.6000000238418579 になり、
// レポートを機械で突き合わせる側で == 0.6 が偽になる（様式 v3 までの実害）。
func TestMeasureRecordsAlphaWithoutFloat32Rounding(t *testing.T) {
	idx := newFakeIndex()

	opts := defaultOptions(t, 1)
	opts.Alpha = 0.6

	got, err := newTestRunner(t, idx, &fakeEmbedder{calls: 0, err: nil}).
		Measure(t.Context(), smallDataset(), opts)
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}

	if got.Conditions.Alpha != 0.6 {
		t.Errorf("Alpha = %v, want 0.6 ちょうど（float32 を経由すると 0.6000000238418579 になる）",
			got.Conditions.Alpha)
	}
}

// recallAt は集計値から k を指定して recall を取り出す。
func recallAt(t *testing.T, values []eval.RecallAtK, k int) float64 {
	t.Helper()

	for _, v := range values {
		if v.K == k {
			return v.Value
		}
	}

	t.Fatalf("recall@%d が見つからない: %+v", k, values)

	return 0
}

// equalStrings は並びが一致するかを見る。
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}

// equalInts は並びが一致するかを見る。
func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}
