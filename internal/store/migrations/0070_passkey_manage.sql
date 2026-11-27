-- +goose Up
-- 设置页的 Passkey 管理与恢复码重新生成：会话记录最近一次 Passkey 验证与登录所用的 Passkey，恢复码记录生成时间。
ALTER TABLE web_sessions ADD COLUMN verified_at INTEGER;   -- 最近一次 Passkey 验证（登录 / 注册 / reauth）；NULL = 从未（恢复码会话、admin 签发、App 会话）
ALTER TABLE web_sessions ADD COLUMN passkey_id  BLOB;      -- 登录所用 Passkey 的 credential id；不设外键，删除 Passkey 时由代码吊销会话
ALTER TABLE recovery_codes ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0;   -- 0 = 本迁移之前生成

-- +goose Down
ALTER TABLE recovery_codes DROP COLUMN created_at;
ALTER TABLE web_sessions DROP COLUMN passkey_id;
ALTER TABLE web_sessions DROP COLUMN verified_at;
