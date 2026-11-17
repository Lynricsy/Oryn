-- +goose Up
-- M18：SSH 主机与网关。私钥、密码经 secrets.Box 加密后存本表列，不进 secrets 表（不出现在「密钥」设置页）；
-- 注释里的 AAD 名称与 internal/sshgw 的 Seal / Open 调用一致。时间戳均为 Unix 毫秒。
CREATE TABLE ssh_keys (
    id              TEXT    PRIMARY KEY,               -- sshk_<ULID>
    name            TEXT    NOT NULL UNIQUE,
    key_type        TEXT    NOT NULL,                  -- ssh-ed25519 | ssh-rsa | ecdsa-sha2-nistp256 | …
    public_key      TEXT    NOT NULL,                  -- authorized_keys 一行，注释为钥匙名
    fingerprint     TEXT    NOT NULL,                  -- SHA256:…
    private_key_enc BLOB    NOT NULL,                  -- AAD "ssh/key/<id>"；无口令的 OpenSSH PEM
    created_at      INTEGER NOT NULL
);
CREATE TABLE ssh_hosts (
    id               TEXT    PRIMARY KEY,              -- ssh_<ULID>
    project_id       TEXT    REFERENCES projects (id) ON DELETE CASCADE,   -- NULL = 全局
    name             TEXT    NOT NULL,
    address          TEXT    NOT NULL,
    port             INTEGER NOT NULL DEFAULT 22,
    username         TEXT    NOT NULL,
    auth_type        TEXT    NOT NULL CHECK (auth_type IN ('key', 'password')),
    key_id           TEXT    REFERENCES ssh_keys (id),
    password_enc     BLOB,                             -- AAD "ssh/host/<id>/password"
    jump_host_id     TEXT    REFERENCES ssh_hosts (id),
    description      TEXT    NOT NULL DEFAULT '',
    tags             TEXT    NOT NULL DEFAULT '[]',    -- JSON 数组：标签
    enabled          BOOLEAN NOT NULL DEFAULT 1,
    allow_automation BOOLEAN NOT NULL DEFAULT 0,
    allow_forward    BOOLEAN NOT NULL DEFAULT 0,
    compat           BOOLEAN,                          -- Core 的 compat 开关；NULL = 不写进 ssh.json
    host_key         TEXT,                             -- 固定的主机公钥（authorized_keys 格式，无注释）；NULL = 尚未固定
    host_key_at      INTEGER,
    last_test_at     INTEGER,
    last_test_ok     BOOLEAN,
    last_test_error  TEXT,
    last_test_os     TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE UNIQUE INDEX ssh_hosts_global_name  ON ssh_hosts (name) WHERE project_id IS NULL;
CREATE UNIQUE INDEX ssh_hosts_project_name ON ssh_hosts (project_id, name) WHERE project_id IS NOT NULL;
CREATE INDEX ssh_hosts_jump ON ssh_hosts (jump_host_id);
CREATE INDEX ssh_hosts_key  ON ssh_hosts (key_id);
-- 无外键：行比主机、线程与 orb 活得久，保留 30 天。
CREATE TABLE ssh_audit (
    id          INTEGER PRIMARY KEY,
    at          INTEGER NOT NULL,
    source      TEXT    NOT NULL CHECK (source IN ('orb', 'owner', 'tool')),
    orb_id      TEXT,
    thread_id   TEXT,
    host_id     TEXT    NOT NULL,
    host_name   TEXT    NOT NULL,                      -- 名称快照
    kind        TEXT    NOT NULL CHECK (kind IN ('connect', 'deny', 'exec', 'shell', 'subsystem', 'forward', 'test', 'sftp')),
    detail      TEXT    NOT NULL DEFAULT '',
    exit_code   INTEGER,
    duration_ms INTEGER,
    ok          BOOLEAN NOT NULL
);
CREATE INDEX ssh_audit_host   ON ssh_audit (host_id, id);
CREATE INDEX ssh_audit_thread ON ssh_audit (thread_id, id);
CREATE INDEX ssh_audit_at     ON ssh_audit (at);
ALTER TABLE orbs ADD COLUMN automation BOOLEAN NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE orbs DROP COLUMN automation;
DROP TABLE ssh_audit;
DROP TABLE ssh_hosts;
DROP TABLE ssh_keys;
