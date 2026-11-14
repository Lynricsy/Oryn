package stats

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// rollupChecks 是「汇总表 ↔ 对原始行现算的 15 分钟 GROUP BY」的成对查询（列序相同、按主键排序）。
var rollupChecks = []struct{ name, got, want string }{
	{"usage_q", `SELECT * FROM usage_q ORDER BY 1, 2, 3, 4, 5, 6, 7`,
		`SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), provider, model, purpose,
			COUNT(*), SUM(stop_reason = 'error'), SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
			SUM(cost_usd), SUM(CASE WHEN is_subagent THEN cost_usd ELSE 0 END), COALESCE(SUM(ttft_ms), 0), COUNT(ttft_ms),
			SUM(CASE WHEN duration_ms > 0 THEN output_tokens ELSE 0 END), SUM(CASE WHEN duration_ms > 0 THEN duration_ms ELSE 0 END)
		FROM usage_records GROUP BY 1, 2, 3, 4, 5, 6, 7 ORDER BY 1, 2, 3, 4, 5, 6, 7`},
	{"tool_q", `SELECT * FROM tool_q ORDER BY 1, 2, 3, 4, 5`,
		`SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), tool, COUNT(*), SUM(is_error)
		FROM tool_calls GROUP BY 1, 2, 3, 4, 5 ORDER BY 1, 2, 3, 4, 5`},
	{"turn_q", `SELECT * FROM turn_q ORDER BY 1, 2, 3, 4`,
		`SELECT ts / 900000, COALESCE(thread_id, ''), COALESCE(project_id, ''), COALESCE(space_id, ''), COUNT(*)
		FROM turns GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3, 4`},
}

func scanAll(t *testing.T, conn *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), query)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		out = append(out, vals)
	}
	require.NoError(t, rows.Err())
	return out
}

func asNum(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// rollupDiff 返回三张汇总表与参考 GROUP BY 的差异（空 = 逐行相等；数值容差 1e-9，键与文本精确）。
func rollupDiff(t *testing.T, conn *sql.DB) []string {
	t.Helper()
	var diffs []string
	for _, c := range rollupChecks {
		got, want := scanAll(t, conn, c.got), scanAll(t, conn, c.want)
		if len(got) != len(want) {
			diffs = append(diffs, fmt.Sprintf("%s：%d 行，参考 %d 行", c.name, len(got), len(want)))
			continue
		}
		for i := range got {
			for j := range got[i] {
				g, w := got[i][j], want[i][j]
				gn, gok := asNum(g)
				wn, wok := asNum(w)
				if (gok && wok && math.Abs(gn-wn) > 1e-9) || (!(gok && wok) && fmt.Sprint(g) != fmt.Sprint(w)) {
					diffs = append(diffs, fmt.Sprintf("%s 第 %d 行：%v ≠ 参考 %v", c.name, i, got[i], want[i]))
					break
				}
			}
		}
	}
	return diffs
}

// rollupScenario 随机做 300 步写入 / 删除 / 改写 / 删线程 / 保留期 / 重算，每步之后比较汇总与参考；
// 返回首个出现差异的步骤及其差异，以及各类操作的执行次数。dropUpdate 为真时先删掉三个 UPDATE 触发器（负向检查）。
func rollupScenario(t *testing.T, dropUpdate bool) ([]string, map[string]int) {
	f := newFixture(t)
	ctx, st, q := f.ctx, f.st, f.st.Q
	if dropUpdate {
		for _, tr := range []string{"usage_records_q_update", "tool_calls_q_update", "turns_q_update"} {
			f.exec("DROP TRIGGER " + tr)
		}
	}
	rng := rand.New(rand.NewPCG(7, 11))
	base := ms("2026-03-07T00:00:00Z")
	const span = 3 * dayMs // 3 天：同一个 q 里常有多行（走 UPSERT 累加与「减到零才删」）
	now := func() int64 { return base }
	ops := map[string]int{}

	var live []string
	nThreads := 0
	newThread := func() {
		id := fmt.Sprintf("T-%02d", nThreads)
		nThreads++
		f.thread(id, []string{"", "prj_a", "prj_b"}[rng.IntN(3)], []string{"", "sp_1"}[rng.IntN(2)], "work")
		live = append(live, id)
	}
	for range 5 {
		newThread()
	}
	pick := func() string { return live[rng.IntN(len(live))] }
	dropLive := func(id string) {
		for i, x := range live {
			if x == id {
				live = append(live[:i], live[i+1:]...)
				return
			}
		}
	}

	type fact struct{ kind, key string }
	var facts []fact
	entries := 0
	extraSeq := int64(1_000_000)
	insertFact := func(kind, tid string, seq, ts int64, key string, file string) {
		var err error
		switch kind {
		case "user":
			_, err = q.InsertTurn(ctx, db.InsertTurnParams{ThreadID: &tid, EntrySeq: seq, Ts: ts, Ukey: &key})
		case "tool":
			_, err = q.InsertToolCall(ctx, db.InsertToolCallParams{ThreadID: &tid, EntrySeq: seq, Ts: ts, Tool: []string{"bash", "read"}[seq%2], IsError: seq%3 == 0, Ukey: &key})
		default:
			p := db.InsertUsageRecordParams{ThreadID: &tid, EntrySeq: seq, Ts: ts, Provider: "p", Model: []string{"m1", "m2"}[seq%2],
				StopReason: []string{"stop", "error", "stop"}[seq%3], Ukey: &key, InputTokens: seq % 97, OutputTokens: seq % 89,
				CacheReadTokens: seq % 13, CacheWriteTokens: seq % 7, CostUsd: float64(seq%1000) / 997, IsSubagent: strings.Contains(file, "/")}
			if kind == "usage" {
				p.Purpose = "title"
			}
			if seq%4 != 0 {
				p.TtftMs = new(seq % 300)
			}
			if seq%5 != 0 {
				p.DurationMs = new(seq % 4 * 500)
			}
			_, err = q.InsertUsageRecord(ctx, p)
		}
		require.NoError(t, err)
	}
	addEntry := func(tid string) {
		entries++
		id := fmt.Sprintf("e%05d", entries)
		ts := base + rng.Int64N(span)
		iso := time.UnixMilli(ts).UTC().Format("2006-01-02T15:04:05.000Z")
		cost := float64(rng.IntN(1000)) / 997
		kind := []string{"user", "assistant", "assistant", "tool", "tool", "usage"}[rng.IntN(6)]
		file, typ, line := "main.jsonl", "message", ""
		switch kind {
		case "user":
			line = fmt.Sprintf(`{"type":"message","id":%q,"parentId":null,"timestamp":%q,"message":{"role":"user","content":"q","timestamp":%d}}`, id, iso, ts)
		case "assistant":
			if rng.IntN(3) == 0 {
				file = "main/sub.jsonl"
			}
			timing := []string{"", `"duration":0,`, `"duration":1500,"ttft":200,`}[rng.IntN(3)]
			line = fmt.Sprintf(`{"type":"message","id":%q,"parentId":null,"timestamp":%q,"message":{"role":"assistant","content":[],"provider":"p","model":%q,"stopReason":%q,%s"timestamp":%d,"usage":{"input":%d,"output":%d,"cacheRead":3,"cacheWrite":1,"cost":{"total":%g}}}}`,
				id, iso, []string{"m1", "m2", "m3"}[rng.IntN(3)], []string{"stop", "error"}[rng.IntN(2)], timing, ts, rng.IntN(500), rng.IntN(500), cost)
		case "tool":
			line = fmt.Sprintf(`{"type":"message","id":%q,"parentId":null,"timestamp":%q,"message":{"role":"toolResult","toolCallId":"c","toolName":%q,"isError":%t,"content":[],"timestamp":%d}}`,
				id, iso, []string{"bash", "read", "edit"}[rng.IntN(3)], rng.IntN(4) == 0, ts)
		case "usage":
			typ = "model_usage"
			line = fmt.Sprintf(`{"type":"model_usage","id":%q,"parentId":null,"timestamp":%q,"purpose":%q,"provider":"fake","model":"fm","stopReason":"stop","usage":{"input":7,"output":3,"cacheRead":0,"cacheWrite":0,"cost":{"total":%g}}}`,
				id, iso, []string{"title", "memory"}[rng.IntN(2)], cost)
		}
		seq, err := q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: tid, File: file, EntryID: id, Type: typ, Ts: &ts, Payload: []byte(line)})
		require.NoError(t, err)
		key := id + "@" + strconv.FormatInt(ts, 10)
		insertFact(kind, tid, seq, ts, key, file)
		facts = append(facts, fact{kind, key})
	}
	// nth 返回某表活线程事实行中的第 i 个（按 id）；没有时 ok = false。
	nth := func(table string) (id int64, tid string, seq int64, ok bool) {
		var n int
		require.NoError(t, st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE thread_id IS NOT NULL`).Scan(&n))
		if n == 0 {
			return 0, "", 0, false
		}
		require.NoError(t, st.DB.QueryRowContext(ctx, `SELECT id, thread_id, entry_seq FROM `+table+` WHERE thread_id IS NOT NULL ORDER BY id LIMIT 1 OFFSET ?`,
			rng.IntN(n)).Scan(&id, &tid, &seq))
		return id, tid, seq, true
	}
	tables := []string{"usage_records", "tool_calls", "turns"}

	for step := range 300 {
		var op string
		switch r := rng.IntN(100); {
		case r < 45:
			op = "insert"
			addEntry(pick())
		case r < 53 && len(facts) > 0:
			op = "dup_ukey" // 另一个线程拿已用过的 ukey 再写（通常被 INSERT OR IGNORE 忽略）
			fc := facts[rng.IntN(len(facts))]
			extraSeq++
			ts, _ := strconv.ParseInt(fc.key[strings.Index(fc.key, "@")+1:], 10, 64)
			insertFact(fc.kind, pick(), extraSeq, ts, fc.key, "main.jsonl")
		case r < 61:
			op = "delete_one"
			table := tables[rng.IntN(3)]
			if _, tid, seq, ok := nth(table); ok {
				var err error
				switch table {
				case "usage_records":
					err = q.DeleteUsageAt(ctx, db.DeleteUsageAtParams{ThreadID: tid, EntrySeq: seq})
				case "tool_calls":
					err = q.DeleteToolCallAt(ctx, db.DeleteToolCallAtParams{ThreadID: tid, EntrySeq: seq})
				default:
					err = q.DeleteTurnAt(ctx, db.DeleteTurnAtParams{ThreadID: tid, EntrySeq: seq})
				}
				require.NoError(t, err)
			}
		case r < 71:
			op = "update_values" // reset 后数值 / 时间改写（含跨 q 的 ts 变化）
			table := tables[rng.IntN(3)]
			if id, _, _, ok := nth(table); ok {
				ts := base + rng.Int64N(span)
				switch table {
				case "usage_records":
					var ttft, dur any
					if rng.IntN(2) == 0 {
						ttft = rng.IntN(400)
					}
					if rng.IntN(2) == 0 {
						dur = rng.IntN(3) * 700
					}
					f.exec(`UPDATE usage_records SET ts = ?, cost_usd = ?, input_tokens = ?, output_tokens = ?, stop_reason = ?, ttft_ms = ?, duration_ms = ?,
						is_subagent = ?, model = ?, purpose = ? WHERE id = ?`,
						ts, float64(rng.IntN(1000))/991, rng.IntN(900), rng.IntN(900), []string{"stop", "error"}[rng.IntN(2)], ttft, dur,
						rng.IntN(2), []string{"m1", "m9"}[rng.IntN(2)], []string{"", "title"}[rng.IntN(2)], id)
				case "tool_calls":
					f.exec(`UPDATE tool_calls SET ts = ?, is_error = ?, tool = ? WHERE id = ?`, ts, rng.IntN(2), []string{"bash", "grep"}[rng.IntN(2)], id)
				default:
					f.exec(`UPDATE turns SET ts = ? WHERE id = ?`, ts, id)
				}
			}
		case r < 76 && len(live) > 1:
			op = "delete_thread" // Tombstone + 删线程（外键置空）
			id := pick()
			f.deleteThread(id)
			dropLive(id)
			newThread()
		case r < 79 && len(live) > 1:
			op = "delete_thread_fk_only" // 不写快照，只有外键置空
			id := pick()
			_, err := q.DeleteThread(ctx, id)
			require.NoError(t, err)
			dropLive(id)
			newThread()
		case r < 81:
			op = "retention"
			cut := base + rng.Int64N(span/8)
			for _, del := range []func(context.Context, int64) (int64, error){q.DeleteUsageBefore, q.DeleteToolCallsBefore, q.DeleteTurnsBefore} {
				_, err := del(ctx, cut)
				require.NoError(t, err)
			}
		case r < 84:
			op = "orphans" // 条目被删（reset_done / dropFile）后清孤儿
			tid := pick()
			f.exec(`DELETE FROM entries WHERE thread_id = ? AND seq IN (SELECT seq FROM entries WHERE thread_id = ? ORDER BY seq LIMIT 2)`, tid, tid)
			for _, del := range []func(context.Context, string) (int64, error){q.DeleteOrphanUsage, q.DeleteOrphanToolCalls, q.DeleteOrphanTurns} {
				_, err := del(ctx, tid)
				require.NoError(t, err)
			}
		case r < 92:
			op = "rebuild_thread"
			require.NoError(t, threads.RebuildThreadFacts(ctx, st, pick(), now))
		case r < 95:
			op = "backfill_purge"
			require.NoError(t, q.SetStatsState(ctx, db.SetStatsStateParams{Key: "facts_version", Value: "0"}))
			require.NoError(t, threads.BackfillFacts(ctx, st, now, true))
		default:
			op = "insert"
			addEntry(pick())
		}
		ops[op]++
		if d := rollupDiff(t, st.DB); len(d) > 0 {
			return append([]string{fmt.Sprintf("第 %d 步（%s）后出现差异", step, op)}, d...), ops
		}
	}
	return nil, ops
}

// TestRollupConsistency：任意写入序列之后三张汇总表逐行等于对原始行的 GROUP BY。
func TestRollupConsistency(t *testing.T) {
	diffs, ops := rollupScenario(t, false)
	require.Empty(t, diffs)
	for _, op := range []string{"insert", "dup_ukey", "delete_one", "update_values", "delete_thread", "delete_thread_fk_only",
		"retention", "orphans", "rebuild_thread", "backfill_purge"} {
		assert.Positive(t, ops[op], "场景应覆盖 %s", op)
	}
}

// TestRollupConsistencyNeedsUpdateTrigger 是负向检查：去掉 UPDATE 触发器后同一场景必须检出差异。
func TestRollupConsistencyNeedsUpdateTrigger(t *testing.T) {
	diffs, _ := rollupScenario(t, true)
	require.NotEmpty(t, diffs)
	t.Log(diffs[0])
}

// TestRollupParts 覆盖头 / 中 / 尾的拆分与回落到原始行的条件。
func TestRollupParts(t *testing.T) {
	const minute = int64(60_000)
	from, to := ms("2026-03-01T00:07:00Z")+123, ms("2026-03-05T00:00:00Z")-3*minute
	tp, err := newTimePlan(time.UTC, from, to, GrainDay)
	require.NoError(t, err)
	require.Len(t, tp.segs, 1)
	assert.Equal(t, []part{
		{From: from, To: ms("2026-03-01T00:15:00Z"), raw: true},
		{From: ms("2026-03-01T00:15:00Z"), To: ms("2026-03-04T23:45:00Z")},
		{From: ms("2026-03-04T23:45:00Z"), To: to, raw: true},
	}, tp.parts(tp.segs[0]))

	aligned := timePlan{segs: []segment{{From: ms("2026-03-01T00:00:00Z"), To: ms("2026-03-02T00:00:00Z")}}, bucketMs: 1}
	assert.Equal(t, []part{{From: aligned.segs[0].From, To: aligned.segs[0].To}}, aligned.parts(aligned.segs[0]), "两端对齐：只有中段")

	short := timePlan{bucketMs: 1}
	seg := segment{From: ms("2026-03-01T00:01:00Z"), To: ms("2026-03-01T00:20:00Z")}
	assert.Equal(t, []part{{From: seg.From, To: seg.To, raw: true}}, short.parts(seg), "不含完整的 15 分钟：整段原始行")

	// 1880 年纽约是地方平时（-4:56:02），偏移不在 15 分钟网格上：整段原始行
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	old, err := newTimePlan(ny, ms("1880-01-01T12:00:00Z"), ms("1880-01-03T12:00:00Z"), GrainDay)
	require.NoError(t, err)
	for _, s := range old.segs {
		for _, p := range old.parts(s) {
			assert.True(t, p.raw)
		}
	}

	useRollup = false
	t.Cleanup(func() { useRollup = true })
	assert.Equal(t, []part{{From: from, To: to, raw: true}}, tp.parts(tp.segs[0]), "开关关闭：整段原始行")
}

// TestRollupEquivalence：同一批数据，Usage / Tools / Output 走「头尾原始行 + 中段汇总」与只读原始行的结果逐字段相等，
// 覆盖 UTC、Asia/Kolkata（+5:30）、America/New_York（跨夏令时）、Australia/Lord_Howe（半小时夏令时）与不对齐 15 分钟的窗口端点。
// 金额取 1/512 的整数倍：求和与触发器的加减都没有舍入误差，因而要求逐字段相等（严于 1e-9 容差）。
func TestRollupEquivalence(t *testing.T) {
	f := newFixture(t)
	rng := rand.New(rand.NewPCG(3, 5))
	start, end := ms("2026-02-20T00:00:00Z"), ms("2026-04-20T00:00:00Z")
	ids := []string{"T-a", "T-b", "T-c", "T-d", "T-e", "T-f"}
	f.thread("T-a", "prj_a", "sp_1", "work")
	f.thread("T-b", "prj_a", "", "work")
	f.thread("T-c", "prj_b", "sp_1", "work")
	f.thread("T-d", "", "", "work")
	f.thread("T-e", "prj_b", "", "work")
	f.thread("T-f", "", "sp_1", "work")
	at := func() int64 { return start + rng.Int64N(end-start) }
	for range 3000 {
		f.usage(ids[rng.IntN(len(ids))], at(), []string{"m1", "m2", "m3"}[rng.IntN(3)], float64(rng.IntN(4096))/512, func(p *db.InsertUsageRecordParams) {
			p.Purpose = []string{"", "", "title", "memory"}[rng.IntN(4)]
			p.StopReason = []string{"stop", "stop", "error"}[rng.IntN(3)]
			p.InputTokens, p.OutputTokens, p.CacheReadTokens, p.CacheWriteTokens = rng.Int64N(5000), rng.Int64N(900), rng.Int64N(9000), rng.Int64N(300)
			p.IsSubagent = rng.IntN(4) == 0
			if rng.IntN(3) > 0 {
				p.TtftMs = new(rng.Int64N(2000))
			}
			if d := rng.Int64N(4); d > 0 {
				p.DurationMs = new((d - 1) * rng.Int64N(9000))
			}
		})
	}
	for range 2000 {
		f.tool(ids[rng.IntN(len(ids))], at(), []string{"bash", "read", "edit", "grep"}[rng.IntN(4)], rng.IntN(5) == 0)
	}
	for range 800 {
		f.turn(ids[rng.IntN(len(ids))], at())
	}
	f.deleteThread("T-c")
	f.deleteThread("T-f")
	var n int
	require.NoError(t, f.st.DB.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM usage_q`).Scan(&n))
	require.Positive(t, n)

	const minute = int64(60_000)
	type window struct {
		name     string
		from, to int64
		hour     bool // 窗口够短，可按小时
	}
	windows := []window{
		{"long_unaligned", start + 7*minute + 13_123, end - 3*minute - 1000, false},
		{"aligned_month", ms("2026-03-01T00:00:00Z"), ms("2026-04-01T00:00:00Z"), false},
		{"dst_10d_unaligned", ms("2026-03-03T10:07:00Z") + 5, ms("2026-03-13T09:52:59Z"), true},
		{"lord_howe_dst_end", ms("2026-04-02T00:03:00Z"), ms("2026-04-08T00:00:01Z"), true},
		{"short_10m", start + 5*dayMs + 100, start + 5*dayMs + 10*minute, true},
	}
	filters := []Filter{{}, {Project: new("prj_a")}, {Project: new("none")}, {Space: new("sp_1")}, {Space: new("none")},
		{Thread: new("T-b")}, {Model: new("p/m2")}}
	type usageCase struct{ groupBy, series string }
	usageCases := []usageCase{
		{GroupTotal, ""}, {GrainDay, ""}, {GrainWeek, ""}, {GrainMonth, ""},
		{GroupModel, ""}, {GroupThread, ""}, {GroupProject, ""}, {GroupSpace, ""}, {GroupPurpose, ""},
		{GroupThread, GrainWeek}, {GroupProject, GrainDay}, {GroupModel, GrainMonth}, {GroupSpace, GrainWeek},
	}
	both := func(run func() (any, error)) (any, any) {
		t.Helper()
		useRollup = false
		old, err := run()
		useRollup = true
		require.NoError(t, err)
		cur, err := run()
		require.NoError(t, err)
		return old, cur
	}
	for _, tz := range []string{"UTC", "Asia/Kolkata", "America/New_York", "Australia/Lord_Howe"} {
		loc, err := time.LoadLocation(tz)
		require.NoError(t, err)
		for _, w := range windows {
			cases := usageCases
			if w.hour {
				cases = append(cases, usageCase{GrainHour, ""}, usageCase{GroupThread, GrainHour})
			}
			for _, c := range cases {
				for fi, flt := range filters {
					// 筛选只与几种代表性的分组交叉（全部交叉要十几秒，走的路径相同）
					if fi > 0 && c != (usageCase{GrainDay, ""}) && c != (usageCase{GroupThread, GrainWeek}) &&
						c != (usageCase{GroupProject, ""}) && c != (usageCase{GrainHour, ""}) {
						continue
					}
					old, cur := both(func() (any, error) {
						return Usage(f.ctx, f.st.Q, UsageQuery{GroupBy: c.groupBy, Series: c.series, From: w.from, To: w.to, Loc: loc, Filter: flt, Limit: 3})
					})
					require.Equal(t, old, cur, "usage %s %s groupBy=%s series=%s filter#%d", tz, w.name, c.groupBy, c.series, fi)
				}
			}
			for _, series := range []string{"", GrainDay, GrainWeek} {
				for fi, flt := range filters[:6] {
					old, cur := both(func() (any, error) {
						return Tools(f.ctx, f.st.Q, ToolQuery{Series: series, From: w.from, To: w.to, Loc: loc, Filter: flt, Limit: 2})
					})
					require.Equal(t, old, cur, "tools %s %s series=%s filter#%d", tz, w.name, series, fi)
				}
			}
			for _, g := range []string{GrainDay, GrainWeek, GrainMonth, GroupProject, GroupSpace} {
				old, cur := both(func() (any, error) {
					return Output(f.ctx, f.st.Q, OutputQuery{GroupBy: g, From: w.from, To: w.to, Loc: loc})
				})
				require.Equal(t, old, cur, "output %s %s groupBy=%s", tz, w.name, g)
				old, cur = both(func() (any, error) {
					return Output(f.ctx, f.st.Q, OutputQuery{GroupBy: g, From: w.from, To: w.to, Loc: loc, Project: new("prj_a")})
				})
				require.Equal(t, old, cur, "output %s %s groupBy=%s project", tz, w.name, g)
			}
		}
	}
}
