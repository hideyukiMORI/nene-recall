package postgres_test

import (
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// 🔴 このファイルは ADR 0003 の追随作業「分離のテストを Phase 1 の最初に書く」
// に対応する。org_id の取り違えは静かに情報漏洩になり、単一テナントで開発・
// テストしている限り一切症状を出さない。だから最初に書く。
//
// ここで使う2つの org は document_id も source_id も同じ値にしてある。
// 分離が効いているかどうかだけが結果を分ける状況を作るためで、
// 別の値にすると「たまたま当たらなかった」だけで通ってしまう。

// putOne は1件だけ投入して採番された id を返す。
func putOne(t *testing.T, ts *testStore, orgID org.ID, content string) int64 {
	t.Helper()

	ids, err := ts.store.Put(t.Context(), orgID, []chunk.Chunk{
		newChunk(chunkSpec{
			orgID: orgID, documentID: 10, sourceID: 100,
			chunkIndex: 0, content: content,
		}),
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if len(ids) != 1 {
		t.Fatalf("採番された id の数 = %d, want 1", len(ids))
	}

	return ids[0]
}

// TestDeleteDoesNotCrossOrgs は、別 org の id を指定しても消えないことを見る。
//
// id を知っていることを権限の代わりにしない。連番の id は推測できるので、
// org_id を条件から落とすと「他人の id を当てれば消せる」になる。
func TestDeleteDoesNotCrossOrgs(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	putOne(t, ts, orgA, "A の本文")
	idB := putOne(t, ts, orgB, "B の本文")

	// A の権限で B の id を消しにいく。成功するが、何も消えてはならない。
	if err := ts.store.Delete(t.Context(), orgA, idB); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if got := countChunks(t, ts, orgB); got != 1 {
		t.Errorf("🔴 org A の削除が org B の行に届いた。B の残り = %d, want 1", got)
	}

	if got := countChunks(t, ts, orgA); got != 1 {
		t.Errorf("org A の行が巻き添えで消えた。残り = %d, want 1", got)
	}
}

// TestDeleteBySourceDoesNotCrossOrgs は source 単位の削除が org を越えないことを見る。
//
// 再取り込みは DeleteBySource → Put の2手順なので、ここが漏れると
// 「自分の再取り込みが他テナントの文書を消す」になる。
func TestDeleteBySourceDoesNotCrossOrgs(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	putOne(t, ts, orgA, "A の本文")
	putOne(t, ts, orgB, "B の本文")

	// 両者の source_id は同じ 100。org_id だけが結果を分ける。
	deleted, err := ts.store.DeleteBySource(t.Context(), orgA, 100)
	if err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}

	if deleted != 1 {
		t.Errorf("消えた件数 = %d, want 1（org B のぶんまで数えていないか）", deleted)
	}

	if got := countChunks(t, ts, orgB); got != 1 {
		t.Errorf("🔴 org A の一括削除が org B の行に届いた。B の残り = %d, want 1", got)
	}

	if got := countChunks(t, ts, orgA); got != 0 {
		t.Errorf("org A の行が残った。残り = %d, want 0", got)
	}
}

// TestPutStoresTheArgumentOrg は、書き込まれる org が引数のものだけであることを見る。
func TestPutStoresTheArgumentOrg(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	putOne(t, ts, orgA, "A の本文")

	if got := countChunks(t, ts, orgB); got != 0 {
		t.Errorf("🔴 書いていない org B に行がある: %d", got)
	}
}
