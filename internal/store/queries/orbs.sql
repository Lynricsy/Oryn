-- Orbs, lifecycle events, metric samples.

-- coordinator = 1: the M9 coordinator orb on the built-in "@host" provider.
-- pool_state 'warming' = a warm-pool orb (M11a); config_hash = project config hash at creation (pool matching);
-- base_image_id = the provider's base image identity when the orb was created (M11a review fix).
-- kind: orb | runner (M11b); NULL = 'orb'. automation (M18): created by an unattended entry point; NULL = 0.
-- sandbox_network (M30): non-NULL = an agent sandbox, value = network mode at creation.
-- name: CreateOrb :exec
INSERT INTO orbs (id, provider_id, type, cpus, memory_mb, image, project_id, state, state_changed_at,
                  token_hash, snapshot_of, created_at, coordinator, pool_state, config_hash, base_image_id, kind, github_identity,
                  automation, computer, box, sandbox_network)
VALUES (@id, @provider_id, @type, @cpus, @memory_mb, @image, @project_id, @state, @state_changed_at,
        @token_hash, @snapshot_of, @created_at, @coordinator, sqlc.narg(pool_state), sqlc.narg(config_hash), @base_image_id,
        COALESCE(CAST(sqlc.narg(kind) AS TEXT), 'orb'), COALESCE(CAST(sqlc.narg(github_identity) AS TEXT), 'user'),
        COALESCE(CAST(sqlc.narg(automation) AS BOOLEAN), 0), COALESCE(CAST(sqlc.narg(computer) AS INTEGER), 0),
        COALESCE(CAST(sqlc.narg(box) AS INTEGER), 0), sqlc.narg(sandbox_network));

-- M30: sandbox orbs older than @before with no creating / ready sandboxes row (left behind by a crash).
-- name: ListOrphanSandboxOrbs :many
SELECT * FROM orbs
WHERE orbs.sandbox_network IS NOT NULL
  AND orbs.state NOT IN ('deleting', 'deleted')
  AND orbs.created_at < @before
  AND NOT EXISTS (SELECT 1 FROM sandboxes s WHERE s.orb_id = orbs.id AND s.state IN ('creating', 'ready'))
ORDER BY orbs.created_at;

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

-- Like InsertOrbEvent, returning the event id (orbs.Event.Seq: monotonic per database).
-- name: InsertOrbEventSeq :one
INSERT INTO orb_events (orb_id, ts, from_state, to_state, detail) VALUES (@orb_id, @ts, @from_state, @to_state, @detail)
RETURNING id;

-- name: MaxOrbEventID :one
SELECT CAST(COALESCE(MAX(id), 0) AS INTEGER) FROM orb_events WHERE orb_id = @orb_id;

-- The orb's latest state change (the one that moved it into its current state); same-state info events
-- (pool, project_scripts_set) are skipped.
-- name: GetLatestOrbStateChange :one
SELECT * FROM orb_events WHERE orb_id = @orb_id AND from_state <> to_state ORDER BY id DESC LIMIT 1;

-- Rotates the orb token (compare-and-swap on the current generation).
-- name: RotateOrbToken :execrows
UPDATE orbs SET token_gen = token_gen + 1, token_hash = @token_hash WHERE id = @id AND token_gen = @token_gen;

-- name: ListOrbEvents :many
SELECT * FROM orb_events WHERE orb_id = @orb_id ORDER BY ts, id;

-- name: ListOrbEventsSince :many
SELECT * FROM orb_events WHERE orb_id = @orb_id AND ts >= @since ORDER BY id;

-- The last event before @before (sets the initial state of a usage window).
-- name: GetOrbEventBefore :one
SELECT * FROM orb_events WHERE orb_id = @orb_id AND ts < @before ORDER BY id DESC LIMIT 1;

-- Box orbs older than @before that no boxes row points at (a create interrupted between the two inserts).
-- name: ListOrphanBoxOrbs :many
SELECT * FROM orbs
WHERE orbs.box = 1 AND orbs.deleted_at IS NULL AND orbs.created_at < @before
  AND NOT EXISTS (SELECT 1 FROM boxes WHERE boxes.orb_id = orbs.id)
ORDER BY orbs.created_at;

-- name: ListOrbEventsInRange :many
SELECT * FROM orb_events WHERE ts >= @from_ts AND ts < @to_ts ORDER BY orb_id, ts, id;

-- name: InsertOrbSample :exec
INSERT OR IGNORE INTO orb_samples (orb_id, ts, cpu_pct, mem_used_mb, mem_limit_mb, disk_used_mb, net_rx_bytes, net_tx_bytes)
VALUES (@orb_id, @ts, @cpu_pct, @mem_used_mb, @mem_limit_mb, @disk_used_mb, @net_rx_bytes, @net_tx_bytes);

-- name: ListOrbSamples :many
SELECT * FROM orb_samples WHERE orb_id = @orb_id AND ts >= @from_ts AND ts < @to_ts ORDER BY ts;

-- name: DeleteOrbSamplesBefore :execrows
DELETE FROM orb_samples WHERE ts < @before;

-- name: CountLiveOrbsBySnapshot :one
SELECT COUNT(*) FROM orbs WHERE snapshot_of = @snapshot_of AND state != 'deleted';

-- The live coordinator orb (M9), newest first; no rows = none yet.
-- name: GetCoordinatorOrb :one
SELECT * FROM orbs
WHERE coordinator = 1 AND deleted_at IS NULL AND state NOT IN ('deleting', 'deleted')
ORDER BY created_at DESC LIMIT 1;

-- The setup config hash actually applied to the orb's environment (creation pick, then each setup.run that ran setup).
-- name: SetOrbConfigHash :exec
UPDATE orbs SET config_hash = CAST(@config_hash AS TEXT) WHERE id = @id;

-- Env-fallback model providers whose real keys the orb's processes may hold (JSON array; NULL = unknown). See 0075.
-- name: SetOrbEnvFallback :exec
UPDATE orbs SET env_fallback = sqlc.narg(env_fallback) WHERE id = @id;

-- The provider fell back to the base image (spawn result snapshotUsed = false): the orb no longer comes from the
-- project snapshot. 0 rows = snapshot_of already moved.
-- name: ClearOrbSnapshot :execrows
UPDATE orbs SET snapshot_of = NULL, image = '' WHERE id = @id AND snapshot_of = CAST(@snapshot_of AS TEXT);

-- Whether a spawn of the orb ever succeeded (requested / spawning / waking -> connecting): the first successful spawn
-- is where the provider's snapshotUsed decides the orb's snapshot provenance; later spawns reuse that machine.
-- name: OrbSpawnedOnce :one
SELECT CAST(EXISTS (SELECT 1 FROM orb_events WHERE orb_id = @orb_id AND to_state = 'connecting' AND from_state IN ('requested', 'spawning', 'waking')) AS BOOLEAN);

-- Warm-pool claim (M11a): CAS on the pool state seen by the caller; pool_state NULL = leaves the pool.
-- 0 rows = claimed / retired by someone else.
-- name: ClaimPoolOrb :execrows
UPDATE orbs SET pool_state = sqlc.narg(pool_state), claimed_at = CAST(@claimed_at AS INTEGER),
                github_identity = COALESCE(CAST(sqlc.narg(github_identity) AS TEXT), github_identity),
                automation = COALESCE(CAST(sqlc.narg(automation) AS BOOLEAN), automation)
WHERE id = @id AND pool_state = CAST(@seen AS TEXT);

-- name: SetOrbAutomation :exec
UPDATE orbs SET automation = @automation WHERE id = @id;

-- Warm-pool state CAS (warming -> idle, claimed -> NULL, -> retiring); 0 rows = the state moved elsewhere.
-- name: SetOrbPoolState :execrows
UPDATE orbs SET pool_state = sqlc.narg(pool_state)
WHERE id = @id AND pool_state = CAST(@seen AS TEXT);

-- Live warm-pool orbs (any pool_state), oldest first; project NULL = all projects.
-- name: ListPoolOrbs :many
SELECT * FROM orbs
WHERE pool_state IS NOT NULL AND deleted_at IS NULL
  AND (CAST(sqlc.narg(project_id) AS TEXT) IS NULL OR project_id = CAST(sqlc.narg(project_id) AS TEXT))
ORDER BY created_at, id;
