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

// runTagPrefix 是定时运行 id 的前缀，也是带标签投递里定时的标签前缀（契约 C1）。
const runTagPrefix = "srn_"

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
// 全程持 loopMu 直到 armLocked 返回（契约 C8）：否则读到的旧的最早触发时间会停掉并发修改刚上的定时器。
func (s *Service) Tick(ctx context.Context) {
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	s.processEvents(ctx)
	s.tickLocked(ctx)
	s.processEvents(ctx)
	s.armLocked(ctx)
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

// armLocked 按最早的 next_run_at 上定时器（封顶 1 小时）。
// 前提：调用方持有 loopMu（契约 C8）——「读最早触发时间 + 替换定时器」只在这一个串行区里发生，
// 定时行的写入也都在 loopMu 下，读到的值不会在替换之前过期。
func (s *Service) armLocked(ctx context.Context) {
	next, err := s.st.Q.MinNextRun(ctx)
	if err != nil {
		slog.Warn("schedule: 读取下一次触发失败", "err", err)
		return
	}
	if s.afterMinNext != nil {
		s.afterMinNext()
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
		s.queue = append(s.queue, event{kind: evState, threadID: ev.ThreadID, state: ev.State, promptError: ev.PromptError})
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

// TurnEnded 实现 threads.Observer：只入队定时运行的标签（契约 C1.5）。
func (s *Service) TurnEnded(ev threads.TurnEvent) {
	if !strings.HasPrefix(ev.Tag, runTagPrefix) {
		return
	}
	s.enqueue(event{kind: evTurn, runID: ev.Tag, threadID: ev.ThreadID, turn: ev})
}

// EntriesAppended 实现 threads.Observer：线程上有等可验证终态的恢复运行时入队重查（补充契约 C1.7）。
// O(1)，不读库。
func (s *Service) EntriesAppended(threadID string) {
	s.mu.Lock()
	rr := s.recovered[s.open[threadID]]
	ok := rr != nil && rr.waiting
	if ok {
		s.queue = append(s.queue, event{kind: evRecheck, threadID: threadID})
	}
	s.mu.Unlock()
	if ok {
		signal(s.wake)
	}
}

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
			s.onState(ctx, ev.threadID, ev.state, ev.promptError)
		case evAck:
			s.onAck(ctx, ev.runID, ev.err)
		case evTimeout:
			s.onTimeout(ctx, ev.runID)
		case evTurn:
			s.onTurn(ctx, ev.turn)
		case evRecheck:
			s.onRecheck(ctx, ev.threadID)
		case evSettleTimeout:
			s.onSettleTimeout(ctx, ev.runID)
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
	advanced := false
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
		advanced = true
		if status == runPending {
			fp = &fireParams{sc: sc, runID: runID, run: sc.RunCount + 1, scheduledFor: scheduledFor, trigger: trigger, missed: missed}
		}
		return nil
	})
	if err != nil {
		slog.Warn("schedule: 触发失败", "schedule", id, "err", err)
		return
	}
	if !advanced {
		return
	}
	if threadID != "" {
		s.th.Publish(ctx, threadID)
	}
	s.notifyChanged(id)
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
	s.notifyChanged(id)
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

// deliver 以运行 id 为标签投递（契约 C1.5）：终态由 TurnEnded 结算。
func (s *Service) deliver(ctx context.Context, fp fireParams, text string) {
	threadID := deref(fp.sc.ThreadID)
	d := &delivery{runID: fp.runID, scheduleID: fp.sc.ID, threadID: threadID}
	s.mu.Lock()
	s.open[threadID] = fp.runID
	s.pending[fp.runID] = d
	s.tagged[fp.runID] = fp.sc.ID
	s.mu.Unlock()
	cancel, err := s.th.Deliver(ctx, threadID, threads.AutoMessage{Text: text, Tag: fp.runID}, func(err error) {
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

// deliveryFailed 记一次投递失败（同步错误 / 应答出错 / 超时 / 未开始就失败的回合）：运行 failed、通知、计入失败次数。
func (s *Service) deliveryFailed(ctx context.Context, sc db.Schedule, threadID, runID, reason string) {
	reason = truncate(reason, maxRunError)
	s.finishRun(ctx, sc.ID, runID, runFailed, reason)
	s.post(ctx, threadID, "", fmt.Sprintf("Scheduled run %q could not be delivered: %s", displayName(sc.Name), reason))
	s.countFailure(ctx, sc.ID)
}

// createThread 以运行 id 为标签新建线程（契约 C1.5）：首条提示词的终态由 TurnEnded 结算。
func (s *Service) createThread(ctx context.Context, fp fireParams, loc *time.Location, text string) {
	sc := fp.sc
	p := threads.CreateParams{
		ProjectID: deref(sc.ProjectID), Prompt: text, Provider: deref(sc.Provider), Type: deref(sc.OrbType),
		Model: parseModel(sc.Model), ThinkingLevel: deref(sc.ThinkingLevel), SpaceID: deref(sc.SpaceID),
		Title:      sc.Name + " · " + time.UnixMilli(fp.scheduledFor).In(loc).Format("01-02 15:04"),
		ScheduleID: sc.ID, ScheduleRun: fp.run, Tag: fp.runID,
	}
	s.mu.Lock()
	s.creating++
	s.tagged[fp.runID] = sc.ID
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
		s.finishRun(ctx, sc.ID, fp.runID, runFailed, reason)
		s.post(ctx, "", fmt.Sprintf("Schedule %q", sc.Name), "Scheduled run could not start a thread: "+reason)
		s.countFailure(ctx, sc.ID)
		return
	}
	if err := s.st.Q.SetScheduleRunThread(ctx, db.SetScheduleRunThreadParams{ID: fp.runID, ThreadID: &t.ID}); err != nil {
		slog.Warn("schedule: 记录运行线程失败", "run", fp.runID, "err", err)
	}
	s.markDelivered(ctx, sc.ID, fp.runID)
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
		// 已结算（TurnEnded 先到、超时撤回）或定时已删除
		return
	}
	if err != nil {
		sc, gerr := s.st.Q.GetSchedule(ctx, d.scheduleID)
		if gerr != nil {
			return
		}
		// 随后到达的 TurnEnded{error} 因运行已结束被忽略：只计一次失败
		s.deliveryFailed(ctx, sc, d.threadID, runID, errText(err))
		return
	}
	s.markDelivered(ctx, d.scheduleID, runID)
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

func (s *Service) onState(ctx context.Context, threadID, state string, promptError bool) {
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
		s.forgetRun(runID)
		return
	}
	s.applyState(ctx, run, threadID, state, promptError)
}

// applyState 按线程状态推进一次已投递（running / needs_input）的运行；pending 的运行不受影响。
// 线程的 idle 只是候选（契约 C1.5 / C1.6）：带标签的运行等 TurnEnded，恢复运行按转录推断。
func (s *Service) applyState(ctx context.Context, run db.ScheduleRun, threadID, state string, promptError bool) {
	if run.Status != runRunning && run.Status != runNeedsInput {
		return
	}
	s.mu.Lock()
	_, tagged := s.tagged[run.ID]
	rr := s.recovered[run.ID]
	s.mu.Unlock()
	switch apigen.ThreadState(state) {
	case apigen.ThreadStateIdle:
		if rr != nil {
			s.tryRecovered(ctx, run, rr)
		}
	case apigen.ThreadStateError:
		switch {
		case promptError && tagged:
			// 只是 prompt_result 报错：同一回合的 TurnEnded 会带上原因
		case promptError && rr != nil:
			s.tryRecovered(ctx, run, rr)
		default:
			s.dropOpen(threadID, run.ID)
			s.finishRun(ctx, run.ScheduleID, run.ID, runFailed, "thread_error")
			s.countFailure(ctx, run.ScheduleID)
		}
	case apigen.ThreadStateAwaitingInput:
		if run.Status == runRunning {
			s.setRunStatus(ctx, run.ScheduleID, run.ID, runNeedsInput)
		}
	case apigen.ThreadStateWorking:
		if run.Status == runNeedsInput {
			s.setRunStatus(ctx, run.ScheduleID, run.ID, runRunning)
		}
	}
}

// onTurn 结算一次带标签投递的终态（契约 C1.5）。
func (s *Service) onTurn(ctx context.Context, ev threads.TurnEvent) {
	run, err := s.st.Q.GetScheduleRun(ctx, ev.Tag)
	if err != nil {
		s.forgetRun(ev.Tag)
		return
	}
	switch run.Status {
	case runPending, runRunning, runNeedsInput:
	default:
		s.forgetRun(run.ID)
		return
	}
	threadID := ev.ThreadID
	if threadID == "" {
		threadID = deref(run.ThreadID)
	}
	s.settle(ctx, run, threadID, ev)
}

// settle 按回合终态结束一次未结束的运行（契约 C1.5 的表）。
func (s *Service) settle(ctx context.Context, run db.ScheduleRun, threadID string, ev threads.TurnEvent) {
	s.mu.Lock()
	if d := s.pending[run.ID]; d != nil {
		if d.stop != nil {
			d.stop()
		}
		delete(s.pending, run.ID)
	}
	s.mu.Unlock()
	s.dropOpen(threadID, run.ID)
	switch ev.Status {
	case threads.TurnCompleted:
		s.finishRun(ctx, run.ScheduleID, run.ID, runCompleted, "")
		if err := s.st.Q.SetScheduleFailures(ctx, db.SetScheduleFailuresParams{ID: run.ScheduleID, Failures: 0}); err != nil {
			slog.Warn("schedule: 清零失败次数失败", "schedule", run.ScheduleID, "err", err)
		}
	case threads.TurnAborted:
		s.finishRun(ctx, run.ScheduleID, run.ID, runFailed, "aborted")
	case threads.TurnError:
		reason := ev.Error
		if reason == "" {
			reason = "thread_error"
		}
		if run.Status == runPending {
			if sc, err := s.st.Q.GetSchedule(ctx, run.ScheduleID); err == nil {
				s.deliveryFailed(ctx, sc, threadID, run.ID, reason)
				return
			}
		}
		s.finishRun(ctx, run.ScheduleID, run.ID, runFailed, reason)
		s.countFailure(ctx, run.ScheduleID)
	default:
		s.finishRun(ctx, run.ScheduleID, run.ID, runFailed, "outcome_unknown")
	}
}

// tryRecovered 用转录推断恢复运行的终态（补充契约 C1.6）：推断得出就结算；推断不出（转录复制没赶上、回合未结束）
// 就保持未结束，置 waiting 等下一批条目入库或下一次状态事件，并在第一次推断不出时上兜底超时。调用方持有 loopMu。
func (s *Service) tryRecovered(ctx context.Context, run db.ScheduleRun, rr *recoveredRun) {
	ev, ok, err := s.th.RecoveredOutcomeMatch(ctx, rr.threadID, rr.match, rr.since)
	if err != nil {
		slog.Warn("schedule: 推断恢复运行的终态失败", "run", run.ID, "err", err)
		ok = false
	}
	if ok {
		ev.ThreadID = rr.threadID
		s.settle(ctx, run, rr.threadID, ev)
		return
	}
	s.mu.Lock()
	first := !rr.waiting
	rr.waiting = true
	s.mu.Unlock()
	if !first {
		return
	}
	runID := run.ID
	stop := s.cfg.AfterFunc(recoverSettleTimeout, func() { s.enqueue(event{kind: evSettleTimeout, runID: runID}) })
	s.mu.Lock()
	rr.stop = stop
	s.mu.Unlock()
}

// onRecheck 在恢复运行所在线程有新条目入库后重查一次（补充契约 C1.7）。
func (s *Service) onRecheck(ctx context.Context, threadID string) {
	s.mu.Lock()
	runID := s.open[threadID]
	rr := s.recovered[runID]
	s.mu.Unlock()
	if rr == nil || !rr.waiting {
		return
	}
	run, err := s.st.Q.GetScheduleRun(ctx, runID)
	if err != nil {
		s.dropOpen(threadID, runID)
		s.forgetRun(runID)
		return
	}
	if run.Status != runRunning && run.Status != runNeedsInput {
		return
	}
	s.tryRecovered(ctx, run, rr)
}

// onSettleTimeout 是恢复运行的兜底：最后再推断一次，仍推断不出就记 outcome_unknown（不计连续失败）。
func (s *Service) onSettleTimeout(ctx context.Context, runID string) {
	s.mu.Lock()
	rr := s.recovered[runID]
	s.mu.Unlock()
	if rr == nil {
		return
	}
	run, err := s.st.Q.GetScheduleRun(ctx, runID)
	if err != nil {
		s.dropOpen(rr.threadID, runID)
		s.forgetRun(runID)
		return
	}
	if run.Status != runRunning && run.Status != runNeedsInput {
		return
	}
	ev, ok, err := s.th.RecoveredOutcomeMatch(ctx, rr.threadID, rr.match, rr.since)
	if err == nil && ok {
		ev.ThreadID = rr.threadID
		s.settle(ctx, run, rr.threadID, ev)
		return
	}
	s.dropOpen(rr.threadID, runID)
	s.finishRun(ctx, run.ScheduleID, runID, runFailed, "outcome_unknown")
}

func (s *Service) dropOpen(threadID, runID string) {
	s.mu.Lock()
	if s.open[threadID] == runID {
		delete(s.open, threadID)
	}
	s.mu.Unlock()
}

// forgetRun 丢掉一次运行的标签与恢复登记（并撤掉其兜底超时）。
func (s *Service) forgetRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tagged, runID)
	if rr := s.recovered[runID]; rr != nil {
		if rr.stop != nil {
			rr.stop()
		}
		delete(s.recovered, runID)
	}
}

func (s *Service) setRunStatus(ctx context.Context, scheduleID, runID, status string) {
	if err := s.st.Q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{ID: runID, Status: status}); err != nil {
		slog.Warn("schedule: 更新运行状态失败", "run", runID, "err", err)
	}
	s.notifyChanged(scheduleID)
}

// markDelivered 记运行已下发（running + delivered_at）。
func (s *Service) markDelivered(ctx context.Context, scheduleID, runID string) {
	nowMs := s.cfg.Now().UnixMilli()
	if err := s.st.Q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{ID: runID, Status: runRunning, DeliveredAt: &nowMs}); err != nil {
		slog.Warn("schedule: 更新运行状态失败", "run", runID, "err", err)
	}
	s.notifyChanged(scheduleID)
}

// finishRun 结束一次运行（错误文本截到 maxRunError），并丢掉它在内存里的标签与恢复登记。
func (s *Service) finishRun(ctx context.Context, scheduleID, runID, status, reason string) {
	s.forgetRun(runID)
	nowMs := s.cfg.Now().UnixMilli()
	if err := s.st.Q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{
		ID: runID, Status: status, FinishedAt: &nowMs, Error: optStr(truncate(reason, maxRunError)),
	}); err != nil {
		slog.Warn("schedule: 更新运行状态失败", "run", runID, "err", err)
	}
	s.notifyChanged(scheduleID)
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
	s.changed(ctx, scheduleID, threadID)
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
// 开始超过 24 小时 → failed(stale)；其余重新登记为恢复运行，终态按已入库的转录推断（补充契约 C1.6）。
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
			s.finishRun(ctx, r.ScheduleID, r.ID, runFailed, "interrupted")
			continue
		case now.Sub(time.UnixMilli(r.StartedAt)) > staleAfter:
			s.finishRun(ctx, r.ScheduleID, r.ID, runFailed, "stale")
			continue
		}
		t, err := s.th.Get(ctx, threadID)
		if err != nil {
			s.finishRun(ctx, r.ScheduleID, r.ID, runFailed, "interrupted")
			continue
		}
		// 锚点（runAnchor）：头行以固定开头起始，且其中 `scheduled for` 的时刻按秒等于本运行的 ScheduledFor。
		// 不用名称（可改，旧消息仍是旧名字），也不依赖定时现在的时区（RFC3339 带偏移，解析出来就是绝对时刻）。
		// 上一个运行的消息迟到（例如它因 orb 失败被记 thread_error 时消息还压在控制面队列里，之后才下发）、
		// 在 since 之后先于本运行的消息出现时，它的 scheduled for 不同，不会被当作锚点。
		// since 取运行开始时刻：投递的应答（delivered_at）可能晚于 omp 写下该用户消息的时间戳，开始时刻一定不晚于它。
		// 残余：同一线程上两个运行的 ScheduledFor 落在同一秒内（线程内定时同一时刻至多一个未结束运行，
		// 手动触发要等上一个结束，实际不会出现）。
		rr := &recoveredRun{scheduleID: r.ScheduleID, threadID: threadID, match: runAnchor(r.ScheduledFor), since: r.StartedAt}
		s.mu.Lock()
		s.open[threadID] = r.ID
		s.recovered[r.ID] = rr
		s.mu.Unlock()
		if apigen.ThreadState(t.State) != apigen.ThreadStateError {
			s.applyState(ctx, r, threadID, string(t.State), false)
			continue
		}
		// 重启时看不出 error 是否只是 prompt 报错：转录推断得出终态就按它结算，否则按线程出错计
		ev, ok, err := s.th.RecoveredOutcomeMatch(ctx, threadID, rr.match, rr.since)
		if err == nil && ok {
			ev.ThreadID = threadID
			s.settle(ctx, r, threadID, ev)
			continue
		}
		s.applyState(ctx, r, threadID, string(t.State), false)
	}
}

// ---- 文本 ----

// headerFixed 是投递正文头行里名称之前的固定开头；headerAt 引出头行里的计划触发时刻（RFC3339，精确到秒）。
const (
	headerFixed = "[Oryn scheduled run "
	headerAt    = " · scheduled for "
)

// runAnchor 返回认出某次运行所投递头行的匹配函数（补充契约 C1.6）：正文以 headerFixed 开头，首行最后一个
// headerAt 之后到下一个空格的文本按 RFC3339 解析，按秒等于 scheduledFor（头行只写到秒，手动触发的
// scheduledFor 可能带毫秒，所以比较 Unix 秒）。名称经 %q 包裹且在前面，LastIndex 只会落在真正的字段上。
func runAnchor(scheduledFor int64) func(string) bool {
	want := time.UnixMilli(scheduledFor).Unix()
	return func(text string) bool {
		if !strings.HasPrefix(text, headerFixed) {
			return false
		}
		line, _, _ := strings.Cut(text, "\n")
		i := strings.LastIndex(line, headerAt)
		if i < 0 {
			return false
		}
		at := line[i+len(headerAt):]
		if j := strings.IndexByte(at, ' '); j >= 0 {
			at = at[:j]
		}
		t, err := time.Parse(time.RFC3339, at)
		return err == nil && t.Unix() == want
	}
}

// header 是投递正文的首行；agent 定义的定时在右括号前加 ` · defined by an agent`（契约 C9.3）。
func header(fp fireParams, loc *time.Location) string {
	var b strings.Builder
	b.WriteString(headerFixed)
	fmt.Fprintf(&b, "%q #%d%s%s (%s)", displayName(fp.sc.Name), fp.run, headerAt,
		time.UnixMilli(fp.scheduledFor).In(loc).Format(time.RFC3339), fp.sc.Timezone)
	if fp.trigger == triggerCatchup {
		fmt.Fprintf(&b, " · late, %d missed runs coalesced", fp.missed)
	}
	if fp.sc.CreatedBy == createdByAgent {
		b.WriteString(" · defined by an agent")
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
