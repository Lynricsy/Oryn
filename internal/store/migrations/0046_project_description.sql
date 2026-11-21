-- +goose Up
-- M24：项目的一行描述（≤200 字符；NULL = 未设置），项目总览与项目主页显示。
ALTER TABLE projects ADD COLUMN description TEXT;

-- +goose Down
ALTER TABLE projects DROP COLUMN description;
