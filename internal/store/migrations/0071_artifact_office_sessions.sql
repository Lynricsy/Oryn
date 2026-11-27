-- +goose Up
-- M32：Office 编辑会话与编辑占用。会话是一次浏览器编辑器的打开（令牌只存 sha256 十六进制）；占用是某个 artifact
-- 正在被编辑（WOPI 锁串、开编时的基准版本与自动保存的工作副本），占用期间其他写入回 locked。
CREATE TABLE artifact_office_sessions (
    id           TEXT    PRIMARY KEY,                 -- aos_<ULID>
    artifact_id  TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    token_hash   TEXT    NOT NULL UNIQUE,             -- sha256(access_token) 十六进制
    created_at   INTEGER NOT NULL,
    active_at    INTEGER NOT NULL,
    closing_at   INTEGER
);
CREATE INDEX artifact_office_sessions_artifact ON artifact_office_sessions (artifact_id);
CREATE TABLE artifact_office_holds (
    artifact_id  TEXT    PRIMARY KEY REFERENCES artifacts (id) ON DELETE CASCADE,
    lock         TEXT    NOT NULL DEFAULT '',         -- WOPI 锁串
    base_version INTEGER NOT NULL,
    draft_sha    TEXT,
    draft_size   INTEGER
);

-- +goose Down
DROP TABLE artifact_office_holds;
DROP INDEX artifact_office_sessions_artifact;
DROP TABLE artifact_office_sessions;
