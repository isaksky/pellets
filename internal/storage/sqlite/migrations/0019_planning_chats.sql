-- Planning proposals have their own project-bound state, never queue ownership
-- or execution process identity. Reads do not create chats or run Codex.
CREATE TABLE planning_chats (
 chat_id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
 request_id TEXT NOT NULL CHECK(length(request_id) BETWEEN 1 AND 128),
 request_fingerprint TEXT NOT NULL CHECK(length(request_fingerprint)=64),
 revision INTEGER NOT NULL CHECK(revision > 0),
 state_json TEXT NOT NULL CHECK(json_valid(state_json) AND json_type(state_json)='object' AND length(CAST(state_json AS BLOB))<=524288),
 created_at REAL NOT NULL,
 updated_at REAL NOT NULL CHECK(updated_at>=created_at),
 UNIQUE(project_id,request_id),
 UNIQUE(chat_id,project_id)
) STRICT;
CREATE INDEX planning_chats_project_recent_idx ON planning_chats(project_id,chat_id DESC);

-- Receipts survive pellet purge and remain authoritative even if a stale
-- browser tries to clear a draft's created reference. No pellet FK by design.
CREATE TABLE planning_draft_creations (
 chat_id INTEGER NOT NULL,
 project_id INTEGER NOT NULL,
 draft_id TEXT NOT NULL CHECK(length(draft_id) BETWEEN 1 AND 128),
 pellet_number INTEGER NOT NULL CHECK(pellet_number>0),
 draft_json TEXT NOT NULL CHECK(json_valid(draft_json) AND json_type(draft_json)='object' AND length(CAST(draft_json AS BLOB))<=524288),
 pellet_json TEXT NOT NULL CHECK(json_valid(pellet_json) AND json_type(pellet_json)='object' AND length(CAST(pellet_json AS BLOB))<=1048576),
 PRIMARY KEY(chat_id,draft_id),
 UNIQUE(project_id,pellet_number),
 FOREIGN KEY(chat_id,project_id) REFERENCES planning_chats(chat_id,project_id) ON DELETE RESTRICT
) STRICT;
CREATE TRIGGER planning_draft_creation_immutable BEFORE UPDATE ON planning_draft_creations
BEGIN SELECT RAISE(ABORT,'planning creation receipts are immutable'); END;
