package storage

import "context"

// WorkspacePruner revalidates identity and durable protection inside the write
// transaction. An empty reason means the registration was (or would be) pruned.
type WorkspacePruner interface {
	PruneWorkspace(context.Context, Workspace, bool) (reason string, err error)
}
