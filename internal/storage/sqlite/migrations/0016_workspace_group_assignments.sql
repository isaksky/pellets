-- Routing is separate from ownership and from optional exact CLI filters.
CREATE TABLE project_group_routing (
 project_id INTEGER PRIMARY KEY REFERENCES projects(project_id) ON DELETE RESTRICT,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0)
) STRICT;
CREATE TABLE workspace_group_assignments (
 workspace_id INTEGER PRIMARY KEY,
 project_id INTEGER NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('explicit','remaining')),
 include_ungrouped INTEGER NOT NULL CHECK(include_ungrouped IN (0,1)),
 groups_json TEXT NOT NULL CHECK(json_valid(groups_json) AND json_type(groups_json)='array' AND length(groups_json)<=65536),
 FOREIGN KEY(project_id, workspace_id) REFERENCES project_workspaces(project_id, workspace_id) ON DELETE RESTRICT
) STRICT;
ALTER TABLE execution_runs ADD COLUMN workspace_selection_json TEXT NOT NULL DEFAULT 'null'
 CHECK(json_valid(workspace_selection_json) AND length(workspace_selection_json)<=65536);
