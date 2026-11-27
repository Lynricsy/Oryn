-- Control-plane operating overrides (internal/opsconf): key = YAML dot path, value = JSON scalar text.

-- name: ListConfigOverrides :many
SELECT * FROM config_overrides ORDER BY key;

-- name: UpsertConfigOverride :exec
INSERT INTO config_overrides (key, value, updated_at) VALUES (@key, @value, @updated_at)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: DeleteConfigOverride :exec
DELETE FROM config_overrides WHERE key = @key;
