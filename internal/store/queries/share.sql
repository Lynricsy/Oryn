-- M5: public thread shares, scope reads over entries, published attachments.

-- name: InsertThreadShare :exec
INSERT INTO thread_shares (id, thread_id, slug, mode, until_seq, leaf_entry_id, findings, created_at)
VALUES (@id, @thread_id, @slug, @mode, @until_seq, @leaf_entry_id, @findings, @created_at);

-- name: GetThreadShare :one
SELECT * FROM thread_shares WHERE id = @id;

-- name: GetActiveThreadShareBySlug :one
SELECT * FROM thread_shares WHERE slug = @slug AND revoked_at IS NULL;

-- name: ListActiveThreadShares :many
SELECT * FROM thread_shares WHERE revoked_at IS NULL ORDER BY created_at DESC, id DESC;

-- name: ListActiveThreadSharesByThread :many
SELECT * FROM thread_shares WHERE thread_id = @thread_id AND revoked_at IS NULL ORDER BY created_at DESC, id DESC;

-- name: RevokeThreadShare :execrows
UPDATE thread_shares SET revoked_at = CAST(@now AS INTEGER) WHERE id = @id AND revoked_at IS NULL;

-- name: CountActiveThreadShares :one
SELECT COUNT(*) FROM thread_shares WHERE thread_id = @thread_id AND revoked_at IS NULL;

-- name: ListActiveThreadShareCounts :many
SELECT thread_id, COUNT(*) AS n FROM thread_shares WHERE revoked_at IS NULL GROUP BY thread_id ORDER BY thread_id;

-- Parent links of main-session entries (file without '/') up to until_seq; input of transcript.ActiveBranch.
-- name: ListMainEntryLinks :many
SELECT seq, file, entry_id, parent_id, type FROM entries
WHERE thread_id = @thread_id AND seq <= @until_seq AND instr(file, '/') = 0
ORDER BY seq;

-- Main-session entries with after_seq < seq <= until_seq.
-- name: ListEntriesBySeqRange :many
SELECT * FROM entries
WHERE thread_id = @thread_id AND seq > @after_seq AND seq <= @until_seq AND instr(file, '/') = 0
ORDER BY seq
LIMIT @limit;

-- Main-session entries (seq <= until_seq) whose raw payload contains needle (e.g. "blob:sha256:<hex>").
-- name: ListEntrySeqsContaining :many
SELECT seq FROM entries
WHERE thread_id = @thread_id AND seq <= @until_seq AND instr(file, '/') = 0
  AND instr(payload, CAST(@needle AS BLOB)) > 0
ORDER BY seq;

-- name: InsertPublishedFile :exec
INSERT INTO published_files (id, attachment_id, slug, created_at)
VALUES (@id, @attachment_id, @slug, @created_at);

-- name: GetActivePublishedFileBySlug :one
SELECT sqlc.embed(p), sqlc.embed(a)
FROM published_files p JOIN attachments a ON a.id = p.attachment_id
WHERE p.slug = @slug AND p.revoked_at IS NULL;

-- name: GetActivePublishedFileByAttachment :one
SELECT * FROM published_files WHERE attachment_id = @attachment_id AND revoked_at IS NULL;

-- name: ListActivePublishedFiles :many
SELECT sqlc.embed(p), sqlc.embed(a)
FROM published_files p JOIN attachments a ON a.id = p.attachment_id
WHERE p.revoked_at IS NULL
ORDER BY p.created_at DESC, p.id DESC;

-- name: ListActivePublishedFilesByThread :many
SELECT sqlc.embed(p), sqlc.embed(a)
FROM published_files p JOIN attachments a ON a.id = p.attachment_id
WHERE a.thread_id = CAST(@thread_id AS TEXT) AND p.revoked_at IS NULL
ORDER BY p.created_at DESC, p.id DESC;

-- Active publications for a set of attachments (publicUrl on attachment lists).
-- name: ListActivePublishedFilesByAttachments :many
SELECT * FROM published_files
WHERE attachment_id IN (sqlc.slice('attachment_ids')) AND revoked_at IS NULL;

-- name: RevokePublishedFile :execrows
UPDATE published_files SET revoked_at = CAST(@now AS INTEGER) WHERE id = @id AND revoked_at IS NULL;
