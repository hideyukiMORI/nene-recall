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

// New は接続プールと Embedder から Store を組み立てる。
//
// 🔴 ここで次元を突き合わせるのが要点である。embedding 列は vector(1024) で
// 固定されており、次元の異なる Embedder を渡した構成は「起動はするが取り込みが
// 全部落ちる」状態になる。設定の誤りは設定を読んだ直後に落とす。
func New(db *sql.DB, e embed.Embedder) (*Store, error) {
	if got := e.Dimensions(); got != vectorDimensions {
		return nil, fmt.Errorf("%w: embedder %q produces %d, column is vector(%d)",
			errEmbedderDimensions, e.ID(), got, vectorDimensions)
	}

	id := e.ID()
	if id == "" {
		return nil, fmt.Errorf("%w", errEmbedderID)
	}

	return &Store{db: db, embedder: e, embedderID: id}, nil
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

// assertSameEmbedder は、保存済みの行がすべて現在の Embedder のものかを確かめる。
//
// 🔴 検索側で WHERE embedder_id = $current と黙って絞り込む実装にしないこと。
// 不一致の行が「検索に出てこないだけ」になり、ADR 0005 が警告する静かな破損
// （エラーにならないまま無意味な結果が返る）の変種になる。必ずエラーにする。
//
// 検査は org を跨いで全行を対象にする。「ストア全体が単一の埋め込み空間である」
// という不変条件はテナントより上位の性質であり、org ごとに別のモデルを混ぜてよい
// という意味ではないため。
func (s *Store) assertSameEmbedder(ctx context.Context, q queryer) error {
	const stmt = `SELECT embedder_id FROM chunks WHERE embedder_id <> $1 LIMIT 1`

	var stored string

	err := q.QueryRowContext(ctx, stmt, s.embedderID).Scan(&stored)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil // 不一致の行が無い。空のストアもここに来る
	case err != nil:
		return fmt.Errorf("%w: embedder check: %s", errConnect, err.Error())
	default:
		return fmt.Errorf("postgres: stored=%s current=%s: %w",
			stored, s.embedderID, index.ErrEmbedderMismatch)
	}
}
