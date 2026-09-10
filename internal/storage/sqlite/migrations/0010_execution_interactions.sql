-- One bounded pending app-server interaction per durable run. Responses and
-- credentials are deliberately never stored.
ALTER TABLE execution_runs ADD COLUMN interaction_json TEXT NOT NULL DEFAULT 'null'
CHECK (json_valid(interaction_json) AND length(CAST(interaction_json AS BLOB)) <= 262144);
