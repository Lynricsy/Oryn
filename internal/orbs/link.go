package orbs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
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
	// prepared 在 onHello 完成本 link 的准备工作（portal.configure）之后关闭：任何 setup / resume
	// （onHello 自己或 spawn 完成后的 advance）都等它，保证受管服务拉起时已有 PUBLIC_URL 模板。
	prepared chan struct{}
	// runner 为真时这是 runner 的常驻 orb 的 link（M11b）。
	runner bool
	// rejected 为真时发布后的复核没有通过（令牌已吊销 / orb 已删除等）：在关闭 published 之前写入，
	// ctl 处理器等到 published 之后读它，为真则一律不处理。
	rejected bool
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

// HasFeature 报告 orb 当前有已 hello 的 link，且其 hello 的 features 含 feature（判定同 usable）。
func (s *Service) HasFeature(orbID, feature string) bool {
	s.mu.Lock()
	l := s.links[orbID]
	s.mu.Unlock()
	return l != nil && l.helloSeen() && slices.Contains(l.hello.Features, feature)
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

// LinkHandler 返回 GET /orb/v1/link 的处理器（WebSocket + yamux，Bearer oot_… | ort_…）。
func (s *Service) LinkHandler() http.Handler {
	return http.HandlerFunc(s.serveLink)
}

func (s *Service) serveLink(w http.ResponseWriter, r *http.Request) {
	var orb db.Orb
	var token string
	sess, err := link.Accept(w, r, func(r *http.Request, tok string) error {
		o, err := s.orbForToken(r.Context(), tok)
		if err != nil {
			return err
		}
		if !orbLinkAcceptable(o) {
			return fmt.Errorf("orb %s is %s", o.ID, o.State)
		}
		orb, token = o, tok
		return nil
	})
	if err != nil {
		slog.Debug("orb link 拒绝", "err", err)
		return
	}
	defer sess.Close()
	if IsRunner(orb) {
		// runner 接入即上线：stopped → connecting，hello 之后 → ready
		s.runnerConnecting(r.Context(), orb.ID)
	}
	s.attach(orb, sess, r.RemoteAddr, func(ctx context.Context) error { return s.recheckToken(ctx, orb.ID, token) })
}

// recheckToken 是 WebSocket link 发布后的复核：令牌仍映射到同一台 orb（ort_ 的 provider 未停用、令牌未被换掉；
// oot_ 未被轮换），且 orb 仍接受 link（未删除）。
func (s *Service) recheckToken(ctx context.Context, orbID, token string) error {
	o, err := s.orbForToken(ctx, token)
	if err != nil {
		return err
	}
	if o.ID != orbID {
		return fmt.Errorf("token now belongs to orb %s", o.ID)
	}
	if o.DeletedAt != nil || !orbLinkAcceptable(o) {
		return fmt.Errorf("orb %s is %s", o.ID, o.State)
	}
	return nil
}

// ServeConn 在已建立的字节流上服务协调 orb 的 link（M9 协调宿主分册 H6）：conn 是 orynd 与它拉起的
// `orynd coordinator-host` 之间的 socketpair，对端身份由创建它的进程保证，不再鉴权。orb 须存在、是协调 orb
// 且处于接受 link 的状态，否则关闭 conn 并返回错误。阻塞到 link 断开。
func (s *Service) ServeConn(ctx context.Context, orbID string, conn net.Conn) error {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		_ = conn.Close()
		return err
	}
	if !orb.Coordinator {
		_ = conn.Close()
		return fmt.Errorf("%w: orb %s is not a coordinator orb", ErrInvalidState, orbID)
	}
	if !linkAcceptable(orb.State) {
		_ = conn.Close()
		return fmt.Errorf("%w: orb %s is %s", ErrInvalidState, orbID, orb.State)
	}
	sess, err := link.NewConnSession(conn, false)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer sess.Close()
	s.attach(orb, sess, "coordinator-host", func(ctx context.Context) error {
		o, err := s.Get(ctx, orbID)
		if err != nil {
			return err
		}
		if !o.Coordinator || !linkAcceptable(o.State) {
			return fmt.Errorf("orb %s is %s", orbID, o.State)
		}
		return nil
	})
	return nil
}

// attach 是 link 建立之后的通用逻辑（WebSocket link 与协调宿主的 socketpair 共用）：等首条 ctl 流、发布 link、
// 复核准入、分派后续流，直到会话结束。
//
// 准入只在升级前鉴权过一次，之后最长 30 秒在等首条 ctl 流，期间吊销 / 删除的 Kick 看不到这条会话。所以先发布
// （此后 Kick 一定能找到它），再用 recheck 复核令牌与 orb 状态；复核通过之前不分派任何 ctl 消息，不通过则关闭。
// 吊销事务提交在复核之前，复核就会失败；提交在复核之后，吊销之后的 Kick 就能在 links 里看到它，没有空窗。
func (s *Service) attach(orb db.Orb, sess *link.Session, remote string, recheck func(context.Context) error) {
	hctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	first, err := sess.AcceptStream(hctx)
	cancel()
	if err != nil || first.Header.T != link.StreamCtl {
		slog.Warn("orb link 首条流不是 ctl", "orb", orb.ID, "err", err)
		return
	}
	// ctl 端点先完整构造、link 先发布并通过复核，之后才处理其消息：任何人从 links 里拿到的都是 ctl 已就绪的 link，
	// hello 等回调也不会在发布与复核之前运行。
	published := make(chan struct{})
	l := &orbLink{orbID: orb.ID, sess: sess, helloCh: make(chan struct{}), prepared: make(chan struct{}), runner: IsRunner(orb)}
	l.ctl = link.NewCtl(first, link.CtlHandlers{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			<-published
			if l.rejected {
				return nil, &link.CtlError{Code: "unauthorized", Message: "link rejected"}
			}
			if l.runner && !runnerRequestAllowed(method) {
				// runner 不可信：只放行它正常流程会发的请求（控制面的密钥不去 runner）
				slog.Warn("runner 发来不允许的 ctl 请求", "orb", orb.ID, "method", method)
				return nil, &link.CtlError{Code: CodeRunnerUnsupported, Message: method + " is not available to a runner"}
			}
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
			if l.rejected {
				return
			}
			if l.runner && !runnerNotifyAllowed(method) {
				slog.Warn("runner 发来不允许的 ctl 通知，忽略", "orb", orb.ID, "method", method)
				return
			}
			s.onNotify(ctx, l, method, params)
		},
	})
	s.mu.Lock()
	s.linkGen++
	l.gen = s.linkGen
	old := s.links[orb.ID]
	s.links[orb.ID] = l
	var firing *runnerTimer
	if l.runner {
		// 新 link 到来：取消上一条 link 断开时起的离线定时器。与发布同在 s.mu 下：定时器回调要么看到本 link
		// 而放弃，要么已在提交离线（firing），本 link 等它提交完再处理 hello（hello 会把 stopped 拉回来）。
		firing = s.cancelRunnerOfflineLocked(orb.ID)
	}
	s.mu.Unlock()
	if s.hookLinkPublished != nil {
		s.hookLinkPublished(orb.ID)
	}
	if firing != nil {
		if hookRunnerLinkAwaitsOffline != nil {
			hookRunnerLinkAwaitsOffline(orb.ID)
		}
		<-firing.done
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := recheck(rctx); err != nil {
		slog.Warn("orb link 发布后复核未通过，关闭", "orb", orb.ID, "err", err)
		l.rejected = true
	}
	rcancel()
	close(published)
	if old != nil {
		slog.Info("orb link 被新连接替换", "orb", orb.ID)
		_ = old.sess.Close()
	}
	if l.rejected {
		_ = sess.Close()
		s.onLinkDown(l)
		return
	}
	slog.Info("orb link 已连接", "orb", orb.ID, "remote", remote)

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
			if l.runner {
				// runner 不走控制面的 egress 代理（控制面的密钥不去 runner）
				_ = st.Close()
				continue
			}
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
		// S4：hello 的字段会落库并推给所有客户端，先按上限收紧（之后各处只用收紧后的值）
		l.hello = sanitizeHello(h)
		h = l.hello
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
	case string(apigen.MethodServicesState):
		s.onServicesState(ctx, l.orbID, params)
	case string(apigen.MethodDesktopState):
		s.onDesktopState(ctx, l.orbID, params)
	}
	s.hmu.RLock()
	fn := s.notifies[method]
	s.hmu.RUnlock()
	switch {
	case fn != nil:
		fn(ctx, l.orbID, params)
	case method == string(apigen.MethodHello), method == string(apigen.MethodMetricsSample),
		method == string(apigen.MethodSetupLog), method == string(apigen.MethodSetupResult),
		method == string(apigen.MethodServicesState), method == string(apigen.MethodDesktopState):
	default:
		slog.Debug("未处理的 ctl 通知", "orb", l.orbID, "method", method)
	}
}

// onHello 按 orb 当前状态推进：connecting → setup / resume；其余可接受状态只记录 link 已连上
// （spawning / waking 在 spawn 任务完成、进入 connecting 时由 advance 接手）。支持 portal 的 orbd
// 先收到 portal.configure，之后才关闭 prepared 放行 setup / resume（setup / resume 结束时 orbd 会拉起
// 受管服务，届时已有 PUBLIC_URL 模板）；无论成败 prepared 都会关闭。
func (s *Service) onHello(l *orbLink) {
	if s.hookHelloStart != nil {
		s.hookHelloStart(l.orbID)
	}
	ctx := context.Background()
	orb, err := s.Get(ctx, l.orbID)
	if err == nil && orbLinkAcceptable(orb) && slices.Contains(l.hello.Features, FeaturePortal) {
		s.portalConfigure(ctx, l)
	}
	close(l.prepared)
	if err != nil {
		return
	}
	if !orbLinkAcceptable(orb) {
		s.Kick(orb.ID, "orb is "+orb.State)
		return
	}
	if l.runner {
		// 目录与特性先落库，再进入 ready：线程看到 runner 在线时一定能看到它声明的目录
		s.runnerHello(ctx, l)
		if orb.State == StateStopped {
			// 离线定时器恰在本 link 发布之前到期
			s.runnerConnecting(ctx, orb.ID)
			if orb, err = s.Get(ctx, l.orbID); err != nil {
				return
			}
		}
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

// runPhase 执行 connecting → setting_up（首次）/ resuming（唤醒）→ ready。先等本 link 准备完成
// （见 orbLink.prepared）；link 在此期间断开则放弃，由新 link 的 hello 重来。
//
// M11a：刚被认领的池 orb（pool_state = claimed）即使已 ready 过也用 setup.run（阶段名仍是 resuming）并置
// repo.update；setup.run 应答之后、BeforeReady 之前按需发布项目快照；setup 失败的 warming 池 orb 不进 ready、直接回收；
// 进入 ready 时 warming → idle、claimed → 离开池。
//
// S-P1-2：这次 setup.run 之后可能发布快照、且 orbd 支持 setup-defer-resume 时，setup.run 只跑 setup（deferResume），
// 快照拍完（无论发布成败）再单独 resume.run，快照里只留 setup 的产物；resume 的运行记录由 orbd 照常经 setup.result
// 上报。拍快照或 resume 期间 link 断开（sysbox 快照会停机再启动）则退回 connecting 由新 link 重跑本阶段：完成标记
// 已在，setup 不再执行、也就不再发布，deferResume 照旧，resume 随后补上。
func (s *Service) runPhase(l *orbLink) {
	select {
	case <-l.prepared:
	default:
		if s.hookAwaitPrepared != nil {
			s.hookAwaitPrepared(l.orbID)
		}
		select {
		case <-l.prepared:
		case <-l.ctl.Done():
			return
		}
	}
	ctx := context.Background()
	if l.runner {
		s.runnerReady(ctx, l.orbID)
		return
	}
	everReady, err := s.st.Q.OrbEverReady(ctx, l.orbID)
	if err != nil {
		slog.Warn("查询 orb 历史失败", "orb", l.orbID, "err", err)
		return
	}
	cur, err := s.Get(ctx, l.orbID)
	if err != nil {
		return
	}
	claimed := derefStr(cur.PoolState) == PoolClaimed
	phase, method := StateSettingUp, apigen.MethodSetupRun
	if everReady {
		phase, method = StateResuming, apigen.MethodResumeRun
		if claimed {
			method = apigen.MethodSetupRun
		}
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
	hash := plan.ConfigHash
	if plan.Params.Repo != nil && s.wantRepoUpdate(ctx, l, orb, everReady) {
		plan.Params.Repo.Update = new(true)
	}
	pristine := phase == StateSettingUp
	deferResume := pristine && method == apigen.MethodSetupRun && slices.Contains(l.hello.Features, FeatureSetupDeferResume) &&
		s.mayPublish(ctx, orb, hash)
	if deferResume {
		plan.Params.DeferResume = new(true)
	}
	s.setPolicy(l.orbID, plan.Egress)
	// linkLost 处理 ctl 调用因 link 断开失败：退回 connecting（开始 hello 计时），若新 link 已 hello 则立即重来。
	linkLost := func(during string) {
		slog.Info(during+"期间 link 断开", "orb", l.orbID, "phase", phase)
		if _, moved, _ := s.transition(ctx, l.orbID, phase, StateConnecting, map[string]any{"reason": "link lost during " + phase}); moved {
			s.advance(l.orbID)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.SetupTimeout)
	defer cancel()
	var res apigen.LinkCtlSetupRunResult
	err = l.ctl.Call(cctx, string(method), plan.Params, &res)
	if err != nil {
		if errors.Is(err, link.ErrCtlClosed) || l.ctl.Err() != nil {
			linkLost("setup / resume ")
			return
		}
		slog.Warn("setup / resume 失败", "orb", l.orbID, "phase", phase, "err", err)
		_, _, _ = s.transition(ctx, l.orbID, phase, StateFailed, map[string]any{"reason": err.Error()})
		return
	}
	if method == apigen.MethodSetupRun && res.Ok && res.SetupRan {
		// CP-5：只有真正跑过并成功的 setup 才改变环境已应用的配置；resume 与跳过的 setup 不算
		s.recordAppliedHash(ctx, l.orbID, hash)
	}
	detail := map[string]any{"ok": res.Ok, "setupRan": res.SetupRan, "durationMs": res.DurationMs}
	if !res.Ok {
		// 脚本失败不阻塞线程：orb 仍进入 ready（agent 要启动才能修复脚本），日志已经 setup.log 上报。
		if res.SetupError != nil {
			detail["setupError"] = *res.SetupError
		}
		slog.Warn("orb setup 脚本失败（继续）", "orb", l.orbID, "phase", phase, "err", detail["setupError"])
		if derefStr(orb.PoolState) == PoolWarming && s.failWarming(ctx, orb) {
			// 没有线程在等的池 orb：setup 失败就回收（项目退避 1 小时），不进 ready
			return
		}
	}
	if s.afterSetupSnapshot(ctx, l, orb, res, pristine, claimed, hash) && l.ctl.Err() != nil {
		// 提交快照期间容器被暂停或停机、link 断开：由新 link 重跑本阶段（完成标记已在，不会再次发布）。
		linkLost("快照")
		return
	}
	if deferResume {
		rp := plan.Params
		rp.DeferResume = nil
		rctx, rcancel := context.WithTimeout(ctx, s.cfg.SetupTimeout)
		var rres apigen.LinkCtlSetupRunResult
		err := l.ctl.Call(rctx, string(apigen.MethodResumeRun), rp, &rres)
		rcancel()
		if err != nil {
			if errors.Is(err, link.ErrCtlClosed) || l.ctl.Err() != nil {
				linkLost("推迟的 resume ")
				return
			}
			slog.Warn("推迟的 resume 失败", "orb", l.orbID, "phase", phase, "err", err)
			_, _, _ = s.transition(ctx, l.orbID, phase, StateFailed, map[string]any{"reason": err.Error()})
			return
		}
		detail["resumeOk"] = rres.Ok
	}
	if h := s.desktopHooksFor(l); h != nil {
		// Profile 必须在 agent 启动（ready）之前定下来。期间 link 断开则 Profile 可能没定好：
		// 与 setup 期间断开同样退回 connecting，由新 link 重跑本阶段（setup 有完成标记，不会重复执行脚本）。
		h.BeforeReady(ctx, l.orbID)
		if l.ctl.Err() != nil {
			slog.Info("Profile 同步期间 link 断开", "orb", l.orbID, "phase", phase)
			if _, moved, _ := s.transition(ctx, l.orbID, phase, StateConnecting, map[string]any{"reason": "link lost during " + phase}); moved {
				s.advance(l.orbID)
			}
			return
		}
	}
	_, _, _ = s.transitionThen(ctx, l.orbID, phase, StateReady, detail, func(t *txn) error {
		var err error
		switch derefStr(t.orb.PoolState) {
		case PoolWarming:
			_, err = t.q.SetOrbPoolState(t.ctx, db.SetOrbPoolStateParams{PoolState: new(PoolIdle), ID: t.orb.ID, Seen: PoolWarming})
		case PoolClaimed:
			_, err = t.q.SetOrbPoolState(t.ctx, db.SetOrbPoolStateParams{PoolState: nil, ID: t.orb.ID, Seen: PoolClaimed})
		}
		return err
	})
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
	if l.runner {
		s.armRunnerOffline(l.orbID)
	}
}

// hello 字段的上限（S4）：这些值会落库（orbs 版本、runners 行）并随 Orb / Runner 视图推给所有客户端，
// 而 ctl 单行上限有 96 MiB。超限一律截断 / 丢弃而不拒绝 link：诚实的 orbd 远在上限之内，不受影响。
const (
	maxHelloFeatures   = 32
	maxHelloFeatureLen = 64
	maxHelloDirs       = 64
	maxHelloPathLen    = 4096
	maxHelloTextLen    = 255
)

// sanitizeHello 返回收紧后的 hello：features 只留前 32 个合规项（非空、≤64 B、无控制字符）；runner.dirs 只留前
// 64 个合规项（path 非空、≤4096 B、绝对路径、无控制字符；name 去控制字符并截到 255 B）；hostname / os / arch /
// orbdVersion / agentVersion 去控制字符并截到 255 B。
func sanitizeHello(h apigen.LinkCtlHelloParams) apigen.LinkCtlHelloParams {
	h.OrbdVersion = clipText(h.OrbdVersion, maxHelloTextLen)
	h.AgentVersion = clipText(h.AgentVersion, maxHelloTextLen)
	h.Os = clipText(h.Os, maxHelloTextLen)
	h.Arch = clipText(h.Arch, maxHelloTextLen)
	if h.Features != nil {
		feats := make([]string, 0, min(len(h.Features), maxHelloFeatures))
		for _, f := range h.Features {
			if len(feats) == maxHelloFeatures {
				break
			}
			if f != "" && len(f) <= maxHelloFeatureLen && !hasControl(f) {
				feats = append(feats, f)
			}
		}
		h.Features = feats
	}
	if h.Runner != nil {
		info := apigen.LinkRunnerInfo{Hostname: clipText(h.Runner.Hostname, maxHelloTextLen)}
		if h.Runner.Dirs != nil {
			info.Dirs = make([]apigen.RunnerDir, 0, min(len(h.Runner.Dirs), maxHelloDirs))
			for _, d := range h.Runner.Dirs {
				if len(info.Dirs) == maxHelloDirs {
					break
				}
				if d.Path == "" || len(d.Path) > maxHelloPathLen || !filepath.IsAbs(d.Path) || hasControl(d.Path) {
					continue
				}
				info.Dirs = append(info.Dirs, apigen.RunnerDir{Path: d.Path, Name: clipText(d.Name, maxHelloTextLen), Git: d.Git})
			}
		}
		h.Runner = &info
	}
	return h
}

func hasControl(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }

// clipText 去掉 s 里的控制字符，并按 UTF-8 字符边界截到至多 n 字节。
func clipText(s string, n int) string {
	if len(s) <= n && !hasControl(s) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > n {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
