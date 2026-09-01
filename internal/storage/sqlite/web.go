package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// WebReader owns only read-only, query-only SQLite connections. It never runs
// the normal migration/runtime preparation path because that path contains
// persistent journal configuration.
type WebReader struct {
	db *sql.DB
}

func OpenWebReader(ctx context.Context, databasePath string) (*WebReader, error) {
	db, err := openReadOnly(ctx, databasePath, 4)
	if err != nil {
		return nil, err
	}
	return &WebReader{db: db}, nil
}

func (reader *WebReader) Close() error { return reader.db.Close() }

// WebWriter owns one write-capable connection for all UI mutations. The
// monitor and all HTTP reads use other query-only pools.
type WebWriter struct {
	db *sql.DB
}

func OpenWebWriter(ctx context.Context, databasePath string) (*WebWriter, error) {
	db, err := Open(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	return &WebWriter{db: db}, nil
}

func (writer *WebWriter) Close() error { return writer.db.Close() }

func (writer *WebWriter) pelletRepository() *PelletRepository {
	return &PelletRepository{db: writer.db}
}
func (writer *WebWriter) memoryRepository() *MemoryRepository {
	return &MemoryRepository{db: writer.db}
}

func resolvedForScalarWrite(project storage.Project) (storage.ResolvedProject, error) {
	if len(project.Workspaces) == 0 {
		return storage.ResolvedProject{}, domain.NewError(
			domain.Conflict, "project_has_no_workspace", "the project has no registered workspace", map[string]any{"project": project.Code},
		)
	}
	return storage.ResolvedProject{Project: project, Workspace: project.Workspaces[0]}, nil
}

func (writer *WebWriter) CreateWebPellet(ctx context.Context, project storage.Project, input storage.NewPellet) (storage.Pellet, error) {
	resolved, err := resolvedForScalarWrite(project)
	if err != nil {
		return storage.Pellet{}, err
	}
	return writer.pelletRepository().CreateWebPellet(ctx, resolved, input)
}

func (writer *WebWriter) UpdateWebPellet(ctx context.Context, project storage.Project, reference domain.PelletReference, expectedVersion string, changes storage.PelletChanges) (storage.Pellet, error) {
	resolved, err := resolvedForScalarWrite(project)
	if err != nil {
		return storage.Pellet{}, err
	}
	return writer.pelletRepository().UpdateWebPellet(ctx, resolved, reference, expectedVersion, changes)
}

func (writer *WebWriter) MoveWebPellet(ctx context.Context, project storage.Project, reference domain.PelletReference, expectedVersion string, placement storage.PelletPlacement) (storage.Pellet, error) {
	resolved, err := resolvedForScalarWrite(project)
	if err != nil {
		return storage.Pellet{}, err
	}
	return writer.pelletRepository().MoveWebPellet(ctx, resolved, reference, expectedVersion, placement)
}

func (writer *WebWriter) TransitionWebPellet(ctx context.Context, project storage.ResolvedProject, reference domain.PelletReference, expectedVersion string, request storage.PelletLifecycleRequest) (storage.PelletLifecycleResult, error) {
	return writer.pelletRepository().TransitionWebPellet(ctx, project, reference, expectedVersion, request)
}

func (writer *WebWriter) CreateWebMemory(ctx context.Context, project storage.Project, input storage.NewMemory) (storage.Memory, error) {
	return writer.memoryRepository().CreateWebMemory(ctx, project, input)
}

func (writer *WebWriter) UpdateWebMemory(ctx context.Context, project storage.Project, memoryID int64, expectedVersion, text string) (storage.Memory, error) {
	return writer.memoryRepository().UpdateWebMemory(ctx, project, memoryID, expectedVersion, text)
}

func (writer *WebWriter) ApproveWebMemory(ctx context.Context, project storage.Project, memoryID int64, expectedVersion string) (storage.Memory, error) {
	return writer.memoryRepository().ApproveWebMemory(ctx, project, memoryID, expectedVersion)
}

func (reader *WebReader) ListWebProjects(ctx context.Context) ([]storage.WebProjectSummary, error) {
	rows, err := reader.db.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY code")
	if err != nil {
		return nil, projectStorageError("list web projects", err)
	}
	projects := make([]storage.Project, 0)
	for rows.Next() {
		project, scanErr := scanProject(rows)
		if scanErr != nil {
			rows.Close()
			return nil, projectStorageError("read web project", scanErr)
		}
		projects = append(projects, project)
	}
	if err := rows.Close(); err != nil {
		return nil, projectStorageError("close web project rows", err)
	}
	if err := rows.Err(); err != nil {
		return nil, projectStorageError("iterate web projects", err)
	}

	summaries := make([]storage.WebProjectSummary, 0, len(projects))
	for _, project := range projects {
		project.Workspaces, err = loadWorkspaces(ctx, reader.db, project.ID)
		if err != nil {
			return nil, projectStorageError("read web project workspaces", err)
		}
		project.Redirects, err = loadProjectCodeRedirects(ctx, reader.db, project.ID)
		if err != nil {
			return nil, projectStorageError("read web project code redirects", err)
		}
		summary := storage.WebProjectSummary{Project: project}
		if err := reader.db.QueryRowContext(ctx, `
			SELECT
				coalesce(sum(status = 'open'), 0),
				coalesce(sum(status = 'in_progress'), 0),
				coalesce(sum(status = 'closed'), 0),
				coalesce(sum(status = 'maybe_later'), 0)
			FROM pellets WHERE project_id = ?`, project.ID).Scan(
			&summary.Open, &summary.InProgress, &summary.Closed, &summary.MaybeLater,
		); err != nil {
			return nil, pelletStorageError("count web project pellets", err)
		}
		if err := reader.db.QueryRowContext(ctx, `
			SELECT count(*), coalesce(sum(approved_at IS NOT NULL), 0)
			FROM memories WHERE project_id = ?`, project.ID).Scan(
			&summary.MemoryCount, &summary.ApprovedMemory,
		); err != nil {
			return nil, memoryStorageError("count web project memories", err)
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (reader *WebReader) ListWebPellets(ctx context.Context, project storage.Project, filters storage.WebPelletFilters) ([]storage.Pellet, error) {
	if err := validatePelletProject(project); err != nil {
		return nil, err
	}
	if err := ensureStoredProject(ctx, reader.db, project); err != nil {
		return nil, err
	}
	if filters.Status != nil {
		if err := domain.ValidatePelletStatus(*filters.Status); err != nil {
			return nil, err
		}
	}
	if filters.ExternalID != nil && *filters.ExternalID == "" {
		return nil, invalidPelletField("external_id", "optional pellet fields must be non-empty")
	}
	if filters.Group.Set && filters.Group.Value != nil && *filters.Group.Value == "" {
		return nil, invalidPelletField("group", "optional pellet fields must be non-empty")
	}

	query := pelletSelect + "\n\tWHERE p.project_id = ?"
	arguments := []any{project.ID}
	if filters.Status != nil {
		query += " AND p.status = ?"
		arguments = append(arguments, *filters.Status)
	}
	if filters.ExternalID != nil {
		query += " AND p.external_id = ?"
		arguments = append(arguments, *filters.ExternalID)
	}
	if filters.Group.Set {
		if filters.Group.Value == nil {
			query += " AND p.group_id IS NULL"
		} else {
			query += " AND p.group_id = ?"
			arguments = append(arguments, *filters.Group.Value)
		}
	}
	if strings.TrimSpace(filters.Query) != "" {
		query += " AND p.rowid IN (SELECT rowid FROM pellets_fts WHERE pellets_fts MATCH ?)"
		arguments = append(arguments, escapeFTS5Query(filters.Query))
	}
	query += pelletListOrder(storage.PelletListOptions{All: true})

	rows, err := reader.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, pelletStorageError("list web pellets", err)
	}
	pellets := make([]storage.Pellet, 0)
	for rows.Next() {
		pellet, scanErr := scanPellet(rows)
		if scanErr != nil {
			rows.Close()
			return nil, pelletStorageError("read web pellet", scanErr)
		}
		pellets = append(pellets, pellet)
	}
	if err := rows.Close(); err != nil {
		return nil, pelletStorageError("close web pellet rows", err)
	}
	if err := rows.Err(); err != nil {
		return nil, pelletStorageError("iterate web pellets", err)
	}
	return pellets, nil
}

func (reader *WebReader) ReadWebPellet(ctx context.Context, project storage.Project, reference domain.PelletReference) (storage.Pellet, error) {
	if err := validatePelletProject(project); err != nil {
		return storage.Pellet{}, err
	}
	if err := ensureStoredProject(ctx, reader.db, project); err != nil {
		return storage.Pellet{}, err
	}
	if err := ensureReferenceProject(ctx, reader.db, project, reference); err != nil {
		return storage.Pellet{}, err
	}
	pellet, err := loadPellet(ctx, reader.db, project.ID, reference.Number)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Pellet{}, pelletNotFound(reference)
	}
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read web pellet", err)
	}
	return pellet, nil
}

func (reader *WebReader) ListWebGroups(ctx context.Context, project storage.Project) ([]*string, error) {
	if err := validatePelletProject(project); err != nil {
		return nil, err
	}
	rows, err := reader.db.QueryContext(ctx, `
		SELECT DISTINCT group_id
		FROM pellets
		WHERE project_id = ?
		ORDER BY group_id IS NOT NULL, group_id`, project.ID)
	if err != nil {
		return nil, pelletStorageError("list web pellet groups", err)
	}
	groups := make([]*string, 0)
	for rows.Next() {
		var group sql.NullString
		if err := rows.Scan(&group); err != nil {
			rows.Close()
			return nil, pelletStorageError("read web pellet group", err)
		}
		if !group.Valid {
			groups = append(groups, nil)
		} else {
			value := group.String
			groups = append(groups, &value)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, pelletStorageError("close web pellet groups", err)
	}
	if err := rows.Err(); err != nil {
		return nil, pelletStorageError("iterate web pellet groups", err)
	}
	return groups, nil
}

func (reader *WebReader) ListWebMemories(ctx context.Context, project storage.Project) ([]storage.Memory, error) {
	if err := validateMemoryProject(project); err != nil {
		return nil, err
	}
	if err := ensureStoredMemoryProject(ctx, reader.db, project); err != nil {
		return nil, err
	}
	rows, err := reader.db.QueryContext(ctx, memorySelect+`
		WHERE memory.project_id = ?
		ORDER BY memory.memory_id DESC`, project.ID)
	if err != nil {
		return nil, memoryStorageError("list web memories", err)
	}
	memories := make([]storage.Memory, 0)
	for rows.Next() {
		memory, scanErr := scanMemory(rows)
		if scanErr != nil {
			rows.Close()
			return nil, memoryStorageError("read web memory", scanErr)
		}
		memories = append(memories, memory)
	}
	if err := rows.Close(); err != nil {
		return nil, memoryStorageError("close web memory rows", err)
	}
	if err := rows.Err(); err != nil {
		return nil, memoryStorageError("iterate web memories", err)
	}
	return memories, nil
}

func (reader *WebReader) ReadWebMemory(ctx context.Context, project storage.Project, memoryID int64) (storage.Memory, error) {
	if err := validateMemoryProject(project); err != nil {
		return storage.Memory{}, err
	}
	if err := validateMemoryID(memoryID); err != nil {
		return storage.Memory{}, err
	}
	if err := ensureStoredMemoryProject(ctx, reader.db, project); err != nil {
		return storage.Memory{}, err
	}
	memory, err := loadMemory(ctx, reader.db, project.ID, memoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Memory{}, memoryNotFound(memoryID)
	}
	if err != nil {
		return storage.Memory{}, memoryStorageError("read web memory", err)
	}
	return memory, nil
}

// DataVersionMonitor pins exactly one query-only connection for its lifetime.
// Values are comparable only because every call uses this same *sql.Conn.
type DataVersionMonitor struct {
	db   *sql.DB
	conn *sql.Conn
}

func OpenDataVersionMonitor(ctx context.Context, databasePath string) (*DataVersionMonitor, error) {
	db, err := openReadOnly(ctx, databasePath, 1)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, domain.WrapError(domain.Storage, "database_open_failed", "could not open data-version monitor", nil, err)
	}
	return &DataVersionMonitor{db: db, conn: conn}, nil
}

func (monitor *DataVersionMonitor) DataVersion(ctx context.Context) (int64, error) {
	var version int64
	if err := monitor.conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		return 0, domain.WrapError(domain.Storage, "database_monitor_failed", "could not check for database changes", nil, err)
	}
	return version, nil
}

func (monitor *DataVersionMonitor) Close() error {
	return errors.Join(monitor.conn.Close(), monitor.db.Close())
}

func openReadOnly(ctx context.Context, databasePath string, connections int) (*sql.DB, error) {
	dsn, err := readOnlyDataSourceName(databasePath)
	if err != nil {
		return nil, domain.WrapError(domain.Storage, "database_open_failed", "could not open database read-only", nil, err)
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, domain.WrapError(domain.Storage, "database_open_failed", "could not open database read-only", nil, err)
	}
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, domain.WrapError(domain.Storage, "database_open_failed", "could not open database read-only", nil, err)
	}
	var schemaVersion, queryOnly int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schemaVersion); err != nil {
		db.Close()
		return nil, domain.WrapError(domain.Storage, "database_open_failed", "could not verify database version", nil, err)
	}
	if schemaVersion != LatestSchemaVersion {
		db.Close()
		return nil, domain.NewError(domain.Storage, "schema_version_unsupported", "the web interface requires the current database schema", map[string]any{"database_version": schemaVersion, "supported_version": LatestSchemaVersion})
	}
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		db.Close()
		if err == nil {
			err = fmt.Errorf("PRAGMA query_only returned %d", queryOnly)
		}
		return nil, domain.WrapError(domain.Storage, "database_open_failed", "could not enforce query-only database access", nil, err)
	}
	return db, nil
}

func readOnlyDataSourceName(databasePath string) (string, error) {
	u, err := absoluteFileURL(databasePath)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMilliseconds))
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "query_only(ON)")
	query.Add("_pragma", "trusted_schema(OFF)")
	u.RawQuery = query.Encode()
	return u.String(), nil
}
