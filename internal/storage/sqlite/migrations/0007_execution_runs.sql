-- Orchestration evidence, never queue state. Pellet numbers are monotonic per
-- project; deliberately no pellet FK so a confirmed pellet purge retains the
-- exact original snapshot and verified commit for outstanding review targets.
CREATE TABLE execution_runs (
    run_id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
    workspace_id INTEGER NOT NULL,
    pellet_number INTEGER NOT NULL CHECK (pellet_number > 0),
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    resume_from INTEGER REFERENCES execution_runs(run_id) ON DELETE RESTRICT,
    mode TEXT NOT NULL CHECK (mode IN ('run_one', 'drain', 'watch', 'review_checkpoint')),
    external_id TEXT CHECK (external_id IS NULL OR length(CAST(external_id AS BLOB)) BETWEEN 1 AND 4096),
    group_id TEXT CHECK (group_id IS NULL OR length(CAST(group_id AS BLOB)) BETWEEN 1 AND 4096),
    settings_json TEXT NOT NULL CHECK (json_valid(settings_json) AND length(CAST(settings_json AS BLOB)) <= 32768),
    starting_head TEXT NOT NULL CHECK (length(starting_head) IN (40, 64) AND starting_head NOT GLOB '*[^0-9a-f]*'),
    pellet_title TEXT NOT NULL CHECK (length(CAST(pellet_title AS BLOB)) BETWEEN 1 AND 1048576),
    pellet_description TEXT NOT NULL CHECK (length(CAST(pellet_description AS BLOB)) <= 1048576),
    phase TEXT NOT NULL CHECK (phase IN ('preflight', 'thread_start', 'turn_start', 'implementation', 'verification', 'commit', 'close', 'review', 'finalization')),
    state TEXT NOT NULL CHECK (state IN ('running', 'awaiting_input', 'interrupted', 'needs_attention', 'completed')),
    thread_id TEXT NOT NULL DEFAULT '' CHECK (length(thread_id) <= 256),
    turn_id TEXT NOT NULL DEFAULT '' CHECK (length(turn_id) <= 256 AND (turn_id = '' OR thread_id <> '')),
    outcome TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '' CHECK (length(error_code) <= 256),
    summary TEXT NOT NULL DEFAULT '' CHECK (length(CAST(summary AS BLOB)) <= 1024),
    result_commit TEXT NOT NULL DEFAULT '' CHECK (result_commit = '' OR (length(result_commit) IN (40, 64) AND result_commit NOT GLOB '*[^0-9a-f]*')),
    commit_verified_at TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    pending_operation TEXT NOT NULL DEFAULT '' CHECK (pending_operation IN ('', 'thread/start', 'thread/resume', 'turn/start', 'turn/interrupt')),
    pending_revision INTEGER NOT NULL DEFAULT 0,
    pending_turn_id TEXT NOT NULL DEFAULT '' CHECK (length(pending_turn_id) <= 256),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL CHECK (updated_at >= created_at),
    finished_at TEXT,
    activity_pruned INTEGER NOT NULL DEFAULT 0 CHECK (activity_pruned IN (0, 1)),
    UNIQUE (project_id, pellet_number, attempt),
    FOREIGN KEY (project_id, workspace_id) REFERENCES project_workspaces(project_id, workspace_id) ON DELETE RESTRICT,
    CHECK ((state IN ('running', 'awaiting_input') AND outcome = '' AND finished_at IS NULL)
        OR (state = 'completed' AND outcome = 'succeeded' AND finished_at IS NOT NULL AND result_commit <> '')
        OR (state IN ('interrupted', 'needs_attention') AND outcome IN ('failed', 'cancelled', 'unknown') AND finished_at IS NOT NULL)),
    CHECK ((result_commit = '' AND commit_verified_at IS NULL) OR (result_commit <> '' AND commit_verified_at IS NOT NULL)),
    CHECK (finished_at IS NULL OR finished_at >= created_at),
    CHECK ((pending_operation = '' AND pending_revision = 0 AND pending_turn_id = '') OR (pending_operation <> '' AND pending_revision > 0 AND pending_revision <= revision))
) STRICT;

CREATE UNIQUE INDEX execution_runs_workspace_active_idx ON execution_runs(workspace_id)
    WHERE state IN ('running', 'awaiting_input');
CREATE INDEX execution_runs_workspace_recent_idx ON execution_runs(workspace_id, run_id DESC);
CREATE INDEX execution_runs_retention_idx ON execution_runs(finished_at, run_id) WHERE finished_at IS NOT NULL;

CREATE TABLE execution_run_activity (
    run_id INTEGER NOT NULL REFERENCES execution_runs(run_id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    at TEXT NOT NULL,
    phase TEXT NOT NULL,
    state TEXT NOT NULL,
    summary TEXT NOT NULL CHECK (length(CAST(summary AS BLOB)) <= 1024),
    operation TEXT NOT NULL DEFAULT '' CHECK (operation IN ('', 'thread/start', 'thread/resume', 'turn/start', 'turn/interrupt')),
    PRIMARY KEY (run_id, revision)
) STRICT;
