package postgres_test

import (
	"errors"
	"testing"

	"github.com/hideyukiMORI/nene-recall/internal/store/postgres"
)

// TestMigrateIsIdempotent は起動のたびに呼ばれる前提を確かめる。
//
// 適用済みを飛ばせないと、2回目の起動で CREATE TABLE が失敗して立ち上がらない。
func TestMigrateIsIdempotent(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	// newTestStore の中で1回適用されている。
	before := countAppliedMigrations(t, ts)
	if before == 0 {
		t.Fatalf("1回目の Migrate が何も記録していない")
	}

	// もう一度呼んでも失敗せず、記録も増えない。
	if err := ts.store.Migrate(t.Context()); err != nil {
		t.Fatalf("2回目の Migrate が失敗した: %v", err)
	}

	// 🔴 件数をリテラルで固定しない。マイグレーションを1本足すたびに
	// このテストが落ちると、次の人は「数字を直す」ことを覚えてしまい、
	// 二重適用の検出という本来の目的が形骸化する。見るのは増えないことである。
	if after := countAppliedMigrations(t, ts); after != before {
		t.Errorf("適用記録が %d 件から %d 件に増えた。二重適用している", before, after)
	}
}

// countAppliedMigrations は適用済みマイグレーションの件数を数える。
func countAppliedMigrations(t *testing.T, ts *testStore) int {
	t.Helper()

	var applied int
	if err := ts.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("schema_migrations を読めない: %v", err)
	}

	return applied
}

// TestMigrateCreatesChunksTable は列がスキーマどおりに存在することを見る。
func TestMigrateCreatesChunksTable(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	const stmt = `SELECT count(*) FROM information_schema.columns
	              WHERE table_name = 'chunks' AND column_name = $1`

	for _, column := range []string{
		"id", "org_id", "document_id", "source_id", "chunk_index",
		"content", "page_number", "section_label", "embedder_id",
		"embedding", "created_at",
		// 0002 で足した語彙検索の列。lexemes は lexeme_text からの生成列である。
		"lexeme_text", "tokenizer_id", "lexemes",
	} {
		var found int
		if err := ts.db.QueryRowContext(t.Context(), stmt, column).Scan(&found); err != nil {
			t.Fatalf("information_schema を読めない: %v", err)
		}

		if found != 1 {
			t.Errorf("列 %q が chunks に無い", column)
		}
	}
}

// TestSearchIndexesExistWithCorrectOperatorClass は 0004 が張った2つの索引を検査する。
//
// 🔴 このテストは TestNoVectorIndexExists を**反転**したものである。名前を
// 変えた理由を残す:
//
// 旧テストは「ベクトル索引が1つも無いこと」を検査していた。守っていたのは
// ADR 0007 の手順（1: 索引なしで完成 → 2: 10万件で測る → 3: 測ってから入れる）で、
// 最初から張ると before の数字が取れなくなる、という一点である。
// ⇒ 手順 2 は docs/benchmarks/2026-09-02-eval-100k-before-index.md で終わり、
// **ADR 0007 の証拠は before 文書に移った。** 証拠が文書にある以上、テストが
// 「索引が無いこと」を守り続ける意味は無い——守っているのは既に達成された
// 前提条件であって、今後壊れうる不変条件ではない。
//
// 🔑 今後壊れうるのはこちらである: **索引の演算子クラスが検索の演算子と
// 食い違うこと。** searcher.go は <#>（負の内積）で並べるので、索引は
// vector_ip_ops でなければならない。vector_cosine_ops で張っても
// CREATE INDEX は成功し、検索も正しい答えを返し、ただ索引が使われなくなる。
// エラーが出ないので、症状は「なぜか速くならない」だけになる。
// ⇒ テストは「無いこと」から「正しい形であること」へ移した (ADR 0022 Decision 2)。
func TestSearchIndexesExistWithCorrectOperatorClass(t *testing.T) {
	ts := newTestStore(t, newFakeEmbedder("fake:1024"))

	// indclass はその索引の列ごとの演算子クラス（oidvector）。どちらの索引も
	// 単一列なので添字 0 を読む。
	const stmt = `SELECT am.amname, opc.opcname
	              FROM pg_index i
	              JOIN pg_class c    ON c.oid = i.indexrelid
	              JOIN pg_am am      ON am.oid = c.relam
	              JOIN pg_opclass opc ON opc.oid = i.indclass[0]
	              WHERE c.relname = $1`

	cases := []struct {
		index     string
		wantAM    string
		wantOpcls string
	}{
		{index: "chunks_embedding_hnsw", wantAM: "hnsw", wantOpcls: "vector_ip_ops"},
		{index: "chunks_lexemes_gin", wantAM: "gin", wantOpcls: "tsvector_ops"},
	}

	for _, c := range cases {
		t.Run(c.index, func(t *testing.T) {
			var accessMethod, opclass string

			err := ts.db.QueryRowContext(t.Context(), stmt, c.index).Scan(&accessMethod, &opclass)
			if err != nil {
				t.Fatalf("索引 %s を pg_index から読めない: %v", c.index, err)
			}

			if accessMethod != c.wantAM {
				t.Errorf("%s のアクセスメソッド = %q, want %q", c.index, accessMethod, c.wantAM)
			}

			if opclass != c.wantOpcls {
				t.Errorf("%s の演算子クラス = %q, want %q（検索の演算子と食い違うと索引は黙って使われない）",
					c.index, opclass, c.wantOpcls)
			}
		})
	}
}

// TestNewRejectsDimensionMismatch は次元の食い違いを構築時に落とすことを確かめる。
//
// db に nil を渡しているのは、New が DB に触れてはならないことの表明でもある。
// 触れていれば、このテストは panic して気づける。
func TestNewRejectsDimensionMismatch(t *testing.T) {
	e := newFakeEmbedder("fake:768")
	e.dims = 768

	_, err := postgres.New(nil, storeOptions(defaultStoreSpec(e)))
	if !errors.Is(err, postgres.ErrEmbedderDimensions()) {
		t.Fatalf("次元 768 の Embedder が受け入れられた: %v", err)
	}
}

// TestNewRejectsEmptyEmbedderID は空の識別子を構築時に落とすことを確かめる。
//
// 空のまま進むと embedder_id 列の CHECK 違反という分かりにくい形で落ちる。
func TestNewRejectsEmptyEmbedderID(t *testing.T) {
	_, err := postgres.New(nil, storeOptions(defaultStoreSpec(newFakeEmbedder(""))))
	if !errors.Is(err, postgres.ErrEmbedderID()) {
		t.Fatalf("空の識別子が受け入れられた: %v", err)
	}
}

// TestNewRejectsMissingTokenizer は分割器の欠落を構築時に落とすことを確かめる。
//
// 🔴 nil の Tokenizer を持ったストアは、取り込みの瞬間に nil 参照で落ちる。
// 設定の誤りは設定を読んだ直後に落とす、という Embedder 側と同じ扱いにする。
func TestNewRejectsMissingTokenizer(t *testing.T) {
	spec := defaultStoreSpec(newFakeEmbedder("fake:1024"))
	spec.tokenizer = nil

	_, err := postgres.New(nil, storeOptions(spec))
	if !errors.Is(err, postgres.ErrTokenizerID()) {
		t.Fatalf("nil の Tokenizer が受け入れられた: %v", err)
	}
}

// TestNewRejectsEmptyTokenizerID は空の分割器識別子を構築時に落とすことを確かめる。
//
// 空のまま進むと tokenizer_id 列の CHECK 違反という分かりにくい形で落ちる。
// embedder_id と同じ扱いである。
func TestNewRejectsEmptyTokenizerID(t *testing.T) {
	spec := defaultStoreSpec(newFakeEmbedder("fake:1024"))
	spec.tokenizer = newFakeTokenizer("")

	_, err := postgres.New(nil, storeOptions(spec))
	if !errors.Is(err, postgres.ErrTokenizerID()) {
		t.Fatalf("空の分割器識別子が受け入れられた: %v", err)
	}
}

// TestNewRejectsUnknownFusion は未知の融合方式を構築時に落とすことを確かめる。
//
// 🔴 Fusion は int なので、範囲外の値を作ること自体は言語仕様上いつでもできる
// (GO-003)。検索のたびに失敗する構成を「起動はする」状態にしない。
func TestNewRejectsUnknownFusion(t *testing.T) {
	const outOfRange = postgres.Fusion(99)

	spec := defaultStoreSpec(newFakeEmbedder("fake:1024"))
	spec.fusion = outOfRange

	_, err := postgres.New(nil, storeOptions(spec))
	if !errors.Is(err, postgres.ErrUnknownFusion()) {
		t.Fatalf("未知の融合方式が受け入れられた: %v", err)
	}
}
