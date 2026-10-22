-- Keep historical foreign keys and paths while retiring a registration.
ALTER TABLE project_workspaces ADD COLUMN pruned INTEGER NOT NULL DEFAULT 0 CHECK(pruned IN (0,1));
