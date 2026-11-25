-- The owner channel: how a question is answered, and the per-project pause flag (see SCENARIOS OA-1, FP-2).

ALTER TABLE owner_questions ADD COLUMN kind TEXT NOT NULL DEFAULT 'transcript';
ALTER TABLE owner_questions ADD COLUMN allow_text INTEGER NOT NULL DEFAULT 0;

CREATE INDEX owner_questions_task ON owner_questions (task_id, answered_at);

CREATE TABLE project_pauses (
    project      TEXT    PRIMARY KEY,
    requested_at INTEGER NOT NULL
) STRICT;
