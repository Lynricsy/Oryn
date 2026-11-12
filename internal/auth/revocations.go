package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// revocations 把凭据失效（设备吊销、注销、会话过期 / 被删除）广播给订阅者，
// 供实时通道关闭对应主体的 WebSocket 连接。
type revocations struct {
	mu   sync.Mutex
	next int
	subs map[int]func(kind, id string)
}

func (r *revocations) subscribe(fn func(kind, id string)) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subs == nil {
		r.subs = map[int]func(kind, id string){}
	}
	id := r.next
	r.next++
	r.subs[id] = fn
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.subs, id)
	}
}

func (r *revocations) publish(kind, id string) {
	r.mu.Lock()
	fns := make([]func(kind, id string), 0, len(r.subs))
	for _, fn := range r.subs {
		fns = append(fns, fn)
	}
	r.mu.Unlock()
	for _, fn := range fns {
		fn(kind, id)
	}
}

// Subscribe 注册吊销回调：kind 为 "web"（id = 会话 id 哈希，与 Principal.ID 一致）或
// "device"（id = dev_…）。回调在发布者的 goroutine 中同步执行，不得阻塞。返回取消订阅函数。
func (s *Service) Subscribe(fn func(principalKind, principalID string)) (cancel func()) {
	return s.revocations.subscribe(fn)
}

// RevokeDevice 吊销设备（幂等）并发布吊销事件；设备不存在返回 404 错误。
func (s *Service) RevokeDevice(ctx context.Context, id string) error {
	if _, err := s.st.Q.GetDevice(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return newErr(404, "not_found", "设备不存在")
		}
		return err
	}
	now := time.Now().UnixMilli()
	if _, err := s.st.Q.RevokeDevice(ctx, db.RevokeDeviceParams{RevokedAt: &now, ID: id}); err != nil {
		return err
	}
	s.revocations.publish(KindDevice, id)
	return nil
}

// PrincipalValid 复核主体当前是否仍有效：设备未吊销；网页会话仍存在且未过期。
// 供长连接（WebSocket）在登记之后复核票据签发时的主体。
func (s *Service) PrincipalValid(ctx context.Context, p Principal) (bool, error) {
	switch p.Kind {
	case KindDevice:
		d, err := s.st.Q.GetDevice(ctx, p.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return d.RevokedAt == nil, nil
	case KindWeb:
		ws, err := s.st.Q.GetWebSession(ctx, p.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return ws.ExpiresAt > time.Now().UnixMilli(), nil
	}
	return false, nil
}

// sweepEvery 是清理循环的周期。
const sweepEvery = time.Minute

// Run 周期性删除过期网页会话（逐条发布吊销事件）、过期配对请求与过期的已用仪式记录，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		if err := s.sweep(ctx, time.Now()); err != nil && ctx.Err() == nil {
			slog.Warn("清理过期会话失败", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) sweep(ctx context.Context, now time.Time) error {
	s.pruneUsedCeremonies(now)
	ms := now.UnixMilli()
	expired, err := s.st.Q.DeleteExpiredWebSessionsReturning(ctx, ms)
	if err != nil {
		return err
	}
	for _, h := range expired {
		s.revocations.publish(KindWeb, h)
	}
	_, err = s.st.Q.DeleteExpiredDevicePairings(ctx, ms)
	return err
}
