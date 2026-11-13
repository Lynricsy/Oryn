-- +goose Up
-- 项目图标（单个字形：Nerd Font 私有区字符或 emoji；NULL = 未设置，前端回退为名字首字）。
-- 线程自动标题带卡片图标且项目尚无图标时由控制面自动填入一次；手选后不再覆盖。
ALTER TABLE projects ADD COLUMN icon TEXT;
-- agent 头像（所有者级设置）：内置图标字形或 data URL 图片，二者至多其一；都为 NULL = 默认品牌标。
ALTER TABLE owner ADD COLUMN agent_avatar_icon TEXT;
ALTER TABLE owner ADD COLUMN agent_avatar_image TEXT;

-- +goose Down
ALTER TABLE owner DROP COLUMN agent_avatar_image;
ALTER TABLE owner DROP COLUMN agent_avatar_icon;
ALTER TABLE projects DROP COLUMN icon;
