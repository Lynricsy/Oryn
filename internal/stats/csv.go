package stats

import (
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 用量明细导出的限额。
const (
	MaxExportRows  = 500000
	exportPageSize = 5000
)

// CSV 列（docs/protocol.md「M10：统计分析」）。
var (
	usageCSVHeader   = []string{"bucket", "key", "label", "requests", "request_errors", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "subagent_cost_usd", "avg_ttft_ms", "tokens_per_second", "tool_calls", "tool_errors", "turns"}
	toolsCSVHeader   = []string{"bucket", "tool", "calls", "errors"}
	orbsCSVHeader    = []string{"key", "label", "type", "cpus", "memory_mb", "orbs", "running_ms", "frozen_ms", "stopped_ms", "setup_ms", "spawns", "snapshot_spawns", "freezes", "stops", "wakes", "failures"}
	outputCSVHeader  = []string{"key", "label", "threads_created", "threads_shipped", "threads_archived", "shipped_cost_usd", "commits", "turns"}
	recordsCSVHeader = []string{"ts", "time", "thread_id", "project_id", "space_id", "provider", "model", "purpose", "subagent", "stop_reason", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "duration_ms", "ttft_ms"}
)

// ExportFileName 是导出附件的文件名：oryn-<kind>-<groupBy>-<from 的 UTC 日期>-<to 的>.<ext>。
func ExportFileName(kind, groupBy string, from, to int64, ext string) string {
	day := func(ms int64) string { return time.UnixMilli(ms).UTC().Format("20060102") }
	return "oryn-" + kind + "-" + groupBy + "-" + day(from) + "-" + day(to) + "." + ext
}

func i64(v int64) string     { return strconv.FormatInt(v, 10) }
func itoa(v int) string      { return strconv.Itoa(v) }
func f64(v float64) string   { return strconv.FormatFloat(v, 'f', -1, 64) }
func optF(v *float64) string { return optional(v, f64) }
func optI(v *int) string     { return optional(v, itoa) }
func optS(v *string) string  { return optional(v, func(s string) string { return s }) }

func optional[T any](v *T, f func(T) string) string {
	if v == nil {
		return ""
	}
	return f(*v)
}

func writeAll(w io.Writer, header []string, rows [][]string) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	return cw.Error()
}

func usageRow(bucket string, s apigen.UsageStat) []string {
	return []string{bucket, s.Key, optS(s.Label), i64(s.Requests), i64(s.RequestErrors), i64(s.InputTokens), i64(s.OutputTokens),
		i64(s.CacheReadTokens), i64(s.CacheWriteTokens), f64(s.CostUsd), optF(s.SubagentCostUsd), optF(s.AvgTtftMs),
		optF(s.TokensPerSecond), i64(s.ToolCalls), i64(s.ToolErrors), i64(s.Turns)}
}

// WriteUsageCSV 写用量 CSV：没有 series 时 bucket 为空、一组一行；有 series 时一桶一组一行（长表，只含非零组）。
func WriteUsageCSV(w io.Writer, u apigen.UsageStats) error {
	var rows [][]string
	if u.Series == nil {
		for _, g := range u.Groups {
			rows = append(rows, usageRow("", g))
		}
	}
	for _, p := range u.Series {
		for _, g := range p.Groups {
			rows = append(rows, usageRow(p.Key, g))
		}
	}
	return writeAll(w, usageCSVHeader, rows)
}

// WriteToolsCSV 写工具 CSV：每个工具一行（bucket 为空）；有 series 时其后每桶一行合计（tool 为空）。
func WriteToolsCSV(w io.Writer, t apigen.ToolStats) error {
	rows := make([][]string, 0, len(t.Tools)+len(t.Series))
	for _, s := range t.Tools {
		rows = append(rows, []string{"", s.Tool, i64(s.Calls), i64(s.Errors)})
	}
	for _, p := range t.Series {
		rows = append(rows, []string{p.Key, "", i64(p.Calls), i64(p.Errors)})
	}
	return writeAll(w, toolsCSVHeader, rows)
}

// WriteOrbsCSV 写 orb CSV（一组一行）。
func WriteOrbsCSV(w io.Writer, o apigen.OrbStats) error {
	rows := make([][]string, 0, len(o.Groups))
	for _, g := range o.Groups {
		rows = append(rows, []string{g.Key, optS(g.Label), optS(g.Type), optI(g.Cpus), optI(g.MemoryMB), itoa(g.Orbs),
			i64(g.RunningMs), i64(g.FrozenMs), i64(g.StoppedMs), i64(g.SetupMs), itoa(g.Spawns), itoa(g.SnapshotSpawns),
			itoa(g.Freezes), itoa(g.Stops), itoa(g.Wakes), itoa(g.Failures)})
	}
	return writeAll(w, orbsCSVHeader, rows)
}

// WriteOutputCSV 写产出 CSV（一组一行）。
func WriteOutputCSV(w io.Writer, o apigen.OutputStats) error {
	rows := make([][]string, 0, len(o.Groups))
	for _, g := range o.Groups {
		rows = append(rows, []string{g.Key, optS(g.Label), i64(g.ThreadsCreated), i64(g.ThreadsShipped), i64(g.ThreadsArchived),
			f64(g.ShippedCostUsd), i64(g.Commits), i64(g.Turns)})
	}
	return writeAll(w, outputCSVHeader, rows)
}

// CountUsageRecords 返回 [from, to) 内符合筛选的用量明细行数。
func CountUsageRecords(ctx context.Context, q *db.Queries, from, to int64, f Filter) (int64, error) {
	f = f.norm()
	return q.CountUsageRecords(ctx, db.CountUsageRecordsParams{FromTs: from, ToTs: to, Thread: f.Thread, Project: f.Project, Space: f.Space, Model: f.Model})
}

// WriteUsageRecords 把 [from, to) 的用量明细按 (ts, id) 键集分页（每页 5000 行）写成 CSV：每页一次查询、
// 读完即释放连接后再写出，页间不占用数据库连接。调用方先用 CountUsageRecords 检查上限。
func WriteUsageRecords(ctx context.Context, q *db.Queries, w io.Writer, from, to int64, f Filter) error {
	f = f.norm()
	cw := csv.NewWriter(w)
	if err := cw.Write(recordsCSVHeader); err != nil {
		return err
	}
	afterTs, afterID := from, int64(0)
	for {
		rows, err := q.ListUsageRecordsPage(ctx, db.ListUsageRecordsPageParams{
			FromTs: from, ToTs: to, AfterTs: afterTs, AfterID: afterID,
			Thread: f.Thread, Project: f.Project, Space: f.Space, Model: f.Model, Limit: exportPageSize,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			sub := "0"
			if r.IsSubagent {
				sub = "1"
			}
			if err := cw.Write([]string{i64(r.Ts), time.UnixMilli(r.Ts).UTC().Format(time.RFC3339Nano), r.ThreadID, r.ProjectID, r.SpaceID,
				r.Provider, r.Model, r.Purpose, sub, r.StopReason, i64(r.InputTokens), i64(r.OutputTokens),
				i64(r.CacheReadTokens), i64(r.CacheWriteTokens), f64(r.CostUsd), optional(r.DurationMs, i64), optional(r.TtftMs, i64)}); err != nil {
				return err
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
		if len(rows) < exportPageSize {
			return nil
		}
		last := rows[len(rows)-1]
		afterTs, afterID = last.Ts, last.ID
	}
}
