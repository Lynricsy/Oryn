-- Thread to browser extension relay bindings (M27).

-- name: UpsertThreadBrowserRelayBinding :exec
INSERT INTO thread_browser_relay_bindings (thread_id, device_id, created_at, updated_at)
VALUES (@thread_id, @device_id, @created_at, @updated_at)
ON CONFLICT (thread_id) DO UPDATE SET
    device_id = excluded.device_id,
    updated_at = excluded.updated_at;

-- name: GetThreadBrowserRelayBinding :one
SELECT b.thread_id, b.device_id, b.created_at, b.updated_at, d.name AS device_name, d.revoked_at AS device_revoked_at
FROM thread_browser_relay_bindings b
JOIN devices d ON d.id = b.device_id
WHERE b.thread_id = @thread_id;

-- name: ListThreadBrowserRelayBindings :many
SELECT b.thread_id, b.device_id, b.created_at, b.updated_at, d.name AS device_name, d.revoked_at AS device_revoked_at
FROM thread_browser_relay_bindings b
JOIN devices d ON d.id = b.device_id
ORDER BY b.updated_at DESC;

-- name: ListThreadBrowserRelayBindingsByDevice :many
SELECT thread_id, device_id, created_at, updated_at
FROM thread_browser_relay_bindings
WHERE device_id = @device_id
ORDER BY updated_at DESC;

-- name: DeleteThreadBrowserRelayBinding :execrows
DELETE FROM thread_browser_relay_bindings WHERE thread_id = @thread_id;

-- name: DeleteThreadBrowserRelayBindingsByDevice :execrows
DELETE FROM thread_browser_relay_bindings WHERE device_id = @device_id;
