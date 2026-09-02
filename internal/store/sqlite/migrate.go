// マイグレーションは forward-only である。down を書かない。
//
// 🔴 なぜ down を書かないか: 巻き戻しの SQL は、書いた時点では検証できず、
// 必要になったとき（本番で失敗している最中）に初めて実行される。つまり
// 「一度も試されていないコードを、最も余裕の無い瞬間に走らせる」ことになる。
// 前進のみに固定し、誤ったマイグレーションは次のマイグレーションで直す。
//
// 🔴 なぜ外部のマイグレーションツールを入れないか: ここで要る機能は
// 「未適用の SQL を順に、各々トランザクションで適用し、適用済みを記録する」
// だけで、標準ライブラリで足りる。依存を1本足すごとに ADR が要る（ARC-004）
// 以上、この行数のものに外部ツールと ADR を払わない。
// internal/store/postgres/migrate.go と同じ判断であり、形も揃えてある。
//
// 🔴 postgres 側との唯一の違いは advisory lock が無いことである。SQLite に
// 同等の仕組みは無いが、必要でもない——_txlock=immediate で BeginTx が
// BEGIN IMMEDIATE を打つので、トランザクションを開いた時点でファイル全体の
// 書きロックを取っている。適用済みの確認と適用が同じロックの下で行われる、
// という advisory lock の目的はそのまま満たされる。

package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
)

// migrationDir は埋め込んだ SQL の置き場。
const migrationDir = "migrations"

// migrationFiles は適用する SQL をバイナリに埋め込む。
//
// 埋め込むのは、実行ファイルと SQL がずれないようにするため。別ファイルとして
// 配ると「バイナリは新しいが SQL は古い」構成が作れてしまう。
//
// gochecknoglobals は go:embed 付きの embed.FS を対象外にしている（実測）。
// 抑制は不要なので付けない。
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate は未適用のマイグレーションを名前順に適用する。
//
// 何度呼んでも安全である（適用済みは schema_migrations で飛ばす）。
// 起動のたびに呼ぶ前提の設計にしてある。
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.ensureMigrationTable(ctx); err != nil {
		return err
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		if err := s.applyMigration(ctx, name); err != nil {
			return err
		}
	}

	return nil
}

// ensureMigrationTable は適用記録の置き場を用意する。
func (s *Store) ensureMigrationTable(ctx context.Context) error {
	const ddl = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`

	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("%w: schema_migrations: %s", errMigrate, err.Error())
	}

	return nil
}

// migrationNames は埋め込まれた SQL のファイル名を辞書順で返す。
//
// 番号を接頭辞にする命名（0001_...）を前提に、辞書順＝適用順とする。
// fs.ReadDir も整列して返すが、順序が仕様の中心なので明示的に整列する。
func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFiles, migrationDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errMigrate, err.Error())
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	slices.Sort(names)

	return names, nil
}

// applyMigration は1ファイルを1トランザクションで適用する。
//
// 適用済みの確認と適用を同じトランザクションで行う。分けると、2つのプロセスが
// 同時に「未適用」と判定して二重に適用しうる。
func (s *Store) applyMigration(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %s: %s", errMigrate, name, err.Error())
	}

	if err := applyWithinTx(ctx, tx, name); err != nil {
		// 巻き戻しの失敗も握り潰さない（errors.Join は nil を落とす）。
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %s: %s", errMigrate, name, err.Error())
	}

	return nil
}

// applyWithinTx は未適用なら適用して記録する。
func applyWithinTx(ctx context.Context, tx *sql.Tx, name string) error {
	var applied bool

	const check = `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = ?)`
	if err := tx.QueryRowContext(ctx, check, name).Scan(&applied); err != nil {
		return fmt.Errorf("%w: %s: check: %s", errMigrate, name, err.Error())
	}

	if applied {
		return nil
	}

	statements, err := migrationFiles.ReadFile(migrationDir + "/" + name)
	if err != nil {
		return fmt.Errorf("%w: %s: read: %s", errMigrate, name, err.Error())
	}

	// 🔑 1ファイルを1回の ExecContext に渡す。modernc.org/sqlite は複数文を
	// 順に実行し、trigger の本体に含まれる ; も正しく扱う（実測確認済み）。
	// 自前で ; で分割すると、まさにその trigger の本体で割れる。
	if _, err := tx.ExecContext(ctx, string(statements)); err != nil {
		return fmt.Errorf("%w: %s: apply: %s", errMigrate, name, err.Error())
	}

	const record = `INSERT INTO schema_migrations (version) VALUES (?)`
	if _, err := tx.ExecContext(ctx, record, name); err != nil {
		return fmt.Errorf("%w: %s: record: %s", errMigrate, name, err.Error())
	}

	return nil
}
