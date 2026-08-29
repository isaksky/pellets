package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"unicode"
)

type schemaObjectDefinition struct {
	objectType string
	name       string
	tableName  string
	sql        string
}

var productionSchemaContracts struct {
	sync.Once
	byVersion map[int][]schemaObjectDefinition
	err       error
}

// verifyProductionSchemaContract compares every required declared object with
// the endpoint produced by the immutable migrations. Test-only failure
// injection triggers may coexist with that endpoint; tables, indexes, views,
// and FTS objects must match. Building the reference in an in-memory database
// keeps the preflight independent of formatting while the database being
// opened remains read-only.
func verifyProductionSchemaContract(ctx context.Context, conn *sql.Conn, version int) error {
	productionSchemaContracts.Do(buildProductionSchemaContracts)
	if productionSchemaContracts.err != nil {
		return productionSchemaContracts.err
	}

	want, ok := productionSchemaContracts.byVersion[version]
	if !ok {
		return fmt.Errorf("no production schema contract for version %d", version)
	}
	got, err := readSchemaObjectDefinitions(ctx, conn)
	if err != nil {
		return fmt.Errorf("read schema objects: %w", err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("schema has %d declared objects, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].objectType != want[index].objectType ||
			got[index].name != want[index].name ||
			got[index].tableName != want[index].tableName {
			return fmt.Errorf(
				"schema object %d is %s %q on %q, want %s %q on %q",
				index,
				got[index].objectType,
				got[index].name,
				got[index].tableName,
				want[index].objectType,
				want[index].name,
				want[index].tableName,
			)
		}
		if normalizeSchemaSQL(got[index].sql) != normalizeSchemaSQL(want[index].sql) {
			return fmt.Errorf("schema definition for %s %q does not match version %d", want[index].objectType, want[index].name, version)
		}
	}
	return nil
}

func buildProductionSchemaContracts() {
	database, err := sql.Open(driverName, ":memory:")
	if err != nil {
		productionSchemaContracts.err = fmt.Errorf("open in-memory schema contract database: %w", err)
		return
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()

	conn, err := database.Conn(context.Background())
	if err != nil {
		productionSchemaContracts.err = fmt.Errorf("open in-memory schema contract connection: %w", err)
		return
	}
	defer conn.Close()

	endpoints := []struct {
		version int
		name    string
		sql     string
	}{
		{version: 1, name: "initial", sql: migration1SQL},
		{version: 2, name: "database-identity", sql: migration2SQL},
		{version: 3, name: "project-workspaces", sql: migration3SQL},
	}
	productionSchemaContracts.byVersion = make(map[int][]schemaObjectDefinition, len(endpoints))
	for _, endpoint := range endpoints {
		if _, err := conn.ExecContext(context.Background(), endpoint.sql); err != nil {
			productionSchemaContracts.err = fmt.Errorf("build schema contract for migration %d (%s): %w", endpoint.version, endpoint.name, err)
			return
		}
		definitions, err := readSchemaObjectDefinitions(context.Background(), conn)
		if err != nil {
			productionSchemaContracts.err = fmt.Errorf("read schema contract for migration %d (%s): %w", endpoint.version, endpoint.name, err)
			return
		}
		productionSchemaContracts.byVersion[endpoint.version] = definitions
	}
}

func readSchemaObjectDefinitions(ctx context.Context, conn *sql.Conn) ([]schemaObjectDefinition, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT type, name, tbl_name, sql
		FROM sqlite_schema
		WHERE name NOT GLOB 'sqlite_*'
		  AND sql IS NOT NULL
		  AND type <> 'trigger'
		ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var definitions []schemaObjectDefinition
	for rows.Next() {
		var definition schemaObjectDefinition
		if err := rows.Scan(&definition.objectType, &definition.name, &definition.tableName, &definition.sql); err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return definitions, nil
}

// normalizeSchemaSQL collapses insignificant whitespace outside quoted SQL
// values and identifiers. Whitespace within quotes remains part of the schema
// contract (notably for FTS tokenizer configuration).
func normalizeSchemaSQL(statement string) string {
	var normalized strings.Builder
	normalized.Grow(len(statement))
	pendingSpace := false
	var quote rune
	for _, current := range statement {
		if quote != 0 {
			normalized.WriteRune(current)
			if quote == '[' {
				if current == ']' {
					quote = 0
				}
			} else if current == quote {
				quote = 0
			}
			continue
		}
		if unicode.IsSpace(current) {
			pendingSpace = normalized.Len() > 0
			continue
		}
		if pendingSpace {
			normalized.WriteByte(' ')
			pendingSpace = false
		}
		switch current {
		case '\'', '"', '`', '[':
			quote = current
		}
		normalized.WriteRune(current)
	}
	return normalized.String()
}
