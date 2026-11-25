-- The worker kind the owner asked for on a task; empty means the project's (see SCENARIOS PK-7, SP-9).

ALTER TABLE tasks ADD COLUMN worker_kind TEXT NOT NULL DEFAULT '';
