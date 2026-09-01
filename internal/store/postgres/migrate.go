// マイグレーションは forward-only である。down を書かない。
//
// 🔴 なぜ down を書かないか: 巻き戻しの SQL は、書いた時点では検証できず、
// 必要になったとき（本番で失敗している最中）に初めて実行される。つまり
// 「一度も試されていないコードを、最も余裕の無い瞬間に走らせる」ことになる。
// 前進のみに固定し、誤ったマイグレーションは次のマイグレーションで直す。
//
// 🔴 なぜ golang-migrate / goose / atlas を入れないか: ここで要る機能は
// 「未適用の SQL を順に、各々トランザクションで適用し、適用済みを記録する」
// だけで、標準ライブラリで足りる。依存を1本足すごとに ADR が要る（ARC-004）
// 以上、60行で済むものに外部ツールと ADR を払わない。tools/conformance が
// golang.org/x/tools を入れずに済ませたのと同じ判断軸である
// (docs/adr/0010-strictness-is-mechanically-enforced.md)。

package postgres

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

// migrateAdvisoryLockKey は適用を直列化するためのキー。
//
// 値は ASCII の "recall"。他のアプリケーションが同じ DB で別のキーを使っても
// 衝突しないよう、意味のある固定値にしてある。
const migrateAdvisoryLockKey int64 = 0x726563616c6c

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
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
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
// 適用済みの確認と適用を同じトランザクション・同じ advisory lock の下で行う。
// 分けると、2つのプロセスが同時に「未適用」と判定して二重に適用しうる。
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

// applyWithinTx はロックを取り、未適用なら適用して記録する。
func applyWithinTx(ctx context.Context, tx *sql.Tx, name string) error {
	// トランザクション期間のロック。COMMIT / ROLLBACK で自動的に解放されるので、
	// 解放漏れで DB を固める経路が構造的に無い。
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateAdvisoryLockKey); err != nil {
		return fmt.Errorf("%w: %s: lock: %s", errMigrate, name, err.Error())
	}

	var applied bool

	const check = `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`
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

	if _, err := tx.ExecContext(ctx, string(statements)); err != nil {
		return fmt.Errorf("%w: %s: apply: %s", errMigrate, name, err.Error())
	}

	const record = `INSERT INTO schema_migrations (version) VALUES ($1)`
	if _, err := tx.ExecContext(ctx, record, name); err != nil {
		return fmt.Errorf("%w: %s: record: %s", errMigrate, name, err.Error())
	}

	return nil
}
