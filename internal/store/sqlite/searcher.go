package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
)

// Store が検索側の契約を満たしていることをコンパイル時に確かめる。
var _ index.Searcher = (*Store)(nil)

// storeName はレポートに記録するバックエンドの名前。
//
// 🔴 config.Store の値 "sqlite" と同じ綴りにしてある。設定で選んだ名前と
// レポートに出る名前が違うと、条件を追う人が対応を取れない。
const storeName = "sqlite"

// fusionWeightedSumName は融合方式の名前。
//
// 🔴 postgres 側の fusionWeightedSumName と同じ綴りである。同じ方式が
// ストアによって違う名前で記録されると、条件表を並べて読めなくなる。
const fusionWeightedSumName = "weighted-sum"

// lexicalScorerName は語彙スコアの採点関数の名前。
//
// 🔑 postgres 側は "ts_rank"、こちらは "fts5-bm25" である。ADR 0017 が
// 記しているとおり、2つのストアの recall の差には「ストアの差」と
// 「採点関数の差」が混ざる。レポートでそれを分けて読むための印である。
const lexicalScorerName = "fts5-bm25"

// candidateWhere は候補集合を決める条件。
//
// 🔴 分離条件 (org_id) と絞り込みの定義はこの1箇所だけに置く。ベクトル側の
// 問い合わせと語彙側の問い合わせで別々に書くと、片方だけ直して片方を直し忘れる
// 経路ができる。テナントの分離は、どちらの経路から来ても同じでなければ
// ならない (docs/adr/0003-org-id-is-mandatory.md)。
//
// 🔴 org_id を WHERE の先頭に置く。分離条件が最初に来ることを目で追えるようにする。
//
// 🔴 org の絞り込みは SQL のここだけで行う。候補を全件読んでから Go 側で
// org を見て弾く実装にしないこと。Go 側でやると、分離が「順位付けの前処理」の
// 一部になり、その前処理を1回書き換えるだけで漏れる。しかも単一テナントで
// 開発・テストしている限り症状が一切出ない (CLAUDE.md 地雷1)。
//
// 🔑 絞り込みを JSON 配列で渡すのは、プレースホルダを件数ぶん並べないためである
// (encode.go の idFilterJSON を参照)。json_array_length(...) = 0 の分岐が
// 「フィルタ無し」を表す。この分岐を消すと、絞り込まないつもりの検索が全滅する。
const candidateWhere = `WHERE org_id = ?
  AND (json_array_length(?) = 0 OR document_id IN (SELECT value FROM json_each(?)))
  AND (json_array_length(?) = 0 OR source_id   IN (SELECT value FROM json_each(?)))`

// selectCandidatesSQL は候補行を本文とベクトルごと読む。
//
// 🔴 SELECT * を書かない（GO-015）。列の増減で Scan がずれるため。
//
// 🔴 ORDER BY も LIMIT も付けない。順位はベクトルと語彙を合成してから決まり、
// その合成は Go 側で行う (ADR 0017 Decision 2)。SQL に部分的な順序を持たせると、
// 「SQL が決めた順序」と「合成が決めた順序」の2つが存在することになる。
//
// 🔴 上位 N で切らない。索引が無いのでどちらにせよ全候補を読んでおり、切っても
// 計算量は減らない。減らないのにカットオフ由来の取りこぼし——片側の N 位圏外に
// あるが合成では上位に来るはずの行が消える——という誤差源だけが増える。
// postgres 側が全行に両方のスコアを計算しているのと同じ判断である。
const selectCandidatesSQL = `SELECT id, external_id, document_id, source_id, chunk_index, content,
       page_number, section_label, embedding
FROM chunks
` + candidateWhere

// selectLexicalSQL は候補集合に属する行の bm25 を読む。
//
// 🔴 rowid の絞り込みを「候補 id を並べた IN リスト」で書かない。SQLite の
// 変数の上限は既定 32766 で、コーパスが増えると組み立て自体が失敗する。
// 副問い合わせにすればパラメータ数は候補件数に依存しない。
//
// 🔑 bm25() は小さいほど良い（負値）。符号の反転と正規化は Go 側で行う
// (fuse)。SQL 側で -bm25() と書かないのは、index.Result が返すのが
// 「正規化前の生の値」であり、その定義をスコア計算の1箇所に集めるためである。
const selectLexicalSQL = `SELECT chunks_fts.rowid, bm25(chunks_fts)
FROM chunks_fts
WHERE chunks_fts MATCH ?
  AND chunks_fts.rowid IN (SELECT id FROM chunks ` + candidateWhere + `)`

// candidateRow は候補1行ぶんの受け取り口。
//
// page_number と section_label は NULL を取りうるので、いったん Null 型で受けてから
// ポインタに変換する。chunk.Chunk 側の *int / *string に直接 Scan はできない。
type candidateRow struct {
	id int64
	// externalID は外部システムの id。持たない行は NULL なので Null 型で受ける。
	externalID   sql.NullInt64
	documentID   int64
	sourceID     int64
	chunkIndex   int
	content      string
	pageNumber   sql.NullInt64
	sectionLabel sql.NullString
	// vectorScore は正規化済みベクトルどうしの内積。Go 側で計算する。
	vectorScore float64
	// lexicalRaw は -bm25()。大きいほど良い向きに揃えただけで、正規化はしていない。
	lexicalRaw float64
}

// Search はベクトルと語彙を合成した順にチャンクを返す。
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

// SearchVector は埋め込み済みのベクトルで検索する。
//
// q.Text は順位のベクトル側には使わないが、Search と同じく必須のままにしてある。
// Search に渡すのと同一の index.Query をそのまま渡せることが、2系統の計測が
// 同じ条件であることの前提だからである（語彙側は q.Text を使う）。
//
// 🔑 これは計測のための口である。ADR 0009 は p95 を「埋め込み往復を含む／除く
// の両方」で測ることを要求しているが、Search は埋め込みと問い合わせを続けて
// 実行するので DB 部分だけを分離できない。
//
// 🔴 index.Searcher の契約には足していない。「検索する」という契約の一部では
// なく、計測の都合だからである。postgres 側と同じ判断であり、同じ位置に置いて
// あることが2つのストアの p95 を並べられる根拠になる。
//
// 🔴 事前検査を Search と共有する。特に assertSameEmbedderAndTokenizer をここでも
// 通すのは、省くと系統2 が問い合わせを1本ぶんだけ軽くなり、2系統の差が
// 「埋め込み往復ぶん」でなくなるためである (CLAUDE.md 地雷10)。
func (s *Store) SearchVector(
	ctx context.Context, q index.Query, vector []float32,
) ([]index.Result, error) {
	if err := s.prepareSearch(ctx, q); err != nil {
		return nil, err
	}

	// 渡されたベクトルも契約検査を通す。内積は入力が正規化済みであることに
	// 依存しており、外から受け取る経路をそこだけ緩めない。
	if err := validateVector(vector); err != nil {
		return nil, fmt.Errorf("%w: query vector", err)
	}

	return s.searchRows(ctx, q, vector)
}

// prepareSearch は DB へ本題を問い合わせる前の共通の検査。
//
// 🔴 検索の前にも埋め込みモデルと分割規則の一致を確かめる。ここで
// WHERE embedder_id = <current> と黙って絞り込む実装にしないこと。
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
// というのが ADR 0003 の要求である。
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
func (s *Store) encodeQueryText(ctx context.Context, text string) ([]float32, error) {
	// Kind は KindQuery。取り込み側と使い分けることがプロバイダの要求である
	// （multilingual-e5 は接頭辞が変わり、Voyage は input_type が変わる。ADR 0008）。
	vectors, err := s.embedder.Embed(ctx, []string{text}, embed.KindQuery)
	if err != nil {
		// 🔴 %w を2つ使う理由は writer.go の同じ箇所と同じ。
		// 埋め込み側の sentinel を切ると 503 写像が成立しなくなる。
		return nil, fmt.Errorf("%w: embed: %w", errSearch, err)
	}

	if len(vectors) != 1 {
		return nil, fmt.Errorf("%w: embedder returned %d vectors for 1 query", errSearch, len(vectors))
	}

	// 検索語のベクトルも契約検査を通す。ここを素通りさせると、
	// 保存側だけ正規化されていて検索側が違う、という非対称な壊れ方をする。
	if err := validateVector(vectors[0]); err != nil {
		return nil, fmt.Errorf("%w: query vector", err)
	}

	return vectors[0], nil
}

// matchExpression は検索語を FTS5 の MATCH 式にする。
//
// 🔴 取り込みと同じ Tokenizer を通す。ここで別の分割をすると、同じ語を書いた
// はずのチャンクとクエリが別のトークンになり、語彙スコアが常に 0 になる。
// エラーにならないので、単一の分割器で開発している限り表面化しない。
//
// トークンが1つも出なければ空文字を返す。🔴 これはエラーにしない。絵文字だけの
// クエリは異常な入力ではなく、ベクトル側は普通に答えられる。呼び出し側
// (lexicalScores) が空文字を「語彙スコア 0」として扱う。
func (s *Store) matchExpression(text string) (string, error) {
	expression, err := encodeMatchExpression(s.tokenizer.Tokenize(text))
	if err != nil {
		return "", fmt.Errorf("%w: query text", err)
	}

	return expression, nil
}

// searchRows は候補を読み、2つのスコアを合成して順位を決める。
//
// 🔑 順位付けが SQL ではなく Go にあることが、このストアの正体である。
// ADR 0007 が pgvector の比較対象に据えたのは、まさにこの「Go 側の総当たり」で
// ある。SQL に ORDER BY を持たせる形へ寄せると、比較の意味が変わる。
func (s *Store) searchRows(ctx context.Context, q index.Query, vector []float32) ([]index.Result, error) {
	expression, err := s.matchExpression(q.Text)
	if err != nil {
		return nil, err
	}

	candidates, err := s.candidates(ctx, q, vector)
	if err != nil {
		return nil, err
	}

	lexical, err := s.lexicalScores(ctx, q, expression)
	if err != nil {
		return nil, err
	}

	for i := range candidates {
		// 当たらなかった行は 0 のまま。「語彙が当たらない」は正常な結果である。
		candidates[i].lexicalRaw = lexical[candidates[i].id]
	}

	return fuse(candidates, q), nil
}

// candidateArgs は候補集合の条件に渡す引数を組み立てる。
//
// 🔴 candidateWhere の ? の並びと1対1で対応する。SQL と引数が別の場所に
// あるので、片方だけ直すと「別の列で絞り込む」という静かな誤りになる。
// 2箇所（ベクトル側と語彙側）から同じものを使うことで、ずれる余地を減らしてある。
func candidateArgs(q index.Query) []any {
	documents := idFilterJSON(q.DocumentIDs)
	sources := idFilterJSON(q.SourceIDs)

	return []any{q.OrgID.Int64(), documents, documents, sources, sources}
}

// rowsQuery は1本の問い合わせ。queryAll の引数を4つ以下に保つための入れ物 (GO-011)。
type rowsQuery struct {
	// name は失敗を報告するときの経路の名前。
	name string
	// statement は SQL。
	statement string
	// args はプレースホルダに渡す値。
	args []any
}

// queryAll は問い合わせて全行を走査し、走査と後始末の失敗をまとめて返す。
//
// 🔴 内側に無名関数を挟んでいるのは、有効な3つの linter が同時に成り立つ
// 書き方がこれしか無いためである。実測した衝突:
//   - sqlclosecheck は rows.Close() を defer で呼ぶことを要求する
//     （明示的に閉じる形は "Close should use defer" で落ちた）
//   - errcheck (check-blank) は Close の戻り値を捨てることを禁じる
//     （defer rows.Close() は "Error return value is not checked" で落ちた）
//   - nonamedreturns は defer から返り値へ結果を反映する書き方を禁じる
//
// postgres 側は3つ目を //nolint で外して名前付き戻り値を使っている。こちらは
// 無名関数の外に closeErr を置くことで、抑制なしに同じことを達成している。
// 🔑 目的は「error を捨てない」であって、書き方はその手段である。
//
// 走査を関数で受けるのは、候補行と語彙スコアで組み立てる値の型が違うためである。
func queryAll[T any](
	ctx context.Context, db *sql.DB, q rowsQuery, scan func(*sql.Rows) (T, error),
) (T, error) {
	var closeErr error

	value, scanErr := func() (T, error) {
		var zero T

		rows, err := db.QueryContext(ctx, q.statement, q.args...)
		if err != nil {
			return zero, fmt.Errorf("%w: %s: %s", errSearch, q.name, err.Error())
		}

		// 後始末の失敗も呼び出し側まで運ぶ。無名関数の外の変数へ書くので、
		// 名前付き戻り値を使わずに defer の結果を受け取れる。
		defer func() { closeErr = rows.Close() }()

		scanned, err := scan(rows)
		if err != nil {
			return zero, err
		}

		// 🔴 ループを抜けた理由が「終わった」なのか「壊れた」なのかを必ず
		// 確かめる（GO-015）。走査そのものは scan に任せているので、この確認は
		// 走査を呼んだ側が持つ。
		if err := rows.Err(); err != nil {
			return zero, fmt.Errorf("%w: %s: rows: %s", errSearch, q.name, err.Error())
		}

		return scanned, nil
	}()

	if err := errors.Join(scanErr, closeErr); err != nil {
		var zero T

		return zero, err
	}

	return value, nil
}

// candidates は候補行を読み、その場でベクトルスコアを計算する。
func (s *Store) candidates(ctx context.Context, q index.Query, vector []float32) ([]candidateRow, error) {
	return queryAll(ctx, s.db,
		rowsQuery{name: "candidates", statement: selectCandidatesSQL, args: candidateArgs(q)},
		func(rows *sql.Rows) ([]candidateRow, error) { return scanCandidates(rows, vector) })
}

// scanCandidates は全行を走査して候補に組み替える。
//
// rows の後始末と rows.Err() の確認は queryAll が持つ。走査の失敗・後始末の
// 失敗・走査を中断させた失敗を同じ場所で1つの error にまとめるためである。
func scanCandidates(rows *sql.Rows, vector []float32) ([]candidateRow, error) {
	// 「見つからない」は失敗ではないので、空の結果は nil ではなく空スライスで返す。
	candidates := []candidateRow{}

	for rows.Next() {
		var (
			r    candidateRow
			blob []byte
		)

		err := rows.Scan(&r.id, &r.externalID, &r.documentID, &r.sourceID, &r.chunkIndex, &r.content,
			&r.pageNumber, &r.sectionLabel, &blob)
		if err != nil {
			return nil, fmt.Errorf("%w: scan: %s", errSearch, err.Error())
		}

		stored, err := decodeVector(blob)
		if err != nil {
			return nil, fmt.Errorf("%w: chunks.id=%d: %w", errSearch, r.id, err)
		}

		r.vectorScore = dot(vector, stored)
		candidates = append(candidates, r)
	}

	return candidates, nil
}

// lexicalScores は候補集合に属する行の語彙スコア（-bm25）を id 引きで返す。
//
// 🔴 空の MATCH 式を SQLite に渡さない。空文字は fts5 の構文エラーになる
// （postgres の to_tsquery が空を許すのとは違う・実測）。トークンが1つも出ない
// クエリは正常な入力なので、問い合わせを飛ばして「どの行も語彙 0」とする。
// 合成は alpha*vector に縮退する。
func (s *Store) lexicalScores(
	ctx context.Context, q index.Query, expression string,
) (map[int64]float64, error) {
	if expression == "" {
		return map[int64]float64{}, nil
	}

	return queryAll(ctx, s.db, rowsQuery{
		name:      "lexical",
		statement: selectLexicalSQL,
		args:      append([]any{expression}, candidateArgs(q)...),
	}, scanLexicalScores)
}

// scanLexicalScores は bm25 の符号を反転して id 引きの表にする。
//
// 🔑 bm25() は「小さいほど良い」（負値）ので、そのままでは加重和に入れられない。
// 反転して「大きいほど良い」に揃えるのがここの仕事で、[0,1] への正規化は
// 候補全体を見てからでないとできないので fuse が行う。
func scanLexicalScores(rows *sql.Rows) (map[int64]float64, error) {
	scores := map[int64]float64{}

	for rows.Next() {
		var (
			id   int64
			bm25 float64
		)

		if err := rows.Scan(&id, &bm25); err != nil {
			return nil, fmt.Errorf("%w: lexical scan: %s", errSearch, err.Error())
		}

		scores[id] = -bm25
	}

	return scores, nil
}

// fuse は2つのスコアを加重和で1つの順位にまとめる。
//
// 🔴 語彙スコアをクエリ内の最大値で割ってから合成する。割らないと加重和は
// 機能しない——ベクトルと語彙はそもそも単位が違う量であり、alpha は「重み」で
// あって「単位の変換係数」ではない。postgres 側が実測で確かめた性質であり
// (2026-09-02・ts_rank は中央値 0.00016、内積は 0.44 とスケールが3桁違った)、
// bm25 でも同じ形の問題が起きる。
//
// 🔴 ベクトルスコアは正規化しない。正規化済みベクトルの内積であり、OpenAPI が
// その意味で定義している。クエリ内で割ると上位1件が常に 1.0 になり、
// 「どれだけ似ているか」という絶対的な意味が失われる。
//
// 🔴 最大値が 0 のとき（語彙が1件も当たらない、あるいはトークンが空）は
// 語彙側を 0 に落とし、合成は alpha*vector に縮退する。エラーにはしない——
// 語彙が当たらないのは正常な検索結果である。
func fuse(candidates []candidateRow, q index.Query) []index.Result {
	maxLexical := 0.0
	for _, c := range candidates {
		maxLexical = max(maxLexical, c.lexicalRaw)
	}

	scored := make([]scoredCandidate, 0, len(candidates))

	for _, c := range candidates {
		normalized := 0.0
		if maxLexical > 0 {
			normalized = c.lexicalRaw / maxLexical
		}

		scored = append(scored, scoredCandidate{
			row:   c,
			score: float64(q.Alpha)*c.vectorScore + float64(1-q.Alpha)*normalized,
		})
	}

	// 🔴 同点は id の昇順で決める。並びが実行のたびに揺れると、alpha = 1.0 が
	// 純ベクトルと一致することの検証が「たまたま一致した／しなかった」になり、
	// 合成の自己検証が成立しない。postgres 側の ORDER BY score DESC, id と同じ。
	slices.SortFunc(scored, func(a, b scoredCandidate) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}

		return cmp.Compare(a.row.id, b.row.id)
	})

	return toResults(scored[:min(len(scored), q.Limit)], q)
}

// scoredCandidate は合成後のスコアを持つ候補。
type scoredCandidate struct {
	row   candidateRow
	score float64
}

// toResults は上位の候補を検索結果に組み替える。
//
// 🔴 返す LexicalScore は正規化前の生の値である。理由は2つ。
//
// (1) vector_score と lexical_score の意味を条件によって変えないため。
// OpenAPI もこの2つを「ベクトル類似度」「語彙一致度」として定義している。
// (2) スケールの診断に生の値が要るため。postgres 側で ts_rank のスケールが
// 3桁違うと分かったのは、生の値がレポートに出ていたからである（2026-09-02）。
//
// ⚠️ 対価として、Score が2つのスコアから1行だけでは再計算できない。
// 正規化がクエリ全体の最大値を要するためで、これは合成の性質そのものである。
func toResults(scored []scoredCandidate, q index.Query) []index.Result {
	results := make([]index.Result, 0, len(scored))

	for _, s := range scored {
		results = append(results, index.Result{
			Chunk: chunk.Chunk{
				ID: s.row.id,
				// 🔴 org は列から読み戻さず、問い合わせた org をそのまま入れる。
				// WHERE org_id = ? で絞った以上、列の値は必ずこれと等しい。
				// 読み戻すと int64 から org.ID への変換が要るが、それは CNF-001 が
				// 禁じている直接変換であり、経路を増やすほど分離は緩む。
				OrgID: q.OrgID,
				// 🔴 外部 id は列から読み戻す。org と違って「問い合わせた値」が
				// 存在しないので、返せるのは保存されている値だけである。
				ExternalID:   nullableInt64(s.row.externalID),
				DocumentID:   s.row.documentID,
				SourceID:     s.row.sourceID,
				ChunkIndex:   s.row.chunkIndex,
				Content:      s.row.content,
				PageNumber:   nullableInt(s.row.pageNumber),
				SectionLabel: nullableString(s.row.sectionLabel),
			},
			VectorScore:  float32(s.row.vectorScore),
			LexicalScore: float32(s.row.lexicalRaw),
			Score:        float32(s.score),
		})
	}

	return results
}

// nullableInt は NULL を nil に写す。
func nullableInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}

	n := int(v.Int64)

	return &n
}

// nullableInt64 は NULL を nil に写す。
func nullableInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}

	return &v.Int64
}

// nullableString は NULL を nil に写す。
func nullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}

	return &v.String
}
