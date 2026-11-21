-- +goose Up
-- M24 阶段 ④：项目级密钥与环境变量。project_id NULL = 全局（原有行全部是全局）；secret = 0 只允许项目级 env 条目（值可读）。
-- SQLite 去不掉列级 UNIQUE(name)，只能重建：库以 foreign_keys = ON 打开、迁移包在事务里，照 0018 / 0033 先搬进无约束的
-- 暂存表，不用 RENAME TO（secrets 没有子表引用）。id 原样搬回，已下发的占位符 oryn-ph-<id> 继续有效；全局行的 AAD 仍是
-- 名称，不重新封存。
CREATE TABLE secrets_keep (
    id TEXT, name TEXT, mode TEXT, value_enc BLOB, hosts TEXT, created_at INTEGER, updated_at INTEGER
);
INSERT INTO secrets_keep (id, name, mode, value_enc, hosts, created_at, updated_at)
SELECT id, name, mode, value_enc, hosts, created_at, updated_at FROM secrets;
DROP TABLE secrets;

CREATE TABLE secrets (
    id         TEXT    PRIMARY KEY,                    -- sec_<ULID>
    name       TEXT    NOT NULL,
    mode       TEXT    NOT NULL CHECK (mode IN ('proxy', 'env')),
    value_enc  BLOB    NOT NULL,                       -- AAD：全局 = name；项目 = "project:<project_id>:<name>"，不保密的再加 ":plain"
    hosts      TEXT    NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    project_id TEXT    REFERENCES projects (id) ON DELETE CASCADE,   -- NULL = 全局
    secret     BOOLEAN NOT NULL DEFAULT 1,             -- 0 = 普通环境变量（值可读），只用于项目级 env 条目
    CHECK (secret = 1 OR (project_id IS NOT NULL AND mode = 'env'))
);
CREATE UNIQUE INDEX secrets_global_name  ON secrets (name) WHERE project_id IS NULL;
CREATE UNIQUE INDEX secrets_project_name ON secrets (project_id, name) WHERE project_id IS NOT NULL;
INSERT INTO secrets (id, name, mode, value_enc, hosts, created_at, updated_at)
SELECT id, name, mode, value_enc, hosts, created_at, updated_at FROM secrets_keep;
DROP TABLE secrets_keep;

-- +goose Down
-- 回到 0001 的表结构：项目级条目（project_id 非空）无法表示，直接丢弃；全局行的 id 与密文原样保留。
CREATE TABLE secrets_keep (
    id TEXT, name TEXT, mode TEXT, value_enc BLOB, hosts TEXT, created_at INTEGER, updated_at INTEGER
);
INSERT INTO secrets_keep (id, name, mode, value_enc, hosts, created_at, updated_at)
SELECT id, name, mode, value_enc, hosts, created_at, updated_at FROM secrets WHERE project_id IS NULL;
DROP TABLE secrets;

CREATE TABLE secrets (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    mode       TEXT    NOT NULL CHECK (mode IN ('proxy', 'env')),
    value_enc  BLOB    NOT NULL,
    hosts      TEXT    NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
INSERT INTO secrets (id, name, mode, value_enc, hosts, created_at, updated_at)
SELECT id, name, mode, value_enc, hosts, created_at, updated_at FROM secrets_keep;
DROP TABLE secrets_keep;
