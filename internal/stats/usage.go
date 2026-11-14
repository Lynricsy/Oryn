package stats

import (
	"cmp"
	"context"
	"math"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 用量的分组维度（apigen.UsageGroupBy 中非时间粒度的取值）。
const (
	GroupTotal   = "total"
	GroupModel   = "model"
	GroupThread  = "thread"
	GroupProject = "project"
	GroupSpace   = "space"
	GroupPurpose = "purpose"
	GroupType    = "type"
	GroupOrb     = "orb"
)

// UsageQuery 是一次用量查询。GroupBy 必填；Series 为空或时间粒度（GroupBy 本身是时间粒度时不得再给）；
// Loc 为 nil 时取 UTC；Limit 为 0 时取 DefaultLimit。
type UsageQuery struct {
	GroupBy, Series string
	From, To        int64
	Loc             *time.Location
	Filter          Filter
	Limit           int
}

// uacc 是一个分组的累加量（和 + 计数，折叠精确）。
type uacc struct {
	requests, requestErrors, in, out, cacheR, cacheW int64
	cost, subCost                                    float64
	ttftSum, ttftN, timedOut, durMs                  int64
	toolCalls, toolErrors, turns                     int64
}

func (a *uacc) add(b *uacc) {
	a.requests = satAdd(a.requests, b.requests)
	a.requestErrors = satAdd(a.requestErrors, b.requestErrors)
	a.in = satAdd(a.in, b.in)
	a.out = satAdd(a.out, b.out)
	a.cacheR = satAdd(a.cacheR, b.cacheR)
	a.cacheW = satAdd(a.cacheW, b.cacheW)
	a.cost += b.cost
	a.subCost += b.subCost
	a.ttftSum = satAdd(a.ttftSum, b.ttftSum)
	a.ttftN = satAdd(a.ttftN, b.ttftN)
	a.timedOut = satAdd(a.timedOut, b.timedOut)
	a.durMs = satAdd(a.durMs, b.durMs)
	a.toolCalls = satAdd(a.toolCalls, b.toolCalls)
	a.toolErrors = satAdd(a.toolErrors, b.toolErrors)
	a.turns = satAdd(a.turns, b.turns)
}

// satAdd 是饱和加法：SQL 里 CAST(TOTAL(…) AS INTEGER) 会把过大的和饱和到 MaxInt64，Go 侧再相加不能回绕成负数。
func satAdd(a, b int64) int64 {
	c := a + b
	if (c > a) != (b > 0) {
		if b > 0 {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return c
}

// finite 把 NaN / ±Inf 换成 0：encoding/json 编码不了非有限值（会让整个响应失败）。
func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func (a *uacc) addRow(r db.StatsUsageAggRow) {
	a.add(&uacc{
		requests: r.Requests, requestErrors: r.RequestErrors, in: r.InputTokens, out: r.OutputTokens,
		cacheR: r.CacheReadTokens, cacheW: r.CacheWriteTokens, cost: r.CostUsd, subCost: r.SubagentCostUsd,
		ttftSum: r.TtftSumMs, ttftN: r.TtftN, timedOut: r.TimedOutputTokens, durMs: r.DurationMs,
	})
}

func (a *uacc) zero() bool {
	return a.requests == 0 && a.toolCalls == 0 && a.turns == 0 && a.cost == 0 &&
		a.in == 0 && a.out == 0 && a.cacheR == 0 && a.cacheW == 0
}

// stat 输出一个分组；浮点字段都经 finite，保证响应可编码。
func (a *uacc) stat(key string) apigen.UsageStat {
	s := apigen.UsageStat{
		Key: key, Requests: a.requests, RequestErrors: a.requestErrors, InputTokens: a.in, OutputTokens: a.out,
		CacheReadTokens: a.cacheR, CacheWriteTokens: a.cacheW, CostUsd: finite(a.cost),
		ToolCalls: a.toolCalls, ToolErrors: a.toolErrors, Turns: a.turns,
	}
	sub := finite(a.subCost)
	s.SubagentCostUsd = &sub
	if a.ttftN > 0 {
		v := finite(float64(a.ttftSum) / float64(a.ttftN))
		s.AvgTtftMs = &v
	}
	if a.durMs > 0 {
		v := finite(float64(a.timedOut) / (float64(a.durMs) / 1000))
		s.TokensPerSecond = &v
	}
	return s
}

// cell 是「网格下标 × 分组键」；不分时间桶时下标恒为 0。
type cell struct {
	slot int
	key  string
}

func isUsageGroup(g string) bool {
	switch g {
	case GroupTotal, GroupModel, GroupThread, GroupProject, GroupSpace, GroupPurpose:
		return true
	}
	return IsGrain(g)
}

// Usage 聚合 [From, To) 的用量、工具调用与回合。时间粒度的 groups / series 稠密；非时间分组按成本降序
// （并列按键）取前 Limit 个，其余折叠成 OtherKey；total 在折叠前求和。Model 筛选生效或按 model / purpose 分组时
// 工具与回合为 0（它们不归属到模型）。
func Usage(ctx context.Context, q *db.Queries, uq UsageQuery) (apigen.UsageStats, error) {
	if !isUsageGroup(uq.GroupBy) {
		return apigen.UsageStats{}, errorf(CodeInvalidRequest, "groupBy %q 无效", uq.GroupBy)
	}
	if uq.Series != "" && (!IsGrain(uq.Series) || IsGrain(uq.GroupBy)) {
		return apigen.UsageStats{}, errorf(CodeInvalidRequest, "series 须是时间粒度，且 groupBy 为时间粒度时不得再给")
	}
	if uq.From >= uq.To {
		return apigen.UsageStats{}, errorf(CodeInvalidRequest, "from 必须早于 to")
	}
	limit, err := checkLimit(uq.Limit)
	if err != nil {
		return apigen.UsageStats{}, err
	}
	loc := uq.Loc
	if loc == nil {
		loc = time.UTC
	}
	grain, dim := uq.Series, uq.GroupBy
	if IsGrain(uq.GroupBy) {
		grain, dim = uq.GroupBy, ""
	}
	tp, err := newTimePlan(loc, uq.From, uq.To, grain)
	if err != nil {
		return apigen.UsageStats{}, err
	}
	f := uq.Filter.norm()
	withTools := f.Model == nil && dim != GroupModel && dim != GroupPurpose
	sd := sqlDim(dim)

	cells := map[cell]*uacc{}
	get := func(c cell) *uacc {
		a := cells[c]
		if a == nil {
			a = &uacc{}
			cells[c] = a
		}
		return a
	}
	// 每个片段拆成头 / 中 / 尾（rollup.go）；各段的本地序号都按片段 seg 映射到同一网格
	for _, seg := range tp.segs {
		for _, pt := range tp.parts(seg) {
			rows, err := usageAgg(ctx, q, db.StatsUsageAggParams{
				ByBucket: tp.bucketed, OffsetMs: tp.sqlOffset(seg), BucketMs: tp.bucketMs, Dim: sd,
				Thread: f.Thread, Project: f.Project, Space: f.Space, Model: f.Model,
			}, pt)
			if err != nil {
				return apigen.UsageStats{}, err
			}
			for _, r := range rows {
				if i := tp.slot(seg, r.Bucket); i >= 0 {
					get(cell{i, usageKey(dim, r.Key)}).addRow(r)
				}
			}
			if !withTools {
				continue
			}
			tools, err := toolAgg(ctx, q, db.StatsToolAggParams{
				ByBucket: tp.bucketed, OffsetMs: tp.sqlOffset(seg), BucketMs: tp.bucketMs, Dim: sd,
				Thread: f.Thread, Project: f.Project, Space: f.Space,
			}, pt)
			if err != nil {
				return apigen.UsageStats{}, err
			}
			for _, r := range tools {
				if i := tp.slot(seg, r.Bucket); i >= 0 {
					a := get(cell{i, usageKey(dim, r.Key)})
					a.toolCalls = satAdd(a.toolCalls, r.Calls)
					a.toolErrors = satAdd(a.toolErrors, r.Errors)
				}
			}
			turns, err := turnAgg(ctx, q, db.StatsTurnAggParams{
				ByBucket: tp.bucketed, OffsetMs: tp.sqlOffset(seg), BucketMs: tp.bucketMs, Dim: sd,
				Thread: f.Thread, Project: f.Project, Space: f.Space,
			}, pt)
			if err != nil {
				return apigen.UsageStats{}, err
			}
			for _, r := range turns {
				if i := tp.slot(seg, r.Bucket); i >= 0 {
					a := get(cell{i, usageKey(dim, r.Key)})
					a.turns = satAdd(a.turns, r.Turns)
				}
			}
		}
	}

	out := apigen.UsageStats{
		GroupBy: apigen.UsageGroupBy(uq.GroupBy), From: uq.From, To: uq.To, Tz: loc.String(),
	}
	var total uacc
	for _, a := range cells {
		total.add(a)
	}
	out.Total = total.stat(GroupTotal)

	if dim == "" {
		// 时间粒度：groups 即稠密的桶
		out.Groups = make([]apigen.UsageStat, 0, len(tp.grid))
		for i, b := range tp.grid {
			a := cells[cell{i, ""}]
			if a == nil {
				a = &uacc{}
			}
			s := a.stat(b.Key)
			s.Start = new(b.Start)
			out.Groups = append(out.Groups, s)
		}
		return out, nil
	}

	// 维度：按键合计、排序、折叠
	byKey := map[string]*uacc{}
	for c, a := range cells {
		if byKey[c.key] == nil {
			byKey[c.key] = &uacc{}
		}
		byKey[c.key].add(a)
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := cmp.Compare(byKey[b].cost, byKey[a].cost); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	kept, folded := keys, []string(nil)
	if dim != GroupTotal && len(keys) > limit {
		kept, folded = keys[:limit], keys[limit:]
	}
	labels, err := usageLabels(ctx, q, dim)
	if err != nil {
		return apigen.UsageStats{}, err
	}
	stat := func(a *uacc, key string) apigen.UsageStat {
		s := a.stat(key)
		if l := labels[key]; l != "" && key != OtherKey {
			s.Label = new(l)
		}
		return s
	}
	out.Groups = make([]apigen.UsageStat, 0, len(kept)+1)
	for _, k := range kept {
		out.Groups = append(out.Groups, stat(byKey[k], k))
	}
	if len(folded) > 0 {
		var other uacc
		for _, k := range folded {
			other.add(byKey[k])
		}
		out.Groups = append(out.Groups, stat(&other, OtherKey))
	}
	if dim == GroupTotal && len(out.Groups) == 0 {
		out.Groups = append(out.Groups, out.Total)
	}
	if !tp.bucketed {
		return out, nil
	}
	// series：稠密的桶，每点只列非零分组，键与顶层一致
	isFolded := make(map[string]bool, len(folded))
	for _, k := range folded {
		isFolded[k] = true
	}
	out.Series = make([]apigen.UsageSeriesPoint, 0, len(tp.grid))
	for i, b := range tp.grid {
		var pt, other uacc
		groups := []apigen.UsageStat{}
		for _, k := range kept {
			if a := cells[cell{i, k}]; a != nil {
				pt.add(a)
				if !a.zero() {
					groups = append(groups, stat(a, k))
				}
			}
		}
		for _, k := range folded {
			if a := cells[cell{i, k}]; a != nil {
				pt.add(a)
				other.add(a)
			}
		}
		if !other.zero() {
			groups = append(groups, stat(&other, OtherKey))
		}
		ts := pt.stat(GroupTotal)
		out.Series = append(out.Series, apigen.UsageSeriesPoint{Key: b.Key, Start: b.Start, Total: ts, Groups: groups})
	}
	return out, nil
}

// usageKey 把 SQL 按 sqlDim(dim) 返回的键还原成维度 dim 下的分组键：total 的唯一分组键是 "total"，其余原样
// （线程 / 项目 / Space / 模型 / 用途，时间粒度为空串）。
func usageKey(dim, key string) string {
	if dim == GroupTotal {
		return GroupTotal
	}
	return key
}

// usageLabels 返回维度 dim 的 键 → 显示名（线程标题、项目名、Space 名）；各用一次列表查询。
func usageLabels(ctx context.Context, q *db.Queries, dim string) (map[string]string, error) {
	switch dim {
	case GroupThread:
		return threadTitles(ctx, q)
	case GroupProject:
		return projectNames(ctx, q)
	case GroupSpace:
		return spaceNames(ctx, q)
	}
	return nil, nil
}

func threadTitles(ctx context.Context, q *db.Queries) (map[string]string, error) {
	rows, err := q.ListThreads(ctx, db.ListThreadsParams{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, t := range rows {
		out[t.ID] = t.Title
	}
	return out, nil
}

func projectNames(ctx context.Context, q *db.Queries) (map[string]string, error) {
	rows, err := q.ListProjects(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, p := range rows {
		out[p.ID] = p.Name
	}
	return out, nil
}

func spaceNames(ctx context.Context, q *db.Queries) (map[string]string, error) {
	rows, err := q.ListSpaces(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, s := range rows {
		out[s.ID] = s.Name
	}
	return out, nil
}
