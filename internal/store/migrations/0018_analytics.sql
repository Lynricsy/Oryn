-- +goose Up
-- M10 统计分析。
-- 用量事实行的补充列：用途（model_usage.purpose；对话回合为空串）、停止原因、跨线程去重键、
-- 以及「线程被删除时写入」的归属快照（活线程的归属在查询时联表取当前值）。
ALTER TABLE usage_records ADD COLUMN purpose     TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN stop_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN ukey        TEXT;
ALTER TABLE usage_records ADD COLUMN project_id  TEXT;
ALTER TABLE usage_records ADD COLUMN space_id    TEXT;
CREATE UNIQUE INDEX usage_records_ukey ON usage_records (ukey);

ALTER TABLE tool_calls ADD COLUMN ukey       TEXT;
ALTER TABLE tool_calls ADD COLUMN project_id TEXT;
ALTER TABLE tool_calls ADD COLUMN space_id   TEXT;
CREATE UNIQUE INDEX tool_calls_ukey ON tool_calls (ukey);

-- 回合 = 主会话文件里的一条 user 消息。
CREATE TABLE turns (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_id  TEXT    REFERENCES threads (id) ON DELETE SET NULL,
    entry_seq  INTEGER NOT NULL,
    ts         INTEGER NOT NULL,
    ukey       TEXT    UNIQUE,
    project_id TEXT,
    space_id   TEXT,
    UNIQUE (thread_id, entry_seq)
);
CREATE INDEX turns_ts ON turns (ts);

-- 线程相对基线的提交（线程静止时采样，整体替换）；统计按 sha 去重。
CREATE TABLE thread_commits (
    thread_id TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    sha       TEXT    NOT NULL,
    ts        INTEGER NOT NULL,                 -- 作者时间
    PRIMARY KEY (thread_id, sha)
);
CREATE INDEX thread_commits_ts ON thread_commits (ts);

CREATE TABLE stats_state (                      -- facts_version、回填进度
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE budget (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    daily_usd   REAL,
    monthly_usd REAL,
    warn_pct    INTEGER NOT NULL DEFAULT 80,
    timezone    TEXT    NOT NULL DEFAULT 'UTC',
    enabled     BOOLEAN NOT NULL DEFAULT 1,
    updated_at  INTEGER NOT NULL
);

-- 预算告警账本：每个周期每个级别至多一行。只做去重与留痕；送达走 notifications（kind = 'budget'，没有线程）。
CREATE TABLE budget_alerts (
    period     TEXT    NOT NULL CHECK (period IN ('day', 'month')),
    period_key TEXT    NOT NULL,                -- 2026-11-14 / 2026-11
    level      TEXT    NOT NULL CHECK (level IN ('warn', 'over')),
    spent_usd  REAL    NOT NULL,
    limit_usd  REAL    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (period, period_key, level)
);

-- notifications：kind 增加 'budget'；新增 link（没有线程的通知的站内落点）。SQLite 不能改 CHECK，只能重建。
-- 库以 foreign_keys = ON 打开，迁移又整个包在事务里（事务内关不掉外键）：直接 DROP notifications 会经
-- ON DELETE CASCADE 清空 notification_deliveries。所以两张表都先搬进无约束的暂存表，先删子表再删父表，
-- 先建父表再建子表，最后搬回。
CREATE TABLE notifications_keep (
    id TEXT, thread_id TEXT, kind TEXT, title TEXT, body TEXT, request_id TEXT, request TEXT,
    created_at INTEGER, read_at INTEGER, resolved_at INTEGER
);
INSERT INTO notifications_keep (id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at)
SELECT id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at FROM notifications;

CREATE TABLE notification_deliveries_keep (
    id TEXT, notification_id TEXT, channel_id TEXT, subscription_id TEXT, target TEXT, status TEXT,
    attempts INTEGER, next_attempt_at INTEGER, last_error TEXT, external_ref TEXT, created_at INTEGER, finished_at INTEGER
);
INSERT INTO notification_deliveries_keep (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
FROM notification_deliveries;

DROP TABLE notification_deliveries;
DROP TABLE notifications;

CREATE TABLE notifications (
    id          TEXT    PRIMARY KEY,                            -- ntf_<ULID>
    thread_id   TEXT    REFERENCES threads (id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL CHECK (kind IN ('done', 'input', 'error', 'budget')),
    title       TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    link        TEXT,                                           -- 没有线程的通知的站内路径；有线程时为 NULL
    request_id  TEXT,                                           -- input：待答请求 id；计划提案为 'plan'
    request     TEXT,                                           -- input：extension_ui_request / plan_proposal 帧
    created_at  INTEGER NOT NULL,
    read_at     INTEGER,
    resolved_at INTEGER
);
CREATE INDEX notifications_created ON notifications (created_at);
CREATE INDEX notifications_thread ON notifications (thread_id, read_at);
CREATE UNIQUE INDEX notifications_open_request ON notifications (thread_id, request_id)
    WHERE request_id IS NOT NULL AND resolved_at IS NULL;

CREATE TABLE notification_deliveries (
    id              TEXT    PRIMARY KEY,                        -- ndl_<ULID>
    notification_id TEXT    NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    channel_id      TEXT    REFERENCES notification_channels (id) ON DELETE CASCADE,
    subscription_id TEXT    REFERENCES push_subscriptions (id) ON DELETE CASCADE,
    target          TEXT    NOT NULL,                           -- telegram|webhook|ntfy|bark|webpush|fcm（apns 预留）
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

-- M9 的无线程通知（定时失败 / 自动暂停）此前的落点写死在代码里：回填成数据。
INSERT INTO notifications (id, thread_id, kind, title, body, link, request_id, request, created_at, read_at, resolved_at)
SELECT id, thread_id, kind, title, body,
       CASE WHEN thread_id IS NULL THEN '/settings/automations' END,
       request_id, request, created_at, read_at, resolved_at
FROM notifications_keep;
INSERT INTO notification_deliveries (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
FROM notification_deliveries_keep;
DROP TABLE notification_deliveries_keep;
DROP TABLE notifications_keep;

-- 已订阅 error 的渠道同时订阅 budget。events 是 normalizeEvents 写出的规范 JSON（无空格、顺序固定），error 存在时总在末尾。
UPDATE notification_channels SET events = replace(events, '"error"]', '"error","budget"]')
WHERE events LIKE '%"error"]';

-- +goose Down
UPDATE notification_channels SET events = replace(replace(events, ',"budget"', ''), '"budget"', '');

CREATE TABLE notifications_keep (
    id TEXT, thread_id TEXT, kind TEXT, title TEXT, body TEXT, request_id TEXT, request TEXT,
    created_at INTEGER, read_at INTEGER, resolved_at INTEGER
);
INSERT INTO notifications_keep (id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at)
SELECT id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at
FROM notifications WHERE kind <> 'budget';
CREATE TABLE notification_deliveries_keep (
    id TEXT, notification_id TEXT, channel_id TEXT, subscription_id TEXT, target TEXT, status TEXT,
    attempts INTEGER, next_attempt_at INTEGER, last_error TEXT, external_ref TEXT, created_at INTEGER, finished_at INTEGER
);
INSERT INTO notification_deliveries_keep (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT d.id, d.notification_id, d.channel_id, d.subscription_id, d.target, d.status, d.attempts, d.next_attempt_at, d.last_error, d.external_ref, d.created_at, d.finished_at
FROM notification_deliveries d JOIN notifications n ON n.id = d.notification_id WHERE n.kind <> 'budget';
DROP TABLE notification_deliveries;
DROP TABLE notifications;

-- 按 0011_share_notify.sql 原样重建（三值 CHECK、无 link）。
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
CREATE UNIQUE INDEX notifications_open_request ON notifications (thread_id, request_id)
    WHERE request_id IS NOT NULL AND resolved_at IS NULL;

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

INSERT INTO notifications (id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at)
SELECT id, thread_id, kind, title, body, request_id, request, created_at, read_at, resolved_at FROM notifications_keep;
INSERT INTO notification_deliveries (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
FROM notification_deliveries_keep;
DROP TABLE notification_deliveries_keep;
DROP TABLE notifications_keep;

DROP TABLE budget_alerts;
DROP TABLE budget;
DROP TABLE stats_state;
DROP TABLE thread_commits;
DROP TABLE turns;

DROP INDEX tool_calls_ukey;
DROP INDEX usage_records_ukey;
ALTER TABLE tool_calls DROP COLUMN space_id;
ALTER TABLE tool_calls DROP COLUMN project_id;
ALTER TABLE tool_calls DROP COLUMN ukey;
ALTER TABLE usage_records DROP COLUMN space_id;
ALTER TABLE usage_records DROP COLUMN project_id;
ALTER TABLE usage_records DROP COLUMN ukey;
ALTER TABLE usage_records DROP COLUMN stop_reason;
ALTER TABLE usage_records DROP COLUMN purpose;
