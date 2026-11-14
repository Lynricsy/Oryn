package stats

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

var dayD = ms("2026-11-10T00:00:00Z")

// usageFixture 造一批跨三天的用量 / 工具 / 回合（UTC 日：D、D+1、D+2）。
func usageFixture(t *testing.T) *fixture {
	f := newFixture(t)
	h := hourMs
	f.thread("T-1", "prj_a", "sp_1", "work")
	f.thread("T-2", "prj_b", "", "work")
	f.thread("T-3", "", "", "work")
	f.thread("T-9", "prj_b", "sp_1", "work")
	f.usage("T-1", dayD+1*h, "m1", 1.0, func(p *db.InsertUsageRecordParams) {
		p.TtftMs, p.DurationMs = new(int64(100)), new(int64(2000))
	})
	f.usage("T-1", dayD+20*h, "m2", 0.5, func(p *db.InsertUsageRecordParams) {
		p.StopReason, p.IsSubagent = "error", true
	})
	f.usage("T-2", dayD+25*h, "m1", 2.0)
	f.usage("T-2", dayD+26*h, "m3", 0.25, func(p *db.InsertUsageRecordParams) { p.Purpose = "title" })
	f.usage("T-9", dayD+49*h, "m1", 4.0)
	f.usage("T-1", dayD+80*h, "m1", 100) // 窗口外
	f.tool("T-1", dayD+2*h, "bash", false)
	f.tool("T-1", dayD+3*h, "bash", true)
	f.tool("T-3", dayD+53*h, "read", false)
	f.turn("T-1", dayD+1*h)
	f.turn("T-2", dayD+25*h)
	f.turn("T-3", dayD+53*h)
	f.deleteThread("T-9")
	return f
}

func usageQ(t *testing.T, f *fixture, uq UsageQuery) apigen.UsageStats {
	t.Helper()
	if uq.From == 0 && uq.To == 0 {
		uq.From, uq.To = dayD, dayD+3*dayMs
	}
	u, err := Usage(f.ctx, f.st.Q, uq)
	require.NoError(t, err)
	return u
}

func group(t *testing.T, gs []apigen.UsageStat, key string) apigen.UsageStat {
	t.Helper()
	for _, g := range gs {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("没有分组 %q", key)
	return apigen.UsageStat{}
}

func groupKeys(gs []apigen.UsageStat) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.Key
	}
	return out
}

func TestUsageTotalAndDays(t *testing.T) {
	f := usageFixture(t)
	u := usageQ(t, f, UsageQuery{GroupBy: "total"})
	tot := u.Total
	assert.Equal(t, "total", tot.Key)
	assert.InDelta(t, 7.75, tot.CostUsd, 1e-9)
	assert.EqualValues(t, 5, tot.Requests)
	assert.EqualValues(t, 1, tot.RequestErrors, "stop_reason = error")
	assert.EqualValues(t, 3, tot.ToolCalls)
	assert.EqualValues(t, 1, tot.ToolErrors)
	assert.EqualValues(t, 3, tot.Turns)
	assert.InDelta(t, 0.5, *tot.SubagentCostUsd, 1e-9)
	require.Len(t, u.Groups, 1)
	assert.Equal(t, tot, u.Groups[0])
	assert.Equal(t, "UTC", u.Tz)

	u = usageQ(t, f, UsageQuery{GroupBy: "day"})
	assert.Equal(t, []string{"2026-11-10", "2026-11-11", "2026-11-12"}, groupKeys(u.Groups))
	assert.InDelta(t, 1.5, u.Groups[0].CostUsd, 1e-9)
	assert.InDelta(t, 2.25, u.Groups[1].CostUsd, 1e-9)
	assert.InDelta(t, 4.0, u.Groups[2].CostUsd, 1e-9)
	assert.EqualValues(t, 2, u.Groups[0].ToolCalls)
	assert.EqualValues(t, 1, u.Groups[2].ToolCalls)
	assert.EqualValues(t, dayD+dayMs, *u.Groups[1].Start)
	assert.Nil(t, u.Series)

	// 换时区：同一批行落到不同的本地日，总和不变；网格稠密（含全零的最后一天）
	sh := mustLoc(t, "Asia/Shanghai")
	u = usageQ(t, f, UsageQuery{GroupBy: "day", Loc: sh})
	assert.Equal(t, "Asia/Shanghai", u.Tz)
	assert.Equal(t, []string{"2026-11-10", "2026-11-11", "2026-11-12", "2026-11-13"}, groupKeys(u.Groups))
	assert.InDelta(t, 1.0, u.Groups[0].CostUsd, 1e-9)
	assert.InDelta(t, 2.75, u.Groups[1].CostUsd, 1e-9)
	assert.InDelta(t, 4.0, u.Groups[2].CostUsd, 1e-9)
	assert.Zero(t, u.Groups[3].CostUsd)
	assert.EqualValues(t, 0, u.Groups[3].Requests)
	assert.InDelta(t, 7.75, u.Total.CostUsd, 1e-9)

	// 周 / 月由日折叠
	u = usageQ(t, f, UsageQuery{GroupBy: "week"})
	assert.Equal(t, []string{"2026-W46"}, groupKeys(u.Groups))
	assert.InDelta(t, 7.75, u.Groups[0].CostUsd, 1e-9)
	u = usageQ(t, f, UsageQuery{GroupBy: "hour", From: dayD, To: dayD + 4*hourMs})
	require.Len(t, u.Groups, 4)
	assert.EqualValues(t, 1, u.Groups[1].Requests)
	assert.EqualValues(t, 1, u.Groups[2].ToolCalls)
}

func TestUsageDimensions(t *testing.T) {
	f := usageFixture(t)

	u := usageQ(t, f, UsageQuery{GroupBy: "model"})
	assert.Equal(t, []string{"p/m1", "p/m2", "p/m3"}, groupKeys(u.Groups), "按成本降序")
	m1 := u.Groups[0]
	assert.InDelta(t, 7.0, m1.CostUsd, 1e-9)
	assert.InDelta(t, 100, *m1.AvgTtftMs, 1e-9, "只有一行有 ttft")
	assert.InDelta(t, 10, *m1.TokensPerSecond, 1e-9, "20 tokens / 2s；没有时长的行不计")
	assert.Nil(t, u.Groups[2].AvgTtftMs, "分母为 0 时省略")
	assert.Nil(t, u.Groups[2].TokensPerSecond)
	assert.EqualValues(t, 0, u.Total.ToolCalls, "按模型分组时工具与回合为 0")
	assert.EqualValues(t, 0, u.Total.Turns)

	u = usageQ(t, f, UsageQuery{GroupBy: "thread"})
	assert.Equal(t, []string{"", "T-2", "T-1", "T-3"}, groupKeys(u.Groups))
	assert.InDelta(t, 4.0, u.Groups[0].CostUsd, 1e-9, "已删线程为空键")
	assert.Nil(t, u.Groups[0].Label)
	assert.Equal(t, "title T-2", *u.Groups[1].Label)
	t3 := u.Groups[3]
	assert.EqualValues(t, 0, t3.Requests, "只有工具调用与回合的分组")
	assert.EqualValues(t, 1, t3.ToolCalls)
	assert.EqualValues(t, 1, t3.Turns)

	// limit 折叠：total 在折叠前求和
	u = usageQ(t, f, UsageQuery{GroupBy: "thread", Limit: 2})
	assert.Equal(t, []string{"", "T-2", OtherKey}, groupKeys(u.Groups))
	other := u.Groups[2]
	assert.InDelta(t, 1.5, other.CostUsd, 1e-9)
	assert.EqualValues(t, 3, other.ToolCalls)
	assert.EqualValues(t, 2, other.Turns)
	assert.Nil(t, other.Label)
	assert.InDelta(t, 7.75, u.Total.CostUsd, 1e-9)

	u = usageQ(t, f, UsageQuery{GroupBy: "project"})
	b := group(t, u.Groups, "prj_b")
	assert.InDelta(t, 6.25, b.CostUsd, 1e-9, "已删线程按删除时的快照归到项目")
	assert.Equal(t, "beta", *b.Label)
	assert.Equal(t, "alpha", *group(t, u.Groups, "prj_a").Label)
	none := group(t, u.Groups, "")
	assert.EqualValues(t, 1, none.ToolCalls)
	assert.Zero(t, none.CostUsd)

	u = usageQ(t, f, UsageQuery{GroupBy: "space"})
	s1 := group(t, u.Groups, "sp_1")
	assert.InDelta(t, 5.5, s1.CostUsd, 1e-9)
	assert.Equal(t, "S1", *s1.Label)
	assert.InDelta(t, 2.25, group(t, u.Groups, "").CostUsd, 1e-9)

	u = usageQ(t, f, UsageQuery{GroupBy: "purpose"})
	assert.Equal(t, []string{"", "title"}, groupKeys(u.Groups))
	assert.InDelta(t, 0.25, u.Groups[1].CostUsd, 1e-9)
	assert.EqualValues(t, 0, u.Groups[0].ToolCalls)
}

func TestUsageFilters(t *testing.T) {
	f := usageFixture(t)
	total := func(fl Filter) apigen.UsageStat { return usageQ(t, f, UsageQuery{GroupBy: "total", Filter: fl}).Total }
	assert.InDelta(t, 6.25, total(Filter{Project: new("prj_b")}).CostUsd, 1e-9)
	none := total(Filter{Project: new("none")})
	assert.Zero(t, none.CostUsd)
	assert.EqualValues(t, 1, none.ToolCalls, "none = 无项目")
	assert.InDelta(t, 5.5, total(Filter{Space: new("sp_1")}).CostUsd, 1e-9)
	assert.InDelta(t, 2.25, total(Filter{Space: new("none")}).CostUsd, 1e-9)
	t1 := total(Filter{Thread: new("T-1")})
	assert.InDelta(t, 1.5, t1.CostUsd, 1e-9)
	assert.EqualValues(t, 2, t1.ToolCalls)
	assert.EqualValues(t, 1, t1.Turns)
	m := total(Filter{Model: new("p/m1")})
	assert.InDelta(t, 7.0, m.CostUsd, 1e-9)
	assert.EqualValues(t, 0, m.ToolCalls, "model 筛选时工具与回合为 0")
	assert.EqualValues(t, 0, m.Turns)
}

func TestUsageSeries(t *testing.T) {
	f := usageFixture(t)
	u := usageQ(t, f, UsageQuery{GroupBy: "model", Series: "day", Limit: 1})
	assert.Equal(t, []string{"p/m1", OtherKey}, groupKeys(u.Groups))
	require.Len(t, u.Series, 3, "稠密")
	var sum float64
	for _, p := range u.Series {
		var gs float64
		for _, g := range p.Groups {
			gs += g.CostUsd
			assert.Contains(t, []string{"p/m1", OtherKey}, g.Key, "series 的键与顶层一致")
		}
		assert.InDelta(t, p.Total.CostUsd, gs, 1e-9)
		sum += p.Total.CostUsd
	}
	assert.InDelta(t, u.Total.CostUsd, sum, 1e-9)
	assert.Equal(t, "2026-11-11", u.Series[1].Key)
	assert.Equal(t, []string{"p/m1", OtherKey}, groupKeys(u.Series[1].Groups))
	assert.Equal(t, []string{"p/m1"}, groupKeys(u.Series[2].Groups), "只含非零分组")

	u = usageQ(t, f, UsageQuery{GroupBy: "total", Series: "day"})
	require.Len(t, u.Series, 3)
	assert.EqualValues(t, 1, u.Series[2].Total.ToolCalls)
}

func TestUsageErrors(t *testing.T) {
	f := usageFixture(t)
	code := func(uq UsageQuery) string {
		if uq.To == 0 {
			uq.From, uq.To = dayD, dayD+dayMs
		}
		_, err := Usage(f.ctx, f.st.Q, uq)
		var se *Error
		require.True(t, errors.As(err, &se), "%v", err)
		return se.Code
	}
	assert.Equal(t, CodeInvalidRequest, code(UsageQuery{GroupBy: "nope"}))
	assert.Equal(t, CodeInvalidRequest, code(UsageQuery{GroupBy: "day", Series: "day"}))
	assert.Equal(t, CodeInvalidRequest, code(UsageQuery{GroupBy: "model", Series: "minute"}))
	assert.Equal(t, CodeInvalidRequest, code(UsageQuery{GroupBy: "model", From: 5, To: 5}))
	assert.Equal(t, CodeInvalidRequest, code(UsageQuery{GroupBy: "model", Limit: 501}))
	assert.Equal(t, CodeRangeTooLarge, code(UsageQuery{GroupBy: "hour", From: dayD, To: dayD + 60*dayMs}))
	_, err := LoadLocation("Mars/Phobos")
	var se *Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodeInvalidTimezone, se.Code)
	_, err = LoadLocation("Local")
	assert.Error(t, err)
	loc, err := LoadLocation("")
	require.NoError(t, err)
	assert.Equal(t, time.UTC, loc)
}

func TestTools(t *testing.T) {
	f := usageFixture(t)
	for i := range 3 {
		f.tool("T-2", dayD+30*hourMs+int64(i), "edit", i == 0)
	}
	ts, err := Tools(f.ctx, f.st.Q, ToolQuery{From: dayD, To: dayD + 3*dayMs, Series: "day", Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []apigen.ToolStat{{Tool: "edit", Calls: 3, Errors: 1}, {Tool: "bash", Calls: 2, Errors: 1}, {Tool: OtherKey, Calls: 1}}, ts.Tools)
	assert.Equal(t, apigen.ToolStat{Calls: 6, Errors: 2}, ts.Total)
	require.Len(t, ts.Series, 3)
	assert.Equal(t, apigen.ToolSeriesPoint{Key: "2026-11-11", Start: dayD + dayMs, Calls: 3, Errors: 1}, ts.Series[1])

	ts, err = Tools(f.ctx, f.st.Q, ToolQuery{From: dayD, To: dayD + 3*dayMs, Filter: Filter{Project: new("none")}})
	require.NoError(t, err)
	assert.Equal(t, []apigen.ToolStat{{Tool: "read", Calls: 1}}, ts.Tools)
	assert.Nil(t, ts.Series)
}

// 柏林秋季回拨：两次本地 02:30 的用量都要落进同一个 2 小时桶，不能丢掉第一次
func TestUsageBerlinFallBackKeepsFirstOccurrence(t *testing.T) {
	f := newFixture(t)
	f.thread("T", "", "", "work")
	f.usage("T", ms("2026-10-25T00:30:00Z"), "m", 1)
	f.usage("T", ms("2026-10-25T01:30:00Z"), "m", 2)
	u, err := Usage(f.ctx, f.st.Q, UsageQuery{GroupBy: "hour", From: ms("2026-10-25T00:00:00Z"), To: ms("2026-10-25T02:00:00Z"), Loc: mustLoc(t, "Europe/Berlin")})
	require.NoError(t, err)
	assert.EqualValues(t, 2, u.Total.Requests)
	assert.InDelta(t, 3.0, u.Total.CostUsd, 1e-9)
}
