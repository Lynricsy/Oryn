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
// 同时删除它换来的网页会话（App 会话，M6）：标记吊销与删除会话在同一个事务里，
// 任一步失败（含请求 ctx 被取消）整体回滚，不会留下「设备已吊销但 App 会话仍可用」的半完成状态。
// 提交成功后才作废该设备未兑换的 App 会话一次性码，并先发布 (device, id)、再逐个发布 (web, 会话哈希)，
// 让实时通道、portal 与票据表一并失效；失败时不发布任何事件。
func (s *Service) RevokeDevice(ctx context.Context, id string) error {
	var sessions []string
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.GetDevice(ctx, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return newErr(404, "not_found", "设备不存在")
			}
			return err
		}
		now := time.Now().UnixMilli()
		if _, err := q.RevokeDevice(ctx, db.RevokeDeviceParams{RevokedAt: &now, ID: id}); err != nil {
			return err
		}
		var err error
		sessions, err = q.DeleteWebSessionsByDevice(ctx, &id)
		return err
	})
	if err != nil {
		return err
	}
	s.appCodes.Revoke(KindDevice, id)
	s.revocations.publish(KindDevice, id)
	for _, h := range sessions {
		s.revocations.publish(KindWeb, h)
	}
	return nil
}

// PrincipalValid 复核主体当前是否仍有效：设备未吊销；网页会话仍存在且未过期
// （App 会话另要求其设备未吊销，由 GetWebSession 查询本身保证）。
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
