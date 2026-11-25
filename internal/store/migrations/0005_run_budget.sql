-- The silent-stop ladder, the context share, the run's hint and its last pane tail (concept C-BUDGET, SCENARIOS AW-11, AW-12).

ALTER TABLE runs ADD COLUMN context_pct INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN footer_waits INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN continue_nudges INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN secure_nudges INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN hint TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN tail TEXT NOT NULL DEFAULT '';
