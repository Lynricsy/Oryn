-- +goose Up
-- M17：全局助手的主人记忆（Hindsight bank oryn-owner，只由 orynd 访问；只加不删）。
-- 敏感值经 secrets.Box 加密后存列，注释里的 AAD 名称与 internal/ownermem 的 Seal / Open 调用一致。

-- 助手记忆的设置与运行状态（单行）。
CREATE TABLE owner_memory_settings (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    enabled         INTEGER NOT NULL DEFAULT 0,
    owner_names     TEXT    NOT NULL DEFAULT '[]',  -- JSON 数组：主人的名字与别名，写入时统一归为「主人」
    decide_base_url TEXT,                           -- 判断模型接口地址；NULL = 未配置（召回按 RRF 顺序给出）
    decide_model    TEXT,
    decide_key_enc  BLOB,                           -- AAD "owner-memory/decide-key"
    backfill_since  INTEGER,                        -- 首次开启时定下的回填起点（Unix 毫秒）；NULL = 尚未开启过
    backfill_total  INTEGER NOT NULL DEFAULT 0,
    backfill_done   INTEGER NOT NULL DEFAULT 0,
    config_hash     TEXT,                           -- 已写入 bank 配置（mission 等）的哈希；变化时重写
    last_curated_at INTEGER,
    last_curation   TEXT,                           -- JSON：最近一次主题整理的结果
    updated_at      INTEGER NOT NULL
);

-- 写入进度：每个来源线程一行。不设外键：线程删除后要靠这一行清理 Hindsight 里的 document。
CREATE TABLE owner_memory_sources (
    thread_id       TEXT    PRIMARY KEY,
    kind            TEXT    NOT NULL CHECK (kind IN ('conv','thread')),
    last_message_at INTEGER,                        -- 已写入时的 threads.last_message_at（有新消息即变）
    transcript_rev  INTEGER NOT NULL DEFAULT 0,     -- 已写入时的 threads.transcript_rev（转录被重写时加一）
    chunks          INTEGER NOT NULL DEFAULT 0,
    retained_at     INTEGER,
    next_attempt_at INTEGER,                        -- NULL = 没有待重试
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX owner_memory_sources_retry ON owner_memory_sources (next_attempt_at);

-- 每个来源按块写入，一块一个 document；只重写内容哈希变了的块。
CREATE TABLE owner_memory_chunks (
    thread_id   TEXT    NOT NULL,
    idx         INTEGER NOT NULL,
    document_id TEXT    NOT NULL UNIQUE,
    hash        TEXT    NOT NULL,
    seq_from    INTEGER NOT NULL,
    seq_to      INTEGER NOT NULL,
    retained_at INTEGER NOT NULL,
    PRIMARY KEY (thread_id, idx)
);

-- 不进助手记忆的对话与项目。
CREATE TABLE owner_memory_exclusions (
    kind       TEXT    NOT NULL CHECK (kind IN ('thread','project')),
    target_id  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (kind, target_id)
);

-- 主题（bank 里的 mental model）。owner-profile 是固定种子，其余由每日主题整理产生。
CREATE TABLE owner_memory_topics (
    model_id        TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL,
    scope           TEXT    NOT NULL,               -- 一句范围说明，拼进 source_query
    seed            INTEGER NOT NULL DEFAULT 0,
    status          TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    position        INTEGER NOT NULL DEFAULT 0,     -- 注入时的顺序
    last_refresh_at INTEGER,
    last_hit_at     INTEGER,                        -- 最近一次检查时命中的新事实时间
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- 明确记忆：主人说「记住……」或助手调用 remember。pending 等主人批准，active 已写入 explicit:<id>。
CREATE TABLE owner_memory_explicit (
    id               TEXT    PRIMARY KEY,
    content          TEXT    NOT NULL,
    owner_quote      TEXT,
    source_thread_id TEXT,
    status           TEXT    NOT NULL CHECK (status IN ('pending','active')),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE INDEX owner_memory_explicit_status ON owner_memory_explicit (status, created_at);

-- 注入快照：每个助手对话一份，生成超过 1 小时后在下次拉起时重建。
CREATE TABLE owner_memory_snapshots (
    thread_id    TEXT    PRIMARY KEY REFERENCES threads (id) ON DELETE CASCADE,
    content      TEXT    NOT NULL,
    generated_at INTEGER NOT NULL
);

-- M16 的遗忘保护与删除重试按 bank 区分。主人记忆的模型 id 带 owner- 前缀、document id 带 conv: / thread: /
-- explicit: 前缀，与工程记忆不冲突，所以主键不变。
ALTER TABLE memory_stale_models ADD COLUMN bank TEXT NOT NULL DEFAULT 'oryn';
ALTER TABLE memory_tombstones ADD COLUMN bank TEXT NOT NULL DEFAULT 'oryn';

-- +goose Down
ALTER TABLE memory_tombstones DROP COLUMN bank;
ALTER TABLE memory_stale_models DROP COLUMN bank;
DROP TABLE owner_memory_snapshots;
DROP TABLE owner_memory_explicit;
DROP TABLE owner_memory_topics;
DROP TABLE owner_memory_exclusions;
DROP TABLE owner_memory_chunks;
DROP TABLE owner_memory_sources;
DROP TABLE owner_memory_settings;
