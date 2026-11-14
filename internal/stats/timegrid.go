package stats

import (
	"fmt"
	"sort"
	"time"
	_ "time/tzdata" // 内嵌时区库：宿主缺 /usr/share/zoneinfo 时 tz 参数照样可用
)

// MaxBuckets 是单次查询的时间桶上限（超出回 range_too_large）。
const MaxBuckets = 1000

// 时间粒度。
const (
	GrainHour  = "hour"
	GrainDay   = "day"
	GrainWeek  = "week"
	GrainMonth = "month"
)

// IsGrain 报告 s 是否是时间粒度。
func IsGrain(s string) bool {
	switch s {
	case GrainHour, GrainDay, GrainWeek, GrainMonth:
		return true
	}
	return false
}

const (
	hourMs = int64(time.Hour / time.Millisecond)
	dayMs  = 24 * hourMs
)

// Bucket 是一个时间桶：[Start, End)，已裁到窗口。
type Bucket struct {
	Key        string
	Start, End int64
}

// Grid 按 loc 的自然 hour / day / week（周一为始）/ month 切分 [from, to)；超过 MaxBuckets 个桶返回 ErrRangeTooLarge。
// 桶键：`2006-01-02T15:00`、`2006-01-02`、ISO 周 `2006-W46`、`2006-01`。夏令时跳过的本地小时没有桶，
// 回拨时重复的本地小时合并进同一个桶（因而切换日的日桶为 23 / 25 小时）。
func Grid(loc *time.Location, from, to int64, grain string) ([]Bucket, error) {
	if !IsGrain(grain) {
		return nil, errorf(CodeInvalidRequest, "粒度 %q 无效", grain)
	}
	if from >= to {
		return []Bucket{}, nil
	}
	cur := periodLabel(time.UnixMilli(from).In(loc), grain)
	cs := localStart(loc, cur)
	out := []Bucket{}
	for cs < to {
		next := advance(cur, grain)
		ns := localStart(loc, next)
		// ns == cs：cur 这个本地标签不存在（夏令时跳过），没有桶
		if ns > cs {
			if b := (Bucket{Key: bucketKey(cur, grain), Start: max(cs, from), End: min(ns, to)}); b.Start < b.End {
				if len(out) == MaxBuckets {
					return nil, ErrRangeTooLarge
				}
				out = append(out, b)
			}
		}
		cur, cs = next, ns
	}
	return out, nil
}

// periodLabel 返回 t（已在本地时区）所在周期起点的本地墙钟（以 UTC 编码的「标签」）。
func periodLabel(t time.Time, grain string) time.Time {
	y, m, d := t.Date()
	h := t.Hour()
	switch grain {
	case GrainDay:
		h = 0
	case GrainWeek:
		h = 0
		d -= (int(t.Weekday()) + 6) % 7
	case GrainMonth:
		h, d = 0, 1
	}
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC)
}

// advance 把标签推进一个周期（标签是 UTC 编码的墙钟，不受夏令时影响）。
func advance(label time.Time, grain string) time.Time {
	switch grain {
	case GrainHour:
		return label.Add(time.Hour)
	case GrainDay:
		return label.AddDate(0, 0, 1)
	case GrainWeek:
		return label.AddDate(0, 0, 7)
	default:
		return label.AddDate(0, 1, 0)
	}
}

func bucketKey(label time.Time, grain string) string {
	switch grain {
	case GrainHour:
		return label.Format("2006-01-02T15:00")
	case GrainDay:
		return label.Format("2006-01-02")
	case GrainWeek:
		y, w := label.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	default:
		return label.Format("2006-01")
	}
}

// localStart 返回本地墙钟 label 在 loc 中第一次出现的时刻（毫秒）。label 落在夏令时跳过的区间里时取切换时刻；
// 回拨造成的重复取较早的一次。结果随 label 单调不减。
func localStart(loc *time.Location, label time.Time) int64 {
	t := time.Date(label.Year(), label.Month(), label.Day(), label.Hour(), 0, 0, 0, loc)
	if wallOf(t).Before(label) {
		// time.Date 把不存在的墙钟按切换前的偏移换算，落到了切换之前：切换时刻即该标签的起点
		if _, end := t.ZoneBounds(); !end.IsZero() {
			t = end
		}
	}
	if wallOf(t).After(label) {
		// 换算落到了切换之后（同一个跳过区间的另一种表示）：起点是切换时刻
		if start, _ := t.ZoneBounds(); !start.IsZero() && t.Sub(start) < 24*time.Hour && wallOf(start.Add(-time.Millisecond)).Before(label) {
			t = start
		}
	}
	// 回拨造成的重复墙钟：time.Date 对选哪一次不作保证（柏林取第二次）。
	// 若当前区段由回拨开启、且旧偏移下同一墙钟落在切换之前，改取那一次（更早）。
	if start, _ := t.ZoneBounds(); !start.IsZero() {
		_, prevOff := start.Add(-time.Nanosecond).Zone()
		if _, curOff := t.Zone(); prevOff > curOff {
			c := t.Add(time.Duration(curOff-prevOff) * time.Second)
			if c.Before(start) && wallOf(c).Equal(label) {
				t = c
			}
		}
	}
	return t.UnixMilli()
}

// wallOf 返回 t 的本地墙钟（以 UTC 编码）。
func wallOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// segment 是一段 UTC 偏移恒定的时间：[From, To)。
type segment struct {
	From, To, OffsetMs int64
}

// offsetSegments 把 [from, to) 切成 UTC 偏移恒定的片段（在 loc 的偏移变化处断开；只改缩写不改偏移的切换合并）。
// 切换点取自时区库的区段边界（ZoneBounds），精确到秒。
func offsetSegments(loc *time.Location, from, to int64) []segment {
	var out []segment
	for t := from; t < to; {
		tm := time.UnixMilli(t).In(loc)
		_, off := tm.Zone()
		e := to
		if _, end := tm.ZoneBounds(); !end.IsZero() && end.UnixMilli() < to {
			e = end.UnixMilli()
		}
		if n := len(out); n > 0 && out[n-1].OffsetMs == int64(off)*1000 {
			out[n-1].To = e
		} else {
			out = append(out, segment{From: t, To: e, OffsetMs: int64(off) * 1000})
		}
		t = e
	}
	return out
}

// timePlan 是一次聚合的时间切分。bucketed 为假时只有一段（偏移 0），SQL 的桶序号恒为 0、下标恒为 0。
type timePlan struct {
	bucketed bool
	grid     []Bucket
	segs     []segment
	// SQL 的桶：序号 = (ts + 片段偏移 + shiftMs) / bucketMs。hour 用小时；week 用 7 天并平移 3 天（1970-01-01 是周四，
	// 平移后序号以本地周一零点为界）；day / month 用日（月由日折叠）。
	bucketMs, shiftMs int64
}

// newTimePlan 为 grain（空 = 不分时间桶）构造切分。
func newTimePlan(loc *time.Location, from, to int64, grain string) (timePlan, error) {
	if grain == "" {
		return timePlan{segs: []segment{{From: from, To: to}}, bucketMs: 1}, nil
	}
	grid, err := Grid(loc, from, to, grain)
	if err != nil {
		return timePlan{}, err
	}
	p := timePlan{bucketed: true, grid: grid, segs: offsetSegments(loc, from, to), bucketMs: dayMs}
	switch grain {
	case GrainHour:
		p.bucketMs = hourMs
	case GrainWeek:
		// 周桶在 SQL 里直接成形：结果行少 7 倍（按线程分组时尤其明显）
		p.bucketMs, p.shiftMs = 7*dayMs, 3*dayMs
	}
	return p, nil
}

// sqlOffset 是传给 SQL 的 offset_ms（片段的 UTC 偏移加上桶对齐的平移）。
func (p timePlan) sqlOffset(seg segment) int64 { return seg.OffsetMs + p.shiftMs }

// slot 把 SQL 在片段 seg 内返回的本地序号 ord 映射到网格下标；不在网格内返回 -1。月由日序号折叠：本地日的起点
// （按本片段的偏移换算、不早于片段起点）落在哪个桶，该日就归哪个桶。
func (p timePlan) slot(seg segment, ord int64) int {
	if !p.bucketed {
		return 0
	}
	at := max(ord*p.bucketMs-p.sqlOffset(seg), seg.From)
	i := sort.Search(len(p.grid), func(i int) bool { return p.grid[i].End > at })
	if i == len(p.grid) || p.grid[i].Start > at {
		return -1
	}
	return i
}

// gridSlot 返回包含时刻 ts 的桶下标；不在网格内返回 -1。
func gridSlot(grid []Bucket, ts int64) int {
	i := sort.Search(len(grid), func(i int) bool { return grid[i].End > ts })
	if i == len(grid) || grid[i].Start > ts {
		return -1
	}
	return i
}
