-- name: InsertOrbSetupRun :exec
INSERT INTO orb_setup_runs (id, orb_id, phase, trigger, started_at, finished_at, ok, setup_ran, error,
                            duration_ms, has_repo_script, has_project_script, log_tail)
VALUES (@id, @orb_id, @phase, @trigger, @started_at, @finished_at, @ok, @setup_ran, @error,
        @duration_ms, @has_repo_script, @has_project_script, @log_tail);

-- name: ListOrbSetupRuns :many
SELECT id, orb_id, phase, trigger, started_at, finished_at, ok, setup_ran, error, duration_ms,
       has_repo_script, has_project_script
FROM orb_setup_runs WHERE orb_id = @orb_id ORDER BY finished_at DESC, id DESC LIMIT 20;

-- name: LatestOrbSetupRun :one
SELECT id, orb_id, phase, trigger, started_at, finished_at, ok, setup_ran, error, duration_ms,
       has_repo_script, has_project_script
FROM orb_setup_runs WHERE orb_id = @orb_id AND phase = @phase ORDER BY finished_at DESC, id DESC LIMIT 1;

-- name: GetOrbSetupRunLog :one
SELECT log_tail FROM orb_setup_runs WHERE id = @id AND orb_id = @orb_id;

-- Keeps only the newest @keep runs of an orb.
-- name: PruneOrbSetupRuns :execrows
DELETE FROM orb_setup_runs
WHERE orb_setup_runs.orb_id = @orb_id AND orb_setup_runs.id NOT IN (
    SELECT r.id FROM orb_setup_runs r WHERE r.orb_id = @orb_id ORDER BY r.finished_at DESC, r.id DESC LIMIT @keep
);
