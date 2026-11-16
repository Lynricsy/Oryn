-- +goose Up
-- M14：artifact 发布。一个 artifact 至多一条发布记录；取消发布就是删行，链接随即失效。
--   slug：发布地址里的随机标识（16 字节小写无填充 base32）。
--   access：private（只有登录的所有者）| code（链接 + 访问码）| public（链接）。
--   code / code_gen：访问码与它的代数；换码或改访问级别时 code_gen 加一，已发出的授权随即失效。
--   code_failures / code_locked：连续输错次数；到 10 次锁定，换码才解锁。
--   version / follow / pending_version：发布出去的版本；follow 为 1 时随新版本推进，遇到疑似密钥停在 pending_version 等确认。
CREATE TABLE artifact_publications (
    id              TEXT    PRIMARY KEY,                                -- apb_<ULID>
    artifact_id     TEXT    NOT NULL UNIQUE REFERENCES artifacts (id) ON DELETE CASCADE,
    slug            TEXT    NOT NULL UNIQUE,
    access          TEXT    NOT NULL CHECK (access IN ('private', 'code', 'public')),
    code            TEXT,
    code_gen        INTEGER NOT NULL DEFAULT 0,
    code_failures   INTEGER NOT NULL DEFAULT 0,
    code_locked     INTEGER NOT NULL DEFAULT 0,
    version         INTEGER NOT NULL,
    follow          INTEGER NOT NULL DEFAULT 1,
    pending_version INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- 新建的 artifact 是否自动以私有方式发布并跟随最新版本（缺省关闭）。
ALTER TABLE owner ADD COLUMN auto_publish_artifacts INTEGER NOT NULL DEFAULT 0 CHECK (auto_publish_artifacts IN (0, 1));

-- +goose Down
ALTER TABLE owner DROP COLUMN auto_publish_artifacts;
DROP TABLE artifact_publications;
