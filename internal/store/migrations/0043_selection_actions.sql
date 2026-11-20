-- +goose Up
-- M22：自定义选区快捷动作（选中文字后出现在右键菜单里），JSON 数组 [{id, name, prompt}]，整体替换。
ALTER TABLE owner ADD COLUMN selection_actions TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE owner DROP COLUMN selection_actions;
