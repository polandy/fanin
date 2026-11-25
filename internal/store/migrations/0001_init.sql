-- Times are Unix nanoseconds in UTC; 0 means unset.

CREATE TABLE tasks (
    id            TEXT    PRIMARY KEY,
    project       TEXT    NOT NULL,
    title         TEXT    NOT NULL,
    prompt        TEXT    NOT NULL,
    shape         TEXT    NOT NULL,
    state         TEXT    NOT NULL,
    owner_task    INTEGER NOT NULL,
    after_ids     TEXT    NOT NULL,
    owner_model   TEXT    NOT NULL,
    project_model TEXT    NOT NULL,
    picked_model  TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX tasks_project_state ON tasks (project, state);

CREATE TABLE runs (
    id            TEXT    PRIMARY KEY,
    task_id       TEXT    NOT NULL REFERENCES tasks (id),
    number        INTEGER NOT NULL,
    slot          INTEGER NOT NULL,
    branch        TEXT    NOT NULL,
    token         TEXT    NOT NULL,
    fix_rounds    INTEGER NOT NULL,
    review_nudges INTEGER NOT NULL,
    stalls        INTEGER NOT NULL,
    input_tokens  INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    total_tokens  INTEGER NOT NULL,
    state         TEXT    NOT NULL,
    result        TEXT    NOT NULL,
    started_at    INTEGER NOT NULL,
    ended_at      INTEGER NOT NULL,
    UNIQUE (task_id, number)
) STRICT;

CREATE TABLE events (
    seq     INTEGER PRIMARY KEY AUTOINCREMENT,
    type    TEXT    NOT NULL,
    project TEXT    NOT NULL,
    task_id TEXT    NOT NULL,
    run_id  TEXT    NOT NULL,
    at      INTEGER NOT NULL,
    detail  TEXT    NOT NULL
) STRICT;

CREATE INDEX events_task ON events (task_id, seq);
CREATE INDEX events_project ON events (project, seq);

CREATE TABLE project_locks (
    project     TEXT    PRIMARY KEY,
    holder      TEXT    NOT NULL,
    acquired_at INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE holds (
    id         TEXT    PRIMARY KEY,
    project    TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    reason     TEXT    NOT NULL,
    fix_task   TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    lifted_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX holds_project ON holds (project, lifted_at);

CREATE TABLE timers (
    id          TEXT    PRIMARY KEY,
    project     TEXT    NOT NULL,
    task_id     TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    due_at      INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    disarmed_at INTEGER NOT NULL
) STRICT;

CREATE INDEX timers_due ON timers (disarmed_at, due_at);

CREATE TABLE owner_questions (
    id          TEXT    PRIMARY KEY,
    task_id     TEXT    NOT NULL REFERENCES tasks (id),
    run_id      TEXT    NOT NULL,
    project     TEXT    NOT NULL,
    question    TEXT    NOT NULL,
    options     TEXT    NOT NULL,
    recommended INTEGER NOT NULL,
    answer      TEXT    NOT NULL,
    asked_at    INTEGER NOT NULL,
    answered_at INTEGER NOT NULL
) STRICT;
