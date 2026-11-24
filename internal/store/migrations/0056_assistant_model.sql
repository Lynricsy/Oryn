-- +goose Up
-- 全局助手的默认模型与思考档位（NULL = 跟随默认解析）。
ALTER TABLE assistant_settings ADD COLUMN model TEXT;
ALTER TABLE assistant_settings ADD COLUMN thinking_level TEXT;

-- +goose Down
ALTER TABLE assistant_settings DROP COLUMN thinking_level;
ALTER TABLE assistant_settings DROP COLUMN model;
