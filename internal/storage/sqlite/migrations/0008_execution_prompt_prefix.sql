-- Immutable prompt provenance for each Codex attempt. The retained snapshot is
-- the small Pellets workflow layer only; Codex system/user configuration and
-- transcript content remain outside this database.
ALTER TABLE execution_runs ADD COLUMN prompt_prefix_json TEXT NOT NULL DEFAULT '{"template_version":"legacy"}'
    CHECK (json_valid(prompt_prefix_json) AND length(CAST(prompt_prefix_json AS BLOB)) <= 2097152);
ALTER TABLE execution_runs ADD COLUMN cached_input_tokens INTEGER
    CHECK (cached_input_tokens IS NULL OR cached_input_tokens >= 0);
