-- +goose Up
-- 对话内容全文搜索：主会话（file 不含 '/'）里未作废（stale = 0）的 user / assistant 消息条目，取 content 里全部 text 块
-- （content 为字符串时即该串）以换行连接为正文。thinking、工具调用与工具结果不进索引。
--
-- search_source 是「条目 → 可索引正文」的唯一定义：入库触发器与后台回填都从它取，口径一致。
-- payload 先 CAST 成 TEXT 再交给 JSON 函数（BLOB 会被当成 JSONB 解析）；不是合法 JSON 的行按 '{}' 处理，不让坏行挡住条目入库。
CREATE VIEW search_source AS
SELECT thread_id, seq, entry_id, ts, role, body FROM (
    SELECT thread_id, seq, entry_id, ts,
           json_extract(p, '$.message.role') AS role,
           CASE json_type(p, '$.message.content')
               WHEN 'text' THEN json_extract(p, '$.message.content')
               WHEN 'array' THEN (
                   SELECT group_concat(json_extract(c.value, '$.text'), char(10))
                   FROM json_each(p, '$.message.content') c
                   WHERE json_extract(c.value, '$.type') = 'text' AND json_type(c.value, '$.text') = 'text')
           END AS body
    FROM (
        SELECT thread_id, seq, entry_id, COALESCE(ts, 0) AS ts,
               CASE WHEN json_valid(CAST(payload AS TEXT)) THEN CAST(payload AS TEXT) ELSE '{}' END AS p
        FROM entries
        WHERE type = 'message' AND stale = 0 AND instr(file, '/') = 0
    )
)
WHERE role IN ('user', 'assistant') AND body IS NOT NULL AND trim(body) <> '';

-- 索引行：id 即 search_fts 的 rowid。ts 为条目时间（毫秒，缺失为 0），兼作排序键；
-- (entry_id, ts) 是 fork 去重键（与事实行的 ukey 同义：fork 拷贝的条目 id 与时间都与源条目相同）。
-- 外键随条目删除（含删线程的级联）一并删除，再由下面的触发器删掉对应的全文行。
CREATE TABLE search_docs (
    id        INTEGER PRIMARY KEY,
    thread_id TEXT    NOT NULL,
    seq       INTEGER NOT NULL,
    entry_id  TEXT    NOT NULL,
    ts        INTEGER NOT NULL,
    role      TEXT    NOT NULL,
    UNIQUE (thread_id, seq),
    FOREIGN KEY (thread_id, seq) REFERENCES entries (thread_id, seq) ON DELETE CASCADE
);
CREATE INDEX search_docs_ts ON search_docs (ts, id);
CREATE INDEX search_docs_ukey ON search_docs (entry_id, ts);

-- trigram 分词：按 3 个 Unicode 字符切片，不依赖空格分词，中文、日文与代码标识符都能做子串匹配
-- （unicode61 把一整段汉字当成一个词，搜不到其中的词）；不区分大小写、忽略变音符。
-- 不足 3 个字符的搜索词没有 trigram 可查，由服务端改为对正文逐行 instr 过滤（见 internal/search）。
CREATE VIRTUAL TABLE search_fts USING fts5(body, tokenize = 'trigram case_sensitive 0 remove_diacritics 1');

-- 回填进度与索引版本。
CREATE TABLE search_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER search_docs_ad AFTER DELETE ON search_docs
BEGIN
    DELETE FROM search_fts WHERE rowid = OLD.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER search_entries_ai AFTER INSERT ON entries
WHEN NEW.type = 'message' AND NEW.stale = 0 AND instr(NEW.file, '/') = 0
BEGIN
    INSERT OR IGNORE INTO search_docs (thread_id, seq, entry_id, ts, role)
    SELECT thread_id, seq, entry_id, ts, role FROM search_source WHERE thread_id = NEW.thread_id AND seq = NEW.seq;
    INSERT INTO search_fts (rowid, body)
    SELECT d.id, s.body FROM search_docs d
    JOIN search_source s ON s.thread_id = d.thread_id AND s.seq = d.seq
    WHERE d.thread_id = NEW.thread_id AND d.seq = NEW.seq
      AND NOT EXISTS (SELECT 1 FROM search_fts f WHERE f.rowid = d.id);
END;
-- +goose StatementEnd

-- 重发（upsert 改写内容）、标记 stale（entries.reset）与清除 stale 都走这里：先删旧索引行，再按新值重建。
-- 内容与可索引性都没变的改写（重发同一行）跳过，不重建全文行。
-- +goose StatementBegin
CREATE TRIGGER search_entries_au AFTER UPDATE OF payload, stale, type, file ON entries
WHEN OLD.payload IS NOT NEW.payload OR OLD.stale IS NOT NEW.stale OR OLD.type IS NOT NEW.type OR OLD.file IS NOT NEW.file
BEGIN
    DELETE FROM search_docs WHERE thread_id = OLD.thread_id AND seq = OLD.seq;
    INSERT OR IGNORE INTO search_docs (thread_id, seq, entry_id, ts, role)
    SELECT thread_id, seq, entry_id, ts, role FROM search_source WHERE thread_id = NEW.thread_id AND seq = NEW.seq;
    INSERT INTO search_fts (rowid, body)
    SELECT d.id, s.body FROM search_docs d
    JOIN search_source s ON s.thread_id = d.thread_id AND s.seq = d.seq
    WHERE d.thread_id = NEW.thread_id AND d.seq = NEW.seq
      AND NOT EXISTS (SELECT 1 FROM search_fts f WHERE f.rowid = d.id);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER search_entries_au;
DROP TRIGGER search_entries_ai;
DROP TRIGGER search_docs_ad;
DROP TABLE search_state;
DROP TABLE search_fts;
DROP TABLE search_docs;
DROP VIEW search_source;
