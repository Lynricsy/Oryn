-- M10 analytics: narrow fact rows (usage_records / tool_calls / turns), thread commits, stats state, budget,
-- retention. Fact rows are written in the entries transaction; aggregates never touch entries.payload.
-- Invariant: fact rows of a live thread = f(its current entries); rows of a deleted thread are frozen
-- (thread_id NULL, project_id / space_id = attribution snapshot written on delete).

-- ---------------------------------------------------------------- writes

-- ukey = "<entryId>@<ts>": cross-thread dedup key (fork copies keep id and timestamp). 0 rows = ignored
-- (same (thread_id, entry_seq) or ukey already taken by another thread).
-- name: InsertUsageRecord :execrows
INSERT OR IGNORE INTO usage_records (thread_id, entry_seq, ts, provider, model, purpose, stop_reason, ukey,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, duration_ms, ttft_ms, is_subagent)
VALUES (@thread_id, @entry_seq, @ts, @provider, @model, @purpose, @stop_reason, @ukey,
    @input_tokens, @output_tokens, @cache_read_tokens, @cache_write_tokens, @cost_usd, @duration_ms, @ttft_ms, @is_subagent);

-- name: InsertToolCall :execrows
INSERT OR IGNORE INTO tool_calls (thread_id, entry_seq, ts, tool, is_error, ukey)
VALUES (@thread_id, @entry_seq, @ts, @tool, @is_error, @ukey);

-- A turn = one role=user message in a main session file.
-- name: InsertTurn :execrows
INSERT OR IGNORE INTO turns (thread_id, entry_seq, ts, ukey)
VALUES (@thread_id, @entry_seq, @ts, @ukey);

-- ---------------------------------------------------------------- per-entry / per-thread deletes

-- name: DeleteUsageAt :exec
DELETE FROM usage_records WHERE thread_id = CAST(@thread_id AS TEXT) AND entry_seq = @entry_seq;

-- name: DeleteToolCallAt :exec
DELETE FROM tool_calls WHERE thread_id = CAST(@thread_id AS TEXT) AND entry_seq = @entry_seq;

-- name: DeleteTurnAt :exec
DELETE FROM turns WHERE thread_id = CAST(@thread_id AS TEXT) AND entry_seq = @entry_seq;

-- name: DeleteThreadUsage :exec
DELETE FROM usage_records WHERE thread_id = CAST(@thread_id AS TEXT);

-- name: DeleteThreadToolCalls :exec
DELETE FROM tool_calls WHERE thread_id = CAST(@thread_id AS TEXT);

-- name: DeleteThreadTurns :exec
DELETE FROM turns WHERE thread_id = CAST(@thread_id AS TEXT);

-- All live threads (manual full rebuild only); rows of deleted threads stay.
-- name: DeleteLiveUsage :exec
DELETE FROM usage_records WHERE thread_id IS NOT NULL;

-- name: DeleteLiveToolCalls :exec
DELETE FROM tool_calls WHERE thread_id IS NOT NULL;

-- name: DeleteLiveTurns :exec
DELETE FROM turns WHERE thread_id IS NOT NULL;

-- Orphans: fact rows of a thread whose entry (thread_id, entry_seq) no longer exists (after reset_done / dropFile).
-- name: DeleteOrphanUsage :execrows
DELETE FROM usage_records WHERE thread_id = CAST(@thread_id AS TEXT)
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.thread_id = usage_records.thread_id AND e.seq = usage_records.entry_seq);

-- name: DeleteOrphanToolCalls :execrows
DELETE FROM tool_calls WHERE thread_id = CAST(@thread_id AS TEXT)
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.thread_id = tool_calls.thread_id AND e.seq = tool_calls.entry_seq);

-- name: DeleteOrphanTurns :execrows
DELETE FROM turns WHERE thread_id = CAST(@thread_id AS TEXT)
  AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.thread_id = turns.thread_id AND e.seq = turns.entry_seq);

-- Attribution snapshot written right before the thread row is deleted (thread_id then becomes NULL).
-- name: TombstoneUsage :exec
UPDATE usage_records SET project_id = @project_id, space_id = @space_id WHERE thread_id = CAST(@thread_id AS TEXT);

-- name: TombstoneToolCalls :exec
UPDATE tool_calls SET project_id = @project_id, space_id = @space_id WHERE thread_id = CAST(@thread_id AS TEXT);

-- name: TombstoneTurns :exec
UPDATE turns SET project_id = @project_id, space_id = @space_id WHERE thread_id = CAST(@thread_id AS TEXT);

-- ---------------------------------------------------------------- aggregates

-- One static SQL per fact table; dim picks the single group key (Go: stats.dim* constants):
--   0 = none (''), 1 = thread ('' for deleted threads), 2 = project, 3 = space, 4 = '<provider>/<model>' (usage only),
--   5 = purpose (usage only), 6 = tool (tool calls only); any other value = ''.
-- Project / space: live threads take their current value, deleted threads (no thread row) fall back to the snapshot
-- columns. Lazy correlated subqueries instead of LEFT JOIN threads: they run only for dim 2 / 3 or the project /
-- space filters (CASE / OR short-circuit).
-- bucket = (ts + offset_ms) / bucket_ms (local hour / day / week ordinal; 0 when by_bucket is off).
-- Output is (bucket, key, sums...) grouped by the first two columns.
-- Filters: NULL = no filter; project / space '' = none.
-- Raw rows serve the unaligned head / tail of a window; the 15-minute rollups (*AggQ) serve the aligned middle.
-- name: StatsUsageAgg :many
SELECT
    CAST(CASE WHEN CAST(sqlc.arg(by_bucket) AS BOOLEAN)
         THEN (u.ts + CAST(sqlc.arg(offset_ms) AS INTEGER)) / CAST(sqlc.arg(bucket_ms) AS INTEGER) ELSE 0 END AS INTEGER) AS bucket,
    CAST(CASE CAST(sqlc.arg(dim) AS INTEGER)
         WHEN 1 THEN COALESCE(u.thread_id, '')
         WHEN 2 THEN COALESCE((SELECT t.project_id FROM threads t WHERE t.id = u.thread_id), u.project_id, '')
         WHEN 3 THEN COALESCE((SELECT t.space_id FROM threads t WHERE t.id = u.thread_id), u.space_id, '')
         WHEN 4 THEN u.provider || '/' || u.model
         WHEN 5 THEN u.purpose
         ELSE '' END AS TEXT) AS "key",
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(u.stop_reason = 'error'), 0) AS INTEGER) AS request_errors,
    CAST(COALESCE(SUM(u.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COALESCE(SUM(u.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COALESCE(SUM(u.cache_read_tokens), 0) AS INTEGER) AS cache_read_tokens,
    CAST(COALESCE(SUM(u.cache_write_tokens), 0) AS INTEGER) AS cache_write_tokens,
    CAST(COALESCE(SUM(u.cost_usd), 0) AS REAL) AS cost_usd,
    CAST(COALESCE(SUM(CASE WHEN u.is_subagent THEN u.cost_usd ELSE 0 END), 0) AS REAL) AS subagent_cost_usd,
    CAST(COALESCE(SUM(u.ttft_ms), 0) AS INTEGER) AS ttft_sum_ms,
    CAST(COUNT(u.ttft_ms) AS INTEGER) AS ttft_n,
    CAST(COALESCE(SUM(CASE WHEN u.duration_ms > 0 THEN u.output_tokens ELSE 0 END), 0) AS INTEGER) AS timed_output_tokens,
    CAST(COALESCE(SUM(CASE WHEN u.duration_ms > 0 THEN u.duration_ms ELSE 0 END), 0) AS INTEGER) AS duration_ms
FROM usage_records u
WHERE u.ts >= @from_ts AND u.ts < @to_ts
  AND (CAST(sqlc.narg(thread) AS TEXT) IS NULL OR u.thread_id = CAST(sqlc.narg(thread) AS TEXT))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL
       OR COALESCE((SELECT t.project_id FROM threads t WHERE t.id = u.thread_id), u.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL
       OR COALESCE((SELECT t.space_id FROM threads t WHERE t.id = u.thread_id), u.space_id, '') = CAST(sqlc.narg(space) AS TEXT))
  AND (CAST(sqlc.narg(model) AS TEXT) IS NULL OR u.provider || '/' || u.model = CAST(sqlc.narg(model) AS TEXT))
GROUP BY 1, 2;

-- Same parameters (from_q / to_q instead of from_ts / to_ts), dim codes and output as StatsUsageAgg, over the
-- 15-minute rollup usage_q (q = ts / 900000; '' instead of NULL; deleted threads already sit under thread_id '').
-- bucket = (q * 900000 + offset_ms) / bucket_ms, exact when offset_ms and bucket_ms are multiples of 900000.
-- name: StatsUsageAggQ :many
SELECT
    CAST(CASE WHEN CAST(sqlc.arg(by_bucket) AS BOOLEAN)
         THEN (x.q * 900000 + CAST(sqlc.arg(offset_ms) AS INTEGER)) / CAST(sqlc.arg(bucket_ms) AS INTEGER) ELSE 0 END AS INTEGER) AS bucket,
    CAST(CASE CAST(sqlc.arg(dim) AS INTEGER)
         WHEN 1 THEN x.thread_id
         WHEN 2 THEN COALESCE((SELECT t.project_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.project_id, ''), '')
         WHEN 3 THEN COALESCE((SELECT t.space_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.space_id, ''), '')
         WHEN 4 THEN x.provider || '/' || x.model
         WHEN 5 THEN x.purpose
         ELSE '' END AS TEXT) AS "key",
    CAST(COALESCE(SUM(x.requests), 0) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(x.request_errors), 0) AS INTEGER) AS request_errors,
    CAST(COALESCE(SUM(x.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COALESCE(SUM(x.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COALESCE(SUM(x.cache_read_tokens), 0) AS INTEGER) AS cache_read_tokens,
    CAST(COALESCE(SUM(x.cache_write_tokens), 0) AS INTEGER) AS cache_write_tokens,
    CAST(COALESCE(SUM(x.cost_usd), 0) AS REAL) AS cost_usd,
    CAST(COALESCE(SUM(x.subagent_cost_usd), 0) AS REAL) AS subagent_cost_usd,
    CAST(COALESCE(SUM(x.ttft_sum_ms), 0) AS INTEGER) AS ttft_sum_ms,
    CAST(COALESCE(SUM(x.ttft_n), 0) AS INTEGER) AS ttft_n,
    CAST(COALESCE(SUM(x.timed_output_tokens), 0) AS INTEGER) AS timed_output_tokens,
    CAST(COALESCE(SUM(x.duration_ms), 0) AS INTEGER) AS duration_ms
FROM usage_q x
WHERE x.q >= @from_q AND x.q < @to_q
  AND (CAST(sqlc.narg(thread) AS TEXT) IS NULL OR (x.thread_id = CAST(sqlc.narg(thread) AS TEXT) AND x.thread_id <> ''))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL
       OR COALESCE((SELECT t.project_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.project_id, ''), '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL
       OR COALESCE((SELECT t.space_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.space_id, ''), '') = CAST(sqlc.narg(space) AS TEXT))
  AND (CAST(sqlc.narg(model) AS TEXT) IS NULL OR x.provider || '/' || x.model = CAST(sqlc.narg(model) AS TEXT))
GROUP BY 1, 2;

-- dim 1 / 2 / 3 / 6 (see StatsUsageAgg).
-- name: StatsToolAgg :many
SELECT
    CAST(CASE WHEN CAST(sqlc.arg(by_bucket) AS BOOLEAN)
         THEN (c.ts + CAST(sqlc.arg(offset_ms) AS INTEGER)) / CAST(sqlc.arg(bucket_ms) AS INTEGER) ELSE 0 END AS INTEGER) AS bucket,
    CAST(CASE CAST(sqlc.arg(dim) AS INTEGER)
         WHEN 1 THEN COALESCE(c.thread_id, '')
         WHEN 2 THEN COALESCE((SELECT t.project_id FROM threads t WHERE t.id = c.thread_id), c.project_id, '')
         WHEN 3 THEN COALESCE((SELECT t.space_id FROM threads t WHERE t.id = c.thread_id), c.space_id, '')
         WHEN 6 THEN c.tool
         ELSE '' END AS TEXT) AS "key",
    CAST(COUNT(*) AS INTEGER) AS calls,
    CAST(COALESCE(SUM(c.is_error), 0) AS INTEGER) AS errors
FROM tool_calls c
WHERE c.ts >= @from_ts AND c.ts < @to_ts
  AND (CAST(sqlc.narg(thread) AS TEXT) IS NULL OR c.thread_id = CAST(sqlc.narg(thread) AS TEXT))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL
       OR COALESCE((SELECT t.project_id FROM threads t WHERE t.id = c.thread_id), c.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL
       OR COALESCE((SELECT t.space_id FROM threads t WHERE t.id = c.thread_id), c.space_id, '') = CAST(sqlc.narg(space) AS TEXT))
GROUP BY 1, 2;

-- StatsToolAgg over the rollup tool_q (see StatsUsageAggQ).
-- name: StatsToolAggQ :many
SELECT
    CAST(CASE WHEN CAST(sqlc.arg(by_bucket) AS BOOLEAN)
         THEN (x.q * 900000 + CAST(sqlc.arg(offset_ms) AS INTEGER)) / CAST(sqlc.arg(bucket_ms) AS INTEGER) ELSE 0 END AS INTEGER) AS bucket,
    CAST(CASE CAST(sqlc.arg(dim) AS INTEGER)
         WHEN 1 THEN x.thread_id
         WHEN 2 THEN COALESCE((SELECT t.project_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.project_id, ''), '')
         WHEN 3 THEN COALESCE((SELECT t.space_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.space_id, ''), '')
         WHEN 6 THEN x.tool
         ELSE '' END AS TEXT) AS "key",
    CAST(COALESCE(SUM(x.calls), 0) AS INTEGER) AS calls,
    CAST(COALESCE(SUM(x.errors), 0) AS INTEGER) AS errors
FROM tool_q x
WHERE x.q >= @from_q AND x.q < @to_q
  AND (CAST(sqlc.narg(thread) AS TEXT) IS NULL OR (x.thread_id = CAST(sqlc.narg(thread) AS TEXT) AND x.thread_id <> ''))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL
       OR COALESCE((SELECT t.project_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.project_id, ''), '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL
       OR COALESCE((SELECT t.space_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.space_id, ''), '') = CAST(sqlc.narg(space) AS TEXT))
GROUP BY 1, 2;

-- dim 1 / 2 / 3 (see StatsUsageAgg).
-- name: StatsTurnAgg :many
SELECT
    CAST(CASE WHEN CAST(sqlc.arg(by_bucket) AS BOOLEAN)
         THEN (r.ts + CAST(sqlc.arg(offset_ms) AS INTEGER)) / CAST(sqlc.arg(bucket_ms) AS INTEGER) ELSE 0 END AS INTEGER) AS bucket,
    CAST(CASE CAST(sqlc.arg(dim) AS INTEGER)
         WHEN 1 THEN COALESCE(r.thread_id, '')
         WHEN 2 THEN COALESCE((SELECT t.project_id FROM threads t WHERE t.id = r.thread_id), r.project_id, '')
         WHEN 3 THEN COALESCE((SELECT t.space_id FROM threads t WHERE t.id = r.thread_id), r.space_id, '')
         ELSE '' END AS TEXT) AS "key",
    CAST(COUNT(*) AS INTEGER) AS turns
FROM turns r
WHERE r.ts >= @from_ts AND r.ts < @to_ts
  AND (CAST(sqlc.narg(thread) AS TEXT) IS NULL OR r.thread_id = CAST(sqlc.narg(thread) AS TEXT))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL
       OR COALESCE((SELECT t.project_id FROM threads t WHERE t.id = r.thread_id), r.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL
       OR COALESCE((SELECT t.space_id FROM threads t WHERE t.id = r.thread_id), r.space_id, '') = CAST(sqlc.narg(space) AS TEXT))
GROUP BY 1, 2;

-- StatsTurnAgg over the rollup turn_q (see StatsUsageAggQ).
-- name: StatsTurnAggQ :many
SELECT
    CAST(CASE WHEN CAST(sqlc.arg(by_bucket) AS BOOLEAN)
         THEN (x.q * 900000 + CAST(sqlc.arg(offset_ms) AS INTEGER)) / CAST(sqlc.arg(bucket_ms) AS INTEGER) ELSE 0 END AS INTEGER) AS bucket,
    CAST(CASE CAST(sqlc.arg(dim) AS INTEGER)
         WHEN 1 THEN x.thread_id
         WHEN 2 THEN COALESCE((SELECT t.project_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.project_id, ''), '')
         WHEN 3 THEN COALESCE((SELECT t.space_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.space_id, ''), '')
         ELSE '' END AS TEXT) AS "key",
    CAST(COALESCE(SUM(x.turns), 0) AS INTEGER) AS turns
FROM turn_q x
WHERE x.q >= @from_q AND x.q < @to_q
  AND (CAST(sqlc.narg(thread) AS TEXT) IS NULL OR (x.thread_id = CAST(sqlc.narg(thread) AS TEXT) AND x.thread_id <> ''))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL
       OR COALESCE((SELECT t.project_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.project_id, ''), '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL
       OR COALESCE((SELECT t.space_id FROM threads t WHERE t.id = x.thread_id), NULLIF(x.space_id, ''), '') = CAST(sqlc.narg(space) AS TEXT))
GROUP BY 1, 2;

-- Earliest usage fact time; 0 when there is none.
-- name: MinUsageTs :one
SELECT CAST(COALESCE(MIN(ts), 0) AS INTEGER) AS min_ts FROM usage_records;

-- name: SumUsageCost :one
SELECT CAST(COALESCE(SUM(cost_usd), 0) AS REAL) AS cost_usd FROM usage_records WHERE ts >= @from_ts AND ts < @to_ts;

-- name: CountUsageRecords :one
SELECT CAST(COUNT(*) AS INTEGER) AS n
FROM usage_records u
WHERE u.ts >= @from_ts AND u.ts < @to_ts
  AND (CAST(sqlc.narg(thread) AS TEXT) IS NULL OR u.thread_id = CAST(sqlc.narg(thread) AS TEXT))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL
       OR COALESCE((SELECT t.project_id FROM threads t WHERE t.id = u.thread_id), u.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL
       OR COALESCE((SELECT t.space_id FROM threads t WHERE t.id = u.thread_id), u.space_id, '') = CAST(sqlc.narg(space) AS TEXT))
  AND (CAST(sqlc.narg(model) AS TEXT) IS NULL OR u.provider || '/' || u.model = CAST(sqlc.narg(model) AS TEXT));

-- Keyset page of usage facts with their current attribution. First page: after_ts = from_ts, after_id = 0.
-- name: ListUsageRecordsPage :many
SELECT u.id, u.ts,
    CAST(COALESCE(u.thread_id, '') AS TEXT) AS thread_id,
    CAST(COALESCE(t.project_id, u.project_id, '') AS TEXT) AS project_id,
    CAST(COALESCE(t.space_id, u.space_id, '') AS TEXT) AS space_id,
    u.provider, u.model, u.purpose, u.is_subagent, u.stop_reason,
    u.input_tokens, u.output_tokens, u.cache_read_tokens, u.cache_write_tokens, u.cost_usd, u.duration_ms, u.ttft_ms
FROM usage_records u LEFT JOIN threads t ON t.id = u.thread_id
WHERE u.ts >= @from_ts AND u.ts < @to_ts
  AND (u.ts > @after_ts OR (u.ts = @after_ts AND u.id > @after_id))
  AND (CAST(sqlc.narg(thread) AS TEXT) IS NULL OR u.thread_id = CAST(sqlc.narg(thread) AS TEXT))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL OR COALESCE(t.project_id, u.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL OR COALESCE(t.space_id, u.space_id, '') = CAST(sqlc.narg(space) AS TEXT))
  AND (CAST(sqlc.narg(model) AS TEXT) IS NULL OR u.provider || '/' || u.model = CAST(sqlc.narg(model) AS TEXT))
ORDER BY u.ts, u.id
LIMIT @limit;

-- ---------------------------------------------------------------- orbs

-- Orbs that may have existed inside [from_ts, to_ts); state_at_from = to_state of the last event before from_ts
-- ('' = no event yet).
-- name: ListOrbsForStats :many
SELECT o.id, o.type, o.cpus, o.memory_mb, o.project_id, o.created_at, o.deleted_at, o.snapshot_of, o.coordinator,
    CAST(COALESCE((SELECT e.to_state FROM orb_events e WHERE e.orb_id = o.id AND e.ts < @from_ts
                   ORDER BY e.ts DESC, e.id DESC LIMIT 1), '') AS TEXT) AS state_at_from
FROM orbs o
WHERE o.created_at < @to_ts AND (o.deleted_at IS NULL OR o.deleted_at >= @from_ts)
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL OR COALESCE(o.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(orb) AS TEXT) IS NULL OR o.id = CAST(sqlc.narg(orb) AS TEXT))
ORDER BY o.id;

-- Transitions inside [from_ts, to_ts), with the same optional project / orb filters as ListOrbsForStats.
-- name: ListOrbEventsInWindow :many
SELECT e.orb_id, e.ts, e.from_state, e.to_state
FROM orb_events e JOIN orbs o ON o.id = e.orb_id
WHERE e.ts >= @from_ts AND e.ts < @to_ts
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL OR COALESCE(o.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(orb) AS TEXT) IS NULL OR e.orb_id = CAST(sqlc.narg(orb) AS TEXT))
ORDER BY e.orb_id, e.ts, e.id;

-- One orb: events from the last one before from_ts (0 when none) up to to_ts, at most 5000.
-- name: ListOrbEventsForTimeline :many
SELECT e.id, e.ts, e.from_state, e.to_state, e.detail
FROM orb_events e
WHERE e.orb_id = @orb_id AND e.ts < @to_ts
  AND e.ts >= COALESCE((SELECT MAX(p.ts) FROM orb_events p WHERE p.orb_id = @orb_id AND p.ts < @from_ts), 0)
ORDER BY e.ts, e.id
LIMIT 5000;

-- name: ListSetupRunsForStats :many
SELECT r.orb_id, r.phase, r.ok, r.setup_ran, r.duration_ms, r.started_at, o.type
FROM orb_setup_runs r JOIN orbs o ON o.id = r.orb_id
WHERE r.started_at >= @from_ts AND r.started_at < @to_ts
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL OR COALESCE(o.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(orb) AS TEXT) IS NULL OR r.orb_id = CAST(sqlc.narg(orb) AS TEXT))
ORDER BY r.started_at, r.id;

-- ---------------------------------------------------------------- output

-- Work threads created, archived or shipped inside [from_ts, to_ts).
-- name: ListThreadTimesForStats :many
SELECT id, project_id, space_id, created_at, archived_at, shipped_at
FROM threads
WHERE kind = 'work'
  AND ((created_at >= @from_ts AND created_at < @to_ts)
    OR (archived_at >= @from_ts AND archived_at < @to_ts)
    OR (shipped_at >= @from_ts AND shipped_at < @to_ts))
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL OR COALESCE(project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL OR COALESCE(space_id, '') = CAST(sqlc.narg(space) AS TEXT))
ORDER BY created_at, id;

-- Work threads shipped inside [from_ts, to_ts) with their whole-lifetime cost.
-- name: ListShippedThreadCosts :many
SELECT t.id, t.project_id, t.space_id, CAST(t.shipped_at AS INTEGER) AS shipped_at,
    CAST(COALESCE((SELECT SUM(u.cost_usd) FROM usage_records u WHERE u.thread_id = t.id), 0) AS REAL) AS cost_usd
FROM threads t
WHERE t.kind = 'work' AND t.shipped_at >= CAST(@from_ts AS INTEGER) AND t.shipped_at < CAST(@to_ts AS INTEGER)
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL OR COALESCE(t.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL OR COALESCE(t.space_id, '') = CAST(sqlc.narg(space) AS TEXT))
ORDER BY t.shipped_at, t.id;

-- Distinct commit shas per group (a sha shared by forked threads counts once).
-- name: StatsCommitAgg :many
SELECT
    CAST(CASE WHEN CAST(sqlc.arg(by_bucket) AS BOOLEAN)
         THEN (c.ts + CAST(sqlc.arg(offset_ms) AS INTEGER)) / CAST(sqlc.arg(bucket_ms) AS INTEGER) ELSE 0 END AS INTEGER) AS bucket,
    CAST(CASE WHEN CAST(sqlc.arg(by_project) AS BOOLEAN) THEN COALESCE(t.project_id, '') ELSE '' END AS TEXT) AS project_id,
    CAST(CASE WHEN CAST(sqlc.arg(by_space) AS BOOLEAN) THEN COALESCE(t.space_id, '') ELSE '' END AS TEXT) AS space_id,
    CAST(COUNT(DISTINCT c.sha) AS INTEGER) AS commits
FROM thread_commits c JOIN threads t ON t.id = c.thread_id
WHERE c.ts >= @from_ts AND c.ts < @to_ts
  AND (CAST(sqlc.narg(project) AS TEXT) IS NULL OR COALESCE(t.project_id, '') = CAST(sqlc.narg(project) AS TEXT))
  AND (CAST(sqlc.narg(space) AS TEXT) IS NULL OR COALESCE(t.space_id, '') = CAST(sqlc.narg(space) AS TEXT))
GROUP BY 1, 2, 3;

-- name: DeleteThreadCommits :exec
DELETE FROM thread_commits WHERE thread_id = @thread_id;

-- name: InsertThreadCommit :exec
INSERT OR IGNORE INTO thread_commits (thread_id, sha, ts) VALUES (@thread_id, @sha, @ts);

-- ---------------------------------------------------------------- state and budget

-- Keys: facts_version, backfill_cursor ("<created_at>:<thread_id>"), backfill_done, backfill_total.
-- name: GetStatsState :one
SELECT value FROM stats_state WHERE key = @key;

-- name: SetStatsState :exec
INSERT INTO stats_state (key, value) VALUES (@key, @value)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- name: DeleteStatsState :exec
DELETE FROM stats_state WHERE key = @key;

-- name: GetBudget :one
SELECT * FROM budget WHERE id = 1;

-- name: UpsertBudget :exec
INSERT INTO budget (id, daily_usd, monthly_usd, warn_pct, timezone, enabled, updated_at)
VALUES (1, @daily_usd, @monthly_usd, @warn_pct, @timezone, @enabled, @updated_at)
ON CONFLICT (id) DO UPDATE SET
    daily_usd   = excluded.daily_usd,
    monthly_usd = excluded.monthly_usd,
    warn_pct    = excluded.warn_pct,
    timezone    = excluded.timezone,
    enabled     = excluded.enabled,
    updated_at  = excluded.updated_at;

-- At most one row per (period, period_key, level): 0 rows = already alerted.
-- name: InsertBudgetAlert :execrows
INSERT OR IGNORE INTO budget_alerts (period, period_key, level, spent_usd, limit_usd, created_at)
VALUES (@period, @period_key, @level, @spent_usd, @limit_usd, @created_at);

-- ---------------------------------------------------------------- retention

-- Only events of orbs deleted before the cutoff (live orbs' events drive OrbEverReady / FailureReason).
-- name: DeleteDeadOrbEventsBefore :execrows
DELETE FROM orb_events
WHERE orb_id IN (SELECT id FROM orbs WHERE deleted_at IS NOT NULL AND deleted_at < CAST(@before AS INTEGER));

-- name: DeleteDeadOrbSetupRunsBefore :execrows
DELETE FROM orb_setup_runs
WHERE orb_id IN (SELECT id FROM orbs WHERE deleted_at IS NOT NULL AND deleted_at < CAST(@before AS INTEGER));

-- name: DeleteUsageBefore :execrows
DELETE FROM usage_records WHERE ts < @before;

-- name: DeleteToolCallsBefore :execrows
DELETE FROM tool_calls WHERE ts < @before;

-- name: DeleteTurnsBefore :execrows
DELETE FROM turns WHERE ts < @before;

-- name: DeleteBudgetAlertsBefore :execrows
DELETE FROM budget_alerts WHERE created_at < @before;
