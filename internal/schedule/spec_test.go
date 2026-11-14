package schedule

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func TestNextAfterCron(t *testing.T) {
	sh := mustLoc(t, "Asia/Shanghai")
	ny := mustLoc(t, "America/New_York")
	cases := []struct {
		name, spec string
		loc        *time.Location
		after      time.Time
		want       []time.Time // 依次的下一次
	}{
		{
			name: "工作日 9 点（上海）", spec: "0 9 * * 1-5", loc: sh,
			after: time.Date(2026, 11, 13, 10, 0, 0, 0, sh), // 周五 10:00
			want:  []time.Time{time.Date(2026, 11, 16, 9, 0, 0, 0, sh), time.Date(2026, 11, 17, 9, 0, 0, 0, sh)},
		},
		{
			// 春季跳过的 02:00 当天不触发（robfig 跳到下一天）
			name: "纽约春令时 2 点", spec: "0 2 * * *", loc: ny,
			after: time.Date(2026, 3, 7, 3, 0, 0, 0, ny),
			want:  []time.Time{time.Date(2026, 3, 9, 2, 0, 0, 0, ny)},
		},
		{
			name: "日与周取并集", spec: "0 0 1 * 1", loc: time.UTC,
			after: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), // 6/1 是周一
			want: []time.Time{time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		},
		{name: "宏", spec: "@daily", loc: sh, after: time.Date(2026, 1, 1, 0, 0, 0, 0, sh), want: []time.Time{time.Date(2026, 1, 2, 0, 0, 0, 0, sh)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at := c.after
			for _, w := range c.want {
				got, ok := NextAfter(KindCron, c.spec, c.loc, at)
				require.True(t, ok)
				assert.True(t, w.Equal(got), "want %s got %s", w, got)
				at = got
			}
		})
	}
}

func TestNextAfterFallBackFiresTwice(t *testing.T) {
	// 秋季 01:00 出现两次：robfig 两次都触发（钉住该行为）
	ny := mustLoc(t, "America/New_York")
	first := time.Date(2026, 11, 1, 0, 30, 0, 0, ny)
	a, ok := NextAfter(KindCron, "0 1 * * *", ny, first)
	require.True(t, ok)
	b, ok := NextAfter(KindCron, "0 1 * * *", ny, a)
	require.True(t, ok)
	assert.Equal(t, time.Hour, b.Sub(a), "同一天的第二个 01:00")
	c, ok := NextAfter(KindCron, "0 1 * * *", ny, b)
	require.True(t, ok)
	assert.Equal(t, 2, c.Day())
}

func TestNextAfterEveryAndOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, ok := NextAfter(KindEvery, "30m0s", time.UTC, now)
	require.True(t, ok)
	assert.Equal(t, now.Add(30*time.Minute), got)
	_, ok = NextAfter(KindOnce, "", time.UTC, now)
	assert.False(t, ok)
}

func TestCountMissed(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, 4, countMissed(KindEvery, "1m0s", time.UTC, t0, t0.Add(4*time.Minute+30*time.Second)))
	assert.Equal(t, 4, countMissed(KindCron, "* * * * *", time.UTC, t0, t0.Add(4*time.Minute+30*time.Second)))
	assert.Equal(t, 0, countMissed(KindCron, "0 * * * *", time.UTC, t0, t0.Add(59*time.Minute)))
	assert.Equal(t, maxMissedScan, countMissed(KindCron, "* * * * *", time.UTC, t0, t0.Add(48*time.Hour)))
}

func TestValidate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) *int64 { v := now.Add(d).UnixMilli(); return &v }
	ok := func(in Input) Input {
		t.Helper()
		out, _, err := Validate(in, now)
		require.NoError(t, err)
		return out
	}
	out := ok(Input{Kind: KindCron, Cron: " 0  9 * * 1-5 ", Prompt: "p", Timezone: "Asia/Shanghai"})
	assert.Equal(t, "0 9 * * 1-5", out.Cron)
	out = ok(Input{Kind: KindEvery, Every: "90m", Prompt: "p"})
	assert.Equal(t, "1h30m0s", out.Every)
	assert.Equal(t, "UTC", out.Timezone)
	ok(Input{Kind: KindOnce, At: ms(-30 * time.Second), Prompt: "p"})
	out = ok(Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: ModeNewThread, Name: "tick"})
	assert.Equal(t, OverlapSkip, out.Overlap)
	assert.Equal(t, 5, *out.KeepLast)

	bad := []struct {
		name string
		in   Input
	}{
		{"6 段", Input{Kind: KindCron, Cron: "0 0 9 * * 1", Prompt: "p"}},
		{"@every", Input{Kind: KindCron, Cron: "@every 5m", Prompt: "p"}},
		{"CRON_TZ", Input{Kind: KindCron, Cron: "CRON_TZ=UTC 0 9 * * *", Prompt: "p"}},
		{"TZ", Input{Kind: KindCron, Cron: "TZ=UTC 0 9 * * *", Prompt: "p"}},
		{"非法表达式", Input{Kind: KindCron, Cron: "61 * * * *", Prompt: "p"}},
		{"非法时区", Input{Kind: KindCron, Cron: "0 9 * * *", Timezone: "Mars/Base", Prompt: "p"}},
		{"间隔太短", Input{Kind: KindEvery, Every: "30s", Prompt: "p"}},
		{"非整秒", Input{Kind: KindEvery, Every: "1m500ms", Prompt: "p"}},
		{"一次性已过", Input{Kind: KindOnce, At: ms(-2 * time.Minute), Prompt: "p"}},
		{"截止已过", Input{Kind: KindEvery, Every: "1h", EndsAt: ms(-time.Second), Prompt: "p"}},
		{"缺提示词", Input{Kind: KindEvery, Every: "1h", Prompt: "  "}},
		{"未知种类", Input{Kind: "weekly", Prompt: "p"}},
		{"thread 带项目字段", Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: ModeThread, ProjectID: "prj_x"}},
		{"thread 带 keepLast", Input{Kind: KindEvery, Every: "1h", Prompt: "p", KeepLast: new(3)}},
		{"new_thread 带线程", Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: ModeNewThread, Name: "n", ThreadID: "T-1"}},
		{"new_thread 缺名", Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: ModeNewThread}},
		{"keepLast 越界", Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: ModeNewThread, Name: "n", KeepLast: new(51)}},
		{"@host", Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: ModeNewThread, Name: "n", Provider: "@host"}},
		{"模型缺 id", Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: ModeNewThread, Name: "n", Model: &apigen.ModelRef{Provider: "x"}}},
		{"overlap 非法", Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: ModeNewThread, Name: "n", Overlap: "queue"}},
		{"未知模式", Input{Kind: KindEvery, Every: "1h", Prompt: "p", Mode: "loop"}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Validate(c.in, now)
			var se *Error
			require.ErrorAs(t, err, &se)
			assert.Equal(t, CodeInvalidSchedule, se.Code)
		})
	}
}
