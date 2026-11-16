-- +goose Up
-- M16：内置 Hindsight 记忆（只加不删）。
-- 敏感值经 secrets.Box 加密后存列，注释里的 AAD 名称与 internal/memory 的 Seal / Open 调用一致。

-- 连接与全局开关（单行）。
CREATE TABLE memory_settings (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    enabled           INTEGER NOT NULL DEFAULT 0,
    hindsight_url     TEXT,                    -- NULL = 未配置
    hindsight_key_enc BLOB,                    -- AAD "memory/hindsight-key"；生成 .env 时轮换
    relay_token_hash  TEXT,                    -- 中转令牌的 SHA-256 十六进制；生成 .env 时轮换
    deployed_families TEXT    NOT NULL DEFAULT '{}',  -- JSON：用途 → 写进 .env 的协议类型（主人点「已重启」后更新）
    embed_locked      INTEGER NOT NULL DEFAULT 0,     -- bank 有记忆后置 1，嵌入模型与维度只读
    last_memory_count INTEGER,                 -- 上次健康检查看到的记忆数（判定 suspect_empty）
    keys_rotated_at   INTEGER,
    updated_at        INTEGER NOT NULL
);

-- 每种用途选定的模型；没有行 = 跟随 default。
CREATE TABLE memory_models (
    purpose      TEXT    PRIMARY KEY CHECK (purpose IN ('default','retain','reflect','consolidation','mental_model')),
    provider_key TEXT    NOT NULL,             -- 中央模型目录里的 provider key
    model_id     TEXT    NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- 嵌入与重排的远端接口。
CREATE TABLE memory_endpoints (
    kind        TEXT    PRIMARY KEY CHECK (kind IN ('embed','rerank')),
    base_url    TEXT    NOT NULL,
    model       TEXT    NOT NULL,
    dimensions  INTEGER,                       -- 只对 embed 有意义
    api_key_enc BLOB,                          -- AAD "memory/endpoint/<kind>/api-key"
    updated_at  INTEGER NOT NULL
);

-- 项目级开关；没有行 = 跟随全局开关。
CREATE TABLE memory_projects (
    project_id TEXT    PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    enabled    INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- agent 提议的全局记忆，等主人批准（不进 Hindsight）。
CREATE TABLE memory_candidates (
    id                TEXT    PRIMARY KEY,
    content           TEXT    NOT NULL,
    context           TEXT,
    source_thread_id  TEXT,                    -- 不设外键：线程删除后候选仍保留
    source_project_id TEXT,
    created_at        INTEGER NOT NULL
);
CREATE INDEX memory_candidates_created ON memory_candidates (created_at);

-- 网关与管理接口写入 Hindsight 的 document 归属。project_id 为 NULL 表示全局记忆。
-- 不设外键：项目删除后要靠这些行逐个清理 Hindsight 里的 document。
CREATE TABLE memory_documents (
    document_id TEXT    PRIMARY KEY,
    project_id  TEXT,
    thread_id   TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX memory_documents_project ON memory_documents (project_id, updated_at);
CREATE INDEX memory_documents_thread ON memory_documents (thread_id);

-- 中转计数，按小时聚合。
CREATE TABLE memory_usage (
    hour          INTEGER NOT NULL,            -- Unix 秒，取整到小时
    purpose       TEXT    NOT NULL CHECK (purpose IN ('default','retain','reflect','consolidation','mental_model','embed','rerank')),
    calls         INTEGER NOT NULL DEFAULT 0,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, purpose)
);

-- 删除 Hindsight document 失败后的重试队列。
CREATE TABLE memory_tombstones (
    document_id     TEXT    PRIMARY KEY,
    project_id      TEXT,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT,
    created_at      INTEGER NOT NULL
);
CREATE INDEX memory_tombstones_due ON memory_tombstones (next_attempt_at);

-- +goose Down
DROP TABLE memory_tombstones;
DROP TABLE memory_usage;
DROP TABLE memory_documents;
DROP TABLE memory_candidates;
DROP TABLE memory_projects;
DROP TABLE memory_endpoints;
DROP TABLE memory_models;
DROP TABLE memory_settings;
