-- M30: agent sandboxes. Live = creating / ready / destroying.

-- name: CreateSandbox :exec
INSERT INTO sandboxes (id, orb_id, owner_orb_id, owner_thread_id, backend, provider_name, type,
                       created_network, network, state, idle_sec, last_active_at, ttl_deadline, hard_deadline, created_at)
VALUES (@id, @orb_id, @owner_orb_id, @owner_thread_id, @backend, @provider_name, @type,
        @created_network, @network, 'creating', @idle_sec, @last_active_at, sqlc.narg(ttl_deadline), @hard_deadline, @created_at);

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE id = @id;

-- name: GetSandboxByOrb :one
SELECT * FROM sandboxes WHERE orb_id = @orb_id;

-- Live rows of an owner orb plus those that ended at or after @since.
-- name: ListSandboxesByOwnerOrb :many
SELECT * FROM sandboxes
WHERE owner_orb_id = @owner_orb_id
  AND (state IN ('creating', 'ready', 'destroying') OR COALESCE(destroyed_at, created_at) >= @since)
ORDER BY created_at;

-- name: ListSandboxesByThread :many
SELECT * FROM sandboxes
WHERE owner_thread_id = @owner_thread_id
  AND (state IN ('creating', 'ready', 'destroying') OR COALESCE(destroyed_at, created_at) >= @since)
ORDER BY created_at;

-- name: ListLiveSandboxes :many
SELECT * FROM sandboxes WHERE state IN ('creating', 'ready', 'destroying') ORDER BY created_at;

-- name: CountLiveSandboxes :one
SELECT COUNT(*) FROM sandboxes WHERE state IN ('creating', 'ready');

-- name: CountLiveSandboxesByOwnerOrb :one
SELECT COUNT(*) FROM sandboxes WHERE owner_orb_id = @owner_orb_id AND state IN ('creating', 'ready');

-- name: MarkSandboxReady :execrows
UPDATE sandboxes SET state = 'ready', ready_at = @now, last_active_at = @now
WHERE id = @id AND state = 'creating';

-- name: MarkSandboxFailed :execrows
UPDATE sandboxes SET state = 'failed', error = @error, destroy_reason = @reason, destroyed_at = @now
WHERE id = @id AND state = 'creating';

-- name: MarkSandboxDestroying :execrows
UPDATE sandboxes SET state = 'destroying', destroy_reason = @reason
WHERE id = @id AND state IN ('creating', 'ready');

-- name: MarkSandboxDestroyed :execrows
UPDATE sandboxes SET state = 'destroyed', destroyed_at = @now
WHERE id = @id AND state = 'destroying';

-- name: TouchSandbox :exec
UPDATE sandboxes SET last_active_at = @now WHERE id = @id AND state = 'ready';

-- name: SetSandboxNetwork :exec
UPDATE sandboxes SET network = @network WHERE id = @id;

-- name: SetSandboxTTL :exec
UPDATE sandboxes SET ttl_deadline = sqlc.narg(ttl_deadline) WHERE id = @id;

-- name: TrimSandboxes :execrows
DELETE FROM sandboxes WHERE state IN ('destroyed', 'failed') AND destroyed_at < @before;
