-- +goose Up
-- M30：provider 任务增加 set_network（切换沙箱的网络模式）。
-- SQLite 改不了 CHECK，只能重建：照 0054 先搬进无约束的暂存表，不用 RENAME TO（jobs 没有子表引用）。
-- 列顺序保持不变（0005 的 claim_gen、0021 的 result 仍在末尾），sqlc 生成物不受影响。
CREATE TABLE jobs_keep (
    id TEXT, provider_id TEXT, orb_id TEXT, op TEXT, params TEXT, state TEXT, attempt INTEGER, claimed_at INTEGER,
    lease_until INTEGER, finished_at INTEGER, output TEXT, error TEXT, created_at INTEGER, claim_gen INTEGER, result TEXT
);
INSERT INTO jobs_keep SELECT id, provider_id, orb_id, op, params, state, attempt, claimed_at, lease_until, finished_at,
    output, error, created_at, claim_gen, result FROM jobs;
DROP TABLE jobs;

CREATE TABLE jobs (
    id          TEXT    PRIMARY KEY,
    provider_id TEXT    NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    orb_id      TEXT    REFERENCES orbs (id) ON DELETE CASCADE,
    op          TEXT    NOT NULL CHECK (op IN ('spawn', 'pause', 'snapshot', 'delete', 'delete_snapshot', 'set_network')),
    params      TEXT    NOT NULL DEFAULT '{}',
    state       TEXT    NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'claimed', 'succeeded', 'failed')),
    attempt     INTEGER NOT NULL DEFAULT 0,
    claimed_at  INTEGER,
    lease_until INTEGER,
    finished_at INTEGER,
    output      TEXT    NOT NULL DEFAULT '',
    error       TEXT,
    created_at  INTEGER NOT NULL,
    claim_gen   INTEGER NOT NULL DEFAULT 0,
    result      TEXT
);
INSERT INTO jobs SELECT * FROM jobs_keep;
DROP TABLE jobs_keep;
CREATE INDEX jobs_provider_state_created ON jobs (provider_id, state, created_at);
CREATE INDEX jobs_state_lease_until ON jobs (state, lease_until);
CREATE INDEX jobs_orb_id ON jobs (orb_id);

-- +goose Down
DELETE FROM jobs WHERE op = 'set_network';
CREATE TABLE jobs_keep (
    id TEXT, provider_id TEXT, orb_id TEXT, op TEXT, params TEXT, state TEXT, attempt INTEGER, claimed_at INTEGER,
    lease_until INTEGER, finished_at INTEGER, output TEXT, error TEXT, created_at INTEGER, claim_gen INTEGER, result TEXT
);
INSERT INTO jobs_keep SELECT id, provider_id, orb_id, op, params, state, attempt, claimed_at, lease_until, finished_at,
    output, error, created_at, claim_gen, result FROM jobs;
DROP TABLE jobs;

CREATE TABLE jobs (
    id          TEXT    PRIMARY KEY,
    provider_id TEXT    NOT NULL REFERENCES providers (id) ON DELETE CASCADE,
    orb_id      TEXT    REFERENCES orbs (id) ON DELETE CASCADE,
    op          TEXT    NOT NULL CHECK (op IN ('spawn', 'pause', 'snapshot', 'delete', 'delete_snapshot')),
    params      TEXT    NOT NULL DEFAULT '{}',
    state       TEXT    NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'claimed', 'succeeded', 'failed')),
    attempt     INTEGER NOT NULL DEFAULT 0,
    claimed_at  INTEGER,
    lease_until INTEGER,
    finished_at INTEGER,
    output      TEXT    NOT NULL DEFAULT '',
    error       TEXT,
    created_at  INTEGER NOT NULL,
    claim_gen   INTEGER NOT NULL DEFAULT 0,
    result      TEXT
);
INSERT INTO jobs SELECT * FROM jobs_keep;
DROP TABLE jobs_keep;
CREATE INDEX jobs_provider_state_created ON jobs (provider_id, state, created_at);
CREATE INDEX jobs_state_lease_until ON jobs (state, lease_until);
CREATE INDEX jobs_orb_id ON jobs (orb_id);
