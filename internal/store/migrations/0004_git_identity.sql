-- +goose Up
-- orb 内 git 提交身份：所有者的默认值 + 项目级覆盖（两项须同时设置才生效）。
ALTER TABLE owner ADD COLUMN git_name TEXT;
ALTER TABLE owner ADD COLUMN git_email TEXT;
ALTER TABLE projects ADD COLUMN git_author_name TEXT;
ALTER TABLE projects ADD COLUMN git_author_email TEXT;

-- +goose Down
ALTER TABLE projects DROP COLUMN git_author_email;
ALTER TABLE projects DROP COLUMN git_author_name;
ALTER TABLE owner DROP COLUMN git_email;
ALTER TABLE owner DROP COLUMN git_name;
