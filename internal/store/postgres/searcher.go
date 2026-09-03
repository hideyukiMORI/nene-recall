package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
)

// Store が検索側の契約を満たしていることをコンパイル時に確かめる。
var _ index.Searcher = (*Store)(nil)

// tsRankNormalization は ts_rank に渡す正規化フラグ。
//
// 🔴 語彙スコアの性質を決める唯一のつまみである。ここ1箇所を変えれば
// Search と SearchVector の両方に効く。
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
// 🔑 0 は実測で選んだ値である（2026-09-02・語彙のみ alpha=0 で計測）。
//
//	フラグ 0    (正規化なし)   recall@10 0.620 / MRR 0.736
//	フラグ 2|32 (長さ正規化)   recall@10 0.544 / MRR 0.496
//
// 差は 58クエリの分解能（1クエリ = 0.017）をはるかに超えており、MRR の
// 0.24 差は「最初の1件が何位に来るか」が壊れていたことを意味する。
//
// 🔴 当初の既定は 2|32 だった。理由は2つとも実測で否定されている。
//
// (1) 「32 で [0,1) に有界化すれば alpha の加重和が成立する」——成立しなかった。
// 32 は rank/(rank+1) であり、rank が小さいときはほぼ恒等写像である。実測の
// lexical_score は 0.000〜0.0036（中央値 0.00016）で、vector_score の
// 0.23〜0.74（中央値 0.44）とスケールが3桁違う。alpha=0.7 と alpha=1.0 で
// 順位が変わったクエリは 58 件中 1 件だけだった。
// 🔑 有界化は再スケールではない。スケールを揃えるのは合成側の仕事である。
//
// (2) 「2 で文書長を割れば、長文 gold が有利になる交絡を避けられる」——
// 逆だった。名指しの長文 gold の recall はベクトル 0.267 / フラグ 0 が 0.200 /
// フラグ 2|32 が 0.067 で、フラグ 0 はベクトルより長文に弱い。
// ⇒ フラグ 0 の優位は長文優遇では説明できない。長さ別の内訳
// (Summary.GoldLengthRecall) を入れてあったので、この切り分けができた。
//
// ⚠️ この値は「語彙のみ」で測って選んである。融合方式を変えると最適値が
// 動きうるので、方式の比較が終わったら振り直す余地がある。
const tsRankNormalization = 0

// candidatesCTE は org で絞った候補集合に、2つのスコアを付けたもの。
//
// 🔴 融合方式ごとに SQL を書き分けるが、候補集合の定義はこの1箇所だけに置く。
// 分離条件 (org_id) と絞り込みを方式ごとに書くと、片方だけ直して片方を
// 直し忘れる経路ができる。テナントの分離は、方式の選択とは独立に成り立って
// いなければならない (docs/adr/0003-org-id-is-mandatory.md)。
// 定数の連結なので、組み立ては実行時ではなくコンパイル時に済む。
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
// 🔴 全行に両方のスコアを計算する。「ベクトルの上位 N と語彙の上位 N をマージ
// する」形にしない。索引が無い（ADR 0007）ので、ベクトル側はどちらにせよ全行を
// 走査しており、上位 N で切っても計算量は減らない。減らないのにカットオフ由来の
// 取りこぼし——片側の N 位圏外にあるが合成では上位に来るはずの行が消える——という
// 新しい誤差源だけが増える。要件定義 Q-1/Q-3 は語彙と合成の効果を測って決める
// 未決事項であり、測定対象に説明のつかない誤差を混ぜない。
//
// 🔑 この選択は索引を入れる段階で必ず再検討が要る、と書いてあった。
// その再検討が ADR 0022 である。HNSW を張っても ORDER BY が合成値である
// この SQL は索引を使えないので、索引を効かせる経路は
// candidateSelectionCTE（両側 top-K の和集合）として**別に**足した。
// 🔴 この全探索の形は消さずに残す。索引の効果と候補生成の効果は分離できず、
// 「索引を入れる前と同じ形」を測れる経路が無くなると before/after が読めなくなる
// (docs/adr/0022-indexed-candidate-search.md Decision 3)。
const candidatesCTE = `WITH candidates AS (
  SELECT id, external_id, document_id, source_id, chunk_index, content,
         page_number, section_label,
         -(embedding <#> $2::vector) AS vector_score,
         ts_rank(lexemes, to_tsquery('simple', $6), $7) AS lexical_score
  FROM chunks
  WHERE org_id = $1
    AND (cardinality($3::bigint[]) = 0 OR document_id = ANY ($3::bigint[]))
    AND (cardinality($4::bigint[]) = 0 OR source_id   = ANY ($4::bigint[]))
)`

// candidateSelectionCTE は「両側 top-K の和集合」を候補集合にする形。
// $9 は K（RECALL_CANDIDATE_K）。判断の正本は ADR 0022 Decision 1。
//
// 🔑 candidatesCTE と**同じ名前・同じ列**の candidates を最後に作る。だから
// 合成の SQL（下の2つの tail）は候補の作り方を1行も知らずに済む。合成の式が
// 経路ごとに分岐すると、「モードを変えたら合成も変わっていた」という交絡が
// 入り、after の実測で候補生成だけの効果を読めなくなる。
//
// 4段の意味:
//
//	vec           … ベクトル側 top-K。ORDER BY が距離そのものなので
//	                HNSW (vector_ip_ops) が効く形になっている
//	lex           … 語彙側 top-K。@@ が GIN で絞ってから ts_rank を計算する。
//	                トークンが 0 個なら to_tsquery は空の tsquery を返し、
//	                @@ が false になるので lex は空集合になる（エラーにしない）
//	candidate_ids … 上2つの**和集合**。片側だけにすると、語彙でしか当たらない
//	                文書（exact-term の識別子）が候補に入らず、ADR 0014 が
//	                測った利得を捨てることになる（ADR 0022 の却下表）
//	candidates    … 候補行に**両方の**スコアを付け直す。@@ に当たらなかった行の
//	                ts_rank は 0 になる
//
// 🔴 lex の ORDER BY には第2ソートキー id が要る。ts_rank は同点になる。
//
// 2026-09-02 の 10万件の実測で、K=100 の**境界に 36 行が同点で並ぶ**クエリが
// あった（q-010・`@@` に 28,760 行が当たる）。第2キーが無いと、そのうちどの行が
// 候補に入るかは実行ごとに決まらない——同じベクトル・同じ tsquery で lex を
// 15 回実行したら **15 通りの id 集合**が返った（ベクトル側 vec は 15 回とも同一）。
//
// 候補集合が変われば MAX(lexical_score) OVER ()（候補集合内の最大値）が変わり、
// 最終順位の下位が入れ替わる。⇒ 系統1（Search）と系統2（SearchVector）の順位が
// 一致せず、評価ハーネスが止まる。**259 件では同点が積み上がらないので出ない。**
// 正本は docs/benchmarks/2026-09-02-eval-100k-after-index.md §9。
//
// ⚠️ vec 側には第2キーを**置けない**。ORDER BY が距離そのものでなくなると
// HNSW の順序に乗らず、索引が黙って使われなくなる（この CTE の存在理由が消える）。
// 上の実測では vec は安定していたが、それは同点が無かったということであって、
// 保証ではない。ベクトル側の同点が問題になったら、K を増やすか後段で決定的に
// 切るかであって、ここに id を足す手は取れない。
//
// 🔴 org_id の WHERE は vec と lex の**両方**に入れる。候補生成の段で別 org が
// 混ざると、後段で弾いたとしても「別テナントの文書を読んだ」ことになる。
// 分離は方式や経路の選択とは独立に成り立っていなければならない (ADR 0003)。
// filters（document_ids / source_ids）も同じ理由で両方に入れる——片側だけに
// 書くと、絞り込んだはずの文書が候補に居座り、候補枠を食い潰す。
//
// 🔴 SELECT * を書かない（GO-015）。
// 🔴 演算子は <#>（負の内積）。索引の演算子クラス vector_ip_ops と対になって
// いなければ索引は**黙って使われない**（migrations/0004_add_search_indexes.sql）。
//
// ⚠️ vec と lex には LIMIT があるので、PostgreSQL はこの2つを CTE として
// 実体化する（インライン展開しない）。それが狙いである——展開されると
// 「まず K 件に絞る」という形そのものが消える。
const candidateSelectionCTE = `WITH vec AS (
  SELECT id
  FROM chunks
  WHERE org_id = $1
    AND (cardinality($3::bigint[]) = 0 OR document_id = ANY ($3::bigint[]))
    AND (cardinality($4::bigint[]) = 0 OR source_id   = ANY ($4::bigint[]))
  ORDER BY embedding <#> $2::vector
  LIMIT $9
), lex AS (
  SELECT id
  FROM chunks
  WHERE org_id = $1
    AND (cardinality($3::bigint[]) = 0 OR document_id = ANY ($3::bigint[]))
    AND (cardinality($4::bigint[]) = 0 OR source_id   = ANY ($4::bigint[]))
    AND lexemes @@ to_tsquery('simple', $6)
  ORDER BY ts_rank(lexemes, to_tsquery('simple', $6), $7) DESC, id
  LIMIT $9
), candidate_ids AS (
  SELECT id FROM vec
  UNION
  SELECT id FROM lex
), candidates AS (
  SELECT c.id, c.external_id, c.document_id, c.source_id, c.chunk_index, c.content,
         c.page_number, c.section_label,
         -(c.embedding <#> $2::vector) AS vector_score,
         ts_rank(c.lexemes, to_tsquery('simple', $6), $7) AS lexical_score
  FROM chunks c
  JOIN candidate_ids ON candidate_ids.id = c.id
)`

// weightedSumTail は方式A（加重和）の本体。$8 は alpha。
//
// 🔴 lexical_score をクエリ内の最大値で割ってから合成する。割らないと加重和は
// 機能しない——2026-09-02 の実測で lexical は中央値 0.00016、vector は中央値
// 0.44 とスケールが3桁違い、alpha=0.7 と alpha=1.0 で順位が変わったクエリは
// 58 件中 1 件だけだった。alpha は「重み」であって「単位の変換係数」ではない。
//
// 🔴 vector_score は正規化しない。正規化済みベクトルの内積であり、OpenAPI が
// その意味で定義している。クエリ内で割ると上位1件が常に 1.0 になり、
// 「どれだけ似ているか」という絶対的な意味が失われる。
//
// 🔴 NULLIF で 0 除算を避ける。全行の lexical が 0（語彙が1件も当たらない、
// あるいはクエリのトークンが空）のとき MAX は 0 になる。NULLIF が NULL を返し、
// 除算が NULL になり、COALESCE が 0 に落とすので、合成は alpha*vector に
// 縮退する。エラーにはしない——語彙が当たらないのは正常な検索結果である。
//
// ⚠️ 返す lexical_score は正規化前の生の値である（下の scannedRow の説明を参照）。
//
// 🔑 「クエリ内の最大値」の意味は候補の作り方で変わる。exhaustive では org 内の
// 全行の最大値、candidates では候補集合内の最大値である。⇒ 同じ alpha でも
// 効き方が違う。ADR 0022 が「alpha の再測定が要る」と書いているのはこの1行の
// ためである (ADR 0015 Decision 3)。
const weightedSumTail = `
SELECT id, external_id, document_id, source_id, chunk_index, content,
       page_number, section_label,
       vector_score, lexical_score,
       $8::real * vector_score
         + (1 - $8::real)
           * COALESCE(lexical_score / NULLIF(MAX(lexical_score) OVER (), 0), 0) AS score
FROM candidates
ORDER BY score DESC, id
LIMIT $5`

// searchWeightedSumSQL は全探索 × 加重和。
const searchWeightedSumSQL = candidatesCTE + weightedSumTail

// searchCandidateWeightedSumSQL は候補生成 × 加重和。
const searchCandidateWeightedSumSQL = candidateSelectionCTE + weightedSumTail

// rrfTail は方式B（順位融合）の本体。$8 は RRF の平滑化定数 k。
//
// 🔑 スコアの値ではなく順位だけを使うので、ベクトルと語彙のスケールが
// 3桁違っても原理的に問題にならない。方式A が正規化で解いている問題を、
// こちらは値を捨てることで解いている。
//
// ⚠️ alpha はこの方式では使わない。重みを表す場所が無いので、指定されても
// 順位に影響しない。契約（要件定義 F-4・OpenAPI）は加重和のままなので、
// この方式は計測のための実装である。既定にするなら ADR が要る。
//
// 🔴 古典的な RRF は「2つの検索器が返した上位 N 件のリスト」を融合するが、
// ここは候補集合の全行に順位を振っている。索引が無く全行を走査しているので
// 上位 N で切る理由が無いのと、切ると新しい誤差源が増えるためである。
// ⚠️ 帰結として、語彙が1件も当たらない行にも lexical_rank が付く
// （lexical_score = 0 の行は RANK() が同順位でまとめる）。それらは互いに同じ
// 加点を受けるので相対順位は変わらないが、語彙が当たった行との差は、
// 上位 N を切る実装より小さくなる。オフラインで上位10件どうしを融合した
// 見積りとは、この点で条件が違う。
//
// float8 に明示的に寄せているのは、1.0 / bigint が numeric になるのを避ける
// ため。numeric はドライバの走査先が float64 と噛み合わない。
//
// 🔑 候補モードでは順位が**候補集合の中**で振られる。上の「全行に順位を振って
// いる」という但し書きは exhaustive のときの話で、candidates では 2K 行以下に
// なる——古典的な RRF（両側の上位 N を融合する）に一段近づく形である。
// ADR 0015 が「候補集合を絞る構成では RRF の評価が変わりうる」と留保したのは
// この違いであり、どちらが良いかは after のレーンが測る (ADR 0022 の却下表)。
const rrfTail = `, ranked AS (
  SELECT id, external_id, document_id, source_id, chunk_index, content,
         page_number, section_label, vector_score, lexical_score,
         RANK() OVER (ORDER BY vector_score  DESC) AS vector_rank,
         RANK() OVER (ORDER BY lexical_score DESC) AS lexical_rank
  FROM candidates
)
SELECT id, external_id, document_id, source_id, chunk_index, content,
       page_number, section_label,
       vector_score, lexical_score,
       1.0::float8 / ($8::int + vector_rank)::float8
         + 1.0::float8 / ($8::int + lexical_rank)::float8 AS score
FROM ranked
ORDER BY score DESC, id
LIMIT $5`

// searchRRFSQL は全探索 × 順位融合。
const searchRRFSQL = candidatesCTE + rrfTail

// searchCandidateRRFSQL は候補生成 × 順位融合。
//
// 🔴 組み合わせを1つ落とさない。candidates × rrf が動かないと、ADR 0015 の
// 留保を after で測れなくなる (ADR 0022 Decision 1)。
const searchCandidateRRFSQL = candidateSelectionCTE + rrfTail

// 🔴 どちらの方式も ORDER BY に id の副キーを置く。同点の並びが実行のたびに
// 揺れると、alpha = 1.0 が純ベクトルと一致することの検証が「たまたま一致した／
// しなかった」になり、合成の自己検証が成立しない。PostgreSQL は同点行の順序を
// 保証しない。RRF では RANK() が同順位をまとめるので同点はむしろ普通に起きる。

// scannedRow は1行ぶんの受け取り口。
//
// page_number と section_label は NULL を取りうるので、いったん Null 型で受けてから
// ポインタに変換する。chunk.Chunk 側の *int / *string に直接 Scan はできない。
type scannedRow struct {
	id int64
	// externalID は外部システムの id。持たない行は NULL なので Null 型で受ける。
	externalID   sql.NullInt64
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

// searchPlan は実行する SQL と、その引数。
//
// 🔑 引数の本数が候補の作り方で変わる（candidates だけが $9 = K を使う）ので、
// 文と引数を1つの値にして持ち運ぶ。別々に返すと、片方だけモードを見て
// もう片方が見ていない、という食い違いが書けてしまう。PostgreSQL は
// 「参照されない引数」を型解決できずに Parse で落ちるため、余分に渡すこともできない。
type searchPlan struct {
	statement string
	args      []any
}

// rowQueryer は *sql.DB と *sql.Tx が共通して持つ複数行の問い合わせ口。
//
// 候補モードは hnsw.ef_search を SET LOCAL するためにトランザクションの内側で
// 走り、全探索はプールから直接走る。同じ走査コードを両方から呼ぶために要る。
// database/sql はこの共通部分を型として提供していないので、必要な1メソッドだけを
// 自前で宣言する（queryer と同じ流儀）。
type rowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// searchRows は SQL を実行して結果を組み立てる。
//
// 🔴 全探索の経路にトランザクションを足していない。候補モードだけが
// SET LOCAL を要するのであって、両方を揃えると before/after の「before 側」に
// BEGIN/COMMIT の往復が1回ぶん増える。索引を入れる前後で測るものを変えない
// (docs/adr/0022-indexed-candidate-search.md Decision 3・CLAUDE.md 地雷10)。
func (s *Store) searchRows(ctx context.Context, q index.Query, vector string) ([]index.Result, error) {
	plan, err := s.searchPlan(q, vector)
	if err != nil {
		return nil, err
	}

	if s.searchMode == SearchModeCandidates {
		return s.searchWithinTx(ctx, q, plan)
	}

	return queryResults(ctx, s.db, q, plan)
}

// searchPlan は文と引数を組み立てる。DB には触れない。
func (s *Store) searchPlan(q index.Query, vector string) (searchPlan, error) {
	expression, err := s.lexicalExpression(q.Text)
	if err != nil {
		return searchPlan{}, err
	}

	statement, fusionArg, err := s.fusion.statement(s.searchMode, q.Alpha)
	if err != nil {
		return searchPlan{}, err
	}

	args := []any{
		q.OrgID.Int64(), vector,
		encodeInt64Array(q.DocumentIDs), encodeInt64Array(q.SourceIDs),
		q.Limit, expression, tsRankNormalization, fusionArg,
	}

	// $9 は候補モードの SQL にしか現れない。無条件に渡すと、全探索の側が
	// 「9個渡されたが8個しか要らない」で Parse に失敗する。
	if s.searchMode == SearchModeCandidates {
		args = append(args, s.candidateK)
	}

	return searchPlan{statement: statement, args: args}, nil
}

// searchWithinTx は hnsw.ef_search と plan_cache_mode を効かせた
// トランザクションの中で検索する。
//
// 🔴 SET LOCAL はトランザクション期間の設定である。プールから直接 SET すると
// 「その接続だけが設定を持ち越す」状態になり、次にその接続を掴んだ検索が
// 別の ef_search で走る。どの検索がどの探索幅で走ったかが分からなくなれば、
// 計測の条件は決まらない (ADR 0022 Decision 4)。
//
// 読み取りしかしないが COMMIT する。ROLLBACK で閉じると、失敗していないのに
// 失敗として記録される（統計・ログ）ので、正常終了は正常終了として閉じる。
func (s *Store) searchWithinTx(
	ctx context.Context, q index.Query, plan searchPlan,
) ([]index.Result, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: begin: %s", errSearch, err.Error())
	}

	results, err := s.searchWithLocalSettings(ctx, tx, q, plan)
	if err != nil {
		// 巻き戻しの失敗も握り潰さない（errors.Join は nil を落とす）。
		return nil, errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("%w: commit: %s", errSearch, err.Error())
	}

	return results, nil
}

// searchWithLocalSettings は候補モードの設定を効かせてから検索を走らせる。
func (s *Store) searchWithLocalSettings(
	ctx context.Context, tx *sql.Tx, q index.Query, plan searchPlan,
) ([]index.Result, error) {
	if err := s.applySearchLocals(ctx, tx); err != nil {
		return nil, err
	}

	return queryResults(ctx, tx, q, plan)
}

// applySearchLocals は候補モードがトランザクション期間だけ要する設定を適用する。
//
// 🔴 2つの設定を1箇所にまとめる。検索・EXPLAIN・テストの読み戻しがすべてここを
// 通るので、「検索には効いているが EXPLAIN には効いていない」条件のずれが
// 書けなくなる。計測の条件は経路によって変わってはならない (ADR 0022 Decision 4)。
func (s *Store) applySearchLocals(ctx context.Context, tx *sql.Tx) error {
	if err := s.setEfSearch(ctx, tx); err != nil {
		return err
	}

	return setPlanCacheMode(ctx, tx)
}

// setPlanCacheMode はトランザクション期間だけ計画のキャッシュを切る。
//
// 🔴 これが無いと、6 回目の検索から HNSW を使わなくなる。
//
// database/sql（pgx stdlib）は検索 SQL を**パラメータつきのプリペアド文**として
// 実行する。PostgreSQL は同じプリペアド文が 5 回走った後、汎用計画（引数の値を
// 見ない計画）の見積りが custom 計画の平均を下回れば汎用計画へ切り替える。
// 候補モードの SQL でそれが起きると、ベクトル側の ORDER BY が索引ではなく
// Parallel Seq Scan + Sort になる——2026-09-02 の 10万件の実測で、同じクエリの
// 6 回目が **40 倍**遅くなった（89ms → 3,625ms / 258ms → 4,223ms）。
// 正本は docs/benchmarks/2026-09-02-eval-100k-after-index.md §10。
//
// 🔑 症状は「遅い」だけで、結果は正しいまま返る。⇒ 索引を張ったのに速くならない
// という形でしか表面化せず、EXPLAIN を1回取っただけでは見えない（1回目は custom
// 計画なので索引が現れる）。**設定で塞ぐのが最小の手当てである。**
//
// 却下した代替: pgx の QueryExecModeSimpleProtocol。プリペアド文そのものを
// やめれば汎用計画も生まれないが、ストアの全クエリ（取り込みを含む）の実行方式が
// 変わる。検索1経路の計画のために、測っていない経路まで動かさない。
//
// ⚠️ 候補モードだけに掛かる。全探索の経路にトランザクションを足していないのは
// before/after で測るものを変えないためで、その判断はここでも変えない
// (ADR 0022 Decision 3・CLAUDE.md 地雷10)。全探索の ORDER BY は合成式なので、
// 汎用計画に切り替わっても索引に乗れないことに変わりはない。
func setPlanCacheMode(ctx context.Context, tx *sql.Tx) error {
	// set_config(..., true) は SET LOCAL と同じ意味。SET 文を使わない理由は
	// setEfSearch と同じである。
	const statement = `SELECT set_config('plan_cache_mode', 'force_custom_plan', true)`

	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("%w: set plan_cache_mode: %s", errSearch, err.Error())
	}

	return nil
}

// setEfSearch はトランザクション期間だけ hnsw.ef_search を上書きする。
//
// 🔴 set_config(..., true) は SET LOCAL と同じ意味である。SET 文を使わないのは、
// 設定値をプレースホルダで渡せないためである——文字列連結で組み立てると、
// 数値であることを型ではなく検査で保証することになる。
//
// 🔴 GUC の名前をここ1箇所にしか書かない。綴りを誤ると PostgreSQL は
// 「未知の設定」でエラーにするが、2箇所に書けば片方だけ直す経路ができる。
func (s *Store) setEfSearch(ctx context.Context, tx *sql.Tx) error {
	const setEfSearch = `SELECT set_config('hnsw.ef_search', $1, true)`

	if _, err := tx.ExecContext(ctx, setEfSearch, strconv.Itoa(s.efSearch)); err != nil {
		return fmt.Errorf("%w: set hnsw.ef_search: %s", errSearch, err.Error())
	}

	return nil
}

// queryResults は文を実行し、全行を結果へ組み替える。
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
func queryResults(
	ctx context.Context, db rowQueryer, q index.Query, plan searchPlan,
) (results []index.Result, err error) {
	rows, err := db.QueryContext(ctx, plan.statement, plan.args...)
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

		err := rows.Scan(&r.id, &r.externalID, &r.documentID, &r.sourceID, &r.chunkIndex, &r.content,
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
// 計算し直さないこと。SQL は float8 で、Go は float32 で計算するので結果が
// わずかにずれ、「返ってきた並び順と、返ってきたスコアの大小が食い違う」と
// いう説明のつかない結果になりうる。順位を決めた式と報告する値は同一で
// なければならない。
//
// 🔑 VectorScore と LexicalScore を分けて返すのは、外したときにどちら側が
// 原因かを切り分けるためである（index.Result の doc・要件定義 §3）。
//
// 🔴 返すのは融合に入れる前の「生の」スコアである。方式A はクエリ内の最大値で
// 割った値を合成に使い、方式B は順位に置き換えて使うが、どちらの場合も
// ここに出るのは変換前の値である。理由は2つ。
//
// (1) vector_score と lexical_score の意味を方式によって変えないため。
// 方式ごとに中身が変わると、条件をまたいでレポートを並べたときに読めなくなる。
// OpenAPI もこの2つを「ベクトル類似度」「語彙一致度」として定義している。
// (2) スケールの診断が今回まさに必要だったため。生の値が出ていたからこそ、
// lexical が中央値 0.00016 で vector が 0.44 だと分かった（2026-09-02）。
//
// ⚠️ 対価として、Score が2つのスコアから1行だけでは再計算できない。方式A は
// クエリ内の最大値、方式B は候補集合全体の順位を要するためである。
// これはクエリ全体を見て決まる融合の性質そのもので、隠すと逆に誤解を招く。
// どの方式・どの係数で測ったかはレポートの conditions に記録される。
func (r scannedRow) toResult(q index.Query) index.Result {
	return index.Result{
		Chunk: chunk.Chunk{
			ID: r.id,
			// 🔴 org は列から読み戻さず、問い合わせた org をそのまま入れる。
			// WHERE org_id = $1 で絞った以上、列の値は必ずこれと等しい。
			// 読み戻すと int64 から org.ID への変換が要るが、それは CNF-001 が
			// 禁じている直接変換であり、経路を増やすほど分離は緩む。
			OrgID: q.OrgID,
			// 🔴 外部 id は列から読み戻す。org と違って「問い合わせた値」が
			// 存在しないので、返せるのは保存されている値だけである。
			ExternalID:   nullableInt64(r.externalID),
			DocumentID:   r.documentID,
			SourceID:     r.sourceID,
			ChunkIndex:   r.chunkIndex,
			Content:      r.content,
			PageNumber:   nullableInt(r.pageNumber),
			SectionLabel: nullableString(r.sectionLabel),
		},
		// 🔴 alpha の既定 0.8 は ADR 0015 が実測から選んだ条件付きの値である。
		// 条件（正規化方式・分割器・埋め込みモデル・候補集合の作り方）が変われば
		// 測り直す対象なので、この係数を普遍的な「最適値」として扱わないこと。
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
