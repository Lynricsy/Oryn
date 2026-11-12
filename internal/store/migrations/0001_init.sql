-- 0001_init：M2 全部表（计划 D 节）。
-- 约定：时间一律 UTC 毫秒整数；ID 为带前缀的 TEXT；JSON 以 TEXT 保存；布尔以 0/1 保存。

-- +goose Up

-- 唯一所有者（单用户）。user_handle 为 WebAuthn 用户句柄（随机字节）。
CREATE TABLE owner (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    display_name TEXT    NOT NULL,
    user_handle  BLOB    NOT NULL,
    created_at   INTEGER NOT NULL
);

-- Passkey 凭据。id = credential id；credential 保存完整 webauthn.Credential JSON（flags、aaguid、attestation 等）。
CREATE TABLE passkeys (
    id           BLOB    PRIMARY KEY,
    public_key   BLOB    NOT NULL,
    sign_count   INTEGER NOT NULL DEFAULT 0,
    transports   TEXT    NOT NULL DEFAULT '[]',
    credential   TEXT    NOT NULL DEFAULT '{}',
    name         TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);

CREATE TABLE recovery_codes (
    code_hash TEXT PRIMARY KEY,
    used_at   INTEGER
);

-- 网页会话。kind=recovery 为恢复码换来的「重新注册 Passkey」一次性会话。
CREATE TABLE web_sessions (
    id_hash      TEXT    PRIMARY KEY,
    kind         TEXT    NOT NULL DEFAULT 'full' CHECK (kind IN ('full', 'recovery')),
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    user_agent   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX web_sessions_expires_at ON web_sessions (expires_at);

CREATE TABLE devices (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL,
    kind         TEXT    NOT NULL CHECK (kind IN ('cli', 'desktop', 'mobile')),
    platform     TEXT    NOT NULL DEFAULT '',
    token_hash   TEXT    NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER,
    revoked_at   INTEGER
);

CREATE TABLE device_pairings (
    code               TEXT    PRIMARY KEY,
    verifier_hash      TEXT    NOT NULL,
    name               TEXT    NOT NULL,
    kind               TEXT    NOT NULL CHECK (kind IN ('cli', 'desktop', 'mobile')),
    platform           TEXT    NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    expires_at         INTEGER NOT NULL,
    approved_device_id TEXT    REFERENCES devices (id) ON DELETE SET NULL
);
CREATE INDEX device_pairings_expires_at ON device_pairings (expires_at);

CREATE TABLE projects (
    id               TEXT    PRIMARY KEY,
    name             TEXT    NOT NULL UNIQUE,
    repo_url         TEXT,
    base_branch      TEXT,
    default_provider TEXT,
    default_orb_type TEXT,
    setup_script     TEXT,
    resume_script    TEXT,
    shallow_clone    BOOLEAN NOT NULL DEFAULT 1,
    color            TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    archived_at      INTEGER
);

-- provider：capabilities / orb_types 为 hello 上报的 JSON；instance_id + lease_until 为单实例租约。
CREATE TABLE providers (
    id             TEXT    PRIMARY KEY,
    name           TEXT    NOT NULL UNIQUE,
    token_hash     TEXT    NOT NULL UNIQUE,
    driver         TEXT    NOT NULL DEFAULT '',
    version        TEXT    NOT NULL DEFAULT '',
    capabilities   TEXT    NOT NULL DEFAULT '{}',
    orb_types      TEXT    NOT NULL DEFAULT '[]',
    default_type   TEXT    NOT NULL DEFAULT '',
    allow_multiple BOOLEAN NOT NULL DEFAULT 0,
    instance_id    TEXT,
    lease_until    INTEGER,
    created_at     INTEGER NOT NULL,
    disabled_at    INTEGER
);

-- orb：状态见计划 E 节状态机；删除为软删除（deleted_at）。
CREATE TABLE orbs (
    id                       TEXT    PRIMARY KEY,
    provider_id              TEXT    NOT NULL REFERENCES providers (id),
    type                     TEXT    NOT NULL,
    cpus                     INTEGER NOT NULL,
    memory_mb                INTEGER NOT NULL,
    image                    TEXT    NOT NULL,
    project_id               TEXT    REFERENCES projects (id) ON DELETE SET NULL,
    state                    TEXT    NOT NULL CHECK (state IN (
        'requested', 'spawning', 'connecting', 'setting_up', 'resuming', 'ready',
        'freezing', 'frozen', 'stopping', 'stopped', 'waking', 'deleting', 'deleted', 'failed')),
    state_changed_at         INTEGER NOT NULL,
    token_hash               TEXT    NOT NULL UNIQUE,
    snapshot_of              TEXT,
    last_agent_activity_at   INTEGER,
    last_user_interaction_at INTEGER,
    orbd_version             TEXT,
    agent_version            TEXT,
    created_at               INTEGER NOT NULL,
    deleted_at               INTEGER
);
CREATE INDEX orbs_state ON orbs (state);
CREATE INDEX orbs_provider_id ON orbs (provider_id);
CREATE INDEX orbs_project_id ON orbs (project_id);

CREATE TABLE orb_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    orb_id     TEXT    NOT NULL REFERENCES orbs (id) ON DELETE CASCADE,
    ts         INTEGER NOT NULL,
    from_state TEXT    NOT NULL,
    to_state   TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX orb_events_orb_id_ts ON orb_events (orb_id, ts);
CREATE INDEX orb_events_ts ON orb_events (ts);

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
    created_at  INTEGER NOT NULL
);
CREATE INDEX jobs_provider_state_created ON jobs (provider_id, state, created_at);
CREATE INDEX jobs_state_lease_until ON jobs (state, lease_until);
CREATE INDEX jobs_orb_id ON jobs (orb_id);

-- 线程。last_seq 是本线程已分配过的最大条目 seq（由触发器维护，单调不减，删除 stale 行后也不回退）。
CREATE TABLE threads (
    id                   TEXT    PRIMARY KEY,
    project_id           TEXT    REFERENCES projects (id) ON DELETE SET NULL,
    orb_id               TEXT    REFERENCES orbs (id) ON DELETE SET NULL,
    title                TEXT    NOT NULL DEFAULT '',
    title_source         TEXT    NOT NULL DEFAULT 'auto' CHECK (title_source IN ('auto', 'user')),
    state                TEXT    NOT NULL DEFAULT 'provisioning' CHECK (state IN (
        'provisioning', 'awaiting_input', 'working', 'error', 'idle')),
    state_changed_at     INTEGER NOT NULL,
    parent_thread_id     TEXT    REFERENCES threads (id) ON DELETE SET NULL,
    forked_from_entry_id TEXT,
    session_dir          TEXT    NOT NULL,
    leaf_entry_id        TEXT,
    model                TEXT,
    thinking_level       TEXT,
    color                TEXT,
    pinned               BOOLEAN NOT NULL DEFAULT 0,
    snoozed_until        INTEGER,
    archived_at          INTEGER,
    unread               BOOLEAN NOT NULL DEFAULT 0,
    last_seq             INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    last_message_at      INTEGER
);
CREATE INDEX threads_state ON threads (state);
CREATE INDEX threads_project_id ON threads (project_id);
CREATE INDEX threads_orb_id ON threads (orb_id);
CREATE INDEX threads_archived_at ON threads (archived_at);
CREATE INDEX threads_parent_thread_id ON threads (parent_thread_id);
CREATE INDEX threads_updated_at ON threads (updated_at);

CREATE TABLE thread_labels (
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    label     TEXT NOT NULL,
    PRIMARY KEY (thread_id, label)
);
CREATE INDEX thread_labels_label ON thread_labels (label);

-- 条目 = 会话 JSONL 的行。ts 为该行 timestamp 换算的毫秒（无则为空）；payload 为原始行。
CREATE TABLE entries (
    thread_id TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    seq       INTEGER NOT NULL,
    file      TEXT    NOT NULL,
    entry_id  TEXT    NOT NULL,
    parent_id TEXT,
    type      TEXT    NOT NULL DEFAULT '',
    ts        INTEGER,
    payload   BLOB    NOT NULL,
    stale     BOOLEAN NOT NULL DEFAULT 0,
    PRIMARY KEY (thread_id, seq),
    UNIQUE (thread_id, file, entry_id)
);
CREATE INDEX entries_thread_file_stale ON entries (thread_id, file, stale);

-- +goose StatementBegin
CREATE TRIGGER entries_bump_last_seq AFTER INSERT ON entries
BEGIN
    UPDATE threads SET last_seq = NEW.seq WHERE id = NEW.thread_id AND last_seq < NEW.seq;
END;
-- +goose StatementEnd

-- 复制游标：byte_offset 为该文件已入库的字节偏移（计划中的 offset 列；避开 SQL 关键字）。
CREATE TABLE entry_cursors (
    thread_id   TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    file        TEXT    NOT NULL,
    byte_offset INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (thread_id, file)
);

CREATE TABLE blobs (
    sha256     TEXT    PRIMARY KEY,
    size       INTEGER NOT NULL,
    mime       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE TABLE secrets (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    mode       TEXT    NOT NULL CHECK (mode IN ('proxy', 'env')),
    value_enc  BLOB    NOT NULL,
    hosts      TEXT    NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE orb_samples (
    orb_id       TEXT    NOT NULL REFERENCES orbs (id) ON DELETE CASCADE,
    ts           INTEGER NOT NULL,
    cpu_pct      REAL    NOT NULL,
    mem_used_mb  INTEGER NOT NULL,
    mem_limit_mb INTEGER NOT NULL,
    disk_used_mb INTEGER NOT NULL,
    net_rx_bytes INTEGER NOT NULL,
    net_tx_bytes INTEGER NOT NULL,
    PRIMARY KEY (orb_id, ts)
);

-- 用量与工具调用：线程删除后保留统计（thread_id 置空）；(thread_id, entry_seq) 唯一，保证条目重发时幂等。
CREATE TABLE usage_records (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id          TEXT    REFERENCES threads (id) ON DELETE SET NULL,
    entry_seq          INTEGER NOT NULL,
    ts                 INTEGER NOT NULL,
    provider           TEXT    NOT NULL DEFAULT '',
    model              TEXT    NOT NULL DEFAULT '',
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL    NOT NULL DEFAULT 0,
    duration_ms        INTEGER,
    ttft_ms            INTEGER,
    is_subagent        BOOLEAN NOT NULL DEFAULT 0,
    UNIQUE (thread_id, entry_seq)
);
CREATE INDEX usage_records_ts ON usage_records (ts);

CREATE TABLE tool_calls (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id TEXT    REFERENCES threads (id) ON DELETE SET NULL,
    entry_seq INTEGER NOT NULL,
    ts        INTEGER NOT NULL,
    tool      TEXT    NOT NULL,
    is_error  BOOLEAN NOT NULL DEFAULT 0,
    UNIQUE (thread_id, entry_seq)
);
CREATE INDEX tool_calls_ts ON tool_calls (ts);

-- +goose Down
DROP TABLE tool_calls;
DROP TABLE usage_records;
DROP TABLE orb_samples;
DROP TABLE secrets;
DROP TABLE blobs;
DROP TABLE entry_cursors;
DROP TRIGGER entries_bump_last_seq;
DROP TABLE entries;
DROP TABLE thread_labels;
DROP TABLE threads;
DROP TABLE jobs;
DROP TABLE orb_events;
DROP TABLE orbs;
DROP TABLE providers;
DROP TABLE projects;
DROP TABLE device_pairings;
DROP TABLE devices;
DROP TABLE web_sessions;
DROP TABLE recovery_codes;
DROP TABLE passkeys;
DROP TABLE owner;
