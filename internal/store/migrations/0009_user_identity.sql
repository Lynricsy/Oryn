-- +goose Up
-- 用户头像（所有者级设置）：规则同 agent 头像——内置图标字形或 data URL 图片，二者至多其一；
-- 都为 NULL = 默认（git 身份名字首字或 User 图标）。
ALTER TABLE owner ADD COLUMN user_avatar_icon TEXT;
ALTER TABLE owner ADD COLUMN user_avatar_image TEXT;

-- +goose Down
ALTER TABLE owner DROP COLUMN user_avatar_image;
ALTER TABLE owner DROP COLUMN user_avatar_icon;
