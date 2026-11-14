package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Notifier 发一条不属于任何线程的系统通知（*notify.Service 实现）。
type Notifier interface {
	PostSystem(ctx context.Context, kind, title, body, link string) error
}

// budgetLink 是预算告警的站内落点。
const budgetLink = "/stats/usage"

// 预算默认值与校验范围。
const (
	defaultWarnPct = 80
	minBudgetUsd   = 0.01
	maxWarnPct     = 99
)

// budgetPeriod 是预算的一个周期（日 / 月）。
type budgetPeriod struct {
	name     string // day | month
	key      string
	from, to int64
	limit    *float64
}

// loadBudget 读预算设置；没有设置时返回 {warnPct: 80, timezone: UTC, enabled: false}，ok 为假。
func (s *Service) loadBudget(ctx context.Context) (apigen.Budget, bool, error) {
	b, err := s.st.Q.GetBudget(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Budget{WarnPct: defaultWarnPct, Timezone: "UTC"}, false, nil
	}
	if err != nil {
		return apigen.Budget{}, false, err
	}
	return apigen.Budget{DailyUsd: b.DailyUsd, MonthlyUsd: b.MonthlyUsd, WarnPct: int(b.WarnPct), Timezone: b.Timezone, Enabled: b.Enabled}, true, nil
}

// periods 返回 now 在 b.timezone 下的自然日与自然月。
func periods(b apigen.Budget, now time.Time) (day, month budgetPeriod) {
	loc, err := LoadLocation(b.Timezone)
	if err != nil {
		loc = time.UTC
	}
	t := now.In(loc)
	d := periodLabel(t, GrainDay)
	m := periodLabel(t, GrainMonth)
	day = budgetPeriod{name: "day", key: d.Format("2006-01-02"), from: localStart(loc, d), to: localStart(loc, advance(d, GrainDay)), limit: b.DailyUsd}
	month = budgetPeriod{name: "month", key: m.Format("2006-01"), from: localStart(loc, m), to: localStart(loc, advance(m, GrainMonth)), limit: b.MonthlyUsd}
	return day, month
}

// budgetLevel 是 spent 相对上限的级别。
func budgetLevel(spent float64, limit *float64, warnPct int) apigen.BudgetLevel {
	switch {
	case limit == nil:
		return apigen.BudgetLevelNone
	case spent >= *limit:
		return apigen.BudgetLevelOver
	case warnPct > 0 && spent >= *limit*float64(warnPct)/100:
		return apigen.BudgetLevelWarn
	}
	return apigen.BudgetLevelNone
}

func (s *Service) periodStatus(ctx context.Context, b apigen.Budget, p budgetPeriod) (apigen.BudgetPeriod, error) {
	spent, err := s.st.Q.SumUsageCost(ctx, db.SumUsageCostParams{FromTs: p.from, ToTs: p.to})
	if err != nil {
		return apigen.BudgetPeriod{}, err
	}
	spent = finite(spent) // 入库前的越界行可能把和推成 +Inf：按 0 报，响应与告警文本才可编码
	return apigen.BudgetPeriod{Key: p.key, From: p.from, To: p.to, SpentUsd: spent, LimitUsd: p.limit, Level: budgetLevel(spent, p.limit, b.WarnPct)}, nil
}

// Budget 返回预算设置与当日 / 当月的花费和级别。
func (s *Service) Budget(ctx context.Context) (apigen.BudgetStatus, error) {
	b, _, err := s.loadBudget(ctx)
	if err != nil {
		return apigen.BudgetStatus{}, err
	}
	day, month := periods(b, s.cfg.Now())
	out := apigen.BudgetStatus{Budget: b}
	if out.Day, err = s.periodStatus(ctx, b, day); err != nil {
		return apigen.BudgetStatus{}, err
	}
	if out.Month, err = s.periodStatus(ctx, b, month); err != nil {
		return apigen.BudgetStatus{}, err
	}
	return out, nil
}

// SetBudget 整体替换预算设置。金额 < 0.01、warnPct 越界、时区非法返回 invalid_budget。
func (s *Service) SetBudget(ctx context.Context, b apigen.Budget) (apigen.BudgetStatus, error) {
	for _, v := range []*float64{b.DailyUsd, b.MonthlyUsd} {
		if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v < minBudgetUsd) {
			return apigen.BudgetStatus{}, errorf(CodeInvalidBudget, "上限须 ≥ %.2f 美元", minBudgetUsd)
		}
	}
	if b.WarnPct < 0 || b.WarnPct > maxWarnPct {
		return apigen.BudgetStatus{}, errorf(CodeInvalidBudget, "warnPct 须在 0–%d 之间", maxWarnPct)
	}
	if b.Timezone == "" {
		return apigen.BudgetStatus{}, errorf(CodeInvalidBudget, "timezone 不能为空")
	}
	if _, err := LoadLocation(b.Timezone); err != nil {
		return apigen.BudgetStatus{}, errorf(CodeInvalidBudget, "无法加载时区 %q", b.Timezone)
	}
	if err := s.st.Q.UpsertBudget(ctx, db.UpsertBudgetParams{
		DailyUsd: b.DailyUsd, MonthlyUsd: b.MonthlyUsd, WarnPct: int64(b.WarnPct), Timezone: b.Timezone,
		Enabled: b.Enabled, UpdatedAt: s.Now(),
	}); err != nil {
		return apigen.BudgetStatus{}, err
	}
	return s.Budget(ctx)
}

// EvaluateBudget 评估当日 / 当月的花费：启用且该周期设了上限、级别不是 none 时，在一个事务里为达到的每个级别写账本
// （over 且 warnPct > 0 时 warn、over 两行都写）；有新插入的行时，事务提交之后只为其中最高的级别发一条系统通知。
// 通知失败只记日志：不回滚账本、不重试。
func (s *Service) EvaluateBudget(ctx context.Context) {
	b, ok, err := s.loadBudget(ctx)
	if err != nil {
		slog.Warn("读取预算失败", "err", err)
		return
	}
	if !ok || !b.Enabled {
		return
	}
	now := s.cfg.Now()
	day, month := periods(b, now)
	for _, p := range []budgetPeriod{day, month} {
		if p.limit == nil {
			continue
		}
		st, err := s.periodStatus(ctx, b, p)
		if err != nil {
			slog.Warn("预算评估失败", "period", p.name, "err", err)
			continue
		}
		var levels []apigen.BudgetLevel
		switch st.Level {
		case apigen.BudgetLevelNone:
			continue
		case apigen.BudgetLevelWarn:
			levels = []apigen.BudgetLevel{apigen.BudgetLevelWarn}
		case apigen.BudgetLevelOver:
			if b.WarnPct > 0 {
				levels = append(levels, apigen.BudgetLevelWarn)
			}
			levels = append(levels, apigen.BudgetLevelOver)
		}
		var top apigen.BudgetLevel
		err = s.st.Tx(ctx, func(q *db.Queries) error {
			for _, lv := range levels {
				n, err := q.InsertBudgetAlert(ctx, db.InsertBudgetAlertParams{
					Period: p.name, PeriodKey: p.key, Level: string(lv), SpentUsd: st.SpentUsd, LimitUsd: *p.limit, CreatedAt: now.UnixMilli(),
				})
				if err != nil {
					return err
				}
				if n > 0 {
					top = lv // levels 升序：最后插入的即最高级别
				}
			}
			return nil
		})
		if err != nil {
			slog.Warn("写预算告警账本失败", "period", p.name, "err", err)
			continue
		}
		if top == "" {
			continue
		}
		title, body := budgetText(p, top, st.SpentUsd, b)
		slog.Info("预算告警", "period", p.name, "key", p.key, "level", top, "spent", st.SpentUsd, "limit", *p.limit)
		if s.n == nil {
			continue
		}
		if err := s.n.PostSystem(ctx, string(apigen.NotificationKindBudget), title, body, budgetLink); err != nil {
			slog.Warn("发送预算告警失败", "period", p.name, "key", p.key, "err", err)
		}
	}
}

// budgetText 返回预算告警的标题与正文（固定英文短语，与 M9 的定时通知一致）。
func budgetText(p budgetPeriod, lv apigen.BudgetLevel, spent float64, b apigen.Budget) (string, string) {
	period := "Daily"
	if p.name == "month" {
		period = "Monthly"
	}
	if lv == apigen.BudgetLevelOver {
		return period + " budget exceeded",
			fmt.Sprintf("$%.2f of $%.2f spent (%s, %s).", spent, *p.limit, p.key, b.Timezone)
	}
	return period + " budget warning",
		fmt.Sprintf("$%.2f of $%.2f spent, past the %d%% warning threshold (%s, %s).", spent, *p.limit, b.WarnPct, p.key, b.Timezone)
}
