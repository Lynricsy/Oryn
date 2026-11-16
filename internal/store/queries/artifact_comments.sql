-- name: InsertArtifactCommentThread :exec
INSERT INTO artifact_comment_threads (id, artifact_id, anchor, created_at) VALUES (@id, @artifact_id, @anchor, @created_at);

-- name: GetArtifactCommentThread :one
SELECT * FROM artifact_comment_threads WHERE id = @id AND artifact_id = @artifact_id;

-- Threads of one artifact, oldest first.
-- name: ListArtifactCommentThreads :many
SELECT * FROM artifact_comment_threads WHERE artifact_id = @artifact_id ORDER BY created_at, id;

-- name: CountArtifactCommentThreads :one
SELECT COUNT(*) FROM artifact_comment_threads WHERE artifact_id = @artifact_id;

-- name: SetArtifactCommentAnchor :exec
UPDATE artifact_comment_threads SET anchor = @anchor WHERE id = @id;

-- name: SetArtifactCommentResolved :execrows
UPDATE artifact_comment_threads SET resolved_at = @resolved_at WHERE id = @id AND artifact_id = @artifact_id;

-- name: SetArtifactCommentSent :execrows
UPDATE artifact_comment_threads SET sent_at = @sent_at, sent_to = @sent_to WHERE id = @id AND artifact_id = @artifact_id;

-- name: InsertArtifactComment :exec
INSERT INTO artifact_comments (thread_id, n, author_thread_id, author_label, body, created_at)
VALUES (@thread_id, @n, @author_thread_id, @author_label, @body, @created_at);

-- Comments of all threads of one artifact, in thread then n order.
-- name: ListArtifactComments :many
SELECT c.* FROM artifact_comments c JOIN artifact_comment_threads t ON t.id = c.thread_id
WHERE t.artifact_id = @artifact_id ORDER BY t.created_at, t.id, c.n;

-- name: CountArtifactComments :one
SELECT COUNT(*) FROM artifact_comments WHERE thread_id = @thread_id;
