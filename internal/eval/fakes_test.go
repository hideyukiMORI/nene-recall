package eval_test

import (
	"context"
	"errors"
	"slices"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// errFake は偽物が意図的に失敗するときの理由。
//
// 動的 error を作らないという規約はテストにも適用する (GO-005)。
var errFake = errors.New("fake: injected failure")

// fakeIndex は index.Writer / index.Searcher と、計測用の SearchVector を
// 満たす最小の索引。
//
// 実 PostgreSQL にも実 Ollama にも依存させない。ここで確かめたいのは
// 計測ループの振る舞い（写像・ウォームアップ・ラウンド数・指標）であって
// 検索そのものではない。
//
// 🔑 採番 id は 100 から始める。1 始まりにすると、投入順の添字と id が
// たまたま一致してしまい、写像を無視した実装でもテストが通ってしまう。
type fakeIndex struct {
	nextID int64
	// ids は本文から採番 id を引く。テストは本文で順位を書ける。
	ids map[string]int64
	// chunks は採番 id から投入されたチャンクを引く。
	chunks map[int64]chunk.Chunk
	// ranking は検索語ごとに返す本文の並び。順位を明示的に決めるためのもの。
	ranking map[string][]string
	// searchCalls は Search が呼ばれた回数。ウォームアップの検証に使う。
	searchCalls int
	// vectorCalls は SearchVector が呼ばれた回数。
	vectorCalls int
	// putErr が非 nil なら Put が失敗する。
	putErr error
	// searchErr が非 nil なら Search が失敗する。
	searchErr error
	// divergent が真なら SearchVector が Search と違う順位を返す。
	divergent bool
	// foreignID が非 0 なら、その id を持つ結果を先頭に混ぜる。
	// 評価コーパス以外の行が DB に居る状況を作るためのもの。
	foreignID int64
	// extraIDs が真なら Put が入力より多い id を返す（契約違反の再現）。
	extraIDs bool
}

// newFakeIndex は空の偽索引を作る。
func newFakeIndex() *fakeIndex {
	return &fakeIndex{
		nextID:      100,
		ids:         map[string]int64{},
		chunks:      map[int64]chunk.Chunk{},
		ranking:     map[string][]string{},
		searchCalls: 0,
		vectorCalls: 0,
		putErr:      nil,
		searchErr:   nil,
		divergent:   false,
		foreignID:   0,
		extraIDs:    false,
	}
}

// Put は採番 id を入力と同じ順で返す。
func (f *fakeIndex) Put(_ context.Context, _ org.ID, chunks []chunk.Chunk) ([]int64, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}

	ids := make([]int64, 0, len(chunks))

	for _, c := range chunks {
		f.nextID++
		// 採番した id をチャンクに書き戻す。実ストアは RETURNING id で
		// 採番した値を返し、検索結果の chunk_id はその値になる。
		c.ID = f.nextID
		f.ids[c.Content] = f.nextID
		f.chunks[f.nextID] = c
		ids = append(ids, f.nextID)
	}

	if f.extraIDs {
		ids = append(ids, f.nextID+1)
	}

	return ids, nil
}

// Delete は index.Writer を満たすためだけにある。
func (f *fakeIndex) Delete(_ context.Context, _ org.ID, _ int64) error { return nil }

// DeleteBySource は index.Writer を満たすためだけにある。
func (f *fakeIndex) DeleteBySource(_ context.Context, _ org.ID, _ int64) (int, error) {
	return 0, nil
}

// Search は ranking に書いた並びで返す。
func (f *fakeIndex) Search(_ context.Context, q index.Query) ([]index.Result, error) {
	f.searchCalls++

	if f.searchErr != nil {
		return nil, f.searchErr
	}

	return f.results(q), nil
}

// SearchVector は Search と同じ並びで返す。divergent のときだけ逆順にする。
func (f *fakeIndex) SearchVector(_ context.Context, q index.Query, _ []float32) ([]index.Result, error) {
	f.vectorCalls++

	if f.searchErr != nil {
		return nil, f.searchErr
	}

	out := f.results(q)
	if f.divergent {
		slices.Reverse(out)
	}

	return out, nil
}

// results は検索語に対応する並びを結果に組み立てる。
func (f *fakeIndex) results(q index.Query) []index.Result {
	contents := f.ranking[q.Text]
	out := []index.Result{}

	if f.foreignID != 0 {
		out = append(out, result(chunk.Chunk{
			ID: f.foreignID, OrgID: q.OrgID, DocumentID: 0, SourceID: 0,
			ChunkIndex: 0, Content: "混入", PageNumber: nil, SectionLabel: nil,
		}, q.Alpha))
	}

	for _, content := range contents {
		if len(out) >= q.Limit {
			break
		}

		if id, known := f.ids[content]; known {
			out = append(out, result(f.chunks[id], q.Alpha))
		}
	}

	return out
}

// result は1件ぶんの検索結果を作る。スコアは順位の検証に使わないので固定値でよい。
func result(c chunk.Chunk, alpha float32) index.Result {
	return index.Result{
		Chunk:        c,
		Score:        alpha,
		VectorScore:  1,
		LexicalScore: 0,
	}
}

// fakeEmbedder は決まったベクトルを返し、呼ばれた回数を数える。
//
// 🔑 回数を数えるのが要点である。系統2（埋め込み往復を除く）の計測が
// 正しいかどうかは、「埋め込みが計測ループの外で1回だけ呼ばれたか」で決まる。
type fakeEmbedder struct {
	calls int
	err   error
}

// embed は eval.EmbedQuery として渡す関数。
func (f *fakeEmbedder) embed(_ context.Context, _ string) ([]float32, error) {
	f.calls++

	if f.err != nil {
		return nil, f.err
	}

	return []float32{1, 0}, nil
}
