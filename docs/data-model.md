# Data Model

SQLite is authoritative for logical projects, their registered workspaces, pellets, and memories. FTS5 tables are derived indexes and can always be rebuilt.

This model intentionally contains no dependency, edge, epic, tag, group, task-note, task-event, agent, PID, session, claim, lease, heartbeat, expiry, assignment-history, or vector table. A workspace row is a Git worktree coordination identity, not an agent or security principal. Group is a nullable scalar on a pellet, not an entity. Memory is documented separately in [memory.md](memory.md); CLI behavior is in [cli-spec.md](cli-spec.md).

## Terminology and identity

- A **database root** is the directory containing `.pellets/`.
- A **repository** is one Git repository, identified locally by Git's common directory.
- A **logical project** is that repository's shared Pellets queue, code, numbering, ordering, groups, external IDs, search, and memories.
- A **worktree** is Git's main work tree or one linked worktree.
- A **workspace** is one worktree registered to a logical project, identified by its worktree root and worktree-specific Git directory.
- A **project code** is a unique 1–12 character lowercase code such as `foo`.
- A **pellet number** is a positive integer allocated monotonically within one project.
- A **pellet reference** combines them, for example `foo-123`.
- A **priority** is an actionable pellet’s unique integer order within its project. Lower comes first. Closed and deferred pellets have no priority.
- A **group** is one optional opaque string used to filter related pellets across external IDs within the same project.

References split at the final hyphen, so `foo-bar-123` means project `foo-bar` and pellet number `123`. Numbers use canonical decimal without leading zeros. Project codes are immutable in v1 so references remain stable even when embedded in memory text or external systems. Purged pellet numbers are never reused.

## Proposed schema

The schema below is normative in shape. Migration files may differ in formatting.

```sql
PRAGMA foreign_keys = ON;
PRAGMA trusted_schema = OFF;

CREATE TABLE application_metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

CREATE TABLE projects (
    project_id              INTEGER PRIMARY KEY,
    code                    TEXT NOT NULL UNIQUE,
    git_common_dir          TEXT NOT NULL,
    git_common_dir_relative INTEGER NOT NULL,
    next_pellet_number      INTEGER NOT NULL DEFAULT 1,
    created_at              REAL NOT NULL,
    updated_at              REAL NOT NULL,

    UNIQUE (git_common_dir_relative, git_common_dir),
    CHECK (length(code) BETWEEN 1 AND 12),
    CHECK (code = lower(code)),
    CHECK (code NOT GLOB '*[^a-z0-9-]*'),
    CHECK (substr(code, 1, 1) <> '-'),
    CHECK (substr(code, -1, 1) <> '-'),
    CHECK (git_common_dir <> ''),
    CHECK (git_common_dir_relative IN (0, 1)),
    CHECK (next_pellet_number > 0),
    CHECK (updated_at >= created_at)
) STRICT;

CREATE TABLE project_workspaces (
    workspace_id       INTEGER PRIMARY KEY,
    project_id         INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
    root_path          TEXT NOT NULL,
    root_path_relative INTEGER NOT NULL,
    git_dir            TEXT NOT NULL,
    git_dir_relative   INTEGER NOT NULL,
    created_at         REAL NOT NULL,
    updated_at         REAL NOT NULL,

    UNIQUE (project_id, workspace_id),
    UNIQUE (root_path_relative, root_path),
    UNIQUE (git_dir_relative, git_dir),
    CHECK (root_path <> ''),
    CHECK (git_dir <> ''),
    CHECK (root_path_relative IN (0, 1)),
    CHECK (git_dir_relative IN (0, 1)),
    CHECK (updated_at >= created_at)
) STRICT;

CREATE TABLE pellets (
    rowid        INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
    workspace_id INTEGER,
    number       INTEGER NOT NULL,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    external_id  TEXT,
    group_id     TEXT,
    status       TEXT NOT NULL DEFAULT 'open',
    priority     INTEGER,
    created_at   REAL NOT NULL,
    updated_at   REAL NOT NULL,
    completed_at REAL,

    UNIQUE (project_id, number),
    FOREIGN KEY (project_id, workspace_id)
        REFERENCES project_workspaces(project_id, workspace_id) ON DELETE RESTRICT,
    CHECK (number > 0),
    CHECK (trim(title) <> ''),
    CHECK (external_id IS NULL OR external_id <> ''),
    CHECK (group_id IS NULL OR group_id <> ''),
    CHECK (status IN ('open', 'in_progress', 'closed', 'maybe_later')),
    CHECK (
        (status = 'in_progress' AND workspace_id IS NOT NULL)
        OR
        (status <> 'in_progress' AND workspace_id IS NULL)
    ),
    CHECK (
        (status IN ('open', 'in_progress') AND priority IS NOT NULL AND priority > 0)
        OR
        (status IN ('closed', 'maybe_later') AND priority IS NULL)
    ),
    CHECK (updated_at >= created_at),
    CHECK (
        (status = 'closed' AND completed_at IS NOT NULL AND completed_at >= created_at)
        OR
        (status <> 'closed' AND completed_at IS NULL)
    )
) STRICT;

CREATE UNIQUE INDEX pellets_workspace_in_progress_idx
    ON pellets(workspace_id)
    WHERE status = 'in_progress';

CREATE UNIQUE INDEX pellets_active_priority_idx
    ON pellets(project_id, priority)
    WHERE priority IS NOT NULL;

CREATE INDEX pellets_closed_completed_idx
    ON pellets(project_id, completed_at)
    WHERE status = 'closed';

CREATE TABLE memories (
    memory_id   INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
    text        TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    approved_at REAL,
    created_at  REAL NOT NULL,
    updated_at  REAL NOT NULL,

    CHECK (trim(text) <> ''),
    CHECK (created_by IN ('agent', 'human')),
    CHECK (created_by <> 'human' OR approved_at IS NOT NULL),
    CHECK (approved_at IS NULL OR approved_at >= created_at),
    CHECK (updated_at >= created_at)
) STRICT;

CREATE INDEX memories_project_approval_idx
    ON memories(project_id, approved_at, memory_id);
```

`pellets.rowid` is a database-internal surrogate used by SQLite and FTS. It is never shown as the public pellet ID.

`memories.memory_id` is a database-local, user-visible identity for a removable record. Under [SQLite's `AUTOINCREMENT` allocation rules](https://sqlite.org/autoinc.html), an automatically allocated ID from a committed row is never assigned to a different memory after removal. SQLite may leave gaps, and an allocation rolled back before commit may be reused. This is the only column that needs that guarantee: the additional sequence-maintenance cost is not justified for the internal `pellets.rowid` or unrelated keys, which remain plain `INTEGER PRIMARY KEY` columns.

`projects.git_common_dir` is the repository-sameness key. `project_workspaces` is the authoritative workspace relation; its globally unique root and Git-directory identities prevent one worktree from attaching to two projects. Composite uniqueness on `(project_id, workspace_id)` supports the pellet composite foreign key, which makes cross-project workspace ownership impossible even when application checks are bypassed.

Each path has a companion `*_relative` flag. A relative slash-normalized value is interpreted from the database root. A Git location outside that root is stored as a normalized absolute path. On platforms with case-insensitive path identity, normalization folds case before comparison. Paths are local diagnostics, not portable repository IDs. A moved workspace is updated by `init` only after an outside-transaction check establishes that its old root is absent; a live duplicate conflicts. Removed worktrees remain registered and can own in-progress work until explicit recovery. There is no automatic cleanup.

`pellets.group_id` is not a foreign key. There is no groups table: each pellet has zero or one case-sensitive group string, and the same value may appear under several external IDs in that project. Group equality has no meaning across projects.

Only `open` and `in_progress` pellets participate in the shared project queue and therefore have positive project-unique priority. Exactly `in_progress` pellets have a workspace owner. `open`, `closed`, and `maybe_later` pellets have `NULL` workspace ownership; `closed` and `maybe_later` also have `NULL` priority. There is no temporary negative-priority or stale-owner state.

Known application metadata keys are `database_id` (a randomly generated UUID), `created_at_julian`, and `product` (`pellets`). Unknown keys must be preserved by migrations. Schema versioning belongs exclusively in SQLite's application-owned `PRAGMA user_version`, not in this table or another application table.

## FTS5 indexes

Use external-content FTS tables so text is stored once in authoritative tables:

```sql
CREATE VIRTUAL TABLE pellets_fts USING fts5(
    title,
    description,
    external_id,
    content = 'pellets',
    content_rowid = 'rowid',
    tokenize = "unicode61 tokenchars '-_'"
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
    text,
    content = 'memories',
    content_rowid = 'memory_id',
    tokenize = "unicode61 tokenchars '-_'"
);
```

The SQLite storage implementation maintains FTS rows explicitly in the same transaction as authoritative writes. Do not use database triggers: current SQLite hardening can reject virtual-table writes from triggers when `trusted_schema` is disabled, and explicit maintenance keeps this boundary visible in the storage package.

Insert new content directly. Before deleting or replacing authoritative text, send FTS5 its special delete record using the old column values, then insert the new values when applicable:

```sql
INSERT INTO pellets_fts(rowid, title, description, external_id)
VALUES (:rowid, :title, :description, :external_id);

INSERT INTO pellets_fts(pellets_fts, rowid, title, description, external_id)
VALUES ('delete', :rowid, :old_title, :old_description, :old_external_id);

INSERT INTO memories_fts(rowid, text)
VALUES (:memory_id, :text);

INSERT INTO memories_fts(memories_fts, rowid, text)
VALUES ('delete', :memory_id, :old_text);
```

Only title, description, and external-ID edits touch pellet FTS. Group and external-ID filters are relational predicates; v1 adds no dedicated indexes for them until measurements justify the write cost. Changing status, priority, or group does not rewrite FTS. Closing a pellet therefore leaves its existing FTS row searchable without reindexing it; only purge removes that row. Memory text is immutable in v1. Integration tests must prove that insert, edit, purge/remove, and rebuild produce identical search results.

## Timestamp rules

All stored dates are SQLite Julian day numbers in `REAL` columns. Mutations obtain time from SQLite within the transaction:

```sql
SELECT julianday('now');
```

This gives every statement in a logical mutation one captured timestamp rather than slightly different values. JSON renders timestamps as UTC RFC 3339 strings; the storage representation is not the public API.

- `created_at` never changes.
- `updated_at` changes for user-visible field, status, or explicit ordering changes.
- Internal rebalance updates do not make every pellet appear edited.
- `completed_at` is set when entering `closed` and cleared when reopening.
- Memory `updated_at` changes only when an agent-created memory is first approved; memory text is immutable in v1.

## Allocation invariants

Pellet numbers are allocated through `projects.next_pellet_number` in the same immediate transaction as insertion:

```sql
UPDATE projects
SET next_pellet_number = next_pellet_number + 1,
    updated_at = :now
WHERE project_id = :project_id
RETURNING next_pellet_number - 1;
```

The counter never moves backward, including after purge. Consequently `foo-123` is never reused for different work.

## Task-ordering invariants

For every committed transaction:

1. Every `open` or `in_progress` pellet has a positive priority.
2. Every `closed` or `maybe_later` pellet has `NULL` priority.
3. Non-null priority is unique within a project.
4. Lower priority values sort first within the active queue.
5. Ordering ties are impossible; `number` is used only as a defensive secondary sort in repair tooling.
6. A move affects only one project and is atomic with any rebalance.
7. Closed and deferred pellets are never rebalanced.
8. There is no separate `position` column, floating-point rank, or linked-list pointer.

Use a default stride of `1024`. A new open pellet added without placement options receives `max(non-null priority) + 1024`; an empty active queue starts at `1024`. A pellet created directly in `maybe_later` receives `NULL` priority.

## Moving and rebalancing

`pl move foo-20 --before foo-10` and `--after` operate as follows:

1. Begin an immediate transaction.
2. Require both the moving and target pellets to be `open` or `in_progress`.
3. Load the active project order, excluding the moving pellet when finding its new neighbors.
4. Reject a target from another project.
5. If inserting between priorities `a` and `b`, use `a + (b-a)/2` when `b-a > 1`.
6. At the head or tail, subtract or add the stride when doing so remains positive and cannot overflow.
7. If there is no safe integer gap, rebalance the active queue, reload neighbors, and retry once.
8. Update only the moving pellet’s `updated_at` and commit.

Rebalancing is deterministic by `(priority, number)` and uses one materialized-CTE update. New priorities occupy a fresh stride-aligned band above the project’s current maximum, so they cannot collide with existing unique priorities regardless of SQLite’s row-update order:

```sql
WITH
bounds AS MATERIALIZED (
    SELECT ((MAX(priority) / :stride) + 1) * :stride AS base
    FROM pellets
    WHERE project_id = :project_id
      AND priority IS NOT NULL
),
ranked AS MATERIALIZED (
    SELECT p.rowid,
           b.base
             + (ROW_NUMBER() OVER (ORDER BY p.priority, p.number) - 1)
             * :stride AS new_priority
    FROM pellets AS p
    CROSS JOIN bounds AS b
    WHERE p.project_id = :project_id
      AND p.priority IS NOT NULL
)
UPDATE pellets AS p
SET priority = (
    SELECT r.new_priority
    FROM ranked AS r
    WHERE r.rowid = p.rowid
)
WHERE p.project_id = :project_id
  AND p.priority IS NOT NULL;
```

Before executing the statement, preflight that the fresh band fits in a signed 64-bit integer. The materialized CTE captures the old maximum and order before the update. Absolute priority values are not meaningful; only their relative order and gaps matter.

## Status invariants

Allowed transitions are:

```text
open -> in_progress -> closed
open -> closed
open -> maybe_later
in_progress -> open (release)
in_progress -> maybe_later
closed -> open (reopen)
maybe_later -> open (reopen)
```

The normative transition and ownership table is:

| Command/source | Required workspace relation | Result |
|---|---|---|
| `start` from `open` | Current workspace owns no other pellet | `in_progress`; assign current workspace; retain priority |
| `start` on `in_progress` | Same pellet is owned by current workspace | Idempotent; no timestamp or position change |
| `release` from `in_progress` | Current workspace owns pellet | `open`; clear workspace; retain priority |
| `close` from `open` | No owner exists | `closed`; clear priority/workspace; set `completed_at` |
| `close` from `in_progress` | Current workspace owns pellet | `closed`; clear priority/workspace; set `completed_at` |
| `defer` from `open` | No owner exists | `maybe_later`; clear priority/workspace |
| `defer` from `in_progress` | Current workspace owns pellet | `maybe_later`; clear priority/workspace |
| `reopen` from `closed` or `maybe_later` | No owner exists | `open`; clear `completed_at` and workspace; append to active order |

Starting an open pellet fails with `workspace_already_in_progress` when the current workspace owns a different pellet. Starting or mutating an in-progress pellet owned by another workspace fails with `pellet_in_progress_elsewhere`. `release`, `close`, and `defer` may cross that boundary only with the explicit recovery tuple `--recover-workspace WORKSPACE_ID --yes`, which must match the stored owner and is validated before the short write transaction. Recovery is available after a worktree is removed, is never implicit, does not authenticate a person, and performs the same owner-clearing transition. Reopen and every non-in-progress state always have `NULL` ownership.

The partial unique workspace index permits at most one `in_progress` pellet per workspace even under concurrent commands. Different workspaces in one project may each own one. Active priority remains unique across all open and in-progress rows in the whole project.

## Listing and filtering

List pellets with exact optional filters. Actionable pellets use queue order; non-actionable pellets use recency:

```sql
SELECT p.code, t.number, t.title, t.description, t.external_id, t.group_id,
       t.status, t.priority, t.workspace_id,
       w.root_path AS workspace_root_path,
       t.created_at, t.updated_at, t.completed_at
FROM pellets AS t
JOIN projects AS p USING (project_id)
LEFT JOIN project_workspaces AS w USING (workspace_id)
WHERE t.project_id = :project_id
  AND (:status IS NULL OR t.status = :status)
  AND (:external_id IS NULL OR t.external_id = :external_id)
  AND (:group_id IS NULL OR t.group_id = :group_id)
ORDER BY
  CASE
    WHEN t.status IN ('open', 'in_progress') THEN 0
    WHEN t.status = 'maybe_later' THEN 1
    ELSE 2
  END,
  CASE WHEN t.status IN ('open', 'in_progress') THEN t.priority END,
  CASE WHEN t.status = 'maybe_later' THEN t.updated_at END DESC,
  CASE WHEN t.status = 'closed' THEN t.completed_at END DESC,
  t.number DESC;
```

Closed and `maybe_later` pellets are omitted by the default `list` command, but remain available through status filters or `--all`. A closed-only list is newest-completed first; a deferred-only list is newest-updated first.

## Selecting and starting the next pellet

`next` is read-only. The current workspace's in-progress pellet wins even if it lies outside an external-ID or group focus. It never returns another workspace's in-progress pellet. If the current workspace has no pellet, external-ID and group filters apply conjunctively to open candidates.

```sql
SELECT t.*
FROM pellets AS t
WHERE t.project_id = :project_id
  AND (
      (t.status = 'in_progress' AND t.workspace_id = :workspace_id)
      OR (
          t.status = 'open'
          AND (:external_id IS NULL OR t.external_id = :external_id)
          AND (:group_id IS NULL OR t.group_id = :group_id)
      )
  )
ORDER BY CASE t.status WHEN 'in_progress' THEN 0 ELSE 1 END,
         t.priority
LIMIT 1;
```

The JSON result identifies `resume_in_progress`, `next_open`, or `none` and includes workspace ownership for in-progress rows.

`start-next` accepts the same project, external-ID, and group focus. Inside one `BEGIN IMMEDIATE` transaction it first rechecks the current workspace owner, otherwise selects the lowest-priority matching `open` pellet and updates that exact row to `in_progress` with the current workspace. If no candidate remains it returns the successful typed empty value `selection_reason: "none", pellet: null`. Constraint or selection races retry deterministically within the documented bounded policy; they never return a stale read result or leave a partial write. This atomic command, not `next` followed by `start`, is the multi-worktree claiming primitive.

## Keyword search

The CLI escapes ordinary search text into a safe FTS query. An explicitly named advanced option may accept raw FTS5 syntax later; raw syntax is not the default.

```sql
SELECT p.code, t.number, t.title, t.description, t.external_id, t.group_id,
       t.status, t.priority,
       bm25(pellets_fts, 8.0, 2.0, 1.0) AS rank
FROM pellets_fts
JOIN pellets AS t ON t.rowid = pellets_fts.rowid
JOIN projects AS p USING (project_id)
WHERE pellets_fts MATCH :query
  AND t.project_id = :project_id
  AND (:external_id IS NULL OR t.external_id = :external_id)
  AND (:group_id IS NULL OR t.group_id = :group_id)
ORDER BY rank,
         t.priority IS NULL,
         t.priority,
         t.updated_at DESC
LIMIT :limit;
```

Search rank orders relevance. For equal ranks, actionable pellets sort by priority before non-actionable pellets, then by update time. Exact external-ID and group selection use relational predicates rather than FTS tokenization; dedicated filter indexes are deferred until measurement shows they are needed.

## Purge and deletion behavior

There is no archive state and no ordinary single-pellet delete command in v1. Closed pellets remain in the database until `purge`.

Purge requires a project and deletes:

- all closed pellets in that project; or
- only closed pellets whose `completed_at` is earlier than an optional cutoff.

It never deletes open, in-progress, or `maybe_later` pellets. It never resets `next_pellet_number`. It never deletes memories, even when their text mentions a purged pellet reference. The storage layer removes derived FTS rows in the same transaction.

`VACUUM` is not automatic because it requires additional locking and may be slow. A future maintenance command may expose it.

## Schema migrations

The executable embeds a consecutive sequence of forward migrations beginning at 1. `PRAGMA user_version` is the sole authoritative on-disk schema version, version 0 denotes an empty or uninitialized database, and the schema contains no `schema_migrations` table. Migration names may appear in diagnostics but are not persisted. Released migration files are immutable; frozen database fixtures for every released version provide forward-compatibility evidence in place of persisted migration checksums.

Opening first reads `user_version` without a persistent write. Negative versions, version 0 with persistent schema, and newer versions fail with stable typed errors. An older version is re-read after `BEGIN IMMEDIATE`; all missing migrations, their assertions, each consecutive `user_version` advance, and `foreign_key_check` run on one connection in one transaction before commit. A failure rolls back both schema and version, and a concurrent migrator that waited for the lock applies nothing twice. FTS tables are disposable during migration and may be rebuilt with the FTS5 `rebuild` command after authoritative tables are copied or changed. Destructive migrations require a backup mechanism first. See [architecture.md](architecture.md#migration-strategy).

Migration 3 is the consecutive project-workspace migration. It does not modify released migration 1 or the frozen v1 fixture. It deterministically treats every legacy `projects.root_path` as the initial workspace root, derives the legacy common/Git directory as `<root>/.git`, assigns the project's legacy in-progress pellet to that workspace, and preserves project IDs/codes/counters/timestamps, pellet rowids/numbers/order/timestamps, memories including the `AUTOINCREMENT` high-water mark, application metadata including unknown keys, and all authoritative text. It drops `pellets_one_in_progress_idx`, installs the workspace-scoped index and composite foreign key, rebuilds both FTS tables, verifies their integrity, and removes every temporary table. No Git or filesystem command runs in that migration transaction; later explicit `init` safely reconciles a moved normal workspace identity.
