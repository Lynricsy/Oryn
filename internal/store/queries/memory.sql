-- M16: built-in Hindsight memory.

-- ---------- settings ----------

-- name: GetMemorySettings :one
SELECT * FROM memory_settings WHERE id = 1;

-- name: UpsertMemorySettings :exec
INSERT INTO memory_settings (id, enabled, hindsight_url, updated_at)
VALUES (1, @enabled, sqlc.narg(hindsight_url), @updated_at)
ON CONFLICT (id) DO UPDATE SET
    enabled = excluded.enabled, hindsight_url = excluded.hindsight_url, updated_at = excluded.updated_at;

-- name: SetMemoryKeys :execrows
UPDATE memory_settings SET hindsight_key_enc = @hindsight_key_enc, relay_token_hash = @relay_token_hash,
    keys_rotated_at = @keys_rotated_at, updated_at = @keys_rotated_at
WHERE id = 1;

-- name: SetMemoryDeployedFamilies :execrows
UPDATE memory_settings SET deployed_families = @deployed_families, updated_at = @updated_at WHERE id = 1;

-- name: SetMemoryHealth :exec
UPDATE memory_settings SET last_memory_count = @last_memory_count,
    embed_locked = CASE WHEN @last_memory_count > 0 THEN 1 ELSE embed_locked END
WHERE id = 1;

-- ---------- models / endpoints ----------

-- name: ListMemoryModels :many
SELECT * FROM memory_models ORDER BY purpose;

-- name: UpsertMemoryModel :exec
INSERT INTO memory_models (purpose, provider_key, model_id, updated_at)
VALUES (@purpose, @provider_key, @model_id, @updated_at)
ON CONFLICT (purpose) DO UPDATE SET
    provider_key = excluded.provider_key, model_id = excluded.model_id, updated_at = excluded.updated_at;

-- name: DeleteMemoryModel :exec
DELETE FROM memory_models WHERE purpose = @purpose;

-- name: ListMemoryEndpoints :many
SELECT * FROM memory_endpoints ORDER BY kind;

-- name: UpsertMemoryEndpoint :exec
INSERT INTO memory_endpoints (kind, base_url, model, dimensions, api_key_enc, updated_at)
VALUES (@kind, @base_url, @model, sqlc.narg(dimensions), sqlc.narg(api_key_enc), @updated_at)
ON CONFLICT (kind) DO UPDATE SET
    base_url = excluded.base_url, model = excluded.model, dimensions = excluded.dimensions,
    api_key_enc = excluded.api_key_enc, updated_at = excluded.updated_at;

-- name: DeleteMemoryEndpoint :exec
DELETE FROM memory_endpoints WHERE kind = @kind;

-- ---------- projects ----------

-- name: ListMemoryProjects :many
SELECT * FROM memory_projects;

-- name: GetMemoryProject :one
SELECT * FROM memory_projects WHERE project_id = @project_id;

-- name: UpsertMemoryProject :exec
INSERT INTO memory_projects (project_id, enabled, updated_at)
VALUES (@project_id, @enabled, @updated_at)
ON CONFLICT (project_id) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at;

-- ---------- candidates ----------

-- name: InsertMemoryCandidate :exec
INSERT INTO memory_candidates (id, content, context, source_thread_id, source_project_id, created_at)
VALUES (@id, @content, sqlc.narg(context), sqlc.narg(source_thread_id), sqlc.narg(source_project_id), @created_at)
ON CONFLICT (id) DO NOTHING;

-- name: ListMemoryCandidates :many
SELECT * FROM memory_candidates ORDER BY created_at DESC, id DESC;

-- name: GetMemoryCandidate :one
SELECT * FROM memory_candidates WHERE id = @id;

-- name: DeleteMemoryCandidate :execrows
DELETE FROM memory_candidates WHERE id = @id;

-- ---------- documents ----------

-- name: UpsertMemoryDocument :exec
INSERT INTO memory_documents (document_id, project_id, thread_id, created_at, updated_at)
VALUES (@document_id, sqlc.narg(project_id), sqlc.narg(thread_id), @now, @now)
ON CONFLICT (document_id) DO UPDATE SET updated_at = excluded.updated_at;

-- name: GetMemoryDocument :one
SELECT * FROM memory_documents WHERE document_id = @document_id;

-- name: ListMemoryDocumentsByProject :many
SELECT * FROM memory_documents WHERE project_id = @project_id
ORDER BY updated_at DESC, document_id LIMIT @lim OFFSET @off;

-- name: ListGlobalMemoryDocuments :many
SELECT * FROM memory_documents WHERE project_id IS NULL
ORDER BY updated_at DESC, document_id LIMIT @lim OFFSET @off;

-- name: ListMemoryDocumentsByThread :many
SELECT * FROM memory_documents WHERE thread_id = @thread_id ORDER BY document_id;

-- name: ListAllMemoryDocumentIDsByProject :many
SELECT document_id FROM memory_documents WHERE project_id = @project_id ORDER BY document_id;

-- name: DeleteMemoryDocument :execrows
DELETE FROM memory_documents WHERE document_id = @document_id;

-- ---------- usage ----------

-- name: AddMemoryUsage :exec
INSERT INTO memory_usage (hour, purpose, calls, input_tokens, output_tokens)
VALUES (@hour, @purpose, 1, @input_tokens, @output_tokens)
ON CONFLICT (hour, purpose) DO UPDATE SET
    calls = calls + 1, input_tokens = input_tokens + excluded.input_tokens,
    output_tokens = output_tokens + excluded.output_tokens;

-- name: SumMemoryUsageSince :many
SELECT purpose, CAST(SUM(calls) AS INTEGER) AS calls, CAST(SUM(input_tokens) AS INTEGER) AS input_tokens,
    CAST(SUM(output_tokens) AS INTEGER) AS output_tokens
FROM memory_usage WHERE hour >= @since GROUP BY purpose ORDER BY purpose;

-- name: PruneMemoryUsage :exec
DELETE FROM memory_usage WHERE hour < @before;

-- ---------- tombstones ----------

-- name: InsertMemoryTombstone :exec
INSERT INTO memory_tombstones (document_id, project_id, attempts, next_attempt_at, last_error, created_at)
VALUES (@document_id, sqlc.narg(project_id), 0, @next_attempt_at, sqlc.narg(last_error), @created_at)
ON CONFLICT (document_id) DO NOTHING;

-- name: ListDueMemoryTombstones :many
SELECT * FROM memory_tombstones WHERE next_attempt_at <= @now ORDER BY next_attempt_at LIMIT @lim;

-- name: ListMemoryTombstones :many
SELECT * FROM memory_tombstones ORDER BY created_at;

-- name: RetryMemoryTombstone :exec
UPDATE memory_tombstones SET attempts = attempts + 1, next_attempt_at = @next_attempt_at, last_error = @last_error
WHERE document_id = @document_id;

-- name: DeleteMemoryTombstone :exec
DELETE FROM memory_tombstones WHERE document_id = @document_id;

-- ---------- stale mental models ----------

-- Forget starts: store this forget's fresh token and drop any earlier refresh op.
-- name: UpsertMemoryStaleModel :exec
INSERT INTO memory_stale_models (model_id, project_id, refresh_op, attempts, next_attempt_at, last_error, created_at, token)
VALUES (@model_id, sqlc.narg(project_id), NULL, 0, @now, NULL, @now, @token)
ON CONFLICT (model_id) DO UPDATE SET
    project_id = excluded.project_id, refresh_op = NULL, attempts = 0,
    next_attempt_at = excluded.next_attempt_at, last_error = NULL, token = excluded.token;

-- A delete in flight for this model: no refresh, write-back or clear while any hold exists.
-- name: InsertMemoryStaleHold :exec
INSERT INTO memory_stale_holds (hold_id, model_id, created_at) VALUES (@hold_id, @model_id, @created_at);

-- Delete finished (success or failure): release exactly this hold; idempotent.
-- name: ReleaseMemoryStaleHold :exec
DELETE FROM memory_stale_holds WHERE hold_id = @hold_id;

-- Startup recovery: deletes in flight ended with the previous process (single orynd instance).
-- name: DeleteAllMemoryStaleHolds :exec
DELETE FROM memory_stale_holds;

-- name: GetMemoryStaleModel :one
SELECT * FROM memory_stale_models WHERE model_id = @model_id;

-- The row exists and no delete is in flight for the model.
-- name: GetRefreshableMemoryStaleModel :one
SELECT m.* FROM memory_stale_models m
WHERE m.model_id = @model_id AND NOT EXISTS (SELECT 1 FROM memory_stale_holds h WHERE h.model_id = m.model_id);

-- name: ListMemoryStaleModels :many
SELECT * FROM memory_stale_models ORDER BY created_at, model_id;

-- name: ListDueMemoryStaleModels :many
SELECT m.* FROM memory_stale_models m
WHERE m.next_attempt_at <= @now AND NOT EXISTS (SELECT 1 FROM memory_stale_holds h WHERE h.model_id = m.model_id)
ORDER BY m.next_attempt_at, m.model_id;

-- Write back a submit or check result; void if a newer forget marked the model (new token or new hold).
-- name: RetryMemoryStaleModel :execrows
UPDATE memory_stale_models SET attempts = attempts + 1, next_attempt_at = @next_attempt_at,
    refresh_op = sqlc.narg(refresh_op), last_error = sqlc.narg(last_error)
WHERE memory_stale_models.model_id = @model_id AND memory_stale_models.token = @token
    AND NOT EXISTS (SELECT 1 FROM memory_stale_holds h WHERE h.model_id = memory_stale_models.model_id);

-- Forget finished (our refresh completed or the model is gone); same voiding conditions.
-- name: ClearMemoryStaleModel :execrows
DELETE FROM memory_stale_models WHERE memory_stale_models.model_id = @model_id AND memory_stale_models.token = @token
    AND NOT EXISTS (SELECT 1 FROM memory_stale_holds h WHERE h.model_id = memory_stale_models.model_id);

-- The model itself was deleted (project purge): regardless of token.
-- name: DeleteMemoryStaleModel :exec
DELETE FROM memory_stale_models WHERE model_id = @model_id;
