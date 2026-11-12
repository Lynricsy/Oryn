-- Lifecycle (jobs / providers / orbs / stats) helper queries.

-- Takes over or renews the provider lease for an instance, atomically: only when no other instance holds an
-- unexpired lease (or the stored provider allows multiple instances). 0 rows = lease held by another instance.
-- name: AcquireProviderLease :execrows
UPDATE providers SET instance_id = @instance_id, lease_until = @lease_until
WHERE id = @id AND disabled_at IS NULL
  AND (instance_id IS NULL OR instance_id = @instance_id OR lease_until IS NULL OR lease_until <= @now OR allow_multiple);

-- Replaces job params (used to scrub the orb token after a spawn job finishes).
-- name: SetJobParams :exec
UPDATE jobs SET params = @params WHERE id = @id;

-- Requeues one specific claim after a failed attempt (fenced by claim_gen).
-- name: RequeueFailedJob :execrows
UPDATE jobs SET state = 'queued', claimed_at = NULL, lease_until = NULL, output = @output, error = @error
WHERE id = @id AND state = 'claimed' AND claim_gen = @claim_gen;

-- name: ListUnfinishedJobsByOrb :many
SELECT * FROM jobs WHERE orb_id = @orb_id AND state IN ('queued', 'claimed') ORDER BY created_at, id;

-- name: OrbEverReady :one
SELECT CAST(EXISTS (SELECT 1 FROM orb_events WHERE orb_id = @orb_id AND to_state = 'ready') AS BOOLEAN);

-- name: ListThreadIDsByOrb :many
SELECT id FROM threads WHERE orb_id = @orb_id ORDER BY created_at, id;

-- name: ListLiveOrbs :many
SELECT * FROM orbs WHERE state <> 'deleted' ORDER BY created_at, id;

-- Events of orbs that may have existed inside [from_ts, to_ts), up to to_ts.
-- name: ListOrbEventsForStats :many
SELECT e.orb_id, e.ts, e.from_state, e.to_state, o.type
FROM orb_events e JOIN orbs o ON o.id = e.orb_id
WHERE e.ts < @to_ts AND (o.deleted_at IS NULL OR o.deleted_at >= @from_ts)
ORDER BY e.orb_id, e.ts, e.id;

-- name: StatsUsageByDay :many
SELECT CAST(ts / 86400000 AS INTEGER) AS day,
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COALESCE(SUM(cache_read_tokens), 0) AS INTEGER) AS cache_read_tokens,
    CAST(COALESCE(SUM(cache_write_tokens), 0) AS INTEGER) AS cache_write_tokens,
    CAST(COALESCE(SUM(cost_usd), 0) AS REAL) AS cost_usd,
    CAST(COALESCE(SUM(CASE WHEN is_subagent THEN cost_usd ELSE 0 END), 0) AS REAL) AS subagent_cost_usd,
    CAST(COALESCE(AVG(ttft_ms), -1) AS REAL) AS avg_ttft_ms,
    CAST(COALESCE(SUM(CASE WHEN duration_ms > 0 THEN output_tokens ELSE 0 END), 0) AS INTEGER) AS timed_output_tokens,
    CAST(COALESCE(SUM(CASE WHEN duration_ms > 0 THEN duration_ms ELSE 0 END), 0) AS INTEGER) AS duration_ms
FROM usage_records WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY day ORDER BY day;

-- name: StatsUsageByModel :many
SELECT provider, model,
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COALESCE(SUM(cache_read_tokens), 0) AS INTEGER) AS cache_read_tokens,
    CAST(COALESCE(SUM(cache_write_tokens), 0) AS INTEGER) AS cache_write_tokens,
    CAST(COALESCE(SUM(cost_usd), 0) AS REAL) AS cost_usd,
    CAST(COALESCE(SUM(CASE WHEN is_subagent THEN cost_usd ELSE 0 END), 0) AS REAL) AS subagent_cost_usd,
    CAST(COALESCE(AVG(ttft_ms), -1) AS REAL) AS avg_ttft_ms,
    CAST(COALESCE(SUM(CASE WHEN duration_ms > 0 THEN output_tokens ELSE 0 END), 0) AS INTEGER) AS timed_output_tokens,
    CAST(COALESCE(SUM(CASE WHEN duration_ms > 0 THEN duration_ms ELSE 0 END), 0) AS INTEGER) AS duration_ms
FROM usage_records WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY provider, model ORDER BY cost_usd DESC, provider, model;

-- name: StatsUsageByThread :many
SELECT CAST(COALESCE(u.thread_id, '') AS TEXT) AS thread_id,
    CAST(COALESCE(MAX(t.title), '') AS TEXT) AS title,
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(u.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COALESCE(SUM(u.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COALESCE(SUM(u.cache_read_tokens), 0) AS INTEGER) AS cache_read_tokens,
    CAST(COALESCE(SUM(u.cache_write_tokens), 0) AS INTEGER) AS cache_write_tokens,
    CAST(COALESCE(SUM(u.cost_usd), 0) AS REAL) AS cost_usd,
    CAST(COALESCE(SUM(CASE WHEN u.is_subagent THEN u.cost_usd ELSE 0 END), 0) AS REAL) AS subagent_cost_usd,
    CAST(COALESCE(AVG(u.ttft_ms), -1) AS REAL) AS avg_ttft_ms,
    CAST(COALESCE(SUM(CASE WHEN u.duration_ms > 0 THEN u.output_tokens ELSE 0 END), 0) AS INTEGER) AS timed_output_tokens,
    CAST(COALESCE(SUM(CASE WHEN u.duration_ms > 0 THEN u.duration_ms ELSE 0 END), 0) AS INTEGER) AS duration_ms
FROM usage_records u LEFT JOIN threads t ON t.id = u.thread_id
WHERE u.ts >= @from_ts AND u.ts < @to_ts
GROUP BY u.thread_id ORDER BY cost_usd DESC, thread_id;

-- name: StatsUsageByProject :many
SELECT CAST(COALESCE(t.project_id, '') AS TEXT) AS project_id,
    CAST(COALESCE(MAX(p.name), '') AS TEXT) AS name,
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(COALESCE(SUM(u.input_tokens), 0) AS INTEGER) AS input_tokens,
    CAST(COALESCE(SUM(u.output_tokens), 0) AS INTEGER) AS output_tokens,
    CAST(COALESCE(SUM(u.cache_read_tokens), 0) AS INTEGER) AS cache_read_tokens,
    CAST(COALESCE(SUM(u.cache_write_tokens), 0) AS INTEGER) AS cache_write_tokens,
    CAST(COALESCE(SUM(u.cost_usd), 0) AS REAL) AS cost_usd,
    CAST(COALESCE(SUM(CASE WHEN u.is_subagent THEN u.cost_usd ELSE 0 END), 0) AS REAL) AS subagent_cost_usd,
    CAST(COALESCE(AVG(u.ttft_ms), -1) AS REAL) AS avg_ttft_ms,
    CAST(COALESCE(SUM(CASE WHEN u.duration_ms > 0 THEN u.output_tokens ELSE 0 END), 0) AS INTEGER) AS timed_output_tokens,
    CAST(COALESCE(SUM(CASE WHEN u.duration_ms > 0 THEN u.duration_ms ELSE 0 END), 0) AS INTEGER) AS duration_ms
FROM usage_records u
LEFT JOIN threads t ON t.id = u.thread_id
LEFT JOIN projects p ON p.id = t.project_id
WHERE u.ts >= @from_ts AND u.ts < @to_ts
GROUP BY t.project_id ORDER BY cost_usd DESC, project_id;

-- name: StatsToolCallsByDay :many
SELECT CAST(ts / 86400000 AS INTEGER) AS day,
    CAST(COUNT(*) AS INTEGER) AS calls,
    CAST(COALESCE(SUM(is_error), 0) AS INTEGER) AS errors
FROM tool_calls WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY day;

-- name: StatsToolCallsByThread :many
SELECT CAST(COALESCE(thread_id, '') AS TEXT) AS thread_id,
    CAST(COUNT(*) AS INTEGER) AS calls,
    CAST(COALESCE(SUM(is_error), 0) AS INTEGER) AS errors
FROM tool_calls WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY thread_id;

-- name: StatsToolCallsByProject :many
SELECT CAST(COALESCE(t.project_id, '') AS TEXT) AS project_id,
    CAST(COUNT(*) AS INTEGER) AS calls,
    CAST(COALESCE(SUM(c.is_error), 0) AS INTEGER) AS errors
FROM tool_calls c LEFT JOIN threads t ON t.id = c.thread_id
WHERE c.ts >= @from_ts AND c.ts < @to_ts
GROUP BY t.project_id;

-- Like ClaimNextJob, but serialized per orb: skips a job while another job of the same orb is
-- claimed, or an older job of the same orb is still queued (per-orb FIFO). Orb-less jobs never block.
-- name: ClaimNextJobSerial :one
UPDATE jobs SET state = 'claimed', attempt = attempt + 1, claim_gen = claim_gen + 1,
    claimed_at = @claimed_at, lease_until = @lease_until
WHERE id = (
    SELECT j.id FROM jobs j
    WHERE j.provider_id = @provider_id AND j.state = 'queued'
      AND NOT EXISTS (SELECT 1 FROM jobs c WHERE c.orb_id = j.orb_id AND c.state = 'claimed')
      AND NOT EXISTS (SELECT 1 FROM jobs e WHERE e.orb_id = j.orb_id AND e.state = 'queued'
                      AND (e.created_at < j.created_at OR (e.created_at = j.created_at AND e.id < j.id)))
    ORDER BY j.created_at, j.id LIMIT 1
)
RETURNING *;
