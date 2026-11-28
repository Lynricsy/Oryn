-- Writing-block edits (thread_drafts, see 0077). Comments stay ASCII: sqlc mis-parses multibyte text here.

-- name: ListThreadDrafts :many
SELECT * FROM thread_drafts WHERE thread_id = @thread_id ORDER BY updated_at;

-- name: GetThreadDraft :one
SELECT * FROM thread_drafts WHERE thread_id = @thread_id AND entry_id = @entry_id AND block = @block;

-- Store the latest text; delivered_at is left to the delivery marks.
-- name: UpsertThreadDraft :one
INSERT INTO thread_drafts (thread_id, entry_id, block, kind, title, text, updated_at)
VALUES (@thread_id, @entry_id, @block, @kind, @title, @text, @updated_at)
ON CONFLICT (thread_id, entry_id, block) DO UPDATE SET
    kind = excluded.kind,
    title = excluded.title,
    text = excluded.text,
    updated_at = excluded.updated_at
RETURNING *;

-- name: DeleteThreadDraft :execrows
DELETE FROM thread_drafts WHERE thread_id = @thread_id AND entry_id = @entry_id AND block = @block;

-- Edits not yet handed to the agent (never delivered, or edited again since), oldest first.
-- name: ListPendingThreadDrafts :many
SELECT * FROM thread_drafts
WHERE thread_id = @thread_id AND (delivered_at IS NULL OR delivered_at < updated_at)
ORDER BY updated_at;

-- Mark delivered only while the row still holds the version that was read.
-- name: MarkThreadDraftDelivered :exec
UPDATE thread_drafts SET delivered_at = @updated_at
WHERE thread_id = @thread_id AND entry_id = @entry_id AND block = @block AND updated_at = @updated_at;

-- Undo a delivery mark when the message did not go out; untouched if another delivery rewrote it.
-- name: RestoreThreadDraftDelivered :exec
UPDATE thread_drafts SET delivered_at = sqlc.narg(prev)
WHERE thread_id = @thread_id AND entry_id = @entry_id AND block = @block AND delivered_at = @updated_at;

-- name: ThreadEntryExists :one
SELECT EXISTS(SELECT 1 FROM entries WHERE thread_id = @thread_id AND entry_id = @entry_id);
