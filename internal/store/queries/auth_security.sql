-- Pairing requester metadata and session expiry sweeps (security hardening).

-- name: CreateDevicePairingWithMeta :exec
INSERT INTO device_pairings (code, verifier_hash, name, kind, platform, created_at, expires_at, requester_ip, user_agent)
VALUES (@code, @verifier_hash, @name, @kind, @platform, @created_at, @expires_at, @requester_ip, @user_agent);

-- name: DeleteExpiredWebSessionsReturning :many
DELETE FROM web_sessions WHERE expires_at <= @now RETURNING id_hash;
