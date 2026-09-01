package postgres_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// threeChunks は同じ文書に属する3件を返す。
func threeChunks(t *testing.T) []chunk.Chunk {
	t.Helper()

	orgA := mustOrgID(t, 1)
	out := make([]chunk.Chunk, 0, 3)

	for i, text := range []string{"一つ目", "二つ目", "三つ目"} {
		out = append(out, newChunk(chunkSpec{
			orgID: orgA, documentID: 7, sourceID: 70,
			chunkIndex: i, content: text,
		}))
	}

	return out
}

// TestPutReturnsIDsInInputOrder は採番が入力順で返ることを見る。
//
// 順序がずれると、呼び出し側が id と本文を取り違えたまま気づけない。
func TestPutReturnsIDsInInputOrder(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	ids, err := ts.store.Put(t.Context(), orgA, threeChunks(t))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if len(ids) != 3 {
		t.Fatalf("採番数 = %d, want 3", len(ids))
	}

	for i, want := range []string{"一つ目", "二つ目", "三つ目"} {
		var got string

		err := ts.db.QueryRowContext(t.Context(),
			`SELECT content FROM chunks WHERE id = $1`, ids[i]).Scan(&got)
		if err != nil {
			t.Fatalf("id %d を読めない: %v", ids[i], err)
		}

		if got != want {
			t.Errorf("ids[%d] の本文 = %q, want %q（採番の順序がずれている）", i, got, want)
		}
	}
}

// TestPutIsAtomic は途中で失敗したら全件が残らないことを見る。
//
// 2件目の chunk_index を負にして DDL の CHECK を踏ませる。部分投入を許すと、
// 呼び出し側は「どこまで入ったか」を知る手段が無く、再送で重複が生まれる。
// insert-only（UNIQUE なし）なので、その重複は後から見分けられない。
func TestPutIsAtomic(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	chunks := threeChunks(t)
	chunks[1].ChunkIndex = -1

	if _, err := ts.store.Put(t.Context(), orgA, chunks); err == nil {
		t.Fatal("CHECK 違反のはずが成功した")
	}

	if got := countChunks(t, ts, orgA); got != 0 {
		t.Errorf("🔴 部分投入が残った。行数 = %d, want 0", got)
	}
}

// TestPutRejectsInvalidBatches は DB に触れずに分かる誤りが弾かれることを見る。
func TestPutRejectsInvalidBatches(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	withID := newChunk(chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文",
	})
	withID.ID = 42

	otherOrg := newChunk(chunkSpec{
		orgID: orgB, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文",
	})

	empty := newChunk(chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "",
	})

	cases := []struct {
		name string
		in   []chunk.Chunk
		want error
	}{
		{name: "空バッチ", in: nil, want: postgres.ErrEmptyBatch()},
		{name: "明示 id", in: []chunk.Chunk{withID}, want: postgres.ErrChunkIDNotAccepted()},
		{name: "org 食い違い", in: []chunk.Chunk{otherOrg}, want: postgres.ErrOrgMismatch()},
		{name: "本文が空", in: []chunk.Chunk{empty}, want: postgres.ErrEmptyContent()},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ts.store.Put(t.Context(), orgA, c.in)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// TestPutRejectsUnnormalizedVectors は Embedder の契約違反が書き込みを止めることを見る。
//
// 通してしまうと <#> の順位が静かに狂う。落ちるのが正しい振る舞いである。
func TestPutRejectsUnnormalizedVectors(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.scale = 2 // ノルム 2 のベクトルを返す契約違反の実装
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	_, err := ts.store.Put(t.Context(), orgA, threeChunks(t))
	if !errors.Is(err, postgres.ErrVectorNotNormalized()) {
		t.Fatalf("err = %v, want 正規化違反", err)
	}

	if got := countChunks(t, ts, orgA); got != 0 {
		t.Errorf("契約違反のベクトルが書き込まれた。行数 = %d, want 0", got)
	}
}

// TestPutUsesDocumentKind は取り込み時の Kind を確かめる。
//
// bge-m3 は Kind を無視するが、multilingual-e5 は接頭辞が必須で、Voyage は
// input_type として送る。使うかどうかは実装の都合であって、渡すかどうかは
// 呼び出し側の契約である（ADR 0008・CLAUDE.md 地雷3）。
func TestPutUsesDocumentKind(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	ts := newTestStore(t, e)

	if _, err := ts.store.Put(t.Context(), mustOrgID(t, 1), threeChunks(t)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got := *e.kinds
	if len(got) != 1 || got[0] != embed.KindDocument {
		t.Errorf("渡された Kind = %v, want [%s]", got, embed.KindDocument)
	}
}

// TestPutRejectsMismatchedEmbedder は別モデルでの追記を拒否することを見る。
func TestPutRejectsMismatchedEmbedder(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake-a:1024"))
	orgA := mustOrgID(t, 1)

	putOne(t, ts, orgA, "先に入れた本文")

	other := attachStore(t, newFakeEmbedder("fake-b:1024"))

	_, err := other.Put(t.Context(), orgA, threeChunks(t))
	if !errors.Is(err, index.ErrEmbedderMismatch) {
		t.Fatalf("err = %v, want index.ErrEmbedderMismatch", err)
	}
}

// TestDeleteSucceedsWhenNothingMatches は対象ゼロが成功であることを見る。
//
// 削除の意味は「その状態にすること」なので、既にその状態なら要求は満たされている。
func TestDeleteSucceedsWhenNothingMatches(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	if err := ts.store.Delete(t.Context(), mustOrgID(t, 1), 999); err != nil {
		t.Errorf("存在しない id の削除が失敗した: %v", err)
	}

	deleted, err := ts.store.DeleteBySource(t.Context(), mustOrgID(t, 1), 999)
	if err != nil {
		t.Errorf("存在しない source の削除が失敗した: %v", err)
	}

	if deleted != 0 {
		t.Errorf("消えた件数 = %d, want 0", deleted)
	}
}

// TestProviderUnavailablePropagates は埋め込み側の sentinel が連鎖に残ることを見る。
//
// 🔴 これが切れていると httpapi は 503 に写せず 500 を返す。呼び出し側から見ると
// 「Ollama を起動し忘れた」が「サーバ内部エラー」になり、原因に辿り着けない。
// ストア内部の error（errWrite / errSearch）で包んでもなお、埋め込み側の
// sentinel が errors.Is で見えることを Put と Search の両方で確かめる。
func TestProviderUnavailablePropagates(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.angles["問い"] = 0
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	// プロバイダが落ちた状態にする。
	e.unavailable = true

	t.Run("Put", func(t *testing.T) {
		_, err := ts.store.Put(t.Context(), orgA, threeChunks(t))
		if !errors.Is(err, embed.ErrProviderUnavailable) {
			t.Errorf("err = %v, want embed.ErrProviderUnavailable", err)
		}
	})

	t.Run("Search", func(t *testing.T) {
		_, err := ts.store.Search(t.Context(), newQuery(querySpec{
			orgID: orgA, text: "問い", limit: 10, alpha: 1,
			documentIDs: nil, sourceIDs: nil,
		}))
		if !errors.Is(err, embed.ErrProviderUnavailable) {
			t.Errorf("err = %v, want embed.ErrProviderUnavailable", err)
		}
	})
}
