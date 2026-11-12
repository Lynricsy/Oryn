-- Provider job queue.

-- name: CreateJob :exec
INSERT INTO jobs (id, provider_id, orb_id, op, params, created_at)
VALUES (@id, @provider_id, @orb_id, @op, @params, @created_at);

-- name: GetJob :one
SELECT * FROM jobs WHERE id = @id;

-- Finishes one specific claim (fenced by claim_gen): a late completion of an older claim matches no row.
-- name: FinishJob :execrows
UPDATE jobs SET state = @state, finished_at = @finished_at, output = @output, error = @error, lease_until = NULL
WHERE id = @id AND state = 'claimed' AND claim_gen = @claim_gen;

-- name: ListExpiredClaimedJobs :many
SELECT * FROM jobs WHERE state = 'claimed' AND lease_until < @now ORDER BY created_at;

-- name: ListJobsByOrb :many
SELECT * FROM jobs WHERE orb_id = @orb_id ORDER BY created_at, id;

-- name: ListJobs :many
SELECT * FROM jobs
WHERE (CAST(sqlc.narg(state) AS TEXT) IS NULL OR state = CAST(sqlc.narg(state) AS TEXT))
  AND (CAST(sqlc.narg(provider_id) AS TEXT) IS NULL OR provider_id = CAST(sqlc.narg(provider_id) AS TEXT))
ORDER BY created_at, id;
