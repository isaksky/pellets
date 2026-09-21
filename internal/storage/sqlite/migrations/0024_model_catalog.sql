CREATE TABLE model_catalog (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 version INTEGER NOT NULL CHECK (version = 1),
 models TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(models)),
 fetched_at INTEGER NOT NULL DEFAULT 0,
 claim_token TEXT NOT NULL DEFAULT '',
 claim_until INTEGER NOT NULL DEFAULT 0,
 retry_after INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT ''
) STRICT;
INSERT INTO model_catalog(id, version) VALUES(1, 1);
