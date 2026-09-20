package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// GroupRepository shares the group operations used by command and web services.
type GroupRepository struct{ db *sql.DB }

func OpenGroupRepository(ctx context.Context, path string) (*GroupRepository, error) {
	db, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return &GroupRepository{db: db}, nil
}
func (r *GroupRepository) Close() error { return r.db.Close() }

const groupSelect = `SELECT group_id,project_id,name,context,revision,
 strftime('%Y-%m-%dT%H:%M:%fZ',created_at),strftime('%Y-%m-%dT%H:%M:%fZ',updated_at) FROM groups`

func scanGroup(row pelletScanner) (storage.Group, error) {
	var g storage.Group
	var created, updated string
	if err := row.Scan(&g.ID, &g.ProjectID, &g.Name, &g.Context, &g.Revision, &created, &updated); err != nil {
		return g, err
	}
	var err error
	if g.CreatedAt, err = parseProjectTimestamp("group created_at", created); err != nil {
		return g, err
	}
	g.UpdatedAt, err = parseProjectTimestamp("group updated_at", updated)
	return g, err
}
func readGroup(ctx context.Context, q projectQuery, project storage.Project, id int64) (storage.Group, error) {
	if err := ensureStoredProject(ctx, q, project); err != nil {
		return storage.Group{}, err
	}
	g, err := scanGroup(q.QueryRowContext(ctx, groupSelect+" WHERE project_id=? AND group_id=?", project.ID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return g, domain.NewError(domain.NotFound, "group_not_found", "group does not exist in this project", map[string]any{"group_id": id})
	}
	return g, groupStorageError(err)
}
func (r *GroupRepository) ReadGroup(ctx context.Context, project storage.Project, id int64) (storage.Group, error) {
	return readGroup(ctx, r.db, project, id)
}
func (r *GroupRepository) ListGroups(ctx context.Context, project storage.Project) ([]storage.Group, error) {
	if err := ensureStoredProject(ctx, r.db, project); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, groupSelect+" WHERE project_id=? ORDER BY name COLLATE BINARY,group_id", project.ID)
	if err != nil {
		return nil, groupStorageError(err)
	}
	defer rows.Close()
	result := []storage.Group{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, groupStorageError(err)
		}
		result = append(result, g)
	}
	return result, groupStorageError(rows.Err())
}
func (r *GroupRepository) write(ctx context.Context, f func(*sql.Conn) (storage.Group, error)) (storage.Group, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return storage.Group{}, groupStorageError(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Group{}, groupStorageError(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	result, err := f(conn)
	if err != nil {
		return storage.Group{}, groupStorageError(err)
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return result, groupStorageError(err)
}
func (r *GroupRepository) CreateGroup(ctx context.Context, project storage.Project, name string) (storage.Group, error) {
	if err := storage.ValidateGroupName(name); err != nil {
		return storage.Group{}, err
	}
	return r.write(ctx, func(conn *sql.Conn) (storage.Group, error) {
		if err := ensureStoredProject(ctx, conn, project); err != nil {
			return storage.Group{}, err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO groups(project_id,name,created_at,updated_at) VALUES(?,?,julianday('now'),julianday('now')) ON CONFLICT(project_id,name) DO NOTHING`, project.ID, name)
		if err != nil {
			return storage.Group{}, err
		}
		return scanGroup(conn.QueryRowContext(ctx, groupSelect+" WHERE project_id=? AND name=?", project.ID, name))
	})
}
func (r *GroupRepository) EditGroupContext(ctx context.Context, project storage.Project, id, revision int64, markdown string) (storage.Group, error) {
	if err := storage.ValidateGroupContext(markdown); err != nil {
		return storage.Group{}, err
	}
	return r.update(ctx, project, id, revision, func(conn *sql.Conn, g storage.Group) error {
		_, err := conn.ExecContext(ctx, `UPDATE groups SET context=?,revision=revision+1,updated_at=MAX(updated_at,julianday('now')) WHERE group_id=?`, markdown, g.ID)
		return err
	})
}

func (r *GroupRepository) CreateGroupWithContext(ctx context.Context, project storage.Project, name, markdown string) (storage.Group, error) {
	if err := storage.ValidateGroupName(name); err != nil {
		return storage.Group{}, err
	}
	if err := storage.ValidateGroupContext(markdown); err != nil {
		return storage.Group{}, err
	}
	return r.write(ctx, func(conn *sql.Conn) (storage.Group, error) {
		if err := ensureStoredProject(ctx, conn, project); err != nil {
			return storage.Group{}, err
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO groups(project_id,name,context,created_at,updated_at) VALUES(?,?,?,julianday('now'),julianday('now')) ON CONFLICT(project_id,name) DO NOTHING`, project.ID, name, markdown)
		if err != nil {
			return storage.Group{}, err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return storage.Group{}, err
		}
		if inserted == 0 {
			return storage.Group{}, domain.NewError(domain.Conflict, "group_name_conflict", "a group with that exact name already exists in this project", map[string]any{"name": name})
		}
		return scanGroup(conn.QueryRowContext(ctx, groupSelect+" WHERE project_id=? AND name=?", project.ID, name))
	})
}
func (r *GroupRepository) RenameGroup(ctx context.Context, project storage.Project, id, revision int64, name string) (storage.Group, error) {
	if err := storage.ValidateGroupName(name); err != nil {
		return storage.Group{}, err
	}
	return r.update(ctx, project, id, revision, func(conn *sql.Conn, g storage.Group) error {
		if g.Name == name {
			return nil
		}
		var collision bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM groups WHERE project_id=? AND name=?)", project.ID, name).Scan(&collision); err != nil {
			return err
		}
		if collision {
			return domain.NewError(domain.Conflict, "group_name_conflict", "a group with that exact name already exists in this project", map[string]any{"name": name})
		}
		// Rewrite only current preferences, never saved execution selections or drafts.
		_, err := conn.ExecContext(ctx, `UPDATE groups SET name=?,revision=revision+1,updated_at=MAX(updated_at,julianday('now')) WHERE group_id=?`, name, id)
		if err != nil {
			return err
		}
		return renameRoutingGroup(ctx, conn, project, g.Name, name)
	})
}
func (r *GroupRepository) update(ctx context.Context, project storage.Project, id, revision int64, f func(*sql.Conn, storage.Group) error) (storage.Group, error) {
	return r.write(ctx, func(conn *sql.Conn) (storage.Group, error) {
		g, err := readGroup(ctx, conn, project, id)
		if err != nil {
			return g, err
		}
		if revision < 1 || g.Revision != revision {
			return g, domain.NewError(domain.Conflict, "group_revision_conflict", "group changed; reload it before saving", map[string]any{"group_id": id, "revision": g.Revision})
		}
		if err := f(conn, g); err != nil {
			return storage.Group{}, err
		}
		return readGroup(ctx, conn, project, id)
	})
}
func renameRoutingGroup(ctx context.Context, conn *sql.Conn, project storage.Project, oldName, newName string) error {
	rows, err := conn.QueryContext(ctx, `SELECT workspace_id,mode,include_ungrouped,groups_json FROM workspace_group_assignments WHERE project_id=?`, project.ID)
	if err != nil {
		return err
	}
	changed := []storage.WorkspaceAssignment{}
	for rows.Next() {
		var a storage.WorkspaceAssignment
		var raw string
		if err = rows.Scan(&a.WorkspaceID, &a.Mode, &a.IncludeUngrouped, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &a.Groups); err != nil {
			break
		}
		if !slices.Contains(a.Groups, oldName) {
			continue
		}
		for i, name := range a.Groups {
			if name == oldName {
				a.Groups[i] = newName
			}
		}
		slices.Sort(a.Groups)
		if err = storage.ValidateWorkspaceAssignment(a); err != nil {
			break
		}
		changed = append(changed, a)
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, a := range changed {
		raw, err := json.Marshal(a.Groups)
		if err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, "UPDATE workspace_group_assignments SET groups_json=? WHERE workspace_id=?", string(raw), a.WorkspaceID); err != nil {
			return err
		}
	}
	// Even an editor that had not selected this name must refresh its choices.
	if _, err = conn.ExecContext(ctx, `INSERT INTO project_group_routing(project_id,revision) VALUES(?,1) ON CONFLICT(project_id) DO UPDATE SET revision=revision+1`, project.ID); err != nil {
		return err
	}
	routing, err := readProjectRouting(ctx, conn, project)
	if err != nil {
		return err
	}
	for _, a := range routing.Assignments {
		if err := storage.ValidateWorkspaceSelection(routing.Selection(a.WorkspaceID)); err != nil {
			return err
		}
	}
	return nil
}
func groupStorageError(err error) error {
	if err == nil {
		return nil
	}
	var known *domain.Error
	if errors.As(err, &known) {
		return err
	}
	if stable := stableDatabaseError("access project groups", err); stable != nil {
		return stable
	}
	return domain.WrapError(domain.Storage, "group_storage_failed", "could not access project groups", nil, err)
}

func (r *WebReader) ListGroups(ctx context.Context, p storage.Project) ([]storage.Group, error) {
	return (&GroupRepository{db: r.db}).ListGroups(ctx, p)
}
func (r *WebReader) ReadGroup(ctx context.Context, p storage.Project, id int64) (storage.Group, error) {
	return (&GroupRepository{db: r.db}).ReadGroup(ctx, p, id)
}
func (w *WebWriter) CreateGroup(ctx context.Context, p storage.Project, name string) (storage.Group, error) {
	return (&GroupRepository{db: w.db}).CreateGroup(ctx, p, name)
}
func (w *WebWriter) EditGroupContext(ctx context.Context, p storage.Project, id, revision int64, markdown string) (storage.Group, error) {
	return (&GroupRepository{db: w.db}).EditGroupContext(ctx, p, id, revision, markdown)
}
func (w *WebWriter) RenameGroup(ctx context.Context, p storage.Project, id, revision int64, name string) (storage.Group, error) {
	return (&GroupRepository{db: w.db}).RenameGroup(ctx, p, id, revision, name)
}

func (w *WebWriter) CreateGroupWithContext(ctx context.Context, p storage.Project, name, markdown string) (storage.Group, error) {
	return (&GroupRepository{db: w.db}).CreateGroupWithContext(ctx, p, name, markdown)
}
