-- +goose Up
-- M15 步骤 1.5：删除 GitHub App 的表与列（破坏性，只能向前）。
-- 旧 App 签发的用户令牌一并作废：两个身份槽位都要按 OAuth App 重新连接（R3）。项目的 github_repo_id、触发规则、
-- PR 关联与 thread_issues 保留。
DROP TABLE github_installations;
DROP TABLE github_app;

DELETE FROM github_identities;
ALTER TABLE github_identities DROP COLUMN access_expires_at;
ALTER TABLE github_identities DROP COLUMN refresh_token_enc;
ALTER TABLE github_identities DROP COLUMN refresh_expires_at;

ALTER TABLE projects DROP COLUMN github_installation_id;
ALTER TABLE projects DROP COLUMN github_owner_id;

-- github_oauth_states：去掉 manifest 相关列，kind 只剩 identity。照 0018 先搬进无约束的暂存表，不用 RENAME TO；
-- 没有子表引用。manifest 的 state 直接丢弃。
CREATE TABLE github_oauth_states_keep (state_hash TEXT, kind TEXT, slot TEXT, expires_at INTEGER);
INSERT INTO github_oauth_states_keep (state_hash, kind, slot, expires_at)
SELECT state_hash, kind, slot, expires_at FROM github_oauth_states WHERE kind = 'identity' AND slot IS NOT NULL;
DROP TABLE github_oauth_states;

-- 身份授权的一次性 state。
CREATE TABLE github_oauth_states (
    state_hash TEXT    PRIMARY KEY,            -- ids.Hash(state)
    kind       TEXT    NOT NULL CHECK (kind IN ('identity')),
    slot       TEXT    NOT NULL CHECK (slot IN ('user','agent')),
    expires_at INTEGER NOT NULL
);
INSERT INTO github_oauth_states (state_hash, kind, slot, expires_at)
SELECT state_hash, kind, slot, expires_at FROM github_oauth_states_keep;
DROP TABLE github_oauth_states_keep;

-- +goose Down
-- 只恢复表结构：App、安装、项目的安装绑定与身份的刷新令牌都已删除，数据不可恢复。
CREATE TABLE github_oauth_states_keep (state_hash TEXT, kind TEXT, slot TEXT, expires_at INTEGER);
INSERT INTO github_oauth_states_keep (state_hash, kind, slot, expires_at)
SELECT state_hash, kind, slot, expires_at FROM github_oauth_states;
DROP TABLE github_oauth_states;

CREATE TABLE github_oauth_states (
    state_hash  TEXT    PRIMARY KEY,
    kind        TEXT    NOT NULL CHECK (kind IN ('manifest','identity')),
    slot        TEXT    CHECK (slot IN ('user','agent')),
    manifest    TEXT,
    action_url  TEXT,
    public      BOOLEAN NOT NULL DEFAULT 0,
    webhook_url TEXT,
    expires_at  INTEGER NOT NULL
);
INSERT INTO github_oauth_states (state_hash, kind, slot, expires_at)
SELECT state_hash, kind, slot, expires_at FROM github_oauth_states_keep;
DROP TABLE github_oauth_states_keep;

ALTER TABLE projects ADD COLUMN github_owner_id INTEGER;
ALTER TABLE projects ADD COLUMN github_installation_id INTEGER;

ALTER TABLE github_identities ADD COLUMN access_expires_at INTEGER;
ALTER TABLE github_identities ADD COLUMN refresh_token_enc BLOB;
ALTER TABLE github_identities ADD COLUMN refresh_expires_at INTEGER;

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
    private_key_enc    BLOB    NOT NULL,
    client_secret_enc  BLOB    NOT NULL,
    webhook_secret_enc BLOB    NOT NULL,
    hook_cursor        INTEGER NOT NULL DEFAULT 0,
    last_delivery_at   INTEGER,
    last_delivery_src  TEXT    CHECK (last_delivery_src IN ('webhook','poll')),
    created_at         INTEGER NOT NULL
);

CREATE TABLE github_installations (
    id                   INTEGER PRIMARY KEY,
    account_login        TEXT    NOT NULL,
    account_type         TEXT    NOT NULL,
    repository_selection TEXT    NOT NULL,
    suspended            BOOLEAN NOT NULL DEFAULT 0,
    updated_at           INTEGER NOT NULL
);
