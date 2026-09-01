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

// New は接続プールと Embedder・Tokenizer から Store を組み立てる。
//
// 🔴 ここで次元を突き合わせるのが要点である。embedding 列は vector(1024) で
// 固定されており、次元の異なる Embedder を渡した構成は「起動はするが取り込みが
// 全部落ちる」状態になる。設定の誤りは設定を読んだ直後に落とす。
//
// 🔴 Tokenizer も識別子の空を構築時に弾く。tokenizer_id 列は空文字を拒否する
// CHECK を持つので、空のまま進むと取り込みの瞬間に制約違反という分かりにくい
// 形で落ちる。embedderID と同じ扱いにしてある。
func New(db *sql.DB, e embed.Embedder, t lexical.Tokenizer) (*Store, error) {
	if got := e.Dimensions(); got != vectorDimensions {
		return nil, fmt.Errorf("%w: embedder %q produces %d, column is vector(%d)",
			errEmbedderDimensions, e.ID(), got, vectorDimensions)
	}

	id := e.ID()
	if id == "" {
		return nil, fmt.Errorf("%w", errEmbedderID)
	}

	if t == nil {
		return nil, fmt.Errorf("%w: tokenizer is required", errTokenizerID)
	}

	tokenizerID := t.ID()
	if tokenizerID == "" {
		return nil, fmt.Errorf("%w", errTokenizerID)
	}

	return &Store{db: db, embedder: e, embedderID: id, tokenizer: t, tokenizerID: tokenizerID}, nil
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
