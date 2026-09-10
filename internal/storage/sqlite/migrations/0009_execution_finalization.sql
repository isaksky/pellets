ALTER TABLE execution_runs ADD COLUMN finalization_json TEXT NOT NULL DEFAULT 'null'
CHECK (json_valid(finalization_json) AND length(CAST(finalization_json AS BLOB)) <= 1048576);
