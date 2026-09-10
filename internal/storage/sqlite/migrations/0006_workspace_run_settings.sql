-- Save only credential-free Codex run preferences for a registered worktree.
-- Runtime credentials, account details, and effective configuration remain
-- exclusively owned by Codex and are deliberately absent from this table.

CREATE TABLE workspace_run_settings (
    workspace_id         INTEGER PRIMARY KEY
                         REFERENCES project_workspaces(workspace_id) ON DELETE CASCADE,
    codex_executable     TEXT NOT NULL DEFAULT '',
    model                TEXT NOT NULL DEFAULT '',
    reasoning_effort     TEXT NOT NULL DEFAULT '',
    max_message_bytes    INTEGER NOT NULL DEFAULT 0,
    event_buffer         INTEGER NOT NULL DEFAULT 0,
    max_pending          INTEGER NOT NULL DEFAULT 0,
    stderr_bytes         INTEGER NOT NULL DEFAULT 0,
    revision             INTEGER NOT NULL DEFAULT 1,
    created_at           REAL NOT NULL,
    updated_at           REAL NOT NULL,

    CHECK (codex_executable = '' OR (trim(codex_executable) = codex_executable AND length(codex_executable) <= 4096)),
    CHECK (model = '' OR (trim(model) = model AND length(model) <= 512)),
    CHECK (reasoning_effort = '' OR (trim(reasoning_effort) = reasoning_effort AND length(reasoning_effort) <= 128)),
    CHECK (max_message_bytes = 0 OR max_message_bytes BETWEEN 256 AND 67108864),
    CHECK (event_buffer BETWEEN 0 AND 65536),
    CHECK (max_pending BETWEEN 0 AND 4096),
    CHECK (stderr_bytes BETWEEN 0 AND 1048576),
    CHECK (revision > 0),
    CHECK (updated_at >= created_at)
) STRICT;
