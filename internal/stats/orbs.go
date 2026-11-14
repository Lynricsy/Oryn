package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// runningStates 是容器在运行（计入 runningMs）的状态。
var runningStates = map[string]bool{
	"connecting": true, "setting_up": true, "resuming": true, "ready": true, "freezing": true, "stopping": true,
}

// setupStates 是计入 setupMs（同时计入 runningMs）的状态。
var setupStates = map[string]bool{"setting_up": true, "resuming": true}

// SlowSetupMs 是「慢 setup」的阈值。
const SlowSetupMs = 120000

// MaxTimelineSegments 是时间线的段数上限。
const MaxTimelineSegments = 2000

// OrbQuery 是一次 orb 统计查询。GroupBy 为空时取 type；Project 的 "none" = 无项目。
type OrbQuery struct {
	GroupBy      string
	From, To     int64
	Loc          *time.Location
	Project, Orb *string
}

// addOrbStat 把 b 的计数与时长加进 a（不动键与标签）。
func addOrbStat(a *apigen.OrbStat, b *apigen.OrbStat) {
	a.Orbs += b.Orbs
	a.RunningMs += b.RunningMs
	a.FrozenMs += b.FrozenMs
	a.StoppedMs += b.StoppedMs
	a.SetupMs += b.SetupMs
	a.Spawns += b.Spawns
	a.SnapshotSpawns += b.SnapshotSpawns
	a.Freezes += b.Freezes
	a.Stops += b.Stops
	a.Wakes += b.Wakes
	a.Failures += b.Failures
}

// addDuration 把处于 state 的 d 毫秒计入 s。
func addDuration(s *apigen.OrbStat, state string, d int64) {
	switch {
	case runningStates[state]:
		s.RunningMs += d
		if setupStates[state] {
			s.SetupMs += d
		}
	case state == "frozen":
		s.FrozenMs += d
	case state == "stopped":
		s.StoppedMs += d
	}
}

// countTransition 把一次真实的转换（from ≠ to）计入 s。
func countTransition(s *apigen.OrbStat, to string) {
	switch to {
	case "spawning":
		s.Spawns++
	case "frozen":
		s.Freezes++
	case "stopped":
		s.Stops++
	case "waking":
		s.Wakes++
	case "failed":
		s.Failures++
	}
}

// Orbs 由 orb_events 计算 [From, To) 内 orb 的状态时长与次数（协调宿主的 orb 不计）。To 先截到 now + 1；
// 每台 orb 从它在 From 时的状态起逐个事件扫出状态区间，时间粒度分组按桶边界切开分摊；次数只在 from_state ≠ to_state 时累加。
func Orbs(ctx context.Context, q *db.Queries, oq OrbQuery, now int64) (apigen.OrbStats, error) {
	groupBy := oq.GroupBy
	if groupBy == "" {
		groupBy = GroupType
	}
	switch groupBy {
	case GroupType, GroupProject, GroupOrb, GrainDay, GrainWeek, GrainMonth:
	default:
		return apigen.OrbStats{}, errorf(CodeInvalidRequest, "groupBy %q 无效", groupBy)
	}
	if oq.From >= oq.To {
		return apigen.OrbStats{}, errorf(CodeInvalidRequest, "from 必须早于 to")
	}
	loc := oq.Loc
	if loc == nil {
		loc = time.UTC
	}
	from, to := oq.From, min(oq.To, now+1)
	to = max(to, from)
	var grid []Bucket
	if IsGrain(groupBy) {
		var err error
		if grid, err = Grid(loc, oq.From, oq.To, groupBy); err != nil {
			return apigen.OrbStats{}, err
		}
	}
	project := noneToEmpty(oq.Project)
	orbs, err := q.ListOrbsForStats(ctx, db.ListOrbsForStatsParams{FromTs: from, ToTs: to, Project: project, Orb: oq.Orb})
	if err != nil {
		return apigen.OrbStats{}, err
	}
	events, err := q.ListOrbEventsInWindow(ctx, db.ListOrbEventsInWindowParams{FromTs: from, ToTs: to, Project: project, Orb: oq.Orb})
	if err != nil {
		return apigen.OrbStats{}, err
	}
	byOrb := map[string][]db.ListOrbEventsInWindowRow{}
	for _, e := range events {
		byOrb[e.OrbID] = append(byOrb[e.OrbID], e)
	}
	var labels map[string]string
	switch groupBy {
	case GroupProject:
		labels, err = projectNames(ctx, q)
	case GroupOrb:
		labels, err = orbTitles(ctx, q)
	}
	if err != nil {
		return apigen.OrbStats{}, err
	}

	out := apigen.OrbStats{GroupBy: apigen.OrbGroupBy(groupBy), From: oq.From, To: oq.To, Tz: loc.String(), Total: apigen.OrbStat{Key: GroupTotal}}
	buckets := make([]apigen.OrbStat, len(grid))
	for i, b := range grid {
		buckets[i] = apigen.OrbStat{Key: b.Key, Start: new(b.Start)}
	}
	groups := map[string]*apigen.OrbStat{}
	coordinator := map[string]bool{}
	for _, o := range orbs {
		if o.Coordinator {
			coordinator[o.ID] = true
			continue
		}
		var st apigen.OrbStat
		st.Orbs = 1
		snap := o.SnapshotOf != nil && o.CreatedAt >= from && o.CreatedAt < to
		if snap {
			st.SnapshotSpawns = 1
		}
		// 时间粒度：按桶分摊
		span := func(state string, a, b int64) {
			if b <= a {
				return
			}
			addDuration(&st, state, b-a)
			if grid == nil {
				return
			}
			for i := gridSlot(grid, a); i >= 0 && i < len(grid) && grid[i].Start < b; i++ {
				addDuration(&buckets[i], state, min(b, grid[i].End)-max(a, grid[i].Start))
			}
		}
		state, t := o.StateAtFrom, from
		for _, e := range byOrb[o.ID] {
			span(state, t, e.Ts)
			if e.FromState != e.ToState {
				countTransition(&st, e.ToState)
				if i := gridSlot(grid, e.Ts); i >= 0 {
					countTransition(&buckets[i], e.ToState)
				}
			}
			state, t = e.ToState, e.Ts
		}
		span(state, t, to)
		for i, b := range grid {
			if b.Start < to && o.CreatedAt < b.End && (o.DeletedAt == nil || *o.DeletedAt >= b.Start) {
				buckets[i].Orbs++
			}
			if snap && o.CreatedAt >= b.Start && o.CreatedAt < b.End {
				buckets[i].SnapshotSpawns++
			}
		}
		addOrbStat(&out.Total, &st)
		if grid != nil {
			continue
		}
		key := o.Type
		switch groupBy {
		case GroupProject:
			key = ""
			if o.ProjectID != nil {
				key = *o.ProjectID
			}
		case GroupOrb:
			key = o.ID
		}
		g := groups[key]
		if g == nil {
			g = &apigen.OrbStat{Key: key}
			if l := labels[key]; l != "" {
				g.Label = new(l)
			}
			if groupBy != GroupProject {
				g.Type, g.Cpus, g.MemoryMB = new(o.Type), new(int(o.Cpus)), new(int(o.MemoryMb))
			}
			groups[key] = g
		}
		addOrbStat(g, &st)
	}
	if grid != nil {
		out.Groups = buckets
	} else {
		out.Groups = make([]apigen.OrbStat, 0, len(groups))
		for _, g := range groups {
			out.Groups = append(out.Groups, *g)
		}
		slices.SortFunc(out.Groups, func(a, b apigen.OrbStat) int {
			if a.Key < b.Key {
				return -1
			}
			if a.Key > b.Key {
				return 1
			}
			return 0
		})
	}
	runs, err := q.ListSetupRunsForStats(ctx, db.ListSetupRunsForStatsParams{FromTs: from, ToTs: to, Project: project, Orb: oq.Orb})
	if err != nil {
		return apigen.OrbStats{}, err
	}
	out.Setup = setupStats(runs, coordinator)
	return out, nil
}

// setupStats 汇总 setup 记录：phase = setup 且真正运行了脚本的记录计 runs / 分位数（最近邻法），phase = resume 计 resume*。
func setupStats(runs []db.ListSetupRunsForStatsRow, skip map[string]bool) apigen.OrbSetupStats {
	var s apigen.OrbSetupStats
	var durs []int64
	for _, r := range runs {
		if skip[r.OrbID] {
			continue
		}
		switch {
		case r.Phase == "setup" && r.SetupRan != 0:
			s.Runs++
			if r.Ok == 0 {
				s.Failures++
			}
			if r.DurationMs >= SlowSetupMs {
				s.SlowRuns++
			}
			durs = append(durs, r.DurationMs)
		case r.Phase == "resume":
			s.ResumeRuns++
			if r.Ok == 0 {
				s.ResumeFailures++
			}
		}
	}
	if len(durs) > 0 {
		slices.Sort(durs)
		s.P50Ms, s.P95Ms, s.MaxMs = new(nearestRank(durs, 50)), new(nearestRank(durs, 95)), new(durs[len(durs)-1])
	}
	return s
}

// nearestRank 是升序 xs 的第 p 百分位数（最近邻法：第 ⌈p/100·n⌉ 个）。
func nearestRank(xs []int64, p int) int64 {
	n := len(xs)
	k := (p*n + 99) / 100
	return xs[max(k, 1)-1]
}

// orbTitles 返回 orb id → 它第一个（最早创建的）线程的标题。
func orbTitles(ctx context.Context, q *db.Queries) (map[string]string, error) {
	rows, err := q.ListThreads(ctx, db.ListThreadsParams{})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	first := map[string]int64{}
	for _, t := range rows {
		if t.OrbID == nil {
			continue
		}
		if at, ok := first[*t.OrbID]; !ok || t.CreatedAt < at {
			first[*t.OrbID] = t.CreatedAt
			out[*t.OrbID] = t.Title
		}
	}
	return out, nil
}

// Timeline 返回单台 orb 在 [from, to) 的状态时间线：To 截到 now；相邻同状态合并；failed 段的 reason 取事件
// detail.reason（没有则 detail.error）；超过 MaxTimelineSegments 段置 truncated。orb 不存在返回 not_found。
func Timeline(ctx context.Context, q *db.Queries, orbID string, from, to, now int64) (apigen.OrbTimeline, error) {
	if _, err := q.GetOrb(ctx, orbID); errors.Is(err, sql.ErrNoRows) {
		return apigen.OrbTimeline{}, errorf(CodeNotFound, "orb %s 不存在", orbID)
	} else if err != nil {
		return apigen.OrbTimeline{}, err
	}
	if from >= to {
		return apigen.OrbTimeline{}, errorf(CodeInvalidRequest, "from 必须早于 to")
	}
	to = max(min(to, now), from)
	rows, err := q.ListOrbEventsForTimeline(ctx, db.ListOrbEventsForTimelineParams{OrbID: orbID, FromTs: from, ToTs: to})
	if err != nil {
		return apigen.OrbTimeline{}, err
	}
	out := apigen.OrbTimeline{OrbID: orbID, From: from, To: to, Segments: []apigen.OrbTimelineSegment{}}
	// 查询有 5000 行上限：取满说明后面还有事件
	out.Truncated = len(rows) >= 5000
	for i, r := range rows {
		a, b := max(r.Ts, from), to
		if i+1 < len(rows) {
			b = min(rows[i+1].Ts, to)
		}
		if b <= a {
			continue
		}
		state := apigen.OrbState(r.ToState)
		if n := len(out.Segments); n > 0 && out.Segments[n-1].State == state && out.Segments[n-1].To == a {
			out.Segments[n-1].To = b
			continue
		}
		seg := apigen.OrbTimelineSegment{From: a, To: b, State: state}
		if state == apigen.OrbStateFailed {
			seg.Reason = failureReason(r.Detail)
		}
		if len(out.Segments) == MaxTimelineSegments {
			out.Truncated = true
			break
		}
		out.Segments = append(out.Segments, seg)
	}
	return out, nil
}

// failureReason 取事件 detail 里的 reason（没有则 error）。
func failureReason(detail string) *string {
	var d struct {
		Reason string `json:"reason"`
		Error  string `json:"error"`
	}
	if json.Unmarshal([]byte(detail), &d) != nil {
		return nil
	}
	switch {
	case d.Reason != "":
		return new(d.Reason)
	case d.Error != "":
		return new(d.Error)
	}
	return nil
}
