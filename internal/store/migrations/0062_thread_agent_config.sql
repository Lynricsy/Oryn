-- +goose Up
-- 工作线程按 subagent 配置启动时保存的角色名、追加系统提示词与允许的内置工具白名单（JSON 字符串数组）。
ALTER TABLE threads ADD COLUMN agent_role TEXT;
ALTER TABLE threads ADD COLUMN append_system_prompt TEXT;
ALTER TABLE threads ADD COLUMN allowed_tools TEXT;

-- +goose Down
ALTER TABLE threads DROP COLUMN allowed_tools;
ALTER TABLE threads DROP COLUMN append_system_prompt;
ALTER TABLE threads DROP COLUMN agent_role;
