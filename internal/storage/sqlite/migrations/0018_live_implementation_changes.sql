-- A bounded-per-edit delivery journal for edits to an owned, executing pellet.
-- Lifecycle transitions deliberately leave gaps: they can never be adopted as edits.
CREATE TABLE execution_changes (
 run_id INTEGER NOT NULL REFERENCES execution_runs(run_id) ON DELETE RESTRICT,
 from_revision INTEGER NOT NULL,
 to_revision INTEGER NOT NULL CHECK(to_revision=from_revision+1),
 old_json TEXT NOT NULL CHECK(json_valid(old_json)),
 new_json TEXT NOT NULL CHECK(json_valid(new_json)),
 assessment_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(assessment_json)),
 delivered INTEGER NOT NULL DEFAULT 0 CHECK(delivered IN (0,1)),
 PRIMARY KEY(run_id,from_revision)
) STRICT;

DROP TRIGGER pellets_implementation_revision;
CREATE TRIGGER pellets_implementation_revision AFTER UPDATE OF title, description, external_id, group_id, status ON pellets
WHEN NEW.title IS NOT OLD.title OR NEW.description IS NOT OLD.description
 OR NEW.external_id IS NOT OLD.external_id OR NEW.group_id IS NOT OLD.group_id
 OR (NEW.status IS NOT OLD.status AND NEW.status IN ('open','maybe_later'))
BEGIN
 INSERT INTO execution_changes(run_id,from_revision,to_revision,old_json,new_json)
 SELECT r.run_id,OLD.implementation_revision,OLD.implementation_revision+1,
 json_object('title',OLD.title,'description',OLD.description,'group',OLD.group_id,'external_id',OLD.external_id),
 json_object('title',NEW.title,'description',NEW.description,'group',NEW.group_id,'external_id',NEW.external_id)
 FROM execution_runs r
 WHERE r.project_id=OLD.project_id AND r.pellet_number=OLD.number
 AND r.workspace_id=OLD.workspace_id AND NEW.workspace_id IS OLD.workspace_id
 AND OLD.kind='ordinary' AND OLD.status='in_progress' AND NEW.status='in_progress'
 AND r.state IN ('running','awaiting_input') AND r.mode<>'review_checkpoint'
 AND r.finalization_json='null';
 UPDATE pellets SET implementation_revision=OLD.implementation_revision+1
 WHERE project_id=NEW.project_id AND number=NEW.number;
END;
