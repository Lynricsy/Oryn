package stats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

// localMs 是 loc 中本地墙钟 y-m-d h:00 的时刻（不在切换附近使用）。
func localMs(loc *time.Location, y int, m time.Month, d, h int) int64 {
	return time.Date(y, m, d, h, 0, 0, 0, loc).UnixMilli()
}

func keys(bs []Bucket) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Key
	}
	return out
}

func hours(b Bucket) float64 { return float64(b.End-b.Start) / float64(hourMs) }

func TestGridDays(t *testing.T) {
	cases := []struct {
		name     string
		tz       string
		from, to int64
		keys     []string
		hours    []float64
	}{
		{"UTC", "UTC", ms("2026-11-01T00:00:00Z"), ms("2026-11-04T00:00:00Z"),
			[]string{"2026-11-01", "2026-11-02", "2026-11-03"}, []float64{24, 24, 24}},
		{"上海：日界在 16:00Z，首尾裁到窗口", "Asia/Shanghai", ms("2026-11-14T00:00:00Z"), ms("2026-11-15T20:00:00Z"),
			[]string{"2026-11-14", "2026-11-15", "2026-11-16"}, []float64{16, 24, 4}},
		{"纽约春季切换日 23 小时", "America/New_York", ms("2026-03-07T05:00:00Z"), ms("2026-03-10T04:00:00Z"),
			[]string{"2026-03-07", "2026-03-08", "2026-03-09"}, []float64{24, 23, 24}},
		{"纽约秋季切换日 25 小时", "America/New_York", ms("2026-10-31T04:00:00Z"), ms("2026-11-03T05:00:00Z"),
			[]string{"2026-10-31", "2026-11-01", "2026-11-02"}, []float64{24, 25, 24}},
		{"加尔各答 +5:30 的日界在 18:30Z", "Asia/Kolkata", ms("2026-11-13T18:30:00Z"), ms("2026-11-15T18:30:00Z"),
			[]string{"2026-11-14", "2026-11-15"}, []float64{24, 24}},
		{"豪勋爵岛半小时夏令时结束：24.5 小时", "Australia/Lord_Howe", ms("2026-04-03T13:00:00Z"), ms("2026-04-06T13:30:00Z"),
			[]string{"2026-04-04", "2026-04-05", "2026-04-06"}, []float64{24, 24.5, 24}},
		{"豪勋爵岛半小时夏令时开始：23.5 小时", "Australia/Lord_Howe", ms("2026-10-02T13:30:00Z"), ms("2026-10-05T13:00:00Z"),
			[]string{"2026-10-03", "2026-10-04", "2026-10-05"}, []float64{24, 23.5, 24}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, err := Grid(mustLoc(t, c.tz), c.from, c.to, GrainDay)
			require.NoError(t, err)
			assert.Equal(t, c.keys, keys(g))
			got := make([]float64, len(g))
			for i, b := range g {
				got[i] = hours(b)
			}
			assert.Equal(t, c.hours, got)
			// 桶首尾相接、覆盖整个窗口
			assert.Equal(t, c.from, g[0].Start)
			assert.Equal(t, c.to, g[len(g)-1].End)
			for i := 1; i < len(g); i++ {
				assert.Equal(t, g[i-1].End, g[i].Start)
			}
		})
	}
}

func TestGridHoursAcrossDST(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 春季：本地 02:00 不存在，23 个小时桶，没有 T02:00
	g, err := Grid(ny, ms("2026-03-08T05:00:00Z"), ms("2026-03-09T04:00:00Z"), GrainHour)
	require.NoError(t, err)
	require.Len(t, g, 23)
	assert.Equal(t, "2026-03-08T01:00", g[1].Key)
	assert.Equal(t, "2026-03-08T03:00", g[2].Key, "跳过的 02:00 没有桶")
	assert.Equal(t, ms("2026-03-08T07:00:00Z"), g[2].Start)
	for _, b := range g {
		assert.Equal(t, 1.0, hours(b), b.Key)
	}
	// 秋季：本地 01:00 出现两次，合并成一个 2 小时的桶
	g, err = Grid(ny, ms("2026-11-01T04:00:00Z"), ms("2026-11-02T05:00:00Z"), GrainHour)
	require.NoError(t, err)
	require.Len(t, g, 24)
	assert.Equal(t, "2026-11-01T01:00", g[1].Key)
	assert.Equal(t, 2.0, hours(g[1]), "重复的本地小时合并进同一个桶")
	assert.Equal(t, "2026-11-01T02:00", g[2].Key)

	// 加尔各答：小时桶从 :30Z 开始
	g, err = Grid(mustLoc(t, "Asia/Kolkata"), ms("2026-11-14T00:00:00Z"), ms("2026-11-14T02:00:00Z"), GrainHour)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-11-14T05:00", "2026-11-14T06:00", "2026-11-14T07:00"}, keys(g))
	assert.Equal(t, ms("2026-11-14T00:30:00Z"), g[1].Start)
}

func TestGridWeeksAndMonths(t *testing.T) {
	sh := mustLoc(t, "Asia/Shanghai")
	// 2026-11-11 是周三：首桶从窗口起点裁起，次桶从周一（11-16）本地零点开始
	g, err := Grid(sh, localMs(sh, 2026, 11, 11, 0), localMs(sh, 2026, 11, 24, 0), GrainWeek)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-W46", "2026-W47", "2026-W48"}, keys(g))
	assert.Equal(t, localMs(sh, 2026, 11, 16, 0), g[1].Start)
	assert.Equal(t, 7*24.0, hours(g[1]))
	// ISO 周跨年：2027-01-01 属于 2026-W53
	g, err = Grid(time.UTC, ms("2026-12-31T00:00:00Z"), ms("2027-01-05T00:00:00Z"), GrainWeek)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-W53", "2027-W01"}, keys(g))

	g, err = Grid(sh, localMs(sh, 2026, 1, 15, 0), localMs(sh, 2026, 3, 2, 0), GrainMonth)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-01", "2026-02", "2026-03"}, keys(g))
	assert.Equal(t, localMs(sh, 2026, 2, 1, 0), g[1].Start)
	assert.Equal(t, 28*24.0, hours(g[1]))
}

func TestGridLimits(t *testing.T) {
	from := ms("2026-01-01T00:00:00Z")
	g, err := Grid(time.UTC, from, from+1000*hourMs, GrainHour)
	require.NoError(t, err)
	assert.Len(t, g, 1000)
	_, err = Grid(time.UTC, from, from+1001*hourMs, GrainHour)
	assert.ErrorIs(t, err, ErrRangeTooLarge)
	_, err = Grid(time.UTC, from, from+hourMs, "minute")
	assert.Error(t, err)
	g, err = Grid(time.UTC, from, from, GrainDay)
	require.NoError(t, err)
	assert.Empty(t, g)
}

func TestOffsetSegments(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	segs := offsetSegments(ny, ms("2026-01-01T00:00:00Z"), ms("2027-01-01T00:00:00Z"))
	require.Len(t, segs, 3)
	assert.Equal(t, segment{From: ms("2026-01-01T00:00:00Z"), To: ms("2026-03-08T07:00:00Z"), OffsetMs: -5 * hourMs}, segs[0])
	assert.Equal(t, segment{From: ms("2026-03-08T07:00:00Z"), To: ms("2026-11-01T06:00:00Z"), OffsetMs: -4 * hourMs}, segs[1])
	assert.Equal(t, segment{From: ms("2026-11-01T06:00:00Z"), To: ms("2027-01-01T00:00:00Z"), OffsetMs: -5 * hourMs}, segs[2])

	segs = offsetSegments(time.UTC, 0, ms("2027-01-01T00:00:00Z"))
	assert.Equal(t, []segment{{From: 0, To: ms("2027-01-01T00:00:00Z")}}, segs)

	lh := offsetSegments(mustLoc(t, "Australia/Lord_Howe"), ms("2026-04-04T00:00:00Z"), ms("2026-04-06T00:00:00Z"))
	require.Len(t, lh, 2)
	assert.Equal(t, int64(11*hourMs), lh[0].OffsetMs)
	assert.Equal(t, int64(10*hourMs+hourMs/2), lh[1].OffsetMs)
}

// SQL 序号经 slot 映射回的桶必须与按时刻直接落桶一致：逐 7 分钟扫过跨夏令时切换的窗口，各粒度、各时区。
func TestTimePlanSlot(t *testing.T) {
	for _, tz := range []string{"UTC", "America/New_York", "Australia/Lord_Howe", "Asia/Kolkata"} {
		loc := mustLoc(t, tz)
		from, to := ms("2026-09-20T03:17:00Z"), ms("2026-11-20T00:00:00Z")
		for _, g := range []string{GrainHour, GrainDay, GrainWeek, GrainMonth} {
			lo, hi := from, to
			if g == GrainHour {
				lo, hi = ms("2026-10-31T00:00:00Z"), ms("2026-11-03T00:00:00Z")
			}
			p, err := newTimePlan(loc, lo, hi, g)
			require.NoError(t, err)
			for ts := lo; ts < hi; ts += 7 * 60 * 1000 {
				for _, seg := range p.segs {
					if ts < seg.From || ts >= seg.To {
						continue
					}
					ord := (ts + p.sqlOffset(seg)) / p.bucketMs
					require.Equal(t, gridSlot(p.grid, ts), p.slot(seg, ord), "%s %s %s", tz, g, time.UnixMilli(ts).In(loc))
				}
			}
		}
	}
}
