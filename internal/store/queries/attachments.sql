-- name: InsertAttachment :exec
INSERT INTO attachments (id, thread_id, name, mime, size, sha256, created_at)
VALUES (@id, @thread_id, @name, @mime, @size, @sha256, @created_at);

-- name: GetAttachment :one
SELECT id, thread_id, name, mime, size, sha256, created_at FROM attachments WHERE id = @id;

-- name: ListAttachmentsByThread :many
SELECT id, thread_id, name, mime, size, sha256, created_at FROM attachments WHERE thread_id = @thread_id ORDER BY created_at, id;

-- Binds to a thread: 1 row when unbound or already bound to the same thread, else 0.
-- name: BindAttachment :execrows
UPDATE attachments SET thread_id = @thread_id
WHERE id = @id AND (thread_id IS NULL OR thread_id = @thread_id);

-- name: DeleteAttachment :execrows
DELETE FROM attachments WHERE id = @id;

-- Attachments created before @ts that are still unbound (for cleanup).
-- name: ListUnboundAttachmentsBefore :many
SELECT id, thread_id, name, mime, size, sha256, created_at FROM attachments WHERE thread_id IS NULL AND created_at < @ts ORDER BY created_at, id;
