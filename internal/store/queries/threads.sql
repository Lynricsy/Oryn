-- Threads and labels.

-- kind: work | space | assistant (M9); schedule_id / schedule_run: set when a new_thread schedule run created it.
-- name: CreateThread :exec
INSERT INTO threads (id, project_id, orb_id, title, title_source, state, state_changed_at, parent_thread_id,
                     forked_from_entry_id, session_dir, model, thinking_level, color, created_at, updated_at, short_code,
                     kind, space_id, schedule_id, schedule_run)
VALUES (@id, @project_id, @orb_id, @title, @title_source, @state, @state_changed_at, @parent_thread_id,
        @forked_from_entry_id, @session_dir, @model, @thinking_level, @color, @created_at, @updated_at, @short_code,
        @kind, @space_id, @schedule_id, @schedule_run);

-- name: GetThread :one
SELECT * FROM threads WHERE id = @id;

-- Thread short code (last 12 hex of the id) in portal host names -> thread (M4).
-- name: GetThreadByShortCode :one
SELECT * FROM threads WHERE short_code = CAST(@short_code AS TEXT);

-- GET threads?state=&project=&archived=&q=&kind=&space= ; a NULL filter means unrestricted
-- (the service passes kind = 'work' unless the caller asks for "all").
-- name: ListThreads :many
SELECT * FROM threads
WHERE (CAST(sqlc.narg(state) AS TEXT) IS NULL OR state = CAST(sqlc.narg(state) AS TEXT))
  AND (CAST(sqlc.narg(project_id) AS TEXT) IS NULL OR project_id = CAST(sqlc.narg(project_id) AS TEXT))
  AND (CAST(sqlc.narg(kind) AS TEXT) IS NULL OR kind = CAST(sqlc.narg(kind) AS TEXT))
  AND (CAST(sqlc.narg(space_id) AS TEXT) IS NULL OR space_id = CAST(sqlc.narg(space_id) AS TEXT))
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

-- M9: spaces, coordinator limits, new_thread schedule retention.

-- Moves a thread into a space (space_id) or out of it (NULL). The service only does this for kind = 'work'.
-- name: SetThreadSpace :execrows
UPDATE threads SET space_id = sqlc.narg(space_id), updated_at = @updated_at WHERE id = @id;

-- shipped_at NULL clears the mark.
-- name: SetThreadShipped :exec
UPDATE threads SET shipped_at = sqlc.narg(shipped_at), updated_at = @updated_at WHERE id = @id;

-- Detaches the work threads of a deleted space (the space chat itself is deleted separately).
-- name: ClearSpaceThreads :exec
UPDATE threads SET space_id = NULL WHERE space_id = CAST(@space_id AS TEXT) AND kind = 'work';

-- Unarchived direct children created by an agent or coordinator (forks excluded).
-- name: CountOpenChildren :one
SELECT COUNT(*) FROM threads
WHERE parent_thread_id = CAST(@parent_thread_id AS TEXT) AND archived_at IS NULL AND forked_from_entry_id IS NULL;

-- Threads created by agents / coordinators (have a parent, not a fork) since a point in time.
-- name: CountAgentThreadsSince :one
SELECT COUNT(*) FROM threads
WHERE parent_thread_id IS NOT NULL AND forked_from_entry_id IS NULL AND created_at >= @since;

-- Space progress chart input.
-- name: ListSpaceThreadTimes :many
SELECT created_at, archived_at, shipped_at FROM threads
WHERE space_id = CAST(@space_id AS TEXT) AND kind = 'work'
ORDER BY created_at;

-- Unarchived threads created by a new_thread schedule, newest first (retention policy input).
-- name: ListScheduleThreads :many
SELECT id, state, pinned, orb_id FROM threads
WHERE schedule_id = CAST(@schedule_id AS TEXT) AND archived_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: CountUnarchivedThreadsByOrb :one
SELECT COUNT(*) FROM threads WHERE orb_id = CAST(@orb_id AS TEXT) AND archived_at IS NULL;
