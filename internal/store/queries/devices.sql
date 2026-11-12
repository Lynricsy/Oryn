-- Devices and device pairing.

-- name: CreateDevice :exec
INSERT INTO devices (id, name, kind, platform, token_hash, created_at)
VALUES (@id, @name, @kind, @platform, @token_hash, @created_at);

-- name: GetDevice :one
SELECT * FROM devices WHERE id = @id;

-- name: GetDeviceByTokenHash :one
SELECT * FROM devices WHERE token_hash = @token_hash;

-- name: ListDevices :many
SELECT * FROM devices ORDER BY created_at;

-- name: TouchDevice :exec
UPDATE devices SET last_seen_at = @last_seen_at WHERE id = @id;

-- name: RevokeDevice :execrows
UPDATE devices SET revoked_at = @revoked_at WHERE id = @id AND revoked_at IS NULL;

-- name: CreateDevicePairing :exec
INSERT INTO device_pairings (code, verifier_hash, name, kind, platform, created_at, expires_at)
VALUES (@code, @verifier_hash, @name, @kind, @platform, @created_at, @expires_at);

-- name: GetDevicePairing :one
SELECT * FROM device_pairings WHERE code = @code;

-- name: ApproveDevicePairing :execrows
UPDATE device_pairings SET approved_device_id = @approved_device_id
WHERE code = @code AND approved_device_id IS NULL AND expires_at > @now;

-- name: DeleteDevicePairing :exec
DELETE FROM device_pairings WHERE code = @code;

-- name: DeleteExpiredDevicePairings :execrows
DELETE FROM device_pairings WHERE expires_at <= @now;
