-- +goose Up
-- 全局助手的备用对话：预先建好、agent 已起的空白助手对话，新建对话时直接认领（免冷启动）。认领前对用户不可见：
-- 不进线程列表与 index 推送，hidden = 1；认领时清零。
ALTER TABLE threads ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0,1));
CREATE INDEX threads_hidden ON threads (orb_id, created_at) WHERE hidden = 1;

-- +goose Down
DROP INDEX threads_hidden;
ALTER TABLE threads DROP COLUMN hidden;
