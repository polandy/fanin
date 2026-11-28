-- The task's file scope as a JSON array of repo-relative paths or globs; empty means no scope (see SCENARIOS SC-1).

ALTER TABLE tasks ADD COLUMN allowed_paths TEXT NOT NULL DEFAULT '[]';
