package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// requestReport 登记：target 静止后向 reportTo（协调对话或发起它的线程）投递一次回报。
func (s *Service) requestReport(ctx context.Context, target, reportTo string) {
	if err := s.st.Q.UpsertThreadReport(ctx, db.UpsertThreadReportParams{ThreadID: target, ReportTo: reportTo, RequestedAt: s.nowMs(), Gen: ids.New("rep")}); err != nil {
		slog.Warn("coord: 登记回报失败", "thread", target, "reportTo", reportTo, "err", err)
	}
}

// StateChanged 实现 threads.Observer：只入队（回调持线程锁）。由 working 进入 idle / error / awaiting_input 的变化
// 才可能产生回报，由 Run 的循环处理。
func (s *Service) StateChanged(ev threads.StateEvent) {
	if ev.Prev != threads.StateWorking {
		return
	}
	switch ev.State {
	case threads.StateIdle, threads.StateError, threads.StateAwaitingInput:
	default:
		return
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// InputRequested 实现 threads.Observer：记下待答问题的标题，awaiting_input 的回报里给出。
func (s *Service) InputRequested(ev threads.InputEvent) {
	title := "plan proposal"
	if !ev.Plan {
		var f struct {
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(ev.Frame, &f)
		title = f.Title
		if title == "" {
			title = f.Message
		}
	}
	s.mu.Lock()
	s.questions[ev.ThreadID] = truncRunes(title, maxReportRunes)
	s.mu.Unlock()
}

// InputResolved 实现 threads.Observer（不关心）。
func (s *Service) InputResolved(string, string) {}

// Seen 实现 threads.Observer（不关心）。
func (s *Service) Seen(string) {}

// TurnEnded 实现 threads.Observer（S0 只落声明，切片 A 接入回报）。
func (s *Service) TurnEnded(threads.TurnEvent) {}

// processReports 处理排队的状态变化：有回报登记的线程向 report_to 投递一次回报。
func (s *Service) processReports(ctx context.Context) {
	for {
		s.mu.Lock()
		evs := s.events
		s.events = nil
		s.mu.Unlock()
		if len(evs) == 0 {
			return
		}
		for _, ev := range evs {
			s.report(ctx, ev)
			if s.afterReport != nil {
				s.afterReport(ev.ThreadID)
			}
		}
	}
}

// report 投递一次回报：idle / error 之后删除登记（只回报一次）；awaiting_input 之后保留（答完继续工作，静止时再报）。
// report_to 已归档 / 不存在，或超过回报限速时丢弃并删除登记。
func (s *Service) report(ctx context.Context, ev threads.StateEvent) {
	r, err := s.st.Q.GetThreadReport(ctx, ev.ThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("coord: 读取回报登记失败", "thread", ev.ThreadID, "err", err)
		return
	}
	final := ev.State != threads.StateAwaitingInput
	drop := func(why string) {
		slog.Info("coord: 丢弃线程回报", "thread", ev.ThreadID, "reportTo", r.ReportTo, "why", why)
		_, _ = s.st.Q.DeleteThreadReportGen(ctx, db.DeleteThreadReportGenParams{ThreadID: ev.ThreadID, Gen: r.Gen})
	}
	to, err := s.st.Q.GetThread(ctx, r.ReportTo)
	if err != nil || to.ArchivedAt != nil {
		drop("report target archived or gone")
		return
	}
	if !s.lim.allow(r.ReportTo+"|report", s.cfg.Now(), reportRules) {
		drop("report rate limit")
		return
	}
	t, err := s.st.Q.GetThread(ctx, ev.ThreadID)
	if err != nil {
		drop("thread gone")
		return
	}
	var body string
	if ev.State == threads.StateAwaitingInput {
		s.mu.Lock()
		body = s.questions[ev.ThreadID]
		s.mu.Unlock()
	} else {
		if body, err = s.lastAssistantText(ctx, t, maxReportRunes); err != nil {
			slog.Warn("coord: 读取线程回复失败", "thread", t.ID, "err", err)
		}
		if ev.State == threads.StateError && ev.Reason != "" {
			body += "\n(error: " + ev.Reason + ")"
		}
	}
	text := "[Oryn thread report · automatic] " + t.ID + " " + quote(t.Title) + " is now " + ev.State + ".\n" +
		wrapUntrusted(t.ID, body)
	if _, err := s.th.Deliver(ctx, r.ReportTo, threads.AutoMessage{Text: text}, nil); err != nil {
		slog.Warn("coord: 投递线程回报失败", "thread", t.ID, "reportTo", r.ReportTo, "err", err)
	}
	if final {
		_, _ = s.st.Q.DeleteThreadReportGen(ctx, db.DeleteThreadReportGenParams{ThreadID: ev.ThreadID, Gen: r.Gen})
	}
}
