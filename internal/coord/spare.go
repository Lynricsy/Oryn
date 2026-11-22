package coord

// 全局助手的备用对话：常驻检查（keepWarm）在协调 orb 上始终备好一个 agent 已就绪、对用户不可见的空白助手对话
// （threads.CoordinatorParams.Hidden）；新建助手对话时直接认领它，免掉起 omp 的几秒「启动中」，随后在后台补一个新的。

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// spareMaxAge 是备用对话的最长保留时间：它的系统提示词（含助手记忆摘要）在起 agent 时就定下了，放久了新对话会带着
// 过时的记忆，超过后换一个新的。
const spareMaxAge = time.Hour

// claimSpare 在常驻开着时把就绪的备用对话交给新对话。没有可认领的备用对话时 ok 为假，调用方照常新建；
// 认领时下发命令失败（如文件库 id 无效）返回错误，备用对话随之删掉（它的模型 / 档位可能已改）。
func (s *Service) claimSpare(ctx context.Context, orbID string, p threads.CoordinatorParams) (apigen.Thread, bool, error) {
	if s.cfg.KeepWarm <= 0 {
		return apigen.Thread{}, false, nil
	}
	s.spareMu.Lock()
	defer s.spareMu.Unlock()
	sp, ok, err := s.th.FindSpare(ctx, orbID)
	if err != nil {
		slog.Warn("coord: 读取备用助手对话失败", "err", err)
		return apigen.Thread{}, false, nil
	}
	if !ok || !sp.Ready {
		return apigen.Thread{}, false, nil
	}
	t, err := s.th.ClaimSpare(ctx, sp.ID, p)
	switch {
	case err == nil:
		s.kickSpare()
		return t, true, nil
	case errors.Is(err, threads.ErrSpareUnavailable):
		return apigen.Thread{}, false, nil
	}
	if derr := s.th.Delete(context.WithoutCancel(ctx), sp.ID); derr != nil {
		slog.Warn("coord: 删除认领失败的备用助手对话失败", "thread", sp.ID, "err", derr)
	}
	s.kickSpare()
	return apigen.Thread{}, false, err
}

// kickSpare 让常驻检查尽快补一个备用对话（不阻塞；已有待处理的信号时合并）。
func (s *Service) kickSpare() {
	select {
	case s.spareKick <- struct{}{}:
	default:
	}
}

// ensureSpare 保证协调 orb 上有一个备用对话：没有就新建（随即起 agent），过了 spareMaxAge 就删掉换新的，
// 已有且未过期时确保它的 agent 在跑（orynd 重启后由这里重新拉起）。
func (s *Service) ensureSpare(ctx context.Context) {
	orbID, err := s.EnsureOrb(ctx)
	if err != nil {
		return
	}
	s.spareMu.Lock()
	defer s.spareMu.Unlock()
	sp, ok, err := s.th.FindSpare(ctx, orbID)
	if err != nil {
		slog.Warn("coord: 读取备用助手对话失败", "err", err)
		return
	}
	if ok {
		if s.nowMs()-sp.CreatedAt < spareMaxAge.Milliseconds() {
			if err := s.th.KeepWarm(ctx, sp.ID); err != nil && ctx.Err() == nil {
				slog.Debug("coord: 唤醒备用助手对话失败", "thread", sp.ID, "err", err)
			}
			return
		}
		if err := s.th.Delete(ctx, sp.ID); err != nil {
			slog.Warn("coord: 删除过期的备用助手对话失败", "thread", sp.ID, "err", err)
			return
		}
	}
	if _, err := s.th.CreateCoordinator(ctx, threads.CoordinatorParams{
		Kind: threads.KindAssistant, OrbID: orbID, Model: s.cfg.Model, Hidden: true,
	}); err != nil && ctx.Err() == nil {
		slog.Warn("coord: 新建备用助手对话失败", "err", err)
	}
}
