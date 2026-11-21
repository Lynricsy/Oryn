-- M24 phase 3: post-push webhook of hosted repositories (at most one per project).

-- name: GetProjectWebhook :one
SELECT * FROM project_webhooks WHERE project_id = @project_id;

-- name: ListProjectWebhooks :many
SELECT * FROM project_webhooks ORDER BY project_id;

-- Insert or overwrite the target; delivery state is kept.
-- name: UpsertProjectWebhook :exec
INSERT INTO project_webhooks (project_id, url, secret_enc, created_at, updated_at)
VALUES (@project_id, @url, @secret_enc, @updated_at, @updated_at)
ON CONFLICT (project_id) DO UPDATE SET
    url = excluded.url,
    secret_enc = excluded.secret_enc,
    updated_at = excluded.updated_at;

-- name: DeleteProjectWebhook :execrows
DELETE FROM project_webhooks WHERE project_id = @project_id;

-- Delivery result only; the row may have been deleted meanwhile (then nothing happens).
-- name: SetProjectWebhookDelivery :exec
UPDATE project_webhooks SET last_delivery_at = @last_delivery_at, last_status = @last_status, last_error = sqlc.narg(last_error)
WHERE project_id = @project_id;
