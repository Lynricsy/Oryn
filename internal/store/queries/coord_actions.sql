-- Coordinator / automation audit log (M9). No FKs: rows outlive threads and schedules.

-- name: InsertCoordAction :exec
INSERT INTO coord_actions (id, ts, actor_kind, actor_thread_id, origin, action, target_thread_id, schedule_id,
                           summary, ok, error)
VALUES (@id, @ts, @actor_kind, @actor_thread_id, @origin, @action, @target_thread_id, @schedule_id,
        @summary, @ok, @error);

-- thread_id NULL = all; otherwise rows where the thread is the actor or the target. Newest first.
-- name: ListCoordActions :many
SELECT * FROM coord_actions
WHERE CAST(sqlc.narg(thread_id) AS TEXT) IS NULL
   OR actor_thread_id = CAST(sqlc.narg(thread_id) AS TEXT)
   OR target_thread_id = CAST(sqlc.narg(thread_id) AS TEXT)
ORDER BY ts DESC, id DESC
LIMIT @limit;

-- Retention (90 days, trimmed daily).
-- name: TrimCoordActions :exec
DELETE FROM coord_actions WHERE ts < @before;
