-- Orbs, lifecycle events, metric samples.

-- name: CreateOrb :exec
INSERT INTO orbs (id, provider_id, type, cpus, memory_mb, image, project_id, state, state_changed_at,
                  token_hash, snapshot_of, created_at)
VALUES (@id, @provider_id, @type, @cpus, @memory_mb, @image, @project_id, @state, @state_changed_at,
        @token_hash, @snapshot_of, @created_at);

-- name: GetOrb :one
SELECT * FROM orbs WHERE id = @id;

-- name: GetOrbByTokenHash :one
SELECT * FROM orbs WHERE token_hash = @token_hash;

-- name: ListOrbs :many
SELECT * FROM orbs
WHERE (CAST(sqlc.narg(include_deleted) AS BOOLEAN) = 1 OR deleted_at IS NULL)
ORDER BY created_at DESC;

-- name: ListOrbsByState :many
SELECT * FROM orbs WHERE state = @state ORDER BY created_at;

-- name: SetOrbState :exec
UPDATE orbs SET state = @state, state_changed_at = @state_changed_at WHERE id = @id;

-- Compare-and-set: only moves when current state = from_state; 0 rows = state changed elsewhere.
-- name: TransitionOrbState :execrows
UPDATE orbs SET state = @to_state, state_changed_at = @state_changed_at WHERE id = @id AND state = @from_state;

-- name: TouchOrbAgentActivity :exec
UPDATE orbs SET last_agent_activity_at = @ts WHERE id = @id;

-- name: TouchOrbUserInteraction :exec
UPDATE orbs SET last_user_interaction_at = @ts WHERE id = @id;

-- name: SetOrbVersions :exec
UPDATE orbs SET orbd_version = @orbd_version, agent_version = @agent_version WHERE id = @id;

-- name: MarkOrbDeleted :exec
UPDATE orbs SET state = 'deleted', state_changed_at = @deleted_at, deleted_at = @deleted_at WHERE id = @id;

-- name: InsertOrbEvent :exec
INSERT INTO orb_events (orb_id, ts, from_state, to_state, detail) VALUES (@orb_id, @ts, @from_state, @to_state, @detail);

-- name: ListOrbEvents :many
SELECT * FROM orb_events WHERE orb_id = @orb_id ORDER BY ts, id;

-- name: ListOrbEventsInRange :many
SELECT * FROM orb_events WHERE ts >= @from_ts AND ts < @to_ts ORDER BY orb_id, ts, id;

-- name: InsertOrbSample :exec
INSERT OR IGNORE INTO orb_samples (orb_id, ts, cpu_pct, mem_used_mb, mem_limit_mb, disk_used_mb, net_rx_bytes, net_tx_bytes)
VALUES (@orb_id, @ts, @cpu_pct, @mem_used_mb, @mem_limit_mb, @disk_used_mb, @net_rx_bytes, @net_tx_bytes);

-- name: ListOrbSamples :many
SELECT * FROM orb_samples WHERE orb_id = @orb_id AND ts >= @from_ts AND ts < @to_ts ORDER BY ts;

-- name: DeleteOrbSamplesBefore :execrows
DELETE FROM orb_samples WHERE ts < @before;
