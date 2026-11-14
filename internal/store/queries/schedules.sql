-- Schedules (M9): one table, two delivery modes.
--   thread:     append the prompt to thread_id when due (at most one per thread).
--   new_thread: start a new thread in project_id (NULL = no project) on every run.

-- last_run_at, run_count and failures start at their defaults.
-- name: InsertSchedule :exec
INSERT INTO schedules (id, mode, thread_id, project_id, space_id, name, kind, spec, timezone, run_at, ends_at, prompt,
                       enabled, paused_reason, next_run_at, created_by, created_at, updated_at, model, thinking_level,
                       provider, orb_type, overlap, keep_last)
VALUES (@id, @mode, @thread_id, @project_id, @space_id, @name, @kind, @spec, @timezone, @run_at, @ends_at, @prompt,
        @enabled, @paused_reason, @next_run_at, @created_by, @created_at, @updated_at, @model, @thinking_level,
        @provider, @orb_type, @overlap, @keep_last);

-- Replaces the definition; the target (mode, thread_id, project_id), id, run_count and last_run_at stay.
-- Clears failures; paused_reason is whatever the caller passes (NULL, or 'user' when saved disabled).
-- Bumps rev (definition version).
-- name: UpdateScheduleDef :execrows
UPDATE schedules SET name = @name, kind = @kind, spec = @spec, timezone = @timezone, run_at = @run_at,
    ends_at = @ends_at, prompt = @prompt, enabled = @enabled, paused_reason = @paused_reason,
    next_run_at = @next_run_at, failures = 0, space_id = @space_id, model = @model,
    thinking_level = @thinking_level, provider = @provider, orb_type = @orb_type, overlap = @overlap,
    keep_last = @keep_last, created_by = @created_by, updated_at = @updated_at, rev = rev + 1
WHERE id = @id;

-- name: GetSchedule :one
SELECT * FROM schedules WHERE id = @id;

-- The thread-mode schedule of a thread.
-- name: GetScheduleByThread :one
SELECT * FROM schedules WHERE thread_id = CAST(@thread_id AS TEXT);

-- Name uniqueness among new_thread schedules of one project (or among those without a project).
-- name: GetScheduleByProjectName :one
SELECT * FROM schedules
WHERE mode = 'new_thread' AND name = @name
  AND ((project_id IS NULL AND CAST(sqlc.narg(project_id) AS TEXT) IS NULL)
       OR project_id = CAST(sqlc.narg(project_id) AS TEXT))
LIMIT 1;

-- name: DeleteSchedule :execrows
DELETE FROM schedules WHERE id = @id;

-- name: DeleteScheduleByThread :execrows
DELETE FROM schedules WHERE thread_id = CAST(@thread_id AS TEXT);

-- A NULL filter means unrestricted.
--   space_id: new_thread schedules of the space plus thread-mode schedules on that space's threads.
--   no_project = true: new_thread schedules without a project.
-- name: ListSchedules :many
SELECT * FROM schedules
WHERE (CAST(sqlc.narg(mode) AS TEXT) IS NULL OR mode = CAST(sqlc.narg(mode) AS TEXT))
  AND (CAST(sqlc.narg(project_id) AS TEXT) IS NULL OR project_id = CAST(sqlc.narg(project_id) AS TEXT))
  AND (CAST(sqlc.narg(space_id) AS TEXT) IS NULL
       OR space_id = CAST(sqlc.narg(space_id) AS TEXT)
       OR thread_id IN (SELECT t.id FROM threads t WHERE t.space_id = CAST(sqlc.narg(space_id) AS TEXT)))
  AND (CAST(sqlc.narg(thread_id) AS TEXT) IS NULL OR thread_id = CAST(sqlc.narg(thread_id) AS TEXT))
  AND (NOT CAST(@no_project AS BOOLEAN) OR (mode = 'new_thread' AND project_id IS NULL))
ORDER BY created_at, id;

-- Enabled schedules of both modes (global cap).
-- name: CountEnabledSchedules :one
SELECT COUNT(*) FROM schedules WHERE enabled = 1;

-- Enabled schedules defined by one creator ('user' | 'agent'); per-creator cap.
-- name: CountEnabledSchedulesBy :one
SELECT COUNT(*) FROM schedules WHERE enabled = 1 AND created_by = @created_by;

-- name: ListDueSchedules :many
SELECT * FROM schedules
WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= CAST(@now AS INTEGER)
ORDER BY next_run_at, id
LIMIT 100;

-- Earliest next_run_at among enabled schedules; 0 = nothing scheduled.
-- name: MinNextRun :one
SELECT CAST(COALESCE(MIN(next_run_at), 0) AS INTEGER) FROM schedules WHERE enabled = 1 AND next_run_at IS NOT NULL;

-- Written when a run fires (or RunNow): next_run_at NULL = will not fire again.
-- rev is bumped only when enabled changes (auto-finish / auto-pause), not on a plain fire.
-- name: AdvanceSchedule :exec
UPDATE schedules SET next_run_at = @next_run_at, last_run_at = @last_run_at, run_count = @run_count,
    rev = rev + (CASE WHEN enabled = @enabled THEN 0 ELSE 1 END),
    enabled = @enabled, paused_reason = @paused_reason, updated_at = @updated_at
WHERE id = @id;

-- Pause / resume / auto-pause. Resuming recomputes next_run_at and normally resets failures. Bumps rev.
-- name: SetScheduleEnabled :exec
UPDATE schedules SET enabled = @enabled, paused_reason = @paused_reason, next_run_at = @next_run_at,
    failures = @failures, updated_at = @updated_at, rev = rev + 1
WHERE id = @id;

-- name: SetScheduleFailures :exec
UPDATE schedules SET failures = @failures WHERE id = @id;

-- Thread.schedule for one thread (thread-mode schedule only).
-- name: GetScheduleSummary :one
SELECT id, enabled, next_run_at FROM schedules WHERE thread_id = CAST(@thread_id AS TEXT);

-- Thread.schedule for list paths: every thread-mode schedule.
-- name: ListScheduleSummaries :many
SELECT id, CAST(thread_id AS TEXT) AS thread_id, enabled, next_run_at FROM schedules WHERE mode = 'thread';

-- Runs.

-- name: InsertScheduleRun :exec
INSERT INTO schedule_runs (id, schedule_id, thread_id, trigger, scheduled_for, missed, started_at, delivered_at,
                           finished_at, status, error)
VALUES (@id, @schedule_id, @thread_id, @trigger, @scheduled_for, @missed, @started_at, @delivered_at,
        @finished_at, @status, @error);

-- name: GetScheduleRun :one
SELECT * FROM schedule_runs WHERE id = @id;

-- new_thread mode: the thread a run created.
-- name: SetScheduleRunThread :exec
UPDATE schedule_runs SET thread_id = @thread_id WHERE id = @id;

-- Unfinished runs of one schedule: pending + running, plus needs_input when include_needs_input.
-- name: CountOpenScheduleRuns :one
SELECT COUNT(*) FROM schedule_runs
WHERE schedule_id = @schedule_id
  AND (status IN ('pending', 'running') OR (CAST(@include_needs_input AS BOOLEAN) AND status = 'needs_input'));

-- All unfinished runs (scheduler recovery), oldest first.
-- name: ListOpenScheduleRuns :many
SELECT * FROM schedule_runs WHERE status IN ('pending', 'running', 'needs_input') ORDER BY started_at, id;

-- NULL delivered_at / finished_at / error keep the stored value.
-- name: SetScheduleRunStatus :exec
UPDATE schedule_runs SET status = @status,
    delivered_at = COALESCE(sqlc.narg(delivered_at), delivered_at),
    finished_at = COALESCE(sqlc.narg(finished_at), finished_at),
    error = COALESCE(sqlc.narg(error), error)
WHERE id = @id;

-- Newest first.
-- name: ListScheduleRuns :many
SELECT * FROM schedule_runs WHERE schedule_id = @schedule_id ORDER BY started_at DESC, id DESC LIMIT @limit;

-- Keeps the newest @keep finished runs of a schedule; unfinished runs are never trimmed.
-- name: TrimScheduleRuns :exec
DELETE FROM schedule_runs
WHERE schedule_runs.schedule_id = CAST(@schedule_id AS TEXT)
  AND schedule_runs.status NOT IN ('pending', 'running', 'needs_input')
  AND schedule_runs.id NOT IN (SELECT r.id FROM schedule_runs r WHERE r.schedule_id = CAST(@schedule_id AS TEXT)
                               ORDER BY r.started_at DESC, r.id DESC LIMIT @keep);

-- Thread reports (coordinator waits for a thread to settle). gen tags the registration (rep_<ULID>).

-- name: UpsertThreadReport :exec
INSERT INTO thread_reports (thread_id, report_to, requested_at, gen) VALUES (@thread_id, @report_to, @requested_at, @gen)
ON CONFLICT (thread_id) DO UPDATE SET report_to = excluded.report_to, requested_at = excluded.requested_at,
    gen = excluded.gen;

-- name: GetThreadReport :one
SELECT * FROM thread_reports WHERE thread_id = @thread_id;

-- Consumes a registration only if it is still the given generation.
-- name: DeleteThreadReportGen :execrows
DELETE FROM thread_reports WHERE thread_id = @thread_id AND gen = @gen;

-- Detach new_thread schedules from a deleted space (space_id has no FK; cleared by code).
-- name: ClearSpaceSchedules :exec
UPDATE schedules SET space_id = NULL WHERE space_id = @space_id;
