package stats

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// sec F1：绕过入库钳制直接写两行 input = 5e18、cost = 1.7e308（同一汇总键：usage_q 的成本相加成 +Inf、token 溢出成 REAL）。
// 原始行路径（窗口头尾）与汇总路径（中段）都不报错，结果可编码成 JSON，预算花费有限。
func TestOverflowFactsStayEncodable(t *testing.T) {
	f := newFixture(t)
	f.thread("T-1", "prj_a", "", "work")
	now := time.Date(2026, 11, 10, 12, 7, 0, 0, time.UTC)
	ts := now.UnixMilli() - 30*60*1000
	for range 2 {
		f.usage("T-1", ts, "m", 1.7e308, func(p *db.InsertUsageRecordParams) {
			p.InputTokens, p.OutputTokens, p.Purpose = 5e18, 5e18, "x"
			p.DurationMs, p.TtftMs = new(int64(5e18)), new(int64(5e18))
		})
		f.tool("T-1", ts, "bash", true)
		f.turn("T-1", ts)
	}
	// 另一个模型的同样两行：按 model 分组时各组在 SQL 里饱和到 MaxInt64，合计在 Go 里再相加
	for range 2 {
		f.usage("T-1", ts, "m2", 1, func(p *db.InsertUsageRecordParams) { p.InputTokens = 5e18 })
	}
	assert.Equal(t, "+Inf", scalar(t, f, `SELECT cost_usd FROM usage_q WHERE model = 'm'`), "前提：汇总里成本已上溢")

	encodable := func(v any) {
		t.Helper()
		_, err := json.Marshal(v)
		require.NoError(t, err)
	}
	day := int64(24 * time.Hour / time.Millisecond)
	for _, w := range []struct{ from, to int64 }{
		{ts - day, ts + day},      // 中段读汇总
		{ts - 1, ts + 1},          // 整段读原始行
		{ts - 7, now.UnixMilli()}, // 头尾原始行
	} {
		for _, g := range []string{GroupModel, GroupThread, GroupPurpose, GroupTotal, GrainDay} {
			u, err := Usage(f.ctx, f.st.Q, UsageQuery{GroupBy: g, From: w.from, To: w.to})
			require.NoError(t, err, g)
			encodable(u)
			assert.Positive(t, u.Total.InputTokens, "饱和而不是回绕成负数")
			assert.False(t, math.IsInf(u.Total.CostUsd, 0) || math.IsNaN(u.Total.CostUsd))
		}
		u, err := Usage(f.ctx, f.st.Q, UsageQuery{GroupBy: GroupModel, Series: GrainHour, From: w.from, To: w.to})
		require.NoError(t, err)
		encodable(u)
		tl, err := Tools(f.ctx, f.st.Q, ToolQuery{Series: GrainDay, From: w.from, To: w.to})
		require.NoError(t, err)
		encodable(tl)
		assert.Equal(t, int64(2), tl.Total.Calls)
	}
	_, err := f.st.Q.SumUsageCost(f.ctx, db.SumUsageCostParams{FromTs: ts - day, ToTs: ts + day})
	require.NoError(t, err, "TOTAL 不抛 integer overflow")

	n := chanNotifier{ch: make(chan string, 4)}
	s := New(Config{Now: func() time.Time { return now }}, f.st, nil, n)
	_, err = s.SetBudget(f.ctx, apigen.Budget{Enabled: true, DailyUsd: new(10.0), MonthlyUsd: new(100.0), WarnPct: 80, Timezone: "UTC"})
	require.NoError(t, err)
	b, err := s.Budget(f.ctx)
	require.NoError(t, err)
	encodable(b)
	assert.False(t, math.IsInf(b.Day.SpentUsd, 0) || math.IsNaN(b.Day.SpentUsd))
	assert.False(t, math.IsInf(b.Month.SpentUsd, 0) || math.IsNaN(b.Month.SpentUsd))
}

func scalar(t *testing.T, f *fixture, q string) string {
	t.Helper()
	var v any
	require.NoError(t, f.st.DB.QueryRowContext(f.ctx, q).Scan(&v))
	if x, ok := v.(float64); ok {
		if math.IsInf(x, 1) {
			return "+Inf"
		}
	}
	return jsonString(v)
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSatAdd(t *testing.T) {
	assert.Equal(t, int64(math.MaxInt64), satAdd(math.MaxInt64, 1))
	assert.Equal(t, int64(math.MaxInt64), satAdd(math.MaxInt64-1, math.MaxInt64))
	assert.Equal(t, int64(math.MinInt64), satAdd(math.MinInt64, -1))
	assert.Equal(t, int64(5), satAdd(2, 3))
	assert.Equal(t, int64(-1), satAdd(2, -3))
	assert.Equal(t, int64(7), satAdd(7, 0))
	assert.Zero(t, finite(math.Inf(1)))
	assert.Zero(t, finite(math.NaN()))
	assert.Equal(t, 1.5, finite(1.5))
}
