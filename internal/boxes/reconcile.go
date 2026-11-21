package boxes

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// failedRetry 是同一台 box 在 failed 时两次重试唤醒之间的最短间隔。
	failedRetry = 60 * time.Second
	// scheduleGrace 是时段停机在 box 忙（有工作中的线程或未结束的部署）时的最长推迟。
	scheduleGrace = 30 * time.Minute
	// orphanAfter 是 box orb 创建之后仍没有 box 行、被当作孤儿删除的时限。
	orphanAfter = time.Minute
)

// Run 运行对账循环：先收拾控制面重启前留下的部署，然后每 Tick 一次，另由 box orb 的事件与写操作立即触发；
// ctx 结束时返回，并中止本进程里在途的部署。
func (s *Service) Run(ctx context.Context) {
	defer s.stop()
	s.sweepDeploys(ctx, true)
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		s.reconcile(ctx)
		s.maybeProbePreviews(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kickCh:
		}
	}
}

// reconcile 做一轮对账：自愈、收拾无主的部署、时段、把每台 box 的 orb 推向期望状态。各台 box 串行处理。
func (s *Service) reconcile(ctx context.Context) {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.healOrphans(ctx)
	s.sweepDeploys(ctx, false)
	boxes, err := s.st.Q.ListBoxes(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("boxes: 对账读取 box 失败", "err", err)
		}
		return
	}
	for _, b := range boxes {
		if ctx.Err() != nil {
			return
		}
		if err := s.reconcileBox(ctx, b); err != nil && ctx.Err() == nil {
			slog.Warn("boxes: 对账失败", "box", b.ID, "name", b.Name, "err", err)
		}
	}
}

// maybeProbePreviews 每 previewEvery 在后台对全部 live app 探测一次预览（上一轮没跑完就跳过）。
func (s *Service) maybeProbePreviews(ctx context.Context) {
	now := s.now()
	if now.Sub(s.lastPreview) < previewEvery || !s.previewing.CompareAndSwap(false, true) {
		return
	}
	s.lastPreview = now
	go func() {
		defer s.previewing.Store(false)
		s.probePreviews(ctx)
	}()
}

// healOrphans 删掉创建超过 orphanAfter 仍没有 box 行的 box orb（Create 写 box 行失败、回收也失败时留下的）。
func (s *Service) healOrphans(ctx context.Context) {
	orphans, err := s.st.Q.ListOrphanBoxOrbs(ctx, s.now().Add(-orphanAfter).UnixMilli())
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("boxes: 读取孤儿 box orb 失败", "err", err)
		}
		return
	}
	for _, o := range orphans {
		if gone(o) {
			continue
		}
		slog.Info("boxes: 删除孤儿 box orb", "orb", o.ID)
		if err := s.orbs.Delete(ctx, o.ID); err != nil && !errors.Is(err, orbs.ErrNotFound) {
			slog.Warn("boxes: 删除孤儿 box orb 失败", "orb", o.ID, "err", err)
		}
	}
}

// reconcileBox 对一台 box 做对账。
func (s *Service) reconcileBox(ctx context.Context, b db.Box) error {
	o, err := s.orbs.Get(ctx, b.OrbID)
	if errors.Is(err, orbs.ErrNotFound) || (err == nil && gone(o)) {
		// Delete 在删 orb 与删 box 行之间中断过：补完
		if _, err := s.st.Q.DeleteBox(ctx, b.ID); err != nil {
			return err
		}
		s.forget(b.ID)
		s.changed()
		return nil
	}
	if err != nil {
		return err
	}
	if b, err = s.applySchedule(ctx, b); err != nil {
		return err
	}
	now := s.now()
	if b.Desired == desiredRunning {
		return s.keepRunning(ctx, b, o, now)
	}
	s.mu.Lock()
	delete(s.unlinkedSince, b.ID)
	s.mu.Unlock()
	if o.State != orbs.StateReady && o.State != orbs.StateFrozen {
		return nil
	}
	if b.DesiredBy == bySchedule && now.Before(time.UnixMilli(b.DesiredAt).Add(scheduleGrace)) {
		busy, err := s.busy(ctx, b)
		if err != nil {
			return err
		}
		if busy {
			// 时段停机等手头的工作做完，至多推迟到边界之后 scheduleGrace
			return nil
		}
	}
	slog.Info("boxes: 停机", "box", b.ID, "name", b.Name, "by", b.DesiredBy)
	return s.orbs.Pause(ctx, o.ID, orbs.PauseStop)
}

// keepRunning 把期望运行的 box 的 orb 推向 ready。
func (s *Service) keepRunning(ctx context.Context, b db.Box, o db.Orb, now time.Time) error {
	if o.State != orbs.StateReady {
		s.mu.Lock()
		delete(s.unlinkedSince, b.ID)
		s.mu.Unlock()
	}
	switch o.State {
	case orbs.StateStopped, orbs.StateFrozen, orbs.StateStopping, orbs.StateFreezing:
		slog.Info("boxes: 唤醒", "box", b.ID, "name", b.Name, "state", o.State)
		return s.orbs.Wake(ctx, o.ID)
	case orbs.StateFailed:
		s.mu.Lock()
		last, tried := s.lastWake[b.ID]
		if tried && now.Sub(last) < failedRetry {
			s.mu.Unlock()
			return nil
		}
		s.lastWake[b.ID] = now
		s.mu.Unlock()
		slog.Info("boxes: 重试唤醒失败的 box", "box", b.ID, "name", b.Name)
		return s.orbs.Wake(ctx, o.ID)
	case orbs.StateReady:
		if s.orbs.Connected(o.ID) {
			s.mu.Lock()
			delete(s.unlinkedSince, b.ID)
			s.mu.Unlock()
			return nil
		}
		s.mu.Lock()
		since, ok := s.unlinkedSince[b.ID]
		if !ok {
			// 首次观察到时起算；控制面重启后内存是空的，于是从启动后的第一轮对账起算
			since = now
			s.unlinkedSince[b.ID] = since
		}
		due := now.Sub(since) >= s.cfg.UnlinkedAfter
		if due {
			delete(s.unlinkedSince, b.ID)
		}
		s.mu.Unlock()
		if !due {
			return nil
		}
		slog.Info("boxes: ready 却没有 link，重新拉起", "box", b.ID, "name", b.Name, "since", since)
		return s.orbs.Revive(ctx, o.ID)
	}
	return nil
}

// busy 报告 box 上是否有工作中的线程或未结束的部署。
func (s *Service) busy(ctx context.Context, b db.Box) (bool, error) {
	if s.th != nil {
		busy, err := s.th.Busy(ctx, b.OrbID)
		if err != nil || busy {
			return busy, err
		}
	}
	open, err := s.st.Q.ListOpenBoxDeploysByBox(ctx, b.ID)
	return len(open) > 0, err
}

// applySchedule 按时段更新期望状态：不晚于现在的最近一个边界晚于 desired_at 时，期望状态改成边界的状态
// （desired_by = schedule、desired_at = 边界时刻）。手动启停因此只保持到下一个边界。返回更新后的行。
func (s *Service) applySchedule(ctx context.Context, b db.Box) (db.Box, error) {
	if b.SchedOn == nil || b.SchedOff == nil || b.SchedTz == nil {
		return b, nil
	}
	loc, err := time.LoadLocation(*b.SchedTz)
	if err != nil {
		slog.Warn("boxes: 时段的时区无效", "box", b.ID, "tz", *b.SchedTz, "err", err)
		return b, nil
	}
	at, desired, ok := lastBoundary(s.now(), *b.SchedOn, *b.SchedOff, loc)
	if !ok || at.UnixMilli() <= b.DesiredAt {
		return b, nil
	}
	if s.hookBeforeScheduleTx != nil {
		s.hookBeforeScheduleTx()
	}
	// 读当前行、复核边界、写期望状态与活动在同一事务里：并发的手动启停或改 / 清时段不会被这次判定覆盖
	cur, applied := b, false
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		r, err := q.GetBox(ctx, b.ID)
		if err != nil {
			return err
		}
		cur = r
		if !sameSchedule(r, b) || at.UnixMilli() <= r.DesiredAt {
			return nil
		}
		if _, err := q.SetBoxDesired(ctx, db.SetBoxDesiredParams{Desired: desired, DesiredBy: bySchedule, DesiredAt: at.UnixMilli(), ID: b.ID}); err != nil {
			return err
		}
		applied = true
		if desired == r.Desired {
			return nil
		}
		kind := apigen.BoxActivityStarted
		if desired == desiredStopped {
			kind = apigen.BoxActivityStopped
		}
		return s.record(ctx, q, b.ID, string(kind), Actor{Kind: ActorSchedule}, "", activity{})
	})
	if err != nil {
		return b, err
	}
	if !applied {
		return cur, nil
	}
	changed := desired != cur.Desired
	cur.Desired, cur.DesiredBy, cur.DesiredAt = desired, bySchedule, at.UnixMilli()
	if changed {
		slog.Info("boxes: 时段切换", "box", b.ID, "name", b.Name, "desired", desired)
		s.changed()
	}
	return cur, nil
}

// sameSchedule 报告两行的时段三列是否都非空且相同。
func sameSchedule(a, b db.Box) bool {
	eq := func(x, y *string) bool { return x != nil && y != nil && *x == *y }
	return eq(a.SchedOn, b.SchedOn) && eq(a.SchedOff, b.SchedOff) && eq(a.SchedTz, b.SchedTz)
}

// lastBoundary 返回在 loc 下不晚于 now 的最近一个时段边界与它对应的期望状态（on → running，off → stopped）：
// 今天与昨天的 on、off 四个时刻里不晚于 now 的最大者。on / off 解析失败时 ok = false。
func lastBoundary(now time.Time, on, off string, loc *time.Location) (at time.Time, desired string, ok bool) {
	onH, onM, ok1 := parseHHMM(on)
	offH, offM, ok2 := parseHHMM(off)
	if !ok1 || !ok2 {
		return time.Time{}, "", false
	}
	local := now.In(loc)
	y, m, d := local.Date()
	for _, day := range []int{d, d - 1} {
		for _, c := range []struct {
			h, min int
			state  string
		}{{onH, onM, desiredRunning}, {offH, offM, desiredStopped}} {
			t := time.Date(y, m, day, c.h, c.min, 0, 0, loc)
			if t.After(now) {
				continue
			}
			if !ok || t.After(at) {
				at, desired, ok = t, c.state, true
			}
		}
	}
	return at, desired, ok
}

// parseHHMM 解析 `HH:MM`。
func parseHHMM(v string) (h, m int, ok bool) {
	if !hhmmRe.MatchString(v) {
		return 0, 0, false
	}
	return int(v[0]-'0')*10 + int(v[1]-'0'), int(v[3]-'0')*10 + int(v[4]-'0'), true
}
