-- M23: boxes (always-on orbs), their apps, releases, deploys, secrets and activity.

-- ---------- boxes ----------

-- name: CreateBox :exec
INSERT INTO boxes (id, name, orb_id, desired, desired_by, desired_at, created_at)
VALUES (@id, @name, @orb_id, @desired, @desired_by, @desired_at, @created_at);

-- name: GetBox :one
SELECT * FROM boxes WHERE id = @id;

-- name: GetBoxByName :one
SELECT * FROM boxes WHERE name = @name;

-- name: GetBoxByOrb :one
SELECT * FROM boxes WHERE orb_id = @orb_id;

-- name: ListBoxes :many
SELECT * FROM boxes ORDER BY created_at, id;

-- name: CountBoxes :one
SELECT COUNT(*) FROM boxes;

-- name: RenameBox :execrows
UPDATE boxes SET name = @name WHERE id = @id;

-- name: SetBoxDesired :execrows
UPDATE boxes SET desired = @desired, desired_by = @desired_by, desired_at = @desired_at WHERE id = @id;

-- All three NULL clears the schedule.
-- name: SetBoxSchedule :execrows
UPDATE boxes SET sched_on = sqlc.narg(sched_on), sched_off = sqlc.narg(sched_off), sched_tz = sqlc.narg(sched_tz)
WHERE id = @id;

-- Cascades to apps, releases, deploys, secrets and activity.
-- name: DeleteBox :execrows
DELETE FROM boxes WHERE id = @id;

-- ---------- apps ----------

-- name: CreateBoxApp :exec
INSERT INTO box_apps (id, box_id, name, port, public, repo, root, source, live_release_id, created_at, updated_at)
VALUES (@id, @box_id, @name, @port, @public, @repo, @root, @source, NULL, @created_at, @updated_at);

-- name: GetBoxApp :one
SELECT * FROM box_apps WHERE box_id = @box_id AND name = @name;

-- name: GetBoxAppByName :one
SELECT * FROM box_apps WHERE name = @name;

-- name: ListBoxApps :many
SELECT * FROM box_apps WHERE box_id = @box_id ORDER BY name;

-- name: ListAllBoxApps :many
SELECT * FROM box_apps ORDER BY box_id, name;

-- name: CountBoxApps :one
SELECT COUNT(*) FROM box_apps WHERE box_id = @box_id;

-- name: ListBoxAppPorts :many
SELECT port FROM box_apps WHERE box_id = @box_id ORDER BY port;

-- name: SetBoxAppPublic :execrows
UPDATE box_apps SET public = @public, updated_at = @updated_at WHERE id = @id;

-- name: SetBoxAppLive :execrows
UPDATE box_apps SET live_release_id = @live_release_id, repo = @repo, root = @root, source = @source, updated_at = @updated_at
WHERE id = @id;

-- name: SetBoxAppPreview :execrows
UPDATE box_apps SET preview = @preview, preview_checked_at = @preview_checked_at WHERE id = @id;

-- name: DeleteBoxApp :execrows
DELETE FROM box_apps WHERE id = @id;

-- Removes the provisional row of a first deploy that did not succeed.
-- name: DeleteProvisionalBoxApp :execrows
DELETE FROM box_apps WHERE box_id = @box_id AND name = @name AND live_release_id IS NULL;

-- ---------- releases ----------

-- name: CreateBoxRelease :exec
INSERT INTO box_releases (id, app_id, repo, commit_sha, branch, root, source, size_bytes, sha256, services, thread_id,
                          state, created_at, activated_at)
VALUES (@id, @app_id, @repo, @commit_sha, @branch, @root, @source, @size_bytes, @sha256, @services, sqlc.narg(thread_id),
        @state, @created_at, @activated_at);

-- name: GetBoxRelease :one
SELECT * FROM box_releases WHERE id = @id;

-- Newest first.
-- name: ListBoxReleases :many
SELECT * FROM box_releases WHERE app_id = @app_id ORDER BY created_at DESC, id DESC;

-- name: SetBoxReleaseState :execrows
UPDATE box_releases SET state = @state WHERE id = @id;

-- name: SetBoxReleaseActivated :execrows
UPDATE box_releases SET state = @state, activated_at = @activated_at WHERE id = @id;

-- ---------- deploys ----------

-- name: CreateBoxDeploy :exec
INSERT INTO box_deploys (id, box_id, app, release_id, actor, thread_id, src_orb_id, repo, root, source, set_public,
                         commit_sha, branch, first, state, created_at)
VALUES (@id, @box_id, @app, @release_id, @actor, sqlc.narg(thread_id), @src_orb_id, @repo, @root, @source,
        sqlc.narg(set_public), @commit_sha, @branch, @first, 'prepared', @created_at);

-- name: GetBoxDeploy :one
SELECT * FROM box_deploys WHERE id = @id;

-- name: GetOpenBoxDeploy :one
SELECT * FROM box_deploys
WHERE box_id = @box_id AND app = @app AND state IN ('prepared', 'transferring', 'activating');

-- name: ListOpenBoxDeploysByBox :many
SELECT * FROM box_deploys WHERE box_id = @box_id AND state IN ('prepared', 'transferring', 'activating') ORDER BY created_at;

-- name: ListBoxDeploysByState :many
SELECT * FROM box_deploys WHERE state = @state ORDER BY created_at;

-- name: ListExpiredBoxDeploys :many
SELECT * FROM box_deploys WHERE state = 'prepared' AND created_at < @before ORDER BY created_at;

-- name: ClaimBoxDeploy :execrows
UPDATE box_deploys SET state = 'transferring', size_bytes = @size_bytes, sha256 = @sha256, services = @services
WHERE id = @id AND state = 'prepared' AND src_orb_id = @src_orb_id;

-- name: SetBoxDeployActivating :execrows
UPDATE box_deploys SET state = 'activating' WHERE id = @id AND state = 'transferring';

-- name: FinishBoxDeploy :execrows
UPDATE box_deploys SET state = @state, error = sqlc.narg(error), finished_at = @now
WHERE id = @id AND state IN ('prepared', 'transferring', 'activating');

-- name: CancelBoxDeploy :execrows
UPDATE box_deploys SET state = 'canceled', finished_at = @now
WHERE id = @id AND state IN ('prepared', 'transferring');

-- ---------- secrets ----------

-- name: PutBoxSecret :exec
INSERT INTO box_secrets (box_id, app, name, value_enc, size, secret, updated_at)
VALUES (@box_id, @app, @name, @value_enc, @size, @secret, @updated_at)
ON CONFLICT (box_id, app, name) DO UPDATE SET value_enc = excluded.value_enc, size = excluded.size,
    secret = excluded.secret, updated_at = excluded.updated_at;

-- name: ListBoxSecrets :many
SELECT * FROM box_secrets WHERE box_id = @box_id AND app = @app ORDER BY name;

-- name: DeleteBoxSecret :execrows
DELETE FROM box_secrets WHERE box_id = @box_id AND app = @app AND name = @name;

-- name: CountBoxSecrets :one
SELECT COUNT(*) FROM box_secrets WHERE box_id = @box_id AND app = @app;

-- Total size of the app's secrets except the one named @name (the one being replaced).
-- name: SumBoxSecretSize :one
SELECT CAST(COALESCE(SUM(size), 0) AS INTEGER) FROM box_secrets WHERE box_id = @box_id AND app = @app AND name <> @name;

-- ---------- activity ----------

-- name: InsertBoxActivity :exec
INSERT INTO box_activity (box_id, ts, kind, actor, app, detail)
VALUES (@box_id, @ts, @kind, @actor, sqlc.narg(app), @detail);

-- name: ListBoxActivity :many
SELECT * FROM box_activity WHERE box_id = @box_id ORDER BY id DESC LIMIT @lim;

-- name: ListAllBoxActivity :many
SELECT * FROM box_activity ORDER BY id DESC LIMIT @lim;
