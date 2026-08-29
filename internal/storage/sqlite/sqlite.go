// Package sqlite owns the SQLite runtime configuration and schema migrations.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"pellets/internal/domain"

	_ "modernc.org/sqlite"
)

const (
	// LatestSchemaVersion is the newest schema understood by this executable.
	LatestSchemaVersion = 1
	driverName          = "sqlite"
)

//go:embed migrations/0001_initial.sql
var migration1SQL string

type migration struct {
	version int
	name    string
	sql     string
}

var migrations = []migration{
	{version: 1, name: "initial", sql: migration1SQL},
}

// Open opens path with the required hardened runtime settings and applies all
// pending migrations. The returned pool intentionally owns one connection.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn, err := dataSourceName(path)
	if err != nil {
		return nil, domain.WrapError(domain.Storage, "database_open_failed", "could not open database", nil, err)
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, domain.WrapError(domain.Storage, "database_open_failed", "could not open database", nil, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := prepare(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func dataSourceName(path string) (string, error) {
	if path == "" {
		return "", errors.New("database path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}

	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	query := u.Query()
	// These settings are connection-local, so the driver reapplies them if
	// database/sql ever has to replace the single idle connection.
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "trusted_schema(OFF)")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func prepare(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return domain.WrapError(domain.Storage, "database_open_failed", "could not open database", nil, err)
	}
	defer conn.Close()

	// Inspect the version before any persistent PRAGMA or migration write. This
	// guarantees that a database from a newer executable is rejected unchanged.
	version, err := currentSchemaVersion(ctx, conn)
	if err != nil {
		return migrationError(err)
	}
	if version > LatestSchemaVersion {
		return schemaTooNew(version)
	}

	var journalMode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return runtimeError("set WAL journal mode", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return runtimeError("verify WAL journal mode", fmt.Errorf("journal_mode is %q", journalMode))
	}
	if err := verifyFTS5(ctx, conn); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return migrationError(fmt.Errorf("begin immediate: %w", err))
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Re-read under the migration lock in case another process migrated between
	// the initial read and BEGIN IMMEDIATE.
	version, err = currentSchemaVersion(ctx, conn)
	if err != nil {
		return migrationError(err)
	}
	if version > LatestSchemaVersion {
		return schemaTooNew(version)
	}
	if err := applyMigrations(ctx, conn, version); err != nil {
		return migrationError(err)
	}
	if err := verifyForeignKeys(ctx, conn); err != nil {
		return migrationError(err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return migrationError(fmt.Errorf("commit: %w", err))
	}
	committed = true

	if err := verifyRuntime(ctx, conn); err != nil {
		return err
	}
	return nil
}

func currentSchemaVersion(ctx context.Context, conn *sql.Conn) (int, error) {
	var exists int
	if err := conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM sqlite_schema
			WHERE type = 'table' AND name = 'schema_migrations'
		)`).Scan(&exists); err != nil {
		return 0, fmt.Errorf("inspect migration table: %w", err)
	}
	if exists == 0 {
		return 0, nil
	}

	var version int
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func applyMigrations(ctx context.Context, conn *sql.Conn, current int) error {
	for _, migration := range migrations {
		if migration.version <= current {
			if err := verifyAppliedMigration(ctx, conn, migration); err != nil {
				return err
			}
			continue
		}
		if migration.version != current+1 {
			return fmt.Errorf("migration sequence jumps from %d to %d", current, migration.version)
		}
		if _, err := conn.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO schema_migrations(version, name, checksum, applied_at)
			VALUES (?, ?, ?, julianday('now'))`,
			migration.version, migration.name, migrationChecksum(migration),
		); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", migration.version, migration.name, err)
		}
		current = migration.version
	}
	return nil
}

func verifyAppliedMigration(ctx context.Context, conn *sql.Conn, migration migration) error {
	var name, checksum string
	if err := conn.QueryRowContext(ctx,
		"SELECT name, checksum FROM schema_migrations WHERE version = ?",
		migration.version,
	).Scan(&name, &checksum); err != nil {
		return fmt.Errorf("read migration %d: %w", migration.version, err)
	}
	if name != migration.name || checksum != migrationChecksum(migration) {
		return fmt.Errorf("migration %d metadata does not match the executable", migration.version)
	}
	return nil
}

func migrationChecksum(migration migration) string {
	sum := sha256.Sum256([]byte(migration.sql))
	return hex.EncodeToString(sum[:])
}

func verifyForeignKeys(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run foreign key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID, parent any
		var foreignKey int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			return fmt.Errorf("read foreign key violation: %w", err)
		}
		return fmt.Errorf("foreign key violation in table %q", table)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read foreign key check: %w", err)
	}
	return nil
}

func verifyFTS5(ctx context.Context, conn *sql.Conn) error {
	var sourceID string
	if err := conn.QueryRowContext(ctx, "SELECT fts5_source_id()").Scan(&sourceID); err != nil {
		return domain.WrapError(domain.Storage, "fts_unavailable", "SQLite FTS5 is unavailable", nil, err)
	}
	if sourceID == "" {
		return domain.NewError(domain.Storage, "fts_unavailable", "SQLite FTS5 is unavailable", nil)
	}
	return nil
}

func verifyRuntime(ctx context.Context, conn *sql.Conn) error {
	checks := []struct {
		pragma string
		want   int
	}{
		{pragma: "foreign_keys", want: 1},
		{pragma: "trusted_schema", want: 0},
		{pragma: "synchronous", want: 2},
		{pragma: "busy_timeout", want: 5000},
	}
	for _, check := range checks {
		var got int
		if err := conn.QueryRowContext(ctx, "PRAGMA "+check.pragma).Scan(&got); err != nil {
			return runtimeError("read PRAGMA "+check.pragma, err)
		}
		if got != check.want {
			return runtimeError("verify PRAGMA "+check.pragma, fmt.Errorf("got %d, want %d", got, check.want))
		}
	}

	var journalMode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return runtimeError("read PRAGMA journal_mode", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return runtimeError("verify PRAGMA journal_mode", fmt.Errorf("got %q, want WAL", journalMode))
	}
	return nil
}

func schemaTooNew(version int) error {
	return domain.NewError(
		domain.Storage,
		"schema_too_new",
		"database schema is newer than this executable",
		map[string]any{"database_version": version, "supported_version": LatestSchemaVersion},
	)
}

func migrationError(err error) error {
	return domain.WrapError(domain.Storage, "database_migration_failed", "could not migrate database", nil, err)
}

func runtimeError(action string, err error) error {
	return domain.WrapError(domain.Storage, "database_configuration_failed", "could not configure database", nil, fmt.Errorf("%s: %w", action, err))
}
