package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

type routingQuery interface {
	projectQuery
	rowsQuery
}

func readProjectRouting(ctx context.Context, query routingQuery, project storage.Project) (storage.ProjectRouting, error) {
	r := storage.ProjectRouting{ProjectID: project.ID, Enabled: true, Assignments: []storage.WorkspaceAssignment{}}
	if err := ensureStoredProject(ctx, query, project); err != nil {
		return r, err
	}
	var revision int64
	err := query.QueryRowContext(ctx, "SELECT enabled, revision FROM project_group_routing WHERE project_id=?", project.ID).Scan(&r.Enabled, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	workspaces, err := loadWorkspaces(ctx, query, project.ID)
	if err != nil {
		return r, err
	}
	var databasePath string
	if err := query.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&databasePath); err != nil {
		return r, err
	}
	root := filepath.Dir(databasePath)
	if filepath.Base(root) == discovery.MetadataDirectory {
		root = filepath.Dir(root)
	}
	for _, w := range workspaces {
		if w.GitDir == project.GitCommonDir {
			r.MainWorkspaceID = w.ID
		}
		for _, path := range []domain.LocalPath{w.RootPath, w.GitDir} {
			absolute, resolveErr := discovery.ResolveLocalPath(root, path)
			if resolveErr != nil {
				return r, resolveErr
			}
			info, statErr := os.Stat(absolute)
			if statErr != nil || !info.IsDir() {
				r.UnavailableWorkspaces = append(r.UnavailableWorkspaces, w.ID)
				break
			}
		}
		a := storage.DefaultWorkspaceAssignment(w.ID)
		var groups string
		err := query.QueryRowContext(ctx, "SELECT mode, include_ungrouped, groups_json FROM workspace_group_assignments WHERE project_id=? AND workspace_id=?", project.ID, w.ID).Scan(&a.Mode, &a.IncludeUngrouped, &groups)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		if err == nil {
			if err = json.Unmarshal([]byte(groups), &a.Groups); err != nil {
				return r, err
			}
			if err = storage.ValidateWorkspaceAssignment(a); err != nil {
				return r, err
			}
		}
		r.Assignments = append(r.Assignments, a)
	}
	// Workspace registration must also invalidate an open assignment editor.
	ids := make([]string, 0, len(workspaces))
	for _, w := range workspaces {
		id := strconv.FormatInt(w.ID, 10)
		if slices.Contains(r.UnavailableWorkspaces, w.ID) {
			id += "-unavailable"
		}
		ids = append(ids, id)
	}
	r.Version = strconv.FormatInt(revision, 10) + ":" + strings.Join(ids, ",")
	return r, nil
}
func (reader *WebReader) ReadProjectRouting(ctx context.Context, project storage.Project) (storage.ProjectRouting, error) {
	tx, err := reader.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return storage.ProjectRouting{}, err
	}
	defer tx.Rollback()
	r, err := readProjectRouting(ctx, tx, project)
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}
func (writer *WebWriter) SaveWorkspaceAssignment(ctx context.Context, project storage.Project, workspaceID int64, expectedVersion string, a storage.WorkspaceAssignment) (storage.ProjectRouting, error) {
	a.WorkspaceID = workspaceID
	if err := storage.ValidateWorkspaceAssignment(a); err != nil {
		return storage.ProjectRouting{}, err
	}
	a.Groups = append([]string{}, a.Groups...)
	slices.Sort(a.Groups)
	a.Groups = slices.Compact(a.Groups)
	return writer.updateRouting(ctx, project, expectedVersion, func(conn *sql.Conn) error {
		selected := storage.ResolvedProject{Project: project, Workspace: storage.Workspace{ID: workspaceID, ProjectID: project.ID}}
		if err := ensureStoredProjectWorkspace(ctx, conn, selected); err != nil {
			return err
		}
		groups, err := json.Marshal(a.Groups)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO workspace_group_assignments(workspace_id,project_id,mode,include_ungrouped,groups_json) VALUES(?,?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET mode=excluded.mode,include_ungrouped=excluded.include_ungrouped,groups_json=excluded.groups_json`, workspaceID, project.ID, a.Mode, a.IncludeUngrouped, string(groups))
		return err
	})
}
func (writer *WebWriter) SetGroupAssignments(ctx context.Context, project storage.Project, expectedVersion string, enabled bool) (storage.ProjectRouting, error) {
	return writer.updateRouting(ctx, project, expectedVersion, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "UPDATE project_group_routing SET enabled=? WHERE project_id=?", enabled, project.ID)
		return err
	})
}

// SaveRoutingRecipients changes one category across the project in one versioned
// transaction. An empty recipient list is valid: resolution supplies main later.
func (writer *WebWriter) SaveRoutingRecipients(ctx context.Context, project storage.Project, expectedVersion, category string, recipients []int64) (storage.ProjectRouting, error) {
	if category != "remaining" && category != "ungrouped" {
		return storage.ProjectRouting{}, storage.InvalidWorkspaceAssignment()
	}
	return writer.updateRouting(ctx, project, expectedVersion, func(conn *sql.Conn) error {
		current, err := readProjectRouting(ctx, conn, project)
		if err != nil {
			return err
		}
		for _, id := range recipients {
			if !slices.ContainsFunc(current.Assignments, func(a storage.WorkspaceAssignment) bool { return a.WorkspaceID == id }) {
				return storage.InvalidWorkspaceAssignment()
			}
		}
		for _, a := range current.Assignments {
			mode, ungrouped := a.Mode, a.IncludeUngrouped
			chosen := slices.Contains(recipients, a.WorkspaceID)
			if category == "ungrouped" {
				a.IncludeUngrouped = chosen
			} else if chosen {
				a.Mode = "remaining"
			} else if a.Mode == "remaining" {
				a.Mode = "explicit"
			}
			if a.Mode == mode && a.IncludeUngrouped == ungrouped {
				continue
			}
			groups, err := json.Marshal(a.Groups)
			if err != nil {
				return err
			}
			_, err = conn.ExecContext(ctx, `INSERT INTO workspace_group_assignments(workspace_id,project_id,mode,include_ungrouped,groups_json) VALUES(?,?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET mode=excluded.mode,include_ungrouped=excluded.include_ungrouped,groups_json=excluded.groups_json`, a.WorkspaceID, project.ID, a.Mode, a.IncludeUngrouped, string(groups))
			if err != nil {
				return err
			}
		}
		return nil
	})
}
func (writer *WebWriter) updateRouting(ctx context.Context, project storage.Project, expectedVersion string, write func(*sql.Conn) error) (result storage.ProjectRouting, err error) {
	db := ProjectDatabase{db: writer.db}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		current, err := readProjectRouting(ctx, conn, project)
		if err != nil {
			return err
		}
		if expectedVersion == "" || current.Version != expectedVersion {
			return storage.RoutingConflict(current)
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO project_group_routing(project_id) VALUES(?) ON CONFLICT(project_id) DO NOTHING", project.ID); err != nil {
			return err
		}
		if err = write(conn); err != nil {
			return err
		}
		if _, err = conn.ExecContext(ctx, "UPDATE project_group_routing SET revision=revision+1 WHERE project_id=?", project.ID); err != nil {
			return err
		}
		result, err = readProjectRouting(ctx, conn, project)
		if err == nil {
			for _, assignment := range result.Assignments {
				if err = storage.ValidateWorkspaceSelection(result.Selection(assignment.WorkspaceID)); err != nil {
					return err
				}
			}
		}
		return err
	})
	return
}

// The policy is resolved on the same connection and writer transaction as
// selection and ownership, so assignment edits cannot race a claim.
func loadNextRoutedOpenPellet(ctx context.Context, query projectQuery, project storage.ResolvedProject, externalID, group *string, selection *storage.WorkspaceSelection) (storage.Pellet, error) {
	statement := pelletSelect + " WHERE p.project_id=? AND p.status='open' AND " + checkpointEligibleSQL
	args := []any{project.Project.ID}
	if externalID != nil {
		statement += " AND p.external_id=?"
		args = append(args, *externalID)
	}
	if group != nil {
		statement += " AND p.group_id=?"
		args = append(args, *group)
	}
	if selection != nil && selection.Enabled {
		predicate, values := routingGroupPredicate("p.group_id", selection)
		targetPredicate, targetValues := routingGroupPredicate("t.group_id", selection)
		statement += " AND ((p.kind='ordinary' AND " + predicate + ") OR (p.kind='review_checkpoint' AND EXISTS(SELECT 1 FROM review_checkpoint_targets t WHERE t.project_id=p.project_id AND t.checkpoint_number=p.number AND " + targetPredicate + ")))"
		args = append(args, values...)
		args = append(args, targetValues...)
	}
	statement += " ORDER BY p.priority,p.number LIMIT 1"
	return scanPellet(query.QueryRowContext(ctx, statement, args...))
}
func routingGroupPredicate(column string, s *storage.WorkspaceSelection) (string, []any) {
	args := []any{}
	grouped := "0"
	if s.Mode == "remaining" {
		grouped = column + " IS NOT NULL"
	}
	if len(s.Groups) > 0 {
		placeholders := make([]string, len(s.Groups))
		for i, g := range s.Groups {
			placeholders[i] = "?"
			args = append(args, g)
		}
		op := " IN ("
		if s.Mode == "remaining" {
			op = " NOT IN ("
		}
		grouped = column + op + strings.Join(placeholders, ",") + ")"
	}
	if s.IncludeUngrouped {
		grouped = "(" + column + " IS NULL OR " + grouped + ")"
	}
	return grouped, args
}
