package sqlite_test

import (
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/lexical"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/kagome"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/union"
	"github.com/hideyukiMORI/nene-recall/internal/store/sqlite"
)

// 🔴 本ファイルは往復同一性のテストである。
//
// 確かめるのは「Go の分割出力 → lexeme_text → FTS5 の ascii トークナイザ →
// トークン」という往復が、期待どおりの形で閉じているかである。
//
// なぜ要るか。ascii トークナイザは ASCII の記号で再分割するので、Go 側の前処理と
// ずれると「取り込みと検索で同じ関数を通しているのに FTS5 の中では別のトークン」
// という壊れ方をする。この壊れ方は例外を出さない——語彙スコアが 0 になるだけで、
// 検索は成功し、結果も返る。偽の分割器では絶対に検出できないので、実物の
// 分割器 (internal/lexical/bigram) と実 SQLite に対して確かめる。
//
// 🔑 postgres 側の lexeme_roundtrip_test.go と対になる。あちらは
// to_tsvector('simple') の再パース、こちらは FTS5 の ascii トークナイザという、
// 別の関数に対する同じ観点である。
//
// 🔴 往復同一性は分割器ごとに確かめる。ADR 0018 で分割器が2つになった以上、
// 片方だけで往復を見ても、もう片方が ascii トークナイザと噛み合っているかは
// 分からない。ADR 0021 の和集合 (internal/lexical/union) も同じ理由で回す。
// 和集合は同じ語を2回並べるので、FTS5 の位置情報とトークン頻度が構成要素の
// どちらとも違う——構成要素が閉じていることは、和集合が閉じる根拠にならない。

// roundTripTokenizer は往復同一性を確かめる分割器1つ。
type roundTripTokenizer struct {
	name      string
	tokenizer lexical.Tokenizer
}

// roundTripTokenizers は実物の分割器をすべて返す。
func roundTripTokenizers(t *testing.T) []roundTripTokenizer {
	t.Helper()

	morphological, err := kagome.New()
	if err != nil {
		t.Fatalf("kagome.New(): %v", err)
	}

	both, err := union.New()
	if err != nil {
		t.Fatalf("union.New(): %v", err)
	}

	return []roundTripTokenizer{
		{name: "bigram", tokenizer: bigram.New()},
		{name: "kagome", tokenizer: morphological},
		{name: "union", tokenizer: both},
	}
}

// TestLexemeRoundTripMatchesItsOwnQuery は、取り込んだ本文が、同じ分割器で
// 作った検索式に必ず一致することを確かめる。
//
// 🔑 これが往復同一性の本体である。「索引側と検索側が噛み合うか」は、片側だけ
// 囲みを外すといった変更で静かに壊れる。本文で引ける、を直接見る。
func TestLexemeRoundTripMatchesItsOwnQuery(t *testing.T) {
	contents := []string{
		"RECALL_STORE は postgres と sqlite を切り替える",
		"pgvector 0.8.6 で測った",
		"PdoChunkSearchRepository.php が現行の実装である",
		"埋め込みは bge-m3 をローカルで動かす",
		"ハイブリッド検索はベクトル類似度と語彙一致を合成する",
		"POST /v1/search で検索する",
		"サーバーの応答時間を測る",
	}

	for _, tc := range roundTripTokenizers(t) {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestStoreWith(t, storeSpec{
				embedder:  newFakeEmbedder("fake:1024"),
				tokenizer: tc.tokenizer,
			})

			for i, content := range contents {
				assertMatchesItsOwnQuery(t, ts, tc.tokenizer, chunkSpec{
					orgID: mustOrgID(t, 1), documentID: 1, sourceID: 1,
					chunkIndex: i, content: content,
				})
			}
		})
	}
}

// assertMatchesItsOwnQuery は本文1件を取り込み、同じ分割器で作った検索式で
// 引けることを確かめる。
func assertMatchesItsOwnQuery(
	t *testing.T, ts *testStore, tokenizer lexical.Tokenizer, spec chunkSpec,
) {
	t.Helper()

	id := putContent(t, ts, spec)

	expression, err := sqlite.EncodeMatchExpression(tokenizer.Tokenize(spec.content))
	if err != nil {
		t.Fatalf("EncodeMatchExpression: %v", err)
	}

	if !matchesQuery(t, ts, id, expression) {
		t.Errorf("本文 %q が自分自身から作った検索式に一致しない（往復が閉じていない）", spec.content)
	}
}

// TestLexemeRoundTripKeepsMorphemesIntact は、Go が切った形態素が FTS5 側で
// 再分割されないことを確かめる。
//
// 🔑 tokenize='ascii' を選んだ理由 (ADR 0017 Decision 3) は bigram だけの
// 都合ではない。unicode61 なら「切り替える」も日本語の分類で割られ、原形に
// 畳んだ意味が消える。分割器が増えた以上、両方で見る。
func TestLexemeRoundTripKeepsMorphemesIntact(t *testing.T) {
	morphological, err := kagome.New()
	if err != nil {
		t.Fatalf("kagome.New(): %v", err)
	}

	ts := newTestStoreWith(t, storeSpec{
		embedder:  newFakeEmbedder("fake:1024"),
		tokenizer: morphological,
	})
	orgA := mustOrgID(t, 1)

	id := putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "検索対象を切り替える",
	})

	for _, token := range morphological.Tokenize("検索対象を切り替える") {
		if !matchesQuery(t, ts, id, `"`+token+`"`) {
			t.Errorf("🔴 形態素 %q が FTS5 の中で再分割されている", token)
		}
	}

	// 「索」1文字だけでは当たらない。当たるなら文字単位に割れている。
	if matchesQuery(t, ts, id, `"索"`) {
		t.Errorf("🔴 CJK が1文字ずつに割れている（ascii トークナイザが効いていない）")
	}
}

// TestLexemeRoundTripKeepsCJKBigramsIntact は、Go が切った CJK の bigram が
// FTS5 側で再分割されないことを確かめる。
//
// 🔴 これが tokenize='ascii' を選んだ理由そのものである (ADR 0017 Decision 3)。
// unicode61 だと日本語が Unicode 分類で割られ、Go の bigram が壊れる。壊れても
// エラーは出ず、語彙スコアが静かに変わるだけなので、機械で縛る。
func TestLexemeRoundTripKeepsCJKBigramsIntact(t *testing.T) {
	ts := newTestStoreWith(t, storeSpec{
		embedder:  newFakeEmbedder("fake:1024"),
		tokenizer: bigram.New(),
	})
	orgA := mustOrgID(t, 1)

	id := putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1, chunkIndex: 0, content: "検索対象",
	})

	// bigram.Tokenize("検索対象") = ["検索" "索対" "対象"]。3つとも FTS5 の中で
	// 1トークンのまま残っていなければ、この3つの MATCH は当たらない。
	for _, token := range bigram.New().Tokenize("検索対象") {
		if !matchesQuery(t, ts, id, `"`+token+`"`) {
			t.Errorf("🔴 bigram トークン %q が FTS5 の中で再分割されている", token)
		}
	}

	// 「索」1文字だけでは当たらない。当たるなら文字単位に割れている。
	if matchesQuery(t, ts, id, `"索"`) {
		t.Errorf("🔴 CJK が1文字ずつに割れている（ascii トークナイザが効いていない）")
	}
}

// TestLexemeRoundTripKeepsIdentifiersPrecise は、下線を含む識別子が語ごとに
// ばらけて偽ヒットを生まないことを確かめる。
//
// 🔑 ascii トークナイザは RECALL_STORE を recall と store に割るが、検索式側も
// 同じトークナイザを通り、囲みがフレーズ（隣接）を意味するので、"recall" と
// "store" が離れて出てくる本文には一致しない。基準線で全滅した q-003
// （RECALL_STORE・正解4件）を救うのはこの精度であり、ここが緩むと語彙検索は
// 偽ヒットで recall を下げる方向に働く。postgres 側の 'recall' <-> 'store' と
// 同じ性質を、別の方言で確かめている。
//
// 🔴 bigram 限定である。形態素側では同じ囮に当たる——理由と実測は postgres 側の
// TestLexemeRoundTripLosesAdjacencyWithMorphemes にある（機能語を捨てると、
// 離れていた語がトークン列の上で隣接する）。方言が違っても原因は同じである。
func TestLexemeRoundTripKeepsIdentifiersPrecise(t *testing.T) {
	ts := newTestStoreWith(t, storeSpec{
		embedder:  newFakeEmbedder("fake:1024"),
		tokenizer: bigram.New(),
	})
	orgA := mustOrgID(t, 1)

	target := putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1,
		chunkIndex: 0, content: "RECALL_STORE を postgres にする",
	})
	decoy := putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1,
		chunkIndex: 1, content: "recall は store とは別の語である",
	})

	expression, err := sqlite.EncodeMatchExpression(bigram.New().Tokenize("RECALL_STORE"))
	if err != nil {
		t.Fatalf("EncodeMatchExpression: %v", err)
	}

	if !matchesQuery(t, ts, target, expression) {
		t.Errorf("RECALL_STORE を含む本文に一致しない")
	}

	if matchesQuery(t, ts, decoy, expression) {
		t.Errorf("recall と store が離れている本文に一致した（識別子が語ごとにばらけている）")
	}
}

// matchesQuery は保存済みの行が検索式に一致するかを見る。
func matchesQuery(t *testing.T, ts *testStore, id int64, expression string) bool {
	t.Helper()

	var n int

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM chunks_fts WHERE chunks_fts MATCH ? AND rowid = ?`,
		expression, id).Scan(&n)
	if err != nil {
		t.Fatalf("一致判定に失敗した: %v", err)
	}

	return n > 0
}
