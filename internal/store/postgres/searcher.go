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

// tsRankNormalization は ts_rank に渡す正規化フラグ。
//
// 🔴 語彙スコアの性質を決める唯一のつまみである。C1/C2 の比較はこの定数を
// 変えて測る。ここ1箇所を変えれば Search と SearchVector の両方に効く。
//
// フラグはビットの論理和で、意味は次のとおり（PostgreSQL 17 の定義）。
//
//	 0 … 文書長を無視する
//	 1 … rank / (1 + log(文書長))
//	 2 … rank / 文書長
//	 4 … 抽出範囲の調和平均距離で割る（ts_rank_cd のみ）
//	 8 … 相異なる語数で割る
//	16 … rank / (1 + log(相異なる語数))
//	32 … rank / (rank + 1)
//
// 既定を 2|32 にした理由は2つある。
//
// (1) 32 が無いと ts_rank の値域に上限が無く、合成 alpha*vector +
// (1-alpha)*lexical の重みが意味を失う。vector_score は正規化済みベクトルの
// 内積なので [-1,1] に収まっており、片側だけ非有界だと alpha が「重み」では
// なく「単位の変換係数」になる。32 は rank/(rank+1) で [0,1) に押し込む。
// 🔑 これは OpenAPI と要件定義 F-4 が定める alpha の契約（加重和）を
// 変えずに済ませるための選択である。RRF のような順位ベースの合成へ移ると
// 契約そのものを触ることになるので、加重和が機能しないという実測が出るまでは採らない。
//
// (2) 2 は文書長で割る。評価セットには 1,136字の一覧表チャンクが5クエリの
// 正解として繰り返し現れており（testdata/eval/README.md「既知の性質」）、
// 長さ正規化の有無で長文が有利にも不利にもなる。BM25 は文書長で正規化し
// ts_rank は既定でしないので、この差を Q-1 の比較に持ち込まないための既定である。
//
// ⚠️ どちらの根拠も「測る前の設計判断」であって実測ではない。予想は
// docs/benchmarks/2026-09-02-lexical-prediction.md に登録してある。
const tsRankNormalization = 2 | 32

// searchSQL は org で絞り、ベクトルと語彙の合成スコアの高い順に返す。
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
//
// 🔴 全行に両方のスコアを計算し、合成値で並べて LIMIT する。「ベクトルの上位 N と
// 語彙の上位 N をマージする」形にしない。索引が無い（ADR 0007）ので、ベクトル側は
// どちらにせよ全行を走査しており、上位 N で切っても計算量は減らない。減らないのに
// カットオフ由来の取りこぼし——片側の N 位圏外にあるが合成では上位に来るはずの行が
// 消える——という新しい誤差源だけが増える。要件定義 Q-1/Q-3 は語彙と合成の効果を
// 測って決める未決事項であり、測定対象に説明のつかない誤差を混ぜない。
//
// 🔑 この選択は索引を入れる段階で必ず再検討が要る。HNSW を張った瞬間、
// ORDER BY が合成値になっているこの SQL は索引を使えない（索引はベクトル距離
// 単体でしか順序を作れない）。そのときに「上位 N のマージ」か「索引で絞ってから
// 再スコア」かを決めることになる。🔴 その判断は Phase 1 項目7 の ADR の仕事で
// あって、ここで先取りしない。先取りすると、測っていない前提で索引の設計を
// 決めることになる。
//
// 🔴 ORDER BY に id の副キーを置く。同点の並びが実行のたびに揺れると、
// alpha = 1.0 が純ベクトルと一致することの検証が「たまたま一致した／しなかった」に
// なり、合成の自己検証が成立しない。PostgreSQL は同点行の順序を保証しない。
//
// 合成は SQL 側で行い、その値をそのまま返す。Go 側で計算し直すと、float8 の
// SQL 演算と float32 の Go 演算がわずかにずれ、「返ってきた並び順と、返ってきた
// スコアの大小が食い違う」という説明のつかない結果になりうる。順位を決めた式と
// 報告する値は同一でなければならない。
const searchSQL = `SELECT id, document_id, source_id, chunk_index, content,
       page_number, section_label,
       -(embedding <#> $2::vector) AS vector_score,
       ts_rank(lexemes, to_tsquery('simple', $6), $7) AS lexical_score,
       $8::real * -(embedding <#> $2::vector)
         + (1 - $8::real) * ts_rank(lexemes, to_tsquery('simple', $6), $7) AS score
FROM chunks
WHERE org_id = $1
  AND (cardinality($3::bigint[]) = 0 OR document_id = ANY ($3::bigint[]))
  AND (cardinality($4::bigint[]) = 0 OR source_id   = ANY ($4::bigint[]))
ORDER BY score DESC, id
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
	lexicalScore float64
	score        float64
}

// Search はベクトルの近い順にチャンクを返す。
func (s *Store) Search(ctx context.Context, q index.Query) ([]index.Result, error) {
	if err := s.prepareSearch(ctx, q); err != nil {
		return nil, err
	}

	vector, err := s.encodeQueryText(ctx, q.Text)
	if err != nil {
		return nil, err
	}

	return s.searchRows(ctx, q, vector)
}

// lexicalExpression は検索語を to_tsquery に渡す形にする。
//
// 🔴 取り込みと同じ Tokenizer を通す。ここで別の分割をすると、同じ語を書いた
// はずのチャンクとクエリが別のトークンになり、語彙スコアが常に 0 になる。
// エラーにならないので、単一の分割器で開発している限り表面化しない。
//
// トークンが1つも出なければ空文字を返す。to_tsquery は空の tsquery を返し、
// ts_rank は 0 になるので、合成は alpha*vector に縮退する。🔴 これはエラーに
// しない。絵文字だけのクエリは異常な入力ではなく、ベクトル側は普通に答えられる。
func (s *Store) lexicalExpression(text string) (string, error) {
	expression, err := encodeTsQuery(s.tokenizer.Tokenize(text))
	if err != nil {
		return "", fmt.Errorf("%w: query text", err)
	}

	return expression, nil
}

// SearchVector は埋め込み済みのベクトルで検索する。
//
// q.Text は順位に使わないが、Search と同じく必須のままにしてある。
// Search に渡すのと同一の index.Query をそのまま渡せることが、2系統の計測が
// 同じ条件であることの前提だからである。
//
// 🔑 これは計測のための口である。ADR 0009 は p95 を「埋め込み往復を含む／除く
// の両方」で測ることを要求しているが、Search は埋め込みと SQL を続けて実行する
// ので DB 部分だけを分離できない。判断の記録は
// docs/adr/0013-evaluation-harness-design.md にある。
//
// 🔴 index.Searcher の契約には足していない。「検索する」という契約の一部では
// なく、計測の都合だからである。契約に入れると、すべてのストア実装（Phase 1
// 項目8 の SQLite を含む）が計測の都合に付き合わされる。Ping を
// embed.Embedder の契約に入れなかったのと同じ判断軸である。
//
// 🔴 SQL も事前検査も Search と共有する。特に assertSameEmbedderAndTokenizer をここでも
// 通すのは、省くと系統2 が SELECT を1本ぶんだけ軽くなり、2系統の差が
// 「埋め込み往復ぶん」でなくなるためである。計測対象と本番経路が乖離したら
// 計測の意味が無い。
func (s *Store) SearchVector(
	ctx context.Context, q index.Query, vector []float32,
) ([]index.Result, error) {
	if err := s.prepareSearch(ctx, q); err != nil {
		return nil, err
	}

	// 渡されたベクトルも契約検査を通す。<#>（負の内積）は入力が正規化済みで
	// あることに依存しており、外から受け取る経路をそこだけ緩めない。
	if err := validateVector(vector); err != nil {
		return nil, fmt.Errorf("%w: query vector", err)
	}

	return s.searchRows(ctx, q, encodeVector(vector))
}

// prepareSearch は DB へ本題を問い合わせる前の共通の検査。
//
// 🔴 検索の前にも埋め込みモデルと分割規則の一致を確かめる。ここで
// WHERE embedder_id = $current と黙って絞り込む実装にしないこと。
// 不一致の行が「検索に出てこないだけ」になり、ADR 0005 が警告する静かな破損の
// 変種になる。必ずエラーとして表面化させる。
func (s *Store) prepareSearch(ctx context.Context, q index.Query) error {
	if err := validateQuery(q); err != nil {
		return err
	}

	return s.assertSameEmbedderAndTokenizer(ctx, s.db)
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
	expression, err := s.lexicalExpression(q.Text)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, searchSQL,
		q.OrgID.Int64(), vector,
		encodeInt64Array(q.DocumentIDs), encodeInt64Array(q.SourceIDs),
		q.Limit, expression, tsRankNormalization, q.Alpha)
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
			&r.pageNumber, &r.sectionLabel, &r.vectorScore, &r.lexicalScore, &r.score)
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
//
// 🔴 3つのスコアはすべて SQL が計算した値をそのまま写す。Score をここで
// alpha*VectorScore + (1-alpha)*LexicalScore と計算し直さないこと。SQL は
// float8 で、Go は float32 で計算するので結果がわずかにずれ、「返ってきた
// 並び順と、返ってきたスコアの大小が食い違う」という説明のつかない結果に
// なりうる。順位を決めた式と報告する値は同一でなければならない。
//
// 🔑 VectorScore と LexicalScore を分けて返すのは、外したときにどちら側が
// 原因かを切り分けるためである（index.Result の doc・要件定義 §3）。
// alpha を実データで調整するには、合成後の値だけでは足りない。
func (r scannedRow) toResult(q index.Query) index.Result {
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
		// 🔴 alpha の値に根拠はまだ無い（要件定義 Q-3）。ADR 0009 の評価セットで
		// 最適値を決めるまで、この係数を「調整済み」として扱わないこと。
		VectorScore:  float32(r.vectorScore),
		LexicalScore: float32(r.lexicalScore),
		Score:        float32(r.score),
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
