package postgres_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// TestSearchVectorMatchesSearch は、同じベクトルなら Search と同じ順位・同じ
// スコアが返ることを見る。
//
// 🔴 これが計測の前提そのものである。ADR 0009 は p95 を「埋め込み往復を含む／
// 除く の両方」で測れと要求するが、2系統が別のものを返していたら、並べた
// 数字に意味が無い。SQL を共有していることをここで実際に確かめる
// (docs/adr/0013-evaluation-harness-design.md)。
func TestSearchVectorMatchesSearch(t *testing.T) {
	ts := rankedStore(t)

	q := newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "問い", limit: 10, alpha: 0.5,
		documentIDs: nil, sourceIDs: nil,
	})

	viaSearch, err := ts.store.Search(t.Context(), q)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// 偽 Embedder が "問い" に割り当てている角度は 0。Search が内部で作るのと
	// 同じベクトルを、外から渡す。
	viaVector, err := ts.store.SearchVector(t.Context(), q, planeVector(0))
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}

	if len(viaSearch) != len(viaVector) {
		t.Fatalf("件数 = %d, want %d", len(viaVector), len(viaSearch))
	}

	for i := range viaSearch {
		if viaSearch[i].Chunk.ID != viaVector[i].Chunk.ID {
			t.Errorf("順位 %d の chunk id = %d, want %d",
				i, viaVector[i].Chunk.ID, viaSearch[i].Chunk.ID)
		}

		if viaSearch[i].VectorScore != viaVector[i].VectorScore {
			t.Errorf("順位 %d の VectorScore = %v, want %v",
				i, viaVector[i].VectorScore, viaSearch[i].VectorScore)
		}

		if viaSearch[i].Score != viaVector[i].Score {
			t.Errorf("順位 %d の Score = %v, want %v",
				i, viaVector[i].Score, viaSearch[i].Score)
		}
	}
}

// TestSearchVectorDoesNotCallTheEmbedder は、埋め込み往復が本当に除かれている
// ことを見る。
//
// 🔴 ここが緩むと「埋め込みを除いた p95」が実は埋め込みを含んだままになる。
// 症状は数字が少し大きいだけで、値そのものはもっともらしいので気づけない。
func TestSearchVectorDoesNotCallTheEmbedder(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["問い"] = 0
	e.angles["本文"] = 0
	ts := newTestStore(t, e)

	putOne(t, ts, mustOrgID(t, 1), "本文")

	*e.kinds = nil // 取り込みのぶんを捨てて、検索だけを見る

	_, err := ts.store.SearchVector(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	}), planeVector(0))
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}

	if got := *e.kinds; len(got) != 0 {
		t.Errorf("Embedder が %d 回呼ばれた: %v, want 0 回", len(got), got)
	}
}

// TestSearchVectorRejectsInvalidQueries は、Search と同じ事前検査が効くことを見る。
//
// 分離条件（org_id）を検査せずに済む経路を作らない。計測用の口だからといって
// 緩めると、ADR 0003 が塞いだ穴が別の入口から開く。
func TestSearchVectorRejectsInvalidQueries(t *testing.T) {
	ts := rankedStore(t)
	orgA := mustOrgID(t, 1)

	cases := map[string]querySpec{
		"limit が 0": {orgID: orgA, text: "問い", limit: 0, alpha: 1, documentIDs: nil, sourceIDs: nil},
		"text が空":   {orgID: orgA, text: "", limit: 10, alpha: 1, documentIDs: nil, sourceIDs: nil},
	}

	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ts.store.SearchVector(t.Context(), newQuery(spec), planeVector(0))
			if !errors.Is(err, index.ErrInvalidQuery) {
				t.Errorf("err = %v, want index.ErrInvalidQuery", err)
			}
		})
	}
}

// TestSearchVectorRejectsAnInvalidVector は、渡されたベクトルにも契約検査が
// 効くことを見る。
//
// 外から受け取る経路だからこそ緩めない。<#>（負の内積）は入力が正規化済みで
// あることに依存しており、破ると順位が静かに狂う。
func TestSearchVectorRejectsAnInvalidVector(t *testing.T) {
	ts := rankedStore(t)

	q := newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	})

	cases := map[string][]float32{
		"次元が足りない":   {1, 0, 0},
		"正規化されていない": scaleVector(planeVector(0), 2),
		"空":         {},
	}

	for name, vector := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ts.store.SearchVector(t.Context(), q, vector)
			if !errors.Is(err, postgres.ErrVectorInvalid()) {
				t.Errorf("err = %v, want errVectorInvalid", err)
			}
		})
	}
}

// TestSearchVectorRejectsMismatchedEmbedder は、モデル一致の検査が
// SearchVector にも効くことを見る。
//
// 🔴 省略したくなる場所である（呼び出し側がベクトルを持っているのだから
// 検査済みだろう、という理屈が立つ）。省くと系統2 だけ SELECT が1本減り、
// 2系統の差が「埋め込み往復ぶん」でなくなる。計測の正しさが検査の有無に
// 依存しているので、ここで固定する。
func TestSearchVectorRejectsMismatchedEmbedder(t *testing.T) {
	rankedStore(t)

	other := attachStore(t, newFakeEmbedder("fake-b:1024"))

	_, err := other.SearchVector(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	}), planeVector(0))
	if !errors.Is(err, index.ErrEmbedderMismatch) {
		t.Fatalf("err = %v, want index.ErrEmbedderMismatch", err)
	}
}
