package storage

import (
	"context"
	"time"
	"unicode/utf8"

	"pellets/internal/domain"
)

// MaxGroupContextBytes bounds raw Markdown, measured in UTF-8 bytes.
const MaxGroupContextBytes = 1024 * 1024

// Group is project-level shared context, independent of queue membership.
type Group struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	Name      string    `json:"name"`
	Context   string    `json:"context"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GroupContext is the current shared document returned with a CLI pellet detail
// or selection. It is not a retained execution snapshot.
type GroupContext struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Context  string `json:"context"`
}

func ValidateGroupName(name string) error {
	if name == "" || !utf8.ValidString(name) {
		return domain.NewError(domain.Usage, "invalid_group_name", "group names must be nonempty UTF-8; names are preserved exactly", nil)
	}
	return nil
}

func ValidateGroupContext(markdown string) error {
	if !utf8.ValidString(markdown) || len(markdown) > MaxGroupContextBytes {
		return domain.NewError(domain.Usage, "invalid_group_context", "group context must be valid UTF-8 and at most 1 MiB; empty context is valid", nil)
	}
	return nil
}

type GroupReader interface {
	ListGroups(context.Context, Project) ([]Group, error)
	ReadGroup(context.Context, Project, int64) (Group, error)
}

type GroupWriter interface {
	// CreateGroup is idempotent by exact name and never replaces existing context.
	CreateGroup(context.Context, Project, string) (Group, error)
	EditGroupContext(context.Context, Project, int64, int64, string) (Group, error)
	RenameGroup(context.Context, Project, int64, int64, string) (Group, error)
}

type GroupRepository interface {
	GroupReader
	GroupWriter
	// CreateGroupWithContext atomically creates a new group and its initial
	// context. Unlike implicit creation, an existing exact name is a conflict.
	CreateGroupWithContext(context.Context, Project, string, string) (Group, error)
	Close() error
}
