-- +goose Up
-- 全局助手的自定义附加系统提示词（空串 = 不追加）。
ALTER TABLE assistant_settings ADD COLUMN system_prompt TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE assistant_settings DROP COLUMN system_prompt;
