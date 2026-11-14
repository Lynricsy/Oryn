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

-- Records a provider hello and takes its lease in one conditional update (same lease rule as
-- AcquireProviderLease, plus: both the stored row and this hello declare allowMultiple). 0 rows = lease conflict.
-- name: AcquireProviderHello :execrows
UPDATE providers SET driver = @driver, version = @version, capabilities = @capabilities,
    orb_types = @orb_types, default_type = @default_type, allow_multiple = @allow_multiple,
    instance_id = @instance_id, lease_until = @lease_until
WHERE id = @id AND disabled_at IS NULL
  AND (instance_id IS NULL OR instance_id = @instance_id OR lease_until IS NULL OR lease_until <= @now
       OR (allow_multiple AND CAST(@allow_multiple AS BOOLEAN)));

-- name: ClearProviderLease :exec
UPDATE providers SET instance_id = NULL, lease_until = NULL WHERE id = @id AND instance_id = @instance_id;

-- name: DisableProvider :execrows
UPDATE providers SET disabled_at = @disabled_at WHERE id = @id AND disabled_at IS NULL;

-- name: DeleteProvider :execrows
DELETE FROM providers WHERE id = @id;

-- The built-in coordinator host provider (M9). Its token is random and discarded, so the HTTP provider
-- protocol can never authenticate as it; restarts keep id and token_hash and refresh the declaration.
-- name: UpsertHostProvider :exec
INSERT INTO providers (id, name, token_hash, driver, version, capabilities, orb_types, default_type, created_at)
VALUES (@id, '@host', @token_hash, 'host', @version, @capabilities, @orb_types, 'host', @created_at)
ON CONFLICT (name) DO UPDATE SET version = excluded.version, capabilities = excluded.capabilities,
    orb_types = excluded.orb_types, default_type = 'host', disabled_at = NULL;

-- Lease renewal for the in-process host provider (no hello round trip).
-- name: SetProviderLease :exec
UPDATE providers SET instance_id = @instance_id, lease_until = @lease_until WHERE id = @id;
