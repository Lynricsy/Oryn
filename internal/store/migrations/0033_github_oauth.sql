-- +goose Up
-- M15：GitHub 改用 OAuth App（只加不删；App 的表与列由 0034 删除）。
-- 敏感值经 secrets.Box 加密后存列，注释里的 AAD 名称与 internal/github 的 Seal / Open 调用一致。

-- OAuth App 凭据（单行）。
CREATE TABLE github_oauth_app (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    client_id         TEXT    NOT NULL,
    client_secret_enc BLOB    NOT NULL,        -- AAD "github/oauth/client-secret"
    webhook_base      TEXT,                    -- 仓库 hook 的回调基址；NULL = public_url
    created_at        INTEGER NOT NULL
);

-- Oryn 在仓库上建的 hook（每个仓库至多一个）。
CREATE TABLE github_hooks (
    repo_id           INTEGER PRIMARY KEY,
    full_name         TEXT    NOT NULL,
    hook_id           INTEGER,                 -- 未建成（no_admin / org_restricted / sso_required / error）时为 NULL
    secret_enc        BLOB,                    -- AAD "github/hook/<repo_id>/secret"
    status            TEXT    NOT NULL CHECK (status IN ('active','no_admin','org_restricted','sso_required','error')),
    error             TEXT,
    delivery_cursor   INTEGER NOT NULL DEFAULT 0,   -- 投递日志已处理到的投递 id；0 = 尚未建立基线
    deliveries_etag   TEXT,
    last_delivery_at  INTEGER,
    last_delivery_src TEXT    CHECK (last_delivery_src IN ('webhook','poll')),
    last_webhook_at   INTEGER,
    updated_at        INTEGER NOT NULL
);
CREATE UNIQUE INDEX github_hooks_hook ON github_hooks (hook_id);

-- 仓库级 REST 轮询（没有 active hook、且有启用规则的已绑定仓库）。
CREATE TABLE github_repo_watches (
    repo_id       INTEGER PRIMARY KEY,
    full_name     TEXT    NOT NULL,
    etag_issues   TEXT,
    etag_comments TEXT,
    etag_pulls    TEXT,
    etag_runs     TEXT,
    since_at      INTEGER,                     -- issues / comments 的 since 游标；NULL = 尚未建立基线
    updated_at    INTEGER NOT NULL
);

-- 对象级 REST 轮询（线程关联的 issue / PR，所在仓库没有 active hook）。
CREATE TABLE github_watches (
    kind                 TEXT    NOT NULL CHECK (kind IN ('pull','issue')),
    repo_id              INTEGER NOT NULL,
    full_name            TEXT    NOT NULL,
    number               INTEGER NOT NULL,
    thread_id            TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    slot                 TEXT    NOT NULL CHECK (slot IN ('user','agent')),   -- 轮询所用令牌的槽位
    etag_issue           TEXT,
    etag_comments        TEXT,
    etag_reviews         TEXT,
    etag_review_comments TEXT,
    etag_checks          TEXT,
    last_seen_at         INTEGER,              -- 评论类接口的 since 游标
    done_at              INTEGER,              -- 对象进入终态的时间；满 24 小时后退场
    PRIMARY KEY (kind, full_name, number)
);
CREATE INDEX github_watches_repo ON github_watches (repo_id, number);
CREATE INDEX github_watches_thread ON github_watches (thread_id);

-- 自身写入的预登记（防回流）：target 为 REST 规范化路径或 GraphQL input 中的目标 node id；无正文的动作 body_sha256 为 NULL。
CREATE TABLE github_self_writes (
    id          INTEGER PRIMARY KEY,
    slot        TEXT    NOT NULL CHECK (slot IN ('user','agent')),
    category    TEXT    NOT NULL,
    target      TEXT    NOT NULL,
    body_sha256 TEXT,
    created_at  INTEGER NOT NULL
);
CREATE INDEX github_self_writes_target ON github_self_writes (target, category);

-- 自身写入的补充键：REST 响应中的 id、node_id 与 html_url 锚点。
CREATE TABLE github_self_keys (
    key        TEXT    PRIMARY KEY,
    created_at INTEGER NOT NULL
);

-- 出站代理记录的 orb 写操作。无外键：行比线程与 orb 活得久，保留 30 天。
CREATE TABLE github_audit (
    id          INTEGER PRIMARY KEY,
    at          INTEGER NOT NULL,
    thread_id   TEXT,
    orb_id      TEXT    NOT NULL,
    slot        TEXT    NOT NULL CHECK (slot IN ('user','agent')),
    method      TEXT    NOT NULL,              -- HTTP 方法；GraphQL 为 POST；git 推送为 PUSH
    op          TEXT    NOT NULL,              -- REST 路径模板、mutation 名或 ref；仓库取不到时附带 node id
    repo        TEXT    NOT NULL DEFAULT '',   -- owner/repo；取不到时为空串
    status      INTEGER NOT NULL,
    gate        TEXT    NOT NULL DEFAULT '',   -- 门禁结论；未经门禁为空串
    body_sha256 TEXT
);
CREATE INDEX github_audit_thread ON github_audit (thread_id, at);
CREATE INDEX github_audit_repo ON github_audit (repo, at);

-- github_prepare 的通过记录（R20）。
CREATE TABLE github_prepare (
    sha     TEXT    NOT NULL,
    orb_id  TEXT    NOT NULL,
    level   TEXT    NOT NULL CHECK (level IN ('push','pr')),
    skipped BOOLEAN NOT NULL DEFAULT 0,        -- skip_checks=true 产生的记录
    reason  TEXT,
    at      INTEGER NOT NULL,
    PRIMARY KEY (sha, orb_id, level)
);

-- 问题自动关闭的排期（R18），每个 issue 至多一条。
CREATE TABLE github_autoclose (
    repo_id    INTEGER NOT NULL,
    number     INTEGER NOT NULL,
    thread_id  TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    comment_id INTEGER NOT NULL,               -- 线程身份在该 issue 上的最近一条评论
    author     TEXT    NOT NULL,               -- issue 作者登录名：只认此人的 👎
    due_at     INTEGER NOT NULL,
    state      TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','claimed','done','cancelled')),
    outcome    TEXT,
    PRIMARY KEY (repo_id, number)
);
CREATE INDEX github_autoclose_due ON github_autoclose (state, due_at);

-- 发布哨兵（R22），每个仓库的每个版本一行。
CREATE TABLE github_releases (
    repo_id    INTEGER NOT NULL,
    version    TEXT    NOT NULL,
    sha        TEXT    NOT NULL,               -- bump 提交
    tag_oid    TEXT,                           -- 事件中 tag v<version> 的 oid
    thread_id  TEXT    REFERENCES threads (id) ON DELETE SET NULL,
    state      TEXT    NOT NULL CHECK (state IN ('awaiting_ci','fixing','green','failed','superseded')),
    rounds     INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (repo_id, version)
);
CREATE INDEX github_releases_thread ON github_releases (thread_id);

-- 「任何人」档的按发送者限流（R13）。无外键：随规则删除后的残行按时间清理。
CREATE TABLE github_sender_runs (
    rule_id TEXT    NOT NULL,
    sender  TEXT    NOT NULL,
    at      INTEGER NOT NULL
);
CREATE INDEX github_sender_runs_key ON github_sender_runs (rule_id, sender, at);

ALTER TABLE github_identities ADD COLUMN scopes TEXT NOT NULL DEFAULT '';   -- X-OAuth-Scopes 原文
ALTER TABLE projects ADD COLUMN github_bind_status TEXT CHECK (github_bind_status IN ('not_found','org_restricted','sso_required'));
ALTER TABLE projects ADD COLUMN github_gates TEXT;   -- JSON {push, pr, fixCmd, checkCmd, testCmd, prHeadings[], requireClosingRef}；NULL = 关闭
ALTER TABLE thread_issues ADD COLUMN state TEXT NOT NULL DEFAULT 'new'
    CHECK (state IN ('new','reproducing','fixing','reviewing','opened','needs_info','merged','closed','abandoned'));
ALTER TABLE thread_issues ADD COLUMN classification TEXT;
ALTER TABLE thread_issues ADD COLUMN impl_authorized BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE thread_issues ADD COLUMN review_only BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE thread_issues ADD COLUMN rule_id TEXT;
ALTER TABLE thread_issues ADD COLUMN deadline_at INTEGER;
ALTER TABLE thread_issues ADD COLUMN reminders INTEGER NOT NULL DEFAULT 0;
ALTER TABLE thread_pulls ADD COLUMN created_via_egress BOOLEAN NOT NULL DEFAULT 0;

-- github_deliveries：source 增加 'rest'；payload 保留到清理时（replay 需要）。
-- github_triggers：event 增加三个新事件，并加规则选项列。
-- SQLite 不能改 CHECK，只能重建。库以 foreign_keys = ON 打开、迁移包在事务里，照 0018 先搬进无约束的暂存表，
-- 不用 RENAME TO。两张表都没有子表引用。
CREATE TABLE github_deliveries_keep (
    guid TEXT, event TEXT, source TEXT, payload BLOB, outcome TEXT, attempts INTEGER, received_at INTEGER
);
INSERT INTO github_deliveries_keep (guid, event, source, payload, outcome, attempts, received_at)
SELECT guid, event, source, payload, outcome, attempts, received_at FROM github_deliveries;
DROP TABLE github_deliveries;

CREATE TABLE github_deliveries (
    guid        TEXT    PRIMARY KEY,           -- X-GitHub-Delivery；REST 合成事件为 rest:<kind>:<full>:<id>:<updated_at>
    event       TEXT    NOT NULL,
    source      TEXT    NOT NULL CHECK (source IN ('webhook','poll','rest')),
    payload     BLOB,                          -- 保留到按时间清理
    outcome     TEXT    NOT NULL DEFAULT 'pending',  -- pending | handled | ignored:<reason> | error:<msg>
    attempts    INTEGER NOT NULL DEFAULT 0,
    received_at INTEGER NOT NULL
);
CREATE INDEX github_deliveries_received ON github_deliveries (received_at);
INSERT INTO github_deliveries (guid, event, source, payload, outcome, attempts, received_at)
SELECT guid, event, source, payload, outcome, attempts, received_at FROM github_deliveries_keep;
DROP TABLE github_deliveries_keep;

CREATE TABLE github_triggers_keep (
    id TEXT, project_id TEXT, name TEXT, event TEXT, label TEXT, prompt TEXT, enabled BOOLEAN, announce BOOLEAN,
    run_count INTEGER, last_run_at INTEGER, created_at INTEGER, updated_at INTEGER
);
INSERT INTO github_triggers_keep (id, project_id, name, event, label, prompt, enabled, announce, run_count, last_run_at, created_at, updated_at)
SELECT id, project_id, name, event, label, prompt, enabled, announce, run_count, last_run_at, created_at, updated_at FROM github_triggers;
DROP TABLE github_triggers;

CREATE TABLE github_triggers (
    id                   TEXT    PRIMARY KEY,  -- ght_<ULID>
    project_id           TEXT    NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name                 TEXT    NOT NULL,
    event                TEXT    NOT NULL CHECK (event IN ('issue_opened','issue_reopened','issue_labeled','mention',
                                                           'pull_request_opened','workflow_run_completed')),
    label                TEXT,
    prompt               TEXT    NOT NULL,
    enabled              BOOLEAN NOT NULL DEFAULT 1,
    announce             BOOLEAN NOT NULL DEFAULT 1,
    run_count            INTEGER NOT NULL DEFAULT 0,
    last_run_at          INTEGER,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    drivers              TEXT    NOT NULL DEFAULT 'writers_and_author' CHECK (drivers IN ('writers','writers_and_author','anyone')),
    maintainers          TEXT    NOT NULL DEFAULT '[]',   -- JSON 登录名数组：规则维护者
    reviewer_bots        TEXT    NOT NULL DEFAULT '[]',   -- JSON 登录名数组：发言视为维护者指令的评审机器人
    models               TEXT    NOT NULL DEFAULT '[]',   -- JSON 模型数组：模型池，随机取一个；空 = 默认模型
    thinking             TEXT,                            -- 思考级别；NULL = 默认
    impl_auth            BOOLEAN NOT NULL DEFAULT 0,      -- 实现需授权（R16）
    comment_only_reviews BOOLEAN NOT NULL DEFAULT 0,      -- 只发 COMMENT（R21）
    autoclose_hours      INTEGER,                         -- NULL = defaultAutocloseHours
    max_duration_sec     INTEGER,                         -- 任务时限；NULL = 不限
    completion_reminders BOOLEAN NOT NULL DEFAULT 0,      -- 完成提醒（R25）
    release_prefix       TEXT                             -- 发布哨兵的 head 提交信息前缀；NULL = 'chore: bump version to '
);
CREATE INDEX github_triggers_project ON github_triggers (project_id, created_at);
INSERT INTO github_triggers (id, project_id, name, event, label, prompt, enabled, announce, run_count, last_run_at, created_at, updated_at)
SELECT id, project_id, name, event, label, prompt, enabled, announce, run_count, last_run_at, created_at, updated_at FROM github_triggers_keep;
DROP TABLE github_triggers_keep;

-- 自动化任务的 GitHub 身份默认改为 agent，现有设置一并改为 agent（R6）。
ALTER TABLE owner DROP COLUMN github_automation_identity;
ALTER TABLE owner ADD COLUMN github_automation_identity TEXT NOT NULL DEFAULT 'agent' CHECK (github_automation_identity IN ('user','agent'));

-- +goose Down
ALTER TABLE owner DROP COLUMN github_automation_identity;
ALTER TABLE owner ADD COLUMN github_automation_identity TEXT NOT NULL DEFAULT 'user' CHECK (github_automation_identity IN ('user','agent'));

-- 按 0028_github.sql 原样重建两张表；新事件的规则、rest 来源的投递无法表示，先删掉。
CREATE TABLE github_triggers_keep (
    id TEXT, project_id TEXT, name TEXT, event TEXT, label TEXT, prompt TEXT, enabled BOOLEAN, announce BOOLEAN,
    run_count INTEGER, last_run_at INTEGER, created_at INTEGER, updated_at INTEGER
);
INSERT INTO github_triggers_keep (id, project_id, name, event, label, prompt, enabled, announce, run_count, last_run_at, created_at, updated_at)
SELECT id, project_id, name, event, label, prompt, enabled, announce, run_count, last_run_at, created_at, updated_at
FROM github_triggers WHERE event IN ('issue_opened','issue_labeled','mention');
DROP TABLE github_triggers;

CREATE TABLE github_triggers (
    id          TEXT    PRIMARY KEY,           -- ght_<ULID>
    project_id  TEXT    NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    event       TEXT    NOT NULL CHECK (event IN ('issue_opened','issue_labeled','mention')),
    label       TEXT,
    prompt      TEXT    NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT 1,
    announce    BOOLEAN NOT NULL DEFAULT 1,
    run_count   INTEGER NOT NULL DEFAULT 0,
    last_run_at INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX github_triggers_project ON github_triggers (project_id, created_at);
INSERT INTO github_triggers (id, project_id, name, event, label, prompt, enabled, announce, run_count, last_run_at, created_at, updated_at)
SELECT id, project_id, name, event, label, prompt, enabled, announce, run_count, last_run_at, created_at, updated_at FROM github_triggers_keep;
DROP TABLE github_triggers_keep;

CREATE TABLE github_deliveries_keep (
    guid TEXT, event TEXT, source TEXT, payload BLOB, outcome TEXT, attempts INTEGER, received_at INTEGER
);
INSERT INTO github_deliveries_keep (guid, event, source, payload, outcome, attempts, received_at)
SELECT guid, event, source, payload, outcome, attempts, received_at FROM github_deliveries WHERE source <> 'rest';
DROP TABLE github_deliveries;

CREATE TABLE github_deliveries (
    guid        TEXT    PRIMARY KEY,           -- X-GitHub-Delivery
    event       TEXT    NOT NULL,
    source      TEXT    NOT NULL CHECK (source IN ('webhook','poll')),
    payload     BLOB,                          -- outcome='pending' 时保留，处理完置 NULL
    outcome     TEXT    NOT NULL DEFAULT 'pending',  -- pending | handled | ignored:<reason> | error:<msg>
    attempts    INTEGER NOT NULL DEFAULT 0,
    received_at INTEGER NOT NULL
);
CREATE INDEX github_deliveries_received ON github_deliveries (received_at);
INSERT INTO github_deliveries (guid, event, source, payload, outcome, attempts, received_at)
SELECT guid, event, source, payload, outcome, attempts, received_at FROM github_deliveries_keep;
DROP TABLE github_deliveries_keep;

ALTER TABLE thread_pulls DROP COLUMN created_via_egress;
ALTER TABLE thread_issues DROP COLUMN reminders;
ALTER TABLE thread_issues DROP COLUMN deadline_at;
ALTER TABLE thread_issues DROP COLUMN rule_id;
ALTER TABLE thread_issues DROP COLUMN review_only;
ALTER TABLE thread_issues DROP COLUMN impl_authorized;
ALTER TABLE thread_issues DROP COLUMN classification;
ALTER TABLE thread_issues DROP COLUMN state;
ALTER TABLE projects DROP COLUMN github_gates;
ALTER TABLE projects DROP COLUMN github_bind_status;
ALTER TABLE github_identities DROP COLUMN scopes;

DROP INDEX github_sender_runs_key;
DROP TABLE github_sender_runs;
DROP INDEX github_releases_thread;
DROP TABLE github_releases;
DROP INDEX github_autoclose_due;
DROP TABLE github_autoclose;
DROP TABLE github_prepare;
DROP INDEX github_audit_repo;
DROP INDEX github_audit_thread;
DROP TABLE github_audit;
DROP TABLE github_self_keys;
DROP INDEX github_self_writes_target;
DROP TABLE github_self_writes;
DROP INDEX github_watches_thread;
DROP INDEX github_watches_repo;
DROP TABLE github_watches;
DROP TABLE github_repo_watches;
DROP INDEX github_hooks_hook;
DROP TABLE github_hooks;
DROP TABLE github_oauth_app;
