-- The context tokens of a run's last counted status-line update, so a repeat is not counted twice (SCENARIOS AW-14).

ALTER TABLE runs ADD COLUMN seen_input INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN seen_output INTEGER NOT NULL DEFAULT 0;
