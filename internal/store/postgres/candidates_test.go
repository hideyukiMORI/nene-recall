package postgres_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// 本ファイルは候補モード（RECALL_SEARCH_MODE=candidates・ADR 0022）を検査する。
//
// 🔴 検査の中心は「速いこと」ではない。速さは計測の仕事であって検査の仕事では
// なく、閾値を切ると 1 回のゆらぎでゲートが赤くなる（ADR 0013 Decision 5）。
// ここで守るのは**候補生成が正解を落としていないこと**と、**索引が使われる形で
// あること**の2つである。

// corpusSize は候補モードの検査に使うコーパスの件数。
//
// 🔑 259 は評価セットの実件数と同じにしてある (testdata/eval/README.md)。
// 候補モードは K=100 で 259 件中 100 件を候補にする構成なので、「K より多い」
// 状態を実データと同じ規模で踏める。
const corpusSize = 259

// candidateCorpus は角度をばらけさせた corpusSize 件のストアを返す。
//
// 🔑 角度を i ごとに変えるので、ベクトルの順位は決定的である。3件おきに
// クエリと語彙を共有する本文を混ぜてあり、語彙側 top-K が空にならない。
func candidateCorpus(t *testing.T, spec storeSpec) *testStore {
	t.Helper()

	ts := newTestStoreWith(t, spec)
	orgA := mustOrgID(t, 1)

	chunks := make([]chunk.Chunk, 0, corpusSize)
	for i := range corpusSize {
		chunks = append(chunks, newChunk(chunkSpec{
			orgID: orgA, documentID: int64(i%7 + 1), sourceID: int64(i%3 + 1),
			chunkIndex: i, content: candidateContent(i),
		}))
	}

	if _, err := ts.store.Put(t.Context(), orgA, chunks); err != nil {
		t.Fatalf("Put: %v", err)
	}

	return ts
}

// candidateContent は i 番目の本文を返す。
//
// 3件に1件だけ検索語 recall_store を含める。ベクトルの角度とは独立なので、
// 「語彙でしか当たらない行」と「ベクトルでしか当たらない行」が両方できる。
func candidateContent(i int) string {
	if i%3 == 0 {
		return "recall_store " + strings.Repeat("あ", i%5+1)
	}

	return "無関係 " + strings.Repeat("い", i%5+1)
}

// candidateEmbedder は corpusSize 件ぶんの角度を登録した偽の埋め込みを返す。
//
// 角度は 0 から広がる。クエリ recall_store は角度 0 なので、i が小さいほど
// 内積が大きい——期待順位を i で書ける。
func candidateEmbedder() *fakeEmbedder {
	e := newFakeEmbedder("fake:1024")
	e.angles["recall_store"] = 0

	for i := range corpusSize {
		// 1.5 まで広げる。全件を別の角度にするので同点は起きない。
		e.angles[candidateContent(i)] = 1.5 * float64(i) / float64(corpusSize)
	}

	return e
}

// candidateSpec は候補モードのストア指定を返す。
func candidateSpec(k, ef int) storeSpec {
	spec := defaultStoreSpec(candidateEmbedder())
	spec.searchMode = postgres.SearchModeCandidates
	spec.candidateK = k
	spec.efSearch = ef

	return spec
}

// TestCandidatesMatchExhaustiveWhenKCoversTheCorpus は、K が件数以上のとき
// 候補モードと全探索が同じ上位 10 件を返すことを見る。
//
// 🔴 ADR 0022 Consequences が「最初の検査」と名指ししたものである。K が全件を
// 覆っているなら候補集合は org 内の全行と一致するので、2つの経路は**同じ答え**を
// 返さなければならない。食い違うなら候補生成が正解を落としているか、
// HNSW の近似が K 件を返しきれていない。
func TestCandidatesMatchExhaustiveWhenKCoversTheCorpus(t *testing.T) {
	const covering = corpusSize + 1

	exhaustive := candidateCorpus(t, defaultStoreSpec(candidateEmbedder()))
	candidates := attachStoreWith(t, candidateSpec(covering, covering))

	query := newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: 0.8,
		documentIDs: nil, sourceIDs: nil,
	})

	want, err := exhaustive.store.Search(t.Context(), query)
	if err != nil {
		t.Fatalf("Search(exhaustive): %v", err)
	}

	got, err := candidates.Search(t.Context(), query)
	if err != nil {
		t.Fatalf("Search(candidates): %v", err)
	}

	assertSameRanking(t, want, got)
}

// TestCandidatesNarrowTheCandidateSet は K が件数より小さいとき、候補が
// 実際に絞られていることを見る。
//
// 🔑 「絞られている」ことは結果の件数では見えない（limit のほうが小さい）ので、
// 語彙スコアの正規化に使われる最大値の違いを通して見る。候補集合が全行なら
// 全探索と同じ答えになるはずで、そうならないことが絞りの証拠である。
//
// ⚠️ 上位が入れ替わることを要求はしない。K を小さくしても上位が同じになる
// データはありうる。ここで見るのは「候補生成の結果として得られる件数が
// K の 2 倍を超えないこと」——両側 top-K の和集合という定義そのものである。
func TestCandidatesNarrowTheCandidateSet(t *testing.T) {
	const k = 20

	ts := candidateCorpus(t, candidateSpec(k, k))

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 2 * corpusSize, alpha: 0.8,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) > 2*k {
		t.Errorf("候補が %d 件ある。両側 top-%d の和集合なので %d 件以下のはず",
			len(results), k, 2*k)
	}

	if len(results) == corpusSize {
		t.Errorf("候補が全 %d 件のままである。K=%d で絞られていない", corpusSize, k)
	}
}

// TestCandidatesIncludeLexicalOnlyMatches は、語彙でしか当たらない行が候補に
// 入ることを見る。
//
// 🔴 ADR 0022 が「片側候補」を却下した理由そのものである。ベクトル側 top-K だけ
// から候補を作ると、識別子で名指しされた文書（exact-term の経路）が候補に
// 入らず、ADR 0014 が測った利得（exact-term +0.25）を捨てることになる。
//
// 仕掛けは2つ。餌はベクトル的に**最も遠い**（角度 1.6・コーパスのどれよりも遠い）
// ので、ベクトル側 top-K には決して入らない。一方でクエリの2トークンを両方
// 含むので、語彙側 top-K では首位になる。⇒ 候補に現れたなら語彙側の経路から
// 来たとしか説明がつかない。
func TestCandidatesIncludeLexicalOnlyMatches(t *testing.T) {
	const (
		k = 5
		// 🔴 クエリと餌の本文を**別の文字列**にする。偽の埋め込みは文字列から
		// 角度を引くので、同じ文字列にすると餌がクエリと同一ベクトルになり、
		// 「最も遠い」という前提が逆になる。
		queryText   = "recall_store bait"
		baitContent = "bait recall_store"
	)

	e := candidateEmbedder()
	e.angles[queryText] = 0     // クエリは角度 0
	e.angles[baitContent] = 1.6 // corpusSize 件のどれよりも遠い

	spec := candidateSpec(k, k)
	spec.embedder = e

	ts := candidateCorpus(t, spec)
	putOne(t, ts, mustOrgID(t, 1), baitContent)

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: queryText, limit: 2 * k, alpha: 0,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	for _, r := range results {
		if r.Chunk.Content == baitContent {
			return
		}
	}

	t.Errorf("語彙でしか当たらない行が候補に入っていない（%d 件返った）。"+
		"ベクトル側 top-K だけで候補を作っている", len(results))
}

// TestCandidatesDoNotLeakAcrossOrgs は候補生成の2段の両方に分離条件が
// 入っていることを見る。
//
// 🔴 org_id を vec と lex の**両方**に書かなければならない。片方だけに書いて
// 後段の JOIN で弾く実装でも結果は正しく見えるが、それは「別テナントの文書を
// 読んでから捨てた」のであって分離ではない (ADR 0003)。
//
// 餌は2種類置く。ベクトル的に最も近いもの（vec 側の穴を突く）と、語彙が
// 完全一致するもの（lex 側の穴を突く）である。片側だけの検査では、もう
// 片方の WHERE を消しても緑のままになる。
func TestCandidatesDoNotLeakAcrossOrgs(t *testing.T) {
	const k = 5

	e := candidateEmbedder()
	e.angles["別orgのベクトル餌"] = 0 // クエリと同じ角度＝最も近い

	spec := candidateSpec(k, k)
	spec.embedder = e

	ts := candidateCorpus(t, spec)
	orgB := mustOrgID(t, 2)

	putOne(t, ts, orgB, "別orgのベクトル餌")
	putOne(t, ts, orgB, "recall_store 別orgの語彙餌")

	assertNoForeignOrg(t, ts, mustOrgID(t, 1))
}

// assertNoForeignOrg は検索結果に別 org が1件も混ざらないことを見る。
//
// alpha を両端で振る。片方だけだと、効いていない側のスコアが 0 に潰れて
// 「漏れているのに順位に出ない」状態を見逃す。
func assertNoForeignOrg(t *testing.T, ts *testStore, orgID org.ID) {
	t.Helper()

	for _, alpha := range []float32{0, 1} {
		results, err := ts.store.Search(t.Context(), newQuery(querySpec{
			orgID: orgID, text: "recall_store", limit: 50, alpha: alpha,
			documentIDs: nil, sourceIDs: nil,
		}))
		if err != nil {
			t.Fatalf("Search(alpha=%v): %v", alpha, err)
		}

		assertAllBelongTo(t, results, orgID, alpha)
	}
}

// assertAllBelongTo は1回ぶんの結果が指定の org のものだけであることを見る。
//
// 🔴 org.ID と本文の両方を見る。列を読み戻さず問い合わせた org を写す実装
// (toResult) なので、OrgID だけを見ると「別 org の行に自分の org を貼って
// 返した」場合に気づけない。本文の印がその穴を塞ぐ。
func assertAllBelongTo(t *testing.T, results []index.Result, orgID org.ID, alpha float32) {
	t.Helper()

	for _, r := range results {
		if r.Chunk.OrgID != orgID {
			t.Fatalf("alpha=%v で別 org が漏れた: %+v", alpha, r.Chunk)
		}

		if strings.Contains(r.Chunk.Content, "別org") {
			t.Fatalf("alpha=%v で別 org の本文が漏れた: %q", alpha, r.Chunk.Content)
		}
	}
}

// TestCandidatesApplyFiltersToBothStages は絞り込みが2段の両方に効くことを見る。
//
// 🔴 org_id と同じ理由で、filters も vec と lex の両方に要る。片側だけだと
// 絞り込んだはずの文書が候補枠を食い潰し、「絞り込むと recall が落ちる」という
// 説明のつかない挙動になる。
func TestCandidatesApplyFiltersToBothStages(t *testing.T) {
	const k = 5

	ts := candidateCorpus(t, candidateSpec(k, k))

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 2 * k, alpha: 0.5,
		documentIDs: []int64{1}, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("結果が空である")
	}

	for _, r := range results {
		if r.Chunk.DocumentID != 1 {
			t.Errorf("絞り込みの外の document_id %d が返った", r.Chunk.DocumentID)
		}
	}
}

// TestCandidatesWorkWithRRF は候補モードと順位融合の組み合わせが動くことを見る。
//
// 🔴 4通り（2方式 × 2モード）を1つも落とさない。candidates × rrf が動かないと、
// 「候補集合を絞る構成では RRF の評価が変わりうる」という ADR 0015 の留保を
// after のレーンが測れなくなる (ADR 0022 Decision 1)。
func TestCandidatesWorkWithRRF(t *testing.T) {
	const k = 20

	spec := candidateSpec(k, k)
	spec.fusion = postgres.FusionRRF

	ts := candidateCorpus(t, spec)

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: 0.8,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 10 {
		t.Fatalf("件数 = %d, want 10", len(results))
	}

	// RRF のスコアは 1/(k+rank) の和なので、必ず正である。0 が返るなら
	// 順位が振られていない（候補集合が空か、CTE が繋がっていない）。
	for i, r := range results {
		if r.Score <= 0 {
			t.Errorf("順位 %d のスコアが %v である。RRF は正の値を返す", i, r.Score)
		}
	}
}

// TestSearchVectorUsesTheCandidatePath は系統2 の計測口も候補モードで走ることを見る。
//
// 🔴 SearchVector は「埋め込み往復を除く p95」を測る口である (CLAUDE.md 地雷10)。
// ここが全探索のままだと、系統1 と系統2 が別の SQL を測ることになり、2つの
// 差が「埋め込み往復ぶん」でなくなる。
func TestSearchVectorUsesTheCandidatePath(t *testing.T) {
	const k = 20

	e := candidateEmbedder()

	spec := candidateSpec(k, k)
	spec.embedder = e

	ts := candidateCorpus(t, spec)

	// 🔴 Kind は KindQuery。Search が内部で使うのと同じ Kind でなければ、
	// 2系統が別のベクトルを測ることになる（ADR 0008）。
	vectors, err := e.Embed(t.Context(), []string{"recall_store"}, embed.KindQuery)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	query := newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: 0.8,
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
}

// TestNewRejectsCandidateKAboveEfSearch は K > ef_search を構築時に落とすことを見る。
//
// 🔴 HNSW は ef_search 件より多くを返せない。K > ef_search の構成は「候補が
// K 件に満たない」だけの症状しか出さず、recall が静かに下がる (ADR 0022
// Decision 4)。設定の誤りは設定を読んだ直後に落とす。
func TestNewRejectsCandidateKAboveEfSearch(t *testing.T) {
	spec := candidateSpec(100, 40)

	_, err := postgres.New(nil, storeOptions(spec))
	if !errors.Is(err, postgres.ErrCandidateK()) {
		t.Fatalf("K=100 > ef_search=40 が受け入れられた: %v", err)
	}
}

// TestNewRejectsNonPositiveCandidateK は K < 1 を構築時に落とすことを見る。
//
// K=0 は候補が常に空になる構成である。検索は成功して 0 件を返すので、
// 「該当なし」と区別がつかない——検索で最も危険な壊れ方である。
func TestNewRejectsNonPositiveCandidateK(t *testing.T) {
	spec := candidateSpec(0, 40)

	_, err := postgres.New(nil, storeOptions(spec))
	if !errors.Is(err, postgres.ErrCandidateK()) {
		t.Fatalf("K=0 が受け入れられた: %v", err)
	}
}

// TestNewIgnoresCandidateKInExhaustiveMode は、全探索では K と探索幅を
// 見ないことを見る。
//
// 🔴 「使わない値の妥当性」を要求すると、全探索で測るだけの構成が候補モードの
// 都合に付き合わされる。全探索の経路に K も探索幅も存在しない。
func TestNewIgnoresCandidateKInExhaustiveMode(t *testing.T) {
	spec := defaultStoreSpec(newFakeEmbedder("fake:1024"))
	spec.candidateK = 0
	spec.efSearch = 0

	if _, err := postgres.New(nil, storeOptions(spec)); err != nil {
		t.Fatalf("全探索なのに K と探索幅で落ちた: %v", err)
	}
}

// TestNewRejectsUnknownSearchMode は未知の候補の作り方を構築時に落とすことを見る。
//
// SearchMode は int なので、範囲外の値を作ること自体は言語仕様上いつでもできる
// (GO-003)。検索のたびに失敗する構成を「起動はする」状態にしない。
func TestNewRejectsUnknownSearchMode(t *testing.T) {
	spec := defaultStoreSpec(newFakeEmbedder("fake:1024"))
	spec.searchMode = postgres.SearchMode(99)

	_, err := postgres.New(nil, storeOptions(spec))
	if !errors.Is(err, postgres.ErrUnknownSearchMode()) {
		t.Fatalf("未知の候補の作り方が受け入れられた: %v", err)
	}
}

// TestRankingSettingsRecordTheSearchMode は条件の記録を見る。
//
// 🔴 全探索では K と探索幅を**記録しない**（nil）。全探索の経路にそのつまみは
// 存在せず、値を書けば「その条件で測った」と読まれる（様式 v4 が sqlite の
// ts_rank_normalization で塞いだのと同じ穴・ADR 0022 Decision 3）。
func TestRankingSettingsRecordTheSearchMode(t *testing.T) {
	exhaustive := newTestStoreWith(t, defaultStoreSpec(newFakeEmbedder("fake:1024"))).
		store.RankingSettings()
	if exhaustive.SearchMode != "exhaustive" {
		t.Errorf("search_mode = %q, want %q", exhaustive.SearchMode, "exhaustive")
	}

	if exhaustive.CandidateK != nil || exhaustive.EfSearch != nil {
		t.Errorf("全探索なのに K/探索幅が記録された: %v / %v",
			exhaustive.CandidateK, exhaustive.EfSearch)
	}

	candidates := attachStoreWith(t, candidateSpec(20, 40)).RankingSettings()
	if candidates.SearchMode != "candidates" {
		t.Errorf("search_mode = %q, want %q", candidates.SearchMode, "candidates")
	}

	if candidates.CandidateK == nil || *candidates.CandidateK != 20 {
		t.Errorf("candidate_k = %v, want 20", candidates.CandidateK)
	}

	if candidates.EfSearch == nil || *candidates.EfSearch != 40 {
		t.Errorf("ef_search = %v, want 40", candidates.EfSearch)
	}
}

// TestCandidateSearchUsesTheHNSWIndex は実行計画に索引が現れることを見る。
//
// 🔴 索引が「存在すること」（migrate_test.go）と「使われること」は別である。
// 演算子クラスが合っていても、SQL の形が索引の順序に乗っていなければ
// planner は Seq Scan を選ぶ。ADR 0022 が候補モードを足した理由はまさにそこで、
// 全探索の SQL は ORDER BY が合成式なので索引に乗らない。
//
// ⚠️ ExplainSearch は enable_seqscan を切って計画を取る。259 件では全行走査の
// ほうが実際に安く、planner は索引が正しく張られていても Seq Scan を選ぶ
// （2026-09-02 実測）。⇒ このテストが言えるのは「**この件数でも速い**」では
// なく「**SQL の形が索引の順序に乗っている**」ことである。速さは計測の仕事で
// あって検査の仕事ではない (ADR 0013 Decision 5)。
//
// 🔑 EXPLAIN は Search と同じ経路（ExplainSearch）で組み立てた文に対して取る。
// テストが SQL を自前で組み直すと、本番が使う文とは別の文の計画を見ることになる。
func TestCandidateSearchUsesTheHNSWIndex(t *testing.T) {
	const k = 20

	ts := candidateCorpus(t, candidateSpec(k, k))
	analyzeChunks(t, ts)

	plan, err := ts.store.ExplainSearch(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: 0.8,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("ExplainSearch: %v", err)
	}

	if !strings.Contains(plan, "chunks_embedding_hnsw") {
		t.Errorf("実行計画に chunks_embedding_hnsw が現れない。"+
			"候補生成の SQL が索引の順序に乗っていない:\n%s", plan)
	}

	// 🔴 語彙側は @@ で絞ってから ts_rank を計算する形であること。
	//
	// ここで chunks_lexemes_gin が計画に**出ることは要求しない**。259 件・単一 org の
	// テストデータでは org_id の B-tree を舐めて全行を取り、そこから @@ で絞るほうが
	// planner の見積りで安く、GIN は選ばれない（2026-09-02 実測）。件数の帰結であって
	// 索引や SQL の欠陥ではない。索引の存在と演算子クラスは
	// TestSearchIndexesExistWithCorrectOperatorClass が別に見ている。
	//
	// 🔑 代わりに**形**を見る。@@ が計画に現れることは、語彙側が「全行に ts_rank を
	// 計算してから並べる」形ではないことの証拠である。ADR 0022 の却下表が
	// 「GIN を後回しにしない」と書いた理由——before の EXPLAIN では ts_rank の
	// 全行計算が距離計算より 1.68 倍重かった——は、この形で解かれている。
	if !strings.Contains(plan, "@@") {
		t.Errorf("実行計画に @@ が現れない。語彙側が全行に ts_rank を計算している:\n%s", plan)
	}
}

// TestExhaustiveSearchDoesNotUseTheHNSWIndex は、全探索の SQL が索引を
// 使わないことを見る。
//
// 🔴 これは失敗の検査ではなく、**ADR 0022 Decision 3 の記録**である。
// 索引は migration 0004 で常に張られているので、「索引を張ったのに速くならない」
// を「索引が壊れている」と読む経路ができる。壊れているのではなく、
// 全探索の ORDER BY が alpha*vector + (1-alpha)*lexical という合成式であり、
// どの索引の順序でもないためである。
//
// ⇒ before/after の差には「索引の効果」と「候補生成の効果」が分離できない形で
// 混ざる。この検査は、その但し書きがコードの実態と一致していることを保つ。
//
// 🔑 ExplainSearch は enable_seqscan を切って計画を取るので、これは
// 「planner が索引より全行走査を選んだ」のではない。**seq scan を禁じても
// なお HNSW に乗れない**——形の問題であることが、この条件下で初めて言える。
func TestExhaustiveSearchDoesNotUseTheHNSWIndex(t *testing.T) {
	ts := candidateCorpus(t, defaultStoreSpec(candidateEmbedder()))
	analyzeChunks(t, ts)

	plan, err := ts.store.ExplainSearch(t.Context(), newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: 0.8,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("ExplainSearch: %v", err)
	}

	if strings.Contains(plan, "chunks_embedding_hnsw") {
		t.Errorf("全探索の実行計画に HNSW が現れた。ADR 0022 Decision 3 の"+
			"「索引の効果と候補生成の効果は分離できない」という記述が実態と食い違う:\n%s", plan)
	}
}

// TestCandidateSearchAppliesEfSearch は hnsw.ef_search が検索の中で効いている
// ことを見る。
//
// 🔴 K ≤ ef_search でなければ HNSW は K 件を返せない (ADR 0022 Decision 4)。
// New が構成を拒否しても、実行時に設定が届いていなければ意味が無い。
// 症状は「候補が K 件に満たない」＝ recall が少し低いだけなので、
// 直接読んで確かめるしかない。
//
// ⚠️ pgvector の既定と違う値を使う。既定 (40) のままだと、設定が1行も
// 効いていなくても同じ値が返り、テストが何も言わなくなる。
func TestCandidateSearchAppliesEfSearch(t *testing.T) {
	const (
		k        = 20
		efSearch = 77 // pgvector の既定 40 とは違う値
	)

	ts := candidateCorpus(t, candidateSpec(k, efSearch))

	got, err := ts.store.EffectiveEfSearch(t.Context())
	if err != nil {
		t.Fatalf("EffectiveEfSearch: %v", err)
	}

	if want := strconv.Itoa(efSearch); got != want {
		t.Errorf("hnsw.ef_search = %q, want %q（SET LOCAL が届いていない）", got, want)
	}
}

// tieContent は語彙スコアが同点になる行の本文。
//
// 本文が同一なら lexemes も同一で、同じクエリに対する ts_rank も同一になる。
// ⇒ 「同点」を作るのに ts_rank の内部を知る必要が無い。
const tieContent = "tie_term filler"

// tieQuery は同点の行だけに当たる検索語。
//
// コーパスの本文（candidateContent）は recall_store か無関係のどちらかなので、
// この語では 1 件も当たらない。⇒ 語彙側 top-K は同点の行だけで埋まる。
const tieQuery = "tie_term"

// tieGroupSize は同点で並べる行数。K より多くして、top-K の境界が
// 同点の途中を切るようにする。
const tieGroupSize = 40

// putTieGroup は同点になる行を tieGroupSize 件投入し、採番された id を返す。
//
// 🔴 id は投入順に増える（insert-only の bigserial）。この前提が崩れると
// 「同点なら小さい id が勝つ」という期待そのものが書けなくなる。
func putTieGroup(t *testing.T, ts *testStore, orgID org.ID) []int64 {
	t.Helper()

	chunks := make([]chunk.Chunk, 0, tieGroupSize)
	for i := range tieGroupSize {
		chunks = append(chunks, newChunk(chunkSpec{
			orgID: orgID, documentID: 9, sourceID: 9,
			chunkIndex: i, content: tieContent,
		}))
	}

	ids, err := ts.store.Put(t.Context(), orgID, chunks)
	if err != nil {
		t.Fatalf("Put(同点の行): %v", err)
	}

	// Put は「入力と同じ順の id」を返す契約である (ADR 0013)。
	if len(ids) != tieGroupSize {
		t.Fatalf("採番された id の数 = %d, want %d", len(ids), tieGroupSize)
	}

	scrambleHeapOrder(t, ts, ids)

	return ids
}

// scrambleHeapOrder は同点の行の**物理的な並び**を id の逆順にする。
//
// 🔑 PostgreSQL の UPDATE は行を書き換えず、新しい版を足す。⇒ id の大きい
// ものから順に更新すると、ヒープ上の並びは id の降順になる（実測で確認した。
// ctid が (7,13)=id 271 … (7,24)=id 260 の順に並ぶ）。投入直後のヒープは
// id 昇順なので、これをやらないと「走査順＝id 昇順」という、本番では
// 成り立たない条件でしか試していないことになる。
//
// ⚠️ 更新するのは採点に関わらない列（section_label）である。lexeme_text や
// content を触ると ts_rank が動き、同点という前提そのものが壊れる。
func scrambleHeapOrder(t *testing.T, ts *testStore, ids []int64) {
	t.Helper()

	for _, id := range slices.Backward(ids) {
		_, err := ts.db.ExecContext(t.Context(),
			`UPDATE chunks SET section_label = $2 WHERE id = $1`, id, "shuffled")
		if err != nil {
			t.Fatalf("物理順序を入れ替えられない (id=%d): %v", id, err)
		}
	}
}

// tieSpec は同点の行を「ベクトルでは絶対に候補に入らない」位置に置いた指定を返す。
//
// 🔑 角度 1.6 はコーパスのどれよりも遠い（candidateEmbedder は 1.5 まで）。
// ⇒ 同点の行が候補に現れたなら、語彙側 top-K から来たとしか説明がつかない。
func tieSpec(k int) storeSpec {
	e := candidateEmbedder()
	e.angles[tieQuery] = 0
	e.angles[tieContent] = 1.6

	spec := candidateSpec(k, postgres.DefaultEfSearch)
	spec.embedder = e

	return spec
}

// TestLexicalCandidatesBreakTiesByID は、語彙側 top-K の同点が id で切られる
// ことを見る。
//
// 🔴 これは 10万件で実際に起きた欠陥の再発防止である。ts_rank は同点になる。
// 2026-09-02 の実測では K=100 の境界に 36 行が同点で並び、第2ソートキーが
// 無かったため、同じクエリの語彙側 top-K が 15 回の実行で **15 通り**の id 集合を
// 返した。候補集合が変われば MAX(lexical_score) OVER () が変わり、最終順位の
// 下位が入れ替わる ⇒ 2系統の順位が一致せず評価ハーネスが止まる
// (docs/benchmarks/2026-09-02-eval-100k-after-index.md §9)。
//
// ⚠️ **このテストは「揺れ」を再現しない。** 2026-09-03 に確かめた:
// 同点を 40 件に増やし、scrambleHeapOrder で物理順序を id の降順にしても、
// 第2ソートキーを外した SQL は id 昇順を返し、このテストは緑のままだった。
// 実行計画は Bitmap Heap Scan + top-N heapsort である。同じ形を psql で
// Seq Scan にすると、同点 12 件のうち**最大側の id** が返る——つまり
// 「どれが残るか」は plan 次第で、259 件では欲しい plan を安定して作れない。
//
// ⇒ **ここで固定するのは規則のほうである。** 同点なら小さい id が候補に入る、
// と決めておけば、どの plan で走っても揺れる余地が SQL から消える。第2キーを
// 落とす変更はこのテストを通り抜けうるが、そのときこのコメントが「なぜ
// 要るのか」を次に読む人へ渡す。**検査で捕まえられない規則は、書いて残す。**
func TestLexicalCandidatesBreakTiesByID(t *testing.T) {
	const k = 5

	orgA := mustOrgID(t, 1)
	ts := candidateCorpus(t, tieSpec(k))
	ids := putTieGroup(t, ts, orgA)

	got := tiedCandidateIDs(t, ts, orgA, k)

	// 同点なら小さい id が勝つ。⇒ 候補に入るのは先頭 K 件である。
	want := ids[:k]
	if !equalInt64s(got, want) {
		t.Errorf("同点の行のうち候補に入った id = %v, want %v（先頭 %d 件）。"+
			"語彙側 top-K の ORDER BY に第2ソートキー id が無い", got, want, k)
	}
}

// TestLexicalCandidatesAreStableAcrossRuns は、同点を含む候補集合が実行を
// またいで同じであることを見る。
//
// 🔑 §9 の実測が読み取ったのは「15 回で 15 通り」という**回数をまたいだ**
// 不安定さだった。⇒ 検査も回数をまたいで見る。1 回の結果が正しいことと、
// 何度実行しても同じであることは別の性質である。
func TestLexicalCandidatesAreStableAcrossRuns(t *testing.T) {
	const (
		k     = 5
		runs  = 15
		first = 0
	)

	orgA := mustOrgID(t, 1)
	ts := candidateCorpus(t, tieSpec(k))
	putTieGroup(t, ts, orgA)

	want := tiedCandidateIDs(t, ts, orgA, k)

	for run := range runs {
		if run == first {
			continue
		}

		got := tiedCandidateIDs(t, ts, orgA, k)
		if !equalInt64s(got, want) {
			t.Fatalf("%d 回目の候補が違う: %v, 1 回目: %v。同点の切り方が決まっていない",
				run+1, got, want)
		}
	}
}

// tiedCandidateIDs は検索を1回実行し、同点の行のうち候補に入ったものの id を返す。
//
// alpha=0（語彙のみ）で引く。ベクトル側の寄与を 0 にしても候補集合は
// 両側 top-K の和集合のままなので、同点の行が「語彙側から来た」ことは変わらない。
func tiedCandidateIDs(t *testing.T, ts *testStore, orgID org.ID, k int) []int64 {
	t.Helper()

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgID, text: tieQuery, limit: 4 * k, alpha: 0,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	ids := []int64{}

	for _, r := range results {
		if r.Chunk.Content == tieContent {
			ids = append(ids, r.Chunk.ID)
		}
	}

	slices.Sort(ids)

	return ids
}

// equalInt64s は2つの id 列が同じかを返す。
func equalInt64s(left, right []int64) bool { return slices.Equal(left, right) }

// TestCandidateSearchForcesCustomPlans は plan_cache_mode が検索に届いている
// ことを見る。
//
// 🔴 これが無いと 6 回目の検索から HNSW を使わなくなる。PostgreSQL は同じ
// プリペアド文が 5 回走った後に汎用計画へ切り替えることがあり、候補モードの
// SQL でそれが起きると、ベクトル側が Parallel Seq Scan + Sort に落ちる。
// 2026-09-02 の 10万件の実測では、同じクエリの 6 回目が 40 倍遅くなった
// (docs/benchmarks/2026-09-02-eval-100k-after-index.md §10)。
//
// 🔑 症状は「遅い」だけで結果は正しいまま返る。⇒ 検査で捕まえられるのは
// 「設定が届いているか」だけである。速さは計測の仕事であって検査の仕事では
// ない (ADR 0013 Decision 5)。EffectiveEfSearch と同じ流儀で、検索が使うのと
// 同じ適用経路（applySearchLocals）を通して読み戻す。
func TestCandidateSearchForcesCustomPlans(t *testing.T) {
	const k = 20

	ts := candidateCorpus(t, candidateSpec(k, postgres.DefaultEfSearch))

	got, err := ts.store.EffectivePlanCacheMode(t.Context())
	if err != nil {
		t.Fatalf("EffectivePlanCacheMode: %v", err)
	}

	if want := "force_custom_plan"; got != want {
		t.Errorf("plan_cache_mode = %q, want %q（SET LOCAL が届いていない）", got, want)
	}
}

// TestCandidateSearchKeepsTheHNSWIndexWhenRepeated は、同じクエリを繰り返しても
// 実行計画から索引が消えないことを見る。
//
// 🔴 切り替わるのは 6 回目からである。1 回だけ EXPLAIN を取る
// TestCandidateSearchUsesTheHNSWIndex では、この欠陥は原理的に見えない。
//
// ⚠️ このテストは緑になっても「切り替わらないこと」を証明しない。プリペアド文の
// 実行回数は接続ごとに数えられるので、プールが毎回別の接続を返せば 6 回目に
// 到達しない。⇒ **赤くなったときだけ意味がある**検査である。決定的な保証は
// TestCandidateSearchForcesCustomPlans（設定の読み戻し）のほうが持つ。
func TestCandidateSearchKeepsTheHNSWIndexWhenRepeated(t *testing.T) {
	const (
		k = 20
		// 汎用計画へ切り替わるのは 6 回目からなので、それを確実に越える回数。
		repeats = 10
	)

	ts := candidateCorpus(t, candidateSpec(k, postgres.DefaultEfSearch))
	analyzeChunks(t, ts)

	query := newQuery(querySpec{
		orgID: mustOrgID(t, 1), text: "recall_store", limit: 10, alpha: 0.8,
		documentIDs: nil, sourceIDs: nil,
	})

	for run := range repeats {
		// 検索そのものも走らせる。EXPLAIN だけを繰り返すと、本番が使う文の
		// 実行回数が増えないので、条件が本番と揃わない。
		if _, err := ts.store.Search(t.Context(), query); err != nil {
			t.Fatalf("%d 回目の Search: %v", run+1, err)
		}

		plan, err := ts.store.ExplainSearch(t.Context(), query)
		if err != nil {
			t.Fatalf("%d 回目の ExplainSearch: %v", run+1, err)
		}

		if !strings.Contains(plan, "chunks_embedding_hnsw") {
			t.Fatalf("%d 回目の実行計画から HNSW が消えた。"+
				"プリペアド文が汎用計画へ切り替わっている:\n%s", run+1, plan)
		}
	}
}

// analyzeChunks は統計を取り直す。
//
// 🔴 EXPLAIN を読む前に必ず呼ぶ。投入直後の表には統計が無く、planner は
// 「@@ に何行当たるか」を既定値で当てずっぽうに見積もる。その見積りの下では
// GIN より B-tree を舐めるほうが安く見え、索引の形とは無関係な計画が出る
// （2026-09-02 実測）。本番では autovacuum が統計を維持するので、
// ここで手動 ANALYZE するのは本番に条件を合わせる操作である。
func analyzeChunks(t *testing.T, ts *testStore) {
	t.Helper()

	if _, err := ts.db.ExecContext(t.Context(), `ANALYZE chunks`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
}
