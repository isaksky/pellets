-- Separate logical Git repositories from their checked-out worktrees.  This
-- migration deliberately derives legacy identities from the only path v1
-- stored; Git/filesystem inspection remains outside SQLite transactions.

CREATE TABLE migration_0003_state (
    memory_sequence INTEGER NOT NULL
) STRICT;

INSERT INTO migration_0003_state(memory_sequence)
VALUES (COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'memories'), 0));

DROP TABLE pellets_fts;
DROP TABLE memories_fts;

ALTER TABLE projects RENAME TO projects_v2;
ALTER TABLE pellets RENAME TO pellets_v2;
ALTER TABLE memories RENAME TO memories_v2;

DROP INDEX pellets_one_in_progress_idx;
DROP INDEX pellets_active_priority_idx;
DROP INDEX pellets_closed_completed_idx;
DROP INDEX memories_project_approval_idx;

CREATE TABLE projects (
    project_id               INTEGER PRIMARY KEY,
    code                     TEXT NOT NULL UNIQUE,
    git_common_dir           TEXT NOT NULL,
    git_common_dir_relative  INTEGER NOT NULL,
    next_pellet_number       INTEGER NOT NULL DEFAULT 1,
    created_at               REAL NOT NULL,
    updated_at               REAL NOT NULL,

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

INSERT INTO projects(
    project_id, code, git_common_dir, git_common_dir_relative,
    next_pellet_number, created_at, updated_at
)
SELECT project_id,
       code,
       CASE root_path WHEN '.' THEN '.git' ELSE root_path || '/.git' END,
       1,
       next_pellet_number,
       created_at,
       updated_at
FROM projects_v2;

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

INSERT INTO project_workspaces(
    workspace_id, project_id, root_path, root_path_relative,
    git_dir, git_dir_relative, created_at, updated_at
)
SELECT project_id,
       project_id,
       root_path,
       1,
       CASE root_path WHEN '.' THEN '.git' ELSE root_path || '/.git' END,
       1,
       created_at,
       updated_at
FROM projects_v2;

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

INSERT INTO pellets(
    rowid, project_id, workspace_id, number, title, description,
    external_id, group_id, status, priority, created_at, updated_at,
    completed_at
)
SELECT rowid,
       project_id,
       CASE status WHEN 'in_progress' THEN project_id ELSE NULL END,
       number,
       title,
       description,
       external_id,
       group_id,
       status,
       priority,
       created_at,
       updated_at,
       completed_at
FROM pellets_v2;

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

INSERT INTO memories(
    memory_id, project_id, text, created_by, approved_at, created_at, updated_at
)
SELECT memory_id, project_id, text, created_by, approved_at, created_at, updated_at
FROM memories_v2;

UPDATE sqlite_sequence
SET seq = MAX(seq, (SELECT memory_sequence FROM migration_0003_state))
WHERE name = 'memories';

INSERT INTO sqlite_sequence(name, seq)
SELECT 'memories', memory_sequence
FROM migration_0003_state
WHERE memory_sequence > 0
  AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'memories');

CREATE INDEX memories_project_approval_idx
    ON memories(project_id, approved_at, memory_id);

DROP TABLE pellets_v2;
DROP TABLE memories_v2;
DROP TABLE projects_v2;
DROP TABLE migration_0003_state;

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

INSERT INTO pellets_fts(pellets_fts) VALUES ('rebuild');
INSERT INTO memories_fts(memories_fts) VALUES ('rebuild');
