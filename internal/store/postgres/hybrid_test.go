package postgres_test

import (
	"math"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// scoreTolerance は SQL(float8) と Go(float32) の丸め差の許容幅。
//
// 合成は SQL 側で計算して返すので、Go 側で検算すると必ずこの程度ずれる。
// ずれ自体は正しく、等号で比べられないことのほうが事実である。
const scoreTolerance = 1e-6

// hybridStore は「ベクトルは遠いが語彙は当たる」件を含むストアを返す。
//
// 🔑 ベクトルの角度と語彙の一致を意図的に逆向きにしてある。合成が本当に
// 効いているなら、alpha を動かしたときに順位が入れ替わる。同じ向きに揃えると
// どちらのスコアが効いたのか区別がつかず、テストが何も言わなくなる。
func hybridStore(t *testing.T) *testStore {
	t.Helper()

	ts := newTestStore(t, hybridEmbedder())
	putHybridChunks(t, ts, mustOrgID(t, 1))

	return ts
}

// hybridEmbedder はベクトルの近さと語彙の一致を逆向きにした偽の埋め込みを返す。
//
// 🔑 逆向きにしてあるのが要点である。同じ向きに揃えるとどちらのスコアが効いたのか
// 区別がつかず、合成のテストが何も言わなくなる。
func hybridEmbedder() *fakeEmbedder {
	e := newFakeEmbedder("fake:1024")
	e.angles["recall_store"] = 0
	// ベクトル的に近いが、語彙は1つも重ならない。
	e.angles["vector neighbour"] = 0
	// ベクトル的に遠いが、語彙は完全に重なる。
	e.angles["recall_store lexical"] = 1.5

	return e
}

// putHybridChunks は hybridEmbedder と対になる2件を投入する。
func putHybridChunks(t *testing.T, ts *testStore, orgID org.ID) {
	t.Helper()

	chunks := []chunk.Chunk{
		newChunk(chunkSpec{
			orgID: orgID, documentID: 1, sourceID: 10,
			chunkIndex: 0, content: "vector neighbour",
		}),
		newChunk(chunkSpec{
			orgID: orgID, documentID: 2, sourceID: 20,
			chunkIndex: 0, content: "recall_store lexical",
		}),
	}

	if _, err := ts.store.Put(t.Context(), orgID, chunks); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// TestSearchBlendsVectorAndLexical は alpha で順位が入れ替わることを見る。
//
// alpha = 1.0 は純ベクトルなので「ベクトルが近い側」が首位、alpha = 0.0 は
// 純語彙なので「語彙が当たる側」が首位になる。両端で入れ替わらないなら、
// 合成は効いていない。
func TestSearchBlendsVectorAndLexical(t *testing.T) {
	ts := hybridStore(t)

	cases := []struct {
		name  string
		alpha float32
		want  string
	}{
		{name: "純ベクトル", alpha: 1, want: "vector neighbour"},
		{name: "純語彙", alpha: 0, want: "recall_store lexical"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := searchContents(t, ts, querySpec{
				orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: tc.alpha,
				documentIDs: nil, sourceIDs: nil,
			})

			if len(got) == 0 {
				t.Fatalf("結果が空である")
			}

			if got[0] != tc.want {
				t.Errorf("alpha=%v の首位 = %q, want %q（全体 %v）", tc.alpha, got[0], tc.want, got)
			}
		})
	}
}

// TestSearchReportsBothScores は両方のスコアが返り、合成が定義どおりであることを見る。
//
// 🔑 vector_score と lexical_score を分けて返すのは、外したときにどちら側が
// 原因かを切り分けるためである（要件定義 §3）。合成後の値だけでは alpha の
// 調整が当てずっぽうになる。
//
// ⚠️ lexical_score の値域は縛らない。ts_rank の正規化フラグを 0 にした
// （2026-09-02 の実測で長さ正規化が有害だったため）ので、上限は無い。
// 🔑 有界かどうかは合成の正しさと無関係である——2026-09-02 の測定は、
// [0,1) に押し込んでもスケールが3桁違えば加重和は機能しないことを示した
// （lexical 中央値 0.00016 に対し vector 中央値 0.44）。
// スケールを揃えるのは合成側の仕事であって、値域の上限の仕事ではない。
func TestSearchReportsBothScores(t *testing.T) {
	ts := hybridStore(t)

	const alpha = 0.7

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: alpha,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// 🔴 方式A の合成は「クエリ内の最大値で割った語彙スコア」を使う。最大値は
	// 候補集合全体（org 内の全行）に対するもので、返ってきた上位 limit 件からは
	// 一般には求められない。このテストは全行が返る大きさのストアを使っている
	// ので、返った中の最大値が候補集合の最大値と一致する。
	// ⚠️ この前提を崩す（limit より多い行を入れる）と、この検算は成り立たない。
	maxLexical := maxLexicalScore(t, results)

	var lexicalSeen bool

	for _, r := range results {
		if r.LexicalScore < 0 {
			t.Errorf("lexical_score = %v が負である", r.LexicalScore)
		}

		if r.LexicalScore > 0 {
			lexicalSeen = true
		}

		want := alpha*r.VectorScore + (1-alpha)*normalizedLexical(r.LexicalScore, maxLexical)
		if math.Abs(float64(r.Score-want)) > scoreTolerance {
			t.Errorf("score = %v, want %v (= %v*%v + %v*%v/%v)",
				r.Score, want, alpha, r.VectorScore, 1-alpha, r.LexicalScore, maxLexical)
		}
	}

	if !lexicalSeen {
		t.Errorf("語彙スコアが全件 0 である。語彙経路が働いていない")
	}
}

// maxLexicalScore は返ってきた結果の語彙スコアの最大値を返す。
//
// 結果が空、または全件 0 のときは 0 を返す（呼び出し側が 0 除算を避ける）。
func maxLexicalScore(t *testing.T, results []index.Result) float32 {
	t.Helper()

	var highest float32
	for _, r := range results {
		if r.LexicalScore > highest {
			highest = r.LexicalScore
		}
	}

	return highest
}

// normalizedLexical は語彙スコアをクエリ内の最大値で割る。
//
// 最大値が 0 なら 0 を返す。SQL 側の COALESCE(... / NULLIF(max, 0), 0) と
// 同じ規則で、テストが SQL の規則を写していることが読めるように分けてある。
func normalizedLexical(score, highest float32) float32 {
	if highest == 0 {
		return 0
	}

	return score / highest
}

// TestSearchWithoutTokensFallsBackToVector は分割できる語が無いクエリを見る。
//
// 🔴 エラーにしない。絵文字だけのクエリは異常な入力ではなく、ベクトル側は
// 普通に答えられる。語彙スコアが 0 に落ちて合成が alpha*vector に縮退する、
// が正しい振る舞いである。
func TestSearchWithoutTokensFallsBackToVector(t *testing.T) {
	ts := hybridStore(t)

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "🔴🔑", limit: 10, alpha: 0.7,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("件数 = %d, want 2", len(results))
	}

	for _, r := range results {
		if r.LexicalScore != 0 {
			t.Errorf("lexical_score = %v, want 0", r.LexicalScore)
		}
	}
}

// TestSearchVectorUsesTheSameLexicalPath は2系統が同じ順位を返すことを見る。
//
// 🔴 SearchVector は計測のための口だが、SQL も語彙化も Search と共有している。
// 語彙化を片方だけに入れると、評価ハーネスの ErrRankingDiverged が発火するか、
// 発火しないまま2系統が別物を測ることになる。ここで直接確かめる。
func TestSearchVectorUsesTheSameLexicalPath(t *testing.T) {
	ts := hybridStore(t)

	e := newFakeEmbedder("fake:1024")
	e.angles["recall_store"] = 0

	// 🔴 Kind は KindQuery。Search が内部で使うのと同じ Kind でなければ、
	// 2系統が別のベクトルを測ることになる（ADR 0008）。
	vectors, err := e.Embed(t.Context(), []string{"recall_store"}, embed.KindQuery)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	query := newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: 0.2,
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

	if len(viaText) != len(viaVector) {
		t.Fatalf("件数が違う: %d / %d", len(viaText), len(viaVector))
	}

	for i := range viaText {
		if viaText[i].Chunk.ID != viaVector[i].Chunk.ID {
			t.Errorf("順位 %d が食い違う: %d / %d", i, viaText[i].Chunk.ID, viaVector[i].Chunk.ID)
		}

		if viaText[i].LexicalScore != viaVector[i].LexicalScore {
			t.Errorf("順位 %d の語彙スコアが食い違う: %v / %v",
				i, viaText[i].LexicalScore, viaVector[i].LexicalScore)
		}
	}
}

// TestSearchBreaksTiesByID は同点の並びが安定していることを見る。
//
// 🔴 PostgreSQL は同点行の順序を保証しない。副キーが無いと、alpha = 1.0 が
// 純ベクトルと一致することの検証が「たまたま一致した／しなかった」になり、
// 合成の自己検証が成立しなくなる。
func TestSearchBreaksTiesByID(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	// 角度も本文も同じ4件。スコアは完全な同点になる。
	chunks := make([]chunk.Chunk, 0, 4)
	for i := range 4 {
		chunks = append(chunks, newChunk(chunkSpec{
			orgID: orgA, documentID: 1, sourceID: 10,
			chunkIndex: i, content: "同点",
		}))
	}

	ids, err := ts.store.Put(t.Context(), orgA, chunks)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

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

// TestSearchDoesNotLeakAcrossOrgsViaLexicalMatch は語彙経路の org 分離を見る。
//
// 🔴 ベクトル経路の分離テストとは別に置く。将来 SQL が2本に割れたとき
// （索引を入れて「上位 N のマージ」へ移るときが最も危ない）、語彙側の WHERE に
// org_id を書き忘れても、ベクトル側のテストは緑のままである。
// 語彙が完全一致する餌を別 org に置いて、それが漏れないことを直接見る。
func TestSearchDoesNotLeakAcrossOrgsViaLexicalMatch(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	// org B にだけ、クエリと語彙が完全に一致する本文を置く。
	putOne(t, ts, orgB, "recall_store lexical bait")
	// org A には語彙が1つも重ならない本文を置く。
	putOne(t, ts, orgA, "unrelated content")

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "recall_store lexical bait", limit: 10, alpha: 0,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for _, r := range results {
		if r.Chunk.Content == "recall_store lexical bait" {
			t.Fatalf("別 org の本文が語彙一致で漏れた: %+v", r.Chunk)
		}

		if r.Chunk.OrgID != orgA {
			t.Fatalf("org %s の結果に org %s が混ざった", orgA, r.Chunk.OrgID)
		}
	}
}

// TestSearchRejectsBrokenTokensFromTheQuery は検索側でも契約違反を落とすことを見る。
//
// 取り込み側だけを検査すると、分割器を差し替えた人は「取り込みは通るのに検索が
// 構文エラーで落ちる」という遠い症状を見ることになる。
func TestSearchRejectsBrokenTokensFromTheQuery(t *testing.T) {
	spec := defaultStoreSpec(newFakeEmbedder("fake:1024"))
	spec.tokenizer = brokenTokenizer{tokens: []string{"a|b"}}

	ts := newTestStoreWith(t, spec)

	_, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "何か", limit: 10, alpha: 0.7,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err == nil {
		t.Fatalf("契約違反のトークンで検索が通った")
	}
}
