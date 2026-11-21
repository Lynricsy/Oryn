-- +goose Up
-- M24 阶段 ③：项目仓库由 Oryn 托管。1 时 <data_dir>/repos/<id>.git 是正本，repo_url 恒为 NULL。
ALTER TABLE projects ADD COLUMN hosted BOOLEAN NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE projects DROP COLUMN hosted;
