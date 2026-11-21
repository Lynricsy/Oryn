-- Blob metadata and secrets.

-- name: InsertBlob :exec
INSERT INTO blobs (sha256, size, mime, created_at) VALUES (@sha256, @size, @mime, @created_at)
ON CONFLICT (sha256) DO NOTHING;

-- name: GetBlob :one
SELECT * FROM blobs WHERE sha256 = @sha256;

-- name: InsertSecret :exec
INSERT INTO secrets (id, project_id, name, mode, secret, value_enc, hosts, created_at, updated_at)
VALUES (@id, sqlc.narg(project_id), @name, @mode, @secret, @value_enc, @hosts, @created_at, @updated_at);

-- Overwrite keeps the id (placeholders already delivered stay valid).
-- name: UpdateSecret :exec
UPDATE secrets SET mode = @mode, secret = @secret, value_enc = @value_enc, hosts = @hosts, updated_at = @updated_at
WHERE id = @id;

-- Global rows only.
-- name: GetSecretByName :one
SELECT * FROM secrets WHERE project_id IS NULL AND name = @name;

-- Placeholder resolution (oryn-ph-<id>) only ever resolves global rows.
-- name: GetSecret :one
SELECT * FROM secrets WHERE id = @id AND project_id IS NULL;

-- Listing never returns ciphertext. Global rows only.
-- name: ListSecrets :many
SELECT id, name, mode, hosts, created_at, updated_at FROM secrets WHERE project_id IS NULL ORDER BY name;

-- name: DeleteSecretByName :execrows
DELETE FROM secrets WHERE project_id IS NULL AND name = @name;

-- name: GetProjectSecret :one
SELECT * FROM secrets WHERE project_id = CAST(@project_id AS TEXT) AND name = @name;

-- Project rows with ciphertext; the caller opens only what it needs.
-- name: ListProjectSecrets :many
SELECT * FROM secrets WHERE project_id = CAST(@project_id AS TEXT) ORDER BY name;

-- name: DeleteProjectSecret :execrows
DELETE FROM secrets WHERE project_id = CAST(@project_id AS TEXT) AND name = @name;

-- Every secret-flagged row (global and project) with its project name, for the known-secret set
-- (share scan, runner screening, owner-memory redaction). Plain project values are not secrets.
-- name: ListSealedSecrets :many
SELECT s.id, s.project_id, s.name, s.value_enc, p.name AS project_name
FROM secrets s LEFT JOIN projects p ON p.id = s.project_id
WHERE s.secret = 1
ORDER BY s.project_id IS NOT NULL, p.name, s.name;

-- Names and last change of every secret whose value can reach an orb of the given project, for the project
-- snapshot config hash (M11a; never the values): global env-mode secrets, global model keys
-- (omp/models/<key>/apiKey, the omp-home render may downgrade them to environment variables), and that project's
-- env entries, prefixed with "project:" so they never collide with a global name. Pass an empty string for no project.
-- name: ListConfigSecretStamps :many
SELECT CAST(CASE WHEN project_id IS NULL THEN name ELSE 'project:' || name END AS TEXT) AS name, updated_at
FROM secrets
WHERE (project_id IS NULL AND (mode = 'env' OR name LIKE 'omp/models/%/apiKey'))
   OR (project_id = CAST(@project_id AS TEXT) AND mode = 'env')
ORDER BY 1;
