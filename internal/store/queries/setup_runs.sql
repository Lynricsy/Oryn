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
