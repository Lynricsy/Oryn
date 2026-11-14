package threads

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- sec F1 / F3：入库钳制

// 越界 token / 成本 / 时长记 0 或 NULL，整行仍然入库；边界值本身保留。
func TestParseLineClampsNumbers(t *testing.T) {
	p, err := ParseLine(`{"type":"message","id":"a1","timestamp":"2026-11-12T10:00:00.000Z","message":{"role":"assistant","provider":"p","model":"m",` +
		`"stopReason":"stop","timestamp":1794477600000,"duration":1e300,"ttft":-5,` +
		`"usage":{"input":9223372036854775807,"output":-3,"cacheRead":1e30,"cacheWrite":12.7,"cost":{"total":1e308}}}}`)
	require.NoError(t, err, "越界数值不会让整行解析失败")
	require.NotNil(t, p.Usage)
	assert.Equal(t, UsageRecord{TS: 1794477600000, Provider: "p", Model: "m", StopReason: "stop", CacheWrt: 12}, *p.Usage)

	p, err = ParseLine(`{"type":"message","id":"a2","message":{"role":"assistant","timestamp":1794477600000,"duration":1000000000,"ttft":0,` +
		`"usage":{"input":1000000000000,"output":1,"cacheRead":0,"cacheWrite":0,"cost":{"total":1000000}}}}`)
	require.NoError(t, err)
	assert.Equal(t, int64(1e12), p.Usage.Input, "上限本身保留")
	assert.Equal(t, 1e6, p.Usage.CostUSD)
	assert.Equal(t, int64(1e9), *p.Usage.DurationMs)
	assert.Equal(t, int64(0), *p.Usage.TTFTMs)

	p, err = ParseLine(`{"type":"message","id":"a3","message":{"role":"assistant","timestamp":1794477600000,"duration":1000000000.6,"ttft":5e18,` +
		`"usage":{"input":5000000000000000000,"output":1000000000001,"cost":{"total":1000000.01}}}}`)
	require.NoError(t, err)
	assert.Zero(t, p.Usage.Input)
	assert.Zero(t, p.Usage.Output)
	assert.Zero(t, p.Usage.CostUSD)
	assert.Nil(t, p.Usage.DurationMs, "取整前判越界")
	assert.Nil(t, p.Usage.TTFTMs)

	p, err = ParseLine(`{"type":"model_usage","id":"m1","timestamp":1794477600000,"purpose":"title","usage":{"input":4,"cost":{"total":-1.7e308}}}`)
	require.NoError(t, err)
	assert.Zero(t, p.Usage.CostUSD, "负成本记 0")
	assert.Equal(t, int64(4), p.Usage.Input)
}

// purpose 收窄到 [A-Za-z0-9:._-] 且 ≤64 字节，provider / model / tool ≤128 字节（按字符边界截断），stop_reason ≤32。
func TestParseLineClampsStrings(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	wide := strings.Repeat("é", 200) // 2 字节字符：128 字节处正好在字符边界上；127 就不是
	line := fmt.Sprintf(`{"type":"model_usage","id":"m1","timestamp":1794477600000,"purpose":%q,"provider":%q,"model":%q,"stopReason":%q,"usage":{"input":1}}`,
		big, big, "a"+wide, big)
	p, err := ParseLine(line)
	require.NoError(t, err)
	u := p.Usage
	assert.Equal(t, "unknown", u.Purpose)
	assert.Len(t, u.Provider, maxNameLen)
	assert.True(t, utf8.ValidString(u.Model))
	assert.Len(t, u.Model, maxNameLen-1, "不切断多字节字符")
	assert.Len(t, u.StopReason, maxStopReasonLen)

	for purpose, want := range map[string]string{
		"title": "title", "cache-warm": "cache-warm", "judge_batch": "judge_batch", "a:b.c": "a:b.c",
		`=HYPERLINK("x")`: "unknown", "a b": "unknown", "\x1b[31m": "unknown", strings.Repeat("p", 65): "unknown",
		strings.Repeat("p", 64): strings.Repeat("p", 64), "": "unknown",
	} {
		raw, _ := json.Marshal(purpose)
		p, err := ParseLine(`{"type":"model_usage","id":"m","purpose":` + string(raw) + `,"usage":{"input":1}}`)
		require.NoError(t, err)
		assert.Equal(t, want, p.Usage.Purpose, "%q", purpose)
	}

	p, err = ParseLine(fmt.Sprintf(`{"type":"message","id":"t1","message":{"role":"toolResult","toolName":%q,"timestamp":1794477600000}}`, big))
	require.NoError(t, err)
	assert.Len(t, p.Tool.Tool, maxNameLen)
}

// 事实时间钳到 [2020-01-01, now + 1 天]，越界取入库时间；去重键保留条目里的原始时间（与旧行一致、重算时不漂移）。
func TestFactsClampTimestamps(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T", threadOpt{})
	const far = int64(32503680000000) // 3000-01-01
	before := time.Now().UnixMilli()
	e.appendLines("T", "main.jsonl",
		asstLine("af", "", far, 1, 0.5, "stop"),
		asstLine("ao", "af", 5000, 1, 0.25, "stop"),
		factUserLine("uf", "ao", far),
		toolLine("tf", "uf", -7, false),
		asstLine("ok", "tf", factsT0, 1, 0.125, "stop"))
	after := time.Now().UnixMilli()
	for _, k := range []string{"af@" + strconv.FormatInt(far, 10), "ao@5000"} {
		ts := e.rows(`SELECT ts FROM usage_records WHERE ukey = ?`, k)
		require.Len(t, ts, 1, k)
		n, _ := strconv.ParseInt(ts[0], 10, 64)
		assert.True(t, n >= before && n <= after, "%s: ts %d 应取入库时间", k, n)
	}
	for _, q := range []string{`SELECT ts FROM turns WHERE ukey = 'uf@` + strconv.FormatInt(far, 10) + `'`, `SELECT ts FROM tool_calls WHERE ukey = 'tf@-7'`} {
		ts := e.rows(q)
		require.Len(t, ts, 1, q)
		n, _ := strconv.ParseInt(ts[0], 10, 64)
		assert.True(t, n >= before && n <= after, q)
	}
	assert.Equal(t, []string{strconv.FormatInt(factsT0, 10)}, e.rows(`SELECT ts FROM usage_records WHERE ukey LIKE 'ok@%'`), "合理时间原样保留")

	// 一天的时钟偏差之内不钳
	soon := time.Now().UnixMilli() + 60*60*1000
	e.appendLines("T", "main.jsonl", asstLine("as", "ok", soon, 1, 0.1, "stop"))
	assert.Equal(t, []string{strconv.FormatInt(soon, 10)}, e.rows(`SELECT ts FROM usage_records WHERE ukey LIKE 'as@%'`))
}

// 1 MiB 的条目 id / purpose / provider / model：各列有界，去重键是「前缀 + 哈希」，usage_q 只多一行。
func TestFactsLongStringsBounded(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T", threadOpt{})
	id := strings.Repeat("i", 1<<20)
	big := strings.Repeat("z", 1<<20)
	line := fmt.Sprintf(`{"type":"model_usage","id":%q,"timestamp":%q,"purpose":%q,"provider":%q,"model":%q,"stopReason":%q,"usage":{"input":7,"cost":{"total":0.5}}}`,
		id, isoMs(factsT0), big, big, big, big)
	e.appendLines("T", "main.jsonl", line)
	assert.Equal(t, []string{"1"}, e.rows(`SELECT COUNT(*) FROM usage_q`))
	assert.Equal(t, []string{"1|128|128|7|32|0.5"},
		e.rows(`SELECT length(ukey) <= 120, length(provider), length(model), length(purpose), length(stop_reason), cost_usd FROM usage_records`),
		"ukey 有界、provider / model 截断、purpose 记 unknown")
	want := strings.Repeat("i", factKeyPrefixLen) + "~"
	k := e.rows(`SELECT ukey FROM usage_records`)[0]
	assert.True(t, strings.HasPrefix(k, want), k)
	assert.Equal(t, factKey(id, factsT0), k)
	assert.Equal(t, "ab@1", factKey("ab", 1), "常规 id 的键不变")
	assert.Equal(t, strings.Repeat("j", 64)+"@1", factKey(strings.Repeat("j", 64), 1), "64 字节以内不哈希")

	// 同一超长 id 的 fork 副本仍按键去重
	e.thread("F", threadOpt{createdAt: 2})
	e.appendLines("F", "main.jsonl", line)
	assert.Equal(t, factTotals{cost: 0.5, requests: 1}, e.totals(""))
}

// ---- 后端发现 2：删掉事实的所属条目时，仍在当前转录里的副本接过事实

// assertRollups 断言三张 15 分钟汇总与原始事实行逐键一致。
func (e *factsEnv) assertRollups() {
	e.t.Helper()
	assert.Equal(e.t,
		e.rows(`SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), provider, model, purpose,
			COUNT(*), TOTAL(input_tokens), TOTAL(cost_usd), TOTAL(CASE WHEN is_subagent THEN cost_usd ELSE 0 END)
			FROM usage_records GROUP BY 1, 2, 3, 4, 5, 6, 7 ORDER BY 1, 2, 3, 4, 5, 6, 7`),
		e.rows(`SELECT q, thread_id, project_id, space_id, provider, model, purpose, requests, CAST(input_tokens AS REAL), cost_usd, subagent_cost_usd
			FROM usage_q ORDER BY 1, 2, 3, 4, 5, 6, 7`), "usage_q")
	assert.Equal(e.t,
		e.rows(`SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), tool, COUNT(*), TOTAL(is_error)
			FROM tool_calls GROUP BY 1, 2, 3, 4, 5 ORDER BY 1, 2, 3, 4, 5`),
		e.rows(`SELECT q, thread_id, project_id, space_id, tool, calls, CAST(errors AS REAL) FROM tool_q ORDER BY 1, 2, 3, 4, 5`), "tool_q")
	assert.Equal(e.t,
		e.rows(`SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), COUNT(*)
			FROM turns GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3, 4`),
		e.rows(`SELECT q, thread_id, project_id, space_id, turns FROM turn_q ORDER BY 1, 2, 3, 4`), "turn_q")
}

// owners 返回某个键前缀的事实行所在的 (线程, 文件)。
func (e *factsEnv) owners(tbl, keyPrefix string) []string {
	e.t.Helper()
	return e.rows(`SELECT f.thread_id || ':' || en.file FROM `+tbl+` f JOIN entries en ON en.thread_id = f.thread_id AND en.seq = f.entry_seq
		WHERE f.ukey LIKE ? ORDER BY 1`, keyPrefix+"%")
}

// 同一线程两个文件里有同一条 assistant + toolResult（omp 重启把子 agent 历史拷进新会话目录）：
// 删非所属文件不影响；删所属文件后事实迁到剩下的副本，只计一次。reset 与 dropFile 两条路径都覆盖。
func TestFactsRestoreDuplicateInThread(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T", threadOpt{})
	lines := []string{asstLine("a", "", factsT0, 10, 0.5, "stop"), toolLine("t", "a", factsT0+1, true)}
	e.appendLines("T", "old/sub.jsonl", lines...)
	e.appendLines("T", "new/sub.jsonl", lines...)
	want := factTotals{cost: 0.5, requests: 1, tools: 1, errs: 1}
	require.Equal(t, want, e.totals(""))
	assert.Equal(t, []string{"T:old/sub.jsonl"}, e.owners("usage_records", "a@"))

	e.rewrite("T", "new/sub.jsonl") // 非所属文件清空
	assert.Equal(t, want, e.totals(""))
	assert.Equal(t, []string{"T:old/sub.jsonl"}, e.owners("usage_records", "a@"))
	e.appendLines("T", "new/sub.jsonl", lines...)

	e.rewrite("T", "old/sub.jsonl") // 所属文件清空
	assert.Equal(t, want, e.totals(""), "仍在 new/sub.jsonl 里的用量只计一次")
	assert.Equal(t, []string{"T:new/sub.jsonl"}, e.owners("usage_records", "a@"))
	assert.Equal(t, []string{"T:new/sub.jsonl"}, e.owners("tool_calls", "t@"))
	assert.Equal(t, []string{"1"}, e.rows(`SELECT is_subagent FROM usage_records`))
	e.assertRollups()

	e.appendLines("T", "third/sub.jsonl", lines...)
	th, err := e.s.get(e.ctx, "T")
	require.NoError(t, err)
	e.s.dropFile(e.ctx, th, "new/sub.jsonl")
	assert.Equal(t, want, e.totals(""))
	assert.Equal(t, []string{"T:third/sub.jsonl"}, e.owners("usage_records", "a@"))
	e.assertRollups()

	e.s.dropFile(e.ctx, th, "third/sub.jsonl")
	assert.Equal(t, factTotals{}, e.totals(""), "最后一份副本也没了：事实清零")
	e.assertRollups()
}

// 回合：同一条 user 消息出现在两个主会话文件里，删所属文件后回合迁到另一个。
func TestFactsRestoreDuplicateTurn(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T", threadOpt{})
	u := factUserLine("u", "", factsT0)
	e.appendLines("T", "a.jsonl", u)
	e.appendLines("T", "b.jsonl", u)
	require.Equal(t, 1, e.totals("").turns)
	assert.Equal(t, []string{"T:a.jsonl"}, e.owners("turns", "u@"))
	e.rewrite("T", "a.jsonl")
	assert.Equal(t, 1, e.totals("").turns)
	assert.Equal(t, []string{"T:b.jsonl"}, e.owners("turns", "u@"))
	e.assertRollups()
}

// reset 重发时条目内容变了（时间变了 → 新键）：旧键回到仍持有旧内容的副本，新键记在改写后的条目上。
func TestFactsRestoreAfterChangedEntry(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T", threadOpt{})
	e.appendLines("T", "main.jsonl", asstLine("a", "", factsT0, 10, 0.5, "stop"))
	e.appendLines("T", "main_x/sub.jsonl", asstLine("a", "", factsT0, 10, 0.5, "stop"))
	require.Equal(t, factTotals{cost: 0.5, requests: 1}, e.totals(""))
	e.rewrite("T", "main.jsonl", asstLine("a", "", factsT0+5, 20, 1, "stop"))
	assert.Equal(t, factTotals{cost: 1.5, requests: 2}, e.totals(""))
	assert.Equal(t, []string{"T:main_x/sub.jsonl"}, e.owners("usage_records", "a@"+strconv.FormatInt(factsT0, 10)))
	assert.Equal(t, []string{"T:main.jsonl"}, e.owners("usage_records", "a@"+strconv.FormatInt(factsT0+5, 10)))
	e.assertRollups()
}

// 跨线程：fork 副本在源线程丢掉历史后接过事实（按线程创建时间取第一个），全库合计不变；
// 已删线程的冻结行持有的键不放出——副本不补记，不会重复计费。
func TestFactsRestoreAcrossThreads(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("A", threadOpt{createdAt: 1})
	e.thread("B", threadOpt{createdAt: 2, parent: new("A")})
	e.thread("C", threadOpt{createdAt: 3, parent: new("A")})
	lines := turnLines("x", "", factsT0)
	for _, id := range []string{"A", "C", "B"} {
		e.appendLines(id, "main.jsonl", lines...)
	}
	want := factTotals{cost: 0.875, requests: 3, tools: 1, turns: 1}
	require.Equal(t, want, e.totals(""))
	require.Equal(t, want, e.totals(`thread_id = 'A'`))

	e.rewrite("A", "main.jsonl")
	assert.Equal(t, want, e.totals(""), "全库合计不变")
	assert.Equal(t, want, e.totals(`thread_id = 'B'`), "B 比 C 先创建，先接过")
	e.assertRollups()

	// B 被删：事实冻结（thread_id 置空），键仍被占用；C 的副本不补记
	require.NoError(t, e.s.Delete(e.ctx, "B"))
	assert.Equal(t, want, e.totals(""))
	assert.Equal(t, want, e.totals(`thread_id IS NULL`))
	e.rewrite("C", "main.jsonl", lines[:2]...) // C 丢掉一部分副本：不影响冻结行
	e.rewrite("C", "main.jsonl")
	assert.Equal(t, want, e.totals(""), "冻结行仍只计一次")
	assert.Equal(t, factTotals{}, e.totals(`thread_id = 'C'`))
	e.assertRollups()
}

// 超长 id 的副本也能找回（去重键是哈希，按前缀在索引上取候选再逐条比对）。
func TestFactsRestoreLongID(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T", threadOpt{})
	long := strings.Repeat("L", 100)
	other := strings.Repeat("L", 99) + "M" // 同前缀、不同 id：不能被当成副本
	line := asstLine(long, "", factsT0, 10, 0.5, "stop")
	e.appendLines("T", "old/sub.jsonl", line)
	e.appendLines("T", "mid/sub.jsonl", asstLine(other, "", factsT0, 10, 0.25, "stop"))
	e.appendLines("T", "new/sub.jsonl", line)
	require.Equal(t, factTotals{cost: 0.75, requests: 2}, e.totals(""))
	e.rewrite("T", "old/sub.jsonl")
	assert.Equal(t, factTotals{cost: 0.75, requests: 2}, e.totals(""))
	assert.Equal(t, []string{"T:new/sub.jsonl"}, e.owners("usage_records", factKey(long, factsT0)))
	e.assertRollups()
}

// ---- FactsVersion 3 的回填：钳制旧行、改写超长 id 的键，fork / 冻结行的去重保持

func TestBackfillSanitizesLegacyFacts(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("A", threadOpt{createdAt: 1})
	long := strings.Repeat("L", 100)
	rawKey := long + "@" + strconv.FormatInt(factsT0, 10) // 旧编码：id 原样进键
	exec := func(q string, args ...any) {
		_, err := e.st.DB.ExecContext(e.ctx, q, args...)
		require.NoError(t, err)
	}
	// 已删线程留下的冻结行：一行旧编码的超长键（合法数值），两行越界数值（同一汇总键，usage_q 里成本已是 +Inf）
	exec(`INSERT INTO usage_records (thread_id, entry_seq, ts, provider, model, purpose, stop_reason, ukey, input_tokens, output_tokens,
		cache_read_tokens, cache_write_tokens, cost_usd, is_subagent) VALUES
		(NULL, 1, ?, 'p', 'm', 'title', 'stop', ?, 7, 3, 0, 0, 0.25, 0),
		(NULL, 2, ?, 'p', 'm', 'x', 'stop', 'o1@1', 5000000000000000000, -1, 0, 0, 1.7e308, 0),
		(NULL, 3, ?, 'p', 'm', 'x', 'stop', 'o2@1', 5000000000000000000, 0, 0, 0, 1.7e308, 0)`, factsT0, rawKey, factsT0, factsT0)
	exec(`INSERT INTO tool_calls (thread_id, entry_seq, ts, tool, is_error, ukey) VALUES (NULL, 4, 32503680000000, ?, 0, 'tl@1')`, strings.Repeat("t", 1000))
	assert.Equal(t, []string{"+Inf"}, e.rows(`SELECT cost_usd FROM usage_q WHERE purpose = 'x'`), "前提：汇总里已有 +Inf")

	// 新代码在回填之前已经入库了同一条超长 id 条目的 fork 副本（新编码的键与冻结行的旧键不冲突，暂时多计一次）
	e.appendLines("A", "main.jsonl", fmt.Sprintf(`{"type":"model_usage","id":%q,"timestamp":%q,"purpose":"title","provider":"p","model":"m","stopReason":"stop","usage":{"input":7,"output":3,"cost":{"total":0.25}}}`,
		long, isoMs(factsT0)))
	require.Equal(t, 1, e.totals(`thread_id = 'A'`).requests)
	require.NoError(t, e.st.Q.SetStatsState(e.ctx, db.SetStatsStateParams{Key: stateFactsVersion, Value: "2"}))
	require.NoError(t, e.st.Q.DeleteStatsState(e.ctx, stateUkeyScheme))

	require.NoError(t, BackfillFacts(e.ctx, e.st, nil, false))
	v, _ := e.state(stateUkeyScheme)
	assert.Equal(t, ukeyScheme, v)
	hashed := factKey(long, factsT0)
	assert.Equal(t, []string{"<nil>|" + hashed}, e.rows(`SELECT thread_id, ukey FROM usage_records WHERE purpose = 'title'`),
		"冻结行改成新编码的键；A 重算时该键已被占用，不再重复计")
	assert.Equal(t, []string{"0|0|0", "0|0|0"}, e.rows(`SELECT input_tokens, output_tokens, cost_usd FROM usage_records WHERE purpose = 'x' ORDER BY id`))
	tc := e.rows(`SELECT length(tool), ts < 32503680000000 FROM tool_calls`)
	assert.Equal(t, []string{"128|1"}, tc)
	assert.Equal(t, []string{"0"}, e.rows(`SELECT cost_usd FROM usage_q WHERE purpose = 'x'`), "汇总整表重建，+Inf 不再残留")
	e.assertRollups()

	// admin rebuild-stats：版本置 0 后全量重算——已改写的键不会被二次哈希
	require.NoError(t, e.st.Q.SetStatsState(e.ctx, db.SetStatsStateParams{Key: stateFactsVersion, Value: "0"}))
	require.NoError(t, BackfillFacts(e.ctx, e.st, nil, true))
	assert.Equal(t, []string{"<nil>|" + hashed}, e.rows(`SELECT thread_id, ukey FROM usage_records WHERE purpose = 'title'`))
	assert.Equal(t, factTotals{}, e.totals(`thread_id = 'A'`))
	e.assertRollups()
}
