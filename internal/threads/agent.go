package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// agentConn 是一条 agent 流：写方向经独立 goroutine 串行写出，避免持锁时被背压阻塞。
type agentConn struct {
	st   *link.Stream
	out  chan []byte
	done chan struct{}
	once sync.Once
}

const agentOutBuffer = 1024

func newAgentConn(st *link.Stream) *agentConn {
	a := &agentConn{st: st, out: make(chan []byte, agentOutBuffer), done: make(chan struct{})}
	go a.writeLoop()
	return a
}

func (a *agentConn) writeLoop() {
	for {
		select {
		case <-a.done:
			return
		case line := <-a.out:
			if _, err := a.st.Write(append(line, '\n')); err != nil {
				a.close()
				return
			}
		}
	}
}

// send 非阻塞地排入一条命令帧；缓冲满或已关闭时返回 false。
func (a *agentConn) send(line []byte) bool {
	select {
	case <-a.done:
		return false
	default:
	}
	select {
	case a.out <- line:
		return true
	default:
		return false
	}
}

func (a *agentConn) close() {
	a.once.Do(func() {
		close(a.done)
		_ = a.st.Close()
	})
}

// pendingCmd 是已注入 id、等待应答的命令。
type pendingCmd struct {
	cmdType string
	// sent 为真表示已写到 agent 流（排队中的命令在流断开时不算失败）。
	sent bool
	// reply 把应答交给发起方（WS 连接）；internal 用于服务内部调用。二者至多其一。
	reply    func(json.RawMessage)
	internal chan json.RawMessage
	// after 在应答到达后、持有线程锁时执行（如 get_state 刷新缓冲）。
	after func(t *thread, resp omprpc.Envelope, data json.RawMessage)
}

// responseHead 是应答中本包关心的字段。
type responseHead struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

// ---- agent 启动 ----

// initFrames 按 C4 第 3 步构造 agent 初始化帧；M3 起在 set_subagent_subscription 之后开启终端
// （terminal_configure：pty:true 的命令与 agent 的 terminal 工具落到 omp 的终端宿主，由 Terminal 面板作答）。
func initFrames(t *thread) []apigen.OmpFrame {
	open := apigen.OmpFrame{"type": "open_session", "sessionDir": t.sessionDir}
	if m := parseModel(t.model); m != nil {
		open["provider"], open["modelId"] = m.Provider, m.ID
	}
	frames := []apigen.OmpFrame{
		open,
		{"type": "set_event_filter", "events": nil, "messageUpdates": "delta"},
		{"type": "set_ask_dialog", "enabled": true, "form": true},
		{"type": "set_subagent_subscription", "level": "progress"},
		{"type": "terminal_configure", "interactiveCommands": true, "agentTools": true},
	}
	if t.thinking != nil && *t.thinking != "" {
		frames = append(frames, apigen.OmpFrame{"type": "set_thinking_level", "level": *t.thinking})
	}
	return frames
}

// startAgent 在线程所在 orb 上执行 agent.start（orbd 侧幂等）。fork copy 时先复制会话目录。
func (s *Service) startAgent(ctx context.Context, threadID string) {
	t, err := s.get(ctx, threadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	if t.starting || t.orbID == "" {
		t.mu.Unlock()
		return
	}
	t.starting = true
	orbID, copyFrom := t.orbID, t.copyFrom
	params := apigen.LinkCtlAgentStartParams{ThreadID: t.id, SessionDir: t.sessionDir, Env: map[string]string{}, InitFrames: initFrames(t)}
	if t.agentStatus != apigen.AgentStatusReady {
		t.agentStatus = apigen.AgentStatusStarting
	}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.starting = false
		t.mu.Unlock()
	}()

	ctl, ok := s.orbs.Ctl(orbID)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.AgentStartTimeout)
	defer cancel()
	if copyFrom != "" {
		err := ctl.Call(cctx, "fs.copyTree", apigen.LinkCtlFsCopyTreeParams{From: sessionDirOf(copyFrom), To: t.sessionDir}, nil)
		if err != nil {
			slog.Warn("threads: 复制会话目录失败", "thread", t.id, "from", copyFrom, "err", err)
			if ctx.Err() == nil && !errors.Is(err, link.ErrCtlClosed) {
				s.markFailed(ctx, t)
			}
			return
		}
		t.mu.Lock()
		t.copyFrom = ""
		// 复制完成才推进持久化阶段：重启在此之前发生时会重新复制（agent 尚未启动，覆盖是安全的）
		var next *forkOp
		if t.forkEntry != "" && t.forkOp != nil {
			next = &forkOp{Stage: forkStageFork, Src: t.forkOp.Src, SrcOrb: t.forkOp.SrcOrb, ForkAt: t.forkEntry}
		}
		err = s.setForkOpLocked(ctx, t, next)
		t.mu.Unlock()
		if err != nil {
			return
		}
	}
	var res apigen.LinkCtlAgentStartResult
	if err := ctl.Call(cctx, "agent.start", params, &res); err != nil {
		slog.Warn("threads: agent.start 失败", "thread", t.id, "orb", orbID, "err", err)
		if !errors.Is(err, link.ErrCtlClosed) && !errors.Is(err, context.Canceled) {
			s.markFailed(ctx, t)
		}
		return
	}
	slog.Debug("threads: agent 已启动", "thread", t.id, "pid", res.Pid)
}

func (s *Service) markFailed(ctx context.Context, t *thread) {
	t.mu.Lock()
	t.failed = true
	// agent 没能启动：不会有在跑的回合，也不会再有 get_state 来重新同步
	t.live.SetWorking(false)
	t.settled, t.unsynced = true, false
	s.notifyReadyWaiters(t, apigen.AgentStatusExited)
	s.recompute(ctx, t)
	t.mu.Unlock()
}

// sessionDirOf 是线程在 orb 内的会话目录（契约 §3：控制面只发 `~/…`）。
func sessionDirOf(threadID string) string { return "~/.oryn/threads/" + threadID }

// ownThread 校验 threadId 属于发起 link 的 orb；不属于时关闭 link（快照副本 orb 继承的源线程只回错误）。
func (s *Service) ownThread(ctx context.Context, orbID, threadID string) (*thread, error) {
	t, err := s.get(ctx, threadID)
	if err == nil {
		t.mu.Lock()
		owner := t.orbID
		t.mu.Unlock()
		if owner == orbID {
			return t, nil
		}
		if owner != "" && s.snapshotOf(ctx, orbID, owner) {
			return nil, &link.CtlError{Code: "foreign_thread", Message: "thread belongs to the snapshot source orb"}
		}
	}
	slog.Warn("threads: orb 访问不属于它的线程，关闭 link", "orb", orbID, "thread", threadID)
	s.orbs.Kick(orbID, "thread "+threadID+" does not belong to this orb")
	return nil, &link.CtlError{Code: "forbidden", Message: "thread does not belong to this orb"}
}

// snapshotOf 报告 orbID 是否（经若干代）由 source 的快照创建。
func (s *Service) snapshotOf(ctx context.Context, orbID, source string) bool {
	for i := 0; i < 16 && orbID != ""; i++ {
		o, err := s.st.Q.GetOrb(ctx, orbID)
		if err != nil || o.SnapshotOf == nil {
			return false
		}
		if *o.SnapshotOf == source {
			return true
		}
		orbID = *o.SnapshotOf
	}
	return false
}

// ---- agent.status / agent.gap ----

func (s *Service) onAgentStatus(ctx context.Context, orbID string, params json.RawMessage) {
	var p apigen.LinkCtlAgentStatusParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	t, err := s.ownThread(ctx, orbID, p.ThreadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch p.Status {
	case "starting":
		t.agentStatus = apigen.AgentStatusStarting
	case "ready":
		t.agentStatus = apigen.AgentStatusReady
		t.failed = false
		s.onReadyLocked(ctx, t)
	case "exited":
		t.agentStatus = apigen.AgentStatusExited
		s.failPendingLocked(t, "agent exited", false)
		t.live.Reset()
		s.out().LiveReset(t.id)
		if p.RestartsLeft != nil && *p.RestartsLeft > 0 {
			// orbd 会重启它：重启后的 get_state 给出权威的运行 / 静止状态
			t.unsynced = true
		} else {
			// 不会再有进程：没有在跑的回合
			t.live.RestartExhausted()
			t.settled, t.unsynced = true, false
		}
		s.notifyReadyWaiters(t, apigen.AgentStatusExited)
		if p.StderrTail != nil {
			slog.Warn("threads: agent 退出", "thread", t.id, "exitCode", p.ExitCode, "stderr", *p.StderrTail)
		}
	}
	s.recompute(ctx, t)
}

// onTerminalGap 处理 orbd 的 terminal.gap：其帧缓冲溢出时丢弃过该线程的 terminal_output，
// 观看终端的客户端须经 terminal_snapshot 重同步。实时缓冲与线程状态不受影响。
func (s *Service) onTerminalGap(ctx context.Context, orbID string, params json.RawMessage) {
	var p apigen.LinkCtlTerminalGapParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	t, err := s.ownThread(ctx, orbID, p.ThreadID)
	if err != nil {
		return
	}
	s.out().TermLagged(t.id)
}

func (s *Service) onAgentGap(ctx context.Context, orbID string, params json.RawMessage) {
	var p apigen.LinkCtlAgentGapParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	t, err := s.ownThread(ctx, orbID, p.ThreadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	t.live.Reset()
	// 断连期间的帧（含应答、agent_end / session_settled）可能已丢失：等 get_state 重新同步
	t.unsynced = true
	s.failPendingLocked(t, "agent frames lost while disconnected", false)
	s.out().LiveReset(t.id)
	s.recompute(ctx, t)
	t.mu.Unlock()
	s.refreshState(t)
}

// onReadyLocked 在 agent 可接收命令时下发排队命令并刷新 state。调用方持有 t.mu。
func (s *Service) onReadyLocked(ctx context.Context, t *thread) {
	if !t.agentReady() {
		return
	}
	// 状态与流都到位才算就绪（二者经不同的 yamux 流到达，先后不定）
	t.live.AgentReady()
	s.notifyReadyWaiters(t, apigen.AgentStatusReady)
	if t.forkEntry != "" {
		entry := t.forkEntry
		t.forkEntry = ""
		frame, _ := json.Marshal(map[string]string{"type": "fork", "entryId": entry})
		// 持久化的 fork 阶段在 agent 应答后才清除（重启前未应答则重发）
		_ = s.enqueueLocked(t, frame, &pendingCmd{cmdType: "fork", after: func(t *thread, env omprpc.Envelope, _ json.RawMessage) {
			if undelivered(env) {
				// 进程在 fork 到达前退出：重启后的进程就绪时重发
				t.forkEntry = entry
				return
			}
			_ = s.setForkOpLocked(context.Background(), t, nil)
		}})
	}
	if st := s.orbState(t.orbID); st == "freezing" || st == "stopping" {
		// 冻结闸门已提交、容器即将暂停：写进流的命令要等解冻才会被处理，而没有人会去唤醒。
		// 排队并请求唤醒（orbs 在暂停完成后执行）；orb 回到非暂停状态时 handleOrbEvent 下发队列。
		// 其余「暂停」状态出现在这里只可能是缓存滞后：agent 能上报就绪，说明进程在跑。
		if len(t.queue) > 0 {
			s.wake(t.id, t.orbID)
		}
		return
	}
	q := t.queue
	t.queue = nil
	for _, f := range q {
		s.dispatchLocked(t, f)
	}
	s.refreshStateLocked(t)
}

func (s *Service) notifyReadyWaiters(t *thread, st apigen.AgentStatus) {
	for _, ch := range t.readyWaiters {
		select {
		case ch <- st:
		default:
		}
	}
	t.readyWaiters = nil
}

// failPendingLocked 以合成的失败应答结束已下发的未决命令（agent 退出或流断开后它们不会再有应答）；
// all 为真时连同排队中的命令一起结束（线程删除）。
func (s *Service) failPendingLocked(t *thread, reason string, all bool) {
	for id, p := range t.pending {
		if !p.sent && !all {
			continue
		}
		resp, _ := json.Marshal(map[string]any{"type": "response", "id": id, "command": p.cmdType, "success": false, "error": reason})
		s.deliver(p, resp)
		delete(t.pending, id)
	}
}

func (s *Service) deliver(p *pendingCmd, resp json.RawMessage) {
	switch {
	case p.reply != nil:
		p.reply(resp)
	case p.internal != nil:
		select {
		case p.internal <- resp:
		default:
		}
	}
}

// ---- agent 流 ----

func (s *Service) onAgentStream(ctx context.Context, orbID string, h link.StreamHeader, st *link.Stream) {
	t, err := s.ownThread(ctx, orbID, h.ThreadID)
	if err != nil {
		return
	}
	ac := newAgentConn(st)
	defer ac.close()
	t.mu.Lock()
	if t.agent != nil {
		t.agent.close()
	}
	t.agent = ac
	s.onReadyLocked(ctx, t)
	s.recompute(ctx, t)
	t.mu.Unlock()

	r := omprpc.NewReader(st, omprpc.MaxReassembledBytes+4096)
	for {
		line, err := r.ReadLine()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, link.ErrCtlClosed) && ctx.Err() == nil {
				slog.Debug("threads: agent 流结束", "thread", t.id, "err", err)
			}
			break
		}
		s.onFrame(ctx, t, orbID, line)
	}
	t.mu.Lock()
	if t.agent == ac {
		// 已下发命令的应答由 orbd 缓冲，重连后在新流上补发，因此这里不结束它们
		t.agent = nil
		s.recompute(ctx, t)
	}
	t.mu.Unlock()
}

// frameHeadExtra 是线程服务（而非实时缓冲）关心的帧字段。
type frameHeadExtra struct {
	Type  string  `json:"type"`
	Title *string `json:"title"`
}

func (s *Service) onFrame(ctx context.Context, t *thread, orbID string, line []byte) {
	env, err := omprpc.ParseEnvelope(line)
	if err != nil {
		return
	}
	frame := json.RawMessage(line)
	// 终端输出走热路径：不进实时缓冲、不算 agent 活动（开着 dev server 的 orb 仍能冻结）、不重算状态，
	// 也不取线程锁——它是可丢弃、可经 terminal_snapshot 重同步的流。
	if env.Type == "terminal_output" {
		s.out().TermOutput(t.id, frame)
		return
	}
	if env.IsResponse() {
		s.onResponse(ctx, t, env, frame)
		return
	}
	var x frameHeadExtra
	_ = json.Unmarshal(line, &x)

	t.mu.Lock()
	eff := t.live.ApplyFrame(frame)
	switch x.Type {
	case "agent_start":
		t.settled = false
	case "session_settled":
		t.settled = true
	case "session_info_update":
		if x.Title != nil && *x.Title != "" {
			if n, err := s.st.Q.SetThreadAutoTitle(ctx, db.SetThreadAutoTitleParams{Title: *x.Title, UpdatedAt: s.now(), ID: t.id}); err == nil && n > 0 {
				s.publish(ctx, t.id)
			}
		}
	}
	s.out().Frame(t.id, frame)
	if eff.UIResolved != "" {
		s.out().UIResolved(t.id, eff.UIResolved)
	}
	if eff.RefreshState {
		s.refreshStateLocked(t)
	}
	s.recompute(ctx, t)
	t.mu.Unlock()
	if eff.AgentActivity {
		s.touchAgent(orbID)
	}
}

// onResponse 把应答交给发起方，并处理少数会改变线程状态的应答。
func (s *Service) onResponse(ctx context.Context, t *thread, env omprpc.Envelope, frame json.RawMessage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.pending[env.ID]
	if p == nil {
		return
	}
	delete(t.pending, env.ID)
	var r responseHead
	_ = json.Unmarshal(frame, &r)
	switch p.cmdType {
	case "switch_leaf", "navigate_tree":
		var d struct {
			ActiveLeafID *string `json:"activeLeafId"`
		}
		if r.Success && json.Unmarshal(r.Data, &d) == nil && d.ActiveLeafID != nil {
			t.leaf = d.ActiveLeafID
			_ = s.st.Q.SetThreadLeaf(ctx, db.SetThreadLeafParams{LeafEntryID: t.leaf, ID: t.id})
			s.publish(ctx, t.id)
		}
	case "plan_approval":
		if r.Success {
			t.live.PlanProposal = nil
			s.out().PlanResolved(t.id)
		}
	case "prompt", "abort_and_prompt":
		var d struct {
			AgentInvoked *bool `json:"agentInvoked"`
		}
		_ = json.Unmarshal(r.Data, &d)
		if !r.Success || (d.AgentInvoked != nil && !*d.AgentInvoked) {
			t.live.working = false
		}
	}
	if p.after != nil {
		p.after(t, env, r.Data)
	}
	s.recompute(ctx, t)
	s.deliver(p, frame)
}

// ---- 命令下发 ----

// dispatchLocked 把已注入 id 的命令写到 agent 流。调用方持有 t.mu 且 agent 就绪。
func (s *Service) dispatchLocked(t *thread, frame []byte) {
	var h struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	_ = json.Unmarshal(frame, &h)
	if !t.agent.send(frame) {
		if p := t.pending[h.ID]; p != nil {
			delete(t.pending, h.ID)
			resp, _ := json.Marshal(map[string]any{"type": "response", "id": h.ID, "command": h.Type, "success": false, "error": "agent stream busy"})
			s.deliver(p, resp)
		}
		return
	}
	if p := t.pending[h.ID]; p != nil {
		p.sent = true
	}
	switch h.Type {
	case "prompt", "abort_and_prompt":
		// prompt 的应答先于 agent_start 到达；乐观地进入 working，避免中间闪现 idle
		t.live.working = true
		t.settled = false
	}
}

// enqueueLocked 登记未决命令并下发或排队；返回错误码（queue_full）或 nil。调用方持有 t.mu。
func (s *Service) enqueueLocked(t *thread, frame []byte, p *pendingCmd) error {
	var h struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(frame, &h)
	if h.ID == "" {
		id := s.newCmdID()
		var err error
		if frame, err = omprpc.SetID(frame, id); err != nil {
			return errf(RouteInvalidFrame, err.Error())
		}
		h.ID = id
	}
	// orb 冻结 / 停机 / 唤醒途中，现有 agent 流即将随 link 重建而作废，写入的命令会丢失：先排队，
	// 新流上报 agent.status ready 后由 onReadyLocked 下发。
	if t.agentReady() && !orbSuspended(s.orbState(t.orbID)) {
		if p != nil {
			t.pending[h.ID] = p
		}
		s.dispatchLocked(t, frame)
		return nil
	}
	if len(t.queue) >= queueLimit {
		return errf("queue_full", fmt.Sprintf("thread command queue is full (%d)", queueLimit))
	}
	if p != nil {
		t.pending[h.ID] = p
	}
	t.queue = append(t.queue, frame)
	t.settled = false
	return nil
}

func (s *Service) newCmdID() string { return fmt.Sprintf("oryn-%d", s.nextID.Add(1)) }

// refreshStateLocked 向 agent 发 get_state 并把结果写入实时缓冲。调用方持有 t.mu。
func (s *Service) refreshStateLocked(t *thread) {
	if !t.agentReady() {
		return
	}
	frame := []byte(`{"type":"get_state"}`)
	_ = s.enqueueLocked(t, frame, &pendingCmd{cmdType: "get_state", after: func(t *thread, env omprpc.Envelope, data json.RawMessage) {
		if !env.OK() || len(data) == 0 {
			return
		}
		rs := t.live.SetState(data)
		if rs.IsStreaming != nil && *rs.IsStreaming {
			t.settled = false
		}
		if t.unsynced {
			// 重新同步（重启 / gap / agent 重启）后的第一份快照：以它恢复 working 与 settled（CP10）
			if rs.IsStreaming != nil {
				t.live.SetWorking(*rs.IsStreaming)
			}
			if rs.IsSettled != nil {
				t.settled = *rs.IsSettled
			}
			t.unsynced = false
		}
		s.syncModelLocked(t, data)
	}})
}

func (s *Service) refreshState(t *thread) {
	t.mu.Lock()
	s.refreshStateLocked(t)
	t.mu.Unlock()
}

// syncModelLocked 把 state 中的模型与思考级别写回线程行（agent 重启时 open_session 沿用）。
func (s *Service) syncModelLocked(t *thread, state json.RawMessage) {
	var st struct {
		Model *struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
		} `json:"model"`
		ThinkingLevel *string `json:"thinkingLevel"`
	}
	if json.Unmarshal(state, &st) != nil {
		return
	}
	var model *string
	if st.Model != nil {
		model = modelString(&apigen.ModelRef{Provider: st.Model.Provider, ID: st.Model.ID})
	}
	if eqPtr(model, t.model) && eqPtr(st.ThinkingLevel, t.thinking) {
		return
	}
	t.model, t.thinking = model, st.ThinkingLevel
	ctx := context.Background()
	if err := s.st.Q.SetThreadModel(ctx, db.SetThreadModelParams{Model: model, ThinkingLevel: st.ThinkingLevel, UpdatedAt: s.now(), ID: t.id}); err == nil {
		s.publish(ctx, t.id)
	}
}

func eqPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Command 处理客户端 `cmd`：路由检查、注入 id、下发或排队（orb 未就绪时触发唤醒）。
// reply 收到 omp 应答（id 仍为注入值，由调用方换回客户端 id）。
func (s *Service) Command(ctx context.Context, threadID string, frame json.RawMessage, reply func(json.RawMessage)) error {
	route, cmdType := RouteCommand(frame)
	switch route {
	case RouteInvalidFrame:
		return errf(RouteInvalidFrame, "frame must be an omp command object with a type")
	case RouteManaged:
		return errf(RouteManaged, cmdType+" is managed by Oryn")
	case RouteUseOrynFork:
		return errf(RouteUseOrynFork, "use POST /api/v1/threads/{id}/fork")
	}
	t, err := s.get(ctx, threadID)
	if err != nil {
		return err
	}
	line, err := omprpc.SetID(frame, s.newCmdID())
	if err != nil {
		return errf(RouteInvalidFrame, err.Error())
	}
	t.mu.Lock()
	if t.orbID == "" {
		t.mu.Unlock()
		return errf("no_orb", "thread has no orb")
	}
	orbID := t.orbID
	if err := s.enqueueLocked(t, line, &pendingCmd{cmdType: cmdType, reply: reply}); err != nil {
		t.mu.Unlock()
		return err
	}
	if cmdType == "terminal_resize" {
		// omp 不为 resize 发帧：由控制面告知其它窗口（含发起方）当前尺寸归属
		var rz struct {
			TerminalID string `json:"terminalId"`
			Cols       int    `json:"cols"`
			Rows       int    `json:"rows"`
		}
		if json.Unmarshal(frame, &rz) == nil && rz.TerminalID != "" && rz.Cols > 0 && rz.Rows > 0 {
			s.out().TermResized(t.id, rz.TerminalID, rz.Cols, rz.Rows)
		}
	}
	needWake := !t.agentReady() || s.orbState(orbID) != orbStateReady // 冻结的 orb 上 agent 流仍在，但进程已暂停
	s.clearUnreadLocked(ctx, t)
	s.recompute(ctx, t)
	t.mu.Unlock()
	s.touchUser(orbID)
	if needWake {
		s.wake(threadID, orbID)
	}
	return nil
}

// call 向线程 agent 发送一条内部命令并等待应答（必要时唤醒 orb）。
func (s *Service) call(ctx context.Context, t *thread, frame []byte) (omprpc.Envelope, json.RawMessage, error) {
	ch := make(chan json.RawMessage, 1)
	t.mu.Lock()
	orbID := t.orbID
	if orbID == "" {
		t.mu.Unlock()
		return omprpc.Envelope{}, nil, errf("no_orb", "thread has no orb")
	}
	var typ struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(frame, &typ)
	err := s.enqueueLocked(t, frame, &pendingCmd{cmdType: typ.Type, internal: ch})
	needWake := !t.agentReady() || s.orbState(orbID) != orbStateReady // 冻结的 orb 上 agent 流仍在，但进程已暂停
	t.mu.Unlock()
	if err != nil {
		return omprpc.Envelope{}, nil, err
	}
	if needWake {
		s.wake(t.id, orbID)
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout)
	defer cancel()
	select {
	case resp := <-ch:
		env, err := omprpc.ParseEnvelope(resp)
		if err != nil {
			return env, nil, err
		}
		var r responseHead
		_ = json.Unmarshal(resp, &r)
		if !env.OK() {
			return env, r.Data, errf("agent_error", fmt.Sprintf("%s: %s", typ.Type, env.ErrorText()))
		}
		return env, r.Data, nil
	case <-cctx.Done():
		return omprpc.Envelope{}, nil, errf("timeout", typ.Type+" timed out")
	}
}

// wake 唤醒线程所在 orb；orb 已就绪但 agent 未运行时直接启动 agent。
func (s *Service) wake(threadID, orbID string) {
	s.go_(func(ctx context.Context) {
		if s.orbState(orbID) == orbStateReady {
			s.startAgent(ctx, threadID)
			return
		}
		if err := s.orbs.Wake(ctx, orbID); err != nil {
			slog.Warn("threads: 唤醒 orb 失败", "orb", orbID, "err", err)
		}
	})
}

// Wake 对应 `POST threads/{id}/wake`。
func (s *Service) Wake(ctx context.Context, threadID string) error {
	t, err := s.get(ctx, threadID)
	if err != nil {
		return err
	}
	t.mu.Lock()
	orbID := t.orbID
	t.mu.Unlock()
	if orbID == "" {
		return errf("no_orb", "thread has no orb")
	}
	s.touchUser(orbID)
	if s.orbState(orbID) == orbStateReady {
		s.go_(func(ctx context.Context) { s.startAgent(ctx, threadID) })
		return nil
	}
	return s.orbs.Wake(ctx, orbID)
}

// ErrUIResolved 表示 UI 请求已被其它客户端作答（先到先得）。
var ErrUIResolved = &Error{Code: "ui_resolved", Message: "ui request already resolved"}

// UIResponse 处理客户端 `ui.response`：只接受侧通道应答（RouteSideChannel），普通命令必须走 Command 的路由检查。
// extension_ui_response 先到先得：请求仍待答、且应答已成功写入 agent 流或排队之后才移除请求并广播 ui.resolved。
// agent 流已建立但仍在启动（未 ready）时直接写入：omp 在初始化期间就接受 UI 应答，扩展的 session_start
// 可能正等着它才能就绪（OD1）。orb 暂停 / 恢复途中与命令一样排队并唤醒。
func (s *Service) UIResponse(ctx context.Context, threadID string, frame json.RawMessage) error {
	typ, id, ok := RouteSideChannel(frame)
	if !ok {
		return errf(RouteInvalidFrame, "frame must be a side-channel response (extension_ui_response, host_tool_result, host_tool_update, host_uri_result) with an id")
	}
	t, err := s.get(ctx, threadID)
	if err != nil {
		return err
	}
	t.mu.Lock()
	orbID := t.orbID
	if orbID == "" {
		t.mu.Unlock()
		return errf("no_orb", "thread has no orb")
	}
	isUI := typ == "extension_ui_response"
	if isUI && !t.live.HasUI(id) {
		t.mu.Unlock()
		return ErrUIResolved
	}
	orbState := s.orbState(orbID)
	needWake := false
	switch {
	case t.agent != nil && !orbSuspended(orbState):
		if !t.agent.send(append([]byte(nil), frame...)) {
			t.mu.Unlock()
			return errf("agent_busy", "agent stream busy")
		}
	case len(t.queue) >= queueLimit:
		t.mu.Unlock()
		return errf("queue_full", "thread command queue is full")
	default:
		t.queue = append(t.queue, append([]byte(nil), frame...))
		needWake = !t.agentReady() || orbState != orbStateReady
	}
	if isUI {
		t.live.ResolveUI(id)
		s.out().UIResolved(t.id, id)
	}
	s.clearUnreadLocked(ctx, t)
	s.recompute(ctx, t)
	t.mu.Unlock()
	s.touchUser(orbID)
	if needWake {
		s.wake(threadID, orbID)
	}
	return nil
}

// Focus 记录连接当前查看的线程（空串 = 无）：清未读、记用户交互、按配置唤醒。
func (s *Service) Focus(ctx context.Context, connID, threadID string) {
	s.mu.Lock()
	if threadID == "" {
		delete(s.focus, connID)
	} else {
		s.focus[connID] = threadID
	}
	s.mu.Unlock()
	if threadID == "" {
		return
	}
	t, err := s.get(ctx, threadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	orbID := t.orbID
	s.clearUnreadLocked(ctx, t)
	// 与命令准入相同的判定：冻结中的 orb 上 agent 流仍在、agentReady 仍为真，但进程已暂停
	needWake := orbID != "" && (!t.agentReady() || s.orbState(orbID) != orbStateReady)
	t.mu.Unlock()
	s.touchUser(orbID)
	if s.cfg.WakeOnFocus && needWake {
		s.wake(threadID, orbID)
	}
}

func (s *Service) clearUnreadLocked(ctx context.Context, t *thread) {
	if !t.unread {
		return
	}
	t.unread = false
	if err := s.st.Q.SetThreadUnread(ctx, db.SetThreadUnreadParams{Unread: false, ID: t.id}); err == nil {
		s.publish(ctx, t.id)
	}
}

// RenameSession 在用户改名后通知 agent（set_session_name）。
func (s *Service) renameSession(t *thread, name string) {
	frame, _ := json.Marshal(map[string]string{"type": "set_session_name", "name": name})
	t.mu.Lock()
	if t.orbID != "" {
		_ = s.enqueueLocked(t, frame, &pendingCmd{cmdType: "set_session_name"})
	}
	t.mu.Unlock()
}

// isMainFile 报告条目文件是否为主会话文件（而非子 agent 转录）。
func isMainFile(file string) bool { return !IsSubagentFile(file) && strings.HasSuffix(file, ".jsonl") }
