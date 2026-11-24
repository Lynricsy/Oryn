-- Entries and replication cursors (plan section D, entry ingestion rules).

-- Upsert by (thread_id, file, entry_id): new rows take threads.last_seq + 1 (trigger then bumps last_seq);
-- existing rows keep their seq and clear stale. Returns the row's seq.
-- name: UpsertEntry :one
INSERT INTO entries (thread_id, seq, file, entry_id, parent_id, type, ts, payload, stale)
VALUES (@thread_id, (SELECT t.last_seq + 1 FROM threads t WHERE t.id = @thread_id),
        @file, @entry_id, @parent_id, @type, @ts, @payload, 0)
ON CONFLICT (thread_id, file, entry_id) DO UPDATE SET
    parent_id = excluded.parent_id,
    type      = excluded.type,
    ts        = excluded.ts,
    payload   = excluded.payload,
    stale     = 0
RETURNING seq;

-- entries.reset: mark the file's existing rows stale.
-- name: MarkEntriesStale :execrows
UPDATE entries SET stale = 1 WHERE thread_id = @thread_id AND file = @file;

-- entries.reset_done: delete rows still stale.
-- name: DeleteStaleEntries :execrows
DELETE FROM entries WHERE thread_id = @thread_id AND file = @file AND stale = 1;

-- name: ListEntriesAfter :many
SELECT * FROM entries WHERE thread_id = @thread_id AND seq > @after_seq ORDER BY seq LIMIT @limit;

-- Earlier page (M19): rows before before_seq, newest first; the service reverses them.
-- name: ListEntriesBefore :many
SELECT * FROM entries WHERE thread_id = @thread_id AND seq < @before_seq ORDER BY seq DESC LIMIT @limit;

-- Tail window start (M19): the smallest seq among the newest n rows minus one; 0 when the thread has fewer than n rows.
-- name: GetTailStartSeq :one
SELECT CAST(CASE WHEN COUNT(*) < CAST(@n AS INTEGER) THEN 0 ELSE MIN(seq) - 1 END AS INTEGER) AS start_seq
FROM (SELECT seq FROM entries WHERE thread_id = @thread_id ORDER BY seq DESC LIMIT CAST(@n AS INTEGER));

-- name: GetEntry :one
SELECT * FROM entries WHERE thread_id = @thread_id AND file = @file AND entry_id = @entry_id;

-- name: UpsertEntryCursor :exec
INSERT INTO entry_cursors (thread_id, file, byte_offset) VALUES (@thread_id, @file, @byte_offset)
ON CONFLICT (thread_id, file) DO UPDATE SET byte_offset = excluded.byte_offset;

-- name: GetEntryCursor :one
SELECT * FROM entry_cursors WHERE thread_id = @thread_id AND file = @file;

-- name: ListEntryCursors :many
SELECT * FROM entry_cursors WHERE thread_id = @thread_id ORDER BY file;

-- name: DeleteEntryCursor :exec
DELETE FROM entry_cursors WHERE thread_id = @thread_id AND file = @file;

-- Latest main-session entry that affects model context (used to decide whether main/research startup needs clear_context).
-- name: GetLatestMainContextEntry :one
SELECT seq, type
FROM entries
WHERE thread_id = @thread_id
  AND instr(file, '/') = 0
  AND type IN ('message', 'custom_message', 'branch_summary', 'compaction', 'reset_boundary')
ORDER BY seq DESC
LIMIT 1;

-- Sequence of an entry in the thread's main session files (for checking whether leaf jumped before the latest reset_boundary).
-- name: GetMainEntrySeq :one
SELECT seq
FROM entries
WHERE thread_id = @thread_id
  AND instr(file, '/') = 0
  AND entry_id = @entry_id
ORDER BY seq DESC
LIMIT 1;
