-- Lifecycle (jobs / providers / orbs / stats) helper queries.

-- Takes over or renews the provider lease for an instance, atomically: only when no other instance holds an
-- unexpired lease (or the stored provider allows multiple instances). 0 rows = lease held by another instance.
-- name: AcquireProviderLease :execrows
UPDATE providers SET instance_id = @instance_id, lease_until = @lease_until
WHERE id = @id AND disabled_at IS NULL
  AND (instance_id IS NULL OR instance_id = @instance_id OR lease_until IS NULL OR lease_until <= @now OR allow_multiple);

-- Replaces job params (used to scrub the orb token after a spawn job finishes).
-- name: SetJobParams :exec
UPDATE jobs SET params = @params WHERE id = @id;

-- Requeues one specific claim after a failed attempt (fenced by claim_gen).
-- name: RequeueFailedJob :execrows
UPDATE jobs SET state = 'queued', claimed_at = NULL, lease_until = NULL, output = @output, error = @error
WHERE id = @id AND state = 'claimed' AND claim_gen = @claim_gen;

-- name: ListUnfinishedJobsByOrb :many
SELECT * FROM jobs WHERE orb_id = @orb_id AND state IN ('queued', 'claimed') ORDER BY created_at, id;

-- name: OrbEverReady :one
SELECT CAST(EXISTS (SELECT 1 FROM orb_events WHERE orb_id = @orb_id AND to_state = 'ready') AS BOOLEAN);

-- name: ListThreadIDsByOrb :many
SELECT id FROM threads WHERE orb_id = @orb_id ORDER BY created_at, id;

-- name: ListLiveOrbs :many
SELECT * FROM orbs WHERE state <> 'deleted' ORDER BY created_at, id;

-- Like ClaimNextJob, but serialized per orb: skips a job while another job of the same orb is
-- claimed, or an older job of the same orb is still queued (per-orb FIFO). Orb-less jobs never block.
-- name: ClaimNextJobSerial :one
UPDATE jobs SET state = 'claimed', attempt = attempt + 1, claim_gen = claim_gen + 1,
    claimed_at = @claimed_at, lease_until = @lease_until
WHERE id = (
    SELECT j.id FROM jobs j
    WHERE j.provider_id = @provider_id AND j.state = 'queued'
      AND NOT EXISTS (SELECT 1 FROM jobs c WHERE c.orb_id = j.orb_id AND c.state = 'claimed')
      AND NOT EXISTS (SELECT 1 FROM jobs e WHERE e.orb_id = j.orb_id AND e.state = 'queued'
                      AND (e.created_at < j.created_at OR (e.created_at = j.created_at AND e.id < j.id)))
    ORDER BY j.created_at, j.id LIMIT 1
)
RETURNING *;
