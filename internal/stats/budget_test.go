package stats

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

type postCall struct{ kind, title, body, link string }

type fakeNotifier struct {
	mu    sync.Mutex
	calls []postCall
	err   error
}

func (n *fakeNotifier) PostSystem(_ context.Context, kind, title, body, link string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, postCall{kind, title, body, link})
	return n.err
}

type budgetEnv struct {
	*fixture
	now time.Time
	n   *fakeNotifier
	s   *Service
}

func newBudgetEnv(t *testing.T, withNotifier bool) *budgetEnv {
	e := &budgetEnv{fixture: newFixture(t), now: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC), n: &fakeNotifier{}}
	e.thread("T", "", "", "work")
	var n Notifier
	if withNotifier {
		n = e.n
	}
	e.s = New(Config{Now: func() time.Time { return e.now }}, e.st, nil, n)
	return e
}

func (e *budgetEnv) spend(cost float64) { e.usage("T", e.now.UnixMilli()-1, "m", cost) }

func (e *budgetEnv) set(b apigen.Budget) apigen.BudgetStatus {
	e.t.Helper()
	st, err := e.s.SetBudget(e.ctx, b)
	require.NoError(e.t, err)
	return st
}

func (e *budgetEnv) ledger() []string {
	e.t.Helper()
	rows, err := e.st.DB.QueryContext(e.ctx, `SELECT period || ' ' || period_key || ' ' || level FROM budget_alerts ORDER BY period, period_key, level`)
	require.NoError(e.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(e.t, rows.Scan(&s))
		out = append(out, s)
	}
	return out
}

func (e *budgetEnv) titles() []string {
	var out []string
	for _, c := range e.n.calls {
		out = append(out, c.title)
	}
	return out
}

func TestBudgetDefaults(t *testing.T) {
	e := newBudgetEnv(t, true)
	e.spend(100)
	st, err := e.s.Budget(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.Budget{WarnPct: 80, Timezone: "UTC", Enabled: false}, st.Budget)
	assert.Equal(t, apigen.BudgetLevelNone, st.Day.Level)
	assert.Equal(t, apigen.BudgetLevelNone, st.Month.Level)
	assert.Equal(t, "2026-11-14", st.Day.Key)
	assert.Equal(t, "2026-11", st.Month.Key)
	assert.InDelta(t, 100, st.Day.SpentUsd, 1e-9)
	e.s.EvaluateBudget(e.ctx)
	assert.Empty(t, e.n.calls, "未设置：不告警")
	assert.Empty(t, e.ledger())

	e.set(apigen.Budget{DailyUsd: new(1.0), WarnPct: 80, Timezone: "UTC", Enabled: false})
	e.s.EvaluateBudget(e.ctx)
	assert.Empty(t, e.n.calls, "enabled = false：不告警")
	assert.Empty(t, e.ledger())
}

func TestBudgetWarnThenOver(t *testing.T) {
	e := newBudgetEnv(t, true)
	st := e.set(apigen.Budget{DailyUsd: new(1.0), WarnPct: 80, Timezone: "UTC", Enabled: true})
	assert.Equal(t, apigen.BudgetLevelNone, st.Day.Level)
	assert.Nil(t, st.Month.LimitUsd)
	e.spend(0.5)
	e.s.EvaluateBudget(e.ctx)
	assert.Empty(t, e.n.calls)

	e.spend(0.35)
	e.s.EvaluateBudget(e.ctx)
	require.Len(t, e.n.calls, 1)
	c := e.n.calls[0]
	assert.Equal(t, postCall{"budget", "Daily budget warning", "$0.85 of $1.00 spent, past the 80% warning threshold (2026-11-14, UTC).", "/stats/usage"}, c)

	e.spend(0.2)
	st, err := e.s.Budget(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.BudgetLevelOver, st.Day.Level)
	for range 3 {
		e.s.EvaluateBudget(e.ctx)
	}
	assert.Equal(t, []string{"Daily budget warning", "Daily budget exceeded"}, e.titles(), "重复评估不重发")
	assert.Equal(t, "$1.05 of $1.00 spent (2026-11-14, UTC).", e.n.calls[1].body)
	assert.Equal(t, []string{"day 2026-11-14 over", "day 2026-11-14 warn"}, e.ledger())

	// 换日：新周期首次评估就已超限 → 只发 exceeded，warn / over 两行都落账本
	e.now = e.now.Add(24 * time.Hour)
	e.spend(2)
	e.s.EvaluateBudget(e.ctx)
	assert.Equal(t, []string{"Daily budget warning", "Daily budget exceeded", "Daily budget exceeded"}, e.titles())
	assert.Equal(t, []string{"day 2026-11-14 over", "day 2026-11-14 warn", "day 2026-11-15 over", "day 2026-11-15 warn"}, e.ledger())
}

// events 保留期缩到 24h 时，月预算的去重账本不能被清掉（同月重复告警），跨月后照常发新周期的告警
func TestBudgetLedgerOutlivesShortEventRetention(t *testing.T) {
	e := newBudgetEnv(t, true)
	e.s.cfg.Retention.Events = config.Duration(24 * time.Hour)
	e.set(apigen.Budget{MonthlyUsd: new(1.0), WarnPct: 80, Timezone: "UTC", Enabled: true})
	e.spend(2)
	e.s.EvaluateBudget(e.ctx)
	require.Len(t, e.n.calls, 1)
	for range 3 {
		e.now = e.now.Add(25 * time.Hour)
		e.s.Sweep(e.ctx)
		e.s.EvaluateBudget(e.ctx)
	}
	assert.Equal(t, []string{"Monthly budget exceeded"}, e.titles(), "同一个月不重发")
	assert.Equal(t, []string{"month 2026-11 over", "month 2026-11 warn"}, e.ledger())

	e.now = time.Date(2026, 12, 2, 10, 0, 0, 0, time.UTC)
	e.spend(2)
	e.s.Sweep(e.ctx)
	e.s.EvaluateBudget(e.ctx)
	assert.Equal(t, []string{"Monthly budget exceeded", "Monthly budget exceeded"}, e.titles(), "新的月份照常告警")
}

func TestBudgetVariants(t *testing.T) {
	t.Run("warnPct = 0 只有 over", func(t *testing.T) {
		e := newBudgetEnv(t, true)
		e.set(apigen.Budget{DailyUsd: new(1.0), WarnPct: 0, Timezone: "UTC", Enabled: true})
		e.spend(5)
		e.s.EvaluateBudget(e.ctx)
		assert.Equal(t, []string{"Daily budget exceeded"}, e.titles())
		assert.Equal(t, []string{"day 2026-11-14 over"}, e.ledger())
	})
	t.Run("日与月同时首次越线：两条", func(t *testing.T) {
		e := newBudgetEnv(t, true)
		e.set(apigen.Budget{DailyUsd: new(1.0), MonthlyUsd: new(2.0), WarnPct: 50, Timezone: "UTC", Enabled: true})
		e.spend(1.2)
		e.s.EvaluateBudget(e.ctx)
		assert.Equal(t, []string{"Daily budget exceeded", "Monthly budget warning"}, e.titles())
		assert.Contains(t, e.n.calls[1].body, "(2026-11, UTC)")
	})
	t.Run("通知失败：账本保留，不重试", func(t *testing.T) {
		e := newBudgetEnv(t, true)
		e.n.err = errors.New("db down")
		e.set(apigen.Budget{DailyUsd: new(1.0), WarnPct: 80, Timezone: "UTC", Enabled: true})
		e.spend(2)
		e.s.EvaluateBudget(e.ctx)
		e.s.EvaluateBudget(e.ctx)
		assert.Len(t, e.n.calls, 1)
		assert.Equal(t, []string{"day 2026-11-14 over", "day 2026-11-14 warn"}, e.ledger())
	})
	t.Run("没有 Notifier 只落账本", func(t *testing.T) {
		e := newBudgetEnv(t, false)
		e.set(apigen.Budget{DailyUsd: new(1.0), WarnPct: 80, Timezone: "UTC", Enabled: true})
		e.spend(2)
		assert.NotPanics(t, func() { e.s.EvaluateBudget(e.ctx) })
		assert.Len(t, e.ledger(), 2)
	})
	t.Run("时区决定周期边界", func(t *testing.T) {
		e := newBudgetEnv(t, true)
		st := e.set(apigen.Budget{DailyUsd: new(1.0), WarnPct: 80, Timezone: "Asia/Shanghai", Enabled: true})
		assert.Equal(t, "2026-11-14", st.Day.Key)
		assert.EqualValues(t, ms("2026-11-13T16:00:00Z"), st.Day.From)
		assert.EqualValues(t, ms("2026-11-14T16:00:00Z"), st.Day.To)
		assert.EqualValues(t, ms("2026-10-31T16:00:00Z"), st.Month.From)
	})
}

func TestSetBudgetInvalid(t *testing.T) {
	e := newBudgetEnv(t, true)
	for name, b := range map[string]apigen.Budget{
		"日上限 < 0.01":  {DailyUsd: new(0.001), WarnPct: 80, Timezone: "UTC"},
		"月上限为负":       {MonthlyUsd: new(-1.0), WarnPct: 80, Timezone: "UTC"},
		"warnPct 100": {WarnPct: 100, Timezone: "UTC"},
		"warnPct 为负":  {WarnPct: -1, Timezone: "UTC"},
		"时区非法":        {WarnPct: 80, Timezone: "Mars/Phobos"},
		"时区为空":        {WarnPct: 80},
	} {
		_, err := e.s.SetBudget(e.ctx, b)
		var se *Error
		require.ErrorAs(t, err, &se, name)
		assert.Equal(t, CodeInvalidBudget, se.Code, name)
	}
	_, err := e.st.Q.GetBudget(e.ctx)
	assert.Error(t, err, "非法输入不落库")
}
