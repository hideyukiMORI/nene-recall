package postgres_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/org"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// 外部 id（Corpus 側の chunks.id）を受ける経路の検査。
//
// 判断の正本は docs/adr/0020-phase2-corpus-integration-contract.md の Decision 1。
// ここで縛りたいのは「同じ (org_id, external_id) の再投入が置き換えになること」と、
// 置き換えが**分離と順序の契約を壊さないこと**の2つである。
//
// 🔴 置き換えの失敗はどれも静かである。org を跨いで上書きすれば別テナントの本文が
// 消え、順序がずれれば呼び出し側の対応表が別の行を指し、同一バッチ内の重複を
// 後勝ちで飲めば「受理した件数」と実際の行数が食い違う。どれもエラーにならない。

// externalChunk は外部 id 付きのチャンクを1件作る。
//
// chunkSpec に externalID を足さないのは、既存の全リテラルが exhaustruct で
// 全フィールドの明示を求められており、テストの本題でない項目が全箇所に
// 並ぶことになるためである。ここでは組み立ててから1項目だけ上書きする
// （writer_test.go が withID.ID = 42 としているのと同じ流儀）。
func externalChunk(t *testing.T, spec chunkSpec, externalID int64) chunk.Chunk {
	t.Helper()

	c := newChunk(spec)
	c.ExternalID = &externalID

	return c
}

// externalIDOf は行に保存されている external_id を読む。NULL なら nil。
func externalIDOf(t *testing.T, ts *testStore, id int64) *int64 {
	t.Helper()

	var value *int64

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT external_id FROM chunks WHERE id = $1`, id).Scan(&value)
	if err != nil {
		t.Fatalf("id %d の external_id を読めない: %v", id, err)
	}

	return value
}

// contentOf は行の本文を読む。
func contentOf(t *testing.T, ts *testStore, id int64) string {
	t.Helper()

	var got string

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT content FROM chunks WHERE id = $1`, id).Scan(&got)
	if err != nil {
		t.Fatalf("id %d の本文を読めない: %v", id, err)
	}

	return got
}

// TestPutReplacesRowWithSameExternalID は同じ外部 id の再投入が置き換えになることを見る。
//
// 🔴 返る id は既存行の id である。新しく採番すると、Corpus 側が覚えている
// chunk_id が指す行が消え、参照が切れる（ADR 0020 Decision 1）。
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

// TestPutKeepsInputOrderWhenReplacing は置き換えが混ざっても id の順序が保たれることを見る。
//
// 🔴 「入力と同じ順の id を返す」は ADR 0013 の写像が依存している契約である。
// 一部が upsert（既存 id）で一部が新規採番になっても、順序は入力に従う。
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

	// 順序を入れ替え、間に外部 id を持たない新規を挟んで投入し直す。
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
//
// 🔴 後勝ちで飲まないこと。飲むと DB は成功し、返る id の列も「入力と同じ順」を
// 保ったまま同じ id を2回並べる。2件受理されたのに行は1件、という差がどこにも
// 現れない状態になる。
func TestPutRejectsDuplicateExternalIDInOneBatch(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	spec := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文"}
	other := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 1, content: "別の本文"}

	_, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{
		externalChunk(t, spec, 77), externalChunk(t, other, 77),
	})
	if !errors.Is(err, postgres.ErrDuplicateExternalID()) {
		t.Fatalf("err = %v, want ErrDuplicateExternalID", err)
	}

	if got := countChunks(t, ts, orgA); got != 0 {
		t.Errorf("拒否したはずの行が入っている。行数 = %d, want 0", got)
	}
}

// TestPutRejectsNonPositiveExternalID は 0 と負値を拒否することを見る。
//
// 「外部 id を持たない」は NULL で表す。0 を通すと、置き換えの鍵が実在しない
// 0 番になり、外部 id を持たないはずの行どうしが互いを上書きする。
func TestPutRejectsNonPositiveExternalID(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	spec := chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文"}

	for name, value := range map[string]int64{"0": 0, "負値": -1} {
		t.Run(name, func(t *testing.T) {
			_, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{externalChunk(t, spec, value)})
			if !errors.Is(err, postgres.ErrExternalIDInvalid()) {
				t.Fatalf("err = %v, want ErrExternalIDInvalid", err)
			}
		})
	}
}

// TestSameExternalIDInDifferentOrgsAreDistinctRows は外部 id が org を跨がないことを見る。
//
// 🔴 一意制約が (org_id, external_id) ではなく external_id 単独だと、別テナントの
// 取り込みが互いを上書きする。単一テナントで開発している限り一切症状が出ない
// 種類の欠陥である（CLAUDE.md 地雷1）。
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
// 🔴 0 を入れないこと。UNIQUE (org_id, external_id) は NULL どうしを重複と
// みなさないが、0 どうしは重複とみなす。0 を入れると単体運用の2件目が
// 1件目を上書きする。
func TestPutStoresNullWhenExternalIDIsAbsent(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	ids, err := ts.store.Put(t.Context(), orgA, threeChunks(t))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	for i, id := range ids {
		if got := externalIDOf(t, ts, id); got != nil {
			t.Errorf("ids[%d] の external_id = %d, want NULL", i, *got)
		}
	}

	if got := countChunks(t, ts, orgA); got != 3 {
		t.Errorf("🔴 行数 = %d, want 3（external_id なしの行が互いを上書きしている）", got)
	}
}

// TestSearchReturnsExternalID は検索結果に外部 id が載ることを見る。
//
// Corpus はこれで自分の chunks を引き直し、soft delete の生存確認を掛ける
// （ADR 0020 Decision 6）。載っていないと、その二段フィルタが成立しない。
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
// 「同じ番号の別テナントの文書」が一緒に消える。しかも消えた側は何も報告されない。
func TestDeleteByDocumentDoesNotCrossOrgs(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	putInDocument(t, ts, orgA, 100)
	putInDocument(t, ts, orgB, 100)

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
//
// 空の結果でも「該当なし」でもなく error にする、というのが ADR 0003 の要求である。
func TestDeleteByDocumentRequiresOrgID(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	var zeroOrg org.ID

	if _, err := ts.store.DeleteByDocument(t.Context(), zeroOrg, 1); !errors.Is(err, postgres.ErrOrgRequired()) {
		t.Fatalf("err = %v, want ErrOrgRequired", err)
	}
}

// putInDocument は指定の文書に1件だけ投入する。
func putInDocument(t *testing.T, ts *testStore, orgID org.ID, documentID int64) {
	t.Helper()

	_, err := ts.store.Put(t.Context(), orgID, []chunk.Chunk{newChunk(chunkSpec{
		orgID: orgID, documentID: documentID, sourceID: 1, chunkIndex: 0, content: "本文",
	})})
	if err != nil {
		t.Fatalf("Put(org=%s, document=%d): %v", orgID, documentID, err)
	}
}
