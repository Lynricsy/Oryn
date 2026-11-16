-- +goose Up
-- M14：artifact 的评论。评论跟 artifact 走，不属于某个版本；锚点在每次读取时按最新版本重新定位。
--   anchor：锚点 JSON {version, path, page, offset, tag, text, missing}。
--   resolved_at：标为已解决的时间；NULL = 未解决。
--   sent_at / sent_to：最近一次发给了哪个线程；用户再回复时清掉。sent_to 不设外键（线程删除后保留原值，只作展示）。
CREATE TABLE artifact_comment_threads (
    id          TEXT    PRIMARY KEY,                                    -- act_<ULID>
    artifact_id TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    anchor      TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    resolved_at INTEGER,
    sent_at     INTEGER,
    sent_to     TEXT
);
CREATE INDEX artifact_comment_threads_artifact ON artifact_comment_threads (artifact_id, created_at);

-- 串里的每条评论（n 从 1 递增）。author_thread_id NULL = 用户；author_label 是写入时的线程标题快照。
CREATE TABLE artifact_comments (
    thread_id        TEXT    NOT NULL REFERENCES artifact_comment_threads (id) ON DELETE CASCADE,
    n                INTEGER NOT NULL,
    author_thread_id TEXT,
    author_label     TEXT    NOT NULL DEFAULT '',
    body             TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    PRIMARY KEY (thread_id, n)
) WITHOUT ROWID;

-- +goose Down
DROP TABLE artifact_comments;
DROP TABLE artifact_comment_threads;
