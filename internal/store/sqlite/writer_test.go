package sqlite_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// TestPutReturnsIDsInInputOrder は、返る id が入力と同じ順であることを見る。
//
// 🔴 これは ADR 0013 が評価ハーネスの土台に据えた契約である。eval_key から
// 採番 id への写像は Put の戻り値の順序だけで作られる。順序が入れ替わると、
// 評価は静かに別の行を正解として数え始め、症状は「recall が変」だけになる。
func TestPutReturnsIDsInInputOrder(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	contents := []string{"一つ目", "二つ目", "三つ目"}
	chunks := make([]chunk.Chunk, 0, len(contents))

	for i, content := range contents {
		chunks = append(chunks, newChunk(chunkSpec{
			orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: i, content: content,
		}))
	}

	ids, err := ts.store.Put(t.Context(), orgA, chunks)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if len(ids) != len(contents) {
		t.Fatalf("id の数 = %d, want %d", len(ids), len(contents))
	}

	for i, id := range ids {
		var got string
		if err := ts.db.QueryRowContext(t.Context(),
			`SELECT content FROM chunks WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("本文を読めない: %v", err)
		}

		if got != contents[i] {
			t.Errorf("🔴 ids[%d] が指す本文 = %q, want %q（順序が入力とずれている）",
				i, got, contents[i])
		}
	}
}

// TestPutDoesNotReuseIDsAfterDelete は、削除した id が再利用されないことを見る。
//
// 🔴 これが AUTOINCREMENT の検証である (ADR 0017 Decision 5)。素の
// INTEGER PRIMARY KEY は rowid の別名で、SQLite は「現在の最大値 + 1」を
// 割り当てる——つまり最大 id の行を消すと、次の挿入がその id を再び使う。
// 評価ハーネスの写像 (ADR 0013) は id が一意な履歴を持つことに依存しているので、
// 再利用が起きると注釈が静かに別の行を指す。
//
// 🔑 postgres 側に対応するテストが無いのは、あちらの IDENTITY が構造上
// 再利用しないからである。SQLite では DDL の1語で挙動が変わるので、機械で守る。
func TestPutDoesNotReuseIDsAfterDelete(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	first := putOne(t, ts, orgA, "最初の本文")

	if err := ts.store.Delete(t.Context(), orgA, first); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	second := putOne(t, ts, orgA, "次の本文")

	if second == first {
		t.Errorf("🔴 削除した id %d が再利用された。AUTOINCREMENT が外れていないか", first)
	}
}

// TestPutRejectsInvalidInput は、DB に触れずに分かる誤りを Put が弾くことを見る。
func TestPutRejectsInvalidInput(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)
	orgB := mustOrgID(t, 2)

	cases := []struct {
		name   string
		chunks []chunk.Chunk
		want   error
	}{
		{
			name:   "空のバッチ",
			chunks: []chunk.Chunk{},
			want:   sqlite.ErrEmptyBatch(),
		},
		{
			name: "空の本文",
			chunks: []chunk.Chunk{newChunk(chunkSpec{
				orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "",
			})},
			want: sqlite.ErrEmptyContent(),
		},
		{
			name: "引数と食い違う org",
			chunks: []chunk.Chunk{newChunk(chunkSpec{
				orgID: orgB, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文",
			})},
			want: sqlite.ErrOrgMismatch(),
		},
		{
			name:   "明示的な chunk id",
			chunks: []chunk.Chunk{chunkWithID(orgA, 42)},
			want:   sqlite.ErrChunkIDNotAccepted(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ts.store.Put(t.Context(), orgA, tc.chunks)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// chunkWithID は明示的な id を持つチャンクを作る。
//
// newChunk が id をゼロ値に固定しているので、拒否されることを確かめるには
// ここで意図的に作る（QLT-006 が求める「意図的な違反」）。
func chunkWithID(orgID org.ID, id int64) chunk.Chunk {
	c := newChunk(chunkSpec{
		orgID: orgID, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文",
	})
	c.ID = id

	return c
}

// TestPutRejectsUnnormalizedVectors は、契約を破る Embedder を通さないことを見る。
//
// 🔴 順位付けの内積は入力が正規化済みであることに依存している。見逃すと
// 「エラーにならないまま順位が静かに狂う」形になる（ADR 0005 の罠と同じ）。
func TestPutRejectsUnnormalizedVectors(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.scale = 2 // 長さ 2 のベクトルを返す実装にする

	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	_, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{newChunk(chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文",
	})})
	if !errors.Is(err, sqlite.ErrVectorNotNormalized()) {
		t.Errorf("err = %v, want ErrVectorNotNormalized", err)
	}
}

// TestPutPropagatesProviderUnavailable は、埋め込み側の sentinel が連鎖に
// 残ることを見る。
//
// 🔴 ここを %s（文字列化）にすると embed.ErrProviderUnavailable が切れ、
// httpapi が 503 に写せず 500 に落ちる。写像は連鎖の上に成り立っている。
func TestPutPropagatesProviderUnavailable(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	e.unavailable = true

	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	_, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{newChunk(chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文",
	})})
	if !errors.Is(err, embed.ErrProviderUnavailable) {
		t.Errorf("err = %v, want embed.ErrProviderUnavailable", err)
	}
}

// TestPutRejectsTokensThatBreakTheContract は、契約を破る Tokenizer を
// 通さないことを見る。
//
// 空白を含むトークンは lexeme_text の中で勝手に割れ、引用符を含むトークンは
// MATCH 式の囲みを破る。どちらも黙って進むと「語彙スコアが少し変」あるいは
// 「検索が失敗する」になる。前者に気づける人はいない。
func TestPutRejectsTokensThatBreakTheContract(t *testing.T) {
	cases := []struct {
		name   string
		tokens []string
		want   error
	}{
		{name: "空白を含む", tokens: []string{"two words"}, want: sqlite.ErrTokenHasWhitespace()},
		{name: "引用符を含む", tokens: []string{`a"b`}, want: sqlite.ErrTokenHasMetaCharacter()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestStoreWith(t, storeSpec{
				embedder:  newFakeEmbedder("fake:1024"),
				tokenizer: brokenTokenizer{tokens: tc.tokens},
			})
			orgA := mustOrgID(t, 1)

			_, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{newChunk(chunkSpec{
				orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "本文",
			})})
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestPutIsAtomic は、途中で失敗した一括投入が1行も残さないことを見る。
//
// insert-only（UNIQUE 制約なし）なので、部分投入を許すと重複を後から
// 見分けられない。
func TestPutIsAtomic(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	// chunk_index が負の行を混ぜる。CHECK (chunk_index >= 0) が2件目で落ちる。
	_, err := ts.store.Put(t.Context(), orgA, []chunk.Chunk{
		newChunk(chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "先頭"}),
		newChunk(chunkSpec{orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: -1, content: "壊れた行"}),
	})
	if err == nil {
		t.Fatalf("CHECK 制約に掛かるはずの投入が成功した")
	}

	if got := countChunks(t, ts, orgA); got != 0 {
		t.Errorf("🔴 失敗した一括投入が %d 行残した。want 0（原子性が壊れている）", got)
	}
}

// TestDeleteBySourceRemovesFTSEntries は、削除した行が語彙検索に当たらなく
// なることを見る。
//
// 🔴 FTS5 の外部コンテンツ表は自分では同期しない。delete の trigger を欠くと、
// 消したはずの行の転置索引が残り、bm25 が幽霊の rowid を返す。候補集合との
// 突き合わせで結果には出ないので、症状は「語彙スコアがどこにも付かない」あるいは
// 「索引が壊れている」という遅れた形でしか出ない。
func TestDeleteBySourceRemovesFTSEntries(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	putOne(t, ts, orgA, "alpha bravo")

	if got := ftsMatches(t, ts, `"alpha"`); got != 1 {
		t.Fatalf("投入直後の FTS の一致件数 = %d, want 1（insert の trigger が効いていない）", got)
	}

	if _, err := ts.store.DeleteBySource(t.Context(), orgA, 100); err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}

	if got := ftsMatches(t, ts, `"alpha"`); got != 0 {
		t.Errorf("🔴 削除後も FTS が %d 件一致した。want 0（delete の trigger が欠けている）", got)
	}
}

// ftsMatches は FTS5 の表に直接問い合わせて一致件数を返す。
func ftsMatches(t *testing.T, ts *testStore, expression string) int {
	t.Helper()

	var n int

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH ?`, expression).Scan(&n)
	if err != nil {
		t.Fatalf("FTS へ問い合わせられない: %v", err)
	}

	return n
}

// TestMismatchedEmbedderAndTokenizerAreRejected は、別のモデル・別の分割器で
// 保存された行が黙って無視されないことを見る。
//
// 🔴 「検索に出てこないだけ」にしない。次元が同じでも別モデルのベクトルは
// 比較できず、規則の違うトークン列は語彙スコアを 0 にする。どちらも
// エラーにならなければ表面化しない (ADR 0005)。
func TestMismatchedEmbedderAndTokenizerAreRejected(t *testing.T) {
	cases := []struct {
		name string
		spec storeSpec
		want error
	}{
		{
			name: "別の Embedder",
			spec: storeSpec{
				embedder:  newFakeEmbedder("другой:1024"),
				tokenizer: newFakeTokenizer("fake-tokenizer:1"),
			},
			want: index.ErrEmbedderMismatch,
		},
		{
			name: "別の Tokenizer",
			spec: storeSpec{
				embedder:  newFakeEmbedder("fake:1024"),
				tokenizer: newFakeTokenizer("fake-tokenizer:2"),
			},
			want: index.ErrTokenizerMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestStore(t, newFakeEmbedder("fake:1024"))
			orgA := mustOrgID(t, 1)
			putOne(t, ts, orgA, "既に入っている本文")

			switched := attachStore(t, ts, tc.spec)

			_, err := switched.Search(t.Context(), newQuery(querySpec{
				orgID: orgA, text: "問い", limit: 10, alpha: 1,
				documentIDs: nil, sourceIDs: nil,
			}))
			if !errors.Is(err, tc.want) {
				t.Errorf("Search の err = %v, want %v", err, tc.want)
			}

			_, err = switched.Put(t.Context(), orgA, []chunk.Chunk{newChunk(chunkSpec{
				orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 1, content: "追記",
			})})
			if !errors.Is(err, tc.want) {
				t.Errorf("Put の err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestPutPassesTheRightKind は、取り込みと検索で Kind を使い分けていることを見る。
//
// 🔴 bge-m3 は Kind を無視するが、multilingual-e5 は接頭辞が変わり、Voyage は
// input_type が変わる。使うかどうかは実装の都合で、渡すかどうかは呼び出し側の
// 契約である（CLAUDE.md 地雷3・ADR 0008）。
func TestPutPassesTheRightKind(t *testing.T) {
	e := newFakeEmbedder("fake:1024")
	ts := newTestStore(t, e)
	orgA := mustOrgID(t, 1)

	putOne(t, ts, orgA, "本文")

	if _, err := ts.store.Search(t.Context(), newQuery(querySpec{
		orgID: orgA, text: "問い", limit: 10, alpha: 1,
		documentIDs: nil, sourceIDs: nil,
	})); err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := []embed.Kind{embed.KindDocument, embed.KindQuery}
	if got := *e.kinds; !slices.Equal(got, want) {
		t.Errorf("渡された Kind = %v, want %v", got, want)
	}
}
