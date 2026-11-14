-- Project snapshots (M11a). Key = (project_id, provider_id, type); at most one 'building' and one 'ready' row per key
-- (partial unique indexes). The image lives on the provider host under the row id.

-- name: GetProjectSnapshot :one
SELECT * FROM project_snapshots WHERE id = @id;

-- name: GetReadyProjectSnapshot :one
SELECT * FROM project_snapshots
WHERE project_id = CAST(@project_id AS TEXT) AND provider_id = @provider_id AND type = @type AND state = 'ready';

-- 0 rows = another builder already holds this key (the building partial unique index); that is the dedupe.
-- name: InsertBuildingProjectSnapshot :execrows
INSERT INTO project_snapshots (id, project_id, provider_id, type, state, config_hash, depth, lineage_at,
                               source_orb_id, created_at)
VALUES (@id, CAST(@project_id AS TEXT), @provider_id, @type, 'building', @config_hash, @depth, @lineage_at,
        sqlc.narg(source_orb_id), @created_at)
ON CONFLICT DO NOTHING;

-- building -> ready. Run RetireOtherReadySnapshots first in the same transaction: the ready partial unique index
-- rejects a second ready row for the key.
-- name: PublishProjectSnapshot :execrows
UPDATE project_snapshots SET state = 'ready', ready_at = CAST(@ready_at AS INTEGER), size_bytes = sqlc.narg(size_bytes),
    base_commit = @base_commit
WHERE id = @id AND state = 'building';

-- Retires every other ready row sharing the key of snapshot @id.
-- name: RetireOtherReadySnapshots :exec
UPDATE project_snapshots SET state = 'retired', retired_at = CAST(@retired_at AS INTEGER), retire_reason = CAST(@retire_reason AS TEXT)
WHERE project_snapshots.state = 'ready' AND project_snapshots.id != sqlc.arg(id)
  AND EXISTS (SELECT 1 FROM project_snapshots k WHERE k.id = sqlc.arg(id) AND k.project_id = project_snapshots.project_id
              AND k.provider_id = project_snapshots.provider_id AND k.type = project_snapshots.type);

-- name: RetireProjectSnapshot :execrows
UPDATE project_snapshots SET state = 'retired', retired_at = CAST(@retired_at AS INTEGER), retire_reason = CAST(@retire_reason AS TEXT)
WHERE id = @id AND state = 'ready';

-- Retires a project's ready snapshots; type NULL = all types.
-- name: RetireProjectSnapshotsByProject :exec
UPDATE project_snapshots SET state = 'retired', retired_at = CAST(@retired_at AS INTEGER), retire_reason = CAST(@retire_reason AS TEXT)
WHERE project_id = CAST(@project_id AS TEXT) AND state = 'ready'
  AND (CAST(sqlc.narg(type) AS TEXT) IS NULL OR type = CAST(sqlc.narg(type) AS TEXT));

-- Retires every ready snapshot on a provider (provider disabled / deleted).
-- name: RetireProjectSnapshotsByProvider :exec
UPDATE project_snapshots SET state = 'retired', retired_at = CAST(@retired_at AS INTEGER), retire_reason = CAST(@retire_reason AS TEXT)
WHERE provider_id = @provider_id AND state = 'ready';

-- 0 rows = the snapshot was retired meanwhile; treat as a miss.
-- name: TouchProjectSnapshotUse :execrows
UPDATE project_snapshots SET use_count = use_count + 1, last_used_at = CAST(@last_used_at AS INTEGER)
WHERE id = @id AND state = 'ready';

-- name: ListProjectSnapshotsByProject :many
SELECT sqlc.embed(s), p.name AS provider_name
FROM project_snapshots s JOIN providers p ON p.id = s.provider_id
WHERE s.project_id = CAST(@project_id AS TEXT)
ORDER BY s.created_at DESC, s.id DESC;

-- name: ListProjectSnapshotsByState :many
SELECT * FROM project_snapshots WHERE state = @state ORDER BY created_at, id;

-- Bytes a provider's ready + retired snapshots hold beyond the base image (NULL size = 0).
-- name: SumProjectSnapshotBytes :one
SELECT CAST(COALESCE(SUM(size_bytes), 0) AS INTEGER) FROM project_snapshots
WHERE provider_id = @provider_id AND state IN ('ready', 'retired');

-- name: ListReadySnapshotsLRU :many
SELECT * FROM project_snapshots
WHERE provider_id = @provider_id AND state = 'ready'
ORDER BY COALESCE(last_used_at, ready_at, created_at), id;

-- name: MarkProjectSnapshotGC :exec
UPDATE project_snapshots SET gc_job_id = CAST(@gc_job_id AS TEXT), gc_attempt_at = CAST(@gc_attempt_at AS INTEGER)
WHERE id = @id;

-- name: DeleteProjectSnapshot :exec
DELETE FROM project_snapshots WHERE id = @id;

-- name: DeleteBuildingProjectSnapshot :exec
DELETE FROM project_snapshots WHERE id = @id AND state = 'building';
