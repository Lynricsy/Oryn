-- Runners (M11b): a driver='runner' provider row + one kind='runner' orb, joined by the runners row.

-- The runner's provider row: driver = 'runner', token_hash = Hash(ort_ token). Never touched by the provider protocol
-- (that only accepts opv_ tokens), so driver stays 'runner'. The name shares the providers UNIQUE constraint.
-- name: CreateRunnerProvider :exec
INSERT INTO providers (id, name, token_hash, driver, created_at) VALUES (@id, @name, @token_hash, 'runner', @created_at);

-- Deleting a runner: disable its provider row and swap token_hash for the hash of a random value (the ort_ token
-- stops working at once; the name stays taken, like a disabled provider). 0 rows = not a live runner.
-- name: RevokeRunnerProvider :execrows
UPDATE providers SET disabled_at = @disabled_at, token_hash = @token_hash
WHERE id = @id AND driver = 'runner' AND disabled_at IS NULL;

-- name: CreateRunner :exec
INSERT INTO runners (provider_id, orb_id) VALUES (@provider_id, @orb_id);

-- name: GetRunnerByProvider :one
SELECT * FROM runners WHERE provider_id = @provider_id;

-- name: GetRunnerByOrb :one
SELECT * FROM runners WHERE orb_id = @orb_id;

-- REST list: runners whose provider is not disabled (deleted), with the provider name and the orb's state / versions.
-- name: ListRunners :many
SELECT r.provider_id, r.orb_id, r.hostname, r.os, r.arch, r.dirs, r.features, r.last_seen_at,
       p.name, p.created_at, o.state AS orb_state, o.orbd_version, o.agent_version
FROM runners r
JOIN providers p ON p.id = r.provider_id
JOIN orbs o ON o.id = r.orb_id
WHERE p.disabled_at IS NULL
ORDER BY p.name;

-- Records a runner orbd hello (hello.runner + features); also marks it seen.
-- name: UpdateRunnerHello :exec
UPDATE runners SET hostname = @hostname, os = @os, arch = @arch, dirs = @dirs, features = @features,
    last_seen_at = CAST(@last_seen_at AS INTEGER)
WHERE orb_id = @orb_id;

-- name: TouchRunner :exec
UPDATE runners SET last_seen_at = CAST(@last_seen_at AS INTEGER) WHERE orb_id = @orb_id;

-- Unarchived threads on an orb whose working directory is exactly cwd (dir_in_use check).
-- name: ListThreadsByOrbCwd :many
SELECT id FROM threads
WHERE orb_id = CAST(@orb_id AS TEXT) AND cwd = CAST(@cwd AS TEXT) AND archived_at IS NULL
ORDER BY created_at, id;

-- worktree: {repo, dir, branch} JSON; NULL clears it (after worktree.remove).
-- name: SetThreadWorktree :exec
UPDATE threads SET worktree = sqlc.narg(worktree) WHERE id = @id;
