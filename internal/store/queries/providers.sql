-- Providers and the single-instance lease.

-- name: CreateProvider :exec
INSERT INTO providers (id, name, token_hash, created_at) VALUES (@id, @name, @token_hash, @created_at);

-- name: GetProvider :one
SELECT * FROM providers WHERE id = @id;

-- name: GetProviderByName :one
SELECT * FROM providers WHERE name = @name;

-- name: GetProviderByTokenHash :one
SELECT * FROM providers WHERE token_hash = @token_hash;

-- name: ListProviders :many
SELECT * FROM providers ORDER BY name;

-- name: UpdateProviderHello :exec
UPDATE providers SET driver = @driver, version = @version, capabilities = @capabilities,
    orb_types = @orb_types, default_type = @default_type, allow_multiple = @allow_multiple,
    instance_id = @instance_id, lease_until = @lease_until
WHERE id = @id;

-- name: RenewProviderLease :execrows
UPDATE providers SET lease_until = @lease_until WHERE id = @id AND instance_id = @instance_id;

-- name: ClearProviderLease :exec
UPDATE providers SET instance_id = NULL, lease_until = NULL WHERE id = @id AND instance_id = @instance_id;

-- name: DisableProvider :execrows
UPDATE providers SET disabled_at = @disabled_at WHERE id = @id AND disabled_at IS NULL;

-- name: DeleteProvider :execrows
DELETE FROM providers WHERE id = @id;
