package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/stats"
)

// StatsDeps 是统计接口（M10）的依赖：只读聚合直接用 Store.Q，meta 与预算走 Service。
type StatsDeps struct {
	Service *stats.Service
}

// statsSvc 返回统计服务；未配置时写 501。
func (s *Server) statsSvc(w http.ResponseWriter) (*stats.Service, bool) {
	if s.Stats == nil || s.Stats.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "stats service not configured")
		return nil, false
	}
	return s.Stats.Service, true
}

// statsWindow 补全窗口并解析时区；from ≥ to → invalid_request，时区不可加载 → invalid_timezone。
// 只读聚合（窗口缺省值、orb 时长截到「现在」）用真实时钟；统计服务的时钟（e2e 可注入）只管 meta 与预算周期。
func (s *Server) statsWindow(w http.ResponseWriter, from, to *int64, tz *string) (int64, int64, *time.Location, bool) {
	f, t := stats.Window(from, to, time.Now())
	if f >= t {
		WriteError(w, http.StatusBadRequest, stats.CodeInvalidRequest, "from 必须早于 to")
		return 0, 0, nil, false
	}
	name := ""
	if tz != nil {
		name = *tz
	}
	loc, err := stats.LoadLocation(name)
	if err != nil {
		writeStatsErr(w, err)
		return 0, 0, nil, false
	}
	return f, t, loc, true
}

// writeStatsErr 把统计错误映射为 {code, message}：*stats.Error → 400（not_found → 404），其余 500。
func writeStatsErr(w http.ResponseWriter, err error) {
	var se *stats.Error
	if errors.As(err, &se) {
		status := http.StatusBadRequest
		if se.Code == stats.CodeNotFound {
			status = http.StatusNotFound
		}
		WriteError(w, status, se.Code, se.Message)
		return
	}
	WriteError(w, http.StatusInternalServerError, "internal", err.Error())
}

// statsExport 描述一次统计响应的导出形态。
type statsExport struct {
	kind, groupBy string
	from, to      int64
	format        *apigen.StatsFormat
	download      *bool
}

// writeStats 按 format 写 JSON 或 CSV 附件；download=true 的 JSON 也带附件头。
func writeStats(w http.ResponseWriter, x statsExport, v any, csvFn func(io.Writer) error) {
	h := w.Header()
	if x.format != nil && *x.format == apigen.StatsFormatCSV {
		setAttachment(h, stats.ExportFileName(x.kind, x.groupBy, x.from, x.to, "csv"))
		h.Set("Content-Type", "text/csv; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if err := csvFn(w); err != nil {
			slog.Warn("写统计 CSV 失败", "kind", x.kind, "err", err)
		}
		return
	}
	if x.download != nil && *x.download {
		setAttachment(h, stats.ExportFileName(x.kind, x.groupBy, x.from, x.to, "json"))
	}
	WriteJSON(w, http.StatusOK, v)
}

func setAttachment(h http.Header, name string) {
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}

func validFormat(w http.ResponseWriter, f *apigen.StatsFormat) bool {
	if f != nil && *f != apigen.StatsFormatJSON && *f != apigen.StatsFormatCSV {
		WriteError(w, http.StatusBadRequest, stats.CodeInvalidRequest, "format 必须是 json 或 csv")
		return false
	}
	return true
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// GetStatsMeta 实现 GET /api/v1/stats/meta。
func (s *Server) GetStatsMeta(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.statsSvc(w)
	if !ok {
		return
	}
	m, err := svc.Meta(r.Context())
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, m)
}

// GetUsageStats 实现 GET /api/v1/stats/usage。
func (s *Server) GetUsageStats(w http.ResponseWriter, r *http.Request, params apigen.GetUsageStatsParams) {
	if !validFormat(w, params.Format) {
		return
	}
	from, to, loc, ok := s.statsWindow(w, params.From, params.To, params.Tz)
	if !ok {
		return
	}
	u, err := stats.Usage(r.Context(), s.Store.Q, stats.UsageQuery{
		GroupBy: string(params.GroupBy), Series: string(deref(params.Series)), From: from, To: to, Loc: loc,
		Filter: stats.Filter{Project: params.Project, Space: params.Space, Thread: params.Thread, Model: params.Model},
		Limit:  deref(params.Limit),
	})
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	writeStats(w, statsExport{"usage", string(params.GroupBy), from, to, params.Format, params.Download}, u,
		func(out io.Writer) error { return stats.WriteUsageCSV(out, u) })
}

// ExportUsageRecords 实现 GET /api/v1/stats/usage/records：先计数（超过 500000 → range_too_large），
// 再按键集分页边取边写。
func (s *Server) ExportUsageRecords(w http.ResponseWriter, r *http.Request, params apigen.ExportUsageRecordsParams) {
	from, to, _, ok := s.statsWindow(w, params.From, params.To, nil)
	if !ok {
		return
	}
	f := stats.Filter{Project: params.Project, Space: params.Space, Thread: params.Thread, Model: params.Model}
	n, err := stats.CountUsageRecords(r.Context(), s.Store.Q, from, to, f)
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	if n > stats.MaxExportRows {
		WriteError(w, http.StatusBadRequest, stats.CodeRangeTooLarge, "明细超过 500000 行，请缩小窗口或加筛选")
		return
	}
	setAttachment(w.Header(), stats.ExportFileName("usage", "records", from, to, "csv"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := stats.WriteUsageRecords(r.Context(), s.Store.Q, w, from, to, f); err != nil {
		slog.Warn("导出用量明细失败", "err", err)
	}
}

// GetToolStats 实现 GET /api/v1/stats/tools。
func (s *Server) GetToolStats(w http.ResponseWriter, r *http.Request, params apigen.GetToolStatsParams) {
	if !validFormat(w, params.Format) {
		return
	}
	from, to, loc, ok := s.statsWindow(w, params.From, params.To, params.Tz)
	if !ok {
		return
	}
	series := string(deref(params.Series))
	t, err := stats.Tools(r.Context(), s.Store.Q, stats.ToolQuery{
		Series: series, From: from, To: to, Loc: loc,
		Filter: stats.Filter{Project: params.Project, Space: params.Space, Thread: params.Thread},
		Limit:  deref(params.Limit),
	})
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	groupBy := "tool"
	if series != "" {
		groupBy = series
	}
	writeStats(w, statsExport{"tools", groupBy, from, to, params.Format, params.Download}, t,
		func(out io.Writer) error { return stats.WriteToolsCSV(out, t) })
}

// GetOrbStats 实现 GET /api/v1/stats/orbs。
func (s *Server) GetOrbStats(w http.ResponseWriter, r *http.Request, params apigen.GetOrbStatsParams) {
	if !validFormat(w, params.Format) {
		return
	}
	from, to, loc, ok := s.statsWindow(w, params.From, params.To, params.Tz)
	if !ok {
		return
	}
	groupBy := string(deref(params.GroupBy))
	if groupBy == "" {
		groupBy = string(apigen.OrbGroupByType)
	}
	o, err := stats.Orbs(r.Context(), s.Store.Q, stats.OrbQuery{
		GroupBy: groupBy, From: from, To: to, Loc: loc, Project: params.Project, Orb: params.Orb,
	}, time.Now().UnixMilli())
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	writeStats(w, statsExport{"orbs", groupBy, from, to, params.Format, params.Download}, o,
		func(out io.Writer) error { return stats.WriteOrbsCSV(out, o) })
}

// GetOrbTimeline 实现 GET /api/v1/orbs/{id}/timeline。
func (s *Server) GetOrbTimeline(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.GetOrbTimelineParams) {
	now := time.Now()
	from, to := stats.Window(params.From, params.To, now)
	tl, err := stats.Timeline(r.Context(), s.Store.Q, id, from, to, now.UnixMilli())
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, tl)
}

// GetOutputStats 实现 GET /api/v1/stats/output。
func (s *Server) GetOutputStats(w http.ResponseWriter, r *http.Request, params apigen.GetOutputStatsParams) {
	if !validFormat(w, params.Format) {
		return
	}
	from, to, loc, ok := s.statsWindow(w, params.From, params.To, params.Tz)
	if !ok {
		return
	}
	groupBy := string(deref(params.GroupBy))
	if groupBy == "" {
		groupBy = string(apigen.OutputGroupByDay)
	}
	o, err := stats.Output(r.Context(), s.Store.Q, stats.OutputQuery{
		GroupBy: groupBy, From: from, To: to, Loc: loc, Project: params.Project, Space: params.Space,
	})
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	writeStats(w, statsExport{"output", groupBy, from, to, params.Format, params.Download}, o,
		func(out io.Writer) error { return stats.WriteOutputCSV(out, o) })
}

// GetBudget 实现 GET /api/v1/stats/budget。
func (s *Server) GetBudget(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.statsSvc(w)
	if !ok {
		return
	}
	b, err := svc.Budget(r.Context())
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, b)
}

// PutBudget 实现 PUT /api/v1/stats/budget。
func (s *Server) PutBudget(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.statsSvc(w)
	if !ok {
		return
	}
	var in apigen.Budget
	if !decodeJSON(w, r, &in) {
		return
	}
	b, err := svc.SetBudget(r.Context(), in)
	if err != nil {
		writeStatsErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, b)
}
