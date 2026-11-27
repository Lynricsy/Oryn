-- +goose Up
-- M31：定时的入站 Webhook 触发。
-- 1. schedules.kind 增加 manual（没有时间触发，只由手动或 Webhook 触发；仅 new_thread）。
-- 2. schedule_runs.trigger 增加 webhook；新增 payload 列（Webhook 运行收到的请求，JSON；其余运行为 NULL）。
-- 3. schedule_webhooks：每个定时至多一个入站地址。地址里的密钥只存 SHA-256；签名密钥用密钥盒封存。
-- SQLite 改不了 CHECK，只能重建。库以 foreign_keys = ON 打开且迁移在事务内，照 0055 把外键子链
-- （schedule_runs → schedules）先搬进无约束暂存表，由子到父删除、由父到子重建，再搬回，避免 CASCADE 误删数据。
-- schedules 的列顺序保持不变（0016 的 rev、0041 的 hand_to_assistant、0044 的 role、0053 的闸门三列仍在末尾），
-- sqlc 生成物不受影响；schedule_runs 的 payload 追加在末尾。

CREATE TABLE schedule_runs_keep (
    id TEXT, schedule_id TEXT, thread_id TEXT, trigger TEXT, scheduled_for INTEGER, missed INTEGER, started_at INTEGER,
    delivered_at INTEGER, finished_at INTEGER, status TEXT, error TEXT
);
INSERT INTO schedule_runs_keep (id, schedule_id, thread_id, trigger, scheduled_for, missed, started_at, delivered_at,
                                finished_at, status, error)
SELECT id, schedule_id, thread_id, trigger, scheduled_for, missed, started_at, delivered_at, finished_at, status, error
FROM schedule_runs;

CREATE TABLE schedules_keep (
    id TEXT, mode TEXT, thread_id TEXT, project_id TEXT, space_id TEXT, name TEXT, kind TEXT, spec TEXT, timezone TEXT,
    run_at INTEGER, ends_at INTEGER, prompt TEXT, enabled BOOLEAN, paused_reason TEXT, next_run_at INTEGER,
    last_run_at INTEGER, run_count INTEGER, failures INTEGER, created_by TEXT, created_at INTEGER, updated_at INTEGER,
    model TEXT, thinking_level TEXT, provider TEXT, orb_type TEXT, overlap TEXT, keep_last INTEGER, rev INTEGER,
    hand_to_assistant INTEGER, role TEXT, gate_host TEXT, gate_command TEXT, gate_timeout INTEGER
);
INSERT INTO schedules_keep (id, mode, thread_id, project_id, space_id, name, kind, spec, timezone, run_at, ends_at, prompt,
                            enabled, paused_reason, next_run_at, last_run_at, run_count, failures, created_by, created_at,
                            updated_at, model, thinking_level, provider, orb_type, overlap, keep_last, rev,
                            hand_to_assistant, role, gate_host, gate_command, gate_timeout)
SELECT id, mode, thread_id, project_id, space_id, name, kind, spec, timezone, run_at, ends_at, prompt, enabled,
       paused_reason, next_run_at, last_run_at, run_count, failures, created_by, created_at, updated_at, model,
       thinking_level, provider, orb_type, overlap, keep_last, rev, hand_to_assistant, role, gate_host, gate_command,
       gate_timeout
FROM schedules;

DROP INDEX schedule_runs_open;
DROP INDEX schedule_runs_schedule;
DROP TABLE schedule_runs;
DROP INDEX schedules_due;
DROP INDEX schedules_project;
DROP INDEX schedules_thread;
DROP TABLE schedules;

CREATE TABLE schedules (
    id                TEXT    PRIMARY KEY,
    mode              TEXT    NOT NULL CHECK (mode IN ('thread', 'new_thread')),
    thread_id         TEXT    REFERENCES threads (id) ON DELETE CASCADE,
    project_id        TEXT    REFERENCES projects (id) ON DELETE CASCADE,
    space_id          TEXT,
    name              TEXT    NOT NULL DEFAULT '',
    kind              TEXT    NOT NULL CHECK (kind IN ('cron', 'every', 'once', 'manual')),
    spec              TEXT    NOT NULL DEFAULT '',
    timezone          TEXT    NOT NULL,
    run_at            INTEGER,
    ends_at           INTEGER,
    prompt            TEXT    NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT 1,
    paused_reason     TEXT    CHECK (paused_reason IN ('user', 'failures', 'archived', 'done')),
    next_run_at       INTEGER,
    last_run_at       INTEGER,
    run_count         INTEGER NOT NULL DEFAULT 0,
    failures          INTEGER NOT NULL DEFAULT 0,
    created_by        TEXT    NOT NULL CHECK (created_by IN ('user', 'agent')),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    model             TEXT,
    thinking_level    TEXT,
    provider          TEXT,
    orb_type          TEXT,
    overlap           TEXT    NOT NULL DEFAULT 'skip' CHECK (overlap IN ('skip', 'allow')),
    keep_last         INTEGER NOT NULL DEFAULT 5,
    rev               INTEGER NOT NULL DEFAULT 1,
    hand_to_assistant INTEGER NOT NULL DEFAULT 0,
    role              TEXT,
    gate_host         TEXT,
    gate_command      TEXT,
    gate_timeout      INTEGER,
    CHECK ((mode = 'thread') = (thread_id IS NOT NULL)),
    CHECK (mode = 'new_thread' OR (project_id IS NULL AND space_id IS NULL)),
    CHECK (kind <> 'manual' OR mode = 'new_thread')
);
INSERT INTO schedules SELECT * FROM schedules_keep;
CREATE INDEX schedules_thread ON schedules (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX schedules_project ON schedules (project_id) WHERE mode = 'new_thread';
CREATE INDEX schedules_due ON schedules (next_run_at) WHERE enabled = 1 AND next_run_at IS NOT NULL;

CREATE TABLE schedule_runs (
    id            TEXT    PRIMARY KEY,
    schedule_id   TEXT    NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    thread_id     TEXT,
    trigger       TEXT    NOT NULL CHECK (trigger IN ('schedule', 'catchup', 'manual', 'webhook')),
    scheduled_for INTEGER NOT NULL,
    missed        INTEGER NOT NULL DEFAULT 0,
    started_at    INTEGER NOT NULL,
    delivered_at  INTEGER,
    finished_at   INTEGER,
    status        TEXT    NOT NULL CHECK (status IN ('pending', 'running', 'needs_input', 'completed', 'failed', 'skipped')),
    error         TEXT,
    payload       TEXT                                -- trigger = webhook：收到的请求（JSON），其余为 NULL
);
INSERT INTO schedule_runs (id, schedule_id, thread_id, trigger, scheduled_for, missed, started_at, delivered_at,
                           finished_at, status, error)
SELECT id, schedule_id, thread_id, trigger, scheduled_for, missed, started_at, delivered_at, finished_at, status, error
FROM schedule_runs_keep;
CREATE INDEX schedule_runs_schedule ON schedule_runs (schedule_id, started_at DESC);
CREATE INDEX schedule_runs_open ON schedule_runs (schedule_id) WHERE status IN ('pending', 'running', 'needs_input');

DROP TABLE schedule_runs_keep;
DROP TABLE schedules_keep;

CREATE TABLE schedule_webhooks (
    schedule_id    TEXT    PRIMARY KEY REFERENCES schedules (id) ON DELETE CASCADE,
    key_hash       TEXT    NOT NULL UNIQUE,            -- 地址里密钥的 SHA-256（十六进制）
    secret_enc     BLOB,                               -- 密钥盒封存的签名密钥；NULL = 不要求签名
    created_at     INTEGER NOT NULL,
    rotated_at     INTEGER NOT NULL,                   -- 最近一次生成地址密钥的时刻
    last_called_at INTEGER                             -- 最近一次通过校验的调用
);

-- +goose Down
DROP TABLE schedule_webhooks;
DELETE FROM schedules WHERE kind = 'manual';

CREATE TABLE schedule_runs_keep (
    id TEXT, schedule_id TEXT, thread_id TEXT, trigger TEXT, scheduled_for INTEGER, missed INTEGER, started_at INTEGER,
    delivered_at INTEGER, finished_at INTEGER, status TEXT, error TEXT
);
INSERT INTO schedule_runs_keep (id, schedule_id, thread_id, trigger, scheduled_for, missed, started_at, delivered_at,
                                finished_at, status, error)
SELECT id, schedule_id, thread_id, trigger, scheduled_for, missed, started_at, delivered_at, finished_at, status, error
FROM schedule_runs WHERE trigger <> 'webhook';

CREATE TABLE schedules_keep (
    id TEXT, mode TEXT, thread_id TEXT, project_id TEXT, space_id TEXT, name TEXT, kind TEXT, spec TEXT, timezone TEXT,
    run_at INTEGER, ends_at INTEGER, prompt TEXT, enabled BOOLEAN, paused_reason TEXT, next_run_at INTEGER,
    last_run_at INTEGER, run_count INTEGER, failures INTEGER, created_by TEXT, created_at INTEGER, updated_at INTEGER,
    model TEXT, thinking_level TEXT, provider TEXT, orb_type TEXT, overlap TEXT, keep_last INTEGER, rev INTEGER,
    hand_to_assistant INTEGER, role TEXT, gate_host TEXT, gate_command TEXT, gate_timeout INTEGER
);
INSERT INTO schedules_keep SELECT * FROM schedules;

DROP INDEX schedule_runs_open;
DROP INDEX schedule_runs_schedule;
DROP TABLE schedule_runs;
DROP INDEX schedules_due;
DROP INDEX schedules_project;
DROP INDEX schedules_thread;
DROP TABLE schedules;

CREATE TABLE schedules (
    id                TEXT    PRIMARY KEY,
    mode              TEXT    NOT NULL CHECK (mode IN ('thread', 'new_thread')),
    thread_id         TEXT    REFERENCES threads (id) ON DELETE CASCADE,
    project_id        TEXT    REFERENCES projects (id) ON DELETE CASCADE,
    space_id          TEXT,
    name              TEXT    NOT NULL DEFAULT '',
    kind              TEXT    NOT NULL CHECK (kind IN ('cron', 'every', 'once')),
    spec              TEXT    NOT NULL DEFAULT '',
    timezone          TEXT    NOT NULL,
    run_at            INTEGER,
    ends_at           INTEGER,
    prompt            TEXT    NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT 1,
    paused_reason     TEXT    CHECK (paused_reason IN ('user', 'failures', 'archived', 'done')),
    next_run_at       INTEGER,
    last_run_at       INTEGER,
    run_count         INTEGER NOT NULL DEFAULT 0,
    failures          INTEGER NOT NULL DEFAULT 0,
    created_by        TEXT    NOT NULL CHECK (created_by IN ('user', 'agent')),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    model             TEXT,
    thinking_level    TEXT,
    provider          TEXT,
    orb_type          TEXT,
    overlap           TEXT    NOT NULL DEFAULT 'skip' CHECK (overlap IN ('skip', 'allow')),
    keep_last         INTEGER NOT NULL DEFAULT 5,
    rev               INTEGER NOT NULL DEFAULT 1,
    hand_to_assistant INTEGER NOT NULL DEFAULT 0,
    role              TEXT,
    gate_host         TEXT,
    gate_command      TEXT,
    gate_timeout      INTEGER,
    CHECK ((mode = 'thread') = (thread_id IS NOT NULL)),
    CHECK (mode = 'new_thread' OR (project_id IS NULL AND space_id IS NULL))
);
INSERT INTO schedules SELECT * FROM schedules_keep;
CREATE INDEX schedules_thread ON schedules (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX schedules_project ON schedules (project_id) WHERE mode = 'new_thread';
CREATE INDEX schedules_due ON schedules (next_run_at) WHERE enabled = 1 AND next_run_at IS NOT NULL;

CREATE TABLE schedule_runs (
    id            TEXT    PRIMARY KEY,
    schedule_id   TEXT    NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    thread_id     TEXT,
    trigger       TEXT    NOT NULL CHECK (trigger IN ('schedule', 'catchup', 'manual')),
    scheduled_for INTEGER NOT NULL,
    missed        INTEGER NOT NULL DEFAULT 0,
    started_at    INTEGER NOT NULL,
    delivered_at  INTEGER,
    finished_at   INTEGER,
    status        TEXT    NOT NULL CHECK (status IN ('pending', 'running', 'needs_input', 'completed', 'failed', 'skipped')),
    error         TEXT
);
INSERT INTO schedule_runs SELECT * FROM schedule_runs_keep;
CREATE INDEX schedule_runs_schedule ON schedule_runs (schedule_id, started_at DESC);
CREATE INDEX schedule_runs_open ON schedule_runs (schedule_id) WHERE status IN ('pending', 'running', 'needs_input');

DROP TABLE schedule_runs_keep;
DROP TABLE schedules_keep;
