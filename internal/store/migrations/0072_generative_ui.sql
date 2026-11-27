-- +goose Up
-- 生成式 UI 总开关：agent 是否可以用 render_ui / render_html 在对话里给出视图（缺省开启）。
ALTER TABLE owner ADD COLUMN generative_ui INTEGER NOT NULL DEFAULT 1 CHECK (generative_ui IN (0, 1));

-- +goose Down
ALTER TABLE owner DROP COLUMN generative_ui;
