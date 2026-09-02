package postgres_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
)

// rankedStore は角度の違う3件を投入したストアを返す。
//
// 角度が小さいほど問い合わせベクトルとの内積が大きい。期待順位を角度で書ける。
func rankedStore(t *testing.T) *testStore {
	t.Helper()

	e := newFakeEmbedder("fake:1024")
	e.angles["問い"] = 0
	e.angles["近い"] = 0
	e.angles["中くらい"] = 0.5
	e.angles["遠い"] = 1.5

	ts := newTestStore(t, e)

	chunks := []chunk.Chunk{
		newChunk(chunkSpec{
			orgID: mustOrgID(t, 1), documentID: 1, sourceID: 10,
			chunkIndex: 0, content: "遠い",
		}),
		newChunk(chunkSpec{
			orgID: mustOrgID(t, 1), documentID: 2, sourceID: 20,
			chunkIndex: 0, content: "近い",
		}),
		newChunk(chunkSpec{
			orgID: mustOrgID(t, 1), documentID: 3, sourceID: 20,
			chunkIndex: 0, content: "中くらい",
		}),
	}

	if _, err := ts.store.Put(t.Context(), mustOrgID(t, 1), chunks); err != nil {
		t.Fatalf("Put: %v", err)
	}

	return ts
}

// searchContents は検索して本文だけを取り出す。
func searchContents(t *testing.T, ts *testStore, spec querySpec) []string {
	t.Helper()

	results, err := ts.store.Search(t.Context(), newQuery(spec))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Chunk.Content)
	}

	return out
}

// TestSearchOrdersByInnerProduct は内積の降順に並ぶことを見る。
//
// 投入順は「遠い・近い・中くらい」なので、挿入順のまま返る実装では通らない。
func TestSearchOrdersByInnerProduct(t *testing.T) {
	ts := rankedStore(t)

	got := searchContents(t, ts, querySpec{
		orgID: mustOrgID(t, 1), text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	})

	want := []string{"近い", "中くらい", "遠い"}
	if len(got) != len(want) {
		t.Fatalf("件数 = %d, want %d (%v)", len(got), len(want), got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("順位 %d = %q, want %q（全体 %v）", i, got[i], want[i], got)
		}
	}
}

// TestSearchAppliesFilters は絞り込みが効くことと、空が全滅を招かないことを見る。
//
// 🔴 空フィルタの扱いはここが要点である。id = ANY('{}') と素直に書くと
// 一件も一致せず、絞り込まないつもりが全滅する。
func TestSearchAppliesFilters(t *testing.T) {
	ts := rankedStore(t)
	orgA := mustOrgID(t, 1)

	cases := []struct {
		name string
		spec querySpec
		want int
	}{
		{
			name: "フィルタ無しは全件",
			spec: querySpec{orgID: orgA, text: "問い", limit: 10, alpha: 1, documentIDs: nil, sourceIDs: nil},
			want: 3,
		},
		{
			name: "空スライスでも全滅しない",
			spec: querySpec{orgID: orgA, text: "問い", limit: 10, alpha: 1, documentIDs: []int64{}, sourceIDs: []int64{}},
			want: 3,
		},
		{
			name: "document_ids で絞る",
			spec: querySpec{orgID: orgA, text: "問い", limit: 10, alpha: 1, documentIDs: []int64{2}, sourceIDs: nil},
			want: 1,
		},
		{
			name: "source_ids で絞る",
			spec: querySpec{orgID: orgA, text: "問い", limit: 10, alpha: 1, documentIDs: nil, sourceIDs: []int64{20}},
			want: 2,
		},
		{
			name: "一致しない id は 0 件",
			spec: querySpec{orgID: orgA, text: "問い", limit: 10, alpha: 1, documentIDs: []int64{999}, sourceIDs: nil},
			want: 0,
		},
		{
			name: "limit で打ち切る",
			spec: querySpec{orgID: orgA, text: "問い", limit: 2, alpha: 1, documentIDs: nil, sourceIDs: nil},
			want: 2,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := searchContents(t, ts, c.spec); len(got) != c.want {
				t.Errorf("件数 = %d, want %d (%v)", len(got), c.want, got)
			}
		})
	}
}

// TestSearchRejectsInvalidQueries は DB に触れずに分かる誤りが弾かれることを見る。
func TestSearchRejectsInvalidQueries(t *testing.T) {
	ts := rankedStore(t)
	orgA := mustOrgID(t, 1)

	cases := map[string]querySpec{
		"limit が 0": {orgID: orgA, text: "問い", limit: 0, alpha: 1, documentIDs: nil, sourceIDs: nil},
		"limit が負":  {orgID: orgA, text: "問い", limit: -1, alpha: 1, documentIDs: nil, sourceIDs: nil},
		"text が空":   {orgID: orgA, text: "", limit: 10, alpha: 1, documentIDs: nil, sourceIDs: nil},
	}

	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ts.store.Search(t.Context(), newQuery(spec))
			if !errors.Is(err, index.ErrInvalidQuery) {
				t.Errorf("err = %v, want index.ErrInvalidQuery", err)
			}
		})
	}
}

// TestSearchRejectsMismatchedEmbedder は別モデルでの検索を拒否することを見る。
//
// 🔴 「該当なし」で隠さないこと。不一致を WHERE で除外する実装にすると、
// 利用者からは「検索が当たらない」としか見えず、原因に辿り着けない。
func TestSearchRejectsMismatchedEmbedder(t *testing.T) {
	// 戻り値は使わない。要るのは「fake:1024 で書かれた行がある DB」という状態だけ。
	rankedStore(t)

	other := attachStore(t, newFakeEmbedder("fake-b:1024"))

	_, err := other.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	}))
	if !errors.Is(err, index.ErrEmbedderMismatch) {
		t.Fatalf("err = %v, want index.ErrEmbedderMismatch", err)
	}
}

// TestSearchUsesQueryKind は検索時の Kind を確かめる。
func TestSearchUsesQueryKind(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["問い"] = 0
	e.angles["本文"] = 0
	ts := newTestStore(t, e)

	putOne(t, ts, mustOrgID(t, 1), "本文")

	*e.kinds = nil // 取り込みのぶんを捨てて、検索だけを見る

	if _, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	})); err != nil {
		t.Fatalf("Search: %v", err)
	}

	got := *e.kinds
	if len(got) != 1 || got[0] != embed.KindQuery {
		t.Errorf("渡された Kind = %v, want [%s]", got, embed.KindQuery)
	}
}

// TestSearchRoundTripsOptionalColumns は NULL 可能列の往復を見る。
//
// page_number と section_label は NULL を取りうる。Null 型で受けてポインタに
// 戻す経路が壊れると、値があるのに nil になる（あるいは逆）。
func TestSearchRoundTripsOptionalColumns(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["問い"] = 0
	e.angles["本文"] = 0
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	page := 3
	label := "第2章"

	withMeta := newChunk(chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文",
	})
	withMeta.PageNumber = &page
	withMeta.SectionLabel = &label

	if _, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{withMeta}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	got := results[0].Chunk
	if got.PageNumber == nil || *got.PageNumber != page {
		t.Errorf("PageNumber = %v, want %d", got.PageNumber, page)
	}

	if got.SectionLabel == nil || *got.SectionLabel != label {
		t.Errorf("SectionLabel = %v, want %q", got.SectionLabel, label)
	}
}

// TestSearchScoresAreConsistent はスコアの分解が保たれることを見る。
//
// このコーパス（遠い / 近い / 中くらい）はクエリ「問い」と bigram を1つも
// 共有しないので LexicalScore は 0 になり、合成は alpha*vector に縮退する。
// 🔴 alpha の値そのものが適切かはここでは問わない（既定の根拠は ADR 0015）。
// ここで確かめているのは「合成式が分解と一致すること」だけである。
func TestSearchScoresAreConsistent(t *testing.T) {
	ts := rankedStore(t)

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "問い", limit: 1, alpha: 0.5,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	got := results[0]
	if got.LexicalScore != 0 {
		t.Errorf("LexicalScore = %v, want 0（語彙一致が無いコーパス）", got.LexicalScore)
	}

	if want := 0.5 * got.VectorScore; got.Score != want {
		t.Errorf("Score = %v, want %v (alpha*vector)", got.Score, want)
	}
}
