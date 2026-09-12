-- Editing the current explicit scope creates a new lifecycle generation.
-- Superseded scope and materialized evidence remain immutable, in addition to
-- the exact checkpoint_scope_json already captured on every execution run.
CREATE TABLE review_checkpoint_scope_history (
    project_id INTEGER NOT NULL,
    checkpoint_number INTEGER NOT NULL,
    implementation_revision INTEGER NOT NULL CHECK (implementation_revision > 0),
    scope_json TEXT NOT NULL CHECK (json_valid(scope_json)),
    captured_at REAL NOT NULL,
    PRIMARY KEY (project_id, checkpoint_number, implementation_revision),
    FOREIGN KEY (project_id, checkpoint_number) REFERENCES pellets(project_id, number) ON DELETE CASCADE
) STRICT;
CREATE TRIGGER review_checkpoint_history_immutable BEFORE UPDATE ON review_checkpoint_scope_history
BEGIN SELECT RAISE(ABORT, 'historical review scope is immutable'); END;

-- Removal is a reversible defer, never completion or destructive purge.
-- Neighbor anchors make Undo preserve relative placement after a rebalance.
CREATE TABLE review_checkpoint_removals (
    project_id INTEGER NOT NULL,
    checkpoint_number INTEGER NOT NULL,
    previous_number INTEGER,
    next_number INTEGER,
    original_priority INTEGER NOT NULL,
    removed_at REAL NOT NULL,
    PRIMARY KEY (project_id, checkpoint_number),
    FOREIGN KEY (project_id, checkpoint_number) REFERENCES pellets(project_id, number) ON DELETE CASCADE
) STRICT;
CREATE TRIGGER review_checkpoint_removal_kind BEFORE INSERT ON review_checkpoint_removals
WHEN NOT EXISTS (SELECT 1 FROM pellets WHERE project_id=NEW.project_id AND number=NEW.checkpoint_number AND kind='review_checkpoint' AND status='open' AND workspace_id IS NULL)
BEGIN SELECT RAISE(ABORT, 'only open unowned checkpoints may be removed'); END;
-- A deliberate CLI reopen also restores visibility. Its existing tail
-- placement semantics remain unchanged; the browser Restore uses the anchors.
CREATE TRIGGER review_checkpoint_removal_restored AFTER UPDATE OF status ON pellets
WHEN NEW.status <> 'maybe_later'
BEGIN
    DELETE FROM review_checkpoint_removals WHERE project_id=NEW.project_id AND checkpoint_number=NEW.number;
END;
