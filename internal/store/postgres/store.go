package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// database/sql のドライバ "pgx" を登録する。pgx をネイティブ API ではなく
	// stdlib 経由で使う判断は docs/adr/0011-pgx-stdlib-driver.md にある。
	// blank import なのは、必要なのが init による登録だけだからである。
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/lexical"
)

// driverName は pgx が database/sql に登録するドライバ名。
const driverName = "pgx"

// queryer は *sql.DB と *sql.Tx が共通して持つ問い合わせ口。
//
// embedder_id の検査をトランザクションの内側（Put）と外側（Search）の両方から
// 呼ぶために要る。database/sql はこの共通部分を型として提供していないので、
// 必要な1メソッドだけを自前で宣言する。
//
// 引数が ...any なのは database/sql の署名をそのまま写しているからで、
// 型を any で代用しているわけではない（GO-006 が禁じているのは後者）。
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store は pgvector を載せた PostgreSQL 上の索引。
//
// index.Searcher と index.Writer を実装する。
// ゼロ値は無効である。必ず New を通すこと（GO-003）。
type Store struct {
	db *sql.DB
	// embedder は検索語と本文をベクトルに変換する。
	embedder embed.Embedder
	// embedderID は構築時に固定する。
	//
	// 毎回 embedder.ID() を呼ばないのは、実装が呼び出しごとに違う値を返した場合に
	// 「書いた値」と「照合する値」がずれるのを防ぐため。ストアが1つの
	// 埋め込み空間に属するという不変条件は、構築時に決まって以後変わらない。
	embedderID string
	// tokenizer は本文と検索語を語彙一致のためのトークン列に分割する。
	//
	// 🔴 取り込みと検索で同じものを使う。別のものを使うと、同じ語を書いた
	// はずのチャンクとクエリが別のトークンになり、語彙スコアが常に 0 になる。
	// ストアが1つ持つ形にしてあるのは、その取り違えを型で防ぐためである。
	tokenizer lexical.Tokenizer
	// tokenizerID は構築時に固定する。理由は embedderID と同じ。
	tokenizerID string
	// fusion はベクトルと語彙のスコアを1つの順位にまとめる方式。
	//
	// 🔴 検索ごとではなくストアごとに決める。要件定義 Q-1/Q-3 を決着させる
	// ための計測用のつまみであり、利用者が要求ごとに選ぶものではない
	// （OpenAPI にこの項目は無い）。配線点が1回決めて、そのプロセスの全検索が
	// 同じ条件で走る形にしてある。
	fusion Fusion
	// searchMode は候補集合の作り方。
	//
	// 🔴 fusion と同じく検索ごとではなくストアごとに決める。索引が効く形
	// （candidates）と効かない形（exhaustive）を1プロセスの中で混ぜると、
	// 測った latency がどちらのものか分からなくなる (ADR 0022 Decision 3)。
	searchMode SearchMode
	// candidateK は候補モードの両側 top-K。exhaustive では使われない。
	candidateK int
	// efSearch は候補モードで検索ごとに SET LOCAL する hnsw.ef_search。
	//
	// 🔴 K ≤ efSearch でなければ HNSW は K 件を返せない。この不変条件は
	// New が構築時に確かめる (ADR 0022 Decision 4)。
	efSearch int
}

// Options は Store の組み立て指定。
//
// 🔑 引数を4つ以下に保つための入れ物である (GO-011)。ゼロ値の Options は
// 無効である——Embedder と Tokenizer が nil になるので New が拒否する。
//
// 🔴 SearchMode のゼロ値が SearchModeExhaustive（現行の全探索）になるように
// 並べてある。既定を candidates へ移すのは after の実測を見て別 ADR を書いて
// からであり、うっかり組み立てた Options が新しい経路を選ばないようにする
// (docs/adr/0022-indexed-candidate-search.md Status)。
type Options struct {
	// Embedder は検索語と本文をベクトルに変換する。
	Embedder embed.Embedder
	// Tokenizer は本文と検索語を語彙一致のためのトークン列に分割する。
	Tokenizer lexical.Tokenizer
	// Fusion はベクトルと語彙のスコアを1つの順位にまとめる方式。
	Fusion Fusion
	// SearchMode は候補集合の作り方。
	SearchMode SearchMode
	// CandidateK は候補モードの両側 top-K。
	//
	// 🔴 SearchMode が exhaustive のときは検証されない。K というつまみが
	// その経路には存在しないためで、「使わない値の妥当性」を要求すると
	// 全探索で測るだけの構成が候補モードの都合に付き合わされる。
	CandidateK int
	// EfSearch は候補モードの hnsw.ef_search。CandidateK と同じ扱いである。
	EfSearch int
}

// Open は DSN から接続プールを開き、実際に到達できることを確認する。
//
// sql.Open は接続を張らないので、それだけでは設定ミスが最初のクエリまで
// 表面化しない。起動時に落とすために PingContext まで行う。
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errConnect, err.Error())
	}

	if err := db.PingContext(ctx); err != nil {
		// 到達できないプールを呼び出し側に持たせない。閉じる側の失敗も握り潰さない。
		return nil, errors.Join(fmt.Errorf("%w: %s", errConnect, err.Error()), db.Close())
	}

	return db, nil
}

// New は接続プールと組み立て指定から Store を作る。
//
// 🔴 ここで次元を突き合わせるのが要点である。embedding 列は vector(1024) で
// 固定されており、次元の異なる Embedder を渡した構成は「起動はするが取り込みが
// 全部落ちる」状態になる。設定の誤りは設定を読んだ直後に落とす。
//
// 🔴 Tokenizer も識別子の空を構築時に弾く。tokenizer_id 列は空文字を拒否する
// CHECK を持つので、空のまま進むと取り込みの瞬間に制約違反という分かりにくい
// 形で落ちる。embedderID と同じ扱いにしてある。
//
// 🔴 融合方式・候補の作り方・K と探索幅も構築時に確かめる。Fusion と SearchMode は
// int なので範囲外の値を作ること自体は言語仕様上いつでもできる (GO-003)。
// 検索のたびに失敗する構成を「起動はする」状態にしない。
func New(db *sql.DB, opts Options) (*Store, error) {
	if err := validateIdentity(opts); err != nil {
		return nil, err
	}

	if err := validateRanking(opts); err != nil {
		return nil, err
	}

	return &Store{
		db:          db,
		embedder:    opts.Embedder,
		embedderID:  opts.Embedder.ID(),
		tokenizer:   opts.Tokenizer,
		tokenizerID: opts.Tokenizer.ID(),
		fusion:      opts.Fusion,
		searchMode:  opts.SearchMode,
		candidateK:  opts.CandidateK,
		efSearch:    opts.EfSearch,
	}, nil
}

// validateIdentity は「このストアがどの埋め込み空間・どの分割規則に属するか」を
// 決める2つを確かめる。
//
// 🔑 どちらも構築時に固定して以後変えない値である。実装が呼び出しごとに違う値を
// 返したときに「書いた値」と「照合する値」がずれるのを防ぐため、New が1度だけ
// 読んで Store に写す。
func validateIdentity(opts Options) error {
	if opts.Embedder == nil {
		return fmt.Errorf("%w: embedder is required", errEmbedderID)
	}

	if got := opts.Embedder.Dimensions(); got != vectorDimensions {
		return fmt.Errorf("%w: embedder %q produces %d, column is vector(%d)",
			errEmbedderDimensions, opts.Embedder.ID(), got, vectorDimensions)
	}

	if opts.Embedder.ID() == "" {
		return fmt.Errorf("%w", errEmbedderID)
	}

	if opts.Tokenizer == nil {
		return fmt.Errorf("%w: tokenizer is required", errTokenizerID)
	}

	if opts.Tokenizer.ID() == "" {
		return fmt.Errorf("%w", errTokenizerID)
	}

	return nil
}

// validateRanking は順位付けの条件を確かめる。
//
// 🔴 K と探索幅を見るのは候補モードのときだけである。全探索の経路には
// どちらのつまみも存在せず、そこで妥当性を要求すると「使わない値を正しく
// 埋めないと起動できない」構成になる。⇒ 逆に、候補モードでは必ず見る。
// K > ef_search は HNSW が K 件を返せない構成であり、症状は「recall が
// 少し低い」だけで一切表面化しない (ADR 0022 Decision 4)。
func validateRanking(opts Options) error {
	if !opts.Fusion.valid() {
		return fmt.Errorf("%w: %d", errUnknownFusion, int(opts.Fusion))
	}

	if !opts.SearchMode.valid() {
		return fmt.Errorf("%w: %d", errUnknownSearchMode, int(opts.SearchMode))
	}

	if opts.SearchMode != SearchModeCandidates {
		return nil
	}

	if opts.CandidateK < 1 {
		return fmt.Errorf("%w: candidate K must be at least 1, got %d",
			errCandidateK, opts.CandidateK)
	}

	if opts.CandidateK > opts.EfSearch {
		return fmt.Errorf("%w: candidate K %d exceeds hnsw.ef_search %d; "+
			"HNSW cannot return more rows than ef_search",
			errCandidateK, opts.CandidateK, opts.EfSearch)
	}

	return nil
}

// RankingSettings はこのストアが順位付けに使っている条件を返す。
//
// 🔑 計測レポートに条件を記録するために要る。どの方式・どの係数で測ったかが
// 残らないレポートは、後から条件を特定できないので正本になれない (ADR 0013)。
func (s *Store) RankingSettings() RankingSettings {
	settings := RankingSettings{
		Fusion:              s.fusion.String(),
		Store:               storeName,
		LexicalScorer:       lexicalScorerName,
		TokenizerID:         s.tokenizerID,
		TsRankNormalization: tsRankNormalization,
		RRFK:                RRFK,
		SearchMode:          s.searchMode.String(),
		// 🔴 全探索では K も探索幅も存在しないつまみである。値を入れると
		// 「その条件で測った」と読まれる。測っていないことを記録に書かない
		// （様式 v4 が sqlite の ts_rank_normalization で塞いだのと同じ穴）。
		CandidateK: nil,
		EfSearch:   nil,
	}

	if s.searchMode == SearchModeCandidates {
		// ローカル変数のコピーを指す。フィールドを直接指すと、後から
		// Store が書き換わったときに記録まで動きうる（実際には書き換えないが、
		// 「動かない」ことを型で示せる書き方を選ぶ）。
		k, ef := s.candidateK, s.efSearch
		settings.CandidateK = &k
		settings.EfSearch = &ef
	}

	return settings
}

// Ping は DB へ到達できることを確かめる。/readyz から呼ぶ。
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: %s", errConnect, err.Error())
	}

	return nil
}

// Close は接続プールを閉じる。
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("%w: %s", errConnect, err.Error())
	}

	return nil
}

// assertSameEmbedderAndTokenizer は、保存済みの行がすべて現在の Embedder と
// Tokenizer のものかを確かめる。
//
// 🔴 検索側で WHERE embedder_id = $current と黙って絞り込む実装にしないこと。
// 不一致の行が「検索に出てこないだけ」になり、ADR 0005 が警告する静かな破損
// （エラーにならないまま無意味な結果が返る）の変種になる。必ずエラーにする。
// 分割器についても同じで、規則の違うトークン列が混ざったときの症状は
// 「語彙スコアが少し低い」だけであり、単一の分割器で開発している限り
// 一切表面化しない。
//
// 🔴 2つの検査を1本の SQL にまとめてある。分けると検索1回あたりの往復が
// 3回になり、基準線（p95 3.3ms・埋め込みを除く）との比較に検査の往復が
// 混ざる。測っているものを変えずに検査だけを足すための形である。
// どちらの列も `<>` の比較なので索引は使えず、まとめても計画は変わらない。
//
// 検査は org を跨いで全行を対象にする。「ストア全体が単一の埋め込み空間・
// 単一の分割規則である」という不変条件はテナントより上位の性質であり、
// org ごとに別のモデルを混ぜてよいという意味ではないため。
func (s *Store) assertSameEmbedderAndTokenizer(ctx context.Context, q queryer) error {
	const stmt = `SELECT embedder_id, tokenizer_id FROM chunks
WHERE embedder_id <> $1 OR tokenizer_id <> $2
LIMIT 1`

	var storedEmbedder, storedTokenizer string

	err := q.QueryRowContext(ctx, stmt, s.embedderID, s.tokenizerID).
		Scan(&storedEmbedder, &storedTokenizer)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil // 不一致の行が無い。空のストアもここに来る
	case err != nil:
		return fmt.Errorf("%w: embedder and tokenizer check: %s", errConnect, err.Error())
	case storedEmbedder != s.embedderID:
		// 両方が食い違う行もありうる。埋め込みを先に報告するのは、
		// ベクトルが比較できない状態のほうが被害が大きく、取り込み直しの
		// 判断もそちらが決めるためである。
		return fmt.Errorf("postgres: stored=%s current=%s: %w",
			storedEmbedder, s.embedderID, index.ErrEmbedderMismatch)
	default:
		return fmt.Errorf("postgres: stored=%s current=%s: %w",
			storedTokenizer, s.tokenizerID, index.ErrTokenizerMismatch)
	}
}
