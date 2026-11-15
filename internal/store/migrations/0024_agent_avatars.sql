-- +goose Up
-- M13：omp agent（子代理定义，`agents/*.md` 或内置）的头像（Oryn 侧设置，不进 omp 配置文本）。
-- name 是 agent 的 frontmatter name，原样区分大小写。行存在 = 设了头像，清除头像即删行。
CREATE TABLE agent_avatars (
    name         TEXT    PRIMARY KEY,
    avatar_icon  TEXT,
    avatar_image TEXT,
    updated_at   INTEGER NOT NULL,
    CHECK ((avatar_icon IS NULL) <> (avatar_image IS NULL))
);
-- 新建线程时输入框的缺省目标：role（按 cycleOrder 的 role 起线程）| assistant（交给全局助手）。
ALTER TABLE owner ADD COLUMN new_thread_target TEXT NOT NULL DEFAULT 'role' CHECK (new_thread_target IN ('role', 'assistant'));

-- +goose Down
ALTER TABLE owner DROP COLUMN new_thread_target;
DROP TABLE agent_avatars;
