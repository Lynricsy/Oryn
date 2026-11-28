-- +goose Up
-- 写作块的用户改稿：按 (线程, 助手消息条目, 块键) 存最新全文；delivered_at = 已随某条用户消息交给 agent 的那一版的 updated_at。
CREATE TABLE thread_drafts (
    thread_id    TEXT    NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    entry_id     TEXT    NOT NULL,
    block        TEXT    NOT NULL,
    kind         TEXT    NOT NULL CHECK (kind IN ('email', 'message', 'doc')),
    title        TEXT    NOT NULL DEFAULT '',
    text         TEXT    NOT NULL,
    updated_at   INTEGER NOT NULL,
    delivered_at INTEGER,
    PRIMARY KEY (thread_id, entry_id, block)
);

-- +goose Down
DROP TABLE thread_drafts;
