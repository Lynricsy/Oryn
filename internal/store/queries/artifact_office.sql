-- M32: Office editing sessions (one per opened browser editor) and holds (one per artifact being edited).

-- name: InsertArtifactOfficeSession :exec
INSERT INTO artifact_office_sessions (id, artifact_id, token_hash, created_at, active_at)
VALUES (@id, @artifact_id, @token_hash, @created_at, @active_at);

-- name: GetArtifactOfficeSession :one
SELECT * FROM artifact_office_sessions WHERE id = @id;

-- name: GetArtifactOfficeSessionByToken :one
SELECT * FROM artifact_office_sessions WHERE token_hash = @token_hash;

-- name: TouchArtifactOfficeSession :exec
UPDATE artifact_office_sessions SET active_at = @active_at WHERE id = @id;

-- Marks a session as closing (keeps the first closing time).
-- name: CloseArtifactOfficeSession :exec
UPDATE artifact_office_sessions SET closing_at = COALESCE(closing_at, @closing_at) WHERE id = @id;

-- Deletes sessions whose token expired, whose close grace passed, or that went idle.
-- name: DeleteStaleArtifactOfficeSessions :execrows
DELETE FROM artifact_office_sessions
WHERE created_at < @created_before
   OR (closing_at IS NOT NULL AND closing_at < @closing_before)
   OR active_at < @active_before;

-- name: InsertArtifactOfficeHold :exec
INSERT INTO artifact_office_holds (artifact_id, base_version) VALUES (@artifact_id, @base_version)
ON CONFLICT (artifact_id) DO NOTHING;

-- name: GetArtifactOfficeHold :one
SELECT * FROM artifact_office_holds WHERE artifact_id = @artifact_id;

-- Holds whose artifact has no remaining session (released by the sweep).
-- name: ListOrphanArtifactOfficeHolds :many
SELECT * FROM artifact_office_holds h
WHERE NOT EXISTS (SELECT 1 FROM artifact_office_sessions s WHERE s.artifact_id = h.artifact_id);

-- name: SetArtifactOfficeHoldLock :exec
UPDATE artifact_office_holds SET lock = @lock WHERE artifact_id = @artifact_id;

-- name: SetArtifactOfficeHoldDraft :exec
UPDATE artifact_office_holds SET draft_sha = @draft_sha, draft_size = @draft_size WHERE artifact_id = @artifact_id;

-- After a committed save: the draft is gone and the new head is the base.
-- name: AdvanceArtifactOfficeHold :exec
UPDATE artifact_office_holds SET base_version = @base_version, draft_sha = NULL, draft_size = NULL WHERE artifact_id = @artifact_id;

-- name: DeleteArtifactOfficeHold :exec
DELETE FROM artifact_office_holds WHERE artifact_id = @artifact_id;

-- name: CountArtifactOfficeDraftsBySHA :one
SELECT COUNT(*) FROM artifact_office_holds WHERE draft_sha = @sha256;

-- name: CountArtifactOfficeSessions :one
SELECT COUNT(*) FROM artifact_office_sessions WHERE artifact_id = @artifact_id;

-- Open (not closing), unexpired sessions of one artifact (the detail's "editing" flag).
-- name: CountOpenArtifactOfficeSessions :one
SELECT COUNT(*) FROM artifact_office_sessions
WHERE artifact_id = @artifact_id AND closing_at IS NULL AND created_at > @created_after AND active_at > @active_after;
