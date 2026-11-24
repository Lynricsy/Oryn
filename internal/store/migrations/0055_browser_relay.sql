-- +goose Up
-- M27：客户端浏览器中继（browser-relay）。
-- 1. devices.kind 与 device_pairings.kind 增加 'browser_extension'。
--    库以 foreign_keys = ON 打开且迁移在事务内，照 0018_analytics.sql 把外键子链
--    （notification_deliveries → push_subscriptions → devices ← device_pairings）
--    先搬进无约束暂存表，由子到父删除、由父到子重建，再搬回，避免 CASCADE 误删数据。

CREATE TABLE notification_deliveries_keep (
    id TEXT, notification_id TEXT, channel_id TEXT, subscription_id TEXT, target TEXT, status TEXT,
    attempts INTEGER, next_attempt_at INTEGER, last_error TEXT, external_ref TEXT, created_at INTEGER, finished_at INTEGER
);
INSERT INTO notification_deliveries_keep (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
FROM notification_deliveries;

CREATE TABLE push_subscriptions_keep (
    id TEXT, kind TEXT, endpoint TEXT, p256dh TEXT, auth TEXT, web_session_hash TEXT,
    device_id TEXT, user_agent TEXT, created_at INTEGER, last_success_at INTEGER, last_error TEXT
);
INSERT INTO push_subscriptions_keep (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error)
SELECT id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error
FROM push_subscriptions;

CREATE TABLE device_pairings_keep (
    code TEXT, verifier_hash TEXT, name TEXT, kind TEXT, platform TEXT,
    created_at INTEGER, expires_at INTEGER, approved_device_id TEXT, requester_ip TEXT, user_agent TEXT
);
INSERT INTO device_pairings_keep (code, verifier_hash, name, kind, platform, created_at, expires_at, approved_device_id, requester_ip, user_agent)
SELECT code, verifier_hash, name, kind, platform, created_at, expires_at, approved_device_id, requester_ip, user_agent
FROM device_pairings;

CREATE TABLE devices_keep (
    id TEXT, name TEXT, kind TEXT, platform TEXT, token_hash TEXT,
    created_at INTEGER, last_seen_at INTEGER, revoked_at INTEGER
);
INSERT INTO devices_keep (id, name, kind, platform, token_hash, created_at, last_seen_at, revoked_at)
SELECT id, name, kind, platform, token_hash, created_at, last_seen_at, revoked_at
FROM devices;

DROP TABLE notification_deliveries;
DROP TABLE push_subscriptions;
DROP TABLE device_pairings;
DROP TABLE devices;

CREATE TABLE devices (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL,
    kind         TEXT    NOT NULL CHECK (kind IN ('cli', 'desktop', 'mobile', 'browser_extension')),
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
    kind               TEXT    NOT NULL CHECK (kind IN ('cli', 'desktop', 'mobile', 'browser_extension')),
    platform           TEXT    NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    expires_at         INTEGER NOT NULL,
    approved_device_id TEXT    REFERENCES devices (id) ON DELETE SET NULL,
    requester_ip       TEXT    NOT NULL DEFAULT '',
    user_agent         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX device_pairings_expires_at ON device_pairings (expires_at);

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

INSERT INTO devices (id, name, kind, platform, token_hash, created_at, last_seen_at, revoked_at)
SELECT id, name, kind, platform, token_hash, created_at, last_seen_at, revoked_at FROM devices_keep;

INSERT INTO device_pairings (code, verifier_hash, name, kind, platform, created_at, expires_at, approved_device_id, requester_ip, user_agent)
SELECT code, verifier_hash, name, kind, platform, created_at, expires_at, approved_device_id, requester_ip, user_agent FROM device_pairings_keep;

INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error)
SELECT id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error FROM push_subscriptions_keep;

INSERT INTO notification_deliveries (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at FROM notification_deliveries_keep;

DROP TABLE notification_deliveries_keep;
DROP TABLE push_subscriptions_keep;
DROP TABLE device_pairings_keep;
DROP TABLE devices_keep;

-- 2. 线程与客户端浏览器扩展设备的绑定关系：每个线程至多绑定一台扩展设备，线程或设备删除时级联清理。
CREATE TABLE thread_browser_relay_bindings (
    thread_id  TEXT    PRIMARY KEY REFERENCES threads (id) ON DELETE CASCADE,
    device_id  TEXT    NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX thread_browser_relay_bindings_device ON thread_browser_relay_bindings (device_id);

-- +goose Down
DROP TABLE thread_browser_relay_bindings;

CREATE TABLE notification_deliveries_keep (
    id TEXT, notification_id TEXT, channel_id TEXT, subscription_id TEXT, target TEXT, status TEXT,
    attempts INTEGER, next_attempt_at INTEGER, last_error TEXT, external_ref TEXT, created_at INTEGER, finished_at INTEGER
);
INSERT INTO notification_deliveries_keep (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
FROM notification_deliveries;

CREATE TABLE push_subscriptions_keep (
    id TEXT, kind TEXT, endpoint TEXT, p256dh TEXT, auth TEXT, web_session_hash TEXT,
    device_id TEXT, user_agent TEXT, created_at INTEGER, last_success_at INTEGER, last_error TEXT
);
INSERT INTO push_subscriptions_keep (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error)
SELECT id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error
FROM push_subscriptions
WHERE device_id IS NULL OR device_id NOT IN (SELECT id FROM devices WHERE kind = 'browser_extension');

CREATE TABLE device_pairings_keep (
    code TEXT, verifier_hash TEXT, name TEXT, kind TEXT, platform TEXT,
    created_at INTEGER, expires_at INTEGER, approved_device_id TEXT, requester_ip TEXT, user_agent TEXT
);
INSERT INTO device_pairings_keep (code, verifier_hash, name, kind, platform, created_at, expires_at, approved_device_id, requester_ip, user_agent)
SELECT code, verifier_hash, name, kind, platform, created_at, expires_at, approved_device_id, requester_ip, user_agent
FROM device_pairings
WHERE kind != 'browser_extension';

CREATE TABLE devices_keep (
    id TEXT, name TEXT, kind TEXT, platform TEXT, token_hash TEXT,
    created_at INTEGER, last_seen_at INTEGER, revoked_at INTEGER
);
INSERT INTO devices_keep (id, name, kind, platform, token_hash, created_at, last_seen_at, revoked_at)
SELECT id, name, kind, platform, token_hash, created_at, last_seen_at, revoked_at
FROM devices
WHERE kind != 'browser_extension';

DROP TABLE notification_deliveries;
DROP TABLE push_subscriptions;
DROP TABLE device_pairings;
DROP TABLE devices;

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
    approved_device_id TEXT    REFERENCES devices (id) ON DELETE SET NULL,
    requester_ip       TEXT    NOT NULL DEFAULT '',
    user_agent         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX device_pairings_expires_at ON device_pairings (expires_at);

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

INSERT INTO devices (id, name, kind, platform, token_hash, created_at, last_seen_at, revoked_at)
SELECT id, name, kind, platform, token_hash, created_at, last_seen_at, revoked_at FROM devices_keep;

INSERT INTO device_pairings (code, verifier_hash, name, kind, platform, created_at, expires_at, approved_device_id, requester_ip, user_agent)
SELECT code, verifier_hash, name, kind, platform, created_at, expires_at, approved_device_id, requester_ip, user_agent FROM device_pairings_keep;

INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error)
SELECT id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at, last_success_at, last_error FROM push_subscriptions_keep;

INSERT INTO notification_deliveries (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at)
SELECT id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, last_error, external_ref, created_at, finished_at
FROM notification_deliveries_keep
WHERE subscription_id IS NULL OR subscription_id IN (SELECT id FROM push_subscriptions);

DROP TABLE notification_deliveries_keep;
DROP TABLE push_subscriptions_keep;
DROP TABLE device_pairings_keep;
DROP TABLE devices_keep;
