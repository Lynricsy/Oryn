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
-- 回滚前的代码不认识 runner：先让存量 runner 安全失效，再丢弃身份（runners 表与 kind 列）。否则重新 Up 后它的 orb 会被
-- 当成普通 orb（kind 默认 orb）而 runners 关联已丢失，既不能经 runner API 管理，又占着名字。
-- 1. runner 上的线程解绑（同删除 runner：置空 orb_id，保留转录；工作目录随 cwd 列一并丢弃）。
UPDATE threads SET orb_id = NULL WHERE orb_id IN (SELECT orb_id FROM runners);
-- 2. runner 的常驻 orb 置 deleted（已删除的保持原删除时间）。
UPDATE orbs SET state = 'deleted',
    state_changed_at = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER),
    deleted_at = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)
WHERE id IN (SELECT orb_id FROM runners) AND state <> 'deleted';
-- 3. runner 的 provider 行停用、换掉令牌哈希（ort_ 失效），并改名腾出原名（'#' 不在合法名字字符里，不会与之后的名字冲突）。
--    行本身保留：已删除的 orb 仍以外键引用它。
UPDATE providers SET disabled_at = COALESCE(disabled_at, CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)),
    token_hash = 'revoked-' || lower(hex(randomblob(16))),
    name = name || '#runner-' || id
WHERE driver = 'runner';
DROP TABLE runners;
ALTER TABLE threads DROP COLUMN worktree;
ALTER TABLE threads DROP COLUMN cwd;
ALTER TABLE orbs DROP COLUMN kind;
