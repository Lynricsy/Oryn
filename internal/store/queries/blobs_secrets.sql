-- Blob metadata and secrets.

-- name: InsertBlob :exec
INSERT INTO blobs (sha256, size, mime, created_at) VALUES (@sha256, @size, @mime, @created_at)
ON CONFLICT (sha256) DO NOTHING;

-- name: GetBlob :one
SELECT * FROM blobs WHERE sha256 = @sha256;

-- name: UpsertSecret :exec
INSERT INTO secrets (id, name, mode, value_enc, hosts, created_at, updated_at)
VALUES (@id, @name, @mode, @value_enc, @hosts, @created_at, @updated_at)
ON CONFLICT (name) DO UPDATE SET
    mode = excluded.mode, value_enc = excluded.value_enc, hosts = excluded.hosts, updated_at = excluded.updated_at;

-- name: GetSecretByName :one
SELECT * FROM secrets WHERE name = @name;

-- name: GetSecret :one
SELECT * FROM secrets WHERE id = @id;

-- Listing never returns ciphertext.
-- name: ListSecrets :many
SELECT id, name, mode, hosts, created_at, updated_at FROM secrets ORDER BY name;

-- name: DeleteSecretByName :execrows
DELETE FROM secrets WHERE name = @name;
