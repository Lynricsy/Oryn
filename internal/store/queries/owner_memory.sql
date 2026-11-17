-- M17: owner memory for the global assistant (Hindsight bank oryn-owner).

-- ---------- settings ----------

-- name: GetOwnerMemorySettings :one
SELECT * FROM owner_memory_settings WHERE id = 1;

-- name: UpsertOwnerMemorySettings :exec
INSERT INTO owner_memory_settings (id, enabled, owner_names, updated_at)
VALUES (1, @enabled, @owner_names, @updated_at)
ON CONFLICT (id) DO UPDATE SET
    enabled = excluded.enabled, owner_names = excluded.owner_names, updated_at = excluded.updated_at;

-- name: SetOwnerMemoryDecide :exec
INSERT INTO owner_memory_settings (id, decide_base_url, decide_model, decide_key_enc, updated_at)
VALUES (1, sqlc.narg(decide_base_url), sqlc.narg(decide_model), sqlc.narg(decide_key_enc), @updated_at)
ON CONFLICT (id) DO UPDATE SET
    decide_base_url = excluded.decide_base_url, decide_model = excluded.decide_model,
    decide_key_enc = excluded.decide_key_enc, updated_at = excluded.updated_at;

-- name: SetOwnerMemoryBackfill :exec
UPDATE owner_memory_settings SET backfill_since = sqlc.narg(backfill_since) WHERE id = 1;

-- name: SetOwnerMemoryConfigHash :exec
UPDATE owner_memory_settings SET config_hash = sqlc.narg(config_hash) WHERE id = 1;

-- name: SetOwnerMemoryCuration :exec
UPDATE owner_memory_settings SET last_curated_at = @last_curated_at, last_curation = @last_curation WHERE id = 1;

-- ---------- sources ----------

-- Threads whose content changed since the last write and that are due: idle past the threshold, archived or shipped.
-- Excluded threads and threads of excluded projects are skipped; so are sources waiting for a retry.
-- name: ListDueOwnerMemoryThreads :many
SELECT t.id, t.kind, t.project_id, t.title, t.state, t.archived_at, t.shipped_at, t.last_message_at, t.transcript_rev,
    t.cwd, t.orb_id
FROM threads t
LEFT JOIN owner_memory_sources s ON s.thread_id = t.id
WHERE t.last_message_at IS NOT NULL AND t.last_message_at >= @since
    AND (s.thread_id IS NULL OR s.last_message_at IS NULL OR s.last_message_at <> t.last_message_at
        OR s.transcript_rev <> t.transcript_rev)
    AND (s.next_attempt_at IS NULL OR s.next_attempt_at <= @now)
    AND (t.archived_at IS NOT NULL OR t.shipped_at IS NOT NULL
        OR (t.state = 'idle' AND t.last_message_at <= @idle_before))
    AND NOT EXISTS (SELECT 1 FROM owner_memory_exclusions e WHERE e.kind = 'thread' AND e.target_id = t.id)
    AND NOT EXISTS (SELECT 1 FROM owner_memory_exclusions e WHERE e.kind = 'project' AND e.target_id = t.project_id)
ORDER BY t.last_message_at
LIMIT @lim;

-- Backfill size: threads with activity since the cutoff, not excluded.
-- name: CountOwnerMemoryBackfillThreads :one
SELECT COUNT(*) FROM threads t
WHERE t.last_message_at IS NOT NULL AND t.last_message_at >= @since
    AND NOT EXISTS (SELECT 1 FROM owner_memory_exclusions e WHERE e.kind = 'thread' AND e.target_id = t.id)
    AND NOT EXISTS (SELECT 1 FROM owner_memory_exclusions e WHERE e.kind = 'project' AND e.target_id = t.project_id);

-- name: CountOwnerMemoryBackfillDone :one
SELECT COUNT(*) FROM owner_memory_sources s JOIN threads t ON t.id = s.thread_id
WHERE t.last_message_at >= @since AND s.retained_at IS NOT NULL;

-- name: GetOwnerMemorySource :one
SELECT * FROM owner_memory_sources WHERE thread_id = @thread_id;

-- name: MarkOwnerMemorySourceRetained :exec
INSERT INTO owner_memory_sources (thread_id, kind, last_message_at, transcript_rev, chunks, retained_at,
    next_attempt_at, attempts, last_error, created_at, updated_at)
VALUES (@thread_id, @kind, sqlc.narg(last_message_at), @transcript_rev, @chunks, @now, NULL, 0, NULL, @now, @now)
ON CONFLICT (thread_id) DO UPDATE SET
    kind = excluded.kind, last_message_at = excluded.last_message_at, transcript_rev = excluded.transcript_rev,
    chunks = excluded.chunks, retained_at = excluded.retained_at, next_attempt_at = NULL, attempts = 0,
    last_error = NULL, updated_at = excluded.updated_at;

-- name: MarkOwnerMemorySourceFailed :exec
INSERT INTO owner_memory_sources (thread_id, kind, next_attempt_at, attempts, last_error, created_at, updated_at)
VALUES (@thread_id, @kind, @next_attempt_at, 1, @last_error, @now, @now)
ON CONFLICT (thread_id) DO UPDATE SET
    next_attempt_at = excluded.next_attempt_at, attempts = owner_memory_sources.attempts + 1,
    last_error = excluded.last_error, updated_at = excluded.updated_at;

-- name: ListOwnerMemorySources :many
SELECT s.*, t.title AS thread_title, t.kind AS thread_kind, t.project_id AS thread_project_id
FROM owner_memory_sources s LEFT JOIN threads t ON t.id = s.thread_id
ORDER BY s.updated_at DESC, s.thread_id
LIMIT @lim OFFSET @off;

-- name: CountOwnerMemorySourcesFailing :one
SELECT COUNT(*) FROM owner_memory_sources WHERE last_error IS NOT NULL;

-- Sources whose thread no longer exists: their documents must be forgotten.
-- name: ListOrphanOwnerMemorySources :many
SELECT s.thread_id FROM owner_memory_sources s
WHERE NOT EXISTS (SELECT 1 FROM threads t WHERE t.id = s.thread_id)
LIMIT @lim;

-- Sources that became excluded (the thread or its project): their documents must be forgotten.
-- name: ListExcludedOwnerMemorySources :many
SELECT s.thread_id FROM owner_memory_sources s JOIN threads t ON t.id = s.thread_id
WHERE EXISTS (SELECT 1 FROM owner_memory_exclusions e WHERE e.kind = 'thread' AND e.target_id = t.id)
    OR EXISTS (SELECT 1 FROM owner_memory_exclusions e WHERE e.kind = 'project' AND e.target_id = t.project_id)
LIMIT @lim;

-- name: DeleteOwnerMemorySource :exec
DELETE FROM owner_memory_sources WHERE thread_id = @thread_id;

-- ---------- chunks ----------

-- name: ListOwnerMemoryChunks :many
SELECT * FROM owner_memory_chunks WHERE thread_id = @thread_id ORDER BY idx;

-- name: GetOwnerMemoryChunkByDocument :one
SELECT * FROM owner_memory_chunks WHERE document_id = @document_id;

-- name: UpsertOwnerMemoryChunk :exec
INSERT INTO owner_memory_chunks (thread_id, idx, document_id, hash, seq_from, seq_to, retained_at)
VALUES (@thread_id, @idx, @document_id, @hash, @seq_from, @seq_to, @retained_at)
ON CONFLICT (thread_id, idx) DO UPDATE SET
    document_id = excluded.document_id, hash = excluded.hash, seq_from = excluded.seq_from,
    seq_to = excluded.seq_to, retained_at = excluded.retained_at;

-- name: DeleteOwnerMemoryChunk :exec
DELETE FROM owner_memory_chunks WHERE thread_id = @thread_id AND idx = @idx;

-- name: DeleteOwnerMemoryChunks :exec
DELETE FROM owner_memory_chunks WHERE thread_id = @thread_id;

-- ---------- exclusions ----------

-- name: ListOwnerMemoryExclusions :many
SELECT * FROM owner_memory_exclusions ORDER BY kind, created_at;

-- name: InsertOwnerMemoryExclusion :exec
INSERT INTO owner_memory_exclusions (kind, target_id, created_at) VALUES (@kind, @target_id, @created_at)
ON CONFLICT (kind, target_id) DO NOTHING;

-- name: DeleteOwnerMemoryExclusion :execrows
DELETE FROM owner_memory_exclusions WHERE kind = @kind AND target_id = @target_id;

-- name: ListOwnerMemorySourcesOfProject :many
SELECT s.thread_id FROM owner_memory_sources s JOIN threads t ON t.id = s.thread_id WHERE t.project_id = @project_id;

-- ---------- topics ----------

-- name: ListOwnerMemoryTopics :many
SELECT * FROM owner_memory_topics ORDER BY seed DESC, position, created_at, model_id;

-- name: GetOwnerMemoryTopic :one
SELECT * FROM owner_memory_topics WHERE model_id = @model_id;

-- name: UpsertOwnerMemoryTopic :exec
INSERT INTO owner_memory_topics (model_id, name, scope, seed, status, position, created_at, updated_at)
VALUES (@model_id, @name, @scope, @seed, @status, @position, @now, @now)
ON CONFLICT (model_id) DO UPDATE SET
    name = excluded.name, scope = excluded.scope, status = excluded.status, position = excluded.position,
    updated_at = excluded.updated_at;

-- name: SetOwnerMemoryTopicRefreshed :exec
UPDATE owner_memory_topics SET last_refresh_at = @last_refresh_at, updated_at = @last_refresh_at WHERE model_id = @model_id;

-- name: SetOwnerMemoryTopicHit :exec
UPDATE owner_memory_topics SET last_hit_at = @last_hit_at WHERE model_id = @model_id;

-- name: DeleteOwnerMemoryTopic :exec
DELETE FROM owner_memory_topics WHERE model_id = @model_id;

-- ---------- explicit ----------

-- name: CountOwnerMemoryExplicitPending :one
SELECT COUNT(*) FROM owner_memory_explicit WHERE status = 'pending';

-- name: ListOwnerMemoryExplicit :many
SELECT * FROM owner_memory_explicit ORDER BY status DESC, created_at DESC;

-- name: GetOwnerMemoryExplicit :one
SELECT * FROM owner_memory_explicit WHERE id = @id;

-- name: InsertOwnerMemoryExplicit :exec
INSERT INTO owner_memory_explicit (id, content, owner_quote, source_thread_id, status, created_at, updated_at)
VALUES (@id, @content, sqlc.narg(owner_quote), sqlc.narg(source_thread_id), @status, @now, @now);

-- name: UpdateOwnerMemoryExplicit :execrows
UPDATE owner_memory_explicit SET content = @content, status = @status, updated_at = @updated_at WHERE id = @id;

-- name: DeleteOwnerMemoryExplicit :execrows
DELETE FROM owner_memory_explicit WHERE id = @id;

-- ---------- snapshots ----------

-- name: GetOwnerMemorySnapshot :one
SELECT * FROM owner_memory_snapshots WHERE thread_id = @thread_id;

-- name: UpsertOwnerMemorySnapshot :exec
INSERT INTO owner_memory_snapshots (thread_id, content, generated_at) VALUES (@thread_id, @content, @generated_at)
ON CONFLICT (thread_id) DO UPDATE SET content = excluded.content, generated_at = excluded.generated_at;

-- name: DeleteOwnerMemorySnapshots :exec
DELETE FROM owner_memory_snapshots;

-- ---------- recent activity (injection block 2) ----------

-- Assistant conversations and work threads with messages since the cutoff, newest first.
-- name: ListOwnerMemoryRecentThreads :many
SELECT t.id, t.kind, t.title, t.state, t.project_id, p.name AS project_name, t.archived_at, t.shipped_at,
    t.last_message_at, t.created_at,
    (SELECT group_concat(tp.number, ',') FROM thread_pulls tp WHERE tp.thread_id = t.id AND tp.unlinked = 0) AS pulls
FROM threads t LEFT JOIN projects p ON p.id = t.project_id
WHERE t.kind IN ('assistant','work') AND t.last_message_at IS NOT NULL AND t.last_message_at >= @since
    AND NOT EXISTS (SELECT 1 FROM owner_memory_exclusions e WHERE e.kind = 'thread' AND e.target_id = t.id)
    AND NOT EXISTS (SELECT 1 FROM owner_memory_exclusions e WHERE e.kind = 'project' AND e.target_id = t.project_id)
ORDER BY t.last_message_at DESC
LIMIT @lim;
