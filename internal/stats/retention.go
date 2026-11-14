package stats

import (
	"context"
	"log/slog"
)

// Sweep 按保留期清理：Samples → orb 资源采样；Events → 已删除 orb 的事件与 setup 记录、预算告警账本（活 orb 的事件
// 被运行逻辑依赖，永不删）；Usage → 用量 / 工具调用 / 回合事实行。值为 0 的一项不清理。错误只记日志。
func (s *Service) Sweep(ctx context.Context) {
	now := s.Now()
	r := s.cfg.Retention
	type step struct {
		name string
		fn   func(context.Context, int64) (int64, error)
	}
	run := func(before int64, steps ...step) {
		for _, st := range steps {
			n, err := st.fn(ctx, before)
			if err != nil {
				slog.Warn("统计保留期清理失败", "table", st.name, "err", err)
				continue
			}
			if n > 0 {
				slog.Info("统计保留期清理", "table", st.name, "rows", n)
			}
		}
	}
	q := s.st.Q
	if d := r.Samples.D(); d > 0 {
		run(now-d.Milliseconds(), step{"orb_samples", q.DeleteOrbSamplesBefore})
	}
	if d := r.Events.D(); d > 0 {
		run(now-d.Milliseconds(),
			step{"orb_events", q.DeleteDeadOrbEventsBefore},
			step{"orb_setup_runs", q.DeleteDeadOrbSetupRunsBefore},
			step{"budget_alerts", q.DeleteBudgetAlertsBefore})
	}
	if d := r.Usage.D(); d > 0 {
		run(now-d.Milliseconds(),
			step{"usage_records", q.DeleteUsageBefore},
			step{"tool_calls", q.DeleteToolCallsBefore},
			step{"turns", q.DeleteTurnsBefore})
	}
}
