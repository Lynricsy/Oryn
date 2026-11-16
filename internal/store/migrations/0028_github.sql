-- +goose Up
-- M14：GitHub 深度集成。敏感值（私钥、client secret、webhook secret、用户令牌）经 secrets.Box 加密后存进本表列，
-- 不进 secrets 表（不出现在「密钥」设置页）；注释里的 AAD 名称与 internal/github 的 Seal / Open 调用一致。

-- GitHub App（单行）。
CREATE TABLE github_app (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    app_id             INTEGER NOT NULL,
    slug               TEXT    NOT NULL,
    name               TEXT    NOT NULL,
    client_id          TEXT    NOT NULL,
    owner_login        TEXT    NOT NULL,
    html_url           TEXT    NOT NULL,
    public             BOOLEAN NOT NULL DEFAULT 0,
    webhook_url        TEXT    NOT NULL,
    private_key_enc    BLOB    NOT NULL,       -- AAD "github/app/private-key"
    client_secret_enc  BLOB    NOT NULL,       -- AAD "github/app/client-secret"
    webhook_secret_enc BLOB    NOT NULL,       -- AAD "github/app/webhook-secret"
    hook_cursor        INTEGER NOT NULL DEFAULT 0,  -- 轮询已处理到的投递 id；0 = 尚未建立基线
    last_delivery_at   INTEGER,
    last_delivery_src  TEXT    CHECK (last_delivery_src IN ('webhook','poll')),
    created_at         INTEGER NOT NULL
);

-- 两个身份槽位：user = 我的账号，agent = Agent 账号。
CREATE TABLE github_identities (
    slot               TEXT    PRIMARY KEY CHECK (slot IN ('user','agent')),
    github_id          INTEGER NOT NULL UNIQUE,
    login              TEXT    NOT NULL,
    name               TEXT,
    avatar_url         TEXT    NOT NULL DEFAULT '',
    access_token_enc   BLOB    NOT NULL,       -- AAD "github/identity/<slot>/access"
    access_expires_at  INTEGER,                -- NULL = 不过期
    refresh_token_enc  BLOB,                   -- AAD "github/identity/<slot>/refresh"
    refresh_expires_at INTEGER,
    status             TEXT    NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','reauth')),
    connected_at       INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

-- manifest 创建与身份授权的一次性 state。
CREATE TABLE github_oauth_states (
    state_hash  TEXT    PRIMARY KEY,           -- ids.Hash(state)
    kind        TEXT    NOT NULL CHECK (kind IN ('manifest','identity')),
    slot        TEXT    CHECK (slot IN ('user','agent')),   -- kind=identity
    manifest    TEXT,                          -- kind=manifest：待提交的 manifest JSON
    action_url  TEXT,                          -- kind=manifest：GitHub 表单地址
    public      BOOLEAN NOT NULL DEFAULT 0,
    webhook_url TEXT,
    expires_at  INTEGER NOT NULL
);

CREATE TABLE github_installations (
    id                   INTEGER PRIMARY KEY,
    account_login        TEXT    NOT NULL,
    account_type         TEXT    NOT NULL,
    repository_selection TEXT    NOT NULL,
    suspended            BOOLEAN NOT NULL DEFAULT 0,
    updated_at           INTEGER NOT NULL
);

ALTER TABLE owner    ADD COLUMN github_automation_identity TEXT NOT NULL DEFAULT 'user' CHECK (github_automation_identity IN ('user','agent'));
ALTER TABLE orbs     ADD COLUMN github_identity TEXT NOT NULL DEFAULT 'user' CHECK (github_identity IN ('user','agent'));
ALTER TABLE projects ADD COLUMN github_repo_id INTEGER;
ALTER TABLE projects ADD COLUMN github_owner_id INTEGER;
ALTER TABLE projects ADD COLUMN github_installation_id INTEGER;
ALTER TABLE projects ADD COLUMN github_prev_urls TEXT NOT NULL DEFAULT '[]';   -- JSON 字符串数组，最多 5 个
ALTER TABLE projects ADD COLUMN github_listen_reviews  BOOLEAN NOT NULL DEFAULT 1;
ALTER TABLE projects ADD COLUMN github_listen_comments BOOLEAN NOT NULL DEFAULT 1;
ALTER TABLE projects ADD COLUMN github_listen_ci       BOOLEAN NOT NULL DEFAULT 1;
CREATE INDEX projects_github_repo ON projects (github_repo_id);

-- 已受理的投递（webhook 与轮询共用，按 guid 去重）。
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

CREATE TABLE github_pulls (
    repo_id        INTEGER NOT NULL,
    number         INTEGER NOT NULL,
    repo_full_name TEXT    NOT NULL,
    title          TEXT    NOT NULL,
    state          TEXT    NOT NULL CHECK (state IN ('open','closed','merged')),
    draft          BOOLEAN NOT NULL,
    same_repo      BOOLEAN NOT NULL,           -- head.repo.id == base.repo.id
    head_ref       TEXT    NOT NULL,
    head_sha       TEXT    NOT NULL,
    base_ref       TEXT    NOT NULL,
    author_login   TEXT    NOT NULL,
    html_url       TEXT    NOT NULL,
    checks         TEXT    CHECK (checks IN ('pending','success','failure')),
    checks_sha     TEXT,
    merged_at      INTEGER,
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (repo_id, number)
);
CREATE INDEX github_pulls_head ON github_pulls (repo_id, head_sha);

CREATE TABLE thread_pulls (
    thread_id TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    repo_id   INTEGER NOT NULL,
    number    INTEGER NOT NULL,
    listening BOOLEAN NOT NULL DEFAULT 1,
    unlinked  BOOLEAN NOT NULL DEFAULT 0,      -- 用户取消关联后的墓碑，阻止自动重连
    ci_rounds INTEGER NOT NULL DEFAULT 0,
    linked_at INTEGER NOT NULL,
    PRIMARY KEY (thread_id, repo_id, number)
);
CREATE INDEX thread_pulls_pull ON thread_pulls (repo_id, number);

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

-- 触发规则建立的 issue / PR ↔ 线程。
CREATE TABLE thread_issues (
    repo_id    INTEGER NOT NULL,
    number     INTEGER NOT NULL,
    thread_id  TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    trigger_id TEXT,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (repo_id, number)
);
CREATE INDEX thread_issues_trigger ON thread_issues (trigger_id, created_at);

-- 待回流到线程的 GitHub 事件（同一 PR / issue 防抖窗口内合成一条自动消息）。
CREATE TABLE github_outbox (
    id            TEXT    PRIMARY KEY,         -- gho_<ULID>
    thread_id     TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    repo_id       INTEGER NOT NULL,
    number        INTEGER NOT NULL,
    is_pull       BOOLEAN NOT NULL,
    title         TEXT    NOT NULL,            -- 入队时载荷里的 PR / issue 标题
    url           TEXT    NOT NULL,            -- 入队时载荷里的 html_url
    kind          TEXT    NOT NULL CHECK (kind IN ('review','review_comment','comment','ci')),
    item          TEXT    NOT NULL,            -- JSON（internal/github 的 outboxItem）
    created_at    INTEGER NOT NULL,
    deliver_after INTEGER NOT NULL,
    delivered_at  INTEGER
);
CREATE INDEX github_outbox_due ON github_outbox (delivered_at, deliver_after);
CREATE INDEX thread_commits_sha ON thread_commits (sha);

-- +goose Down
DROP INDEX thread_commits_sha;
DROP INDEX github_outbox_due;
DROP TABLE github_outbox;
DROP INDEX thread_issues_trigger;
DROP TABLE thread_issues;
DROP INDEX github_triggers_project;
DROP TABLE github_triggers;
DROP INDEX thread_pulls_pull;
DROP TABLE thread_pulls;
DROP INDEX github_pulls_head;
DROP TABLE github_pulls;
DROP INDEX github_deliveries_received;
DROP TABLE github_deliveries;
DROP INDEX projects_github_repo;
ALTER TABLE projects DROP COLUMN github_listen_ci;
ALTER TABLE projects DROP COLUMN github_listen_comments;
ALTER TABLE projects DROP COLUMN github_listen_reviews;
ALTER TABLE projects DROP COLUMN github_prev_urls;
ALTER TABLE projects DROP COLUMN github_installation_id;
ALTER TABLE projects DROP COLUMN github_owner_id;
ALTER TABLE projects DROP COLUMN github_repo_id;
ALTER TABLE orbs     DROP COLUMN github_identity;
ALTER TABLE owner    DROP COLUMN github_automation_identity;
DROP TABLE github_installations;
DROP TABLE github_oauth_states;
DROP TABLE github_identities;
DROP TABLE github_app;
