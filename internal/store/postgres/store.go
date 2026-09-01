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
)

// driverName は pgx が database/sql に登録するドライバ名。
const driverName = "pgx"

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
