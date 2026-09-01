package postgres_test

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// 🔴 本ファイルの要点は「2つの融合方式に同じ不変条件を課す」ことである。
//
// 方式ごとに別々のテストを書くと、片方にしか無い性質（0 除算・空トークン・
// org 分離）が生まれても気づけない。どちらが良いかは測って決めるので、
// 測る前の時点で両方が同じ正しさを満たしていなければ比較が成立しない。

// allFusions は測定の対象になる融合方式をすべて返す。
//
// 🔴 方式を足したらここにも足すこと。ここに足し忘れると、新しい方式だけが
// 不変条件の検査を免れる。exhaustive linter は switch を見るが、テストの
// 表までは見てくれない。
func allFusions() []postgres.Fusion {
	return []postgres.Fusion{postgres.FusionWeightedSum, postgres.FusionRRF}
}

// fusedStore は融合方式を指定したストアを返す。
func fusedStore(t *testing.T, e *fakeEmbedder, fusion postgres.Fusion) *testStore {
	t.Helper()

	return newTestStoreWith(t, storeSpec{
		embedder:  e,
		tokenizer: newFakeTokenizer("fake-tokenizer:1"),
		fusion:    fusion,
	})
}

// TestFusionRoundTripsThroughItsName は名前と値が往復することを確かめる。
//
// 🔑 レポートに記録されるのもコマンドラインで指定されるのもこの文字列である。
// 表記が食い違うと、「どの条件で測ったか」の記録と指定がずれる。
func TestFusionRoundTripsThroughItsName(t *testing.T) {
	t.Parallel()

	for _, want := range allFusions() {
		got, err := postgres.ParseFusion(want.String())
		if err != nil {
			t.Fatalf("ParseFusion(%q): %v", want.String(), err)
		}

		if got != want {
			t.Errorf("ParseFusion(%q) = %v, want %v", want.String(), got, want)
		}

		if !slices.Contains(postgres.FusionNames(), want.String()) {
			t.Errorf("FusionNames() に %q が無い", want.String())
		}
	}
}

// TestParseFusionRejectsUnknownName は未知の指定を既定へ倒さないことを確かめる。
//
// 🔴 綴り誤りを黙って既定にすると、「既定で測った」結果が別の条件のものとして
// 記録される。計測の条件は必ず明示的に決まること。
func TestParseFusionRejectsUnknownName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "weighted_sum", "RRF", "rrf ", "既定"} {
		if _, err := postgres.ParseFusion(name); err == nil {
			t.Errorf("ParseFusion(%q) が通った", name)
		}
	}
}

// TestZeroFusionIsWeightedSum はゼロ値が現状維持であることを固定する。
//
// 既定を変えるのは実測を見て ADR を書いてからである。
func TestZeroFusionIsWeightedSum(t *testing.T) {
	t.Parallel()

	var zero postgres.Fusion
	if zero != postgres.FusionWeightedSum {
		t.Errorf("ゼロ値 = %v, want %v", zero, postgres.FusionWeightedSum)
	}
}

// TestEveryFusionSurvivesAllLexicalZero は 0 除算が起きないことを確かめる。
//
// 🔴 方式A は語彙スコアをクエリ内の最大値で割る。全行の語彙が 0 のとき、
// 分母が 0 になる。SQL の NULLIF がここを守っているが、守れているかどうかは
// 例外ではなく「結果が返るか」でしか分からない——PostgreSQL の 0 除算は
// エラーになるので、守れていなければ検索そのものが失敗する。
//
// 全行の語彙が 0 になるのは異常な状況ではない。クエリの語がコーパスに1つも
// 無いときに普通に起きる。
func TestEveryFusionSurvivesAllLexicalZero(t *testing.T) {
	for _, fusion := range allFusions() {
		t.Run(fusion.String(), func(t *testing.T) {
			ts := fusedStore(t, newFakeEmbedder("fake:1024"), fusion)
			orgA := mustOrgID(t, 1)

			putOne(t, ts, orgA, "本文に含まれる語")

			results, err := ts.store.Search(t.Context(), newQuery(querySpec{
				orgID: orgA, text: "どの本文にも無い語", limit: 10, alpha: 0.7,
				documentIDs: nil, sourceIDs: nil,
			}))
			if err != nil {
				t.Fatalf("Search: %v", err)
			}

			if len(results) != 1 {
				t.Fatalf("件数 = %d, want 1", len(results))
			}

			if results[0].LexicalScore != 0 {
				t.Errorf("lexical_score = %v, want 0", results[0].LexicalScore)
			}
		})
	}
}

// TestEveryFusionSurvivesEmptyTokens は分割できる語が無いクエリを確かめる。
//
// 🔴 エラーにしない。絵文字だけのクエリは異常な入力ではなく、ベクトル側は
// 普通に答えられる。方式B では語彙の順位が全行同じになるだけで、順位は
// ベクトル側だけで決まる。
func TestEveryFusionSurvivesEmptyTokens(t *testing.T) {
	for _, fusion := range allFusions() {
		t.Run(fusion.String(), func(t *testing.T) {
			ts := fusedStore(t, newFakeEmbedder("fake:1024"), fusion)
			orgA := mustOrgID(t, 1)

			putOne(t, ts, orgA, "本文")

			results, err := ts.store.Search(t.Context(), newQuery(querySpec{
				orgID: orgA, text: "🔴🔑", limit: 10, alpha: 0.7,
				documentIDs: nil, sourceIDs: nil,
			}))
			if err != nil {
				t.Fatalf("Search: %v", err)
			}

			if len(results) != 1 || results[0].LexicalScore != 0 {
				t.Errorf("結果 = %+v", results)
			}
		})
	}
}

// TestEveryFusionKeepsOrgsSeparate は語彙経路の分離が方式に依存しないことを確かめる。
//
// 🔴 分離条件は候補集合の定義（candidatesCTE）に1箇所だけ書いてあるが、
// 「1箇所に書いた」ことと「両方の方式で効いている」ことは別の主張である。
// 語彙が完全一致する餌を別 org に置いて、どちらの方式でも漏れないことを見る。
func TestEveryFusionKeepsOrgsSeparate(t *testing.T) {
	for _, fusion := range allFusions() {
		t.Run(fusion.String(), func(t *testing.T) {
			assertNoLexicalLeakAcrossOrgs(t, fusion)
		})
	}
}

// assertNoLexicalLeakAcrossOrgs は、語彙が完全一致する餌を別 org に置いて
// 漏れないことを見る。
func assertNoLexicalLeakAcrossOrgs(t *testing.T, fusion postgres.Fusion) {
	t.Helper()

	const bait = "recall_store lexical bait"

	ts := fusedStore(t, newFakeEmbedder("fake:1024"), fusion)
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	putOne(t, ts, orgB, bait)
	putOne(t, ts, orgA, "unrelated content")

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: bait, limit: 10, alpha: 0,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for _, r := range results {
		if r.Chunk.Content == bait {
			t.Fatalf("別 org の本文が語彙一致で漏れた: %+v", r.Chunk)
		}

		if r.Chunk.OrgID != orgA {
			t.Fatalf("org %s の結果に org %s が混ざった", orgA, r.Chunk.OrgID)
		}
	}
}

// TestEveryFusionBreaksTiesByID は同点の並びがどちらの方式でも安定であることを見る。
//
// 🔴 RRF は RANK() が同順位をまとめるので、同点はむしろ普通に起きる。
// 副キーが無いと、同じ条件で測り直すたびに順位が入れ替わる。
func TestEveryFusionBreaksTiesByID(t *testing.T) {
	for _, fusion := range allFusions() {
		t.Run(fusion.String(), func(t *testing.T) {
			assertTieOrderIsStable(t, fusion)
		})
	}
}

// assertTieOrderIsStable は、完全な同点を繰り返し検索して並びが変わらないことを見る。
func assertTieOrderIsStable(t *testing.T, fusion postgres.Fusion) {
	t.Helper()

	ts := fusedStore(t, newFakeEmbedder("fake:1024"), fusion)
	orgA := mustOrgID(t, 1)
	ids := putIdenticalChunks(t, ts, orgA, 4)

	for round := range 3 {
		results, err := ts.store.Search(t.Context(), newQuery(querySpec{
			orgID: orgA, text: "同点", limit: 10, alpha: 0.7,
			documentIDs: nil, sourceIDs: nil,
		}))
		if err != nil {
			t.Fatalf("Search: %v", err)
		}

		for i, r := range results {
			if r.Chunk.ID != ids[i] {
				t.Fatalf("round %d の順位 %d = id %d, want %d（同点の並びが揺れている）",
					round, i, r.Chunk.ID, ids[i])
			}
		}
	}
}

// putIdenticalChunks は本文もベクトルも同じチャンクを n 件入れる。
func putIdenticalChunks(t *testing.T, ts *testStore, orgID org.ID, n int) []int64 {
	t.Helper()

	chunks := make([]chunk.Chunk, 0, n)
	for i := range n {
		chunks = append(chunks, newChunk(chunkSpec{
			orgID: orgID, documentID: 1, sourceID: 10,
			chunkIndex: i, content: "同点",
		}))
	}

	ids, err := ts.store.Put(t.Context(), orgID, chunks)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	return ids
}

// TestRRFComposesReciprocalRanks は方式B の合成が定義どおりであることを見る。
//
// 🔑 このテストは、返ってきた結果から順位を組み直して検算する。全行が返る
// 大きさのストアなので、候補集合の順位と返った順位が一致する。
// ⚠️ 全行が返らない大きさにすると、この検算は成り立たない（RRF の順位は
// 候補集合全体に対するものだからである）。
func TestRRFComposesReciprocalRanks(t *testing.T) {
	ts := fusedStore(t, hybridEmbedder(), postgres.FusionRRF)
	orgA := mustOrgID(t, 1)
	putHybridChunks(t, ts, orgA)

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "recall_store", limit: 10, alpha: 0.7,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	vectorRanks := ranksByScore(results, func(r index.Result) float32 { return r.VectorScore })
	lexicalRanks := ranksByScore(results, func(r index.Result) float32 { return r.LexicalScore })

	for _, r := range results {
		want := 1/float64(postgres.RRFK+vectorRanks[r.Chunk.ID]) +
			1/float64(postgres.RRFK+lexicalRanks[r.Chunk.ID])
		if math.Abs(float64(r.Score)-want) > scoreTolerance {
			t.Errorf("id %d の score = %v, want %v (1/(%d+%d) + 1/(%d+%d))",
				r.Chunk.ID, r.Score, want,
				postgres.RRFK, vectorRanks[r.Chunk.ID],
				postgres.RRFK, lexicalRanks[r.Chunk.ID])
		}
	}
}

// ranksByScore は RANK() と同じ規則（同点は同順位、次は飛ぶ）で順位を振る。
func ranksByScore(results []index.Result, score func(index.Result) float32) map[int64]int {
	sorted := slices.Clone(results)
	slices.SortFunc(sorted, func(a, b index.Result) int {
		switch {
		case score(a) > score(b):
			return -1
		case score(a) < score(b):
			return 1
		default:
			return 0
		}
	})

	ranks := make(map[int64]int, len(sorted))

	for i, r := range sorted {
		rank := i + 1
		if i > 0 && score(sorted[i-1]) == score(r) {
			rank = ranks[sorted[i-1].Chunk.ID]
		}

		ranks[r.Chunk.ID] = rank
	}

	return ranks
}

// TestRRFIgnoresAlpha は alpha が方式B の順位に影響しないことを確かめる。
//
// ⚠️ これは「alpha が壊れている」テストではなく、方式B の性質の宣言である。
// 順位融合には重みを表す場所が無い。契約（要件定義 F-4・OpenAPI）は加重和の
// ままなので、この方式を既定にするなら ADR で契約ごと決め直す必要がある。
// その判断は実測を見てからである。
func TestRRFIgnoresAlpha(t *testing.T) {
	ts := fusedStore(t, hybridEmbedder(), postgres.FusionRRF)
	orgA := mustOrgID(t, 1)
	putHybridChunks(t, ts, orgA)

	var previous []int64

	for _, alpha := range []float32{0, 0.5, 1} {
		results, err := ts.store.Search(t.Context(), newQuery(querySpec{
			orgID: orgA, text: "recall_store", limit: 10, alpha: alpha,
			documentIDs: nil, sourceIDs: nil,
		}))
		if err != nil {
			t.Fatalf("Search(alpha=%v): %v", alpha, err)
		}

		ids := make([]int64, 0, len(results))
		for _, r := range results {
			ids = append(ids, r.Chunk.ID)
		}

		if previous != nil && !slices.Equal(ids, previous) {
			t.Errorf("alpha=%v で順位が変わった: %v -> %v", alpha, previous, ids)
		}

		previous = ids
	}
}

// TestRRFGivesLittleCreditWhenFewDocumentsMatch は、この RRF 実装の
// 「効きにくい場合」を明示的に固定する。
//
// 🔴 これは欠陥のテストではなく、条件の宣言である。古典的な RRF は各検索器の
// 上位 N 件のリストを融合するが、この実装は候補集合の全行に順位を振る。結果、
// 語彙が1件も当たらない行にも lexical_rank が付き、RANK() がそれらを同順位で
// まとめる——語彙で1位を取った行のすぐ次の順位に、当たらなかった行が全部並ぶ。
//
// ⇒ 語彙に当たる行がごく少ないとき、語彙側の寄与はほぼ打ち消し合い、
// 順位はベクトル側だけで決まる。逆に、当たる行が多くて語彙の順位が広く
// 散らばるときには効く。
//
// 🔑 コーディネーターがオフラインで見積もった RRF は「上位10件どうしの融合」で
// あり、この実装とは条件が違う。実測がその見積りを下回った場合、まずここを
// 疑うこと（方式の優劣ではなく、リストの取り方の違いかもしれない）。
func TestRRFGivesLittleCreditWhenFewDocumentsMatch(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["recall_store"] = 0
	e.angles["vector neighbour"] = 0
	e.angles["another neighbour"] = 0.1
	e.angles["recall_store lexical"] = 0.3

	ts := fusedStore(t, e, postgres.FusionRRF)
	orgA := mustOrgID(t, 1)

	contents := []string{"vector neighbour", "another neighbour", "recall_store lexical"}
	for i, content := range contents {
		putContent(t, ts, chunkSpec{
			orgID: orgA, documentID: 1, sourceID: 10,
			chunkIndex: i, content: content,
		})
	}

	got := searchContents(t, ts, querySpec{
		orgID: orgA, text: "recall_store", limit: 10, alpha: 0.7,
		documentIDs: nil, sourceIDs: nil,
	})

	// 実際の RRF スコア（k=60）:
	//   vector neighbour     v_rank 1, l_rank 2 -> 1/61 + 1/62 = 0.0325224
	//   recall_store lexical v_rank 3, l_rank 1 -> 1/63 + 1/61 = 0.0322664
	//   another neighbour    v_rank 2, l_rank 2 -> 1/62 + 1/62 = 0.0322581
	//
	// 🔴 語彙で1位を取った行が首位に上がらない。買えたのは1つ順位を上げること
	// だけで、しかも2位との差は 0.0000083 しかない。当たらなかった2件が
	// 語彙順位2位に同着で並び、ほぼ同じ加点を受けるためである。
	want := []string{"vector neighbour", "recall_store lexical", "another neighbour"}
	if !slices.Equal(got, want) {
		t.Errorf("順位 = %v, want %v", got, want)
	}
}

// TestFusionsProduceDifferentScoreScales は、方式の切り替えが実際に効いている
// ことを、同じデータに対する score の桁で確かめる。
//
// 加重和は [0,1] 付近、RRF は 2/(k+1) ≈ 0.033 付近にしかならない。順位で
// 比べると小さなデータでは差が出ないことがあるが、スケールは必ず違う。
func TestFusionsProduceDifferentScoreScales(t *testing.T) {
	e := hybridEmbedder()
	weighted := fusedStore(t, e, postgres.FusionWeightedSum)
	orgA := mustOrgID(t, 1)
	putHybridChunks(t, weighted, orgA)

	rrf := attachStoreWith(t, storeSpec{
		embedder:  e,
		tokenizer: newFakeTokenizer("fake-tokenizer:1"),
		fusion:    postgres.FusionRRF,
	})

	query := newQuery(querySpec{
		orgID: orgA, text: "recall_store", limit: 10, alpha: 0.7,
		documentIDs: nil, sourceIDs: nil,
	})

	weightedResults, weightedErr := weighted.store.Search(t.Context(), query)
	weightedTop := topScore(t, weightedResults, weightedErr)

	rrfResults, rrfErr := rrf.Search(t.Context(), query)
	rrfTop := topScore(t, rrfResults, rrfErr)

	if weightedTop < 0.5 {
		t.Errorf("加重和の首位スコア = %v。加重和なら 1 に近いはず", weightedTop)
	}

	// RRF の最大値は 2/(k+1)。k=60 なら 0.0328。
	if rrfTop > float32(2)/float32(postgres.RRFK+1)+scoreTolerance {
		t.Errorf("RRF の首位スコア = %v。2/(k+1) を超えている", rrfTop)
	}
}

// topScore は検索結果の首位のスコアを取り出す。
func topScore(t *testing.T, results []index.Result, err error) float32 {
	t.Helper()

	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("結果が空である")
	}

	return results[0].Score
}

// TestSearchVectorUsesTheSameFusion は2系統が同じ方式を通ることを確かめる。
//
// 🔴 SearchVector は計測のための口だが、SQL も融合も Search と共有している。
// 片方だけ別の方式になると、評価ハーネスの ErrRankingDiverged が発火するか、
// 発火しないまま2系統が別物を測ることになる。
func TestSearchVectorUsesTheSameFusion(t *testing.T) {
	for _, fusion := range allFusions() {
		t.Run(fusion.String(), func(t *testing.T) {
			e := hybridEmbedder()
			ts := fusedStore(t, e, fusion)
			orgA := mustOrgID(t, 1)
			putHybridChunks(t, ts, orgA)

			// 🔴 Kind は KindQuery。Search が内部で使うのと同じ Kind でなければ、
			// 2系統が別のベクトルを測ることになる（ADR 0008）。
			vectors, err := e.Embed(t.Context(), []string{"recall_store"}, embed.KindQuery)
			if err != nil {
				t.Fatalf("Embed: %v", err)
			}

			query := newQuery(querySpec{
				orgID: orgA, text: "recall_store", limit: 10, alpha: 0.2,
				documentIDs: nil, sourceIDs: nil,
			})

			viaText, err := ts.store.Search(t.Context(), query)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}

			viaVector, err := ts.store.SearchVector(t.Context(), query, vectors[0])
			if err != nil {
				t.Fatalf("SearchVector: %v", err)
			}

			assertSameRanking(t, viaText, viaVector)
		})
	}
}

// assertSameRanking は2つの結果の順位とスコアが一致することを確かめる。
func assertSameRanking(t *testing.T, left, right []index.Result) {
	t.Helper()

	if len(left) != len(right) {
		t.Fatalf("件数が違う: %d / %d", len(left), len(right))
	}

	for i := range left {
		if left[i].Chunk.ID != right[i].Chunk.ID {
			t.Errorf("順位 %d が食い違う: %d / %d", i, left[i].Chunk.ID, right[i].Chunk.ID)
		}

		if left[i].Score != right[i].Score {
			t.Errorf("順位 %d のスコアが食い違う: %v / %v", i, left[i].Score, right[i].Score)
		}
	}
}

// TestStatementRejectsUnknownFusion は実行経路の番人が働くことを見る。
//
// New が構築時に弾くので通常はここに来ないが、Fusion は int なので範囲外の値を
// 作ること自体は言語仕様上いつでもできる (GO-003)。番人が無いと、未知の方式は
// 「SQL が空文字」という読めない失敗になる。
func TestStatementRejectsUnknownFusion(t *testing.T) {
	t.Parallel()

	if _, _, err := postgres.StatementFor(postgres.Fusion(99), 0.7); !errors.Is(
		err, postgres.ErrUnknownFusion()) {
		t.Fatalf("err = %v, want errUnknownFusion", err)
	}
}

// TestEachFusionHasItsOwnStatement は方式ごとに別の SQL が選ばれることを見る。
//
// 🔑 切り替えが効いていることを、DB を立てずに直接確かめる。ここが同じ SQL を
// 返していると、以後の測定は「方式を変えたつもりで同じものを測った」になる。
func TestEachFusionHasItsOwnStatement(t *testing.T) {
	t.Parallel()

	weighted, weightedArg, err := postgres.StatementFor(postgres.FusionWeightedSum, 0.7)
	if err != nil {
		t.Fatalf("StatementFor(weighted-sum): %v", err)
	}

	rrf, rrfArg, err := postgres.StatementFor(postgres.FusionRRF, 0.7)
	if err != nil {
		t.Fatalf("StatementFor(rrf): %v", err)
	}

	if weighted == rrf {
		t.Errorf("2つの方式が同じ SQL を返している")
	}

	// 🔴 $8 の中身も方式ごとに違う。加重和は alpha、RRF は k である。
	if weightedArg != float32(0.7) {
		t.Errorf("加重和の $8 = %v, want alpha 0.7", weightedArg)
	}

	if rrfArg != postgres.RRFK {
		t.Errorf("RRF の $8 = %v, want k %d", rrfArg, postgres.RRFK)
	}
}
