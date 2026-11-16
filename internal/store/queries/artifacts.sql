-- name: InsertArtifact :exec
INSERT INTO artifacts (id, project_id, thread_id, origin, type, kind, title, head, size, pages, created_at, updated_at)
VALUES (@id, @project_id, @thread_id, @origin, @type, @kind, @title, @head, @size, @pages, @created_at, @updated_at);

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE id = @id;

-- Listing, most recently updated first. @deleted = 0: live artifacts only; 1: trash only.
-- @thread_id non-empty: created by that thread or with a version written by it. @project_id non-empty: that project.
-- name: ListArtifacts :many
SELECT * FROM artifacts
WHERE ((CAST(@deleted AS INTEGER) = 0 AND deleted_at IS NULL) OR (CAST(@deleted AS INTEGER) <> 0 AND deleted_at IS NOT NULL))
  AND (CAST(@thread_id AS TEXT) = '' OR thread_id = CAST(@thread_id AS TEXT)
       OR EXISTS (SELECT 1 FROM artifact_versions v WHERE v.artifact_id = artifacts.id AND v.author_thread_id = CAST(@thread_id AS TEXT)))
  AND (CAST(@project_id AS TEXT) = '' OR project_id = CAST(@project_id AS TEXT))
ORDER BY updated_at DESC, id DESC;

-- name: UpdateArtifactHead :exec
UPDATE artifacts SET head = @head, size = @size, pages = @pages, kind = @kind, updated_at = @updated_at WHERE id = @id;

-- name: RenameArtifact :execrows
UPDATE artifacts SET title = @title WHERE id = @id AND deleted_at IS NULL;

-- name: SetArtifactDeleted :execrows
UPDATE artifacts SET deleted_at = @deleted_at WHERE id = @id;

-- name: SetArtifactOpened :execrows
UPDATE artifacts SET opened_at = @opened_at WHERE id = @id;

-- Permanent delete; only while still in the trash (a concurrent undelete wins).
-- name: EraseArtifact :execrows
DELETE FROM artifacts WHERE id = @id AND deleted_at IS NOT NULL;

-- Cleanup delete; re-checks the expiry in the same statement (a concurrent undelete wins).
-- name: PurgeArtifact :execrows
DELETE FROM artifacts WHERE id = @id AND deleted_at IS NOT NULL AND deleted_at < @ts;

-- Trash entries deleted before @ts (for cleanup).
-- name: ListArtifactsDeletedBefore :many
SELECT * FROM artifacts WHERE deleted_at IS NOT NULL AND deleted_at < @ts ORDER BY deleted_at, id;

-- name: InsertArtifactVersion :exec
INSERT INTO artifact_versions (artifact_id, n, size, pages, author_thread_id, author_label, note, capabilities, format, files_key, created_at)
VALUES (@artifact_id, @n, @size, @pages, @author_thread_id, @author_label, @note, @capabilities, @format, @files_key, @created_at);

-- name: GetArtifactVersion :one
SELECT * FROM artifact_versions WHERE artifact_id = @artifact_id AND n = @n;

-- name: ListArtifactVersions :many
SELECT * FROM artifact_versions WHERE artifact_id = @artifact_id ORDER BY n DESC;

-- Threads that wrote versions, most recent write first.
-- name: ListArtifactAuthorThreads :many
SELECT author_thread_id, MAX(n) AS last_n FROM artifact_versions
WHERE artifact_id = @artifact_id AND author_thread_id IS NOT NULL
GROUP BY author_thread_id
ORDER BY last_n DESC;

-- name: InsertArtifactFile :exec
INSERT INTO artifact_files (artifact_id, n, path, sha256, size) VALUES (@artifact_id, @n, @path, @sha256, @size);

-- name: ListArtifactFiles :many
SELECT * FROM artifact_files WHERE artifact_id = @artifact_id AND n = @n ORDER BY path;

-- name: GetArtifactFile :one
SELECT * FROM artifact_files WHERE artifact_id = @artifact_id AND n = @n AND path = @path;

-- name: CountArtifactFilesBySHA :one
SELECT COUNT(*) FROM artifact_files WHERE sha256 = @sha256;
