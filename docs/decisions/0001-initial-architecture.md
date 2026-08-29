# ADR 0001: Initial Architecture

- Status: Accepted
- Date: 2026-08-28

> [ADR 0002](0002-worktree-scoped-workspaces.md) supersedes this record's one-active-agent-per-project, project-as-one-root, project-wide-in-progress, and project-wide `next` decisions. The remaining product, ordering, storage, FTS, memory, JSON, and PID/lease rejection decisions stay accepted.

## Context

Coding agents need to split long-running work into a durable, trackable sequence. Markdown task lists work, but repeatedly locating, parsing, and editing them consumes tokens and makes concurrent or partial updates fragile.

Beads-like systems demonstrate the value of structured local work, but dependency graphs, epics, agent coordination, daemons, and synchronization add concepts that make agent behavior harder to predict. Pellets needs a deliberately narrower model.

The product is named Pellets and the executable is `pl`.

## Decision

### Product model

- Pellets is a local task queue for coding agents.
- Worktree-scoped worker coordination is defined by ADR 0002; it adds no agent identity or orchestration.
- A database may contain several Git projects.
- A project has a unique code of at most 12 characters and project-local pellet numbers. Public references look like `foo-123`.
- Pellets have title, description, optional opaque external ID, one optional opaque group, status, nullable priority, and timestamps.
- A group is a project-scoped exact-filter value that can span several external IDs. It has no table, hierarchy, metadata, or effect on priority.
- Statuses are `open`, `in_progress`, `closed`, and `maybe_later`.
- Each registered workspace has at most one in-progress pellet; several workspaces may progress independently in one project.
- `pl next` is read-only. It returns the current workspace's in-progress pellet first, otherwise the highest-priority eligible open pellet.

### Ordering

- Priority is the sole active-queue ordering value, not a category separate from position.
- Every `open` or `in_progress` pellet has a unique sparse integer priority within its project; lower values rank higher. `closed` and `maybe_later` pellets have `NULL` priority.
- New open pellets are appended by default. Reopened pellets return at the end of the active queue.
- Relative `before`/`after` operations allocate an integer midpoint when possible.
- Gap exhaustion triggers a deterministic project-scoped rebalance of actionable rows using a window function and one materialized-CTE update into a fresh priority band.
- Moves and rebalances are transactional.
- Closing or deferring removes a pellet from the active-priority index; these rows are not reprocessed by later rebalances.
- Floating-point ordering and linked lists are forbidden.

### Implementation and storage

- Implement in Go.
- Use SQLite as authoritative storage with explicit SQL and no large ORM.
- Prefer a pinned CGo-free SQLite driver to produce self-contained macOS and Windows executables.
- Store one database at `.pellets/pellets.db` under a database root.
- Discover the nearest database by walking upward, even across Git boundaries.
- Store Git common-directory project identity and worktree-root/Git-directory workspace identity as normalized local paths, relative to the database root where possible.
- Store timestamps as SQLite Julian day `REAL` values and render them as UTC RFC 3339 in JSON.
- Embed forward migrations in the executable.
- Use transactions for every mutation and immediate transactions for allocation, lifecycle uniqueness, reorder, migration, and purge.

### Search and memory

- Use SQLite FTS5 for keyword search over pellets and memories.
- Memory is independent project-scoped text with agent/human provenance and optional human approval.
- Memory has no task foreign key; pellet references may appear as ordinary text.
- Do not automatically create memory from task activity.
- Do not include `sqlite-vec`, semantic search, embedding models, or providers.

### CLI contract

- Compact, versioned JSON is the default output because agents are the primary users.
- Human-readable output is available explicitly and is not a stable scripting interface.
- JSON shapes, machine error codes, stdout/stderr rules, and exit codes are public compatibility contracts.
- Destructive purge is explicit and limited to closed pellets selected by project and optional completion cutoff.

### Explicit exclusions

Do not implement:

- dependencies, edges, blocking, graphs, epics, subtasks, or milestones;
- multi-agent assignment, PID ownership, claiming, leases, or orchestration;
- tags, multiple groups, a group entity or hierarchy, task notes, or an automatic task event/history table;
- a daemon, server, account, network requirement, cloud synchronization, or automatic Git synchronization;
- committing the database to Git;
- plugins or custom workflow machinery.

## Consequences

### Positive

- Agents use small structured commands instead of repeatedly rewriting Markdown.
- One in-progress row per workspace gives deterministic worktree-specific resumption without agent/PID/lease machinery.
- Project-scoped external IDs support splitting one external issue into many pellets; a project-scoped group can collect pellets from several external issues without introducing an epic.
- A shared ancestor database supports several repositories without making IDs globally verbose.
- SQLite provides transactions, constraints, FTS5, and migrations in one local file.
- CGo-free builds reduce cross-platform packaging complexity.
- Dropping vector retrieval eliminates model distribution, native inference, and re-embedding lifecycle concerns.

### Negative

- Two workers using the same worktree are not isolated; distinct registered worktrees coordinate through workspace ownership.
- SQLite allows only one writer at a time across projects sharing a database, although writes should be short.
- Project codes cannot be renamed safely while textual references exist, so v1 treats them as immutable.
- Keyword memory search cannot find purely semantic matches.
- A locally excluded, unsynchronized database is not automatically available in another checkout or machine.
- Hands-on Windows testing is unavailable; CI must carry more confidence than usual.

### Tradeoffs accepted

- Sparse priority occasionally requires an O(n) active-queue rebalance in exchange for simple, exact integer ordering; historical and deferred rows add no rebalance cost.
- Julian `REAL` timestamps simplify SQLite date comparison while JSON hides that storage choice from clients.
- No event table means there is no automatic audit trail or undo, but it avoids unbounded history and additional agent-facing concepts.
- Memory approval tracks validation state without authentication or approver identity.

## Rejected alternatives

### Markdown as authoritative storage

Rejected because agents must repeatedly parse and rewrite surrounding prose, which is less token-efficient and harder to update atomically.

### Dependency graph or epic hierarchy

Rejected because the product needs a linear priority queue. Graph semantics and parent-child planning are the complexity Pellets is intended to avoid.

### Separate priority category and position

Rejected because two ordering mechanisms create ambiguous `next` behavior. A unique integer priority for each actionable pellet is sufficient; non-actionable pellets need no queue position.

### Many-to-many tags or first-class groups

Rejected because the required behavior is one exact filter that may span several external issues. A single nullable group string provides that without a join table, hierarchy, lifecycle, metadata, or another ordering mechanism.

### PID-based claiming or leases

Rejected because the `pl` subprocess PID is short-lived, caller identity is awkward to establish portably, PID reuse needs additional state, and worktree-scoped coordination does not require process ownership, heartbeats, expiry, or background cleanup. See ADR 0002.

### SQLite vector extension and offline embedding model

Rejected because offline embedding adds large model artifacts, native/runtime packaging, cross-platform test burden, and re-embedding behavior disproportionate to the initial memory use case.

### Database per Git project only

Rejected because one user may want a shared pellet database above several related repositories. Project-scoped codes, numbers, and priorities preserve isolation within one file.

### Git synchronization

Rejected because SQLite files and WAL companions are not a merge format. The database is explicitly local and excluded from Git.

## Follow-up decisions

ADR 0002 supplies the accepted multi-worktree coordination model. New decision records are still required before adding agent identity/authentication, PID/session ownership, leases, vector retrieval, synchronization, custom statuses, project-code rename, or any dependency-like relationship.

Related documents:

- [Project goals](../project-goals.md)
- [Architecture](../architecture.md)
- [Data model](../data-model.md)
- [CLI specification](../cli-spec.md)
- [Memory](../memory.md)
- [Implementation plan](../implementation-plan.md)
