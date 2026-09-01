# ADR 0002: Logical Projects and Worktree-Scoped Workspaces

- Status: Accepted
- Date: 2026-08-28
- Supersedes: the one-active-agent-per-project, project-as-one-root, project-wide-in-progress, and related `next` portions of [ADR 0001](0001-initial-architecture.md)

## Context

Git permits one repository to have a main work tree and several linked worktrees. Treating every checkout as a separate Pellets project gives those worktrees different codes, pellet numbers, queues, groups, search results, and memories even though they operate on the same repository. Treating the whole repository as one active-worker slot prevents independent worktrees from safely progressing different pellets.

Git already exposes the required local identities. [`git rev-parse --git-common-dir`](https://git-scm.com/docs/git-rev-parse) identifies shared repository administration, while `--show-toplevel` and `--absolute-git-dir` identify a checkout and its worktree-specific administration. [Git worktree documentation](https://git-scm.com/docs/git-worktree) distinguishes main and linked worktrees and explains move, removal, prune, and repair behavior. These are path identities, not portable repository UUIDs.

The product still does not need an account, authenticated agent, daemon, lease manager, or process supervisor. It needs only enough durable coordination to prevent one worktree from accidentally starting two pellets and to make another worktree's ownership visible.

## Decision

### Identity and shared state

- A logical project represents one Git repository and is keyed locally by its normalized Git common-directory identity.
- A project owns its immutable public code, monotonically allocated pellet numbers, shared active priority order, external IDs, groups, FTS search, and memories.
- A project has an authoritative `project_workspaces` relation containing its main work tree and every registered linked worktree.
- A workspace is identified by the normalized pair of worktree root and worktree-specific Git directory. Its numeric database ID may appear in diagnostic and ownership output; public pellet references remain `foo-123`.
- Store slash-normalized paths relative to the database root when possible and marked absolute paths otherwise. Normalize platform case where path identity is case-insensitive.
- Ordinary upward database discovery remains authoritative. A database may contain multiple workspaces of one project and unrelated sibling repositories as separate projects.

### Registration and stale paths

- After parsing and usage validation, the first valid command that needs the current project/workspace creates or discovers the database, generates and transactionally stores an immutable project code, and registers the logical repository plus current workspace before executing the requested operation. No separate project-initialization command or caller-supplied code exists.
- Repeated exact resolution is read-only and idempotent. A linked worktree attaches to the project found by common-directory identity and reuses its stored code. One worktree attached to two projects and inconsistent common/root/Git-directory identity remain typed conflicts without partial registration.
- Git and filesystem inspection finishes before SQLite write transactions. Commands that do not need the current workspace never register or repair one. An otherwise read-only current-project command may perform this one-time bootstrap before its operation; subsequent reads remain write-free.
- When one Git-directory identity appears at a new root and the old registered root is absent, automatic bootstrap updates that workspace path. If the old root still exists, the second path is a duplicate conflict.
- Removed worktrees remain registered. No timer, heartbeat, background task, or automatic cleanup guesses whether a path is abandoned. Project output exposes enough identity to diagnose stale entries, and lifecycle recovery explicitly names the stored workspace.
- A manually moved main repository may present a new common-directory path and therefore conflict until an explicit future repository-reassociation design exists; Pellets never silently merges repositories by content or remote URL.

### In-progress ownership and queue behavior

- `in_progress` pellets have exactly one `workspace_id` from the same project. All other statuses have no workspace owner.
- A partial unique index allows at most one in-progress pellet per workspace. Different workspaces in one project may each own one.
- The active priority remains positive and unique across the entire project for every open/in-progress pellet.
- `next` is read-only and resumes only the current workspace before considering filtered open work.
- `start` assigns the named open pellet to the current workspace.
- `start-next` atomically resumes or selects and starts eligible work so two worktrees cannot act on the same read-only result.
- Owner `release` returns work to open, clears ownership, and retains priority. Cross-workspace release, close, and defer conflict unless the caller supplies the exact stored workspace ID and explicit confirmation for recovery. Recovery coordinates state; it does not authenticate a human or identify an agent.
- Reopen never restores workspace ownership.

### Database enforcement and migration

- A composite foreign key from `(pellets.project_id, pellets.workspace_id)` to `(project_workspaces.project_id, project_workspaces.workspace_id)` enforces same-project ownership.
- A `CHECK` enforces ownership exactly for `in_progress`; the workspace partial unique index enforces one per workspace; the existing project-priority partial unique index remains shared.
- Migration 3 uses create/copy/validate/swap without changing frozen migration 1 or its fixture. Each legacy project root becomes its initial workspace and any legacy in-progress pellet is assigned to it.
- Project IDs/codes/counters/timestamps, pellet rowids/numbers/order/timestamps, memories and their allocation high-water mark, application metadata, and authoritative text are preserved. Derived FTS tables are rebuilt and verified.
- Migration SQL and Git/filesystem discovery never share a transaction. Any SQL, assertion, version, foreign-key, or commit failure restores the preceding schema and `user_version`.

### Explicit exclusions retained

There are no agent accounts, agent IDs, PIDs, process owners, sessions, leases, heartbeats, timeouts, expiry fields, assignment histories, presence records, or event rows. Two workers in one worktree are not isolated. ADR 0001's rejection of PID/lease orchestration remains in force; its rationale no longer depends on only one worker per logical project.

## Consequences

### Positive

- Linked worktrees share the state users expect from one repository while independently resuming their own work.
- Database constraints protect ownership even when application code is bypassed.
- Atomic `start-next` supports concurrent workers without a daemon or identity system.
- Stale ownership remains diagnosable and recoverable without silent stealing.

### Negative

- Project and pellet output carries workspace identity, and lifecycle commands have additional conflicts and recovery options.
- Local path identity cannot automatically recognize every manual repository move or copy.
- Removed workspace rows accumulate until an explicit safe administrative design is justified.

## Superseded ADR 0001 text

ADR 0001 remains authoritative for the product boundary, ordering, storage, FTS, memory, JSON, and rejected PID/lease machinery except as follows:

- the original project-wide worker restriction becomes at most one active worker per worktree;
- a registered Git root becomes a logical repository plus registered workspace relation;
- the original project-wide in-progress limit becomes one per workspace;
- `next` resumes the current workspace only, and `start-next` is the atomic start path;
- the absence of ownership means no agent/process ownership; a workspace foreign key is now stored for worktree coordination.

Related documents:

- [Project goals](../project-goals.md)
- [Architecture](../architecture.md)
- [Data model](../data-model.md)
- [CLI specification](../cli-spec.md)
- [Memory](../memory.md)
- [Implementation plan](../implementation-plan.md)
