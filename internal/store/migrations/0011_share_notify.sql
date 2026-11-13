-- +goose Up
-- M5：线程公开分享、文件发布、通知渠道、推送订阅、通知与投递队列。

-- 线程公开分享。slug 明文存储（能力令牌：库泄露时转录本身已泄露，哈希不增加保护）。
CREATE TABLE thread_shares (
    id            TEXT    PRIMARY KEY,                          -- shr_<ULID>
    thread_id     TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    slug          TEXT    NOT NULL UNIQUE,                      -- 32 位小写十六进制（16 字节 crypto/rand）
    mode          TEXT    NOT NULL CHECK (mode IN ('live', 'snapshot')),
    until_seq     INTEGER,                                      -- snapshot：创建时的 threads.last_seq；live：NULL
    leaf_entry_id TEXT,                                         -- snapshot：创建时的叶子
    findings      INTEGER NOT NULL DEFAULT 0,                   -- 创建时确认过的扫描命中数
    created_at    INTEGER NOT NULL,
    revoked_at    INTEGER
);
CREATE INDEX thread_shares_thread ON thread_shares (thread_id, revoked_at);

-- 发布为公开 URL 的附件；同一附件至多一条未撤回的发布。
CREATE TABLE published_files (
    id            TEXT    PRIMARY KEY,                          -- pub_<ULID>
    attachment_id TEXT    NOT NULL REFERENCES attachments (id) ON DELETE CASCADE,
    slug          TEXT    NOT NULL UNIQUE,
    created_at    INTEGER NOT NULL,
    revoked_at    INTEGER
);
CREATE UNIQUE INDEX published_files_active ON published_files (attachment_id) WHERE revoked_at IS NULL;

-- 通知渠道。机密配置用 secrets.Box 加密（AAD = "notify/" || id），不进 secrets 表。
CREATE TABLE notification_channels (
    id         TEXT    PRIMARY KEY,                             -- nch_<ULID>
    kind       TEXT    NOT NULL CHECK (kind IN ('telegram', 'webhook', 'ntfy', 'bark')),
    name       TEXT    NOT NULL,
    enabled    BOOLEAN NOT NULL DEFAULT 1,
    events     TEXT    NOT NULL DEFAULT '["done","input","error"]',
    locale     TEXT    NOT NULL DEFAULT 'zh-CN',
    config_enc BLOB    NOT NULL,                                -- secrets.Box.Seal("notify/" || id, 配置 JSON)
    state      TEXT    NOT NULL DEFAULT '{}',                   -- 非机密运行状态（telegram：botUsername、chatId、chatTitle、offset、linkCode、linkExpires）
    last_error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- 推送订阅（webpush；apns / fcm 为 M7 预留）。归属网页会话或设备，随其删除而级联删除。
CREATE TABLE push_subscriptions (
    id               TEXT    PRIMARY KEY,                       -- psub_<ULID>
    kind             TEXT    NOT NULL DEFAULT 'webpush' CHECK (kind IN ('webpush', 'apns', 'fcm')),
    endpoint         TEXT    NOT NULL UNIQUE,                   -- webpush：端点 URL；apns / fcm（M7）：设备推送令牌
    p256dh           TEXT    NOT NULL DEFAULT '',
    auth             TEXT    NOT NULL DEFAULT '',
    web_session_hash TEXT    REFERENCES web_sessions (id_hash) ON DELETE CASCADE,
    device_id        TEXT    REFERENCES devices (id) ON DELETE CASCADE,
    user_agent       TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    last_success_at  INTEGER,
    last_error       TEXT
);

CREATE TABLE notifications (
    id          TEXT    PRIMARY KEY,                            -- ntf_<ULID>
    thread_id   TEXT    REFERENCES threads (id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL CHECK (kind IN ('done', 'input', 'error')),
    title       TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    request_id  TEXT,                                           -- input：待答请求 id；计划提案为 'plan'
    request     TEXT,                                           -- input：extension_ui_request / plan_proposal 帧（作答解析用）
    created_at  INTEGER NOT NULL,
    read_at     INTEGER,
    resolved_at INTEGER                                         -- input：请求已被作答 / 取消 / 随 agent 重启消失
);
CREATE INDEX notifications_created ON notifications (created_at);
CREATE INDEX notifications_thread ON notifications (thread_id, read_at);
-- 同一未决请求只通知一次（控制面重启后 orbd 重放待答请求时去重）。
CREATE UNIQUE INDEX notifications_open_request ON notifications (thread_id, request_id)
    WHERE request_id IS NOT NULL AND resolved_at IS NULL;

-- 投递队列：每条通知 × 每个目标（渠道或推送订阅）一行，库驱动、重启不丢。
CREATE TABLE notification_deliveries (
    id              TEXT    PRIMARY KEY,                        -- ndl_<ULID>
    notification_id TEXT    NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    channel_id      TEXT    REFERENCES notification_channels (id) ON DELETE CASCADE,
    subscription_id TEXT    REFERENCES push_subscriptions (id) ON DELETE CASCADE,
    target          TEXT    NOT NULL,                           -- telegram|webhook|ntfy|bark|webpush（apns|fcm 预留）
    status          TEXT    NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER,
    last_error      TEXT,
    external_ref    TEXT,                                       -- telegram："<chat_id>:<message_id>"
    created_at      INTEGER NOT NULL,
    finished_at     INTEGER
);
CREATE INDEX notification_deliveries_due ON notification_deliveries (status, next_attempt_at);
CREATE INDEX notification_deliveries_ref ON notification_deliveries (target, external_ref);
CREATE INDEX notification_deliveries_notification ON notification_deliveries (notification_id);
CREATE INDEX notification_deliveries_channel ON notification_deliveries (channel_id, created_at);

-- +goose Down
DROP TABLE notification_deliveries;
DROP TABLE notifications;
DROP TABLE push_subscriptions;
DROP TABLE notification_channels;
DROP TABLE published_files;
DROP TABLE thread_shares;
