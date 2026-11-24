-- Providers and the single-instance lease.

-- name: CreateProvider :exec
INSERT INTO providers (id, name, token_hash, created_at) VALUES (@id, @name, @token_hash, @created_at);

-- name: GetProvider :one
SELECT * FROM providers WHERE id = @id;

-- name: GetProviderByName :one
SELECT * FROM providers WHERE name = @name;

-- name: GetProviderByTokenHash :one
SELECT * FROM providers WHERE token_hash = @token_hash;

-- Runner providers (driver = 'runner', M11b) are listed by ListRunners, not here: this feeds GET /providers and
-- the "only one provider" fallback of resolveProvider.
-- name: ListProviders :many
SELECT * FROM providers WHERE driver != 'runner' ORDER BY name;

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
    instance_id = @instance_id, lease_until = @lease_until, base_image_id = @base_image_id,
    host_metrics = CASE WHEN CAST(@host_metrics AS TEXT) != '' THEN CAST(@host_metrics AS TEXT) ELSE host_metrics END
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

-- Latest metric sample per live (non-deleted) orb for the providers overview page.
-- name: ListLatestOrbSamples :many
SELECT s.orb_id, s.ts, s.cpu_pct, s.mem_used_mb, s.mem_limit_mb, s.disk_used_mb, s.net_rx_bytes, s.net_tx_bytes
FROM orb_samples s
JOIN (
    SELECT orb_id, MAX(ts) AS max_ts
    FROM orb_samples
    WHERE orb_id IN (SELECT id FROM orbs WHERE deleted_at IS NULL)
    GROUP BY orb_id
) latest ON s.orb_id = latest.orb_id AND s.ts = latest.max_ts;

-- Count of soft-deleted orbs grouped by provider.
-- name: CountDeletedOrbsByProvider :many
SELECT provider_id, CAST(COUNT(*) AS INTEGER) AS deleted_count
FROM orbs
WHERE deleted_at IS NOT NULL
GROUP BY provider_id;

-- Project snapshot counts and ready-cache storage bytes grouped by provider.
-- name: SummarizeProjectSnapshotsByProvider :many
SELECT
    provider_id,
    CAST(SUM(CASE WHEN state = 'ready' THEN 1 ELSE 0 END) AS INTEGER) AS ready_count,
    CAST(SUM(CASE WHEN state = 'building' THEN 1 ELSE 0 END) AS INTEGER) AS building_count,
    CAST(SUM(CASE WHEN state = 'retired' THEN 1 ELSE 0 END) AS INTEGER) AS retired_count,
    CAST(COALESCE(SUM(CASE WHEN state = 'ready' THEN COALESCE(size_bytes, 0) ELSE 0 END), 0) AS INTEGER) AS ready_bytes
FROM project_snapshots
GROUP BY provider_id;

-- Provider job queue status and 24h execution summary grouped by provider.
-- name: SummarizeJobsByProvider :many
SELECT
    provider_id,
    CAST(SUM(CASE WHEN state = 'queued' THEN 1 ELSE 0 END) AS INTEGER) AS queued_count,
    CAST(SUM(CASE WHEN state = 'claimed' THEN 1 ELSE 0 END) AS INTEGER) AS claimed_count,
    CAST(SUM(CASE WHEN state = 'succeeded' AND COALESCE(finished_at, created_at) >= @since_ts THEN 1 ELSE 0 END) AS INTEGER) AS succeeded_24h,
    CAST(SUM(CASE WHEN state = 'failed' AND COALESCE(finished_at, created_at) >= @since_ts THEN 1 ELSE 0 END) AS INTEGER) AS failed_24h
FROM jobs
GROUP BY provider_id;

-- Threads attached to live (non-deleted) orbs, newest activity first.
-- name: ListThreadsForLiveOrbs :many
SELECT id, orb_id, title
FROM threads
WHERE orb_id IS NOT NULL
  AND orb_id IN (SELECT id FROM orbs WHERE deleted_at IS NULL)
ORDER BY updated_at DESC, id;

-- IDs of all project snapshots so overview can classify snapshot_of as project vs fork without N+1 queries.
-- name: ListProjectSnapshotIDs :many
SELECT id FROM project_snapshots;
