package sandboxes

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ownerGone 是 owner orb 离场的状态：进入其一即销毁它拥有的沙箱（冻结不算）。
var ownerGone = []string{orbs.StateStopping, orbs.StateStopped, orbs.StateDeleting, orbs.StateDeleted, orbs.StateFailed}

// Run 跑对账循环（启动时先跑一次）并处理 orb 事件，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.Reconcile(ctx)
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-s.events:
			s.onEvent(ctx, ev)
		case <-t.C:
			s.Reconcile(ctx)
		}
	}
}

// ThreadRetired 在线程归档（deleted=false）或删除（deleted=true）之后调用：异步销毁它名下在用的沙箱。
func (s *Service) ThreadRetired(threadID string, deleted bool) {
	reason := ReasonThreadArchived
	if deleted {
		reason = ReasonThreadDeleted
	}
	go func() {
		ctx := context.Background()
		since := s.now().UnixMilli()
		rows, err := s.st.Q.ListSandboxesByThread(ctx, db.ListSandboxesByThreadParams{OwnerThreadID: threadID, Since: &since})
		if err != nil {
			slog.Warn("sandboxes: 列出线程的沙箱失败，对账兜底", "thread", threadID, "err", err)
			return
		}
		s.destroyLive(ctx, rows, reason)
	}()
}

// destroyLive 销毁 rows 里 creating / ready 的沙箱。
func (s *Service) destroyLive(ctx context.Context, rows []db.Sandbox, reason string) {
	for _, sb := range rows {
		if sb.State != stateCreating && sb.State != stateReady {
			continue
		}
		if err := s.destroy(ctx, sb, reason); err != nil {
			slog.Warn("sandboxes: 销毁沙箱失败，对账兜底", "sandbox", sb.ID, "reason", reason, "err", err)
		}
	}
}

// onEvent 处理一条 orb 状态事件：owner orb 离场 → 销毁它的沙箱；沙箱 orb 到达 deleted → 行置为 destroyed；
// 沙箱 orb 在 ready 之后失败 → 按 lost 销毁。
func (s *Service) onEvent(ctx context.Context, ev orbs.Event) {
	if orbs.IsSandbox(ev.Orb) {
		sb, err := s.st.Q.GetSandboxByOrb(ctx, ev.OrbID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				slog.Warn("sandboxes: 读取沙箱行失败", "orb", ev.OrbID, "err", err)
			}
			return
		}
		switch {
		case ev.To == orbs.StateDeleted:
			s.markDestroyed(ctx, sb)
		case ev.To == orbs.StateFailed && sb.State == stateReady:
			if err := s.destroy(ctx, sb, ReasonLost); err != nil {
				slog.Warn("sandboxes: 销毁失联沙箱失败", "sandbox", sb.ID, "err", err)
			}
		}
		return
	}
	if !slices.Contains(ownerGone, ev.To) {
		return
	}
	since := s.now().UnixMilli()
	rows, err := s.st.Q.ListSandboxesByOwnerOrb(ctx, db.ListSandboxesByOwnerOrbParams{OwnerOrbID: ev.OrbID, Since: &since})
	if err != nil {
		slog.Warn("sandboxes: 列出 orb 的沙箱失败，对账兜底", "orb", ev.OrbID, "err", err)
		return
	}
	s.destroyLive(ctx, rows, ReasonOwnerOrbGone)
}

// markDestroyed 把 destroying 的行置为 destroyed（orb 已删除）。
func (s *Service) markDestroyed(ctx context.Context, sb db.Sandbox) {
	now := s.now().UnixMilli()
	n, err := s.st.Q.MarkSandboxDestroyed(ctx, db.MarkSandboxDestroyedParams{Now: &now, ID: sb.ID})
	if err != nil {
		slog.Warn("sandboxes: 标记沙箱已销毁失败", "sandbox", sb.ID, "err", err)
		return
	}
	if n > 0 {
		s.changed(sb.OwnerThreadID)
	}
}

// Reconcile 对账一轮：清理孤儿沙箱 orb、推进销毁、执行级联与各种到期，并定期裁剪旧行。
func (s *Service) Reconcile(ctx context.Context) {
	now := s.now()
	orphans, err := s.st.Q.ListOrphanSandboxOrbs(ctx, now.Add(-orphanGrace).UnixMilli())
	if err != nil {
		slog.Warn("sandboxes: 列出孤儿沙箱 orb 失败", "err", err)
	}
	for _, o := range orphans {
		slog.Info("sandboxes: 删除孤儿沙箱 orb", "orb", o.ID)
		if err := s.orbs.Delete(ctx, o.ID); err != nil && !errors.Is(err, orbs.ErrNotFound) {
			slog.Warn("sandboxes: 删除孤儿沙箱 orb 失败", "orb", o.ID, "err", err)
		}
	}
	live, err := s.st.Q.ListLiveSandboxes(ctx)
	if err != nil {
		slog.Warn("sandboxes: 列出在用沙箱失败", "err", err)
		return
	}
	for _, sb := range live {
		if ctx.Err() != nil {
			return
		}
		s.reconcileOne(ctx, sb, now)
	}
	if now.Sub(s.lastTrim) >= trimEvery {
		s.lastTrim = now
		before := now.Add(-trimAfter).UnixMilli()
		if _, err := s.st.Q.TrimSandboxes(ctx, &before); err != nil {
			slog.Warn("sandboxes: 裁剪旧行失败", "err", err)
		}
	}
}

func (s *Service) reconcileOne(ctx context.Context, sb db.Sandbox, now time.Time) {
	destroy := func(reason string) {
		if err := s.destroy(ctx, sb, reason); err != nil {
			slog.Warn("sandboxes: 销毁沙箱失败", "sandbox", sb.ID, "reason", reason, "err", err)
		}
	}
	o, err := s.orbs.Get(ctx, sb.OrbID)
	orbMissing := errors.Is(err, orbs.ErrNotFound)
	if err != nil && !orbMissing {
		slog.Warn("sandboxes: 读取沙箱 orb 失败", "sandbox", sb.ID, "err", err)
		return
	}
	if sb.State == stateDestroying {
		switch {
		case orbMissing || o.State == orbs.StateDeleted:
			s.markDestroyed(ctx, sb)
		case o.State != orbs.StateDeleting:
			if err := s.orbs.Delete(ctx, sb.OrbID); err != nil && !errors.Is(err, orbs.ErrNotFound) {
				slog.Warn("sandboxes: 重试删除沙箱 orb 失败", "sandbox", sb.ID, "err", err)
			}
		}
		return
	}

	// 级联：归属线程与 owner orb
	t, err := s.st.Q.GetThread(ctx, sb.OwnerThreadID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		destroy(ReasonThreadDeleted)
		return
	case err != nil:
		slog.Warn("sandboxes: 读取归属线程失败", "sandbox", sb.ID, "err", err)
		return
	case t.ArchivedAt != nil:
		destroy(ReasonThreadArchived)
		return
	}
	owner, err := s.st.Q.GetOrb(ctx, sb.OwnerOrbID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		destroy(ReasonOwnerOrbGone)
		return
	case err != nil:
		slog.Warn("sandboxes: 读取 owner orb 失败", "sandbox", sb.ID, "err", err)
		return
	case slices.Contains(ownerGone, owner.State):
		destroy(ReasonOwnerOrbGone)
		return
	}

	nowMs := now.UnixMilli()
	if sb.State == stateCreating {
		// 创建它的进程已不在（重启）：等待流程不会再推进它
		if now.Sub(time.UnixMilli(sb.CreatedAt)) > s.cfg.CreateTimeout+30*time.Second {
			destroy(ReasonCreateTimeout)
		}
		return
	}
	switch {
	case nowMs >= sb.HardDeadline:
		destroy(ReasonMaxLifetime)
		return
	case sb.TtlDeadline != nil && nowMs >= *sb.TtlDeadline:
		destroy(ReasonTTL)
		return
	case orbMissing:
		destroy(ReasonLost)
		return
	}
	if s.lost(ctx, sb, o, now) {
		destroy(ReasonLost)
		return
	}
	if s.isBusy(sb.ID) || nowMs-sb.LastActiveAt < sb.IdleSec*1000 {
		return
	}
	// 空闲：有在跑的后台作业就续上活动时间
	var jobs apigen.OrbSandboxJobList
	if err := s.orbJSON(ctx, sb.OrbID, "GET", "/sandbox/jobs", &jobs); err != nil {
		slog.Debug("sandboxes: 查询后台作业失败，本轮跳过空闲回收", "sandbox", sb.ID, "err", err)
		return
	}
	if slices.ContainsFunc(jobs.Jobs, func(j apigen.OrbSandboxJob) bool { return j.State == apigen.OrbSandboxJobRunning }) {
		s.touch(ctx, sb.ID)
		return
	}
	destroy(ReasonIdle)
}

// lost 报告 ready 的沙箱是否失联：provider 在线而 link 连续 LostAfter 不在。provider 离线期间不计时。
func (s *Service) lost(ctx context.Context, sb db.Sandbox, o db.Orb, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.orbs.Connected(sb.OrbID) {
		delete(s.lostSince, sb.ID)
		return false
	}
	p, err := s.st.Q.GetProvider(ctx, o.ProviderID)
	if err != nil || !providerOnline(p, now.UnixMilli()) {
		delete(s.lostSince, sb.ID)
		return false
	}
	since, ok := s.lostSince[sb.ID]
	if !ok {
		s.lostSince[sb.ID] = now
		return false
	}
	return now.Sub(since) >= s.cfg.LostAfter
}
