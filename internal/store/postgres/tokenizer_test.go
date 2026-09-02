package postgres_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// TestPutStoresLexemeTextAndTokenizerID は取り込みが両方の列を書くことを見る。
//
// 🔴 lexemes（tsvector）は生成列なので書かない。DB が lexeme_text から導出する。
// アプリケーションが両方を書く形にすると、片方だけ更新された行が作れてしまう。
func TestPutStoresLexemeTextAndTokenizerID(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	id := putOne(t, ts, orgA, "Alpha BETA gamma")

	var lexemeText, tokenizerID, lexemes string

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT lexeme_text, tokenizer_id, lexemes::text FROM chunks WHERE id = $1`, id).
		Scan(&lexemeText, &tokenizerID, &lexemes)
	if err != nil {
		t.Fatalf("列を読めない: %v", err)
	}

	if lexemeText != "alpha beta gamma" {
		t.Errorf("lexeme_text = %q", lexemeText)
	}

	if tokenizerID != "fake-tokenizer:1" {
		t.Errorf("tokenizer_id = %q", tokenizerID)
	}

	if lexemes == "" {
		t.Errorf("生成列 lexemes が空である（lexeme_text から導出されていない）")
	}
}

// TestPutRejectsMismatchedTokenizer は別の分割規則での追記を拒否することを見る。
//
// 🔴 これが無いと、規則の違うトークン列が同じ列に混ざる。症状は「語彙スコアが
// 少し低い」だけで、エラーにならない。ADR 0005 が埋め込みについて記録した罠と
// まったく同じ形である。
func TestPutRejectsMismatchedTokenizer(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	putOne(t, ts, orgA, "先に入れた本文")

	other := attachStoreWith(t, tokenizedSpec(newFakeTokenizer("fake-tokenizer:2")))

	_, err := other.Put(t.Context(), orgA, threeChunks(t))
	if !errors.Is(err, index.ErrTokenizerMismatch) {
		t.Fatalf("err = %v, want index.ErrTokenizerMismatch", err)
	}
}

// TestSearchRejectsMismatchedTokenizer は検索側でも不一致を表面化させることを見る。
//
// 🔴 WHERE tokenizer_id = $current で黙って絞り込む実装にしないこと。不一致の行が
// 「検索に出てこないだけ」になり、静かな破損の変種になる。
func TestSearchRejectsMismatchedTokenizer(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))
	orgA := mustOrgID(t, 1)

	putOne(t, ts, orgA, "先に入れた本文")

	other := attachStoreWith(t, tokenizedSpec(newFakeTokenizer("fake-tokenizer:2")))

	_, err := other.Search(t.Context(), index.Query{
		OrgID: orgA, Text: "本文", Limit: 10, Alpha: 0.7,
		DocumentIDs: nil, SourceIDs: nil,
	})
	if !errors.Is(err, index.ErrTokenizerMismatch) {
		t.Fatalf("err = %v, want index.ErrTokenizerMismatch", err)
	}
}

// TestEmbedderMismatchIsReportedBeforeTokenizerMismatch は、両方が食い違う行に
// 対して埋め込み側を先に報告することを見る。
//
// 2つの検査を1本の SQL にまとめてあるので、どちらを報告するかは実装の選択に
// なる。ベクトルが比較できない状態のほうが被害が大きく、取り込み直しの判断も
// そちらが決めるので、埋め込みを先にする。この順序を固定しておかないと、
// 将来 SQL を書き換えた人が気づかずに入れ替えられる。
func TestEmbedderMismatchIsReportedBeforeTokenizerMismatch(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake-a:1024"))
	orgA := mustOrgID(t, 1)

	putOne(t, ts, orgA, "先に入れた本文")

	otherSpec := tokenizedSpec(newFakeTokenizer("fake-tokenizer:2"))
	otherSpec.embedder = newFakeEmbedder("fake-b:1024")

	other := attachStoreWith(t, otherSpec)

	_, err := other.Put(t.Context(), orgA, threeChunks(t))
	if !errors.Is(err, index.ErrEmbedderMismatch) {
		t.Fatalf("err = %v, want index.ErrEmbedderMismatch", err)
	}
}

// TestPutRejectsTokensThatBreakTheContract は契約違反のトークンを拒否することを見る。
//
// 🔴 黙って落とす実装にしない。落とすと語彙スコアが静かに欠けたまま「動いて
// いる」状態になり、分割器の不具合が誰にも見えなくなる。要件定義 Q-2 が未決で
// ある以上、分割器が差し替わる機会は将来に確実にある。
func TestPutRejectsTokensThatBreakTheContract(t *testing.T) {
	cases := []struct {
		name   string
		tokens []string
		want   error
	}{
		{
			name:   "空白を含むトークン",
			tokens: []string{"ok", "not ok"},
			want:   postgres.ErrTokenHasWhitespace(),
		},
		{
			name:   "tsquery のメタ文字を含むトークン",
			tokens: []string{"ok", "a|b"},
			want:   postgres.ErrTokenHasMetaCharacter(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestStoreWith(t, tokenizedSpec(brokenTokenizer{tokens: tc.tokens}))
			orgA := mustOrgID(t, 1)

			_, err := ts.store.Put(t.Context(), orgA, threeChunks(t))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}

			if !errors.Is(err, postgres.ErrTokenInvalid()) {
				t.Errorf("err が errTokenInvalid を包んでいない: %v", err)
			}

			if got := countChunks(t, ts, orgA); got != 0 {
				t.Errorf("契約違反なのに %d 行が入った", got)
			}
		})
	}
}
