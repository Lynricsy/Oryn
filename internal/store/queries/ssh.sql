-- M18: SSH hosts, keys and gateway audit.

-- ---------- keys ----------

-- name: ListSshKeys :many
SELECT id, name, key_type, public_key, fingerprint, created_at FROM ssh_keys ORDER BY name;

-- name: GetSshKey :one
SELECT * FROM ssh_keys WHERE id = @id;

-- name: InsertSshKey :exec
INSERT INTO ssh_keys (id, name, key_type, public_key, fingerprint, private_key_enc, created_at)
VALUES (@id, @name, @key_type, @public_key, @fingerprint, @private_key_enc, @created_at);

-- name: RenameSshKey :execrows
UPDATE ssh_keys SET name = @name, public_key = @public_key WHERE id = @id;

-- name: DeleteSshKey :execrows
DELETE FROM ssh_keys WHERE id = @id;

-- name: CountSshHostsByKey :one
SELECT COUNT(*) FROM ssh_hosts WHERE key_id = @key_id;

-- ---------- hosts ----------

-- Global hosts first, then by project and name.
-- name: ListSshHosts :many
SELECT * FROM ssh_hosts ORDER BY project_id IS NOT NULL, project_id, name;

-- name: GetSshHost :one
SELECT * FROM ssh_hosts WHERE id = @id;

-- name: InsertSshHost :exec
INSERT INTO ssh_hosts (id, project_id, name, address, port, username, auth_type, key_id, password_enc, jump_host_id,
                       description, tags, enabled, allow_automation, allow_forward, compat, created_at, updated_at)
VALUES (@id, sqlc.narg(project_id), @name, @address, @port, @username, @auth_type, sqlc.narg(key_id), sqlc.narg(password_enc),
        sqlc.narg(jump_host_id), @description, @tags, @enabled, @allow_automation, @allow_forward, sqlc.narg(compat),
        @created_at, @updated_at);

-- Everything but the scope, the pinned host key and the last test result.
-- name: UpdateSshHost :execrows
UPDATE ssh_hosts
SET name = @name, address = @address, port = @port, username = @username, auth_type = @auth_type,
    key_id = sqlc.narg(key_id), password_enc = sqlc.narg(password_enc), jump_host_id = sqlc.narg(jump_host_id),
    description = @description, tags = @tags, enabled = @enabled, allow_automation = @allow_automation,
    allow_forward = @allow_forward, compat = sqlc.narg(compat), updated_at = @updated_at, rev = rev + 1
WHERE id = @id;

-- name: DeleteSshHost :execrows
DELETE FROM ssh_hosts WHERE id = @id;

-- name: CountSshHostsByJump :one
SELECT COUNT(*) FROM ssh_hosts WHERE jump_host_id = @id;

-- name: CountEnabledSshHostsByJump :one
SELECT COUNT(*) FROM ssh_hosts WHERE jump_host_id = @id AND enabled;

-- First successful connection pins the key. CAS on rev: 0 rows = someone else pinned it first, or the host was
-- changed / reset while dialing (the dial used a stale snapshot and must not write its key back).
-- name: PinSshHostKey :execrows
UPDATE ssh_hosts SET host_key = @host_key, host_key_at = @at WHERE id = @id AND host_key IS NULL AND rev = @rev;

-- name: ClearSshHostKey :execrows
UPDATE ssh_hosts SET host_key = NULL, host_key_at = NULL, updated_at = @at, rev = rev + 1 WHERE id = @id;

-- name: SetSshHostTest :exec
UPDATE ssh_hosts
SET last_test_at = @last_test_at, last_test_ok = @last_test_ok, last_test_error = sqlc.narg(last_test_error),
    last_test_os = sqlc.narg(last_test_os)
WHERE id = @id;

-- ---------- audit ----------

-- name: InsertSshAudit :exec
INSERT INTO ssh_audit (at, source, orb_id, thread_id, host_id, host_name, kind, detail, exit_code, duration_ms, ok)
VALUES (@at, @source, sqlc.narg(orb_id), sqlc.narg(thread_id), @host_id, @host_name, @kind, @detail,
        sqlc.narg(exit_code), sqlc.narg(duration_ms), @ok);

-- host_id / thread_id NULL = no filter. Newest first; page with @before = an id (exclusive).
-- name: ListSshAudit :many
SELECT * FROM ssh_audit
WHERE (CAST(sqlc.narg(host_id) AS TEXT) IS NULL OR host_id = CAST(sqlc.narg(host_id) AS TEXT))
  AND (CAST(sqlc.narg(thread_id) AS TEXT) IS NULL OR thread_id = CAST(sqlc.narg(thread_id) AS TEXT))
  AND id < @before
ORDER BY id DESC
LIMIT @limit;

-- name: DeleteSshAuditBefore :exec
DELETE FROM ssh_audit WHERE at < @before;
