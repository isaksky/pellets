-- Never backfill historical context from today's membership or document.
ALTER TABLE execution_runs ADD COLUMN group_context_json TEXT NOT NULL
 DEFAULT '{"version":1,"state":"legacy","group":null}'
 CHECK (json_valid(group_context_json) AND length(CAST(group_context_json AS BLOB)) <= 6324224);

CREATE TRIGGER execution_group_context_immutable BEFORE UPDATE OF group_context_json ON execution_runs
WHEN NEW.group_context_json IS NOT OLD.group_context_json
BEGIN SELECT RAISE(ABORT, 'captured execution group context is immutable'); END;
