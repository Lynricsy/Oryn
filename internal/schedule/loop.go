package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// ---- 事件循环 ----

// Run 恢复未结束的运行 → 补跑到期定时 → 按最早的 next_run_at 上定时器并处理事件，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.recover(ctx)
	s.Tick(ctx)
	defer func() {
		s.mu.Lock()
		if s.stopArm != nil {
			s.stopArm()
			s.stopArm = nil
		}
		s.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.mu.Lock()
			due := s.tickDue
			s.tickDue = false
			s.mu.Unlock()
			if due {
				s.Tick(ctx)
				continue
			}
			s.loopMu.Lock()
			s.processEvents(ctx)
			s.loopMu.Unlock()
		}
	}
}

// Tick 处理已入队的事件与全部到期的定时，然后重新上定时器（测试直接调用）。
func (s *Service) Tick(ctx context.Context) {
	s.loopMu.Lock()
	s.processEvents(ctx)
	s.tickLocked(ctx)
	s.processEvents(ctx)
	s.loopMu.Unlock()
	s.arm(ctx)
}

func (s *Service) tickLocked(ctx context.Context) {
	for {
		now := s.cfg.Now()
		due, err := s.st.Q.ListDueSchedules(ctx, now.UnixMilli())
		if err != nil {
			slog.Warn("schedule: 读取到期定时失败", "err", err)
			return
		}
		for _, sc := range due {
			s.fire(ctx, sc.ID, now)
		}
		// 每次触发都把 next_run_at 推到 now 之后（或停用），满 100 条时再查一轮
		if len(due) < 100 || ctx.Err() != nil {
			return
		}
	}
}

// arm 按最早的 next_run_at 上定时器（封顶 1 小时）。
func (s *Service) arm(ctx context.Context) {
	next, err := s.st.Q.MinNextRun(ctx)
	if err != nil {
		slog.Warn("schedule: 读取下一次触发失败", "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopArm != nil {
		s.stopArm()
		s.stopArm = nil
	}
	if next == 0 {
		return
	}
	d := min(max(time.Duration(next-s.cfg.Now().UnixMilli())*time.Millisecond, 0), maxArm)
	s.stopArm = s.cfg.AfterFunc(d, func() {
		s.mu.Lock()
		s.tickDue = true
		s.mu.Unlock()
		signal(s.wake)
	})
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Service) enqueue(ev event) {
	s.mu.Lock()
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
	signal(s.wake)
}

// StateChanged 实现 threads.Observer：只入队有未结束运行的线程（以及建线程期间的全部事件）。
func (s *Service) StateChanged(ev threads.StateEvent) {
	s.mu.Lock()
	_, ok := s.open[ev.ThreadID]
	ok = ok || s.creating > 0
	if ok {
		s.queue = append(s.queue, event{kind: evState, threadID: ev.ThreadID, state: ev.State})
	}
	s.mu.Unlock()
	if ok {
		signal(s.wake)
	}
}

// InputRequested 实现 threads.Observer（不关心）。
func (s *Service) InputRequested(threads.InputEvent) {}

// InputResolved 实现 threads.Observer（不关心）。
func (s *Service) InputResolved(string, string) {}

// Seen 实现 threads.Observer（不关心）。
func (s *Service) Seen(string) {}

// TurnEnded 实现 threads.Observer（S0 只落声明，切片 B 接入运行结算）。
func (s *Service) TurnEnded(threads.TurnEvent) {}

// processEvents 顺序处理已入队的事件直到队列为空。调用方持有 loopMu。
func (s *Service) processEvents(ctx context.Context) {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		ev := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		switch ev.kind {
		case evState:
			s.onState(ctx, ev.threadID, ev.state)
		case evAck:
			s.onAck(ctx, ev.runID, ev.err)
		case evTimeout:
			s.onTimeout(ctx, ev.runID)
		}
	}
}

// ---- 触发 ----

// fireParams 是一次已插入 pending 的运行的投递参数。
type fireParams struct {
	sc           db.Schedule // 推进之前的定义
	runID        string
	run          int64
	scheduledFor int64
	trigger      string
	missed       int64
}

// fire 处理一个到期的定时：事务里推进并判定，事务外投递 / 建线程。调用方持有 loopMu。
func (s *Service) fire(ctx context.Context, id string, now time.Time) {
	nowMs := now.UnixMilli()
	var fp *fireParams
	var threadID string
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		sc, err := q.GetSchedule(ctx, id)
		if err != nil {
			return err
		}
		// 读到期列表之后定义被改动：以事务内的现值为准
		if !sc.Enabled || sc.NextRunAt == nil || *sc.NextRunAt > nowMs {
			return nil
		}
		threadID = deref(sc.ThreadID)
		loc := loadLoc(sc.Timezone)
		scheduledFor := *sc.NextRunAt
		trigger := triggerSchedule
		if nowMs-scheduledFor > lateAfter.Milliseconds() {
			trigger = triggerCatchup
		}
		var missed int64
		if sc.Kind != KindOnce {
			missed = int64(countMissed(sc.Kind, sc.Spec, loc, time.UnixMilli(scheduledFor), now))
		}
		adv := db.AdvanceScheduleParams{ID: id, LastRunAt: &nowMs, RunCount: sc.RunCount + 1, Enabled: true, UpdatedAt: nowMs}
		if sc.Kind != KindOnce {
			if t, ok := NextAfter(sc.Kind, sc.Spec, loc, now); ok && (sc.EndsAt == nil || t.UnixMilli() <= *sc.EndsAt) {
				adv.NextRunAt = new(t.UnixMilli())
			}
		}
		if adv.NextRunAt == nil {
			adv.Enabled, adv.PausedReason = false, new(pausedDone)
		}
		status, reason, archived, err := decide(ctx, q, sc)
		if err != nil {
			return err
		}
		if archived {
			adv.Enabled, adv.PausedReason, adv.NextRunAt = false, new(pausedArchived), nil
		}
		if err := q.AdvanceSchedule(ctx, adv); err != nil {
			return err
		}
		runID, err := insertRun(ctx, q, sc, trigger, scheduledFor, missed, nowMs, status, reason)
		if err != nil {
			return err
		}
		if status == runPending {
			fp = &fireParams{sc: sc, runID: runID, run: sc.RunCount + 1, scheduledFor: scheduledFor, trigger: trigger, missed: missed}
		}
		return nil
	})
	if err != nil {
		slog.Warn("schedule: 触发失败", "schedule", id, "err", err)
		return
	}
	if threadID != "" {
		s.th.Publish(ctx, threadID)
	}
	if fp != nil {
		s.start(ctx, *fp)
	}
}

// decide 判定一次运行是投递（pending）还是跳过；archived 为真表示目标已归档、定时应停用。
func decide(ctx context.Context, q *db.Queries, sc db.Schedule) (status, reason string, archived bool, err error) {
	if sc.Mode == ModeThread {
		t, err := q.GetThread(ctx, deref(sc.ThreadID))
		if err != nil {
			return "", "", false, err
		}
		if t.ArchivedAt != nil {
			return runSkipped, "thread_archived", true, nil
		}
		n, err := q.CountOpenScheduleRuns(ctx, db.CountOpenScheduleRunsParams{ScheduleID: sc.ID, IncludeNeedsInput: true})
		if err != nil {
			return "", "", false, err
		}
		if n > 0 {
			return runSkipped, "overlap", false, nil
		}
		return runPending, "", false, nil
	}
	if sc.ProjectID != nil {
		pr, err := q.GetProject(ctx, *sc.ProjectID)
		if err != nil {
			return "", "", false, err
		}
		if pr.ArchivedAt != nil {
			return runSkipped, "project_archived", true, nil
		}
	}
	// new_thread：needs_input 不挡后续运行（每次是独立线程）
	n, err := q.CountOpenScheduleRuns(ctx, db.CountOpenScheduleRunsParams{ScheduleID: sc.ID, IncludeNeedsInput: false})
	if err != nil {
		return "", "", false, err
	}
	switch {
	case sc.Overlap == OverlapAllow && n >= overlapCap:
		return runSkipped, "overlap_cap", false, nil
	case sc.Overlap != OverlapAllow && n > 0:
		return runSkipped, "overlap", false, nil
	}
	return runPending, "", false, nil
}

func insertRun(ctx context.Context, q *db.Queries, sc db.Schedule, trigger string, scheduledFor, missed, nowMs int64, status, reason string) (string, error) {
	p := db.InsertScheduleRunParams{
		ID: ids.New("srn"), ScheduleID: sc.ID, ThreadID: sc.ThreadID, Trigger: trigger, ScheduledFor: scheduledFor,
		Missed: missed, StartedAt: nowMs, Status: status, Error: optStr(reason),
	}
	if status == runSkipped {
		p.FinishedAt = &nowMs
	}
	if err := q.InsertScheduleRun(ctx, p); err != nil {
		return "", err
	}
	if err := q.TrimScheduleRuns(ctx, db.TrimScheduleRunsParams{ScheduleID: sc.ID, Keep: keepRuns}); err != nil {
		return "", err
	}
	return p.ID, nil
}

// RunNow 手动触发一次（已暂停的定时也可以）：不改 next_run_at，run_count + 1。
// 已有未结束的运行时回 schedule_busy（线程内定时含 needs_input；new_thread 按重叠策略）。
func (s *Service) RunNow(ctx context.Context, id string) (apigen.ScheduleRun, error) {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	now := s.cfg.Now()
	nowMs := now.UnixMilli()
	var fp fireParams
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		sc, err := q.GetSchedule(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return errScheduleNotFound
		}
		if err != nil {
			return err
		}
		status, reason, archived, err := decide(ctx, q, sc)
		if err != nil {
			return err
		}
		switch {
		case archived && sc.Mode == ModeThread:
			return errf(CodeThreadArchived, "the thread is archived")
		case archived:
			return invalid("the project is archived")
		case status != runPending:
			return errf(CodeScheduleBusy, "the previous run has not finished (%s)", reason)
		}
		if err := q.AdvanceSchedule(ctx, db.AdvanceScheduleParams{
			ID: id, NextRunAt: sc.NextRunAt, LastRunAt: &nowMs, RunCount: sc.RunCount + 1, Enabled: sc.Enabled,
			PausedReason: sc.PausedReason, UpdatedAt: nowMs,
		}); err != nil {
			return err
		}
		runID, err := insertRun(ctx, q, sc, triggerManual, nowMs, 0, nowMs, runPending, "")
		if err != nil {
			return err
		}
		fp = fireParams{sc: sc, runID: runID, run: sc.RunCount + 1, scheduledFor: nowMs, trigger: triggerManual}
		return nil
	})
	if err != nil {
		return apigen.ScheduleRun{}, err
	}
	s.start(ctx, fp)
	s.processEvents(ctx)
	r, err := s.st.Q.GetScheduleRun(ctx, fp.runID)
	if err != nil {
		return apigen.ScheduleRun{}, err
	}
	return runAPI(r), nil
}

// start 在事务外执行一次 pending 运行。调用方持有 loopMu。
func (s *Service) start(ctx context.Context, fp fireParams) {
	loc := loadLoc(fp.sc.Timezone)
	text := header(fp, loc) + "\n" + expand(fp, s.cfg.Now(), loc)
	if fp.sc.Mode == ModeThread {
		s.deliver(ctx, fp, text)
		return
	}
	s.createThread(ctx, fp, loc, text)
}

func (s *Service) deliver(ctx context.Context, fp fireParams, text string) {
	threadID := deref(fp.sc.ThreadID)
	d := &delivery{runID: fp.runID, scheduleID: fp.sc.ID, threadID: threadID}
	s.mu.Lock()
	s.open[threadID] = fp.runID
	s.pending[fp.runID] = d
	s.mu.Unlock()
	cancel, err := s.th.Deliver(ctx, threadID, threads.AutoMessage{Text: text}, func(err error) {
		s.enqueue(event{kind: evAck, runID: fp.runID, err: err})
	})
	if err != nil {
		s.mu.Lock()
		delete(s.pending, fp.runID)
		delete(s.open, threadID)
		s.mu.Unlock()
		s.deliveryFailed(ctx, fp.sc, threadID, fp.runID, errText(err))
		return
	}
	stop := s.cfg.AfterFunc(s.cfg.DeliverTimeout, func() { s.enqueue(event{kind: evTimeout, runID: fp.runID}) })
	s.mu.Lock()
	if s.pending[fp.runID] == d {
		d.cancel, d.stop = cancel, stop
	} else {
		stop() // 定时在投递期间被删除
	}
	s.mu.Unlock()
}

// deliveryFailed 记一次投递失败（同步错误 / 应答出错 / 超时）：运行 failed、通知、计入失败次数。
func (s *Service) deliveryFailed(ctx context.Context, sc db.Schedule, threadID, runID, reason string) {
	s.finishRun(ctx, runID, runFailed, reason)
	s.post(ctx, threadID, "", fmt.Sprintf("Scheduled run %q could not be delivered: %s", displayName(sc.Name), reason))
	s.countFailure(ctx, sc.ID)
}

func (s *Service) createThread(ctx context.Context, fp fireParams, loc *time.Location, text string) {
	sc := fp.sc
	p := threads.CreateParams{
		ProjectID: deref(sc.ProjectID), Prompt: text, Provider: deref(sc.Provider), Type: deref(sc.OrbType),
		Model: parseModel(sc.Model), ThinkingLevel: deref(sc.ThinkingLevel), SpaceID: deref(sc.SpaceID),
		Title:      sc.Name + " · " + time.UnixMilli(fp.scheduledFor).In(loc).Format("01-02 15:04"),
		ScheduleID: sc.ID, ScheduleRun: fp.run,
	}
	s.mu.Lock()
	s.creating++
	s.mu.Unlock()
	t, err := s.th.Create(ctx, p)
	s.mu.Lock()
	s.creating--
	if err == nil {
		s.open[t.ID] = fp.runID
	}
	s.mu.Unlock()
	if err != nil {
		reason := truncate(errText(err), maxRunError)
		s.finishRun(ctx, fp.runID, runFailed, reason)
		s.post(ctx, "", fmt.Sprintf("Schedule %q", sc.Name), "Scheduled run could not start a thread: "+reason)
		s.countFailure(ctx, sc.ID)
		return
	}
	nowMs := s.cfg.Now().UnixMilli()
	if err := s.st.Q.SetScheduleRunThread(ctx, db.SetScheduleRunThreadParams{ID: fp.runID, ThreadID: &t.ID}); err != nil {
		slog.Warn("schedule: 记录运行线程失败", "run", fp.runID, "err", err)
	}
	if err := s.st.Q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{ID: fp.runID, Status: runRunning, DeliveredAt: &nowMs}); err != nil {
		slog.Warn("schedule: 更新运行状态失败", "run", fp.runID, "err", err)
	}
	s.retain(ctx, sc)
}

// retain 是 new_thread 的保留策略：最近 keep_last 个未归档线程之外，静止（idle / error）且未置顶的自动归档；
// 归档后没有别的未归档线程在用的 orb 一并删除。从不删除线程。
func (s *Service) retain(ctx context.Context, sc db.Schedule) {
	rows, err := s.st.Q.ListScheduleThreads(ctx, sc.ID)
	if err != nil {
		slog.Warn("schedule: 读取定时线程失败", "schedule", sc.ID, "err", err)
		return
	}
	keep := int(sc.KeepLast)
	if len(rows) <= keep {
		return
	}
	for _, r := range rows[keep:] {
		if r.Pinned || (r.State != string(apigen.ThreadStateIdle) && r.State != string(apigen.ThreadStateError)) {
			continue
		}
		if err := s.th.Archive(ctx, r.ID); err != nil {
			slog.Warn("schedule: 自动归档失败", "schedule", sc.ID, "thread", r.ID, "err", err)
			continue
		}
		if r.OrbID == nil {
			continue
		}
		n, err := s.st.Q.CountUnarchivedThreadsByOrb(ctx, *r.OrbID)
		if err != nil || n > 0 {
			continue
		}
		if err := s.orbs.Delete(ctx, *r.OrbID); err != nil {
			slog.Warn("schedule: 删除自动归档线程的 orb 失败", "schedule", sc.ID, "orb", *r.OrbID, "err", err)
		}
	}
}

// ---- 运行的后续 ----

func (s *Service) onAck(ctx context.Context, runID string, err error) {
	s.mu.Lock()
	d := s.pending[runID]
	delete(s.pending, runID)
	if d != nil && d.stop != nil {
		d.stop()
	}
	if d != nil && err != nil && s.open[d.threadID] == runID {
		delete(s.open, d.threadID)
	}
	s.mu.Unlock()
	if d == nil {
		return
	}
	if err != nil {
		sc, gerr := s.st.Q.GetSchedule(ctx, d.scheduleID)
		if gerr != nil {
			return
		}
		s.deliveryFailed(ctx, sc, d.threadID, runID, errText(err))
		return
	}
	nowMs := s.cfg.Now().UnixMilli()
	if err := s.st.Q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{ID: runID, Status: runRunning, DeliveredAt: &nowMs}); err != nil {
		slog.Warn("schedule: 更新运行状态失败", "run", runID, "err", err)
	}
}

func (s *Service) onTimeout(ctx context.Context, runID string) {
	s.mu.Lock()
	d := s.pending[runID]
	s.mu.Unlock()
	if d == nil {
		return
	}
	// cancel 在消息仍在控制面排队时撤回它；已下发则继续等应答
	if d.cancel == nil || !d.cancel() {
		s.mu.Lock()
		d.stop = nil
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	delete(s.pending, runID)
	if s.open[d.threadID] == runID {
		delete(s.open, d.threadID)
	}
	s.mu.Unlock()
	sc, err := s.st.Q.GetSchedule(ctx, d.scheduleID)
	if err != nil {
		return
	}
	s.deliveryFailed(ctx, sc, d.threadID, runID, "deliver_timeout")
}

func (s *Service) onState(ctx context.Context, threadID, state string) {
	s.mu.Lock()
	runID, ok := s.open[threadID]
	s.mu.Unlock()
	if !ok {
		return
	}
	run, err := s.st.Q.GetScheduleRun(ctx, runID)
	if err != nil {
		// 定时已删除（运行随之级联删除）
		s.dropOpen(threadID, runID)
		return
	}
	s.applyState(ctx, run, threadID, state)
}

// applyState 按线程状态推进一次已投递（running / needs_input）的运行；pending 的运行不受影响。
func (s *Service) applyState(ctx context.Context, run db.ScheduleRun, threadID, state string) {
	if run.Status != runRunning && run.Status != runNeedsInput {
		return
	}
	switch apigen.ThreadState(state) {
	case apigen.ThreadStateIdle:
		s.dropOpen(threadID, run.ID)
		s.finishRun(ctx, run.ID, runCompleted, "")
		if err := s.st.Q.SetScheduleFailures(ctx, db.SetScheduleFailuresParams{ID: run.ScheduleID, Failures: 0}); err != nil {
			slog.Warn("schedule: 清零失败次数失败", "schedule", run.ScheduleID, "err", err)
		}
	case apigen.ThreadStateError:
		s.dropOpen(threadID, run.ID)
		s.finishRun(ctx, run.ID, runFailed, "thread_error")
		s.countFailure(ctx, run.ScheduleID)
	case apigen.ThreadStateAwaitingInput:
		if run.Status == runRunning {
			s.setRunStatus(ctx, run.ID, runNeedsInput)
		}
	case apigen.ThreadStateWorking:
		if run.Status == runNeedsInput {
			s.setRunStatus(ctx, run.ID, runRunning)
		}
	}
}

func (s *Service) dropOpen(threadID, runID string) {
	s.mu.Lock()
	if s.open[threadID] == runID {
		delete(s.open, threadID)
	}
	s.mu.Unlock()
}

func (s *Service) setRunStatus(ctx context.Context, runID, status string) {
	if err := s.st.Q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{ID: runID, Status: status}); err != nil {
		slog.Warn("schedule: 更新运行状态失败", "run", runID, "err", err)
	}
}

func (s *Service) finishRun(ctx context.Context, runID, status, reason string) {
	nowMs := s.cfg.Now().UnixMilli()
	if err := s.st.Q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{
		ID: runID, Status: status, FinishedAt: &nowMs, Error: optStr(reason),
	}); err != nil {
		slog.Warn("schedule: 更新运行状态失败", "run", runID, "err", err)
	}
}

// countFailure 计一次失败；达到 MaxFailures 且仍启用时自动暂停并通知。
func (s *Service) countFailure(ctx context.Context, scheduleID string) {
	sc, err := s.st.Q.GetSchedule(ctx, scheduleID)
	if err != nil {
		return
	}
	failures := sc.Failures + 1
	if failures < int64(s.cfg.MaxFailures) || !sc.Enabled {
		if err := s.st.Q.SetScheduleFailures(ctx, db.SetScheduleFailuresParams{ID: scheduleID, Failures: failures}); err != nil {
			slog.Warn("schedule: 记录失败次数失败", "schedule", scheduleID, "err", err)
		}
		return
	}
	if err := s.st.Q.SetScheduleEnabled(ctx, db.SetScheduleEnabledParams{
		ID: scheduleID, Enabled: false, PausedReason: new(pausedFailures), Failures: failures, UpdatedAt: s.cfg.Now().UnixMilli(),
	}); err != nil {
		slog.Warn("schedule: 自动暂停失败", "schedule", scheduleID, "err", err)
		return
	}
	slog.Info("schedule: 连续失败后自动暂停", "schedule", scheduleID, "failures", failures)
	threadID := deref(sc.ThreadID)
	title := ""
	if threadID == "" {
		title = fmt.Sprintf("Schedule %q", sc.Name)
	}
	s.post(ctx, threadID, title, fmt.Sprintf("Schedule %q was paused after %d consecutive failures", displayName(sc.Name), failures))
	s.changed(ctx, threadID)
}

func (s *Service) post(ctx context.Context, threadID, title, body string) {
	if s.n == nil {
		return
	}
	if err := s.n.Post(ctx, threadID, title, body); err != nil {
		slog.Warn("schedule: 发送通知失败", "thread", threadID, "err", err)
	}
}

// ---- 恢复 ----

// recover 在启动时核对未结束的运行：pending（重启前没等到应答 / 没建出线程）→ failed(interrupted)；
// 开始超过 24 小时 → failed(stale)；其余按线程现状结束或重新登记。
func (s *Service) recover(ctx context.Context) {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	runs, err := s.st.Q.ListOpenScheduleRuns(ctx)
	if err != nil {
		slog.Warn("schedule: 读取未结束运行失败", "err", err)
		return
	}
	now := s.cfg.Now()
	for _, r := range runs {
		threadID := deref(r.ThreadID)
		switch {
		case r.Status == runPending || threadID == "":
			s.finishRun(ctx, r.ID, runFailed, "interrupted")
			continue
		case now.Sub(time.UnixMilli(r.StartedAt)) > staleAfter:
			s.finishRun(ctx, r.ID, runFailed, "stale")
			continue
		}
		t, err := s.th.Get(ctx, threadID)
		if err != nil {
			s.finishRun(ctx, r.ID, runFailed, "interrupted")
			continue
		}
		s.mu.Lock()
		s.open[threadID] = r.ID
		s.mu.Unlock()
		s.applyState(ctx, r, threadID, string(t.State))
	}
}

// ---- 文本 ----

// header 是投递正文的首行。
func header(fp fireParams, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Oryn scheduled run %q #%d · scheduled for %s (%s)", displayName(fp.sc.Name), fp.run,
		time.UnixMilli(fp.scheduledFor).In(loc).Format(time.RFC3339), fp.sc.Timezone)
	if fp.trigger == triggerCatchup {
		fmt.Fprintf(&b, " · late, %d missed runs coalesced", fp.missed)
	}
	b.WriteString("]")
	return b.String()
}

// expand 替换提示词里的占位符；其它花括号原样保留。
func expand(fp fireParams, now time.Time, loc *time.Location) string {
	last := ""
	if fp.sc.LastRunAt != nil {
		last = time.UnixMilli(*fp.sc.LastRunAt).In(loc).Format(time.RFC3339)
	}
	local := now.In(loc)
	return strings.NewReplacer(
		"{{now}}", local.Format(time.RFC3339),
		"{{date}}", local.Format("2006-01-02"),
		"{{scheduled_at}}", time.UnixMilli(fp.scheduledFor).In(loc).Format(time.RFC3339),
		"{{run_number}}", strconv.FormatInt(fp.run, 10),
		"{{last_run_at}}", last,
	).Replace(fp.sc.Prompt)
}

func displayName(name string) string {
	if name == "" {
		return "untitled"
	}
	return name
}

func errText(err error) string {
	var te *threads.Error
	if errors.As(err, &te) {
		if te.Message == "" {
			return te.Code
		}
		return te.Code + ": " + te.Message
	}
	return err.Error()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
