package orbs

import "context"

// SetBoxGate 注册 box 闸门（M23）：返回 false 时 Wake 不唤醒这台 box 的 orb（它被主人或时段停掉了）。
func (s *Service) SetBoxGate(fn func(orbID string) bool) {
	s.hmu.Lock()
	s.boxGate = fn
	s.hmu.Unlock()
}

// boxAllowed 报告闸门是否放行 orbID 的唤醒；没有闸门时放行。
func (s *Service) boxAllowed(orbID string) bool {
	s.hmu.RLock()
	fn := s.boxGate
	s.hmu.RUnlock()
	return fn == nil || fn(orbID)
}

// BoxStopped 报告 orbID 是不是一台被闸门挡住的 box（不是 box、没有闸门、读不到 orb 都是 false）。
func (s *Service) BoxStopped(ctx context.Context, orbID string) bool {
	s.hmu.RLock()
	fn := s.boxGate
	s.hmu.RUnlock()
	if fn == nil {
		return false
	}
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil || orb.Box == 0 {
		return false
	}
	return !fn(orbID)
}

// Revive 把 ready 却没有 link 的 orb 送回 waking 并入队 spawn（宿主重启、容器退出后的恢复）；状态已变则无操作。
// 之后走现成的 spawn → connecting → hello → resuming → ready，orbd 在 resume 结尾拉起服务。
func (s *Service) Revive(ctx context.Context, orbID string) error {
	_, _, err := s.transitionThen(ctx, orbID, StateReady, StateWaking, map[string]any{"reason": "link lost"}, s.spawnIn)
	return err
}
