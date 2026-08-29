CREATE TABLE application_metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    checksum   TEXT NOT NULL,
    applied_at REAL NOT NULL
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
    memory_id   INTEGER PRIMARY KEY,
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
