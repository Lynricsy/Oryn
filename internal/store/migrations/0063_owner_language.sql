-- +goose Up
-- 全局语言设置（界面显示跨设备同步，以及 agent 自动生成标题与工具调用 intent 的语言）。
ALTER TABLE owner ADD COLUMN language TEXT NOT NULL DEFAULT 'zh-CN' CHECK (language IN ('zh-CN', 'en'));

-- +goose Down
ALTER TABLE owner DROP COLUMN language;
