package stats

import (
	"context"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// quarterMs 是汇总表的粒度（q = ts / quarterMs）。所有真实时区偏移与夏令时切换都落在 15 分钟边界上，
// 所以本地小时 / 日 / 周桶都由整数个 q 拼成。
const quarterMs = 15 * 60 * 1000

// useRollup 为假时全部走原始行（测试据此对比新旧两条路径）。
var useRollup = true

// part 是一个片段内的一段 [From, To)：raw 读原始行；否则读 15 分钟汇总（两端都对齐 quarterMs）。
type part struct {
	From, To int64
	raw      bool
}

// parts 把片段 seg 拆成头 [From, ceil15(From))、中 [ceil15(From), floor15(To))、尾 [floor15(To), To)：头尾读原始行
// （各不足 15 分钟，走 ts 索引），中段读汇总。中段为空、或桶边界不在 15 分钟网格上（不分桶时不受限）时整段读原始行。
func (p timePlan) parts(seg segment) []part {
	lo := ceilQuarter(max(seg.From, 0))
	hi := seg.To - seg.To%quarterMs
	aligned := !p.bucketed || (p.sqlOffset(seg)%quarterMs == 0 && p.bucketMs%quarterMs == 0)
	if !useRollup || !aligned || lo >= hi {
		return []part{{From: seg.From, To: seg.To, raw: true}}
	}
	out := make([]part, 0, 3)
	if seg.From < lo {
		out = append(out, part{From: seg.From, To: lo, raw: true})
	}
	out = append(out, part{From: lo, To: hi})
	if hi < seg.To {
		out = append(out, part{From: hi, To: seg.To, raw: true})
	}
	return out
}

func ceilQuarter(t int64) int64 {
	if r := t % quarterMs; r != 0 {
		return t + quarterMs - r
	}
	return t
}

// SQL 的分组键编号（queries/stats.sql 里 Stats*Agg / Stats*AggQ 的 dim 参数）：每条查询只按「桶 + 一个键」分组。
const (
	dimNone    int64 = 0 // 不分组（键为空串）
	dimThread  int64 = 1 // 线程 id（已删线程为空串）
	dimProject int64 = 2 // 项目 id（活线程取当前值，已删线程取快照；无为空串）
	dimSpace   int64 = 3 // Space id（同上）
	dimModel   int64 = 4 // <provider>/<model>（仅用量）
	dimPurpose int64 = 5 // 用途（仅用量）
	dimTool    int64 = 6 // 工具名（仅工具调用）
)

// sqlDim 把用量 / 产出的分组维度换成 SQL 的 dim；时间粒度与 total 不分键。
func sqlDim(group string) int64 {
	switch group {
	case GroupThread:
		return dimThread
	case GroupProject:
		return dimProject
	case GroupSpace:
		return dimSpace
	case GroupModel:
		return dimModel
	case GroupPurpose:
		return dimPurpose
	}
	return dimNone
}

// usageAgg 对一段求用量聚合：p 的 FromTs / ToTs 由 pt 覆盖。
func usageAgg(ctx context.Context, q *db.Queries, p db.StatsUsageAggParams, pt part) ([]db.StatsUsageAggRow, error) {
	if pt.raw {
		p.FromTs, p.ToTs = pt.From, pt.To
		return q.StatsUsageAgg(ctx, p)
	}
	rows, err := q.StatsUsageAggQ(ctx, db.StatsUsageAggQParams{
		ByBucket: p.ByBucket, OffsetMs: p.OffsetMs, BucketMs: p.BucketMs, Dim: p.Dim,
		FromQ: pt.From / quarterMs, ToQ: pt.To / quarterMs, Thread: p.Thread, Project: p.Project, Space: p.Space, Model: p.Model,
	})
	if err != nil {
		return nil, err
	}
	out := make([]db.StatsUsageAggRow, len(rows))
	for i, r := range rows {
		out[i] = db.StatsUsageAggRow(r)
	}
	return out, nil
}

// toolAgg 对一段求工具调用聚合：p 的 FromTs / ToTs 由 pt 覆盖。
func toolAgg(ctx context.Context, q *db.Queries, p db.StatsToolAggParams, pt part) ([]db.StatsToolAggRow, error) {
	if pt.raw {
		p.FromTs, p.ToTs = pt.From, pt.To
		return q.StatsToolAgg(ctx, p)
	}
	rows, err := q.StatsToolAggQ(ctx, db.StatsToolAggQParams{
		ByBucket: p.ByBucket, OffsetMs: p.OffsetMs, BucketMs: p.BucketMs, Dim: p.Dim,
		FromQ: pt.From / quarterMs, ToQ: pt.To / quarterMs, Thread: p.Thread, Project: p.Project, Space: p.Space,
	})
	if err != nil {
		return nil, err
	}
	out := make([]db.StatsToolAggRow, len(rows))
	for i, r := range rows {
		out[i] = db.StatsToolAggRow(r)
	}
	return out, nil
}

// turnAgg 对一段求回合聚合：p 的 FromTs / ToTs 由 pt 覆盖。
func turnAgg(ctx context.Context, q *db.Queries, p db.StatsTurnAggParams, pt part) ([]db.StatsTurnAggRow, error) {
	if pt.raw {
		p.FromTs, p.ToTs = pt.From, pt.To
		return q.StatsTurnAgg(ctx, p)
	}
	rows, err := q.StatsTurnAggQ(ctx, db.StatsTurnAggQParams{
		ByBucket: p.ByBucket, OffsetMs: p.OffsetMs, BucketMs: p.BucketMs, Dim: p.Dim,
		FromQ: pt.From / quarterMs, ToQ: pt.To / quarterMs, Thread: p.Thread, Project: p.Project, Space: p.Space,
	})
	if err != nil {
		return nil, err
	}
	out := make([]db.StatsTurnAggRow, len(rows))
	for i, r := range rows {
		out[i] = db.StatsTurnAggRow(r)
	}
	return out, nil
}
