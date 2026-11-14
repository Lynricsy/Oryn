-- Spaces (M9): name + goal + optional project + one coordinator chat thread.

-- name: CreateSpace :exec
INSERT INTO spaces (id, name, goal, project_id, thread_id, created_at, updated_at)
VALUES (@id, @name, @goal, @project_id, @thread_id, @created_at, @updated_at);

-- name: GetSpace :one
SELECT * FROM spaces WHERE id = @id;

-- name: GetSpaceByName :one
SELECT * FROM spaces WHERE name = @name;

-- The space whose chat is this thread.
-- name: GetSpaceByThread :one
SELECT * FROM spaces WHERE thread_id = @thread_id;

-- Unarchived spaces unless include_archived is true.
-- name: ListSpaces :many
SELECT * FROM spaces
WHERE CAST(@include_archived AS BOOLEAN) OR archived_at IS NULL
ORDER BY name;

-- Full write-back (the service reads, merges, writes).
-- name: UpdateSpace :execrows
UPDATE spaces SET name = @name, goal = @goal, project_id = @project_id, archived_at = @archived_at,
    updated_at = @updated_at
WHERE id = @id;

-- name: DeleteSpace :execrows
DELETE FROM spaces WHERE id = @id;
