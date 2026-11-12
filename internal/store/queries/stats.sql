-- Usage records, tool calls and basic aggregates (M2 stats endpoints).

-- name: InsertUsageRecord :exec
INSERT OR IGNORE INTO usage_records (thread_id, entry_seq, ts, provider, model, input_tokens, output_tokens,
    cache_read_tokens, cache_write_tokens, cost_usd, duration_ms, ttft_ms, is_subagent)
VALUES (@thread_id, @entry_seq, @ts, @provider, @model, @input_tokens, @output_tokens,
    @cache_read_tokens, @cache_write_tokens, @cost_usd, @duration_ms, @ttft_ms, @is_subagent);

-- name: ListUsageRecords :many
SELECT * FROM usage_records WHERE ts >= @from_ts AND ts < @to_ts ORDER BY ts, id;

-- name: ListThreadUsageRecords :many
SELECT * FROM usage_records WHERE thread_id = @thread_id ORDER BY ts, id;

-- name: UsageByModel :many
SELECT provider, model,
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(SUM(input_tokens) AS INTEGER) AS input_tokens,
    CAST(SUM(output_tokens) AS INTEGER) AS output_tokens,
    CAST(SUM(cache_read_tokens) AS INTEGER) AS cache_read_tokens,
    CAST(SUM(cache_write_tokens) AS INTEGER) AS cache_write_tokens,
    CAST(SUM(cost_usd) AS REAL) AS cost_usd
FROM usage_records WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY provider, model ORDER BY cost_usd DESC, provider, model;

-- day = UTC day number (ts / 86400000).
-- name: UsageByDay :many
SELECT CAST(ts / 86400000 AS INTEGER) AS day,
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(SUM(input_tokens) AS INTEGER) AS input_tokens,
    CAST(SUM(output_tokens) AS INTEGER) AS output_tokens,
    CAST(SUM(cache_read_tokens) AS INTEGER) AS cache_read_tokens,
    CAST(SUM(cache_write_tokens) AS INTEGER) AS cache_write_tokens,
    CAST(SUM(cost_usd) AS REAL) AS cost_usd
FROM usage_records WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY day ORDER BY day;

-- name: UsageByThread :many
SELECT CAST(COALESCE(thread_id, '') AS TEXT) AS thread_id,
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(SUM(input_tokens) AS INTEGER) AS input_tokens,
    CAST(SUM(output_tokens) AS INTEGER) AS output_tokens,
    CAST(SUM(cache_read_tokens) AS INTEGER) AS cache_read_tokens,
    CAST(SUM(cache_write_tokens) AS INTEGER) AS cache_write_tokens,
    CAST(SUM(cost_usd) AS REAL) AS cost_usd
FROM usage_records WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY thread_id ORDER BY cost_usd DESC, thread_id;

-- name: UsageByProject :many
SELECT CAST(COALESCE(t.project_id, '') AS TEXT) AS project_id,
    CAST(COUNT(*) AS INTEGER) AS requests,
    CAST(SUM(u.input_tokens) AS INTEGER) AS input_tokens,
    CAST(SUM(u.output_tokens) AS INTEGER) AS output_tokens,
    CAST(SUM(u.cache_read_tokens) AS INTEGER) AS cache_read_tokens,
    CAST(SUM(u.cache_write_tokens) AS INTEGER) AS cache_write_tokens,
    CAST(SUM(u.cost_usd) AS REAL) AS cost_usd
FROM usage_records u LEFT JOIN threads t ON t.id = u.thread_id
WHERE u.ts >= @from_ts AND u.ts < @to_ts
GROUP BY project_id ORDER BY cost_usd DESC, project_id;

-- name: InsertToolCall :exec
INSERT OR IGNORE INTO tool_calls (thread_id, entry_seq, ts, tool, is_error)
VALUES (@thread_id, @entry_seq, @ts, @tool, @is_error);

-- name: ToolCallStats :many
SELECT tool,
    CAST(COUNT(*) AS INTEGER) AS calls,
    CAST(SUM(is_error) AS INTEGER) AS errors
FROM tool_calls WHERE ts >= @from_ts AND ts < @to_ts
GROUP BY tool ORDER BY calls DESC, tool;
