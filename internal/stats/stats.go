// Package stats 计算统计分析（M10）：用量 / 工具 / 回合的分组聚合（时区在查询时处理）、orb 状态时长与时间线、
// 线程产出与提交采样、保留期清理、预算与告警，以及 orb 资源采样的降采样。
package stats

import (
	"context"
	"fmt"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// RawStepSeconds 是 orbd 的原始采样周期。
const RawStepSeconds = 15

// DefaultWindow 是 from 缺省时向前的时长。
const DefaultWindow = 7 * 24 * time.Hour

// 错误码（docs/protocol.md「M10：统计分析」）。
const (
	CodeInvalidRequest  = "invalid_request"
	CodeInvalidTimezone = "invalid_timezone"
	CodeRangeTooLarge   = "range_too_large"
	CodeInvalidBudget   = "invalid_budget"
	CodeNotFound        = "not_found"
)

// Error 是调用方可见的统计错误（REST 映射为 400 / 404 的 {code, message}）。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrRangeTooLarge 表示时间桶超过 MaxBuckets。
var ErrRangeTooLarge = &Error{Code: CodeRangeTooLarge, Message: fmt.Sprintf("时间桶超过 %d 个", MaxBuckets)}

// Filter 是用量 / 工具 / 产出的可选筛选；nil = 不限。Project / Space 的字面量 "none"（= 无项目 / 不在 Space）
// 由各查询函数换成空串。
type Filter struct {
	Project, Space, Thread, Model *string
}

// noneToEmpty 把筛选值 "none" 换成空串（SQL 侧空串 = 无项目 / 无 Space）。
func noneToEmpty(p *string) *string {
	if p != nil && *p == "none" {
		return new("")
	}
	return p
}

func (f Filter) norm() Filter {
	f.Project, f.Space = noneToEmpty(f.Project), noneToEmpty(f.Space)
	return f
}

// LoadLocation 解析 IANA 时区名（空 = UTC）；"Local" 与不可加载的名字返回 invalid_timezone。
func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}
	if name == "Local" {
		return nil, errorf(CodeInvalidTimezone, "时区 %q 不是 IANA 时区名", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, errorf(CodeInvalidTimezone, "无法加载时区 %q", name)
	}
	return loc, nil
}

// checkLimit 返回生效的 limit（0 = DefaultLimit）；越界返回 invalid_request。
func checkLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultLimit, nil
	}
	if limit < 1 || limit > MaxLimit {
		return 0, errorf(CodeInvalidRequest, "limit 必须在 1–%d 之间", MaxLimit)
	}
	return limit, nil
}

// 非时间分组的折叠参数。
const (
	// OtherKey 是被折叠的分组合并成的一项的键。
	OtherKey = "__other__"
	// DefaultLimit / MaxLimit 是非时间分组保留的组数（缺省 / 上限）。
	DefaultLimit = 50
	MaxLimit     = 500
)

// Window 按「from 缺省 = to − 7 天，to 缺省 = now」补全时间窗。
func Window(from, to *int64, now time.Time) (int64, int64) {
	// 区间右开：默认终点取 now+1ms，当前毫秒内写入的记录也计入
	t := now.UnixMilli() + 1
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
