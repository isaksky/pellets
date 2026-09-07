-- Short-lived retry receipts, independent of pellet lifecycle and purge.
CREATE TABLE pellet_add_requests (
    project_id INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    request_id TEXT NOT NULL CHECK (length(request_id) > 0),
    fingerprint TEXT NOT NULL,
    result_json TEXT NOT NULL,
    created_at REAL NOT NULL,
    PRIMARY KEY (project_id, request_id)
) STRICT;

CREATE INDEX pellet_add_requests_created_idx ON pellet_add_requests(created_at);
