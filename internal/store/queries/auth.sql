-- Owner, passkeys, recovery codes, web sessions.

-- name: GetOwner :one
SELECT * FROM owner WHERE id = 1;

-- name: CreateOwner :exec
INSERT INTO owner (id, display_name, user_handle, created_at) VALUES (1, @display_name, @user_handle, @created_at);

-- name: UpdateOwnerDisplayName :exec
UPDATE owner SET display_name = @display_name WHERE id = 1;

-- name: CreatePasskey :exec
INSERT INTO passkeys (id, public_key, sign_count, transports, credential, name, created_at)
VALUES (@id, @public_key, @sign_count, @transports, @credential, @name, @created_at);

-- name: GetPasskey :one
SELECT * FROM passkeys WHERE id = @id;

-- name: ListPasskeys :many
SELECT * FROM passkeys ORDER BY created_at;

-- name: CountPasskeys :one
SELECT COUNT(*) FROM passkeys;

-- name: UpdatePasskeyUse :exec
UPDATE passkeys SET sign_count = @sign_count, credential = @credential, last_used_at = @last_used_at WHERE id = @id;

-- name: RenamePasskey :execrows
UPDATE passkeys SET name = @name WHERE id = @id;

-- name: DeletePasskey :execrows
DELETE FROM passkeys WHERE id = @id;

-- name: InsertRecoveryCode :exec
INSERT INTO recovery_codes (code_hash) VALUES (@code_hash);

-- name: UseRecoveryCode :execrows
UPDATE recovery_codes SET used_at = @used_at WHERE code_hash = @code_hash AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT COUNT(*) FROM recovery_codes WHERE used_at IS NULL;

-- name: DeleteAllRecoveryCodes :exec
DELETE FROM recovery_codes;

-- name: CreateWebSession :exec
INSERT INTO web_sessions (id_hash, kind, created_at, expires_at, last_seen_at, user_agent)
VALUES (@id_hash, @kind, @created_at, @expires_at, @last_seen_at, @user_agent);

-- name: GetWebSession :one
SELECT * FROM web_sessions WHERE id_hash = @id_hash;

-- name: TouchWebSession :exec
UPDATE web_sessions SET last_seen_at = @last_seen_at, expires_at = @expires_at WHERE id_hash = @id_hash;

-- name: DeleteWebSession :exec
DELETE FROM web_sessions WHERE id_hash = @id_hash;

-- name: DeleteExpiredWebSessions :execrows
DELETE FROM web_sessions WHERE expires_at <= @now;
