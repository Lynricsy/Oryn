-- +goose Up
-- 全局助手对话已按需加载的宿主工具组（JSON 字符串数组，按组定义顺序；NULL = 一个都没加载）。agent 重启时据此恢复工具表。
ALTER TABLE threads ADD COLUMN tool_groups TEXT;

-- +goose Down
ALTER TABLE threads DROP COLUMN tool_groups;
