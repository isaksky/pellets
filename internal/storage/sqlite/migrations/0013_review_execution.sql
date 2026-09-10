-- Review checkpoints complete without manufacturing an implementation commit.
-- The immutable input snapshot and structured reviewer output remain bounded
-- durable evidence for resume and the separately-owned follow-up phase.
PRAGMA defer_foreign_keys = ON;
DROP TRIGGER review_checkpoint_preserve_purged_evidence;
DROP VIEW review_checkpoint_readiness;

CREATE TABLE execution_runs_v13 (
    run_id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
    workspace_id INTEGER NOT NULL,
    pellet_number INTEGER NOT NULL CHECK (pellet_number > 0),
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    resume_from INTEGER REFERENCES execution_runs_v13(run_id) ON DELETE RESTRICT,
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
    pending_operation TEXT NOT NULL DEFAULT '' CHECK (pending_operation IN ('', 'thread/start', 'thread/resume', 'turn/start', 'turn/interrupt', 'review/start')),
    pending_revision INTEGER NOT NULL DEFAULT 0,
    pending_turn_id TEXT NOT NULL DEFAULT '' CHECK (length(pending_turn_id) <= 256),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL CHECK (updated_at >= created_at),
    finished_at TEXT,
    activity_pruned INTEGER NOT NULL DEFAULT 0 CHECK (activity_pruned IN (0, 1)),
    prompt_prefix_json TEXT NOT NULL DEFAULT '{"template_version":"legacy"}' CHECK (json_valid(prompt_prefix_json) AND length(CAST(prompt_prefix_json AS BLOB)) <= 2097152),
    cached_input_tokens INTEGER CHECK (cached_input_tokens IS NULL OR cached_input_tokens >= 0),
    finalization_json TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(finalization_json) AND length(CAST(finalization_json AS BLOB)) <= 1048576),
    interaction_json TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(interaction_json) AND length(CAST(interaction_json AS BLOB)) <= 262144),
    starting_ref TEXT NOT NULL DEFAULT '' CHECK (length(CAST(starting_ref AS BLOB)) <= 4096),
    schedule_mode TEXT NOT NULL DEFAULT 'run_one' CHECK (schedule_mode IN ('run_one','drain','watch')),
    schedule_remaining INTEGER NOT NULL DEFAULT 1 CHECK (schedule_remaining BETWEEN 1 AND 10000),
    implementation_revision INTEGER NOT NULL DEFAULT 0 CHECK (implementation_revision >= 0),
    checkpoint_scope_json TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(checkpoint_scope_json)),
    review_snapshot_json TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(review_snapshot_json) AND length(CAST(review_snapshot_json AS BLOB)) <= 1048576),
    review_result_json TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(review_result_json) AND length(CAST(review_result_json AS BLOB)) <= 1048576),
    UNIQUE (project_id, pellet_number, attempt),
    FOREIGN KEY (project_id, workspace_id) REFERENCES project_workspaces(project_id, workspace_id) ON DELETE RESTRICT,
    CHECK ((state IN ('running', 'awaiting_input') AND outcome = '' AND finished_at IS NULL)
        OR (state = 'completed' AND outcome = 'succeeded' AND finished_at IS NOT NULL
            AND ((mode = 'review_checkpoint' AND result_commit = '' AND review_result_json <> 'null')
              OR (mode <> 'review_checkpoint' AND result_commit <> '' AND review_result_json = 'null')))
        OR (state IN ('interrupted', 'needs_attention') AND outcome IN ('failed', 'cancelled', 'unknown') AND finished_at IS NOT NULL)),
    CHECK ((result_commit = '' AND commit_verified_at IS NULL) OR (result_commit <> '' AND commit_verified_at IS NOT NULL)),
    CHECK (finished_at IS NULL OR finished_at >= created_at),
    CHECK ((pending_operation = '' AND pending_revision = 0 AND pending_turn_id = '') OR (pending_operation <> '' AND pending_revision > 0 AND pending_revision <= revision))
) STRICT;

INSERT INTO execution_runs_v13 SELECT
    run_id, project_id, workspace_id, pellet_number, attempt, resume_from, mode,
    external_id, group_id, settings_json, starting_head, pellet_title,
    pellet_description, phase, state, thread_id, turn_id, outcome, error_code,
    summary, result_commit, commit_verified_at, revision, pending_operation,
    pending_revision, pending_turn_id, created_at, updated_at, finished_at,
    activity_pruned, prompt_prefix_json, cached_input_tokens, finalization_json,
    interaction_json, starting_ref, schedule_mode, schedule_remaining,
    implementation_revision, checkpoint_scope_json, 'null', 'null'
FROM execution_runs;

CREATE TABLE execution_run_activity_v13 (
    run_id INTEGER NOT NULL REFERENCES execution_runs_v13(run_id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    at TEXT NOT NULL,
    phase TEXT NOT NULL,
    state TEXT NOT NULL,
    summary TEXT NOT NULL CHECK (length(CAST(summary AS BLOB)) <= 1024),
    operation TEXT NOT NULL DEFAULT '' CHECK (operation IN ('', 'thread/start', 'thread/resume', 'turn/start', 'turn/interrupt', 'review/start')),
    PRIMARY KEY (run_id, revision)
) STRICT;
INSERT INTO execution_run_activity_v13 SELECT * FROM execution_run_activity;
DROP TABLE execution_run_activity;
DROP TABLE execution_runs;
ALTER TABLE execution_runs_v13 RENAME TO execution_runs;
ALTER TABLE execution_run_activity_v13 RENAME TO execution_run_activity;
CREATE UNIQUE INDEX execution_runs_workspace_active_idx ON execution_runs(workspace_id) WHERE state IN ('running', 'awaiting_input');
CREATE INDEX execution_runs_workspace_recent_idx ON execution_runs(workspace_id, run_id DESC);
CREATE INDEX execution_runs_retention_idx ON execution_runs(finished_at, run_id) WHERE finished_at IS NOT NULL;
CREATE INDEX execution_runs_review_evidence ON execution_runs(project_id, pellet_number, implementation_revision, run_id);

CREATE VIEW review_checkpoint_readiness AS
SELECT t.*, target.status AS target_status, target.implementation_revision,
    CASE WHEN target.number IS NULL THEN 'target_missing'
         WHEN target.title IS NOT t.title OR target.description IS NOT t.description
           OR target.external_id IS NOT t.external_id OR target.group_id IS NOT t.group_id THEN 'scope_changed'
         WHEN target.status <> 'closed' THEN 'target_incomplete'
         WHEN evidence.run_id IS NULL THEN 'evidence_missing'
         ELSE 'ready' END AS reason,
    evidence.run_id, evidence.workspace_id, evidence.starting_head, evidence.result_commit
FROM review_checkpoint_targets t
LEFT JOIN pellets target ON target.project_id=t.project_id AND target.number=t.target_number
LEFT JOIN execution_runs evidence ON evidence.run_id=CASE
    WHEN target.number IS NULL THEN t.purged_evidence_run_id
    ELSE (
    SELECT r.run_id FROM execution_runs r
    WHERE r.project_id=t.project_id AND r.pellet_number=t.target_number
      AND r.implementation_revision=target.implementation_revision
      AND r.pellet_title=t.title AND r.pellet_description=t.description
      AND r.mode <> 'review_checkpoint' AND r.state='completed' AND r.outcome='succeeded'
      AND r.pending_operation='' AND r.result_commit<>'' AND r.result_commit<>r.starting_head
      AND r.commit_verified_at IS NOT NULL AND r.thread_id<>'' AND r.turn_id<>''
      AND r.finalization_json<>'null'
    ORDER BY r.run_id DESC LIMIT 1
) END;

CREATE TRIGGER review_checkpoint_preserve_purged_evidence BEFORE DELETE ON pellets
WHEN OLD.kind='ordinary'
BEGIN
    UPDATE review_checkpoint_targets
    SET purged_evidence_run_id=(
        SELECT r.run_id FROM review_checkpoint_readiness r
        WHERE r.project_id=review_checkpoint_targets.project_id
          AND r.checkpoint_number=review_checkpoint_targets.checkpoint_number
          AND r.target_number=review_checkpoint_targets.target_number
    )
    WHERE project_id=OLD.project_id AND target_number=OLD.number;
END;

-- INSERT ... SELECT creates an empty sequence row even when no run exists.
-- Preserve the prior allocator high-water mark without manufacturing one.
DELETE FROM sqlite_sequence WHERE name='execution_runs';
INSERT INTO sqlite_sequence(name, seq)
SELECT 'execution_runs', MAX(run_id) FROM execution_runs HAVING MAX(run_id) IS NOT NULL;
