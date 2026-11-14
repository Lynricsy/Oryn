-- +goose Up
-- M9: spaces, global assistant conversations, schedules (thread / new_thread), coordinator audit log.
-- Coordinator conversations are rows in threads (kind = space | assistant); the coordinator orb is a
-- normal orbs row with coordinator = 1 on the built-in "@host" provider.

ALTER TABLE threads ADD COLUMN kind TEXT NOT NULL DEFAULT 'work' CHECK (kind IN ('work', 'space', 'assistant'));
ALTER TABLE threads ADD COLUMN space_id TEXT;          -- no FK: cleared by code when a space is deleted
ALTER TABLE threads ADD COLUMN shipped_at INTEGER;
ALTER TABLE threads ADD COLUMN schedule_id TEXT;       -- no FK: kept after the schedule is deleted
ALTER TABLE threads ADD COLUMN schedule_run INTEGER;   -- run number of the new_thread schedule that created it
CREATE INDEX threads_space_id ON threads (space_id) WHERE space_id IS NOT NULL;
CREATE INDEX threads_schedule_id ON threads (schedule_id, created_at) WHERE schedule_id IS NOT NULL;
ALTER TABLE orbs ADD COLUMN coordinator BOOLEAN NOT NULL DEFAULT 0;

CREATE TABLE spaces (
    id          TEXT    PRIMARY KEY,                  -- spc_<ULID>
    name        TEXT    NOT NULL UNIQUE,
    goal        TEXT    NOT NULL DEFAULT '',
    project_id  TEXT    REFERENCES projects (id) ON DELETE SET NULL,
    thread_id   TEXT    NOT NULL UNIQUE REFERENCES threads (id) ON DELETE CASCADE,  -- the space chat
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    archived_at INTEGER
);

CREATE TABLE schedules (
    id             TEXT    PRIMARY KEY,               -- sch_<ULID>
    mode           TEXT    NOT NULL CHECK (mode IN ('thread', 'new_thread')),
    thread_id      TEXT    REFERENCES threads (id) ON DELETE CASCADE,    -- mode = thread
    project_id     TEXT    REFERENCES projects (id) ON DELETE CASCADE,   -- mode = new_thread; NULL = no project
    space_id       TEXT,                                                 -- mode = new_thread; no FK
    name           TEXT    NOT NULL DEFAULT '',
    kind           TEXT    NOT NULL CHECK (kind IN ('cron', 'every', 'once')),
    spec           TEXT    NOT NULL DEFAULT '',       -- cron: expression; every: normalized duration (30m0s); once: ''
    timezone       TEXT    NOT NULL,
    run_at         INTEGER,                           -- once
    ends_at        INTEGER,
    prompt         TEXT    NOT NULL,
    enabled        BOOLEAN NOT NULL DEFAULT 1,
    paused_reason  TEXT    CHECK (paused_reason IN ('user', 'failures', 'archived', 'done')),
    next_run_at    INTEGER,                           -- NULL = will not fire again
    last_run_at    INTEGER,
    run_count      INTEGER NOT NULL DEFAULT 0,
    failures       INTEGER NOT NULL DEFAULT 0,        -- consecutive failures
    created_by     TEXT    NOT NULL CHECK (created_by IN ('user', 'agent')),
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    model          TEXT,                              -- "provider/id"
    thinking_level TEXT,
    provider       TEXT,
    orb_type       TEXT,
    overlap        TEXT    NOT NULL DEFAULT 'skip' CHECK (overlap IN ('skip', 'allow')),
    keep_last      INTEGER NOT NULL DEFAULT 5,
    CHECK ((mode = 'thread') = (thread_id IS NOT NULL)),
    CHECK (mode = 'new_thread' OR (project_id IS NULL AND space_id IS NULL))
);
CREATE UNIQUE INDEX schedules_thread ON schedules (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX schedules_project ON schedules (project_id) WHERE mode = 'new_thread';
CREATE INDEX schedules_due ON schedules (next_run_at) WHERE enabled = 1 AND next_run_at IS NOT NULL;

CREATE TABLE schedule_runs (
    id            TEXT    PRIMARY KEY,                -- srn_<ULID>
    schedule_id   TEXT    NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    thread_id     TEXT,                               -- thread mode: the target; new_thread mode: the created thread
    trigger       TEXT    NOT NULL CHECK (trigger IN ('schedule', 'catchup', 'manual')),
    scheduled_for INTEGER NOT NULL,
    missed        INTEGER NOT NULL DEFAULT 0,         -- coalesced triggers
    started_at    INTEGER NOT NULL,
    delivered_at  INTEGER,
    finished_at   INTEGER,
    status        TEXT    NOT NULL CHECK (status IN ('pending', 'running', 'needs_input', 'completed', 'failed', 'skipped')),
    error         TEXT
);
CREATE INDEX schedule_runs_schedule ON schedule_runs (schedule_id, started_at DESC);
CREATE INDEX schedule_runs_open ON schedule_runs (schedule_id) WHERE status IN ('pending', 'running', 'needs_input');

-- Reports a coordinator waits for: delivered once to report_to when thread_id settles.
CREATE TABLE thread_reports (
    thread_id    TEXT    PRIMARY KEY REFERENCES threads (id) ON DELETE CASCADE,
    report_to    TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    requested_at INTEGER NOT NULL
);

CREATE TABLE coord_actions (                          -- audit log; no FKs so rows outlive threads
    id               TEXT    PRIMARY KEY,             -- cac_<ULID>
    ts               INTEGER NOT NULL,
    actor_kind       TEXT    NOT NULL CHECK (actor_kind IN ('space', 'assistant', 'agent')),
    actor_thread_id  TEXT,
    origin           TEXT    NOT NULL CHECK (origin IN ('owner', 'auto')),
    action           TEXT    NOT NULL,
    target_thread_id TEXT,
    schedule_id      TEXT,
    summary          TEXT    NOT NULL DEFAULT '',
    ok               BOOLEAN NOT NULL,
    error            TEXT
);
CREATE INDEX coord_actions_ts ON coord_actions (ts DESC);
CREATE INDEX coord_actions_target ON coord_actions (target_thread_id, ts DESC);
CREATE INDEX coord_actions_actor ON coord_actions (actor_thread_id, ts DESC);

-- +goose Down
DROP INDEX coord_actions_actor;
DROP INDEX coord_actions_target;
DROP INDEX coord_actions_ts;
DROP TABLE coord_actions;
DROP TABLE thread_reports;
DROP INDEX schedule_runs_open;
DROP INDEX schedule_runs_schedule;
DROP TABLE schedule_runs;
DROP INDEX schedules_due;
DROP INDEX schedules_project;
DROP INDEX schedules_thread;
DROP TABLE schedules;
DROP TABLE spaces;
DROP INDEX threads_schedule_id;
DROP INDEX threads_space_id;
ALTER TABLE orbs DROP COLUMN coordinator;
ALTER TABLE threads DROP COLUMN schedule_run;
ALTER TABLE threads DROP COLUMN schedule_id;
ALTER TABLE threads DROP COLUMN shipped_at;
ALTER TABLE threads DROP COLUMN space_id;
ALTER TABLE threads DROP COLUMN kind;
