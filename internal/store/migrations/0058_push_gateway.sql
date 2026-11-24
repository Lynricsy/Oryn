-- +goose Up
-- M28：官方推送网关。push_subscriptions.kind 改为 ('webpush', 'gateway')：orynd 不再直发 FCM / APNs，
-- 旧的 fcm / apns 订阅连同其投递一并删除（App 下次进入时经网关重新登记）。
-- 库以 foreign_keys = ON 打开且迁移在事务内，照 0055_browser_relay.sql 把外键子链
-- （notification_deliveries → push_subscriptions）先搬进无约束暂存表，由子到父删除、由父到子重建，再搬回，
-- 避免 CASCADE 误删数据；devices 与 device_pairings 不动。

CREATE TABLE notification_deliveries_keep (
    id TEXT, notification_id TEXT, channel_id TEXT, subscription_id TEXT, target TEXT, status TEXT,
    attempts INTEGER, next_attempt_at INTEGER, last_error TEXT, external_ref TEXT, created_at INTEGER, finished_at INTEGER
);
INSERT INTO notification_deliveries_keep (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
FROM notification_deliveries
WHERE subscription_id IS NULL OR subscription_id IN (SELECT id FROM push_subscriptions WHERE kind = 'webpush');

CREATE TABLE push_subscriptions_keep (
    id TEXT, kind TEXT, endpoint TEXT, p256dh TEXT, auth TEXT, web_session_hash TEXT,
    device_id TEXT, user_agent TEXT, created_at INTEGER, last_success_at INTEGER, last_error TEXT
);
INSERT INTO push_subscriptions_keep (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error)
SELECT id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error
FROM push_subscriptions
WHERE kind = 'webpush';

DROP TABLE notification_deliveries;
DROP TABLE push_subscriptions;

CREATE TABLE push_subscriptions (
    id               TEXT    PRIMARY KEY,
    kind             TEXT    NOT NULL DEFAULT 'webpush' CHECK (kind IN ('webpush', 'gateway')),
    endpoint         TEXT    NOT NULL UNIQUE,
    p256dh           TEXT    NOT NULL DEFAULT '',
    auth             TEXT    NOT NULL DEFAULT '',
    web_session_hash TEXT    REFERENCES web_sessions (id_hash) ON DELETE CASCADE,
    device_id        TEXT    REFERENCES devices (id) ON DELETE CASCADE,
    user_agent       TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    last_success_at  INTEGER,
    last_error       TEXT
);

CREATE TABLE notification_deliveries (
    id              TEXT    PRIMARY KEY,
    notification_id TEXT    NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    channel_id      TEXT    REFERENCES notification_channels (id) ON DELETE CASCADE,
    subscription_id TEXT    REFERENCES push_subscriptions (id) ON DELETE CASCADE,
    target          TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER,
    last_error      TEXT,
    external_ref    TEXT,
    created_at      INTEGER NOT NULL,
    finished_at     INTEGER
);
CREATE INDEX notification_deliveries_due ON notification_deliveries (status, next_attempt_at);
CREATE INDEX notification_deliveries_ref ON notification_deliveries (target, external_ref);
CREATE INDEX notification_deliveries_notification ON notification_deliveries (notification_id);
CREATE INDEX notification_deliveries_channel ON notification_deliveries (channel_id, created_at);

INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error)
SELECT id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error FROM push_subscriptions_keep;

INSERT INTO notification_deliveries (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at FROM notification_deliveries_keep;

DROP TABLE notification_deliveries_keep;
DROP TABLE push_subscriptions_keep;

-- +goose Down
-- 还原 kind 的 CHECK；gateway 订阅连同其投递删除（旧版本没有网关路径），只保留 webpush。

CREATE TABLE notification_deliveries_keep (
    id TEXT, notification_id TEXT, channel_id TEXT, subscription_id TEXT, target TEXT, status TEXT,
    attempts INTEGER, next_attempt_at INTEGER, last_error TEXT, external_ref TEXT, created_at INTEGER, finished_at INTEGER
);
INSERT INTO notification_deliveries_keep (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
FROM notification_deliveries
WHERE subscription_id IS NULL OR subscription_id IN (SELECT id FROM push_subscriptions WHERE kind = 'webpush');

CREATE TABLE push_subscriptions_keep (
    id TEXT, kind TEXT, endpoint TEXT, p256dh TEXT, auth TEXT, web_session_hash TEXT,
    device_id TEXT, user_agent TEXT, created_at INTEGER, last_success_at INTEGER, last_error TEXT
);
INSERT INTO push_subscriptions_keep (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error)
SELECT id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error
FROM push_subscriptions
WHERE kind = 'webpush';

DROP TABLE notification_deliveries;
DROP TABLE push_subscriptions;

CREATE TABLE push_subscriptions (
    id               TEXT    PRIMARY KEY,
    kind             TEXT    NOT NULL DEFAULT 'webpush' CHECK (kind IN ('webpush', 'apns', 'fcm')),
    endpoint         TEXT    NOT NULL UNIQUE,
    p256dh           TEXT    NOT NULL DEFAULT '',
    auth             TEXT    NOT NULL DEFAULT '',
    web_session_hash TEXT    REFERENCES web_sessions (id_hash) ON DELETE CASCADE,
    device_id        TEXT    REFERENCES devices (id) ON DELETE CASCADE,
    user_agent       TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    last_success_at  INTEGER,
    last_error       TEXT
);

CREATE TABLE notification_deliveries (
    id              TEXT    PRIMARY KEY,
    notification_id TEXT    NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
    channel_id      TEXT    REFERENCES notification_channels (id) ON DELETE CASCADE,
    subscription_id TEXT    REFERENCES push_subscriptions (id) ON DELETE CASCADE,
    target          TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER,
    last_error      TEXT,
    external_ref    TEXT,
    created_at      INTEGER NOT NULL,
    finished_at     INTEGER
);
CREATE INDEX notification_deliveries_due ON notification_deliveries (status, next_attempt_at);
CREATE INDEX notification_deliveries_ref ON notification_deliveries (target, external_ref);
CREATE INDEX notification_deliveries_notification ON notification_deliveries (notification_id);
CREATE INDEX notification_deliveries_channel ON notification_deliveries (channel_id, created_at);

INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error)
SELECT id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error FROM push_subscriptions_keep;

INSERT INTO notification_deliveries (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at FROM notification_deliveries_keep;

DROP TABLE notification_deliveries_keep;
DROP TABLE push_subscriptions_keep;
