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

const memorySelect = `
	SELECT memory.memory_id, memory.project_id, project.code, memory.text, memory.created_by,
	       strftime('%Y-%m-%dT%H:%M:%fZ', memory.created_at),
	       strftime('%Y-%m-%dT%H:%M:%fZ', memory.updated_at),
	       strftime('%Y-%m-%dT%H:%M:%fZ', memory.approved_at)
	FROM memories AS memory
	JOIN projects AS project ON project.project_id = memory.project_id`

const memorySnippetMaxRunes = 240

// MemoryRepository owns a configured SQLite database used for project memory.
type MemoryRepository struct {
	db *sql.DB
}

func OpenMemoryRepository(ctx context.Context, databasePath string) (*MemoryRepository, error) {
	db, err := Open(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	return &MemoryRepository{db: db}, nil
}

func (repository *MemoryRepository) Close() error { return repository.db.Close() }

// CreateMemory writes the authoritative row and its derived FTS row in one
// transaction. Human provenance is approved at that same captured instant.
func (repository *MemoryRepository) CreateMemory(ctx context.Context, project storage.Project, input storage.NewMemory) (storage.Memory, error) {
	if err := validateMemoryProject(project); err != nil {
		return storage.Memory{}, err
	}
	normalized, err := validateNewMemory(input)
	if err != nil {
		return storage.Memory{}, err
	}

	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.Memory{}, memoryStorageError("open memory creation connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Memory{}, memoryStorageError("begin memory creation", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if err := ensureStoredMemoryProject(ctx, connection, project); err != nil {
		return storage.Memory{}, err
	}
	timestamp, err := captureJulianTimestamp(ctx, connection)
	if err != nil {
		return storage.Memory{}, memoryStorageError("capture memory creation timestamp", err)
	}
	var approvedAt any
	if normalized.CreatedBy == domain.MemoryCreatedByHuman {
		approvedAt = timestamp
	}
	result, err := connection.ExecContext(ctx, `
		INSERT INTO memories(project_id, text, created_by, approved_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		project.ID, normalized.Text, normalized.CreatedBy, approvedAt, timestamp, timestamp)
	if err != nil {
		return storage.Memory{}, memoryStorageError("insert memory", err)
	}
	memoryID, err := result.LastInsertId()
	if err != nil {
		return storage.Memory{}, memoryStorageError("read inserted memory identity", err)
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO memories_fts(rowid, text)
		VALUES (?, ?)`, memoryID, normalized.Text); err != nil {
		return storage.Memory{}, memoryFTSError("index inserted memory", err)
	}

	memory, err := loadMemory(ctx, connection, project.ID, memoryID)
	if err != nil {
		return storage.Memory{}, memoryStorageError("read inserted memory", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Memory{}, memoryStorageError("commit memory creation", err)
	}
	committed = true
	return memory, nil
}

func (repository *MemoryRepository) CreateWebMemory(ctx context.Context, project storage.Project, input storage.NewMemory) (storage.Memory, error) {
	return repository.CreateMemory(ctx, project, input)
}

// ListMemories returns newest records first with memory ID as the deterministic
// ordering key. IDs are monotonically allocated for committed rows.
func (repository *MemoryRepository) ListMemories(ctx context.Context, project storage.Project, options storage.MemoryListOptions) ([]storage.Memory, error) {
	if err := validateMemoryProject(project); err != nil {
		return nil, err
	}
	if err := validateMemoryListOptions(options); err != nil {
		return nil, err
	}
	if err := ensureStoredMemoryProject(ctx, repository.db, project); err != nil {
		return nil, err
	}
	query := memorySelect + "\n\tWHERE memory.project_id = ?"
	arguments := []any{project.ID}
	if options.ApprovedOnly {
		query += " AND memory.approved_at IS NOT NULL"
	}
	query += " ORDER BY memory.memory_id DESC"
	if options.Limit != nil {
		query += " LIMIT ?"
		arguments = append(arguments, *options.Limit)
	}
	rows, err := repository.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, memoryStorageError("list memories", err)
	}
	defer rows.Close()
	memories := make([]storage.Memory, 0)
	for rows.Next() {
		memory, scanErr := scanMemory(rows)
		if scanErr != nil {
			return nil, memoryStorageError("read listed memory", scanErr)
		}
		memories = append(memories, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, memoryStorageError("iterate listed memories", err)
	}
	return memories, nil
}

// SearchMemories uses the disposable FTS table only for candidate retrieval,
// ranking, and snippets. Project and approval constraints remain relational
// predicates over authoritative memory rows.
func (repository *MemoryRepository) SearchMemories(ctx context.Context, project storage.Project, options storage.MemorySearchOptions) ([]storage.MemorySearchResult, error) {
	if err := validateMemoryProject(project); err != nil {
		return nil, err
	}
	if err := validateMemorySearchOptions(options); err != nil {
		return nil, err
	}
	if err := ensureStoredMemoryProject(ctx, repository.db, project); err != nil {
		return nil, err
	}

	query := `
		SELECT memory.memory_id, memory.project_id, project.code, memory.text, memory.created_by,
		       strftime('%Y-%m-%dT%H:%M:%fZ', memory.created_at),
		       strftime('%Y-%m-%dT%H:%M:%fZ', memory.updated_at),
		       strftime('%Y-%m-%dT%H:%M:%fZ', memory.approved_at),
		       bm25(memories_fts),
		       snippet(memories_fts, 0, char(1), char(2), '…', 24)
		FROM memories_fts
		JOIN memories AS memory ON memory.memory_id = memories_fts.rowid
		JOIN projects AS project ON project.project_id = memory.project_id
		WHERE memories_fts MATCH ? AND memory.project_id = ?`
	arguments := []any{escapeFTS5Query(options.Query), project.ID}
	if options.ApprovedOnly {
		query += " AND memory.approved_at IS NOT NULL"
	}
	query += `
		ORDER BY bm25(memories_fts),
		         memory.created_at DESC,
		         memory.memory_id DESC`
	if options.Limit != nil {
		query += " LIMIT ?"
		arguments = append(arguments, *options.Limit)
	}

	rows, err := repository.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, memoryFTSError("search memories", err)
	}
	defer rows.Close()
	results := make([]storage.MemorySearchResult, 0)
	for rows.Next() {
		var rank float64
		var snippet string
		memory, scanErr := scanMemoryColumns(rows, &rank, &snippet)
		if scanErr != nil {
			return nil, memoryStorageError("read memory search result", scanErr)
		}
		results = append(results, storage.MemorySearchResult{
			Memory: memory, Rank: rank, Snippet: normalizeMemorySnippet(snippet),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, memoryFTSError("iterate memory search results", err)
	}
	return results, nil
}

// RebuildMemorySearchIndex regenerates the disposable external-content index
// from every authoritative memory under one writer transaction.
func (repository *MemoryRepository) RebuildMemorySearchIndex(ctx context.Context) error {
	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return memoryStorageError("open memory search rebuild connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return memoryStorageError("begin memory search rebuild", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := connection.ExecContext(ctx, "INSERT INTO memories_fts(memories_fts) VALUES ('rebuild')"); err != nil {
		return memoryFTSError("rebuild memory search index", err)
	}
	if err := verifyExternalContentFTSIndex(ctx, connection, "memories_fts"); err != nil {
		return memoryFTSError("verify rebuilt memory search index", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return memoryStorageError("commit memory search rebuild", err)
	}
	committed = true
	return nil
}

func (repository *MemoryRepository) ReadMemory(ctx context.Context, project storage.Project, memoryID int64) (storage.Memory, error) {
	if err := validateMemoryProject(project); err != nil {
		return storage.Memory{}, err
	}
	if err := validateMemoryID(memoryID); err != nil {
		return storage.Memory{}, err
	}
	if err := ensureStoredMemoryProject(ctx, repository.db, project); err != nil {
		return storage.Memory{}, err
	}
	memory, err := loadMemory(ctx, repository.db, project.ID, memoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Memory{}, memoryNotFound(memoryID)
	}
	if err != nil {
		return storage.Memory{}, memoryStorageError("read memory", err)
	}
	return memory, nil
}

// ApproveMemory changes only a previously unapproved row. Repeated approval
// commits no update and therefore preserves the first approval/update instant.
func (repository *MemoryRepository) ApproveMemory(ctx context.Context, project storage.Project, memoryID int64) (storage.Memory, error) {
	return repository.approveMemory(ctx, project, memoryID, "")
}

func (repository *MemoryRepository) ApproveWebMemory(ctx context.Context, project storage.Project, memoryID int64, expectedVersion string) (storage.Memory, error) {
	if err := validateWebVersion(expectedVersion); err != nil {
		return storage.Memory{}, err
	}
	return repository.approveMemory(ctx, project, memoryID, expectedVersion)
}

func (repository *MemoryRepository) approveMemory(ctx context.Context, project storage.Project, memoryID int64, expectedVersion string) (storage.Memory, error) {
	if err := validateMemoryProject(project); err != nil {
		return storage.Memory{}, err
	}
	if err := validateMemoryID(memoryID); err != nil {
		return storage.Memory{}, err
	}
	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.Memory{}, memoryStorageError("open memory approval connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Memory{}, memoryStorageError("begin memory approval", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := ensureStoredMemoryProject(ctx, connection, project); err != nil {
		return storage.Memory{}, err
	}
	memory, err := loadMemory(ctx, connection, project.ID, memoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Memory{}, memoryNotFound(memoryID)
	}
	if err != nil {
		return storage.Memory{}, memoryStorageError("read memory before approval", err)
	}
	if expectedVersion != "" && expectedVersion != storage.MemoryVersion(memory) {
		return storage.Memory{}, &storage.OptimisticConflict{Memory: &memory}
	}
	if memory.ApprovedAt == nil {
		timestamp, captureErr := captureJulianTimestamp(ctx, connection)
		if captureErr != nil {
			return storage.Memory{}, memoryStorageError("capture memory approval timestamp", captureErr)
		}
		result, updateErr := connection.ExecContext(ctx, `
			UPDATE memories
			SET approved_at = ?, updated_at = ?
			WHERE project_id = ? AND memory_id = ? AND approved_at IS NULL`,
			timestamp, timestamp, project.ID, memoryID)
		if updateErr != nil {
			return storage.Memory{}, memoryStorageError("approve memory", updateErr)
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil || changed != 1 {
			if rowsErr == nil {
				rowsErr = fmt.Errorf("updated %d rows, want 1", changed)
			}
			return storage.Memory{}, memoryStorageError("verify memory approval", rowsErr)
		}
		memory, err = loadMemory(ctx, connection, project.ID, memoryID)
		if err != nil {
			return storage.Memory{}, memoryStorageError("read approved memory", err)
		}
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Memory{}, memoryStorageError("commit memory approval", err)
	}
	committed = true
	return memory, nil
}

// UpdateMemory replaces authoritative text and the external-content FTS row
// in one immediate transaction.
func (repository *MemoryRepository) UpdateMemory(ctx context.Context, project storage.Project, memoryID int64, text string) (storage.Memory, error) {
	return repository.updateMemory(ctx, project, memoryID, "", text)
}

func (repository *MemoryRepository) UpdateWebMemory(ctx context.Context, project storage.Project, memoryID int64, expectedVersion, text string) (storage.Memory, error) {
	if err := validateWebVersion(expectedVersion); err != nil {
		return storage.Memory{}, err
	}
	return repository.updateMemory(ctx, project, memoryID, expectedVersion, text)
}

func (repository *MemoryRepository) updateMemory(ctx context.Context, project storage.Project, memoryID int64, expectedVersion, text string) (storage.Memory, error) {
	if err := validateMemoryProject(project); err != nil {
		return storage.Memory{}, err
	}
	if err := validateMemoryID(memoryID); err != nil {
		return storage.Memory{}, err
	}
	if err := domain.ValidateMemoryText(text); err != nil {
		return storage.Memory{}, err
	}
	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.Memory{}, memoryStorageError("open memory update connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Memory{}, memoryStorageError("begin memory update", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if err := ensureStoredMemoryProject(ctx, connection, project); err != nil {
		return storage.Memory{}, err
	}
	before, err := loadMemory(ctx, connection, project.ID, memoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Memory{}, memoryNotFound(memoryID)
	}
	if err != nil {
		return storage.Memory{}, memoryStorageError("read memory before update", err)
	}
	if expectedVersion != "" && expectedVersion != storage.MemoryVersion(before) {
		return storage.Memory{}, &storage.OptimisticConflict{Memory: &before}
	}
	if before.Text == text {
		if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
			return storage.Memory{}, memoryStorageError("commit unchanged memory update", err)
		}
		committed = true
		return before, nil
	}
	timestamp, err := captureJulianTimestamp(ctx, connection)
	if err != nil {
		return storage.Memory{}, memoryStorageError("capture memory update timestamp", err)
	}
	var approvedAt any
	if before.CreatedBy == domain.MemoryCreatedByHuman {
		// Browser/CLI text-edit entry points are explicit human actions. The
		// schema requires human-authored memory to remain approved.
		approvedAt = timestamp
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO memories_fts(memories_fts, rowid, text)
		VALUES ('delete', ?, ?)`, before.ID, before.Text); err != nil {
		return storage.Memory{}, memoryFTSError("remove old memory search text", err)
	}
	if _, err := connection.ExecContext(ctx, `
		UPDATE memories
		SET text = ?, approved_at = ?, updated_at = ?
		WHERE project_id = ? AND memory_id = ?`, text, approvedAt, timestamp, project.ID, memoryID); err != nil {
		return storage.Memory{}, memoryStorageError("update memory", err)
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO memories_fts(rowid, text)
		VALUES (?, ?)`, before.ID, text); err != nil {
		return storage.Memory{}, memoryFTSError("index updated memory", err)
	}
	updated, err := loadMemory(ctx, connection, project.ID, memoryID)
	if err != nil {
		return storage.Memory{}, memoryStorageError("read updated memory", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Memory{}, memoryStorageError("commit memory update", err)
	}
	committed = true
	return updated, nil
}

// RemoveMemory deletes one project-scoped authoritative row and its derived
// external-content FTS entry under the same immediate transaction.
func (repository *MemoryRepository) RemoveMemory(ctx context.Context, project storage.Project, memoryID int64) (storage.Memory, error) {
	if err := validateMemoryProject(project); err != nil {
		return storage.Memory{}, err
	}
	if err := validateMemoryID(memoryID); err != nil {
		return storage.Memory{}, err
	}
	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.Memory{}, memoryStorageError("open memory removal connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Memory{}, memoryStorageError("begin memory removal", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if err := ensureStoredMemoryProject(ctx, connection, project); err != nil {
		return storage.Memory{}, err
	}
	memory, err := loadMemory(ctx, connection, project.ID, memoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Memory{}, memoryNotFound(memoryID)
	}
	if err != nil {
		return storage.Memory{}, memoryStorageError("read memory before removal", err)
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO memories_fts(memories_fts, rowid, text)
		VALUES ('delete', ?, ?)`, memory.ID, memory.Text); err != nil {
		return storage.Memory{}, memoryFTSError("remove memory from search index", err)
	}
	result, err := connection.ExecContext(ctx, `
		DELETE FROM memories
		WHERE project_id = ? AND memory_id = ?`, project.ID, memoryID)
	if err != nil {
		return storage.Memory{}, memoryStorageError("delete memory", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return storage.Memory{}, memoryStorageError("verify memory removal", err)
	}
	if deleted != 1 {
		return storage.Memory{}, memoryStorageError("verify memory removal", fmt.Errorf("deleted %d rows, want 1", deleted))
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Memory{}, memoryStorageError("commit memory removal", err)
	}
	committed = true
	return memory, nil
}

func loadMemory(ctx context.Context, query projectQuery, projectID, memoryID int64) (storage.Memory, error) {
	return scanMemory(query.QueryRowContext(ctx, memorySelect+`
		WHERE memory.project_id = ? AND memory.memory_id = ?`,
		projectID, memoryID))
}

type memoryScanner interface{ Scan(...any) error }

func scanMemory(scanner memoryScanner) (storage.Memory, error) {
	return scanMemoryColumns(scanner)
}

func scanMemoryColumns(scanner memoryScanner, additionalDestinations ...any) (storage.Memory, error) {
	var memory storage.Memory
	var createdBy, createdAt, updatedAt string
	var approvedAt sql.NullString
	destinations := []any{
		&memory.ID, &memory.ProjectID, &memory.ProjectCode, &memory.Text, &createdBy,
		&createdAt, &updatedAt, &approvedAt,
	}
	destinations = append(destinations, additionalDestinations...)
	if err := scanner.Scan(destinations...); err != nil {
		return storage.Memory{}, err
	}
	memory.CreatedBy = domain.MemoryCreator(createdBy)
	var err error
	memory.CreatedAt, err = parseProjectTimestamp("memory created_at", createdAt)
	if err != nil {
		return storage.Memory{}, err
	}
	memory.UpdatedAt, err = parseProjectTimestamp("memory updated_at", updatedAt)
	if err != nil {
		return storage.Memory{}, err
	}
	if approvedAt.Valid {
		parsed, parseErr := parseProjectTimestamp("memory approved_at", approvedAt.String)
		if parseErr != nil {
			return storage.Memory{}, parseErr
		}
		memory.ApprovedAt = &parsed
	}
	return memory, nil
}

func validateMemoryProject(project storage.Project) error {
	if project.ID <= 0 {
		return domain.NewError(domain.Unexpected, "internal_error", "resolved memory project context is inconsistent", nil)
	}
	if err := domain.ValidateProjectCode(project.Code); err != nil {
		return domain.NewError(domain.Unexpected, "internal_error", "resolved memory project code is invalid", nil)
	}
	return nil
}

func validateNewMemory(input storage.NewMemory) (storage.NewMemory, error) {
	if input.CreatedBy == "" {
		input.CreatedBy = domain.MemoryCreatedByAgent
	}
	if err := domain.ValidateMemoryCreator(input.CreatedBy); err != nil {
		return storage.NewMemory{}, err
	}
	if err := domain.ValidateMemoryText(input.Text); err != nil {
		return storage.NewMemory{}, err
	}
	return input, nil
}

func validateMemoryListOptions(options storage.MemoryListOptions) error {
	if options.Limit != nil && *options.Limit <= 0 {
		return domain.NewError(domain.Usage, "invalid_limit", "limit must be a positive integer", map[string]any{"limit": *options.Limit})
	}
	return nil
}

func validateMemorySearchOptions(options storage.MemorySearchOptions) error {
	if strings.TrimSpace(options.Query) == "" {
		return domain.NewError(domain.Usage, "missing_query", "memory search requires a non-empty QUERY", nil)
	}
	if options.Limit != nil && *options.Limit <= 0 {
		return domain.NewError(domain.Usage, "invalid_limit", "limit must be a positive integer", map[string]any{"limit": *options.Limit})
	}
	return nil
}

func normalizeMemorySnippet(snippet string) string {
	marked := []rune(snippet)
	clean := make([]rune, 0, len(marked))
	matchStart, matchEnd := -1, -1
	for _, value := range marked {
		switch value {
		case 1:
			if matchStart < 0 {
				matchStart = len(clean)
			}
		case 2:
			if matchStart >= 0 && matchEnd < 0 {
				matchEnd = len(clean)
			}
		default:
			clean = append(clean, value)
		}
	}
	if len(clean) <= memorySnippetMaxRunes {
		return string(clean)
	}

	// An individual token may approach the 1 MiB memory limit, so FTS5's
	// token-count bound alone is insufficient. Center the hard rune bound on
	// the first marked match and retain explicit truncation at both edges.
	windowLength := memorySnippetMaxRunes - 2
	center := windowLength / 2
	if matchStart >= 0 {
		if matchEnd < matchStart {
			matchEnd = matchStart
		}
		center = matchStart + (matchEnd-matchStart)/2
	}
	windowStart := center - windowLength/2
	if windowStart < 0 {
		windowStart = 0
	}
	if maximum := len(clean) - windowLength; windowStart > maximum {
		windowStart = maximum
	}
	return "…" + string(clean[windowStart:windowStart+windowLength]) + "…"
}

func validateMemoryID(memoryID int64) error {
	if memoryID > 0 {
		return nil
	}
	return domain.NewError(
		domain.Usage, "invalid_memory_id", "memory ID must be a positive canonical decimal integer",
		map[string]any{"memory_id": memoryID},
	)
}

func ensureStoredMemoryProject(ctx context.Context, query projectQuery, project storage.Project) error {
	var storedCode string
	err := query.QueryRowContext(ctx, "SELECT code FROM projects WHERE project_id = ?", project.ID).Scan(&storedCode)
	if errors.Is(err, sql.ErrNoRows) {
		return projectNotFound(map[string]any{"code": project.Code})
	}
	if err != nil {
		return memoryStorageError("verify memory project", err)
	}
	if storedCode != project.Code {
		resolvedProjectID, resolveErr := resolveProjectCodeID(ctx, query, project.Code)
		if resolveErr == nil && resolvedProjectID == project.ID {
			return nil
		}
		return domain.NewError(
			domain.Conflict,
			"project_identity_mismatch",
			"the resolved logical project identity does not match the selected database",
			map[string]any{"project_id": project.ID, "resolved_code": project.Code, "stored_code": storedCode},
		)
	}
	return nil
}

func memoryNotFound(memoryID int64) error {
	return domain.NewError(
		domain.NotFound,
		"memory_not_found",
		"the memory does not exist in the selected logical project",
		map[string]any{"memory_id": memoryID},
	)
}

func memoryStorageError(operation string, err error) error {
	if stable := stableDatabaseError(operation, err); stable != nil {
		return stable
	}
	return domain.WrapError(
		domain.Storage,
		"memory_storage_failed",
		"could not access memory records",
		map[string]any{"operation": operation},
		fmt.Errorf("%s: %w", operation, err),
	)
}

func memoryFTSError(operation string, err error) error {
	if stable := stableDatabaseError(operation, err); stable != nil {
		return stable
	}
	return domain.WrapError(
		domain.Storage,
		"fts_unavailable",
		"memory full-text search is unavailable",
		map[string]any{"operation": operation},
		err,
	)
}
