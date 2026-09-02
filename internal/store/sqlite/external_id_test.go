package sqlite_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/org"
	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// 外部 id（Corpus 側の chunks.id）を受ける経路の検査。
//
// 🔴 postgres 側の external_id_test.go と観点を1つずつ対応させてある。
// 2つのストアは比較のために並べて読まれるので、片方にしか無い観点があると
// 「どちらの契約が正か」が読めなくなる。片方に足したらもう片方にも足すこと。
//
// 判断の正本は docs/adr/0020-phase2-corpus-integration-contract.md の Decision 1。

// externalChunk は外部 id 付きのチャンクを1件作る。
func externalChunk(t *testing.T, spec chunkSpec, externalID int64) chunk.Chunk {
	t.Helper()

	c := newChunk(spec)
	c.ExternalID = &externalID

	return c
}

// contentOf は行の本文を読む。
func contentOf(t *testing.T, ts *testStore, id int64) string {
	t.Helper()

	var got string

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT content FROM chunks WHERE id = ?`, id).Scan(&got)
	if err != nil {
		t.Fatalf("id %d の本文を読めない: %v", id, err)
	}

	return got
}

// TestPutReplacesRowWithSameExternalID は同じ外部 id の再投入が置き換えになることを見る。
func TestPutReplacesRowWithSameExternalID(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	spec := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "はじめの本文"}

	first, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{externalChunk(t, spec, 1000)})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	spec.content = "書き換えた本文"
	spec.documentID = 2

	second, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{externalChunk(t, spec, 1000)})
	if err != nil {
		t.Fatalf("再 Put: %v", err)
	}

	if second[0] != first[0] {
		t.Errorf("🔴 id = %d, want %d（置き換えではなく新規採番になっている）", second[0], first[0])
	}

	if got := countChunks(t, ts, orgA); got != 1 {
		t.Errorf("🔴 行数 = %d, want 1（置き換えのはずが2行になった）", got)
	}

	if got := contentOf(t, ts, first[0]); got != "書き換えた本文" {
		t.Errorf("本文 = %q, want %q（更新されていない）", got, "書き換えた本文")
	}
}

// TestPutReplacementUpdatesFTSIndex は置き換えが転置索引にも伝わることを見る。
//
// 🔴 これは SQLite 側にしか無い観点である。外部コンテンツの FTS5 表は自分では
// 同期せず、chunks_fts_after_update trigger だけが差分を伝える
// (migrations/0001_create_chunks.sql)。trigger を欠くと、置き換えた行の語彙
// スコアだけが古い本文のまま残る——エラーにならず、語彙が少しずれるだけである。
// postgres 側は lexemes が生成列なので、この経路が構造的に存在しない。
func TestPutReplacementUpdatesFTSIndex(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	spec := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "いぬ"}

	if _, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{externalChunk(t, spec, 1)}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	spec.content = "ねこ"

	if _, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{externalChunk(t, spec, 1)}); err != nil {
		t.Fatalf("再 Put: %v", err)
	}

	if got := ftsMatchCount(t, ts, "いぬ"); got != 0 {
		t.Errorf("🔴 古い本文が転置索引に残っている。一致件数 = %d, want 0", got)
	}

	if got := ftsMatchCount(t, ts, "ねこ"); got != 1 {
		t.Errorf("新しい本文が転置索引に無い。一致件数 = %d, want 1", got)
	}
}

// ftsMatchCount は fts 表を直に引いて一致件数を返す。
//
// 分割器は fakeTokenizer なので、本文がそのまま1トークンになる。
func ftsMatchCount(t *testing.T, ts *testStore, term string) int {
	t.Helper()

	var n int

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH ?`, `"`+term+`"`).Scan(&n)
	if err != nil {
		t.Fatalf("fts を引けない: %v", err)
	}

	return n
}

// TestPutKeepsInputOrderWhenReplacing は置き換えが混ざっても id の順序が保たれることを見る。
//
// 🔴 「入力と同じ順の id を返す」は ADR 0013 の写像が依存している契約である。
func TestPutKeepsInputOrderWhenReplacing(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	base := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "一つ目"}
	second := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 1, content: "二つ目"}

	first, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{
		externalChunk(t, base, 10), externalChunk(t, second, 20),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	third := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 2, content: "三つ目"}

	got, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{
		externalChunk(t, second, 20), newChunk(third), externalChunk(t, base, 10),
	})
	if err != nil {
		t.Fatalf("再 Put: %v", err)
	}

	if got[0] != first[1] {
		t.Errorf("ids[0] = %d, want %d（external_id=20 の既存行）", got[0], first[1])
	}

	if got[2] != first[0] {
		t.Errorf("ids[2] = %d, want %d（external_id=10 の既存行）", got[2], first[0])
	}

	if got[1] == first[0] || got[1] == first[1] {
		t.Errorf("ids[1] = %d は既存行の id である。外部 id を持たない行は新規採番になるはず", got[1])
	}

	if n := countChunks(t, ts, orgA); n != 3 {
		t.Errorf("行数 = %d, want 3", n)
	}
}

// TestPutRejectsDuplicateExternalIDInOneBatch は同一バッチ内の重複を拒否することを見る。
func TestPutRejectsDuplicateExternalIDInOneBatch(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	spec := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文"}
	other := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 1, content: "別の本文"}

	_, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{
		externalChunk(t, spec, 77), externalChunk(t, other, 77),
	})
	if !errors.Is(err, sqlite.ErrDuplicateExternalID()) {
		t.Fatalf("err = %v, want ErrDuplicateExternalID", err)
	}

	if got := countChunks(t, ts, orgA); got != 0 {
		t.Errorf("拒否したはずの行が入っている。行数 = %d, want 0", got)
	}
}

// TestPutRejectsNonPositiveExternalID は 0 と負値を拒否することを見る。
func TestPutRejectsNonPositiveExternalID(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	spec := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文"}

	for name, value := range map[string]int64{"0": 0, "負値": -1} {
		t.Run(name, func(t *testing.T) {
			_, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{externalChunk(t, spec, value)})
			if !errors.Is(err, sqlite.ErrExternalIDInvalid()) {
				t.Fatalf("err = %v, want ErrExternalIDInvalid", err)
			}
		})
	}
}

// TestSameExternalIDInDifferentOrgsAreDistinctRows は外部 id が org を跨がないことを見る。
func TestSameExternalIDInDifferentOrgsAreDistinctRows(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	specA := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "A の本文"}
	specB := chunkSpec{orgID: orgB, documentID: 1, sourceID: 1, chunkIndex: 0, content: "B の本文"}

	idsA, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{externalChunk(t, specA, 500)})
	if err != nil {
		t.Fatalf("Put(A): %v", err)
	}

	idsB, err := ts.store.Put(t.Context(), orgB, []chunk.Chunk{externalChunk(t, specB, 500)})
	if err != nil {
		t.Fatalf("Put(B): %v", err)
	}

	if idsA[0] == idsB[0] {
		t.Fatalf("🔴 別 org の同じ external_id が同じ行になった (id=%d)", idsA[0])
	}

	if got := contentOf(t, ts, idsA[0]); got != "A の本文" {
		t.Errorf("🔴 org A の本文が上書きされた: %q", got)
	}

	if got := countChunks(t, ts, orgA); got != 1 {
		t.Errorf("org A の行数 = %d, want 1", got)
	}
}

// TestPutStoresNullWhenExternalIDIsAbsent は外部 id を持たない投入が NULL になることを見る。
//
// 🔴 0 を入れないこと。UNIQUE 索引は NULL どうしを重複とみなさないが、0 どうしは
// 重複とみなす。0 を入れると単体運用の2件目が1件目を上書きする。
func TestPutStoresNullWhenExternalIDIsAbsent(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	for i, content := range []string{"一つ目", "二つ目", "三つ目"} {
		putContent(t, ts, chunkSpec{
			orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: i, content: content,
		})
	}

	var stored int

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM chunks WHERE external_id IS NOT NULL`).Scan(&stored)
	if err != nil {
		t.Fatalf("external_id を数えられない: %v", err)
	}

	if stored != 0 {
		t.Errorf("external_id を持つ行 = %d, want 0", stored)
	}

	if got := countChunks(t, ts, orgA); got != 3 {
		t.Errorf("🔴 行数 = %d, want 3（external_id なしの行が互いを上書きしている）", got)
	}
}

// TestSearchReturnsExternalID は検索結果に外部 id が載ることを見る。
func TestSearchReturnsExternalID(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["問い"] = 0
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	spec := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文"}

	if _, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{externalChunk(t, spec, 4242)}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "問い", limit: 10, alpha: 1, documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("件数 = %d, want 1", len(results))
	}

	got := results[0].Chunk.ExternalID
	if got == nil || *got != 4242 {
		t.Errorf("external_id = %v, want 4242", got)
	}
}

// TestDeleteByDocumentDoesNotCrossOrgs は文書単位の削除が org を跨がないことを見る。
//
// 🔴 document_id は org ごとに独立した採番なので、org を条件に入れ忘れると
// 「同じ番号の別テナントの文書」が一緒に消える。消えた側には何も報告されない。
func TestDeleteByDocumentDoesNotCrossOrgs(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 100, sourceID: 1, chunkIndex: 0, content: "A の本文",
	})
	putContent(t, ts, chunkSpec{
		orgID: orgB, documentID: 100, sourceID: 1, chunkIndex: 0, content: "B の本文",
	})

	deleted, err := ts.store.DeleteByDocument(t.Context(), orgA, 100)
	if err != nil {
		t.Fatalf("DeleteByDocument: %v", err)
	}

	if deleted != 1 {
		t.Errorf("消えた件数 = %d, want 1", deleted)
	}

	if got := countChunks(t, ts, orgB); got != 1 {
		t.Errorf("🔴 別 org の文書が消えた。org B の行数 = %d, want 1", got)
	}
}

// TestDeleteByDocumentRequiresOrgID はゼロ値の org を拒否することを見る。
func TestDeleteByDocumentRequiresOrgID(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	var zeroOrg org.ID

	if _, err := ts.store.DeleteByDocument(t.Context(), zeroOrg, 1); !errors.Is(err, sqlite.ErrOrgRequired()) {
		t.Fatalf("err = %v, want ErrOrgRequired", err)
	}
}
