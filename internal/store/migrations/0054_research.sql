-- +goose Up
-- M26：全局助手的后台研究。
-- 研究员对话：隐藏的助手对话，research 标明角色；全库每种至多一个。
ALTER TABLE threads ADD COLUMN research TEXT CHECK (research IN ('web','private'));
CREATE UNIQUE INDEX threads_research ON threads (research) WHERE research IS NOT NULL;

-- assistant_inbox：kind 增加 research；restricted = 1 的收件只投给受限的主线 agent。
-- SQLite 改不了 CHECK，只能重建：照 0050 先搬进无约束的暂存表，不用 RENAME TO（没有子表引用）。
CREATE TABLE assistant_inbox_keep (
    id TEXT, kind TEXT, source_id TEXT, source_name TEXT, thread_id TEXT, outcome TEXT, body TEXT, error TEXT,
    created_at INTEGER, delivered_at INTEGER
);
INSERT INTO assistant_inbox_keep SELECT id, kind, source_id, source_name, thread_id, outcome, body, error, created_at, delivered_at
FROM assistant_inbox;
DROP INDEX assistant_inbox_pending;
DROP TABLE assistant_inbox;

CREATE TABLE assistant_inbox (
    id           TEXT    PRIMARY KEY,
    kind         TEXT    NOT NULL CHECK (kind IN ('schedule','trigger','research')),
    source_id    TEXT    NOT NULL,
    source_name  TEXT    NOT NULL,
    thread_id    TEXT    NOT NULL,                           -- 不设外键：线程删除后结果仍可投递
    outcome      TEXT    NOT NULL,
    body         TEXT    NOT NULL DEFAULT '',
    error        TEXT,
    created_at   INTEGER NOT NULL,
    delivered_at INTEGER,
    restricted   INTEGER NOT NULL DEFAULT 0 CHECK (restricted IN (0,1))  -- 1 = 来自外部内容（网页、邮件、日历）
);
CREATE INDEX assistant_inbox_pending ON assistant_inbox (created_at) WHERE delivered_at IS NULL;
INSERT INTO assistant_inbox (id, kind, source_id, source_name, thread_id, outcome, body, error, created_at, delivered_at)
SELECT id, kind, source_id, source_name, thread_id, outcome, body, error, created_at, delivered_at FROM assistant_inbox_keep;
DROP TABLE assistant_inbox_keep;

-- 跟进话题：question = 问题跟进（带当时的回答作基线，到期结束）；interest = 长期关注。
CREATE TABLE research_topics (
    id               TEXT    PRIMARY KEY,                    -- rtp_<ULID>
    kind             TEXT    NOT NULL CHECK (kind IN ('question','interest')),
    title            TEXT    NOT NULL,
    question         TEXT    NOT NULL,
    baseline         TEXT    NOT NULL DEFAULT '',
    origin           TEXT    NOT NULL CHECK (origin IN ('mined','manual','thread')),
    source_thread_id TEXT,                                   -- 不设外键：来源对话删除后话题仍保留
    state            TEXT    NOT NULL CHECK (state IN ('active','paused','done','dismissed')),
    interval_s       INTEGER NOT NULL,
    next_run_at      INTEGER,
    expires_at       INTEGER,
    last_run_at      INTEGER,
    run_count        INTEGER NOT NULL DEFAULT 0,
    last_report      TEXT    NOT NULL DEFAULT '',
    last_sources     TEXT    NOT NULL DEFAULT '[]',          -- JSON [{url,title,date?}]
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE INDEX research_topics_due ON research_topics (next_run_at) WHERE state = 'active';

-- 研究运行记录；id 即投递的标签 rsr_<ULID>。
CREATE TABLE research_runs (
    id          TEXT    PRIMARY KEY,
    pipeline    TEXT    NOT NULL CHECK (pipeline IN ('web','mine','internal','mcp')),
    topic_id    TEXT,
    subject     TEXT    NOT NULL DEFAULT '',                 -- 挖掘：对话 id
    status      TEXT    NOT NULL CHECK (status IN ('running','completed','failed')),
    outcome     TEXT    NOT NULL DEFAULT '',                 -- nothing | findings:<n> | topics:<n>
    report      TEXT    NOT NULL DEFAULT '',
    error       TEXT,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER
);
CREATE INDEX research_runs_started ON research_runs (started_at);

-- 自动挖掘的游标：每个助手对话上一次看到的位置。
CREATE TABLE research_mined (
    thread_id       TEXT    PRIMARY KEY,
    last_message_at INTEGER,
    last_seq        INTEGER NOT NULL DEFAULT 0,
    mined_at        INTEGER NOT NULL
);

-- 后台研究的设置（单行）。
CREATE TABLE research_settings (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    enabled         INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    web             INTEGER NOT NULL DEFAULT 1 CHECK (web IN (0,1)),
    mine            INTEGER NOT NULL DEFAULT 1 CHECK (mine IN (0,1)),
    internal        INTEGER NOT NULL DEFAULT 1 CHECK (internal IN (0,1)),
    mcp             INTEGER NOT NULL DEFAULT 0 CHECK (mcp IN (0,1)),
    model           TEXT,                                    -- provider/id；NULL = 与助手相同
    daily_usd       REAL    NOT NULL DEFAULT 20,
    preferences     TEXT    NOT NULL DEFAULT '',
    mcp_tools       TEXT    NOT NULL DEFAULT '{}',           -- JSON {server: [tool,…]}：研究可用的只读工具
    internal_cursor INTEGER,
    mcp_cursor      INTEGER,
    mine_since      INTEGER,                                 -- 只挖掘总开关打开之后有新消息的对话
    capped_day      TEXT,                                    -- 已发过预算通知的那一天（YYYY-MM-DD，本地时区）
    updated_at      INTEGER NOT NULL
);

-- +goose Down
DROP TABLE research_settings;
DROP TABLE research_mined;
DROP INDEX research_runs_started;
DROP TABLE research_runs;
DROP INDEX research_topics_due;
DROP TABLE research_topics;

DELETE FROM assistant_inbox WHERE kind = 'research';
CREATE TABLE assistant_inbox_keep (
    id TEXT, kind TEXT, source_id TEXT, source_name TEXT, thread_id TEXT, outcome TEXT, body TEXT, error TEXT,
    created_at INTEGER, delivered_at INTEGER
);
INSERT INTO assistant_inbox_keep SELECT id, kind, source_id, source_name, thread_id, outcome, body, error, created_at, delivered_at
FROM assistant_inbox;
DROP INDEX assistant_inbox_pending;
DROP TABLE assistant_inbox;
CREATE TABLE assistant_inbox (
    id           TEXT    PRIMARY KEY,
    kind         TEXT    NOT NULL CHECK (kind IN ('schedule','trigger')),
    source_id    TEXT    NOT NULL,
    source_name  TEXT    NOT NULL,
    thread_id    TEXT    NOT NULL,
    outcome      TEXT    NOT NULL,
    body         TEXT    NOT NULL DEFAULT '',
    error        TEXT,
    created_at   INTEGER NOT NULL,
    delivered_at INTEGER
);
CREATE INDEX assistant_inbox_pending ON assistant_inbox (created_at) WHERE delivered_at IS NULL;
INSERT INTO assistant_inbox SELECT * FROM assistant_inbox_keep;
DROP TABLE assistant_inbox_keep;

DELETE FROM threads WHERE research IS NOT NULL;
DROP INDEX threads_research;
ALTER TABLE threads DROP COLUMN research;
