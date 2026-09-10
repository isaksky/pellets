-- Permanent reconciliation evidence, independent of the two-day add cache.
CREATE TABLE checkpoint_triage (
 project_id INTEGER NOT NULL REFERENCES projects(project_id) ON DELETE RESTRICT,
 checkpoint_number INTEGER NOT NULL CHECK(checkpoint_number>0),
 implementation_revision INTEGER NOT NULL CHECK(implementation_revision>0),
 review_result_json TEXT NOT NULL CHECK(json_valid(review_result_json) AND length(CAST(review_result_json AS BLOB))<=1048576),
 review_thread_id TEXT NOT NULL CHECK(length(review_thread_id) BETWEEN 1 AND 256),
 review_turn_id TEXT NOT NULL CHECK(length(review_turn_id) BETWEEN 1 AND 256),
 PRIMARY KEY(project_id, checkpoint_number, implementation_revision)
) STRICT;
CREATE TABLE checkpoint_finding_assessments (
 project_id INTEGER NOT NULL,
 checkpoint_number INTEGER NOT NULL,
 implementation_revision INTEGER NOT NULL,
 finding_id TEXT NOT NULL CHECK(length(finding_id)=64 AND finding_id NOT GLOB '*[^0-9a-f]*'),
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 assessment_json TEXT NOT NULL CHECK(json_valid(assessment_json) AND length(CAST(assessment_json AS BLOB))<=1048576),
 PRIMARY KEY(project_id, checkpoint_number, implementation_revision, finding_id),
 UNIQUE(project_id, checkpoint_number, implementation_revision, ordinal),
 FOREIGN KEY(project_id, checkpoint_number, implementation_revision) REFERENCES checkpoint_triage(project_id, checkpoint_number, implementation_revision) ON DELETE RESTRICT
) STRICT;
