-- Thread durability: startup input, fork continuations, transcript revision (migration 0006).

-- name: SetThreadStartupFrames :exec
UPDATE threads SET startup_frames = @startup_frames WHERE id = @id;

-- name: SetThreadForkOp :exec
UPDATE threads SET fork_op = @fork_op WHERE id = @id;

-- Binds the thread to its orb and advances the fork stage in one statement.
-- name: SetThreadOrbAndForkOp :exec
UPDATE threads SET orb_id = @orb_id, fork_op = @fork_op, updated_at = @updated_at WHERE id = @id;

-- name: ListThreadsWithForkOp :many
SELECT * FROM threads WHERE length(fork_op) > 0 ORDER BY created_at;

-- name: BumpTranscriptRev :one
UPDATE threads SET transcript_rev = transcript_rev + 1 WHERE id = @id RETURNING transcript_rev;

-- name: CountThreadMessageEntries :one
SELECT COUNT(*) FROM entries WHERE thread_id = @thread_id AND type = 'message' AND instr(file, '/') = 0;

-- name: EntryIDExists :one
SELECT COUNT(*) FROM entries WHERE thread_id = @thread_id AND entry_id = @entry_id AND instr(file, '/') = 0;

-- Newest non-synthetic main-file entry (leaf fallback after a rewrite removed the leaf).
-- name: LastMainEntryID :one
SELECT entry_id FROM entries
WHERE thread_id = @thread_id AND instr(file, '/') = 0 AND substr(entry_id, 1, 2) <> '__'
ORDER BY seq DESC LIMIT 1;

-- Orb state together with the id of its latest orb_events row (orbs.Event.Seq), read atomically.
-- name: GetOrbStateSeq :one
SELECT o.state, CAST(COALESCE((SELECT MAX(e.id) FROM orb_events e WHERE e.orb_id = o.id), 0) AS INTEGER) AS seq
FROM orbs o WHERE o.id = @id;

-- A snapshot-spawned orb not yet bound to any thread (fork copy recovery: reuse instead of creating another).
-- name: FindUnboundSnapshotOrb :one
SELECT * FROM orbs o
WHERE o.snapshot_of = @snapshot_of AND o.image = @image AND o.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM threads t WHERE t.orb_id = o.id)
ORDER BY o.created_at LIMIT 1;
