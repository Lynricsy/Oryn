-- +goose Up
-- M13：全局助手文件库。字节存 <data_dir>/assistant-files/<id>，与消息附件（attachments）分开存放、分开回收。
--   scope：conversation 只对所属对话可见；shared 对所有助手对话可见。
--   thread_id：所属对话。线程是硬删除（DELETE FROM threads，连接上 foreign_keys=1），删除后这里置 NULL，
--     文件留在库里（不随线程删除）；新对话发送前上传的文件也暂为 NULL，发送时由 Bind 填上。
--   source：upload（用户上传）| assistant（助手存入）| orb（orb 内 `oryn library put`）；
--   source_thread_id：来源线程，仅作展示，不设外键（来源线程删除后保留原值）。
--   bound：0 = 上传了但还没随消息发送，创建超过 24h 由文件库服务回收；1 = 永不自动回收。
CREATE TABLE assistant_files (
    id               TEXT    PRIMARY KEY,                                  -- afl_<ULID>，ids.AssistantFile()
    name             TEXT    NOT NULL,
    mime             TEXT    NOT NULL,
    size             INTEGER NOT NULL,
    sha256           TEXT    NOT NULL,
    scope            TEXT    NOT NULL CHECK (scope IN ('conversation', 'shared')),
    thread_id        TEXT    REFERENCES threads (id) ON DELETE SET NULL,
    source           TEXT    NOT NULL CHECK (source IN ('upload', 'assistant', 'orb')),
    source_thread_id TEXT,
    bound            INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL
);
CREATE INDEX assistant_files_thread ON assistant_files (thread_id);

-- +goose Down
DROP TABLE assistant_files;
