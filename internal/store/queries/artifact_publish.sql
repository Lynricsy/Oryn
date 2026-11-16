-- name: InsertArtifactPublication :exec
INSERT INTO artifact_publications (id, artifact_id, slug, access, code, code_gen, version, follow, created_at, updated_at)
VALUES (@id, @artifact_id, @slug, @access, @code, @code_gen, @version, @follow, @created_at, @updated_at);

-- name: GetArtifactPublication :one
SELECT * FROM artifact_publications WHERE artifact_id = @artifact_id;

-- name: GetArtifactPublicationBySlug :one
SELECT * FROM artifact_publications WHERE slug = @slug;

-- name: UpdateArtifactPublication :exec
UPDATE artifact_publications SET access = @access, code = @code, code_gen = @code_gen, code_failures = @code_failures,
    code_locked = @code_locked, version = @version, follow = @follow, pending_version = @pending_version, updated_at = @updated_at
WHERE artifact_id = @artifact_id;

-- Records one wrong access code; locks at @max failures. Returns the updated row.
-- name: FailArtifactPublicationCode :one
UPDATE artifact_publications SET code_failures = code_failures + 1,
    code_locked = CASE WHEN code_failures + 1 >= CAST(@max AS INTEGER) THEN 1 ELSE code_locked END
WHERE slug = @slug
RETURNING *;

-- name: ResetArtifactPublicationCodeFailures :exec
UPDATE artifact_publications SET code_failures = 0 WHERE slug = @slug;

-- name: DeleteArtifactPublication :execrows
DELETE FROM artifact_publications WHERE artifact_id = @artifact_id;

-- Publications with their artifacts, most recently updated first.
-- name: ListArtifactPublications :many
SELECT sqlc.embed(p), sqlc.embed(a) FROM artifact_publications p JOIN artifacts a ON a.id = p.artifact_id
ORDER BY p.updated_at DESC, p.id DESC;

-- Following publications of one artifact (for advancing on a new version).
-- name: ListFollowingArtifactPublications :many
SELECT * FROM artifact_publications WHERE artifact_id = @artifact_id AND follow = 1;

-- name: GetArtifactAutoPublish :one
SELECT auto_publish_artifacts FROM owner WHERE id = 1;

-- name: SetArtifactAutoPublish :execrows
UPDATE owner SET auto_publish_artifacts = @auto_publish_artifacts WHERE id = 1;
