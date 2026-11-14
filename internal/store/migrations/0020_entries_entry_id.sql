-- +goose Up
-- 按条目 id 跨线程查找：删掉一条事实行的所属条目时，同一去重键（ukey = 条目 id + 时间）若还有别的当前条目
-- （同线程另一个文件，或 fork 拷贝了它的活线程），要把事实重建到那份副本上（threads.restoreFacts）。
CREATE INDEX entries_entry_id ON entries (entry_id);

-- +goose Down
DROP INDEX entries_entry_id;
