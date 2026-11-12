-- Provider job queue.

-- name: CreateJob :exec
INSERT INTO jobs (id, provider_id, orb_id, op, params, created_at)
VALUES (@id, @provider_id, @orb_id, @op, @params, @created_at);

-- name: GetJob :one
SELECT * FROM jobs WHERE id = @id;

-- Claims the provider's oldest queued job (attempt += 1); sql.ErrNoRows when none.
-- name: ClaimNextJob :one
UPDATE jobs SET state = 'claimed', attempt = attempt + 1, claimed_at = @claimed_at, lease_until = @lease_until
WHERE id = (
    SELECT j.id FROM jobs j WHERE j.provider_id = @provider_id AND j.state = 'queued'
    ORDER BY j.created_at, j.id LIMIT 1
)
RETURNING *;

-- name: FinishJob :execrows
UPDATE jobs SET state = @state, finished_at = @finished_at, output = @output, error = @error, lease_until = NULL
WHERE id = @id AND state = 'claimed';

-- name: ListExpiredClaimedJobs :many
SELECT * FROM jobs WHERE state = 'claimed' AND lease_until < @now ORDER BY created_at;

-- name: RequeueJob :execrows
UPDATE jobs SET state = 'queued', claimed_at = NULL, lease_until = NULL WHERE id = @id AND state = 'claimed';

-- name: ListJobsByOrb :many
SELECT * FROM jobs WHERE orb_id = @orb_id ORDER BY created_at, id;

-- name: ListJobs :many
SELECT * FROM jobs
WHERE (CAST(sqlc.narg(state) AS TEXT) IS NULL OR state = CAST(sqlc.narg(state) AS TEXT))
  AND (CAST(sqlc.narg(provider_id) AS TEXT) IS NULL OR provider_id = CAST(sqlc.narg(provider_id) AS TEXT))
ORDER BY created_at, id;
