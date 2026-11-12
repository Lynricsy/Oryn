// Package stats 计算统计接口（计划 H6.2）：orb 资源采样降采样、模型用量分组聚合、
// 由 orb_events 推导的各类型运行 / 冻结 / 停机时长与次数。
package stats

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// RawStepSeconds 是 orbd 的原始采样周期。
const RawStepSeconds = 15

// DefaultWindow 是 from 缺省时向前的时长。
const DefaultWindow = 7 * 24 * time.Hour

// Window 按「from 缺省 = to − 7 天，to 缺省 = now」补全时间窗。
func Window(from, to *int64, now time.Time) (int64, int64) {
	t := now.UnixMilli()
	if to != nil {
		t = *to
	}
	f := t - DefaultWindow.Milliseconds()
	if from != nil {
		f = *from
	}
	return f, t
}

// Metrics 返回 orb 在 [from, to) 的采样；step（秒）> 0 时按 step 对齐分桶，
// CPU / 内存 / 磁盘取桶内均值，网络字节取桶内最后一个值。
func Metrics(ctx context.Context, q *db.Queries, orbID string, from, to int64, step int) (apigen.OrbMetrics, error) {
	rows, err := q.ListOrbSamples(ctx, db.ListOrbSamplesParams{OrbID: orbID, FromTs: from, ToTs: to})
	if err != nil {
		return apigen.OrbMetrics{}, err
	}
	out := apigen.OrbMetrics{OrbID: orbID, Samples: make([]apigen.OrbMetricSample, 0, len(rows)), Step: RawStepSeconds}
	if step <= 0 {
		for _, r := range rows {
			out.Samples = append(out.Samples, sample(r))
		}
		return out, nil
	}
	out.Step = step
	stepMs := int64(step) * 1000
	type acc struct {
		n                   int
		cpu, mem, lim, disk float64
		rx, tx              int64
		bucket              int64
	}
	var cur *acc
	flush := func() {
		if cur == nil {
			return
		}
		n := float64(cur.n)
		out.Samples = append(out.Samples, apigen.OrbMetricSample{
			TS: cur.bucket, CPUPct: float32(cur.cpu / n), MemUsedMB: float32(cur.mem / n), MemLimitMB: float32(cur.lim / n),
			DiskUsedMB: float32(cur.disk / n), NetRxBytes: cur.rx, NetTxBytes: cur.tx,
		})
	}
	for _, r := range rows {
		b := r.Ts - mod(r.Ts, stepMs)
		if cur == nil || cur.bucket != b {
			flush()
			cur = &acc{bucket: b}
		}
		cur.n++
		cur.cpu += r.CpuPct
		cur.mem += float64(r.MemUsedMb)
		cur.lim += float64(r.MemLimitMb)
		cur.disk += float64(r.DiskUsedMb)
		cur.rx, cur.tx = r.NetRxBytes, r.NetTxBytes
	}
	flush()
	return out, nil
}

func mod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

func sample(r db.OrbSample) apigen.OrbMetricSample {
	return apigen.OrbMetricSample{
		TS: r.Ts, CPUPct: float32(r.CpuPct), MemUsedMB: float32(r.MemUsedMb), MemLimitMB: float32(r.MemLimitMb),
		DiskUsedMB: float32(r.DiskUsedMb), NetRxBytes: r.NetRxBytes, NetTxBytes: r.NetTxBytes,
	}
}

// usageAgg 是各分组查询的公共列。
type usageAgg struct {
	key, label                                         string
	requests, in, out, cacheR, cacheW, timedOut, durMs int64
	cost, subCost, avgTtft                             float64
}

func (a usageAgg) stat() apigen.UsageStat {
	s := apigen.UsageStat{
		Key: a.key, Requests: a.requests, InputTokens: a.in, OutputTokens: a.out,
		CacheReadTokens: a.cacheR, CacheWriteTokens: a.cacheW, CostUsd: float32(a.cost),
	}
	if a.label != "" {
		l := a.label
		s.Label = &l
	}
	sub := float32(a.subCost)
	s.SubagentCostUsd = &sub
	if a.avgTtft >= 0 {
		v := float32(a.avgTtft)
		s.AvgTtftMs = &v
	}
	if a.durMs > 0 {
		v := float32(float64(a.timedOut) / (float64(a.durMs) / 1000))
		s.TokensPerSecond = &v
	}
	return s
}

// DayKey 把 UTC 日序号格式化为 YYYY-MM-DD。
func DayKey(day int64) string { return time.UnixMilli(day * 86400000).UTC().Format("2006-01-02") }

// Usage 按 groupBy（day | model | thread | project）聚合 [from, to) 的用量与工具调用。
func Usage(ctx context.Context, q *db.Queries, groupBy string, from, to int64) (apigen.UsageStats, error) {
	var aggs []usageAgg
	tools := map[string][2]int64{}
	switch groupBy {
	case "day":
		rows, err := q.StatsUsageByDay(ctx, db.StatsUsageByDayParams{FromTs: from, ToTs: to})
		if err != nil {
			return apigen.UsageStats{}, err
		}
		for _, r := range rows {
			aggs = append(aggs, usageAgg{DayKey(r.Day), "", r.Requests, r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.TimedOutputTokens, r.DurationMs, r.CostUsd, r.SubagentCostUsd, r.AvgTtftMs})
		}
		tc, err := q.StatsToolCallsByDay(ctx, db.StatsToolCallsByDayParams{FromTs: from, ToTs: to})
		if err != nil {
			return apigen.UsageStats{}, err
		}
		for _, r := range tc {
			tools[DayKey(r.Day)] = [2]int64{r.Calls, r.Errors}
		}
	case "model":
		rows, err := q.StatsUsageByModel(ctx, db.StatsUsageByModelParams{FromTs: from, ToTs: to})
		if err != nil {
			return apigen.UsageStats{}, err
		}
		for _, r := range rows {
			aggs = append(aggs, usageAgg{r.Provider + "/" + r.Model, "", r.Requests, r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.TimedOutputTokens, r.DurationMs, r.CostUsd, r.SubagentCostUsd, r.AvgTtftMs})
		}
	case "thread":
		rows, err := q.StatsUsageByThread(ctx, db.StatsUsageByThreadParams{FromTs: from, ToTs: to})
		if err != nil {
			return apigen.UsageStats{}, err
		}
		for _, r := range rows {
			aggs = append(aggs, usageAgg{r.ThreadID, r.Title, r.Requests, r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.TimedOutputTokens, r.DurationMs, r.CostUsd, r.SubagentCostUsd, r.AvgTtftMs})
		}
		tc, err := q.StatsToolCallsByThread(ctx, db.StatsToolCallsByThreadParams{FromTs: from, ToTs: to})
		if err != nil {
			return apigen.UsageStats{}, err
		}
		for _, r := range tc {
			tools[r.ThreadID] = [2]int64{r.Calls, r.Errors}
		}
	case "project":
		rows, err := q.StatsUsageByProject(ctx, db.StatsUsageByProjectParams{FromTs: from, ToTs: to})
		if err != nil {
			return apigen.UsageStats{}, err
		}
		for _, r := range rows {
			aggs = append(aggs, usageAgg{r.ProjectID, r.Name, r.Requests, r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.TimedOutputTokens, r.DurationMs, r.CostUsd, r.SubagentCostUsd, r.AvgTtftMs})
		}
		tc, err := q.StatsToolCallsByProject(ctx, db.StatsToolCallsByProjectParams{FromTs: from, ToTs: to})
		if err != nil {
			return apigen.UsageStats{}, err
		}
		for _, r := range tc {
			tools[r.ProjectID] = [2]int64{r.Calls, r.Errors}
		}
	default:
		return apigen.UsageStats{}, fmt.Errorf("groupBy %q 无效", groupBy)
	}
	out := apigen.UsageStats{From: from, To: to, GroupBy: apigen.UsageStatsGroupBy(groupBy), Groups: make([]apigen.UsageStat, 0, len(aggs))}
	seen := map[string]bool{}
	for _, a := range aggs {
		s := a.stat()
		if t, ok := tools[a.key]; ok {
			s.ToolCalls, s.ToolErrors = t[0], t[1]
		}
		seen[a.key] = true
		out.Groups = append(out.Groups, s)
	}
	// 只有工具调用、没有模型请求的分组。
	var extra []string
	for k := range tools {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		out.Groups = append(out.Groups, apigen.UsageStat{Key: k, ToolCalls: tools[k][0], ToolErrors: tools[k][1]})
	}
	if groupBy == "day" {
		sort.SliceStable(out.Groups, func(i, k int) bool { return out.Groups[i].Key < out.Groups[k].Key })
	}
	return out, nil
}

// runningStates 是容器在运行（计入 runningMs）的状态。
var runningStates = map[string]bool{
	"connecting": true, "setting_up": true, "resuming": true, "ready": true, "freezing": true, "stopping": true,
}

// Orbs 由 orb_events 计算 [from, to) 内各类型 orb 的状态时长与次数。
func Orbs(ctx context.Context, q *db.Queries, from, to int64) (apigen.OrbStats, error) {
	rows, err := q.ListOrbEventsForStats(ctx, db.ListOrbEventsForStatsParams{FromTs: &from, ToTs: to})
	if err != nil {
		return apigen.OrbStats{}, err
	}
	byType := map[string]*apigen.OrbTypeStat{}
	get := func(t string) *apigen.OrbTypeStat {
		s := byType[t]
		if s == nil {
			s = &apigen.OrbTypeStat{Type: t}
			byType[t] = s
		}
		return s
	}
	// 行按 orb_id, ts 排序：逐 orb 计算状态区间。
	for i := 0; i < len(rows); {
		k := i
		for k < len(rows) && rows[k].OrbID == rows[i].OrbID {
			k++
		}
		s := get(rows[i].Type)
		s.Orbs++
		for e := i; e < k; e++ {
			r := rows[e]
			start, end := r.Ts, to
			if e+1 < k {
				end = rows[e+1].Ts
			}
			start, end = max(start, from), min(end, to)
			if d := end - start; d > 0 {
				switch {
				case runningStates[r.ToState]:
					s.RunningMs += d
				case r.ToState == "frozen":
					s.FrozenMs += d
				case r.ToState == "stopped":
					s.StoppedMs += d
				}
			}
			if r.Ts < from {
				continue
			}
			switch r.ToState {
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
		i = k
	}
	out := apigen.OrbStats{From: from, To: to, Types: make([]apigen.OrbTypeStat, 0, len(byType))}
	for _, s := range byType {
		out.Types = append(out.Types, *s)
	}
	sort.Slice(out.Types, func(i, k int) bool { return out.Types[i].Type < out.Types[k].Type })
	return out, nil
}
