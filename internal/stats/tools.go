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

	// 合计：整个窗口不分桶（拆成头 / 中 / 尾后按工具合并）
	whole, err := newTimePlan(loc, tq.From, tq.To, "")
	if err != nil {
		return apigen.ToolStats{}, err
	}
	byTool := map[string]*db.StatsToolAggRow{}
	for _, pt := range whole.parts(whole.segs[0]) {
		part, err := toolAgg(ctx, q, db.StatsToolAggParams{
			BucketMs: 1, Dim: dimTool, Thread: f.Thread, Project: f.Project, Space: f.Space,
		}, pt)
		if err != nil {
			return apigen.ToolStats{}, err
		}
		for _, r := range part {
			if a := byTool[r.Key]; a != nil {
				a.Calls = satAdd(a.Calls, r.Calls)
				a.Errors = satAdd(a.Errors, r.Errors)
			} else {
				byTool[r.Key] = &r
			}
		}
	}
	rows := make([]db.StatsToolAggRow, 0, len(byTool))
	for _, r := range byTool {
		rows = append(rows, *r)
	}
	slices.SortFunc(rows, func(a, b db.StatsToolAggRow) int {
		if c := cmp.Compare(b.Calls, a.Calls); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	other := apigen.ToolStat{Tool: OtherKey}
	for i, r := range rows {
		out.Total.Calls = satAdd(out.Total.Calls, r.Calls)
		out.Total.Errors = satAdd(out.Total.Errors, r.Errors)
		if i < limit {
			out.Tools = append(out.Tools, apigen.ToolStat{Tool: r.Key, Calls: r.Calls, Errors: r.Errors})
		} else {
			other.Calls = satAdd(other.Calls, r.Calls)
			other.Errors = satAdd(other.Errors, r.Errors)
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
		for _, pt := range tp.parts(seg) {
			rows, err := toolAgg(ctx, q, db.StatsToolAggParams{
				ByBucket: true, OffsetMs: tp.sqlOffset(seg), BucketMs: tp.bucketMs,
				Thread: f.Thread, Project: f.Project, Space: f.Space,
			}, pt)
			if err != nil {
				return apigen.ToolStats{}, err
			}
			for _, r := range rows {
				if i := tp.slot(seg, r.Bucket); i >= 0 {
					pts[i].Calls = satAdd(pts[i].Calls, r.Calls)
					pts[i].Errors = satAdd(pts[i].Errors, r.Errors)
				}
			}
		}
	}
	out.Series = pts
	return out, nil
}
