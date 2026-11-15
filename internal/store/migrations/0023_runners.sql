-- +goose Up
-- M11b：runner（用户自己的机器以 `orbd run --runner` 接入）。
-- runner = 一行 driver='runner' 的 provider（令牌 ort_ 的哈希存在 providers.token_hash）+ 一台 kind='runner' 的常驻 orb。
ALTER TABLE orbs ADD COLUMN kind TEXT NOT NULL DEFAULT 'orb';       -- orb | runner
CREATE TABLE runners (
    provider_id  TEXT PRIMARY KEY REFERENCES providers (id),
    orb_id       TEXT NOT NULL UNIQUE REFERENCES orbs (id),
    hostname     TEXT NOT NULL DEFAULT '',
    os           TEXT NOT NULL DEFAULT '',
    arch         TEXT NOT NULL DEFAULT '',
    dirs         TEXT NOT NULL DEFAULT '[]',                         -- RunnerDir[] 的 JSON（最近一次 hello）
    features     TEXT NOT NULL DEFAULT '[]',                         -- 特性 string[] 的 JSON（最近一次 hello）
    last_seen_at INTEGER
);
ALTER TABLE threads ADD COLUMN cwd TEXT;                             -- runner 线程的工作目录（绝对路径）
ALTER TABLE threads ADD COLUMN worktree TEXT;                        -- Oryn 创建的 worktree：{repo, dir, branch} 的 JSON

-- +goose Down
DROP TABLE runners;
ALTER TABLE threads DROP COLUMN worktree;
ALTER TABLE threads DROP COLUMN cwd;
ALTER TABLE orbs DROP COLUMN kind;
