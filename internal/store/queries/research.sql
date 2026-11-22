-- M26: background research of the global assistant (settings, follow-up topics, runs, mining cursors).

-- ---------- settings ----------

-- name: GetResearchSettings :one
SELECT * FROM research_settings WHERE id = 1;

-- Writes the owner-editable fields; cursors and capped_day stay.
-- name: UpsertResearchSettings :exec
INSERT INTO research_settings (id, enabled, web, mine, internal, mcp, model, daily_usd, preferences, mcp_tools, updated_at)
VALUES (1, @enabled, @web, @mine, @internal, @mcp, sqlc.narg(model), @daily_usd, @preferences, @mcp_tools, @updated_at)
ON CONFLICT (id) DO UPDATE SET
    enabled = excluded.enabled, web = excluded.web, mine = excluded.mine, internal = excluded.internal,
    mcp = excluded.mcp, model = excluded.model, daily_usd = excluded.daily_usd, preferences = excluded.preferences,
    mcp_tools = excluded.mcp_tools, updated_at = excluded.updated_at;

-- name: SetResearchInternalCursor :exec
INSERT INTO research_settings (id, internal_cursor, updated_at) VALUES (1, @cursor, @updated_at)
ON CONFLICT (id) DO UPDATE SET internal_cursor = excluded.internal_cursor;

-- name: SetResearchMcpCursor :exec
INSERT INTO research_settings (id, mcp_cursor, updated_at) VALUES (1, @cursor, @updated_at)
ON CONFLICT (id) DO UPDATE SET mcp_cursor = excluded.mcp_cursor;

-- name: SetResearchMineSince :exec
INSERT INTO research_settings (id, mine_since, updated_at) VALUES (1, @since, @updated_at)
ON CONFLICT (id) DO UPDATE SET mine_since = excluded.mine_since;

-- name: SetResearchCappedDay :exec
INSERT INTO research_settings (id, capped_day, updated_at) VALUES (1, @day, @updated_at)
ON CONFLICT (id) DO UPDATE SET capped_day = excluded.capped_day;

-- ---------- topics ----------

-- name: InsertResearchTopic :exec
INSERT INTO research_topics (id, kind, title, question, baseline, origin, source_thread_id, state, interval_s,
                             next_run_at, expires_at, created_at, updated_at)
VALUES (@id, @kind, @title, @question, @baseline, @origin, sqlc.narg(source_thread_id), @state, @interval_s,
        sqlc.narg(next_run_at), sqlc.narg(expires_at), @created_at, @updated_at);

-- name: GetResearchTopic :one
SELECT * FROM research_topics WHERE id = @id;

-- state NULL = every state except dismissed; 'all' = every state.
-- name: ListResearchTopics :many
SELECT * FROM research_topics
WHERE (CAST(sqlc.narg(state) AS TEXT) IS NULL AND state <> 'dismissed')
   OR CAST(sqlc.narg(state) AS TEXT) = 'all'
   OR state = CAST(sqlc.narg(state) AS TEXT)
ORDER BY created_at DESC, id DESC;

-- Titles of every topic in every state (duplicate check).
-- name: ListResearchTopicTitles :many
SELECT title FROM research_topics;

-- name: CountActiveResearchTopics :one
SELECT COUNT(*) FROM research_topics WHERE state = 'active';

-- name: UpdateResearchTopicMeta :execrows
UPDATE research_topics SET title = @title, question = @question, kind = @kind, expires_at = sqlc.narg(expires_at),
    updated_at = @updated_at
WHERE id = @id;

-- name: SetResearchTopicState :execrows
UPDATE research_topics SET state = @state, next_run_at = sqlc.narg(next_run_at), updated_at = @updated_at
WHERE id = @id;

-- name: NextDueResearchTopic :one
SELECT * FROM research_topics WHERE state = 'active' AND next_run_at <= @now ORDER BY next_run_at, id LIMIT 1;

-- Records one finished run of a topic (report and sources only change when the caller passes the new ones).
-- name: RecordResearchTopicRun :exec
UPDATE research_topics SET last_run_at = @last_run_at, run_count = run_count + 1, interval_s = @interval_s,
    next_run_at = sqlc.narg(next_run_at), state = @state, last_report = @last_report, last_sources = @last_sources,
    updated_at = @last_run_at
WHERE id = @id;

-- ---------- runs ----------

-- name: InsertResearchRun :exec
INSERT INTO research_runs (id, pipeline, topic_id, subject, status, started_at)
VALUES (@id, @pipeline, sqlc.narg(topic_id), @subject, 'running', @started_at);

-- name: GetResearchRun :one
SELECT * FROM research_runs WHERE id = @id;

-- name: FinishResearchRun :exec
UPDATE research_runs SET status = @status, outcome = @outcome, report = @report, error = sqlc.narg(error),
    finished_at = @finished_at
WHERE id = @id;

-- name: ListResearchRuns :many
SELECT * FROM research_runs ORDER BY started_at DESC, id DESC LIMIT @lim;

-- name: ListRunningResearchRuns :many
SELECT * FROM research_runs WHERE status = 'running' ORDER BY started_at;

-- Reports of recent runs of one pipeline that produced findings (titles already reported).
-- name: ListRecentResearchReports :many
SELECT report FROM research_runs
WHERE pipeline = @pipeline AND status = 'completed' AND outcome LIKE 'findings:%'
ORDER BY started_at DESC LIMIT @lim;

-- name: DeleteOldResearchRuns :execrows
DELETE FROM research_runs WHERE started_at < @before AND status <> 'running';

-- ---------- mining ----------

-- Idle, visible assistant conversations with new messages since the last mining pass.
-- name: ListDueResearchMineThreads :many
SELECT t.id, t.last_message_at, CAST(COALESCE(m.last_seq, 0) AS INTEGER) AS last_seq
FROM threads t LEFT JOIN research_mined m ON m.thread_id = t.id
WHERE t.kind = 'assistant' AND t.hidden = 0 AND t.archived_at IS NULL AND t.state = 'idle'
  AND t.last_message_at >= @since AND t.last_message_at <= @idle_before
  AND (m.thread_id IS NULL OR m.last_message_at IS NULL OR m.last_message_at <> t.last_message_at)
ORDER BY t.last_message_at
LIMIT @lim;

-- name: UpsertResearchMined :exec
INSERT INTO research_mined (thread_id, last_message_at, last_seq, mined_at)
VALUES (@thread_id, sqlc.narg(last_message_at), @last_seq, @mined_at)
ON CONFLICT (thread_id) DO UPDATE SET
    last_message_at = excluded.last_message_at, last_seq = excluded.last_seq, mined_at = excluded.mined_at;

-- ---------- internal correlation ----------

-- name: CountWorkThreadsActiveSince :one
SELECT COUNT(*) FROM threads WHERE kind = 'work' AND hidden = 0 AND last_message_at > @since;
