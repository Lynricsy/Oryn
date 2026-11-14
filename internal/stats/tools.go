package stats

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ToolQuery 是一次工具调用查询（Filter.Model 不适用于工具，忽略）。
type ToolQuery struct {
	Series   string
	From, To int64
	Loc      *time.Location
	Filter   Filter
	Limit    int
}

// Tools 按工具聚合 [From, To) 的调用与错误：按调用数降序（并列按名）取前 Limit 个，其余折叠成 OtherKey；
// 给了 Series 时另附稠密的按桶合计。
func Tools(ctx context.Context, q *db.Queries, tq ToolQuery) (apigen.ToolStats, error) {
	if tq.Series != "" && !IsGrain(tq.Series) {
		return apigen.ToolStats{}, errorf(CodeInvalidRequest, "series %q 无效", tq.Series)
	}
	if tq.From >= tq.To {
		return apigen.ToolStats{}, errorf(CodeInvalidRequest, "from 必须早于 to")
	}
	limit, err := checkLimit(tq.Limit)
	if err != nil {
		return apigen.ToolStats{}, err
	}
	loc := tq.Loc
	if loc == nil {
		loc = time.UTC
	}
	f := tq.Filter.norm()
	tp, err := newTimePlan(loc, tq.From, tq.To, tq.Series)
	if err != nil {
		return apigen.ToolStats{}, err
	}
	out := apigen.ToolStats{From: tq.From, To: tq.To, Tz: loc.String(), Tools: []apigen.ToolStat{}}

	rows, err := q.StatsToolAgg(ctx, db.StatsToolAggParams{
		ByTool: true, BucketMs: 1, FromTs: tq.From, ToTs: tq.To, Thread: f.Thread, Project: f.Project, Space: f.Space,
	})
	if err != nil {
		return apigen.ToolStats{}, err
	}
	slices.SortFunc(rows, func(a, b db.StatsToolAggRow) int {
		if c := cmp.Compare(b.Calls, a.Calls); c != 0 {
			return c
		}
		return cmp.Compare(a.Tool, b.Tool)
	})
	other := apigen.ToolStat{Tool: OtherKey}
	for i, r := range rows {
		out.Total.Calls += r.Calls
		out.Total.Errors += r.Errors
		if i < limit {
			out.Tools = append(out.Tools, apigen.ToolStat{Tool: r.Tool, Calls: r.Calls, Errors: r.Errors})
		} else {
			other.Calls += r.Calls
			other.Errors += r.Errors
		}
	}
	if len(rows) > limit {
		out.Tools = append(out.Tools, other)
	}
	if !tp.bucketed {
		return out, nil
	}
	pts := make([]apigen.ToolSeriesPoint, len(tp.grid))
	for i, b := range tp.grid {
		pts[i] = apigen.ToolSeriesPoint{Key: b.Key, Start: b.Start}
	}
	for _, seg := range tp.segs {
		rows, err := q.StatsToolAgg(ctx, db.StatsToolAggParams{
			ByBucket: true, OffsetMs: tp.sqlOffset(seg), BucketMs: tp.bucketMs,
			FromTs: seg.From, ToTs: seg.To, Thread: f.Thread, Project: f.Project, Space: f.Space,
		})
		if err != nil {
			return apigen.ToolStats{}, err
		}
		for _, r := range rows {
			if i := tp.slot(seg, r.Bucket); i >= 0 {
				pts[i].Calls += r.Calls
				pts[i].Errors += r.Errors
			}
		}
	}
	out.Series = pts
	return out, nil
}
