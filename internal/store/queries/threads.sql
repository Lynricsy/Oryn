-- Threads and labels.

-- name: CreateThread :exec
INSERT INTO threads (id, project_id, orb_id, title, title_source, state, state_changed_at, parent_thread_id,
                     forked_from_entry_id, session_dir, model, thinking_level, color, created_at, updated_at, short_code)
VALUES (@id, @project_id, @orb_id, @title, @title_source, @state, @state_changed_at, @parent_thread_id,
        @forked_from_entry_id, @session_dir, @model, @thinking_level, @color, @created_at, @updated_at, @short_code);

-- name: GetThread :one
SELECT * FROM threads WHERE id = @id;

-- Thread short code (last 12 hex of the id) in portal host names -> thread (M4).
-- name: GetThreadByShortCode :one
SELECT * FROM threads WHERE short_code = CAST(@short_code AS TEXT);

-- GET threads?state=&project=&archived=&q= ; a NULL filter means unrestricted.
-- name: ListThreads :many
SELECT * FROM threads
WHERE (CAST(sqlc.narg(state) AS TEXT) IS NULL OR state = CAST(sqlc.narg(state) AS TEXT))
  AND (CAST(sqlc.narg(project_id) AS TEXT) IS NULL OR project_id = CAST(sqlc.narg(project_id) AS TEXT))
  AND (CAST(sqlc.narg(archived) AS BOOLEAN) IS NULL OR (archived_at IS NOT NULL) = CAST(sqlc.narg(archived) AS BOOLEAN))
  AND (CAST(sqlc.narg(q) AS TEXT) IS NULL OR title LIKE '%' || CAST(sqlc.narg(q) AS TEXT) || '%')
ORDER BY pinned DESC, COALESCE(last_message_at, updated_at) DESC, id;

-- name: ListThreadsByOrb :many
SELECT * FROM threads WHERE orb_id = @orb_id ORDER BY created_at;

-- Full write-back for PATCH threads/{id} (handler reads, merges, writes).
-- name: UpdateThreadMeta :execrows
UPDATE threads SET title = @title, title_source = @title_source, color = @color, pinned = @pinned,
    snoozed_until = @snoozed_until, archived_at = @archived_at, unread = @unread, updated_at = @updated_at
WHERE id = @id;

-- name: SetThreadState :exec
UPDATE threads SET state = @state, state_changed_at = @state_changed_at, updated_at = @state_changed_at WHERE id = @id;

-- name: SetThreadOrb :exec
UPDATE threads SET orb_id = @orb_id, updated_at = @updated_at WHERE id = @id;

-- name: SetThreadLeaf :exec
UPDATE threads SET leaf_entry_id = @leaf_entry_id WHERE id = @id;

-- name: SetThreadUnread :exec
UPDATE threads SET unread = @unread WHERE id = @id;

-- name: SetThreadModel :exec
UPDATE threads SET model = @model, thinking_level = @thinking_level, updated_at = @updated_at WHERE id = @id;

-- name: SetThreadAutoTitle :execrows
UPDATE threads SET title = @title, updated_at = @updated_at WHERE id = @id AND title_source = 'auto';

-- name: TouchThreadLastMessage :exec
UPDATE threads SET last_message_at = @last_message_at, updated_at = @last_message_at WHERE id = @id;

-- name: DeleteThread :execrows
DELETE FROM threads WHERE id = @id;

-- name: AddThreadLabel :exec
INSERT OR IGNORE INTO thread_labels (thread_id, label) VALUES (@thread_id, @label);

-- name: RemoveThreadLabel :exec
DELETE FROM thread_labels WHERE thread_id = @thread_id AND label = @label;

-- name: ClearThreadLabels :exec
DELETE FROM thread_labels WHERE thread_id = @thread_id;

-- name: ListThreadLabels :many
SELECT label FROM thread_labels WHERE thread_id = @thread_id ORDER BY label;

-- name: ListAllThreadLabels :many
SELECT * FROM thread_labels ORDER BY thread_id, label;
