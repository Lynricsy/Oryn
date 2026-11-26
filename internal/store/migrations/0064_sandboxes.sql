-- +goose Up
-- M30：agent 沙箱。沙箱是 orbs.sandbox_network IS NOT NULL 的特殊 orb（值 = 创建时的网络模式），
-- sandboxes 行持有归属、回收期限与当前网络模式。时间戳均为 Unix 毫秒。
ALTER TABLE orbs ADD COLUMN sandbox_network TEXT CHECK (sandbox_network IN ('none', 'egress'));

CREATE TABLE sandboxes (
    id              TEXT    PRIMARY KEY,                       -- sbx_<ULID>
    orb_id          TEXT    NOT NULL UNIQUE REFERENCES orbs (id),
    owner_orb_id    TEXT    NOT NULL,                          -- 鉴权主体；不加外键（owner orb 软删除后行仍要可读）
    owner_thread_id TEXT    NOT NULL,                          -- 归因；不加外键（线程删除后行保留到裁剪）
    backend         TEXT    NOT NULL CHECK (backend IN ('docker', 'kvm')),
    provider_name   TEXT    NOT NULL,
    type            TEXT    NOT NULL,
    created_network TEXT    NOT NULL CHECK (created_network IN ('none', 'egress')),
    network         TEXT    NOT NULL CHECK (network IN ('none', 'egress')),
    state           TEXT    NOT NULL CHECK (state IN ('creating', 'ready', 'destroying', 'destroyed', 'failed')),
    idle_sec        INTEGER NOT NULL,
    last_active_at  INTEGER NOT NULL,
    ttl_deadline    INTEGER,
    hard_deadline   INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    ready_at        INTEGER,
    destroyed_at    INTEGER,
    destroy_reason  TEXT,
    error           TEXT
);

CREATE INDEX sandboxes_owner_orb ON sandboxes (owner_orb_id, state);
CREATE INDEX sandboxes_owner_thread ON sandboxes (owner_thread_id, created_at DESC);
CREATE INDEX sandboxes_state ON sandboxes (state);

-- +goose Down
DROP TABLE sandboxes;
ALTER TABLE orbs DROP COLUMN sandbox_network;
