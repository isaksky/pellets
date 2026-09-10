ALTER TABLE pellets ADD COLUMN kind TEXT NOT NULL DEFAULT 'ordinary'
    CHECK (kind IN ('ordinary', 'review_checkpoint'));
ALTER TABLE pellets ADD COLUMN implementation_revision INTEGER NOT NULL DEFAULT 1 CHECK (implementation_revision > 0);
-- Legacy attempts cannot establish which lifecycle generation they implemented.
ALTER TABLE execution_runs ADD COLUMN implementation_revision INTEGER NOT NULL DEFAULT 0 CHECK (implementation_revision >= 0);
ALTER TABLE execution_runs ADD COLUMN checkpoint_scope_json TEXT NOT NULL DEFAULT 'null' CHECK (json_valid(checkpoint_scope_json));

CREATE TRIGGER pellets_implementation_revision AFTER UPDATE OF title, description, external_id, group_id, status ON pellets
WHEN NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
  OR NEW.external_id IS NOT OLD.external_id OR NEW.group_id IS NOT OLD.group_id
  OR (NEW.status IS NOT OLD.status AND (NEW.status IN ('open', 'maybe_later')))
BEGIN
    UPDATE pellets SET implementation_revision = OLD.implementation_revision + 1
    WHERE project_id = NEW.project_id AND number = NEW.number;
END;
CREATE TRIGGER pellets_kind_immutable BEFORE UPDATE OF kind ON pellets
WHEN NEW.kind IS NOT OLD.kind BEGIN SELECT RAISE(ABORT, 'pellet kind is immutable'); END;

CREATE TABLE review_checkpoint_targets (
    project_id INTEGER NOT NULL,
    checkpoint_number INTEGER NOT NULL,
    target_number INTEGER NOT NULL CHECK (target_number > 0 AND target_number <> checkpoint_number),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    selected_reference TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL,
    external_id TEXT,
    group_id TEXT,
    purged_evidence_run_id INTEGER REFERENCES execution_runs(run_id),
    PRIMARY KEY (project_id, checkpoint_number, target_number),
    UNIQUE (project_id, checkpoint_number, ordinal),
    FOREIGN KEY (project_id, checkpoint_number) REFERENCES pellets(project_id, number) ON DELETE CASCADE
) STRICT;
-- No target FK: purging implementation rows must retain diagnosable scope.
CREATE TRIGGER review_checkpoint_target_kind BEFORE INSERT ON review_checkpoint_targets
WHEN NOT EXISTS (SELECT 1 FROM pellets WHERE project_id=NEW.project_id AND number=NEW.checkpoint_number AND kind='review_checkpoint')
  OR NOT EXISTS (SELECT 1 FROM pellets WHERE project_id=NEW.project_id AND number=NEW.target_number AND kind='ordinary')
BEGIN SELECT RAISE(ABORT, 'review requires ordinary targets in the same project'); END;
CREATE TRIGGER review_checkpoint_targets_immutable BEFORE UPDATE OF
    project_id, checkpoint_number, target_number, ordinal, selected_reference,
    title, description, external_id, group_id ON review_checkpoint_targets
BEGIN SELECT RAISE(ABORT, 'review target scope is immutable'); END;

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

-- Freeze the exact currently available receipt before deleting a target. Do
-- not infer an older generation's evidence after its live revision disappears.
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
