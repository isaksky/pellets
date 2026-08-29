# Data Model

SQLite is authoritative for projects, pellets, and memories. FTS5 tables are derived indexes and can always be rebuilt.

This model intentionally contains no dependency, edge, epic, tag, group, task-note, task-event, agent, claim, lease, or vector table. Group is a nullable scalar on a pellet, not an entity. Memory is documented separately in [memory.md](memory.md); CLI behavior is in [cli-spec.md](cli-spec.md).

## Terminology and identity

- A **database root** is the directory containing `.pellets/`.
- A **project** is a registered Git work-tree root stored relative to the database root.
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
    project_id         INTEGER PRIMARY KEY,
    code               TEXT NOT NULL UNIQUE,
    root_path          TEXT NOT NULL UNIQUE,
    next_pellet_number INTEGER NOT NULL DEFAULT 1,
    created_at         REAL NOT NULL,
    updated_at         REAL NOT NULL,

    CHECK (length(code) BETWEEN 1 AND 12),
    CHECK (code = lower(code)),
    CHECK (code NOT GLOB '*[^a-z0-9-]*'),
    CHECK (substr(code, 1, 1) <> '-'),
    CHECK (substr(code, -1, 1) <> '-'),
    CHECK (root_path <> ''),
    CHECK (next_pellet_number > 0),
    CHECK (updated_at >= created_at)
) STRICT;

CREATE TABLE pellets (
    rowid        INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
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
    CHECK (number > 0),
    CHECK (trim(title) <> ''),
    CHECK (external_id IS NULL OR external_id <> ''),
    CHECK (group_id IS NULL OR group_id <> ''),
    CHECK (status IN ('open', 'in_progress', 'closed', 'maybe_later')),
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

CREATE UNIQUE INDEX pellets_one_in_progress_idx
    ON pellets(project_id)
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

Pellets store `project_id` rather than repeating the low-cardinality project path. The `projects.root_path` row is the single normalized representation of that path.

`pellets.group_id` is not a foreign key. There is no groups table: each pellet has zero or one case-sensitive group string, and the same value may appear under several external IDs in that project. Group equality has no meaning across projects.

Only `open` and `in_progress` pellets participate in the active queue and therefore have positive priority. `closed` and `maybe_later` pellets have `NULL` priority and no entry in the partial active-priority index. There is no temporary negative-priority state.

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
open --------> in_progress --------> closed
  |                  |                  |
  +-----> closed     +-> maybe_later    +-> open
  |
  +-----> maybe_later -----------------> open
```

- `start`: `open -> in_progress`.
- `close`: `open|in_progress -> closed`, setting priority to `NULL`.
- `reopen`: `closed|maybe_later -> open`, appending at the end of the active queue.
- `defer`: `open|in_progress -> maybe_later`, setting priority to `NULL`.

The partial unique index permits at most one `in_progress` pellet per project even under concurrent commands.

## Listing and filtering

List pellets with exact optional filters. Actionable pellets use queue order; non-actionable pellets use recency:

```sql
SELECT p.code, t.number, t.title, t.description, t.external_id, t.group_id,
       t.status, t.priority, t.created_at, t.updated_at, t.completed_at
FROM pellets AS t
JOIN projects AS p USING (project_id)
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

## Selecting the next pellet

The sole in-progress pellet wins even if it lies outside an external-ID or group focus. This prevents the agent from being directed to new work it cannot start until current work is closed or deferred. If no pellet is in progress, external-ID and group filters apply conjunctively to open candidates.

```sql
SELECT t.*
FROM pellets AS t
WHERE t.project_id = :project_id
  AND (
      t.status = 'in_progress'
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

The JSON result identifies whether it was returned for resumption or as the next open candidate.

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
