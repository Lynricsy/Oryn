package stats

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// OutputQuery 是一次产出查询。GroupBy 为空时取 day；Project / Space 的 "none" = 无项目 / 不在 Space。
type OutputQuery struct {
	GroupBy        string
	From, To       int64
	Loc            *time.Location
	Project, Space *string
}

func addOutput(a, b *apigen.OutputStat) {
	a.ThreadsCreated += b.ThreadsCreated
	a.ThreadsShipped += b.ThreadsShipped
	a.ThreadsArchived += b.ThreadsArchived
	a.ShippedCostUsd += b.ShippedCostUsd
	a.Commits += b.Commits
	a.Turns += b.Turns
}

// Output 统计 [From, To) 的线程产出：新建 / 已交付 / 已归档（不含已交付）只数工作线程，分别按 created_at /
// shipped_at / archived_at 落桶；turns 与 commits 不按线程种类过滤，commits 按 sha 去重（total.commits 另做一次
// 不分组的计数）；shippedCostUsd 是窗口内交付的线程的全生命周期成本，按 shipped_at 落桶。
func Output(ctx context.Context, q *db.Queries, oq OutputQuery) (apigen.OutputStats, error) {
	groupBy := oq.GroupBy
	if groupBy == "" {
		groupBy = GrainDay
	}
	switch groupBy {
	case GrainDay, GrainWeek, GrainMonth, GroupProject, GroupSpace:
	default:
		return apigen.OutputStats{}, errorf(CodeInvalidRequest, "groupBy %q 无效", groupBy)
	}
	if oq.From >= oq.To {
		return apigen.OutputStats{}, errorf(CodeInvalidRequest, "from 必须早于 to")
	}
	loc := oq.Loc
	if loc == nil {
		loc = time.UTC
	}
	grain, dim := "", groupBy
	if IsGrain(groupBy) {
		grain, dim = groupBy, ""
	}
	tp, err := newTimePlan(loc, oq.From, oq.To, grain)
	if err != nil {
		return apigen.OutputStats{}, err
	}
	project, space := noneToEmpty(oq.Project), noneToEmpty(oq.Space)

	cells := map[cell]*apigen.OutputStat{}
	get := func(c cell) *apigen.OutputStat {
		s := cells[c]
		if s == nil {
			s = &apigen.OutputStat{}
			cells[c] = s
		}
		return s
	}
	keyOf := func(projectID, spaceID *string) string {
		switch dim {
		case GroupProject:
			return deref(projectID)
		case GroupSpace:
			return deref(spaceID)
		}
		return ""
	}
	// at 把时刻 ts 落到网格下标（不分时间桶时恒为 0）；窗口外返回 -1
	at := func(ts int64) int {
		if ts < oq.From || ts >= oq.To {
			return -1
		}
		if !tp.bucketed {
			return 0
		}
		return gridSlot(tp.grid, ts)
	}
	times, err := q.ListThreadTimesForStats(ctx, db.ListThreadTimesForStatsParams{FromTs: oq.From, ToTs: oq.To, Project: project, Space: space})
	if err != nil {
		return apigen.OutputStats{}, err
	}
	for _, t := range times {
		k := keyOf(t.ProjectID, t.SpaceID)
		if i := at(t.CreatedAt); i >= 0 {
			get(cell{i, k}).ThreadsCreated++
		}
		if t.ShippedAt != nil {
			if i := at(*t.ShippedAt); i >= 0 {
				get(cell{i, k}).ThreadsShipped++
			}
		} else if t.ArchivedAt != nil {
			if i := at(*t.ArchivedAt); i >= 0 {
				get(cell{i, k}).ThreadsArchived++
			}
		}
	}
	shipped, err := q.ListShippedThreadCosts(ctx, db.ListShippedThreadCostsParams{FromTs: oq.From, ToTs: oq.To, Project: project, Space: space})
	if err != nil {
		return apigen.OutputStats{}, err
	}
	for _, t := range shipped {
		if i := at(t.ShippedAt); i >= 0 {
			get(cell{i, keyOf(t.ProjectID, t.SpaceID)}).ShippedCostUsd += t.CostUsd
		}
	}
	for _, seg := range tp.segs {
		for _, pt := range tp.parts(seg) {
			turns, err := turnAgg(ctx, q, db.StatsTurnAggParams{
				ByBucket: tp.bucketed, OffsetMs: tp.sqlOffset(seg), BucketMs: tp.bucketMs, Dim: sqlDim(dim),
				Project: project, Space: space,
			}, pt)
			if err != nil {
				return apigen.OutputStats{}, err
			}
			for _, r := range turns {
				if i := tp.slot(seg, r.Bucket); i >= 0 {
					get(cell{i, r.Key}).Turns += r.Turns
				}
			}
		}
		commits, err := q.StatsCommitAgg(ctx, db.StatsCommitAggParams{
			ByBucket: tp.bucketed, OffsetMs: tp.sqlOffset(seg), BucketMs: tp.bucketMs,
			ByProject: dim == GroupProject, BySpace: dim == GroupSpace,
			FromTs: seg.From, ToTs: seg.To, Project: project, Space: space,
		})
		if err != nil {
			return apigen.OutputStats{}, err
		}
		for _, r := range commits {
			if i := tp.slot(seg, r.Bucket); i >= 0 {
				get(cell{i, keyOf(&r.ProjectID, &r.SpaceID)}).Commits += r.Commits
			}
		}
	}

	out := apigen.OutputStats{GroupBy: apigen.OutputGroupBy(groupBy), From: oq.From, To: oq.To, Tz: loc.String(), Total: apigen.OutputStat{Key: GroupTotal}}
	for _, s := range cells {
		addOutput(&out.Total, s)
	}
	// 同一个 sha 可能出现在多个分组 / 桶（fork 出的线程共享历史）：总数不分组单独去重计一次
	total, err := q.StatsCommitAgg(ctx, db.StatsCommitAggParams{BucketMs: 1, FromTs: oq.From, ToTs: oq.To, Project: project, Space: space})
	if err != nil {
		return apigen.OutputStats{}, err
	}
	out.Total.Commits = 0
	for _, r := range total {
		out.Total.Commits += r.Commits
	}
	if dim == "" {
		out.Groups = make([]apigen.OutputStat, 0, len(tp.grid))
		for i, b := range tp.grid {
			s := apigen.OutputStat{}
			if c := cells[cell{i, ""}]; c != nil {
				s = *c
			}
			s.Key, s.Start = b.Key, new(b.Start)
			out.Groups = append(out.Groups, s)
		}
		return out, nil
	}
	labels, err := usageLabels(ctx, q, dim)
	if err != nil {
		return apigen.OutputStats{}, err
	}
	out.Groups = make([]apigen.OutputStat, 0, len(cells))
	for c, s := range cells {
		g := *s
		g.Key = c.key
		if l := labels[c.key]; l != "" {
			g.Label = new(l)
		}
		out.Groups = append(out.Groups, g)
	}
	slices.SortFunc(out.Groups, func(a, b apigen.OutputStat) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
