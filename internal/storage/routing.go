package storage

import (
	"context"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"pellets/internal/domain"
)

// WorkspaceAssignment routes the next web-scheduled claim. Groups retain opaque
// exact bytes; assignments never change pellet ownership or CLI group filters.
type WorkspaceAssignment struct {
	WorkspaceID      int64    `json:"workspace_id"`
	Mode             string   `json:"mode"`
	Groups           []string `json:"groups"`
	IncludeUngrouped bool     `json:"include_ungrouped"`
}

type ProjectRouting struct {
	ProjectID   int64                 `json:"project_id"`
	Enabled     bool                  `json:"enabled"`
	Version     string                `json:"version"`
	Assignments []WorkspaceAssignment `json:"assignments"`
}

// WorkspaceSelection is the policy observed atomically when a pellet was
// claimed. Groups are inclusions in explicit mode and exclusions in remaining
// mode. A nil selection denotes the legacy exact-filter scheduling contract.
type WorkspaceSelection struct {
	Enabled          bool     `json:"enabled"`
	Mode             string   `json:"mode"`
	Groups           []string `json:"groups"`
	IncludeUngrouped bool     `json:"include_ungrouped"`
}

func DefaultWorkspaceAssignment(workspaceID int64) WorkspaceAssignment {
	return WorkspaceAssignment{WorkspaceID: workspaceID, Mode: "remaining", Groups: []string{}, IncludeUngrouped: true}
}
func (r ProjectRouting) Assignment(workspaceID int64) WorkspaceAssignment {
	for _, a := range r.Assignments {
		if a.WorkspaceID == workspaceID {
			return a
		}
	}
	return DefaultWorkspaceAssignment(workspaceID)
}
func (r ProjectRouting) Selection(workspaceID int64) *WorkspaceSelection {
	a := r.Assignment(workspaceID)
	s := &WorkspaceSelection{Enabled: r.Enabled, Mode: a.Mode, IncludeUngrouped: a.IncludeUngrouped, Groups: append([]string{}, a.Groups...)}
	if a.Mode == "remaining" {
		s.Groups = []string{}
		for _, other := range r.Assignments {
			if other.WorkspaceID != workspaceID && other.Mode == "explicit" {
				s.Groups = append(s.Groups, other.Groups...)
			}
		}
		slices.Sort(s.Groups)
		s.Groups = slices.Compact(s.Groups)
	}
	return s
}
func (s *WorkspaceSelection) AcceptsGroup(group *string) bool {
	if s == nil || !s.Enabled {
		return true
	}
	if group == nil {
		return s.IncludeUngrouped
	}
	contains := slices.Contains(s.Groups, *group)
	if s.Mode == "remaining" {
		return !contains
	}
	return contains
}
func (r ProjectRouting) Accepts(workspaceID int64, p Pellet) bool {
	if p.Workspace != nil && p.Workspace.ID == workspaceID {
		return true
	}
	s := r.Selection(workspaceID)
	if p.Checkpoint != nil {
		for _, target := range p.Checkpoint.Targets {
			if s.AcceptsGroup(target.Group) {
				return true
			}
		}
		return false
	}
	return s.AcceptsGroup(p.Group)
}
func ValidateWorkspaceAssignment(a WorkspaceAssignment) error {
	if a.WorkspaceID < 1 || (a.Mode != "explicit" && a.Mode != "remaining") || len(a.Groups) > 256 {
		return InvalidWorkspaceAssignment()
	}
	size := 0
	for _, group := range a.Groups {
		size += len(group)
		if group == "" || len(group) > 4096 || !utf8.ValidString(group) {
			return InvalidWorkspaceAssignment()
		}
	}
	if size > 32768 {
		return InvalidWorkspaceAssignment()
	}
	encoded, err := json.Marshal(a)
	if err != nil || len(encoded) > 16384 {
		return InvalidWorkspaceAssignment()
	}
	return nil
}
func ValidateWorkspaceSelection(s *WorkspaceSelection) error {
	if s == nil {
		return nil
	}
	return ValidateWorkspaceAssignment(WorkspaceAssignment{WorkspaceID: 1, Mode: s.Mode, Groups: s.Groups, IncludeUngrouped: s.IncludeUngrouped})
}
func InvalidWorkspaceAssignment() error {
	return domain.NewError(domain.Usage, "invalid_workspace_assignment", "choose explicit groups or all other groups; group names must be nonempty exact UTF-8 values within the assignment limit", nil)
}
func RoutingConflict(current ProjectRouting) error {
	return domain.NewError(domain.Conflict, "workspace_assignment_conflict", "workspace assignments changed; reload the current settings before saving", map[string]any{"version": current.Version})
}

type WorkspaceRoutingReader interface {
	ReadProjectRouting(context.Context, Project) (ProjectRouting, error)
}
type WorkspaceRoutingWriter interface {
	SaveWorkspaceAssignment(context.Context, Project, int64, string, WorkspaceAssignment) (ProjectRouting, error)
	SetGroupAssignments(context.Context, Project, string, bool) (ProjectRouting, error)
}
