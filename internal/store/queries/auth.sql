-- Owner, passkeys, recovery codes, web sessions.

-- name: GetOwner :one
SELECT * FROM owner WHERE id = 1;

-- name: CreateOwner :exec
INSERT INTO owner (id, display_name, user_handle, created_at) VALUES (1, @display_name, @user_handle, @created_at);

-- name: UpdateOwnerDisplayName :exec
UPDATE owner SET display_name = @display_name WHERE id = 1;

-- name: SetOwnerPassword :exec
-- Replaces password + TOTP together; resets the replay guard because the TOTP secret is new.
UPDATE owner SET password_hash = @password_hash, totp_secret_enc = @totp_secret_enc, totp_last_step = 0 WHERE id = 1;

-- name: ClearOwnerPassword :exec
UPDATE owner SET password_hash = NULL, totp_secret_enc = NULL, totp_last_step = 0 WHERE id = 1;

-- name: UseTotpStep :execrows
-- Accepts a TOTP time step only once: 0 rows = the step (or a later one) was already used.
UPDATE owner SET totp_last_step = @step WHERE id = 1 AND totp_last_step < @step;

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
INSERT INTO recovery_codes (code_hash, created_at) VALUES (@code_hash, @created_at);

-- name: UseRecoveryCode :execrows
UPDATE recovery_codes SET used_at = @used_at WHERE code_hash = @code_hash AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT COUNT(*) FROM recovery_codes WHERE used_at IS NULL;

-- name: DeleteAllRecoveryCodes :exec
DELETE FROM recovery_codes;

-- name: CountRecoveryCodes :one
SELECT COUNT(*) FROM recovery_codes;

-- name: RecoveryCodesGeneratedAt :one
SELECT CAST(COALESCE(MAX(created_at), 0) AS INTEGER) AS generated_at FROM recovery_codes;

-- name: CreateWebSession :exec
-- verified_at / passkey_id: set when the session was created by a passkey ceremony (login / setup), NULL otherwise.
-- via_password = 1: created by password + TOTP login / setup (verified_at set, passkey_id NULL).
INSERT INTO web_sessions (id_hash, kind, created_at, expires_at, last_seen_at, user_agent, verified_at, passkey_id, via_password)
VALUES (@id_hash, @kind, @created_at, @expires_at, @last_seen_at, @user_agent, sqlc.narg(verified_at), sqlc.narg(passkey_id), @via_password);

-- name: GetWebSession :one
-- App sessions (device_id set) are treated as gone once their device is revoked or missing,
-- so a session left behind by a partial revocation can never authenticate (defense in depth;
-- auth.RevokeDevice deletes them in the same transaction). Costs one PK lookup on devices.
SELECT web_sessions.* FROM web_sessions
WHERE web_sessions.id_hash = @id_hash
  AND (web_sessions.device_id IS NULL OR EXISTS (
    SELECT 1 FROM devices WHERE devices.id = web_sessions.device_id AND devices.revoked_at IS NULL
  ));

-- name: TouchWebSession :exec
UPDATE web_sessions SET last_seen_at = @last_seen_at, expires_at = @expires_at WHERE id_hash = @id_hash;

-- name: DeleteWebSession :exec
DELETE FROM web_sessions WHERE id_hash = @id_hash;

-- name: TouchWebSessionVerified :exec
UPDATE web_sessions SET verified_at = @verified_at WHERE id_hash = @id_hash;

-- name: DeleteWebSessionsByPasskey :many
DELETE FROM web_sessions WHERE passkey_id = @passkey_id AND id_hash <> @keep_id_hash RETURNING id_hash;

-- name: DeleteWebSessionsByPassword :many
DELETE FROM web_sessions WHERE via_password = 1 AND id_hash <> @keep_id_hash RETURNING id_hash;

-- name: DeleteExpiredWebSessions :execrows
DELETE FROM web_sessions WHERE expires_at <= @now;

-- name: CreateDeviceWebSession :exec
INSERT INTO web_sessions (id_hash, kind, created_at, expires_at, last_seen_at, user_agent, device_id)
VALUES (@id_hash, 'full', @created_at, @expires_at, @last_seen_at, @user_agent, @device_id);

-- name: DeleteWebSessionsByDevice :many
DELETE FROM web_sessions WHERE device_id = @device_id RETURNING id_hash;
