-- +goose Up
-- 全局 agent 身份的名字（头像见 0008）；NULL = 未设置，界面不显示名字。
ALTER TABLE owner ADD COLUMN agent_name TEXT;
-- 全局助手的身份：名字、头像各自为 NULL 时跟随 agent 身份；图标与图片至多其一。
ALTER TABLE owner ADD COLUMN assistant_name TEXT;
ALTER TABLE owner ADD COLUMN assistant_avatar_icon TEXT;
ALTER TABLE owner ADD COLUMN assistant_avatar_image TEXT;
-- omp role 的头像（Oryn 侧设置，不进 omp 配置文本）。role 是 omp 的 role id，原样区分大小写。
-- 行存在 = 设了头像，清除头像即删行。
CREATE TABLE role_avatars (
    role         TEXT    PRIMARY KEY,
    avatar_icon  TEXT,
    avatar_image TEXT,
    updated_at   INTEGER NOT NULL,
    CHECK ((avatar_icon IS NULL) <> (avatar_image IS NULL))
);
-- 快照分享创建时固化的身份（PublicShareIdentity 的 JSON）；实时分享不写行。撤销时删除。
CREATE TABLE thread_share_identities (
    share_id TEXT PRIMARY KEY REFERENCES thread_shares (id) ON DELETE CASCADE,
    payload  TEXT NOT NULL
);

-- +goose Down
DROP TABLE thread_share_identities;
DROP TABLE role_avatars;
ALTER TABLE owner DROP COLUMN assistant_avatar_image;
ALTER TABLE owner DROP COLUMN assistant_avatar_icon;
ALTER TABLE owner DROP COLUMN assistant_name;
ALTER TABLE owner DROP COLUMN agent_name;
