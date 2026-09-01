-- Preserve former public project codes as direct aliases of the stable
-- projects.project_id row. Cross-table triggers make projects.code and
-- project_code_redirects.code one reserved namespace for every writer.

CREATE TABLE project_code_redirects (
    code       TEXT PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,

    CHECK (length(code) BETWEEN 1 AND 12),
    CHECK (code = lower(code)),
    CHECK (code NOT GLOB '*[^a-z0-9-]*'),
    CHECK (substr(code, 1, 1) <> '-'),
    CHECK (substr(code, -1, 1) <> '-'),
    CHECK (updated_at >= created_at)
) STRICT;

CREATE INDEX project_code_redirects_project_idx
    ON project_code_redirects(project_id, code);

CREATE TRIGGER projects_code_redirect_namespace_insert
BEFORE INSERT ON projects
WHEN EXISTS (
    SELECT 1 FROM project_code_redirects AS redirect
    WHERE redirect.code = NEW.code
)
BEGIN
    SELECT RAISE(ABORT, 'project code is reserved by a redirect');
END;

CREATE TRIGGER projects_code_redirect_namespace_update
BEFORE UPDATE OF code ON projects
WHEN NEW.code <> OLD.code AND EXISTS (
    SELECT 1 FROM project_code_redirects AS redirect
    WHERE redirect.code = NEW.code
)
BEGIN
    SELECT RAISE(ABORT, 'project code is reserved by a redirect');
END;

CREATE TRIGGER project_code_redirects_canonical_namespace_insert
BEFORE INSERT ON project_code_redirects
WHEN EXISTS (
    SELECT 1 FROM projects AS project
    WHERE project.code = NEW.code
)
BEGIN
    SELECT RAISE(ABORT, 'redirect code is reserved by a canonical project');
END;

CREATE TRIGGER project_code_redirects_canonical_namespace_update
BEFORE UPDATE OF code ON project_code_redirects
WHEN NEW.code <> OLD.code AND EXISTS (
    SELECT 1 FROM projects AS project
    WHERE project.code = NEW.code
)
BEGIN
    SELECT RAISE(ABORT, 'redirect code is reserved by a canonical project');
END;
