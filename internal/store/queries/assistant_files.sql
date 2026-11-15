-- name: InsertAssistantFile :exec
INSERT INTO assistant_files (id, name, mime, size, sha256, scope, thread_id, source, source_thread_id, bound, created_at)
VALUES (@id, @name, @mime, @size, @sha256, @scope, @thread_id, @source, @source_thread_id, @bound, @created_at);

-- name: GetAssistantFile :one
SELECT * FROM assistant_files WHERE id = @id;

-- Library listing, newest first. @thread_id non-empty: files of that conversation plus shared files;
-- @scope non-empty: only that scope; @include_unbound = 0: only bound files.
-- name: ListAssistantFiles :many
SELECT * FROM assistant_files
WHERE (CAST(@thread_id AS TEXT) = '' OR thread_id = CAST(@thread_id AS TEXT) OR scope = 'shared')
  AND (CAST(@scope AS TEXT) = '' OR scope = CAST(@scope AS TEXT))
  AND (CAST(@include_unbound AS INTEGER) <> 0 OR bound = 1)
ORDER BY created_at DESC, id DESC;

-- name: SetAssistantFileScope :execrows
UPDATE assistant_files SET scope = @scope WHERE id = @id;

-- Binds to a conversation: 1 row when the file is unowned, owned by the same thread, or shared, else 0.
-- thread_id is only filled in when still NULL.
-- name: BindAssistantFile :execrows
UPDATE assistant_files SET bound = 1, thread_id = COALESCE(thread_id, CAST(@thread_id AS TEXT))
WHERE id = @id AND (thread_id IS NULL OR thread_id = CAST(@thread_id AS TEXT) OR scope = 'shared');

-- name: DeleteAssistantFile :execrows
DELETE FROM assistant_files WHERE id = @id;

-- Unbound files created before @ts (for cleanup).
-- name: ListUnboundAssistantFilesBefore :many
SELECT * FROM assistant_files WHERE bound = 0 AND created_at < @ts ORDER BY created_at, id;

-- Deletes only while still unbound (a concurrent Bind wins over cleanup).
-- name: DeleteUnboundAssistantFile :execrows
DELETE FROM assistant_files WHERE id = @id AND bound = 0;
