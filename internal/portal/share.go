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

// shareLocks 按 (orb, 端口) 串行化分享的生命周期：库变更、定时器换代与匿名连接取消在同一把键锁内完成，
// 旧操作（终止、到期回调、清理）因此不会停掉或删除新一代分享。条目按引用计数回收。
type shareLocks struct {
	mu sync.Mutex
	m  map[shareKey]*shareLock
	// contended 仅供测试：拿锁时发现已被占用、即将阻塞等待时调用。
	contended func(shareKey)
}

type shareLock struct {
	mu   sync.Mutex
	refs int
}

// lock 取 k 的键锁，返回解锁函数。
func (l *shareLocks) lock(k shareKey) (unlock func()) {
	l.mu.Lock()
	e := l.m[k]
	if e == nil {
		e = &shareLock{}
		l.m[k] = e
	}
	e.refs++
	hook := l.contended
	l.mu.Unlock()
	if !e.mu.TryLock() {
		if hook != nil {
			hook(k)
		}
		e.mu.Lock()
	}
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(l.m, k)
		}
		l.mu.Unlock()
	}
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
	k := shareKey{orbID, port}
	unlock := s.locks.lock(k)
	now := s.now()
	exp := now.Add(d).UnixMilli()
	err = s.st.Q.UpsertPortalShare(ctx, db.UpsertPortalShareParams{
		OrbID: orbID, Port: int64(port), CreatedAt: now.UnixMilli(), ExpiresAt: exp,
	})
	if err == nil {
		s.arm(k, exp)
	}
	unlock()
	if err != nil {
		return apigen.OrbPortal{}, err
	}
	s.orbs.Refresh(ctx, orbID)
	links := svc.Links
	if links == nil {
		links = []apigen.OrbPortalLink{}
	}
	return apigen.OrbPortal{Port: port, Service: svc.Name, Links: links, Public: &apigen.OrbPortalPublic{ExpiresAt: exp}}, nil
}

// EndShare 终止公开（幂等）：删除分享行、停定时器、立即断开该端口的全部匿名连接。
func (s *Service) EndShare(ctx context.Context, orbID string, port int) error {
	k := shareKey{orbID, port}
	unlock := s.locks.lock(k)
	_, err := s.st.Q.DeletePortalShare(ctx, db.DeletePortalShareParams{OrbID: orbID, Port: int64(port)})
	if err == nil {
		if h := s.hookEndShareDeleted; h != nil {
			h()
		}
		s.disarm(k)
		s.reg.cancelKey(anonKey(orbID, port))
	}
	unlock()
	if err != nil {
		return err
	}
	s.orbs.Refresh(ctx, orbID)
	return nil
}

// arm 为 k 装（或重装）到期定时器。调用方持有 k 的键锁。
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

// disarm 停掉 k 的定时器。调用方持有 k 的键锁。
func (s *Service) disarm(k shareKey) {
	s.timers.mu.Lock()
	defer s.timers.mu.Unlock()
	if t := s.timers.m[k]; t != nil {
		t.stop()
		delete(s.timers.m, k)
	}
}

// fire 是到期回调：被续期则按新到期时间重装；否则只删除它读到的那一代分享行、断开匿名连接并推送 orb 更新。
func (s *Service) fire(k shareKey, t *shareTimer) {
	unlock := s.locks.lock(k)
	s.timers.mu.Lock()
	if s.timers.m[k] != t {
		s.timers.mu.Unlock()
		unlock()
		return // 已被重装或停止
	}
	delete(s.timers.m, k)
	s.timers.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timerCtxTimeout)
	defer cancel()
	ended := s.expire(ctx, k)
	unlock()
	if ended {
		s.orbs.Refresh(ctx, k.orbID)
	}
}

// expire 在持有 k 的键锁时处理到期：行未到期则按其 expires_at 重装定时器；已到期则带 expires_at 条件删除
// （行在读与删之间被改写时删不到，重读后再判定）；分享已不存在时断开匿名连接。返回分享是否已结束。
func (s *Service) expire(ctx context.Context, k shareKey) bool {
	for range 3 {
		sh, err := s.st.Q.GetPortalShare(ctx, db.GetPortalShareParams{OrbID: k.orbID, Port: int64(k.port)})
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			slog.Warn("portal: 读取分享失败", "orb", k.orbID, "port", k.port, "err", err)
			break
		}
		now := s.now().UnixMilli()
		if sh.ExpiresAt > now {
			s.arm(k, sh.ExpiresAt)
			return false
		}
		if h := s.hookFireRead; h != nil {
			h()
		}
		n, err := s.st.Q.DeleteExpiredPortalShare(ctx, db.DeleteExpiredPortalShareParams{
			OrbID: k.orbID, Port: int64(k.port), ExpiresAt: sh.ExpiresAt, Now: now,
		})
		if err != nil {
			slog.Warn("portal: 删除到期分享失败", "orb", k.orbID, "port", k.port, "err", err)
			break
		}
		if n > 0 {
			break
		}
	}
	s.reg.cancelKey(anonKey(k.orbID, k.port))
	return true
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
	if h := s.hookSweepListed; h != nil {
		h()
	}
	refresh := map[string]bool{}
	for _, sh := range shares {
		k := shareKey{sh.OrbID, int(sh.Port)}
		if sh.ExpiresAt > now.UnixMilli() {
			if load {
				unlock := s.locks.lock(k)
				s.arm(k, sh.ExpiresAt)
				unlock()
			}
			continue
		}
		if s.sweepExpired(ctx, k, sh.ExpiresAt, now.UnixMilli()) {
			refresh[sh.OrbID] = true
		}
	}
	for orbID := range refresh {
		s.orbs.Refresh(ctx, orbID)
	}
}

// sweepExpired 在 k 的键锁内删除列表里读到的那一代过期分享；行已被续期或删除则什么都不做。
func (s *Service) sweepExpired(ctx context.Context, k shareKey, expMs, nowMs int64) bool {
	unlock := s.locks.lock(k)
	defer unlock()
	n, err := s.st.Q.DeleteExpiredPortalShare(ctx, db.DeleteExpiredPortalShareParams{
		OrbID: k.orbID, Port: int64(k.port), ExpiresAt: expMs, Now: nowMs,
	})
	if err != nil {
		slog.Warn("portal: 删除到期分享失败", "orb", k.orbID, "port", k.port, "err", err)
		return false
	}
	if n == 0 {
		return false
	}
	s.disarm(k)
	s.reg.cancelKey(anonKey(k.orbID, k.port))
	return true
}

func (s *Service) stopTimers() {
	s.timers.mu.Lock()
	defer s.timers.mu.Unlock()
	for k, t := range s.timers.m {
		t.stop()
		delete(s.timers.m, k)
	}
}
