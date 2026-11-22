package research

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// settingsRow 是 research_settings 的一行（没有行时取缺省值）。
type settingsRow = db.ResearchSetting

// defaultSettings 是还没有设置行时的缺省值（与迁移 0054 的列缺省值相同）。
func defaultSettings() settingsRow {
	return settingsRow{ID: 1, Enabled: 0, Web: 1, Mine: 1, Internal: 1, Mcp: 0, DailyUsd: defaultDailyUSD, McpTools: "{}"}
}

// loadSettings 读设置行；没有时返回缺省值。
func (s *Service) loadSettings(ctx context.Context) (settingsRow, error) {
	row, err := s.st.Q.GetResearchSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultSettings(), nil
	}
	return row, err
}

// mcpTools 解出设置里的研究可用 MCP 工具（解析失败按空表处理）。
func mcpTools(set settingsRow) map[string][]string {
	out := map[string][]string{}
	if err := json.Unmarshal([]byte(set.McpTools), &out); err != nil {
		slog.Warn("research: mcp_tools 不是合法 JSON，按空表处理", "err", err)
		return map[string][]string{}
	}
	return out
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// settingsToAPI 把设置行转成接口类型。
func settingsToAPI(set settingsRow) apigen.ResearchSettings {
	out := apigen.ResearchSettings{
		Enabled: set.Enabled == 1, Web: set.Web == 1, Mine: set.Mine == 1, Internal: set.Internal == 1, Mcp: set.Mcp == 1,
		DailyUsd: float32(set.DailyUsd), Preferences: set.Preferences, McpTools: mcpTools(set),
	}
	if set.Model != nil {
		if p, id, ok := strings.Cut(*set.Model, "/"); ok && p != "" && id != "" {
			out.Model = &apigen.ModelRef{Provider: p, ID: id}
		}
	}
	return out
}

// dayStart 是 t 所在本地自然日的零点。
func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// spentToday 是研究员对话今天（本地时区）的花费合计。
func (s *Service) spentToday(ctx context.Context) (float64, error) {
	ids, err := s.cfg.Host.ResearcherThreads(ctx)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	ptrs := make([]*string, len(ids))
	for i := range ids {
		ptrs[i] = &ids[i]
	}
	return s.st.Q.SumUsageCostForThreads(ctx, db.SumUsageCostForThreadsParams{ThreadIds: ptrs, FromTs: dayStart(s.now()).UnixMilli()})
}

// capped 报告今天的花费是否已达上限；首次达到时发一条预算通知（每天至多一条）。
func (s *Service) capped(ctx context.Context, set settingsRow) bool {
	spent, err := s.spentToday(ctx)
	if err != nil {
		slog.Warn("research: 统计今天的花费失败，本轮不发起运行", "err", err)
		return true
	}
	if spent < set.DailyUsd {
		return false
	}
	day := s.now().Format(time.DateOnly)
	if set.CappedDay == nil || *set.CappedDay != day {
		if s.cfg.Notifier != nil {
			body := fmt.Sprintf("$%.2f of $%.2f spent", spent, set.DailyUsd)
			if err := s.cfg.Notifier.PostSystem(ctx, "budget", "Background research paused for today", body, "/settings/assistant"); err != nil {
				slog.Warn("research: 发送预算通知失败", "err", err)
			}
		}
		if err := s.st.Q.SetResearchCappedDay(ctx, db.SetResearchCappedDayParams{Day: &day, UpdatedAt: s.now().UnixMilli()}); err != nil {
			slog.Warn("research: 记录预算通知日期失败", "err", err)
		}
	}
	return true
}

// Settings 返回设置与今天的花费。
func (s *Service) Settings(ctx context.Context) (apigen.ResearchSettingsEnvelope, error) {
	set, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.ResearchSettingsEnvelope{}, err
	}
	spent, err := s.spentToday(ctx)
	if err != nil {
		return apigen.ResearchSettingsEnvelope{}, err
	}
	return apigen.ResearchSettingsEnvelope{
		Settings: settingsToAPI(set), SpentTodayUsd: float32(spent), Capped: spent >= set.DailyUsd,
	}, nil
}

// PutSettings 整体改写设置。总开关由关变开且还没有挖掘起点时，从现在起算（只挖掘之后有新消息的对话）。保存后刷新
// 出站策略（研究可用的 MCP 工具）、模型变化时改研究员的模型，并唤醒引擎。
func (s *Service) PutSettings(ctx context.Context, in apigen.ResearchSettings) (apigen.ResearchSettingsEnvelope, error) {
	if in.DailyUsd < dailyUSDMin || in.DailyUsd > dailyUSDMax {
		return apigen.ResearchSettingsEnvelope{}, errInvalid(fmt.Sprintf("dailyUsd must be between %g and %g", dailyUSDMin, dailyUSDMax))
	}
	if utf8.RuneCountInString(in.Preferences) > prefsMax {
		return apigen.ResearchSettingsEnvelope{}, errInvalid(fmt.Sprintf("preferences must be at most %d characters", prefsMax))
	}
	tools := map[string][]string{}
	for server, names := range in.McpTools {
		if strings.TrimSpace(server) == "" {
			return apigen.ResearchSettingsEnvelope{}, errInvalid("mcpTools server names must not be empty")
		}
		if len(names) > mcpToolsPerServer {
			return apigen.ResearchSettingsEnvelope{}, errInvalid(fmt.Sprintf("at most %d tools per server", mcpToolsPerServer))
		}
		seen := map[string]bool{}
		var keep []string
		for _, n := range names {
			if n == "" || utf8.RuneCountInString(n) > mcpToolNameMax {
				return apigen.ResearchSettingsEnvelope{}, errInvalid(fmt.Sprintf("tool names must be 1-%d characters", mcpToolNameMax))
			}
			if !seen[n] {
				seen[n] = true
				keep = append(keep, n)
			}
		}
		if len(keep) > 0 {
			tools[server] = keep
		}
	}
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		return apigen.ResearchSettingsEnvelope{}, err
	}
	var model *string
	if in.Model != nil {
		if in.Model.Provider == "" || in.Model.ID == "" {
			return apigen.ResearchSettingsEnvelope{}, errInvalid("model must have provider and id")
		}
		m := in.Model.Provider + "/" + in.Model.ID
		model = &m
	}
	prev, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.ResearchSettingsEnvelope{}, err
	}
	now := s.now().UnixMilli()
	err = s.st.Q.UpsertResearchSettings(ctx, db.UpsertResearchSettingsParams{
		Enabled: b2i(in.Enabled), Web: b2i(in.Web), Mine: b2i(in.Mine), Internal: b2i(in.Internal), Mcp: b2i(in.Mcp),
		Model: model, DailyUsd: float64(in.DailyUsd), Preferences: in.Preferences, McpTools: string(toolsJSON), UpdatedAt: now,
	})
	if err != nil {
		return apigen.ResearchSettingsEnvelope{}, err
	}
	if in.Enabled && prev.Enabled == 0 && prev.MineSince == nil {
		if err := s.st.Q.SetResearchMineSince(ctx, db.SetResearchMineSinceParams{Since: &now, UpdatedAt: now}); err != nil {
			return apigen.ResearchSettingsEnvelope{}, err
		}
	}
	if err := s.cfg.Host.RefreshPolicy(ctx); err != nil {
		slog.Warn("research: 刷新协调宿主的出站策略失败", "err", err)
	}
	if deref(prev.Model) != deref(model) {
		if err := s.cfg.Host.SetResearcherModel(ctx, deref(model)); err != nil {
			slog.Warn("research: 改研究员的模型失败", "err", err)
		}
	}
	s.Wake()
	return s.Settings(ctx)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
