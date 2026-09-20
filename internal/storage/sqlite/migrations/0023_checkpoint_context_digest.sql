-- Old triage predates implementation-context capture. New receipts bind the
-- complete review snapshot, including target/run/context associations.
ALTER TABLE checkpoint_triage ADD COLUMN review_snapshot_sha256 TEXT NOT NULL DEFAULT ''
 CHECK (review_snapshot_sha256='' OR (length(review_snapshot_sha256)=64 AND review_snapshot_sha256 NOT GLOB '*[^0-9a-f]*'));

CREATE TRIGGER checkpoint_triage_snapshot_immutable BEFORE UPDATE OF review_snapshot_sha256 ON checkpoint_triage
WHEN NEW.review_snapshot_sha256 IS NOT OLD.review_snapshot_sha256
BEGIN SELECT RAISE(ABORT, 'triage review snapshot digest is immutable'); END;
