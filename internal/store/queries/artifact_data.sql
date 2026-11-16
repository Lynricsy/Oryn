-- name: GetArtifactDoc :one
SELECT * FROM artifact_data WHERE artifact_id = @artifact_id AND collection = @collection AND doc_id = @doc_id;

-- Insert or replace one document.
-- name: UpsertArtifactDoc :exec
INSERT INTO artifact_data (artifact_id, collection, doc_id, json, size, version, updated_at)
VALUES (@artifact_id, @collection, @doc_id, @json, @size, @version, @updated_at)
ON CONFLICT (artifact_id, collection, doc_id) DO UPDATE SET
    json = excluded.json, size = excluded.size, version = excluded.version, updated_at = excluded.updated_at;

-- name: DeleteArtifactDoc :execrows
DELETE FROM artifact_data WHERE artifact_id = @artifact_id AND collection = @collection AND doc_id = @doc_id;

-- Documents of one collection by id, after @after (exclusive), at most @lim.
-- name: ListArtifactDocs :many
SELECT * FROM artifact_data
WHERE artifact_id = @artifact_id AND collection = @collection AND doc_id > @after
ORDER BY doc_id LIMIT @lim;

-- All documents of one collection (in-memory query; per-artifact data is bounded).
-- name: ListArtifactCollectionDocs :many
SELECT * FROM artifact_data WHERE artifact_id = @artifact_id AND collection = @collection ORDER BY doc_id;

-- name: ListArtifactCollections :many
SELECT collection, COUNT(*) AS docs, SUM(size) AS bytes FROM artifact_data
WHERE artifact_id = @artifact_id GROUP BY collection ORDER BY collection;

-- name: GetArtifactDataTotals :one
SELECT * FROM artifact_data_totals WHERE artifact_id = @artifact_id;

-- Adds @delta bytes and bumps seq; returns the new row.
-- name: BumpArtifactDataTotals :one
INSERT INTO artifact_data_totals (artifact_id, bytes, seq) VALUES (@artifact_id, @delta, 1)
ON CONFLICT (artifact_id) DO UPDATE SET bytes = bytes + excluded.bytes, seq = seq + 1
RETURNING *;

-- name: UpsertArtifactConsent :exec
INSERT INTO artifact_consents (artifact_id, capability, granted_at) VALUES (@artifact_id, @capability, @granted_at)
ON CONFLICT (artifact_id, capability) DO UPDATE SET granted_at = excluded.granted_at;

-- name: GetArtifactConsent :one
SELECT * FROM artifact_consents WHERE artifact_id = @artifact_id AND capability = @capability;

-- Consents of artifacts not in the trash, newest grant first.
-- name: ListArtifactConsents :many
SELECT c.artifact_id, c.capability, c.granted_at, a.title, a.project_id
FROM artifact_consents c JOIN artifacts a ON a.id = c.artifact_id
WHERE a.deleted_at IS NULL
ORDER BY c.granted_at DESC, c.artifact_id, c.capability;

-- name: DeleteArtifactConsents :execrows
DELETE FROM artifact_consents WHERE artifact_id = @artifact_id;
