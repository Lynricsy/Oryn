package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orbLink 是一条活动的 orbd link。
type orbLink struct {
	orbID string
	gen   uint64
	sess  *link.Session
	ctl   *link.Ctl
	// helloCh 在收到 hello 后关闭。
	helloCh chan struct{}
	hello   apigen.LinkCtlHelloParams
}

func (l *orbLink) helloSeen() bool {
	select {
	case <-l.helloCh:
		return true
	default:
		return false
	}
}

// HandleRequest 注册 orbd→orynd 的 ctl 请求处理器（如 entries.cursors / entries.append）。
func (s *Service) HandleRequest(method string, fn RequestFunc) {
	s.hmu.Lock()
	s.reqs[method] = fn
	s.hmu.Unlock()
}

// HandleNotify 注册 ctl 通知处理器（如 agent.status）。hello / metrics.sample / setup.log / setup.result 先由本包处理，
// 再交给同名注册处理器（若有）。
func (s *Service) HandleNotify(method string, fn NotifyFunc) {
	s.hmu.Lock()
	s.notifies[method] = fn
	s.hmu.Unlock()
}

// HandleStream 注册 orbd 发起的流处理器（如 agent / blob）；egress 由本包处理。
func (s *Service) HandleStream(t string, fn StreamFunc) {
	s.hmu.Lock()
	s.streams[t] = fn
	s.hmu.Unlock()
}

// Ctl 返回 orb 当前活动 link 的 ctl。
func (s *Service) Ctl(orbID string) (*link.Ctl, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.links[orbID]
	if l == nil {
		return nil, false
	}
	return l.ctl, true
}

// Connected 报告 orb 是否有已完成 hello 的活动 link。
func (s *Service) Connected(orbID string) bool {
	s.mu.Lock()
	l := s.links[orbID]
	s.mu.Unlock()
	return l != nil && l.helloSeen()
}

// Kick 关闭 orb 的活动 link（越权或 orb 删除时）。
func (s *Service) Kick(orbID, reason string) {
	s.mu.Lock()
	l := s.links[orbID]
	s.mu.Unlock()
	if l != nil {
		slog.Warn("关闭 orb link", "orb", orbID, "reason", reason)
		_ = l.sess.Close()
	}
}

// waitLink 返回已 hello 的活动 link；没有时至多等待 wait。
func (s *Service) waitLink(ctx context.Context, orbID string, wait time.Duration) *orbLink {
	deadline := time.Now().Add(wait)
	for {
		s.mu.Lock()
		l := s.links[orbID]
		s.mu.Unlock()
		if l != nil && l.helloSeen() {
			return l
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// linkAcceptable 报告 orb 处于 state 时是否接受 link。frozen / stopped / failed / deleting / deleted 一律拒绝：
// 唤醒只由 provider 的 spawn 驱动（spawn 同时轮换令牌），hello 不再能把 orb 拉回来。stopping 也接受，
// 因为从 frozen 停机要先解冻、等 orbd 用新令牌重连后发 shutdown。
func linkAcceptable(state string) bool {
	switch state {
	case StateSpawning, StateConnecting, StateSettingUp, StateResuming, StateReady, StateWaking, StateStopping:
		return true
	}
	return false
}

// LinkHandler 返回 GET /orb/v1/link 的处理器（WebSocket + yamux，Bearer oot_…）。
func (s *Service) LinkHandler() http.Handler {
	return http.HandlerFunc(s.serveLink)
}

func (s *Service) serveLink(w http.ResponseWriter, r *http.Request) {
	var orb db.Orb
	sess, err := link.Accept(w, r, func(r *http.Request, token string) error {
		if ids.TokenPrefix(token) != "oot" {
			return errors.New("not an orb token")
		}
		o, err := s.st.Q.GetOrbByTokenHash(r.Context(), ids.Hash(token))
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("unknown orb token")
		}
		if err != nil {
			return err
		}
		if !linkAcceptable(o.State) {
			return fmt.Errorf("orb %s is %s", o.ID, o.State)
		}
		orb = o
		return nil
	})
	if err != nil {
		slog.Debug("orb link 拒绝", "err", err)
		return
	}
	defer sess.Close()
	hctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	first, err := sess.AcceptStream(hctx)
	cancel()
	if err != nil || first.Header.T != link.StreamCtl {
		slog.Warn("orb link 首条流不是 ctl", "orb", orb.ID, "err", err)
		return
	}
	// ctl 端点先完整构造、link 先发布，之后才处理其消息：任何人从 links 里拿到的都是 ctl 已就绪的 link，
	// hello 等回调也不会在发布之前运行。
	published := make(chan struct{})
	l := &orbLink{orbID: orb.ID, sess: sess, helloCh: make(chan struct{})}
	l.ctl = link.NewCtl(first, link.CtlHandlers{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			<-published
			s.hmu.RLock()
			fn := s.reqs[method]
			s.hmu.RUnlock()
			if fn == nil {
				return nil, link.ErrMethodNotFound
			}
			return fn(ctx, orb.ID, params)
		},
		OnNotify: func(ctx context.Context, method string, params json.RawMessage) {
			<-published
			s.onNotify(ctx, l, method, params)
		},
	})
	s.mu.Lock()
	s.linkGen++
	l.gen = s.linkGen
	old := s.links[orb.ID]
	s.links[orb.ID] = l
	s.mu.Unlock()
	if s.hookLinkPublished != nil {
		s.hookLinkPublished(orb.ID)
	}
	close(published)
	if old != nil {
		slog.Info("orb link 被新连接替换", "orb", orb.ID)
		_ = old.sess.Close()
	}
	slog.Info("orb link 已连接", "orb", orb.ID, "remote", r.RemoteAddr)

	ctx, cancelStreams := context.WithCancel(context.Background())
	defer cancelStreams()
	go func() {
		<-l.ctl.Done()
		_ = sess.Close()
	}()
	for {
		st, err := sess.AcceptStream(ctx)
		if err != nil {
			var he *link.HeaderError
			if errors.As(err, &he) && !sess.IsClosed() {
				slog.Warn("orb 流头无效", "orb", orb.ID, "err", err)
				continue
			}
			break
		}
		switch st.Header.T {
		case link.StreamEgress:
			s.serveEgress(orb.ID, st)
		case link.StreamCtl:
			slog.Warn("重复的 ctl 流，关闭 link", "orb", orb.ID)
			_ = st.Close()
			_ = sess.Close()
		default:
			s.hmu.RLock()
			fn := s.streams[st.Header.T]
			s.hmu.RUnlock()
			if fn == nil {
				slog.Warn("未知流类型", "orb", orb.ID, "t", st.Header.T)
				_ = st.Close()
				continue
			}
			go func() {
				defer st.Close()
				fn(ctx, orb.ID, st.Header, st)
			}()
		}
	}
	s.onLinkDown(l)
}

func (s *Service) onNotify(ctx context.Context, l *orbLink, method string, params json.RawMessage) {
	switch method {
	case string(apigen.MethodHello):
		var h apigen.LinkCtlHelloParams
		if err := json.Unmarshal(params, &h); err != nil {
			slog.Warn("hello 参数无效", "orb", l.orbID, "err", err)
			return
		}
		if l.helloSeen() {
			slog.Warn("重复的 hello，忽略", "orb", l.orbID)
			return
		}
		l.hello = h
		close(l.helloCh)
		ov, av := h.OrbdVersion, h.AgentVersion
		if err := s.st.Q.SetOrbVersions(ctx, db.SetOrbVersionsParams{OrbdVersion: &ov, AgentVersion: &av, ID: l.orbID}); err != nil {
			slog.Warn("记录 orb 版本失败", "orb", l.orbID, "err", err)
		}
		slog.Info("orbd hello", "orb", l.orbID, "orbd", h.OrbdVersion, "agent", h.AgentVersion, "threads", len(h.Threads))
		s.goBG(func() { s.onHello(l) })
	case string(apigen.MethodMetricsSample):
		var m apigen.OrbMetricSample
		if err := json.Unmarshal(params, &m); err != nil {
			slog.Warn("metrics.sample 参数无效", "orb", l.orbID, "err", err)
			return
		}
		if err := s.st.Q.InsertOrbSample(ctx, db.InsertOrbSampleParams{
			OrbID: l.orbID, Ts: m.TS, CpuPct: float64(m.CPUPct), MemUsedMb: int64(m.MemUsedMB), MemLimitMb: int64(m.MemLimitMB),
			DiskUsedMb: int64(m.DiskUsedMB), NetRxBytes: m.NetRxBytes, NetTxBytes: m.NetTxBytes,
		}); err != nil {
			slog.Warn("写入 orb 采样失败", "orb", l.orbID, "err", err)
		}
	case string(apigen.MethodSetupLog):
		s.onSetupLog(l.orbID, params)
	case string(apigen.MethodSetupResult):
		s.onSetupResult(ctx, l.orbID, params)
	}
	s.hmu.RLock()
	fn := s.notifies[method]
	s.hmu.RUnlock()
	switch {
	case fn != nil:
		fn(ctx, l.orbID, params)
	case method == string(apigen.MethodHello), method == string(apigen.MethodMetricsSample),
		method == string(apigen.MethodSetupLog), method == string(apigen.MethodSetupResult):
	default:
		slog.Debug("未处理的 ctl 通知", "orb", l.orbID, "method", method)
	}
}

// onHello 按 orb 当前状态推进：connecting → setup / resume；其余可接受状态只记录 link 已连上
// （spawning / waking 在 spawn 任务完成、进入 connecting 时由 advance 接手）。
func (s *Service) onHello(l *orbLink) {
	ctx := context.Background()
	orb, err := s.Get(ctx, l.orbID)
	if err != nil {
		return
	}
	if !linkAcceptable(orb.State) {
		s.Kick(orb.ID, "orb is "+orb.State)
		return
	}
	if orb.State == StateConnecting {
		s.runPhase(l)
	}
	s.emitInfo(ctx, EventLinkUp, orb)
}

// advance 在 orb 进入 connecting 后检查是否已有完成 hello 的 link，有则开始 setup / resume。
func (s *Service) advance(orbID string) {
	s.mu.Lock()
	l := s.links[orbID]
	s.mu.Unlock()
	if l != nil && l.helloSeen() {
		s.goBG(func() { s.runPhase(l) })
	}
}

// runPhase 执行 connecting → setting_up（首次）/ resuming（唤醒）→ ready。
func (s *Service) runPhase(l *orbLink) {
	ctx := context.Background()
	everReady, err := s.st.Q.OrbEverReady(ctx, l.orbID)
	if err != nil {
		slog.Warn("查询 orb 历史失败", "orb", l.orbID, "err", err)
		return
	}
	phase, method := StateSettingUp, apigen.MethodSetupRun
	if everReady {
		phase, method = StateResuming, apigen.MethodResumeRun
	}
	orb, moved, err := s.transition(ctx, l.orbID, StateConnecting, phase, nil)
	if err != nil || !moved {
		return
	}
	var plan SetupPlan
	if s.setup != nil {
		plan, err = s.setup.Plan(ctx, orb)
		if err != nil {
			slog.Warn("生成 setup 参数失败", "orb", l.orbID, "err", err)
			_, _, _ = s.transition(ctx, l.orbID, phase, StateFailed, map[string]any{"reason": err.Error()})
			return
		}
	} else {
		plan.Params = apigen.LinkCtlSetupRunParams{GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{}}
	}
	s.setPolicy(l.orbID, plan.Egress)
	cctx, cancel := context.WithTimeout(ctx, s.cfg.SetupTimeout)
	defer cancel()
	var res apigen.LinkCtlSetupRunResult
	err = l.ctl.Call(cctx, string(method), plan.Params, &res)
	if err != nil {
		if errors.Is(err, link.ErrCtlClosed) || l.ctl.Err() != nil {
			// link 断开：退回 connecting（开始 hello 计时），若新 link 已 hello 则立即重来。
			slog.Info("setup / resume 期间 link 断开", "orb", l.orbID, "phase", phase)
			if _, moved, _ := s.transition(ctx, l.orbID, phase, StateConnecting, map[string]any{"reason": "link lost during " + phase}); moved {
				s.advance(l.orbID)
			}
			return
		}
		slog.Warn("setup / resume 失败", "orb", l.orbID, "phase", phase, "err", err)
		_, _, _ = s.transition(ctx, l.orbID, phase, StateFailed, map[string]any{"reason": err.Error()})
		return
	}
	detail := map[string]any{"ok": res.Ok, "setupRan": res.SetupRan, "durationMs": res.DurationMs}
	if !res.Ok {
		// 脚本失败不阻塞线程：orb 仍进入 ready（agent 要启动才能修复脚本），日志已经 setup.log 上报。
		if res.SetupError != nil {
			detail["setupError"] = *res.SetupError
		}
		slog.Warn("orb setup 脚本失败（继续）", "orb", l.orbID, "phase", phase, "err", detail["setupError"])
	}
	_, _, _ = s.transition(ctx, l.orbID, phase, StateReady, detail)
}

func (s *Service) onLinkDown(l *orbLink) {
	s.mu.Lock()
	current := s.links[l.orbID] == l
	if current {
		delete(s.links, l.orbID)
	}
	s.mu.Unlock()
	slog.Info("orb link 断开", "orb", l.orbID, "current", current)
	if !current {
		return
	}
	ctx := context.Background()
	orb, err := s.Get(ctx, l.orbID)
	if err != nil {
		return
	}
	s.emitInfo(ctx, EventLinkDown, orb)
}
