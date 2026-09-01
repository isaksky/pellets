// Package sqlite owns the SQLite runtime configuration and schema migrations.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"pellets/internal/domain"

	_ "modernc.org/sqlite"
)

const (
	// LatestSchemaVersion is the newest schema understood by this executable.
	LatestSchemaVersion     = 4
	driverName              = "sqlite"
	busyTimeoutMilliseconds = 5000
)

//go:embed migrations/0001_initial.sql
var migration1SQL string

//go:embed migrations/0002_database_identity.sql
var migration2SQL string

//go:embed migrations/0003_project_workspaces.sql
var migration3SQL string

//go:embed migrations/0004_project_code_redirects.sql
var migration4SQL string

type migration struct {
	version    int
	name       string
	sql        string
	assert     func(context.Context, *sql.Conn) error
	preflight  func(context.Context, *sql.Conn) error
	ftsIndexes []string
}

type migrationHooks struct {
	beforeLock func(observedVersion int) error
}

// Shipped migration SQL is immutable. Compatibility is verified from released
// database fixtures rather than by storing migration metadata in the database.
var migrations = []migration{
	{version: 1, name: "initial", sql: migration1SQL, assert: assertMigration1, preflight: preflightMigration1, ftsIndexes: []string{"pellets_fts", "memories_fts"}},
	{version: 2, name: "database-identity", sql: migration2SQL, assert: assertMigration2, preflight: preflightMigration2, ftsIndexes: []string{"pellets_fts", "memories_fts"}},
	{version: 3, name: "project-workspaces", sql: migration3SQL, assert: assertMigration3, preflight: preflightMigration3, ftsIndexes: []string{"pellets_fts", "memories_fts"}},
	{version: 4, name: "project-code-redirects", sql: migration4SQL, assert: assertMigration4, preflight: preflightMigration4, ftsIndexes: []string{"pellets_fts", "memories_fts"}},
}

// Open opens path with the required hardened runtime settings and applies all
// pending migrations. The returned pool intentionally owns one connection.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	latest, err := validateMigrations(migrations)
	if err != nil {
		return nil, migrationError(err)
	}
	if latest != LatestSchemaVersion {
		return nil, migrationError(fmt.Errorf(
			"latest embedded migration is %d, but LatestSchemaVersion is %d",
			latest, LatestSchemaVersion,
		))
	}
	return openWithMigrations(ctx, path, migrations)
}

func openWithMigrations(ctx context.Context, path string, sequence []migration) (*sql.DB, error) {
	return openWithMigrationHooks(ctx, path, sequence, migrationHooks{})
}

func openWithMigrationHooks(ctx context.Context, path string, sequence []migration, hooks migrationHooks) (*sql.DB, error) {
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

	if err := prepare(ctx, db, sequence, hooks); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func dataSourceName(path string) (string, error) {
	u, err := absoluteFileURL(path)
	if err != nil {
		return "", err
	}
	query := u.Query()
	// These settings are connection-local, so the driver reapplies them if
	// database/sql ever has to replace the single idle connection.
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMilliseconds))
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Add("_pragma", "trusted_schema(OFF)")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func absoluteFileURL(path string) (*url.URL, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}

	uriPath := filepath.ToSlash(absolute)
	if runtime.GOOS == "windows" && filepath.VolumeName(absolute) != "" && !strings.HasPrefix(uriPath, "/") {
		// SQLite requires an absolute Windows drive path in a file URI to begin
		// with /X:/. Without the leading slash, file:C:/... is a relative URI
		// and fails to open outside the process working directory.
		uriPath = "/" + uriPath
	}
	return &url.URL{Scheme: "file", Path: uriPath}, nil
}

func prepare(ctx context.Context, db *sql.DB, sequence []migration, hooks migrationHooks) error {
	latest, err := validateMigrations(sequence)
	if err != nil {
		return migrationError(err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		if stable := stableDatabaseError("open database connection", err); stable != nil {
			return stable
		}
		return domain.WrapError(domain.Storage, "database_open_failed", "could not open database", nil, err)
	}
	defer conn.Close()

	version, err := inspectDatabaseSchema(ctx, conn, sequence, latest)
	if err != nil {
		return err
	}

	if err := ensureWALJournalMode(ctx, conn); err != nil {
		return err
	}
	if err := verifyFTS5(ctx, conn); err != nil {
		return err
	}

	if version == latest {
		if err := verifyForeignKeys(ctx, conn); err != nil {
			return migrationError(err)
		}
		return verifyRuntime(ctx, conn)
	}
	if hooks.beforeLock != nil {
		if err := hooks.beforeLock(version); err != nil {
			return migrationError(fmt.Errorf("before migration lock: %w", err))
		}
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
	if err := validateSchemaVersion(version, latest); err != nil {
		return err
	}
	if err := validateUninitializedDatabase(ctx, conn, version, latest); err != nil {
		return err
	}
	if err := verifyCurrentSchema(ctx, conn, sequence, version, latest); err != nil {
		return err
	}
	if err := applyMigrations(ctx, conn, sequence, version); err != nil {
		return migrationError(err)
	}
	if err := verifyForeignKeys(ctx, conn); err != nil {
		return migrationError(err)
	}
	if err := verifyMigrationSearchIndexes(ctx, conn, sequence); err != nil {
		return migrationError(err)
	}
	if err := verifyDatabaseIntegrity(ctx, conn); err != nil {
		return err
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

func inspectDatabaseSchema(ctx context.Context, conn *sql.Conn, sequence []migration, latest int) (int, error) {
	// One deferred read transaction keeps user_version, sqlite_schema, and the
	// integrity/contract checks on the same snapshot while another process may
	// be completing initialization or a migration.
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return 0, migrationError(fmt.Errorf("begin schema inspection: %w", err))
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	version, err := currentSchemaVersion(ctx, conn)
	if err != nil {
		return 0, migrationError(err)
	}
	if err := validateSchemaVersion(version, latest); err != nil {
		return 0, err
	}
	if err := validateUninitializedDatabase(ctx, conn, version, latest); err != nil {
		return 0, err
	}
	// This diagnostic precedes every persistent PRAGMA or migration write, so a
	// corrupt compatible-looking file is rejected unchanged.
	if err := verifyDatabaseIntegrity(ctx, conn); err != nil {
		return 0, err
	}
	if err := verifyCurrentSchema(ctx, conn, sequence, version, latest); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, migrationError(fmt.Errorf("commit schema inspection: %w", err))
	}
	committed = true
	return version, nil
}

func currentSchemaVersion(ctx context.Context, conn *sql.Conn) (int, error) {
	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read PRAGMA user_version: %w", err)
	}
	return version, nil
}

func validateMigrations(sequence []migration) (int, error) {
	if len(sequence) == 0 {
		return 0, errors.New("embedded migration sequence is empty")
	}
	for index, migration := range sequence {
		expected := index + 1
		if migration.version != expected {
			return 0, fmt.Errorf(
				"embedded migration at index %d has version %d, want %d",
				index, migration.version, expected,
			)
		}
	}
	return sequence[len(sequence)-1].version, nil
}

func validateSchemaVersion(version, latest int) error {
	if version < 0 {
		return domain.NewError(
			domain.Storage,
			"schema_version_invalid",
			"database schema version is invalid",
			map[string]any{"database_version": version, "supported_version": latest},
		)
	}
	if version > latest {
		return schemaTooNew(version, latest)
	}
	return nil
}

func validateUninitializedDatabase(ctx context.Context, conn *sql.Conn, version, latest int) error {
	if version != 0 {
		return nil
	}

	var objectCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema").Scan(&objectCount); err != nil {
		return migrationError(fmt.Errorf("inspect version-0 schema: %w", err))
	}
	if objectCount != 0 {
		return domain.NewError(
			domain.Storage,
			"schema_version_unsupported",
			"database schema version is unsupported",
			map[string]any{"database_version": version, "supported_version": latest},
		)
	}
	return nil
}

func applyMigrations(ctx context.Context, conn *sql.Conn, sequence []migration, current int) error {
	for _, migration := range sequence {
		if migration.version <= current {
			continue
		}
		if _, err := conn.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
		}
		if migration.assert != nil {
			if err := migration.assert(ctx, conn); err != nil {
				return fmt.Errorf("assert migration %d (%s): %w", migration.version, migration.name, err)
			}
		}
		if err := setSchemaVersion(ctx, conn, migration.version); err != nil {
			return fmt.Errorf("advance user_version after migration %d (%s): %w", migration.version, migration.name, err)
		}
		current = migration.version
	}
	return nil
}

func setSchemaVersion(ctx context.Context, conn *sql.Conn, version int) error {
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("set PRAGMA user_version to %d: %w", version, err)
	}
	got, err := currentSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	if got != version {
		return fmt.Errorf("PRAGMA user_version is %d after setting it to %d", got, version)
	}
	return nil
}

func assertMigration1(ctx context.Context, conn *sql.Conn) error {
	return verifyProductionSchemaContract(ctx, conn, 1)
}

func preflightMigration1(ctx context.Context, conn *sql.Conn) error {
	return verifyProductionSchemaContract(ctx, conn, 1)
}

func assertMigration2(ctx context.Context, conn *sql.Conn) error {
	if err := verifyProductionSchemaContract(ctx, conn, 2); err != nil {
		return err
	}
	return assertMigration2Metadata(ctx, conn)
}

func preflightMigration2(ctx context.Context, conn *sql.Conn) error {
	if err := verifyProductionSchemaContract(ctx, conn, 2); err != nil {
		return err
	}
	return assertMigration2Metadata(ctx, conn)
}

func assertMigration2Metadata(ctx context.Context, conn *sql.Conn) error {
	return assertConnectionRowCount(ctx, conn, `
		SELECT COUNT(*)
		FROM application_metadata
		WHERE key IN ('database_id', 'created_at_julian', 'product')`, 3)
}

func assertMigration3(ctx context.Context, conn *sql.Conn) error {
	return assertMigration3Schema(ctx, conn)
}

func preflightMigration3(ctx context.Context, conn *sql.Conn) error {
	return assertMigration3Schema(ctx, conn)
}

func assertMigration4(ctx context.Context, conn *sql.Conn) error {
	return assertMigration4Schema(ctx, conn)
}

func preflightMigration4(ctx context.Context, conn *sql.Conn) error {
	return assertMigration4Schema(ctx, conn)
}

func assertMigration4Schema(ctx context.Context, conn *sql.Conn) error {
	if err := verifyProductionSchemaContract(ctx, conn, 4); err != nil {
		return err
	}
	if err := assertConnectionRowCount(ctx, conn, `
		SELECT COUNT(*)
		FROM project_code_redirects AS redirect
		JOIN projects AS project ON project.code = redirect.code`, 0); err != nil {
		return fmt.Errorf("verify unambiguous project-code namespace: %w", err)
	}
	return nil
}

func assertMigration3Schema(ctx context.Context, conn *sql.Conn) error {
	if err := verifyProductionSchemaContract(ctx, conn, 3); err != nil {
		return err
	}
	if err := assertConnectionRowCount(ctx, conn, `
		SELECT COUNT(*)
		FROM pellets
		WHERE (status = 'in_progress') <> (workspace_id IS NOT NULL)`, 0); err != nil {
		return fmt.Errorf("verify migrated workspace ownership: %w", err)
	}
	if err := assertConnectionRowCount(ctx, conn, `
		SELECT COUNT(*) FROM application_metadata
		WHERE key IN ('database_id', 'created_at_julian', 'product')`, 3); err != nil {
		return fmt.Errorf("verify database identity metadata: %w", err)
	}
	return nil
}

func assertConnectionRowCount(ctx context.Context, conn *sql.Conn, query string, want int, args ...any) error {
	var got int
	if err := conn.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("query row count = %d, want %d", got, want)
	}
	return nil
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

func verifyDatabaseIntegrity(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		if stable := stableDatabaseError("verify database integrity", err); stable != nil {
			return stable
		}
		return databaseIntegrityError(err)
	}
	defer rows.Close()

	var diagnostics []string
	for rows.Next() {
		var diagnostic string
		if err := rows.Scan(&diagnostic); err != nil {
			return databaseIntegrityError(err)
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	if err := rows.Err(); err != nil {
		if stable := stableDatabaseError("verify database integrity", err); stable != nil {
			return stable
		}
		return databaseIntegrityError(err)
	}
	if len(diagnostics) != 1 || !strings.EqualFold(diagnostics[0], "ok") {
		return databaseCorruptError(
			"verify database integrity",
			fmt.Errorf("PRAGMA integrity_check reported: %s", strings.Join(diagnostics, "; ")),
		)
	}
	return nil
}

func ensureWALJournalMode(ctx context.Context, conn *sql.Conn) error {
	deadline := time.Now().Add(time.Duration(busyTimeoutMilliseconds) * time.Millisecond)
	var lastBusy error
	for {
		var journalMode string
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
			if stable := stableDatabaseError("read WAL journal mode", err); stable == nil || domain.PublicError(stable).Code != "database_busy" {
				return runtimeError("read WAL journal mode", err)
			}
			lastBusy = err
		} else if strings.EqualFold(journalMode, "wal") {
			return nil
		} else if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
			if stable := stableDatabaseError("set WAL journal mode", err); stable == nil || domain.PublicError(stable).Code != "database_busy" {
				return runtimeError("set WAL journal mode", err)
			}
			lastBusy = err
		} else if strings.EqualFold(journalMode, "wal") {
			return nil
		}

		if time.Now().After(deadline) {
			if lastBusy != nil {
				return runtimeError("set WAL journal mode", lastBusy)
			}
			return runtimeError("set WAL journal mode", fmt.Errorf("journal mode did not become WAL before the busy timeout"))
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func verifyCurrentSchema(ctx context.Context, conn *sql.Conn, sequence []migration, version, latest int) error {
	if version == 0 {
		return nil
	}
	if version > len(sequence) || sequence[version-1].version != version {
		return domain.NewError(
			domain.Storage,
			"schema_version_unsupported",
			"database schema version is unsupported",
			map[string]any{"database_version": version, "supported_version": latest},
		)
	}
	preflight := sequence[version-1].preflight
	if preflight == nil {
		return nil
	}
	if err := preflight(ctx, conn); err != nil {
		if stable := stableDatabaseError("validate database schema", err); stable != nil {
			return stable
		}
		return domain.WrapError(
			domain.Storage,
			"schema_version_unsupported",
			"database schema version is unsupported",
			map[string]any{"database_version": version, "supported_version": latest},
			err,
		)
	}
	return nil
}

func verifyMigrationSearchIndexes(ctx context.Context, conn *sql.Conn, sequence []migration) error {
	if len(sequence) == 0 {
		return errors.New("cannot verify search indexes for an empty migration sequence")
	}
	for _, name := range sequence[len(sequence)-1].ftsIndexes {
		if err := verifyExternalContentFTSIndex(ctx, conn, name); err != nil {
			return fmt.Errorf("verify migrated FTS index %q: %w", name, err)
		}
	}
	return nil
}

func verifyExternalContentFTSIndex(ctx context.Context, conn *sql.Conn, name string) error {
	if name == "" || strings.IndexFunc(name, func(value rune) bool {
		return value != '_' && (value < 'a' || value > 'z') && (value < 'A' || value > 'Z') && (value < '0' || value > '9')
	}) >= 0 {
		return fmt.Errorf("invalid FTS table name %q", name)
	}
	statement := fmt.Sprintf(
		"INSERT INTO %s(%s, rank) VALUES ('integrity-check', 1)",
		name,
		name,
	)
	_, err := conn.ExecContext(ctx, statement)
	return err
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
		{pragma: "busy_timeout", want: busyTimeoutMilliseconds},
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

func schemaTooNew(version, latest int) error {
	return domain.NewError(
		domain.Storage,
		"schema_too_new",
		"database schema is newer than this executable",
		map[string]any{"database_version": version, "supported_version": latest},
	)
}

func migrationError(err error) error {
	if stable := stableDatabaseError("migrate database", err); stable != nil {
		return stable
	}
	return domain.WrapError(domain.Storage, "database_migration_failed", "could not migrate database", nil, err)
}

func runtimeError(action string, err error) error {
	if stable := stableDatabaseError(action, err); stable != nil {
		return stable
	}
	return domain.WrapError(domain.Storage, "database_configuration_failed", "could not configure database", nil, fmt.Errorf("%s: %w", action, err))
}
