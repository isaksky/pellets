-- The legacy group_id TEXT column is a synchronized name projection. Stable
-- membership lives in group_record_id; historical name captures stay untouched.
CREATE TABLE groups (
 group_id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
 name TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(name AS BLOB)) > 0),
 context TEXT NOT NULL DEFAULT '' CHECK(length(CAST(context AS BLOB)) <= 1048576),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
 created_at REAL NOT NULL,
 updated_at REAL NOT NULL CHECK(updated_at >= created_at),
 UNIQUE(project_id, name)
) STRICT;

INSERT INTO groups(project_id, name, created_at, updated_at)
SELECT project_id, name, MIN(created_at), MAX(updated_at) FROM (
 SELECT project_id, group_id AS name, created_at, updated_at FROM pellets WHERE group_id IS NOT NULL
 UNION ALL
 SELECT a.project_id, j.value AS name, julianday('now'), julianday('now')
 FROM workspace_group_assignments a, json_each(a.groups_json) j
) GROUP BY project_id, name COLLATE BINARY;

ALTER TABLE pellets ADD COLUMN group_record_id INTEGER REFERENCES groups(group_id) ON DELETE RESTRICT;
UPDATE pellets SET group_record_id=(SELECT g.group_id FROM groups g WHERE g.project_id=pellets.project_id AND g.name=pellets.group_id);
CREATE INDEX pellets_group_record_idx ON pellets(group_record_id);

-- All legacy name-based writers, including checkpoint triage and planning,
-- converge under their existing writer transaction. No synthetic NULL group.
CREATE TRIGGER pellets_group_insert AFTER INSERT ON pellets
BEGIN
 INSERT INTO groups(project_id,name,created_at,updated_at)
 SELECT NEW.project_id,NEW.group_id,NEW.created_at,NEW.updated_at WHERE NEW.group_id IS NOT NULL
 ON CONFLICT(project_id,name) DO NOTHING;
 UPDATE pellets SET group_record_id=(SELECT group_id FROM groups WHERE project_id=NEW.project_id AND name=NEW.group_id) WHERE rowid=NEW.rowid;
END;
CREATE TRIGGER pellets_group_name_update AFTER UPDATE OF group_id, project_id ON pellets
WHEN NEW.group_id IS NOT OLD.group_id OR NEW.project_id IS NOT OLD.project_id
BEGIN
 INSERT INTO groups(project_id,name,created_at,updated_at)
 SELECT NEW.project_id,NEW.group_id,NEW.updated_at,NEW.updated_at WHERE NEW.group_id IS NOT NULL
 ON CONFLICT(project_id,name) DO NOTHING;
 UPDATE pellets SET group_record_id=(SELECT group_id FROM groups WHERE project_id=NEW.project_id AND name=NEW.group_id) WHERE rowid=NEW.rowid;
END;
CREATE TRIGGER pellets_group_identity_update BEFORE UPDATE OF group_record_id ON pellets
WHEN (NEW.group_id IS NULL) <> (NEW.group_record_id IS NULL)
 OR (NEW.group_record_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM groups WHERE group_id=NEW.group_record_id AND project_id=NEW.project_id AND name=NEW.group_id))
BEGIN SELECT RAISE(ABORT, 'group membership must match its project and current name'); END;
CREATE TRIGGER groups_project_immutable BEFORE UPDATE OF group_id, project_id ON groups
WHEN NEW.group_id IS NOT OLD.group_id OR NEW.project_id IS NOT OLD.project_id
BEGIN SELECT RAISE(ABORT, 'group identity and project are immutable'); END;
CREATE TRIGGER groups_rename_members AFTER UPDATE OF name ON groups
WHEN NEW.name IS NOT OLD.name
BEGIN
 UPDATE pellets SET group_id=NEW.name, updated_at=MAX(updated_at,NEW.updated_at) WHERE group_record_id=NEW.group_id;
END;

CREATE TRIGGER workspace_groups_insert AFTER INSERT ON workspace_group_assignments
BEGIN
 INSERT INTO groups(project_id,name,created_at,updated_at)
 SELECT NEW.project_id,value,julianday('now'),julianday('now') FROM json_each(NEW.groups_json) WHERE 1
 ON CONFLICT(project_id,name) DO NOTHING;
END;
CREATE TRIGGER workspace_groups_update AFTER UPDATE OF groups_json ON workspace_group_assignments
BEGIN
 INSERT INTO groups(project_id,name,created_at,updated_at)
 SELECT NEW.project_id,value,julianday('now'),julianday('now') FROM json_each(NEW.groups_json) WHERE 1
 ON CONFLICT(project_id,name) DO NOTHING;
END;
