package postgres_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/lexical/bigram"
	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// 🔴 本ファイルは往復同一性のテストである。
//
// 確かめるのは「Go の分割出力 → lexeme_text → to_tsvector('simple', …) →
// レキシーム」という往復が、期待どおりの形で閉じているかである。
//
// なぜ要るか。'simple' パーサは記号で再分割し小文字化するので、Go 側の前処理と
// ずれると「取り込みと検索で同じ関数を通しているのに DB 内では別のレキシーム」
// という壊れ方をする。この壊れ方は例外を出さない——語彙スコアが 0 になるだけで、
// 検索は成功し、結果も返る。単体テスト（偽の DB）では絶対に検出できないので、
// 実 Postgres に対して確かめる。
//
// 🔴 ここだけは実物の分割器 (internal/lexical/bigram) を使う。偽実装で代用すると、
// 検証しているのが偽実装とパーサの噛み合わせになり、何も保証しない。

// TestLexemeRoundTrip は代表的な入力のレキシームを実 Postgres で確かめる。
//
// 期待値は「こうあってほしい」ではなく「実測してこうだった」である。パーサの
// 挙動が版で変われば、この表が落ちて気づける。
func TestLexemeRoundTrip(t *testing.T) {
	ts := newTestStoreWith(t, newFakeEmbedder("fake:1024"), bigram.New())
	orgA := mustOrgID(t, 1)

	for i, tc := range lexemeRoundTripCases() {
		t.Run(tc.name, func(t *testing.T) {
			id := putContent(t, ts, chunkSpec{
				orgID: orgA, documentID: 1, sourceID: 1,
				chunkIndex: i, content: tc.content,
			})

			got := storedLexemes(t, ts, id)

			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)

			if !slices.Equal(got, want) {
				t.Errorf("content %q のレキシーム = %v, want %v", tc.content, got, want)
			}
		})
	}
}

// lexemeRoundTripCase は代表的な入力1件と、実測したレキシーム。
type lexemeRoundTripCase struct {
	name    string
	content string
	want    []string
}

// lexemeRoundTripCases は往復同一性の表を返す。
//
// 期待値は「こうあってほしい」ではなく「実測してこうだった」である。
func lexemeRoundTripCases() []lexemeRoundTripCase {
	return []lexemeRoundTripCase{
		{
			name:    "下線はパーサが割る（Go 側は1トークン）",
			content: "RECALL_STORE",
			want:    []string{"recall", "store"},
		},
		{
			name:    "版番号は1レキシームのまま",
			content: "pgvector 0.8.6",
			want:    []string{"pgvector", "0.8.6"},
		},
		{
			name:    "拡張子は1レキシームのまま",
			content: "PdoChunkSearchRepository.php",
			want:    []string{"pdochunksearchrepository.php"},
		},
		{
			name:    "ハイフン語は複合と部分の3つに展開される",
			content: "bge-m3",
			want:    []string{"bge-m3", "bge", "m3"},
		},
		{
			name:    "斜線は1レキシームのまま",
			content: "v1/search",
			want:    []string{"v1/search"},
		},
		{
			name:    "CJK は Go が切った bigram がそのまま残る",
			content: "検索対象",
			want:    []string{"検索", "索対", "対象"},
		},
		{
			name:    "長音符はレキシームの内側に残る",
			content: "サーバー",
			want:    []string{"サー", "ーバ", "バー"},
		},
		{
			name:    "分割できる語が無ければ空の tsvector になる",
			content: "🔴🔑",
			want:    []string{},
		},
	}
}

// TestLexemeRoundTripMatchesItsOwnQuery は、取り込んだ本文が、同じ分割器で作った
// 検索式に必ず一致することを確かめる。
//
// 🔑 これが往復同一性の本体である。表（TestLexemeRoundTrip）はレキシームの形を
// 固定するが、「索引側と検索側が噛み合うか」は別の性質であり、片側だけ引用符で
// 囲むといった変更で静かに壊れる。本文で引ける、を直接見る。
func TestLexemeRoundTripMatchesItsOwnQuery(t *testing.T) {
	ts := newTestStoreWith(t, newFakeEmbedder("fake:1024"), bigram.New())
	orgA := mustOrgID(t, 1)

	contents := []string{
		"RECALL_STORE は postgres と sqlite を切り替える",
		"pgvector 0.8.6 で測った",
		"PdoChunkSearchRepository.php が現行の実装である",
		"埋め込みは bge-m3 をローカルで動かす",
		"ハイブリッド検索はベクトル類似度と語彙一致を合成する",
		"POST /v1/search で検索する",
	}

	tokenizer := bigram.New()

	for i, content := range contents {
		id := putContent(t, ts, chunkSpec{
			orgID: orgA, documentID: 1, sourceID: 1,
			chunkIndex: i, content: content,
		})

		expression, err := postgres.EncodeTsQuery(tokenizer.Tokenize(content))
		if err != nil {
			t.Fatalf("EncodeTsQuery: %v", err)
		}

		if !matchesQuery(t, ts, id, expression) {
			t.Errorf("本文 %q が自分自身から作った検索式に一致しない（往復が閉じていない）", content)
		}
	}
}

// TestLexemeRoundTripKeepsIdentifiersPrecise は、下線を含む識別子が語ごとに
// ばらけて偽ヒットを生まないことを確かめる。
//
// 🔑 パーサは RECALL_STORE を 'recall' と 'store' に割るが、検索式側も同じ
// パーサを通るので 'recall' <-> 'store'（隣接）になる。したがって "recall" と
// "store" が離れて出てくる本文には一致しない。基準線で全滅した q-003
// （RECALL_STORE・正解4件）を救うのはこの精度であり、ここが緩むと語彙検索は
// 偽ヒットで recall を下げる方向に働く。
func TestLexemeRoundTripKeepsIdentifiersPrecise(t *testing.T) {
	ts := newTestStoreWith(t, newFakeEmbedder("fake:1024"), bigram.New())
	orgA := mustOrgID(t, 1)

	target := putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1,
		chunkIndex: 0, content: "RECALL_STORE を postgres にする",
	})
	decoy := putContent(t, ts, chunkSpec{
		orgID: orgA, documentID: 1, sourceID: 1,
		chunkIndex: 1, content: "recall は store とは別の語である",
	})

	expression, err := postgres.EncodeTsQuery(bigram.New().Tokenize("RECALL_STORE"))
	if err != nil {
		t.Fatalf("EncodeTsQuery: %v", err)
	}

	if !matchesQuery(t, ts, target, expression) {
		t.Errorf("RECALL_STORE を含む本文に一致しない")
	}

	if matchesQuery(t, ts, decoy, expression) {
		t.Errorf("recall と store が離れている本文に一致した（識別子が語ごとにばらけている）")
	}
}

// putContent は1件を取り込んで採番された id を返す。
func putContent(t *testing.T, ts *testStore, spec chunkSpec) int64 {
	t.Helper()

	ids, err := ts.store.Put(t.Context(), spec.orgID, []chunk.Chunk{newChunk(spec)})
	if err != nil {
		t.Fatalf("Put(%q): %v", spec.content, err)
	}

	return ids[0]
}

// storedLexemes は生成列 lexemes のレキシームを読む。
//
// 改行区切りで受けて Go 側で割る。text[] のドライバ変換仕様に依存させないため
// （encodeInt64Array がテキスト表記を自前で作っているのと同じ判断軸）。
func storedLexemes(t *testing.T, ts *testStore, id int64) []string {
	t.Helper()

	var joined string

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT array_to_string(tsvector_to_array(lexemes), E'\n') FROM chunks WHERE id = $1`,
		id).Scan(&joined)
	if err != nil {
		t.Fatalf("lexemes を読めない: %v", err)
	}

	if joined == "" {
		return []string{}
	}

	return strings.Split(joined, "\n")
}

// matchesQuery は保存済みの行が検索式に一致するかを見る。
func matchesQuery(t *testing.T, ts *testStore, id int64, expression string) bool {
	t.Helper()

	var matched bool

	err := ts.db.QueryRowContext(t.Context(),
		`SELECT lexemes @@ to_tsquery('simple', $2) FROM chunks WHERE id = $1`,
		id, expression).Scan(&matched)
	if err != nil {
		t.Fatalf("一致判定に失敗した: %v", err)
	}

	return matched
}
