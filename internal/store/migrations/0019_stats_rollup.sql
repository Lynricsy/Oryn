-- +goose Up
-- M10 补充：事实行的 15 分钟汇总（q = ts / 900000）。所有真实时区偏移与夏令时切换都落在 15 分钟边界上，
-- 所以本地小时 / 日 / 周 / 月桶都由整数个 q 拼成，按汇总求出的结果与按原始行求出的完全相等。
-- 汇总只由下面的触发器维护（写入路径不变）；NULL 一律存成空串以便进主键。project_id / space_id 是事实行上的
-- 归属快照列（活线程为空串、删线程时由 Tombstone 写入），不是线程的当前归属——查询时仍按线程现值优先。
CREATE TABLE usage_q (
    q                   INTEGER NOT NULL,
    thread_id           TEXT    NOT NULL,
    project_id          TEXT    NOT NULL,
    space_id            TEXT    NOT NULL,
    provider            TEXT    NOT NULL,
    model               TEXT    NOT NULL,
    purpose             TEXT    NOT NULL,
    requests            INTEGER NOT NULL,
    request_errors      INTEGER NOT NULL,
    input_tokens        INTEGER NOT NULL,
    output_tokens       INTEGER NOT NULL,
    cache_read_tokens   INTEGER NOT NULL,
    cache_write_tokens  INTEGER NOT NULL,
    cost_usd            REAL    NOT NULL,
    subagent_cost_usd   REAL    NOT NULL,
    ttft_sum_ms         INTEGER NOT NULL,
    ttft_n              INTEGER NOT NULL,
    timed_output_tokens INTEGER NOT NULL,               -- duration_ms > 0 的请求的输出 tokens
    duration_ms         INTEGER NOT NULL,               -- duration_ms > 0 的请求的时长和
    PRIMARY KEY (q, thread_id, project_id, space_id, provider, model, purpose)
) WITHOUT ROWID;

CREATE TABLE tool_q (
    q          INTEGER NOT NULL,
    thread_id  TEXT    NOT NULL,
    project_id TEXT    NOT NULL,
    space_id   TEXT    NOT NULL,
    tool       TEXT    NOT NULL,
    calls      INTEGER NOT NULL,
    errors     INTEGER NOT NULL,
    PRIMARY KEY (q, thread_id, project_id, space_id, tool)
) WITHOUT ROWID;

CREATE TABLE turn_q (
    q          INTEGER NOT NULL,
    thread_id  TEXT    NOT NULL,
    project_id TEXT    NOT NULL,
    space_id   TEXT    NOT NULL,
    turns      INTEGER NOT NULL,
    PRIMARY KEY (q, thread_id, project_id, space_id)
) WITHOUT ROWID;

-- 回填现有事实行。求和用 TOTAL（REAL，从不报错）再 CAST：旧库里可能有入库校验之前写下的越界行，
-- SUM 遇到整数溢出会抛 integer overflow 让迁移失败；CAST 过大的 REAL 到 INTEGER 会饱和。这些行之后由
-- threads.sanitizeFacts（FactsVersion 3 的回填开头）钳制并整表重建汇总。
INSERT INTO usage_q (q, thread_id, project_id, space_id, provider, model, purpose,
    requests, request_errors, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    cost_usd, subagent_cost_usd, ttft_sum_ms, ttft_n, timed_output_tokens, duration_ms)
SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), provider, model, purpose,
    COUNT(*), CAST(TOTAL(stop_reason = 'error') AS INTEGER),
    CAST(TOTAL(input_tokens) AS INTEGER), CAST(TOTAL(output_tokens) AS INTEGER),
    CAST(TOTAL(cache_read_tokens) AS INTEGER), CAST(TOTAL(cache_write_tokens) AS INTEGER),
    TOTAL(cost_usd), TOTAL(CASE WHEN is_subagent THEN cost_usd ELSE 0 END), CAST(TOTAL(ttft_ms) AS INTEGER), COUNT(ttft_ms),
    CAST(TOTAL(CASE WHEN duration_ms > 0 THEN output_tokens ELSE 0 END) AS INTEGER),
    CAST(TOTAL(CASE WHEN duration_ms > 0 THEN duration_ms ELSE 0 END) AS INTEGER)
FROM usage_records
GROUP BY 1, 2, 3, 4, 5, 6, 7;

INSERT INTO tool_q (q, thread_id, project_id, space_id, tool, calls, errors)
SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), tool, COUNT(*),
    CAST(TOTAL(is_error) AS INTEGER)
FROM tool_calls
GROUP BY 1, 2, 3, 4, 5;

INSERT INTO turn_q (q, thread_id, project_id, space_id, turns)
SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), COUNT(*)
FROM turns
GROUP BY 1, 2, 3, 4;

-- 触发器：INSERT 加到 NEW 的键上；DELETE 从 OLD 的键上减，减空（requests / calls / turns = 0）的行删掉；
-- UPDATE 先按 OLD 减再按 NEW 加（Tombstone、外键 ON DELETE SET NULL、数值改写都走这里）。
-- 被 INSERT OR IGNORE 忽略的行不触发。

-- +goose StatementBegin
CREATE TRIGGER usage_records_q_insert AFTER INSERT ON usage_records
BEGIN
    INSERT INTO usage_q (q, thread_id, project_id, space_id, provider, model, purpose,
        requests, request_errors, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
        cost_usd, subagent_cost_usd, ttft_sum_ms, ttft_n, timed_output_tokens, duration_ms)
    VALUES (NEW.ts / 900000, COALESCE(NEW.thread_id, ''), COALESCE(NEW.project_id, ''), COALESCE(NEW.space_id, ''),
        NEW.provider, NEW.model, NEW.purpose,
        1, NEW.stop_reason = 'error', NEW.input_tokens, NEW.output_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens,
        NEW.cost_usd, CASE WHEN NEW.is_subagent THEN NEW.cost_usd ELSE 0 END,
        COALESCE(NEW.ttft_ms, 0), NEW.ttft_ms IS NOT NULL,
        CASE WHEN NEW.duration_ms > 0 THEN NEW.output_tokens ELSE 0 END, CASE WHEN NEW.duration_ms > 0 THEN NEW.duration_ms ELSE 0 END)
    ON CONFLICT (q, thread_id, project_id, space_id, provider, model, purpose) DO UPDATE SET
        requests            = requests + excluded.requests,
        request_errors      = request_errors + excluded.request_errors,
        input_tokens        = input_tokens + excluded.input_tokens,
        output_tokens       = output_tokens + excluded.output_tokens,
        cache_read_tokens   = cache_read_tokens + excluded.cache_read_tokens,
        cache_write_tokens  = cache_write_tokens + excluded.cache_write_tokens,
        cost_usd            = cost_usd + excluded.cost_usd,
        subagent_cost_usd   = subagent_cost_usd + excluded.subagent_cost_usd,
        ttft_sum_ms         = ttft_sum_ms + excluded.ttft_sum_ms,
        ttft_n              = ttft_n + excluded.ttft_n,
        timed_output_tokens = timed_output_tokens + excluded.timed_output_tokens,
        duration_ms         = duration_ms + excluded.duration_ms;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER usage_records_q_delete AFTER DELETE ON usage_records
BEGIN
    UPDATE usage_q SET
        requests            = requests - 1,
        request_errors      = request_errors - (OLD.stop_reason = 'error'),
        input_tokens        = input_tokens - OLD.input_tokens,
        output_tokens       = output_tokens - OLD.output_tokens,
        cache_read_tokens   = cache_read_tokens - OLD.cache_read_tokens,
        cache_write_tokens  = cache_write_tokens - OLD.cache_write_tokens,
        cost_usd            = cost_usd - OLD.cost_usd,
        subagent_cost_usd   = subagent_cost_usd - CASE WHEN OLD.is_subagent THEN OLD.cost_usd ELSE 0 END,
        ttft_sum_ms         = ttft_sum_ms - COALESCE(OLD.ttft_ms, 0),
        ttft_n              = ttft_n - (OLD.ttft_ms IS NOT NULL),
        timed_output_tokens = timed_output_tokens - CASE WHEN OLD.duration_ms > 0 THEN OLD.output_tokens ELSE 0 END,
        duration_ms         = duration_ms - CASE WHEN OLD.duration_ms > 0 THEN OLD.duration_ms ELSE 0 END
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND provider = OLD.provider AND model = OLD.model AND purpose = OLD.purpose;
    DELETE FROM usage_q
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND provider = OLD.provider AND model = OLD.model AND purpose = OLD.purpose
      AND requests = 0;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER usage_records_q_update AFTER UPDATE ON usage_records
BEGIN
    UPDATE usage_q SET
        requests            = requests - 1,
        request_errors      = request_errors - (OLD.stop_reason = 'error'),
        input_tokens        = input_tokens - OLD.input_tokens,
        output_tokens       = output_tokens - OLD.output_tokens,
        cache_read_tokens   = cache_read_tokens - OLD.cache_read_tokens,
        cache_write_tokens  = cache_write_tokens - OLD.cache_write_tokens,
        cost_usd            = cost_usd - OLD.cost_usd,
        subagent_cost_usd   = subagent_cost_usd - CASE WHEN OLD.is_subagent THEN OLD.cost_usd ELSE 0 END,
        ttft_sum_ms         = ttft_sum_ms - COALESCE(OLD.ttft_ms, 0),
        ttft_n              = ttft_n - (OLD.ttft_ms IS NOT NULL),
        timed_output_tokens = timed_output_tokens - CASE WHEN OLD.duration_ms > 0 THEN OLD.output_tokens ELSE 0 END,
        duration_ms         = duration_ms - CASE WHEN OLD.duration_ms > 0 THEN OLD.duration_ms ELSE 0 END
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND provider = OLD.provider AND model = OLD.model AND purpose = OLD.purpose;
    DELETE FROM usage_q
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND provider = OLD.provider AND model = OLD.model AND purpose = OLD.purpose
      AND requests = 0;
    INSERT INTO usage_q (q, thread_id, project_id, space_id, provider, model, purpose,
        requests, request_errors, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
        cost_usd, subagent_cost_usd, ttft_sum_ms, ttft_n, timed_output_tokens, duration_ms)
    VALUES (NEW.ts / 900000, COALESCE(NEW.thread_id, ''), COALESCE(NEW.project_id, ''), COALESCE(NEW.space_id, ''),
        NEW.provider, NEW.model, NEW.purpose,
        1, NEW.stop_reason = 'error', NEW.input_tokens, NEW.output_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens,
        NEW.cost_usd, CASE WHEN NEW.is_subagent THEN NEW.cost_usd ELSE 0 END,
        COALESCE(NEW.ttft_ms, 0), NEW.ttft_ms IS NOT NULL,
        CASE WHEN NEW.duration_ms > 0 THEN NEW.output_tokens ELSE 0 END, CASE WHEN NEW.duration_ms > 0 THEN NEW.duration_ms ELSE 0 END)
    ON CONFLICT (q, thread_id, project_id, space_id, provider, model, purpose) DO UPDATE SET
        requests            = requests + excluded.requests,
        request_errors      = request_errors + excluded.request_errors,
        input_tokens        = input_tokens + excluded.input_tokens,
        output_tokens       = output_tokens + excluded.output_tokens,
        cache_read_tokens   = cache_read_tokens + excluded.cache_read_tokens,
        cache_write_tokens  = cache_write_tokens + excluded.cache_write_tokens,
        cost_usd            = cost_usd + excluded.cost_usd,
        subagent_cost_usd   = subagent_cost_usd + excluded.subagent_cost_usd,
        ttft_sum_ms         = ttft_sum_ms + excluded.ttft_sum_ms,
        ttft_n              = ttft_n + excluded.ttft_n,
        timed_output_tokens = timed_output_tokens + excluded.timed_output_tokens,
        duration_ms         = duration_ms + excluded.duration_ms;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tool_calls_q_insert AFTER INSERT ON tool_calls
BEGIN
    INSERT INTO tool_q (q, thread_id, project_id, space_id, tool, calls, errors)
    VALUES (NEW.ts / 900000, COALESCE(NEW.thread_id, ''), COALESCE(NEW.project_id, ''), COALESCE(NEW.space_id, ''),
        NEW.tool, 1, NEW.is_error != 0)
    ON CONFLICT (q, thread_id, project_id, space_id, tool) DO UPDATE SET
        calls  = calls + excluded.calls,
        errors = errors + excluded.errors;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tool_calls_q_delete AFTER DELETE ON tool_calls
BEGIN
    UPDATE tool_q SET calls = calls - 1, errors = errors - (OLD.is_error != 0)
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND tool = OLD.tool;
    DELETE FROM tool_q
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND tool = OLD.tool AND calls = 0;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER tool_calls_q_update AFTER UPDATE ON tool_calls
BEGIN
    UPDATE tool_q SET calls = calls - 1, errors = errors - (OLD.is_error != 0)
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND tool = OLD.tool;
    DELETE FROM tool_q
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND tool = OLD.tool AND calls = 0;
    INSERT INTO tool_q (q, thread_id, project_id, space_id, tool, calls, errors)
    VALUES (NEW.ts / 900000, COALESCE(NEW.thread_id, ''), COALESCE(NEW.project_id, ''), COALESCE(NEW.space_id, ''),
        NEW.tool, 1, NEW.is_error != 0)
    ON CONFLICT (q, thread_id, project_id, space_id, tool) DO UPDATE SET
        calls  = calls + excluded.calls,
        errors = errors + excluded.errors;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER turns_q_insert AFTER INSERT ON turns
BEGIN
    INSERT INTO turn_q (q, thread_id, project_id, space_id, turns)
    VALUES (NEW.ts / 900000, COALESCE(NEW.thread_id, ''), COALESCE(NEW.project_id, ''), COALESCE(NEW.space_id, ''), 1)
    ON CONFLICT (q, thread_id, project_id, space_id) DO UPDATE SET turns = turns + excluded.turns;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER turns_q_delete AFTER DELETE ON turns
BEGIN
    UPDATE turn_q SET turns = turns - 1
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '');
    DELETE FROM turn_q
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND turns = 0;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER turns_q_update AFTER UPDATE ON turns
BEGIN
    UPDATE turn_q SET turns = turns - 1
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '');
    DELETE FROM turn_q
    WHERE q = OLD.ts / 900000 AND thread_id = COALESCE(OLD.thread_id, '') AND project_id = COALESCE(OLD.project_id, '')
      AND space_id = COALESCE(OLD.space_id, '') AND turns = 0;
    INSERT INTO turn_q (q, thread_id, project_id, space_id, turns)
    VALUES (NEW.ts / 900000, COALESCE(NEW.thread_id, ''), COALESCE(NEW.project_id, ''), COALESCE(NEW.space_id, ''), 1)
    ON CONFLICT (q, thread_id, project_id, space_id) DO UPDATE SET turns = turns + excluded.turns;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER turns_q_update;
DROP TRIGGER turns_q_delete;
DROP TRIGGER turns_q_insert;
DROP TRIGGER tool_calls_q_update;
DROP TRIGGER tool_calls_q_delete;
DROP TRIGGER tool_calls_q_insert;
DROP TRIGGER usage_records_q_update;
DROP TRIGGER usage_records_q_delete;
DROP TRIGGER usage_records_q_insert;
DROP TABLE turn_q;
DROP TABLE tool_q;
DROP TABLE usage_q;
