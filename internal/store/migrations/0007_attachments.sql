-- +goose Up
-- 消息附件（M3）：字节存 <data_dir>/attachments/<id>，不进转录 blob、不进仓库。
--   thread_id 为 NULL：新建线程前上传、尚未绑定；创建超过 24h 仍未绑定的由附件服务清理。
CREATE TABLE attachments (
    id         TEXT    PRIMARY KEY,                                  -- att_<ULID>，ids.New("att")
    thread_id  TEXT    REFERENCES threads (id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    mime       TEXT    NOT NULL DEFAULT '',
    size       INTEGER NOT NULL,
    sha256     TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX attachments_thread ON attachments (thread_id, created_at);

-- +goose Down
DROP TABLE attachments;
