-- +goose Up
-- M6：桌面 / 移动 App 用设备令牌换来的网页会话：device_id 非空。吊销设备时由 auth.RevokeDevice 删除。
-- 不声明外键：SQLite 的 DROP COLUMN 不能删带外键的列，而设备行只会被吊销、不会被删除。
ALTER TABLE web_sessions ADD COLUMN device_id TEXT;
CREATE INDEX web_sessions_device ON web_sessions (device_id) WHERE device_id IS NOT NULL;

-- +goose Down
DROP INDEX web_sessions_device;
ALTER TABLE web_sessions DROP COLUMN device_id;
