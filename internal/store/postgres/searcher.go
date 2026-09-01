package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
)

// Store が検索側の契約を満たしていることをコンパイル時に確かめる。
var _ index.Searcher = (*Store)(nil)

// searchSQL は org で絞り、ベクトルの近い順に返す。
//
// 🔑 演算子は <#>（負の内積）を使う。<=>（コサイン距離）ではない。
//
// 正規化済みベクトル同士では内積とコサイン類似度の順位が一致し、<#> は乗算と
// 加算だけで済む。bge-m3 が返すベクトルの L2 ノルムは実測でちょうど 1.0 である
// (docs/benchmarks/2026-09-01-baseline.md §2)。
//
// 🔴 ただし「入力が常に正規化されている」ことは自明ではない。ベンチマークは
// この前提を黙って置くなと明示的に警告している。本実装では前提を3箇所で支えている:
// (1) embed.Embedder の doc コメントが「正規化済みを返すこと」を実装の契約と定めている
// (2) validateVector が取り込み時と検索時の両方で実行時に検査する（encode.go）
// (3) 契約違反は書き込み・検索が即エラーになる（順位が静かに狂う形にはならない）
// この3つのどれかを外すなら、同時に <=> へ戻すこと。片方だけ動かさない。
//
// 🔴 SELECT * を書かない（GO-015）。列の増減で Scan がずれるため。
// 🔴 org_id を WHERE の先頭に置く。分離条件が最初に来ることを目で追えるようにする
// (docs/adr/0003-org-id-is-mandatory.md)。
//
// フィルタが cardinality(...) = 0 OR ... の形なのは、空配列 "{}" を
// 「フィルタ無し」として扱うため。単に id = ANY('{}') と書くと一件も一致せず、
// 絞り込まないつもりが全滅する（encode.go の encodeInt64Array を参照）。
const searchSQL = `SELECT id, document_id, source_id, chunk_index, content,
       page_number, section_label,
       -(embedding <#> $2::vector) AS vector_score
FROM chunks
WHERE org_id = $1
  AND (cardinality($3::bigint[]) = 0 OR document_id = ANY ($3::bigint[]))
  AND (cardinality($4::bigint[]) = 0 OR source_id   = ANY ($4::bigint[]))
ORDER BY embedding <#> $2::vector
LIMIT $5`

// scannedRow は1行ぶんの受け取り口。
//
// page_number と section_label は NULL を取りうるので、いったん Null 型で受けてから
// ポインタに変換する。chunk.Chunk 側の *int / *string に直接 Scan はできない。
type scannedRow struct {
	id           int64
	documentID   int64
	sourceID     int64
	chunkIndex   int
	content      string
	pageNumber   sql.NullInt32
	sectionLabel sql.NullString
	vectorScore  float64
}

// Search はベクトルの近い順にチャンクを返す。
func (s *Store) Search(ctx context.Context, q index.Query) ([]index.Result, error) {
	if err := validateQuery(q); err != nil {
		return nil, err
	}

	// 🔴 検索の前にも確かめる。ここで WHERE embedder_id = $current と黙って
	// 絞り込む実装にしないこと。不一致の行が「検索に出てこないだけ」になり、
	// ADR 0005 が警告する静かな破損の変種になる。必ずエラーとして表面化させる。
	if err := s.assertSameEmbedder(ctx, s.db); err != nil {
		return nil, err
	}

	vector, err := s.encodeQueryText(ctx, q.Text)
	if err != nil {
		return nil, err
	}

	return s.searchRows(ctx, q, vector)
}

// validateQuery は DB にも埋め込みにも触れずに分かる誤りを先に落とす。
//
// 🔴 OrgID を最初に見る。分離条件なので、他の何よりも先に確かめる。
//
// 「org_id 列の CHECK (org_id >= 1) が弾くから不要」ではない。それだと
// 一件も一致しない＝空の結果になり、呼び出し側からは「該当なし」と区別がつかない。
// 検索で最も危険な壊れ方は、まさにこの「静かに何も返さない」形である。
// 未指定は既定 org へのフォールバックでもなく空の結果でもなく error として扱う、
// というのが ADR 0003 の要求である (docs/adr/0003-org-id-is-mandatory.md)。
//
// 境界（HTTP）の org.ParseID が既に拒否しているが、org.ID のゼロ値は言語仕様上
// どうやっても作れるので (GO-003)、ここでも検証する。ゼロ値がここまで届いたなら
// 上流にバグがあるということで、それを隠さず表面化させる。
func validateQuery(q index.Query) error {
	if q.OrgID < 1 {
		return fmt.Errorf("%w: org_id is required, got %d", index.ErrInvalidQuery, q.OrgID.Int64())
	}

	if q.Limit < 1 {
		return fmt.Errorf("%w: limit must be at least 1, got %d", index.ErrInvalidQuery, q.Limit)
	}

	if q.Text == "" {
		return fmt.Errorf("%w: text is required", index.ErrInvalidQuery)
	}

	return nil
}

// encodeQueryText は検索語を1本のベクトルに変換する。
func (s *Store) encodeQueryText(ctx context.Context, text string) (string, error) {
	// Kind は KindQuery。取り込み側と使い分けることがプロバイダの要求である
	// （multilingual-e5 は接頭辞が変わり、Voyage は input_type が変わる。ADR 0008）。
	vectors, err := s.embedder.Embed(ctx, []string{text}, embed.KindQuery)
	if err != nil {
		// 🔴 %w を2つ使う理由は writer.go の同じ箇所と同じ。
		// 埋め込み側の sentinel を切ると 503 写像が成立しなくなる。
		return "", fmt.Errorf("%w: embed: %w", errSearch, err)
	}

	if len(vectors) != 1 {
		return "", fmt.Errorf("%w: embedder returned %d vectors for 1 query", errSearch, len(vectors))
	}

	// 検索語のベクトルも契約検査を通す。ここを素通りさせると、
	// 保存側だけ正規化されていて検索側が違う、という非対称な壊れ方をする。
	if err := validateVector(vectors[0]); err != nil {
		return "", fmt.Errorf("%w: query vector", err)
	}

	return encodeVector(vectors[0]), nil
}

// searchRows は SQL を実行して結果を組み立てる。
//
// 🔴 名前付き戻り値を使っているのは、有効な3つの linter が同時に成り立つ書き方が
// ここだけ存在しないためである。実測した衝突:
//   - sqlclosecheck は rows.Close() を defer で呼ぶことを要求する
//     （errors.Join で明示的に閉じる形は "Close should use defer" で落ちた）
//   - errcheck (check-blank) は Close の戻り値を捨てることを禁じる
//   - nonamedreturns は defer から返り値へ結果を反映する書き方を禁じる
//
// 三つのうち一つを外すしかない。外すのを nonamedreturns にしたのは、
// これが唯一「error を握り潰さない」選択肢だからである。errcheck を抑制すると
// Close の失敗が消えるが、こちらは errors.Join で呼び出し側まで運ばれる。
// 抑制の目的は規約を緩めることではなく、規約が守りたかったもの（error を捨てない）を
// 守るためである。裸の return は書かない（GO-004 が本当に禁じているのはそれ）。
//
//nolint:nonamedreturns // defer から Close の失敗を返り値へ合流させる唯一の手段。sqlclosecheck が defer を、errcheck が戻り値の検査を要求するため、この3つを同時に満たす書き方が他に無い
func (s *Store) searchRows(ctx context.Context, q index.Query, vector string) (results []index.Result, err error) {
	rows, err := s.db.QueryContext(ctx, searchSQL,
		q.OrgID.Int64(), vector,
		encodeInt64Array(q.DocumentIDs), encodeInt64Array(q.SourceIDs),
		q.Limit)
	if err != nil {
		return nil, fmt.Errorf("%w: query: %s", errSearch, err.Error())
	}

	// 後始末の失敗も呼び出し側まで運ぶ。errors.Join は nil を落とすので、
	// 走査も後始末も成功したときだけ nil になる。
	defer func() {
		err = errors.Join(err, rows.Close())
	}()

	return collectResults(rows, q)
}

// collectResults は全行を走査して結果に組み替える。
//
// rows の後始末は呼び出し側が持つ。走査の失敗と後始末の失敗を同じ場所で
// 1つの error にまとめるため、ここでは閉じない。
func collectResults(rows *sql.Rows, q index.Query) ([]index.Result, error) {
	// 「見つからない」は失敗ではないので、空の結果は nil ではなく空スライスで返す。
	results := []index.Result{}

	for rows.Next() {
		var r scannedRow

		err := rows.Scan(&r.id, &r.documentID, &r.sourceID, &r.chunkIndex, &r.content,
			&r.pageNumber, &r.sectionLabel, &r.vectorScore)
		if err != nil {
			return nil, fmt.Errorf("%w: scan: %s", errSearch, err.Error())
		}

		results = append(results, r.toResult(q))
	}

	// ループを抜けた理由が「終わった」なのか「壊れた」なのかを必ず確かめる（GO-015）。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: rows: %s", errSearch, err.Error())
	}

	return results, nil
}

// toResult は1行を検索結果に組み替える。
func (r scannedRow) toResult(q index.Query) index.Result {
	vectorScore := float32(r.vectorScore)

	return index.Result{
		Chunk: chunk.Chunk{
			ID: r.id,
			// 🔴 org は列から読み戻さず、問い合わせた org をそのまま入れる。
			// WHERE org_id = $1 で絞った以上、列の値は必ずこれと等しい。
			// 読み戻すと int64 から org.ID への変換が要るが、それは CNF-001 が
			// 禁じている直接変換であり、経路を増やすほど分離は緩む。
			OrgID:        q.OrgID,
			DocumentID:   r.documentID,
			SourceID:     r.sourceID,
			ChunkIndex:   r.chunkIndex,
			Content:      r.content,
			PageNumber:   nullableInt(r.pageNumber),
			SectionLabel: nullableString(r.sectionLabel),
		},
		VectorScore: vectorScore,
		// 語彙検索は未実装（Phase 1 項目4・5）。合成は alpha*vector + (1-alpha)*lexical で、
		// lexical が 0 の間は alpha*vector に縮退する。これは過渡形である。
		//
		// 🔴 alpha の値に根拠はまだ無い（要件定義 Q-3）。ADR 0009 の評価セットで
		// 最適値を決めるまで、この係数を「調整済み」として扱わないこと。
		LexicalScore: 0,
		Score:        q.Alpha * vectorScore,
	}
}

// nullableInt は NULL を nil に写す。
func nullableInt(v sql.NullInt32) *int {
	if !v.Valid {
		return nil
	}

	n := int(v.Int32)

	return &n
}

// nullableString は NULL を nil に写す。
func nullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}

	return &v.String
}
