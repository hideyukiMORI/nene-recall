package sqlite_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// 🔴 このファイルは ADR 0003 の分離テストである。org_id の取り違えは静かに
// 情報漏洩になり、単一テナントで開発・テストしている限り一切症状を出さない。
// 観点は postgres 側の同名ファイルと同じで、SQL の方言だけが違う。
//
// 🔴 SQLite 版はさらに重い意味を持つ。候補行を Go 側へ読み出してから順位を
// 付ける実装なので、「SQL で絞る」を1行外して「Go で弾く」に変えることが
// 物理的に可能である (ADR 0017 Decision 2)。その変更を落とすのがここである。
//
// ここで使う2つの org は document_id も source_id も同じ値にしてある。
// 分離が効いているかどうかだけが結果を分ける状況を作るためで、
// 別の値にすると「たまたま当たらなかった」だけで通ってしまう。

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

// TestSearchDoesNotCrossOrgs は検索が別 org の行を返さないことを見る。
//
// 🔴 これが分離の中で最も静かに壊れる経路である。書き込みと削除は結果が目に
// 見えるが、検索の漏洩は「余計な結果が混じる」だけなので、単一テナントで
// 開発している限り一切症状を出さない（CLAUDE.md 地雷1）。
func TestSearchDoesNotCrossOrgs(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["A だけの本文"] = 0
	e.angles["B だけの本文"] = 0
	e.angles["問い"] = 0

	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	putOne(t, ts, orgA, "A だけの本文")
	putOne(t, ts, orgB, "B だけの本文")

	// 両者のベクトルは同一（角度 0）。分離が効いていなければ 2 件返る。
	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("🔴 org A の検索が %d 件返した。want 1（org B の行が混じっていないか）", len(results))
	}

	if got := results[0].Chunk.Content; got != "A だけの本文" {
		t.Errorf("🔴 org B の本文が返った: %q", got)
	}

	if got := results[0].Chunk.OrgID; got != orgA {
		t.Errorf("結果の OrgID = %s, want %s", got, orgA)
	}
}

// TestLexicalSearchDoesNotCrossOrgs は語彙側の経路が org を越えないことを見る。
//
// 🔴 ベクトル側と語彙側で候補集合の条件を別々に書けてしまう構造なので、
// 片方だけ分離が効いている状態がありうる。語彙は FTS5 の別の表に対する
// 問い合わせであり、その表は org_id を持たない——rowid の副問い合わせだけが
// 分離を担っている。ここが落ちると、別テナントの行が語彙スコアを受け取る。
func TestLexicalSearchDoesNotCrossOrgs(t *testing.T) {
	e := newFakeEmbedder("fake:1024")

	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	putOne(t, ts, orgA, "alpha bravo")
	putOne(t, ts, orgB, "alpha bravo")

	// alpha=0 で純語彙にする。分離が効いていなければ org B の行も返る。
	results, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "alpha", limit: 10, alpha: 0,
		documentIDs: nil, sourceIDs: nil,
	}))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("🔴 純語彙の検索が %d 件返した。want 1", len(results))
	}

	if results[0].LexicalScore <= 0 {
		t.Errorf("語彙スコアが付いていない: %v（FTS の trigger か MATCH 式が効いていない）",
			results[0].LexicalScore)
	}
}

// TestZeroOrgIsRejectedEverywhere は、未指定の org がどの入口でも error になることを見る。
//
// 🔴 これは QLT-006 が要求する「意図的な違反で検査が発火することの証明」である。
// org.ID のゼロ値は言語仕様上どうやっても作れる（GO-003）ので、ここでは
// 「作れてしまうこと」を前提に、届いた先で必ず落ちることを確かめている。
func TestZeroOrgIsRejectedEverywhere(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	// 「org_id が無いとき」を表す ID は存在しない。テストのためだけに作る。
	var zeroOrg org.ID

	t.Run("Search は空の結果ではなく error を返す", func(t *testing.T) {
		results, err := ts.store.Search(t.Context(), newQuery(querySpec{
			orgID: zeroOrg, text: "問い", limit: 10, alpha: 1,
			documentIDs: nil, sourceIDs: nil,
		}))
		if !errors.Is(err, index.ErrInvalidQuery) {
			t.Fatalf("err = %v, want index.ErrInvalidQuery", err)
		}

		if len(results) != 0 {
			t.Errorf("error と一緒に結果が返っている: %d 件", len(results))
		}
	})

	t.Run("Put", func(t *testing.T) {
		_, err := ts.store.Put(t.Context(), zeroOrg, []chunk.Chunk{
			newChunk(chunkSpec{
				orgID: zeroOrg, documentID: 1, sourceID: 1,
				chunkIndex: 0, content: "本文",
			}),
		})
		if !errors.Is(err, sqlite.ErrOrgRequired()) {
			t.Errorf("err = %v, want ErrOrgRequired", err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		if err := ts.store.Delete(t.Context(), zeroOrg, 1); !errors.Is(err, sqlite.ErrOrgRequired()) {
			t.Errorf("err = %v, want ErrOrgRequired", err)
		}
	})

	t.Run("DeleteBySource", func(t *testing.T) {
		_, err := ts.store.DeleteBySource(t.Context(), zeroOrg, 1)
		if !errors.Is(err, sqlite.ErrOrgRequired()) {
			t.Errorf("err = %v, want ErrOrgRequired", err)
		}
	})
}
