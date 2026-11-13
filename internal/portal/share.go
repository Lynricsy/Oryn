package portal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ShareDuration 把公开时长枚举映射为时长：1h / 3h / 1d / 7d。
func ShareDuration(d apigen.PortalShareDuration) (time.Duration, bool) {
	switch d {
	case apigen.PortalShareDuration1H:
		return time.Hour, true
	case apigen.PortalShareDuration3H:
		return 3 * time.Hour, true
	case apigen.PortalShareDuration1D:
		return 24 * time.Hour, true
	case apigen.PortalShareDuration7D:
		return 7 * 24 * time.Hour, true
	}
	return 0, false
}

// timerCtxTimeout 是到期回调里读写库与刷新推送的时限。
const timerCtxTimeout = 10 * time.Second

type shareKey struct {
	orbID string
	port  int
}

type shareTimer struct{ stop func() }

// shareTimers 是每个公开中的 (orb, 端口) 的到期定时器。
type shareTimers struct {
	mu sync.Mutex
	m  map[shareKey]*shareTimer
}

// SetShare 公开或续期 (orb, 端口)：expires_at = now + d。端口不是已声明的 portal → ErrNotFound。
func (s *Service) SetShare(ctx context.Context, orbID string, port int, d time.Duration) (apigen.OrbPortal, error) {
	if d <= 0 {
		return apigen.OrbPortal{}, fmt.Errorf("portal: invalid share duration %s", d)
	}
	svc, ok, err := s.orbs.PortalService(ctx, orbID, port)
	if err != nil {
		return apigen.OrbPortal{}, err
	}
	if !ok {
		return apigen.OrbPortal{}, ErrNotFound
	}
	now := s.now()
	exp := now.Add(d).UnixMilli()
	if err := s.st.Q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{
		OrbID: orbID, Port: int64(port), CreatedAt: now.UnixMilli(), ExpiresAt: exp,
	}); err != nil {
		return apigen.OrbPortal{}, err
	}
	s.arm(shareKey{orbID, port}, exp)
	s.orbs.Refresh(ctx, orbID)
	links := svc.Links
	if links == nil {
		links = []apigen.OrbPortalLink{}
	}
	return apigen.OrbPortal{Port: port, Service: svc.Name, Links: links, Public: &apigen.OrbPortalPublic{ExpiresAt: exp}}, nil
}

// EndShare 终止公开（幂等）：删除分享行、停定时器、立即断开该端口的全部匿名连接。
func (s *Service) EndShare(ctx context.Context, orbID string, port int) error {
	if _, err := s.st.Q.DeletePortalShare(ctx, db.DeletePortalShareParams{OrbID: orbID, Port: int64(port)}); err != nil {
		return err
	}
	k := shareKey{orbID, port}
	s.disarm(k)
	s.reg.cancelKey(anonKey(orbID, port))
	s.orbs.Refresh(ctx, orbID)
	return nil
}

// arm 为 k 装（或重装）到期定时器。
func (s *Service) arm(k shareKey, expMs int64) {
	s.timers.mu.Lock()
	defer s.timers.mu.Unlock()
	if old := s.timers.m[k]; old != nil {
		old.stop()
	}
	t := &shareTimer{}
	d := max(time.UnixMilli(expMs).Sub(s.now()), 0)
	t.stop = s.afterFunc(d, func() { s.fire(k, t) })
	s.timers.m[k] = t
}

func (s *Service) disarm(k shareKey) {
	s.timers.mu.Lock()
	defer s.timers.mu.Unlock()
	if t := s.timers.m[k]; t != nil {
		t.stop()
		delete(s.timers.m, k)
	}
}

// fire 是到期回调：被续期则按新到期时间重装；否则删除分享行、断开匿名连接并推送 orb 更新。
func (s *Service) fire(k shareKey, t *shareTimer) {
	s.timers.mu.Lock()
	if s.timers.m[k] != t {
		s.timers.mu.Unlock()
		return // 已被重装或停止
	}
	delete(s.timers.m, k)
	s.timers.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timerCtxTimeout)
	defer cancel()
	sh, err := s.st.Q.GetPortalShare(ctx, db.GetPortalShareParams{OrbID: k.orbID, Port: int64(k.port)})
	switch {
	case err == nil && sh.ExpiresAt > s.now().UnixMilli():
		s.arm(k, sh.ExpiresAt)
		return
	case err == nil:
		if _, err := s.st.Q.DeletePortalShare(ctx, db.DeletePortalShareParams{OrbID: k.orbID, Port: int64(k.port)}); err != nil {
			slog.Warn("portal: 删除到期分享失败", "orb", k.orbID, "port", k.port, "err", err)
		}
	case !errors.Is(err, sql.ErrNoRows):
		slog.Warn("portal: 读取分享失败", "orb", k.orbID, "port", k.port, "err", err)
	}
	s.reg.cancelKey(anonKey(k.orbID, k.port))
	s.orbs.Refresh(ctx, k.orbID)
}

// Run 启动时为库里每个未过期的分享装定时器、删除已过期的行；之后每小时清理过期的分享、portal 会话与一次性码，
// 直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.sweep(ctx, true)
	tk := time.NewTicker(sweepEvery)
	defer tk.Stop()
	defer s.stopTimers()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			s.sweep(ctx, false)
		}
	}
}

func (s *Service) sweep(ctx context.Context, load bool) {
	now := s.now()
	s.codes.prune(now)
	if _, err := s.st.Q.DeleteExpiredPortalSessions(ctx, now.UnixMilli()); err != nil && ctx.Err() == nil {
		slog.Warn("portal: 清理过期 portal 会话失败", "err", err)
	}
	shares, err := s.st.Q.ListPortalShares(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("portal: 读取分享失败", "err", err)
		}
		return
	}
	refresh := map[string]bool{}
	for _, sh := range shares {
		k := shareKey{sh.OrbID, int(sh.Port)}
		if sh.ExpiresAt > now.UnixMilli() {
			if load {
				s.arm(k, sh.ExpiresAt)
			}
			continue
		}
		if _, err := s.st.Q.DeletePortalShare(ctx, db.DeletePortalShareParams{OrbID: sh.OrbID, Port: sh.Port}); err != nil {
			slog.Warn("portal: 删除到期分享失败", "orb", sh.OrbID, "port", sh.Port, "err", err)
			continue
		}
		s.disarm(k)
		s.reg.cancelKey(anonKey(sh.OrbID, int(sh.Port)))
		refresh[sh.OrbID] = true
	}
	for orbID := range refresh {
		s.orbs.Refresh(ctx, orbID)
	}
}

func (s *Service) stopTimers() {
	s.timers.mu.Lock()
	defer s.timers.mu.Unlock()
	for k, t := range s.timers.m {
		t.stop()
		delete(s.timers.m, k)
	}
}
