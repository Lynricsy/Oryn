-- M4: managed-service mirror, time-limited portal shares, portal owner sessions.

-- name: DeleteOrbServices :exec
DELETE FROM orb_services WHERE orb_id = @orb_id;

-- name: InsertOrbService :exec
INSERT INTO orb_services (orb_id, name, port, has_portal, doc, updated_at)
VALUES (@orb_id, @name, @port, @has_portal, @doc, @updated_at);

-- name: ListOrbServices :many
SELECT * FROM orb_services WHERE orb_id = @orb_id ORDER BY name;

-- name: GetOrbPortalService :one
SELECT * FROM orb_services WHERE orb_id = @orb_id AND port = CAST(@port AS INTEGER) AND has_portal = 1 LIMIT 1;

-- First share and renewal use the same statement.
-- name: UpsertPortalShare :exec
INSERT INTO portal_shares (orb_id, port, created_at, expires_at)
VALUES (@orb_id, @port, @created_at, @expires_at)
ON CONFLICT (orb_id, port) DO UPDATE SET expires_at = excluded.expires_at;

-- name: GetPortalShare :one
SELECT * FROM portal_shares WHERE orb_id = @orb_id AND port = @port;

-- name: ListPortalSharesByOrb :many
SELECT * FROM portal_shares WHERE orb_id = @orb_id ORDER BY port;

-- name: ListPortalShares :many
SELECT * FROM portal_shares ORDER BY orb_id, port;

-- name: DeletePortalShare :execrows
DELETE FROM portal_shares WHERE orb_id = @orb_id AND port = @port;

-- Expiry deletes only the generation it read: the row must still carry that expires_at and be past due,
-- so a concurrent renewal is never removed.
-- name: DeleteExpiredPortalShare :execrows
DELETE FROM portal_shares
WHERE orb_id = @orb_id AND port = @port AND expires_at = @expires_at AND expires_at <= @now;

-- name: DeletePortalSharesByOrb :exec
DELETE FROM portal_shares WHERE orb_id = @orb_id;

-- Drop shares whose port is no longer declared as a portal in the mirror.
-- name: DeleteUndeclaredPortalShares :execrows
DELETE FROM portal_shares
WHERE portal_shares.orb_id = @orb_id
  AND portal_shares.port NOT IN (
      SELECT s.port FROM orb_services s
      WHERE s.orb_id = @orb_id AND s.has_portal = 1 AND s.port IS NOT NULL
  );

-- name: InsertPortalSession :exec
INSERT INTO portal_sessions (id_hash, web_session_hash, label, created_at, expires_at)
VALUES (@id_hash, @web_session_hash, @label, @created_at, @expires_at);

-- Valid portal session: neither it nor its web session has expired, and the web session is not a recovery session.
-- name: GetPortalSession :one
SELECT ps.id_hash, ps.web_session_hash, ps.label, ps.created_at, ps.expires_at
FROM portal_sessions ps
JOIN web_sessions ws ON ws.id_hash = ps.web_session_hash
WHERE ps.id_hash = @id_hash AND ps.expires_at > @now AND ws.expires_at > @now AND ws.kind = 'full';

-- name: DeleteExpiredPortalSessions :execrows
DELETE FROM portal_sessions WHERE expires_at <= @now;
