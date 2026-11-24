-- 0001_init：官方推送网关的授权表（docs/protocol.md「网关存储、限额与隐私」）。
-- 约定：时间一律 UTC 毫秒整数；公钥为请求头里的 base64url 原文；不存任何通知内容，也不存 orynd 的地址。

-- +goose Up

-- 授权：一台手机（设备公钥）允许一个 orynd（主机公钥）经本网关给它的推送令牌发通知。
-- token_enc = 主密钥 Seal("grant/" + id, 推送令牌)。environment 只对 APNs 有意义（production / sandbox），FCM 为空串。
-- last_used_at 至多每小时写一次；连续 90 天未用的授权删除，已吊销的 7 天后删除。
CREATE TABLE grants (
    id           TEXT    PRIMARY KEY,
    device_key   TEXT    NOT NULL,
    host_key     TEXT    NOT NULL,
    channel      TEXT    NOT NULL CHECK (channel IN ('apns', 'fcm')),
    app_id       TEXT    NOT NULL,
    environment  TEXT    NOT NULL DEFAULT '',
    token_enc    BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL,
    revoked_at   INTEGER
);

-- 一把设备密钥同一时间至多一个有效授权（App 同一时间只配对一台服务器）。
CREATE UNIQUE INDEX grants_device_active ON grants (device_key) WHERE revoked_at IS NULL;

-- +goose Down

DROP TABLE grants;
