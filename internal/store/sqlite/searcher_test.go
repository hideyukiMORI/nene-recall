package sqlite_test

import (
	"errors"
	"math"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// scoreTolerance はスコアの突き合わせに使う許容幅。
//
// index.Result のスコアは float32 で、合成は float64 で行う。丸めのぶんだけ
// ずれるので、等値ではなく幅で比べる。1e-6 は float32 の相対精度 (約 1.2e-7) の
// 10 倍で、計算の誤りは必ずこれより大きく出る。
const scoreTolerance = 1e-6

// TestWeightedSumMatchesTheFormula は、合成スコアが式どおりであることを見る。
//
// 🔑 手で計算できる3行だけを入れる。ベクトルは角度で、語彙は返ってきた生の
// 値で検算する。「順位が期待どおり」ではなく「値が式どおり」を見るのは、
// 順位だけを見ると係数の誤りが同順位のまま隠れるためである。
//
// 🔴 LexicalScore は正規化前の生の値である（postgres 側と同じ）。したがって
// 検算は「返ってきた生の値をクエリ内の最大値で割ってから合成する」形になる。
// この対応関係が崩れたら、レポートに出る値と順位を決めた値がずれている。
func TestWeightedSumMatchesTheFormula(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["問い"] = 0
	e.angles["alpha bravo"] = 0             // cos 0 = 1
	e.angles["alpha charlie"] = math.Pi / 3 // cos 60° = 0.5
	e.angles["delta echo"] = math.Pi / 2    // cos 90° = 0

	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	for i, content := range []string{"alpha bravo", "alpha charlie", "delta echo"} {
		putContent(t, ts, chunkSpec{
			orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: i, content: content,
		})
	}

	const alpha = 0.25

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "問い alpha", limit: 10, alpha: alpha,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("件数 = %d, want 3", len(results))
	}

	maxLexical := 0.0
	for _, r := range results {
		maxLexical = math.Max(maxLexical, float64(r.LexicalScore))
	}

	if maxLexical <= 0 {
		t.Fatalf("語彙スコアが1件も付いていない（FTS5 の経路が動いていない）")
	}

	for _, r := range results {
		want := alpha*float64(r.VectorScore) + (1-alpha)*(float64(r.LexicalScore)/maxLexical)
		if math.Abs(float64(r.Score)-want) > scoreTolerance {
			t.Errorf("chunk %d の score = %v, want %v（alpha*vector + (1-alpha)*正規化語彙）",
				r.Chunk.ID, r.Score, want)
		}
	}
}

// TestSearchIsOrderedByScoreThenID は、同点の並びが決定的であることを見る。
//
// 🔴 同点の順序が実行のたびに揺れると、alpha = 1.0 が純ベクトルと一致することの
// 検証が「たまたま一致した／しなかった」になり、合成の自己検証が成立しない。
func TestSearchIsOrderedByScoreThenID(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["問い"] = 0
	// 3件とも同じ角度・同じ本文にして、順位を分けるものを id だけにする。
	e.angles["同じ本文"] = 0

	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	ids := make([]int64, 0, 3)
	for i := range 3 {
		ids = append(ids, putContent(t, ts, chunkSpec{
			orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: i, content: "同じ本文",
		}))
	}

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for i, r := range results {
		if r.Chunk.ID != ids[i] {
			t.Fatalf("同点の並びが id の昇順でない: %d 番目 = %d, want %d", i, r.Chunk.ID, ids[i])
		}
	}
}

// TestSearchAppliesLimit は、Limit が件数を切ることを見る。
func TestSearchAppliesLimit(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	for i := range 5 {
		putContent(t, ts, chunkSpec{
			orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: i, content: "本文",
		})
	}

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "問い", limit: 2, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("件数 = %d, want 2", len(results))
	}
}

// TestSearchFiltersByDocumentAndSource は、絞り込みが効くことと、
// 空の絞り込みが「全件」を意味することを見る。
//
// 🔴 空配列を「一件も一致しない」と読む実装にすると、絞り込まないつもりの
// 検索が全滅する（encode.go の idFilterJSON を参照）。ここが両方の意味を縛る。
func TestSearchFiltersByDocumentAndSource(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 10, sourceID: 100, chunkIndex: 0, content: "文書10",
	})
	putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 20, sourceID: 200, chunkIndex: 0, content: "文書20",
	})

	cases := []struct {
		name        string
		documentIDs []int64
		sourceIDs   []int64
		want        int
	}{
		{name: "絞り込み無しは全件", documentIDs: nil, sourceIDs: nil, want: 2},
		{name: "空スライスも全件", documentIDs: []int64{}, sourceIDs: []int64{}, want: 2},
		{name: "document で絞る", documentIDs: []int64{10}, sourceIDs: nil, want: 1},
		{name: "source で絞る", documentIDs: nil, sourceIDs: []int64{200}, want: 1},
		{name: "両方で絞ると積になる", documentIDs: []int64{10}, sourceIDs: []int64{200}, want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results, err := ts.store.Search(t.Context(), newQuery(querySpec{
				orgID: orgA, text: "問い", limit: 10, alpha: 1,
				documentIDs: tc.documentIDs, sourceIDs: tc.sourceIDs,
			}))
			if err != nil {
				t.Fatalf("Search: %v", err)
			}

			if len(results) != tc.want {
				t.Errorf("件数 = %d, want %d", len(results), tc.want)
			}
		})
	}
}

// TestSearchVectorRejectsAnInvalidVector は、外から渡すベクトルにも
// 契約検査が掛かることを見る。
//
// 🔴 SearchVector は計測のための口だが、検査を緩めない。緩めると系統2 だけが
// 別の前提で動き、2系統の p95 を並べる意味が消える (CLAUDE.md 地雷10)。
//
// 期待する sentinel は postgres 側の同名テストと同じ errVectorInvalid である。
// index.ErrInvalidQuery ではない——あちらは「Query の内容が契約を満たさない」を
// 表し、ベクトルの契約違反とは別の失敗である（HTTP 層の写像も別）。
func TestSearchVectorRejectsAnInvalidVector(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

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
			if !errors.Is(err, sqlite.ErrVectorInvalid()) {
				t.Errorf("err = %v, want errVectorInvalid", err)
			}
		})
	}
}

// TestSearchVectorMatchesSearch は、2系統が同じ順位・同じスコアを返すことを見る。
//
// 🔑 これが「p95 の差は埋め込み往復のぶんだけ」であることの根拠になる。
// 返るものが違えば、測っているものも違う。
func TestSearchVectorMatchesSearch(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["問い alpha"] = 0
	e.angles["alpha bravo"] = 0
	e.angles["charlie delta"] = math.Pi / 3

	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "alpha bravo",
	})
	putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 1, content: "charlie delta",
	})

	q := newQuery(querySpec{
		orgID: orgA, text: "問い alpha", limit: 10, alpha: 0.5,
		documentIDs: nil, sourceIDs: nil,
	})

	viaSearch, err := ts.store.Search(t.Context(), q)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	viaVector, err := ts.store.SearchVector(t.Context(), q, planeVector(0))
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}

	if len(viaSearch) != len(viaVector) {
		t.Fatalf("件数が違う: Search %d, SearchVector %d", len(viaSearch), len(viaVector))
	}

	for i := range viaSearch {
		if viaSearch[i].Chunk.ID != viaVector[i].Chunk.ID {
			t.Errorf("%d 番目の id が違う: %d と %d", i, viaSearch[i].Chunk.ID, viaVector[i].Chunk.ID)
		}

		if math.Abs(float64(viaSearch[i].Score-viaVector[i].Score)) > scoreTolerance {
			t.Errorf("%d 番目の score が違う: %v と %v", i, viaSearch[i].Score, viaVector[i].Score)
		}
	}
}

// TestSearchWithoutLexicalTokensDegradesToVector は、分割できる語が無いクエリが
// エラーにならず、合成が alpha*vector に縮退することを見る。
//
// 🔴 空の MATCH 式は SQLite の構文エラーになる（postgres の to_tsquery が
// 空を許すのとは違う）。ストア側が明示的に分岐していないと、絵文字だけのクエリで
// 検索そのものが失敗する。
func TestSearchWithoutLexicalTokensDegradesToVector(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["🔴🔑"] = 0
	e.angles["本文"] = 0

	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	putOne(t, ts, orgA, "本文")

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "🔴🔑", limit: 10, alpha: 0.5,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("件数 = %d, want 1", len(results))
	}

	if got := results[0].LexicalScore; got != 0 {
		t.Errorf("語彙スコア = %v, want 0", got)
	}

	// alpha*vector に縮退する。vector は cos 0 = 1。
	if want := float32(0.5); math.Abs(float64(results[0].Score-want)) > scoreTolerance {
		t.Errorf("score = %v, want %v（alpha*vector への縮退）", results[0].Score, want)
	}
}

// TestSearchRejectsCorruptedEmbedding は、壊れた BLOB を読んだときに
// 順位を返さず失敗することを見る。
//
// 🔴 DDL の CHECK は Go を通した書き込みしか守らない。ここでは制約を外して
// から短い BLOB を直接入れ、読み取り側の検査が働くことを確かめる
// （QLT-006 が求める「意図的な違反で検査が発火することの証明」）。
func TestSearchRejectsCorruptedEmbedding(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	putOne(t, ts, orgA, "正しい本文")

	// CHECK を迂回して壊す。UPDATE でも CHECK は効くので、PRAGMA で一時的に
	// 検査を止める。ファイルはテストごとの一時ファイルなので影響は残らない。
	if _, err := ts.db.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("CHECK を止められない: %v", err)
	}

	if _, err := ts.db.ExecContext(t.Context(),
		`UPDATE chunks SET embedding = x'0102'`); err != nil {
		t.Fatalf("BLOB を壊せない: %v", err)
	}

	_, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	}))
	if !errors.Is(err, sqlite.ErrVectorBlobLength()) {
		t.Errorf("🔴 err = %v, want ErrVectorBlobLength（壊れた行が黙って順位に入っている）", err)
	}
}

// TestSearchRejectsInvalidQuery は、DB に触れずに分かる誤りを弾くことを見る。
func TestSearchRejectsInvalidQuery(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	cases := []struct {
		name string
		spec querySpec
	}{
		{
			name: "limit が 0",
			spec: querySpec{orgID: orgA, text: "問い", limit: 0, alpha: 1, documentIDs: nil, sourceIDs: nil},
		},
		{
			name: "text が空",
			spec: querySpec{orgID: orgA, text: "", limit: 10, alpha: 1, documentIDs: nil, sourceIDs: nil},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ts.store.Search(t.Context(), newQuery(tc.spec))
			if !errors.Is(err, index.ErrInvalidQuery) {
				t.Errorf("err = %v, want index.ErrInvalidQuery", err)
			}
		})
	}
}

// TestRankingSettingsRecordsTheBackend は、レポートに載る条件が
// ストアを見分けられる形であることを見る。
//
// 🔴 2つのストアのレポートが条件表で見分けられない状態を作らない
// (ADR 0017 Decision 6)。ここが空文字や postgres と同じ値になったら、
// 比較レポートは「どちらで測ったか」を失う。
func TestRankingSettingsRecordsTheBackend(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	settings := ts.store.RankingSettings()

	if settings.Store != "sqlite" {
		t.Errorf("Store = %q, want %q", settings.Store, "sqlite")
	}

	if settings.LexicalScorer != "fts5-bm25" {
		t.Errorf("LexicalScorer = %q, want %q", settings.LexicalScorer, "fts5-bm25")
	}

	if settings.Fusion != "weighted-sum" {
		t.Errorf("Fusion = %q, want %q", settings.Fusion, "weighted-sum")
	}
}

// TestPingAndMigrateAreIdempotent は、起動のたびに Migrate を呼んでよいことを見る。
func TestPingAndMigrateAreIdempotent(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	if err := ts.store.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if err := ts.store.Migrate(t.Context()); err != nil {
		t.Errorf("2回目の Migrate: %v", err)
	}
}

// TestNewRejectsBrokenDependencies は、起動時に落ちるべき構成を見る。
//
// 🔴 「起動はするが取り込みが全部落ちる」構成を作らない。設定の誤りは
// 設定を読んだ直後に落とす。
func TestNewRejectsBrokenDependencies(t *testing.T) {
	db, err := sqlite.Open(t.Context(), t.TempDir()+"/recall.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	for _, tc := range brokenDependencyCases() {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sqlite.New(db, tc.embedder, tc.tokenizer); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// brokenDependencyCase は New が拒否すべき依存の組み合わせ1件。
type brokenDependencyCase struct {
	name      string
	embedder  embed.Embedder
	tokenizer lexical.Tokenizer
	want      error
}

// brokenDependencyCases は拒否すべき組み合わせを返す。
func brokenDependencyCases() []brokenDependencyCase {
	shortDims := newFakeEmbedder("fake:16")
	shortDims.dims = 16

	return []brokenDependencyCase{
		{
			name:      "次元が合わない Embedder",
			embedder:  shortDims,
			tokenizer: newFakeTokenizer("t:1"),
			want:      sqlite.ErrEmbedderDimensions(),
		},
		{
			name:      "識別子が空の Embedder",
			embedder:  newFakeEmbedder(""),
			tokenizer: newFakeTokenizer("t:1"),
			want:      sqlite.ErrEmbedderID(),
		},
		{
			name:      "Tokenizer が nil",
			embedder:  newFakeEmbedder("fake:1024"),
			tokenizer: nil,
			want:      sqlite.ErrTokenizerID(),
		},
		{
			name:      "識別子が空の Tokenizer",
			embedder:  newFakeEmbedder("fake:1024"),
			tokenizer: newFakeTokenizer(""),
			want:      sqlite.ErrTokenizerID(),
		},
	}
}
