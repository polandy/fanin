-- Where a run's worker lives, so a restart observes it again instead of starting a second one (SCENARIOS SP-10).

ALTER TABLE runs ADD COLUMN host TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN agent TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN workspace TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN tab TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN pane TEXT NOT NULL DEFAULT '';
