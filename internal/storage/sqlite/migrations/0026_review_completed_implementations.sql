-- Review completed implementations, whose requirements were captured by the run.
-- Later task edits do not invalidate the code or require a new scope generation.
DROP VIEW review_checkpoint_readiness;
CREATE VIEW review_checkpoint_readiness AS
SELECT t.*, target.status AS target_status, evidence.implementation_revision,
    evidence.pellet_title AS evidence_title, evidence.pellet_description AS evidence_description,
    CASE WHEN target.number IS NULL THEN 'target_missing'
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
      AND r.mode <> 'review_checkpoint' AND r.state='completed' AND r.outcome='succeeded'
      AND r.pending_operation='' AND r.result_commit<>'' AND (r.result_commit<>r.starting_head OR json_extract(r.finalization_json,'$.no_changes')=1)
      AND r.commit_verified_at IS NOT NULL AND r.thread_id<>'' AND r.turn_id<>''
      AND r.finalization_json<>'null'
    ORDER BY r.run_id DESC LIMIT 1
) END;
