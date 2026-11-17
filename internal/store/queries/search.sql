-- Conversation full-text search (0027_search.sql). Index rows are maintained by triggers on entries;
-- this file holds the backfill and state queries (the search itself is built in internal/search).

-- name: GetSearchState :one
SELECT value FROM search_state WHERE key = @key;

-- name: SetSearchState :exec
INSERT INTO search_state (key, value) VALUES (@key, @value)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- name: DeleteSearchState :exec
DELETE FROM search_state WHERE key = @key;

-- Threads to backfill, id ascending, from the cursor thread on (inclusive: it may be half done).
-- name: ListSearchBackfillThreads :many
SELECT id, last_seq FROM threads WHERE id >= @from_id ORDER BY id LIMIT @limit;

-- Index the indexable entries of one thread with seq in (from_seq, to_seq]; rows already written by the triggers are skipped.
-- name: BackfillSearchDocs :exec
INSERT OR IGNORE INTO search_docs (thread_id, seq, entry_id, ts, role)
SELECT s.thread_id, s.seq, s.entry_id, s.ts, s.role FROM search_source s
WHERE s.thread_id = @thread_id AND s.seq > @from_seq AND s.seq <= @to_seq;

-- Body rows for index rows in the same range that have none yet.
-- name: BackfillSearchFTS :exec
INSERT INTO search_fts (rowid, body)
SELECT d.id, s.body FROM search_docs d
JOIN search_source s ON s.thread_id = d.thread_id AND s.seq = d.seq
WHERE d.thread_id = @thread_id AND d.seq > @from_seq AND d.seq <= @to_seq
  AND NOT EXISTS (SELECT 1 FROM search_fts f WHERE f.rowid = d.id);

-- Drop the whole index (index version changed; rebuilt from scratch).
-- name: PurgeSearchFTS :exec
DELETE FROM search_fts;

-- name: PurgeSearchDocs :exec
DELETE FROM search_docs;

-- Search hits, newest first, keyset-paged by (ts, id) < (@before_ts, @before_id).
-- @match is an FTS5 expression of quoted trigram phrases (all terms of >= 3 characters); @short is a JSON array
-- of the shorter terms, each of which must also occur in the body (instr over lower(), ASCII case folding only).
-- Fork dedupe: of the index rows sharing a key (entry_id, ts > 0), only the one in the earliest thread
-- (created_at, id; then the lowest row id) is returned; copies = number of threads holding the message.
-- @kinds is a JSON array of thread kinds ('[]' = any); @project is a project id ('' = any).
-- name: SearchMessages :many
SELECT d.id, d.thread_id, d.seq, d.entry_id, d.ts, d.role, CAST(f.body AS TEXT) AS body,
       t.title, t.kind, t.archived_at,
       CAST(CASE WHEN d.ts > 0 THEN (SELECT COUNT(DISTINCT c.thread_id) FROM search_docs c
                                     WHERE c.entry_id = d.entry_id AND c.ts = d.ts) ELSE 1 END AS INTEGER) AS copies
FROM search_fts f
JOIN search_docs d ON d.id = f.rowid
JOIN threads t ON t.id = d.thread_id
WHERE f.body MATCH @match
  AND NOT EXISTS (SELECT 1 FROM json_each(@short) s WHERE instr(lower(f.body), lower(s.value)) = 0)
  AND (d.ts < @before_ts OR (d.ts = @before_ts AND d.id < @before_id))
  AND (@kinds = '[]' OR t.kind IN (SELECT value FROM json_each(@kinds)))
  AND (@project = '' OR t.project_id = @project)
  AND NOT EXISTS (
      SELECT 1 FROM search_docs o JOIN threads ot ON ot.id = o.thread_id
      WHERE d.ts > 0 AND o.entry_id = d.entry_id AND o.ts = d.ts AND o.id <> d.id
        AND (ot.created_at < t.created_at
             OR (ot.created_at = t.created_at AND (ot.id < t.id OR (ot.id = t.id AND o.id < d.id)))))
ORDER BY d.ts DESC, d.id DESC
LIMIT @limit;

-- Same as SearchMessages for queries whose terms are all shorter than a trigram: no FTS lookup, the bodies are
-- scanned newest first (search_docs_ts) until enough rows pass the instr filter.
-- name: SearchMessagesShort :many
SELECT d.id, d.thread_id, d.seq, d.entry_id, d.ts, d.role, CAST(f.body AS TEXT) AS body,
       t.title, t.kind, t.archived_at,
       CAST(CASE WHEN d.ts > 0 THEN (SELECT COUNT(DISTINCT c.thread_id) FROM search_docs c
                                     WHERE c.entry_id = d.entry_id AND c.ts = d.ts) ELSE 1 END AS INTEGER) AS copies
FROM search_docs d
JOIN search_fts f ON f.rowid = d.id
JOIN threads t ON t.id = d.thread_id
WHERE NOT EXISTS (SELECT 1 FROM json_each(@short) s WHERE instr(lower(f.body), lower(s.value)) = 0)
  AND (d.ts < @before_ts OR (d.ts = @before_ts AND d.id < @before_id))
  AND (@kinds = '[]' OR t.kind IN (SELECT value FROM json_each(@kinds)))
  AND (@project = '' OR t.project_id = @project)
  AND NOT EXISTS (
      SELECT 1 FROM search_docs o JOIN threads ot ON ot.id = o.thread_id
      WHERE d.ts > 0 AND o.entry_id = d.entry_id AND o.ts = d.ts AND o.id <> d.id
        AND (ot.created_at < t.created_at
             OR (ot.created_at = t.created_at AND (ot.id < t.id OR (ot.id = t.id AND o.id < d.id)))))
ORDER BY d.ts DESC, d.id DESC
LIMIT @limit;
