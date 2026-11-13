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
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/glyph"
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
		// 冻结闸门已提交、容器即将暂停：写进流的命令要等解冻才会被处理。排队；队列里有用户命令时请求唤醒
		// （orbs 在暂停完成后执行）；orb 回到非暂停状态时 handleOrbEvent 下发队列。只有只读查询时不唤醒：
		// 它们不是用户交互，唤醒会让停机 / 冻结刚完成就被撤销。
		// 其余「暂停」状态出现在这里只可能是缓存滞后：agent 能上报就绪，说明进程在跑。
		if t.queueNeedsWake() {
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
// all 为真时连同排队中的命令与消息顺序队列里等待的消息一起结束（线程删除；仍在投递的消息由 runDelivery 结束）。
func (s *Service) failPendingLocked(t *thread, reason string, all bool) {
	for id, p := range t.pending {
		if !p.sent && !all {
			continue
		}
		resp, _ := json.Marshal(map[string]any{"type": "response", "id": id, "command": p.cmdType, "success": false, "error": reason})
		s.deliver(p, resp)
		delete(t.pending, id)
	}
	if all {
		for _, sl := range t.msgOrder {
			if sl.ready && sl.frame != nil {
				s.failCmd(sl.p, reason)
			}
		}
		t.msgOrder = nil
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
			s.adoptProjectIcon(ctx, t.id, *x.Title)
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
	s.setActivityLocked(ctx, t, frameActivity(x.Type, line))
	t.mu.Unlock()
	if eff.AgentActivity {
		s.touchAgent(orbID)
	}
}

// adoptProjectIcon 在 omp 自动标题带卡片图标（`<图标> <代码>: <标题>`）且线程所属项目尚无图标时，
// 把该图标写入项目（只填一次，不覆盖手选）。项目没有实时推送：前端在项目列表下次刷新时看到。
func (s *Service) adoptProjectIcon(ctx context.Context, threadID, title string) {
	card, ok := glyph.ParseTitleCard(title)
	if !ok {
		return
	}
	row, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil || row.ProjectID == nil {
		return
	}
	if _, err := s.st.Q.AdoptProjectIcon(ctx, db.AdoptProjectIconParams{Icon: &card.Icon, UpdatedAt: s.now(), ID: *row.ProjectID}); err != nil {
		slog.Warn("threads: 写入项目图标失败", "thread", threadID, "project", *row.ProjectID, "err", err)
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
	// 不在这里清 settled：排队的命令由 len(t.queue) 阻止静止；会开启回合的 prompt / abort_and_prompt 在下发时
	// （dispatchLocked）清 settled。其余命令（get_state 等查询）下发后不会有回合，也就不会有 session_settled——
	// 在这里清 settled 会让它永远为 false，orb 永不冻结。
	t.queue = append(t.queue, frame)
	return nil
}

// queueNeedsWake 报告排队的命令里是否有值得唤醒 orb 的（只读查询之外的命令）。调用方持有 t.mu。
func (t *thread) queueNeedsWake() bool {
	for _, f := range t.queue {
		if typ, _, ok := frameHeader(f); !ok || !passiveCommand(typ) {
			return true
		}
	}
	return false
}

// passiveLocked 处理客户端的只读查询（见 passiveCommand）：agent 可接收命令时直接下发；orb 在恢复途中、agent 正在启动
// 或等 link 重连（agentStatus 为 starting / ready）时排队，就绪后由 onReadyLocked 下发；其余情况（orb 正在或已经挂起、
// agent 未运行）立即拒绝，免得查询在队列里滞留、阻止静止。从不唤醒 orb、不记用户交互、不清未读。调用方持有 t.mu。
func (s *Service) passiveLocked(t *thread, line []byte, p *pendingCmd) error {
	st := s.orbState(t.orbID)
	if !t.agentReady() || orbSuspended(st) {
		if orbGoingDown(st) || (t.agentStatus != apigen.AgentStatusStarting && t.agentStatus != apigen.AgentStatusReady) {
			return errf("agent_unavailable", fmt.Sprintf("%s: agent is not running (orb %s); read-only queries do not wake the orb", p.cmdType, st))
		}
	}
	return s.enqueueLocked(t, line, p)
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

// Command 处理客户端 `cmd`：路由检查、注入 id、下发或排队（orb 未就绪时触发唤醒）。只读查询（passiveCommand）
// 不算用户交互：从不唤醒 orb，orb 挂起时直接拒绝（见 passiveLocked）。
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
	ids, frame, err := takeAttachments(cmdType, frame)
	if err != nil {
		return err
	}
	t, err := s.get(ctx, threadID)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return s.commandWithAttachments(ctx, t, cmdType, frame, ids, reply)
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
	if passiveCommand(cmdType) {
		err := s.passiveLocked(t, line, &pendingCmd{cmdType: cmdType, reply: reply})
		t.mu.Unlock()
		return err
	}
	p := &pendingCmd{cmdType: cmdType, reply: reply}
	if messageCommands[cmdType] && len(t.msgOrder) > 0 {
		// 前面有仍在投递附件的消息：按受理顺序排在它后面
		sl, err := s.reserveMsgLocked(t, false)
		if err != nil {
			t.mu.Unlock()
			return err
		}
		s.readyMsgLocked(t, sl, line, p)
	} else if err := s.enqueueLocked(t, line, p); err != nil {
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

// ---- 消息附件（M3 计划 5.3）----

// orynAttachmentsKey 是客户端在 prompt / steer / follow_up / abort_and_prompt 帧上携带附件 id 的 Oryn 扩展字段；
// 控制面在下发前删除它（omp 永远看不到），投递完成后把附件块追加到 message 末尾。
const orynAttachmentsKey = "orynAttachments"

// maxAttachmentsPerMessage 是每条消息的附件数上限（与 internal/attachments.MaxPerMessage 一致）。
const maxAttachmentsPerMessage = 20

// deliverTimeout 是一次附件投递（最多 20 × 100MiB 经 link 上传）的上限：超时视为投递失败，避免线程永远不静止。
const deliverTimeout = 30 * time.Minute

// messageCommands 是消息命令（都有 message 字段）：可以带附件，且按受理顺序下发（见 thread.msgOrder）。
var messageCommands = map[string]bool{"prompt": true, "steer": true, "follow_up": true, "abort_and_prompt": true}

// takeAttachments 取出并删除帧上的 orynAttachments，返回附件 id 与去掉该字段后的帧；没有该字段时原样返回。
func takeAttachments(cmdType string, frame json.RawMessage) ([]string, json.RawMessage, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(frame, &m) != nil {
		return nil, frame, nil // RouteCommand 已校验过帧
	}
	raw, has := m[orynAttachmentsKey]
	if !has {
		return nil, frame, nil
	}
	delete(m, orynAttachmentsKey)
	var ids []string
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, nil, errf(RouteInvalidFrame, "orynAttachments must be an array of attachment ids")
		}
	}
	if len(ids) > 0 && !messageCommands[cmdType] {
		return nil, nil, errf(RouteInvalidFrame, cmdType+" does not accept orynAttachments")
	}
	if len(ids) > maxAttachmentsPerMessage {
		return nil, nil, errf("invalid", fmt.Sprintf("at most %d attachments per message", maxAttachmentsPerMessage))
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return nil, nil, errf("invalid", "orynAttachments must be distinct, non-empty attachment ids")
		}
		seen[id] = true
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, nil, errf(RouteInvalidFrame, err.Error())
	}
	return ids, out, nil
}

// attachmentBlockHead 是附件块的固定开头（前端据此识别并渲染为附件芯片）。
const attachmentBlockHead = "<oryn-attachments>\nThe user attached these files. They are stored outside the repository; " +
	"read them when relevant and do not copy them into the repository unless asked.\n"

// attachmentBlock 构造追加到消息末尾的附件块。
func attachmentBlock(d []DeliveredAttachment) string {
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(attachmentBlockHead)
	for _, a := range d {
		m := a.Mime
		if m == "" {
			m = "application/octet-stream"
		}
		fmt.Fprintf(&b, "- %s (%s, %d bytes)\n", a.Path, m, a.Size)
	}
	b.WriteString("</oryn-attachments>")
	return b.String()
}

// appendMessage 把 text 追加到帧的 message 字段（缺省为空串；空消息时去掉前导空行）。
func appendMessage(frame []byte, text string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(frame, &m); err != nil {
		return nil, err
	}
	var msg string
	if raw, ok := m["message"]; ok {
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, fmt.Errorf("message must be a string")
		}
	}
	if msg == "" {
		text = strings.TrimLeft(text, "\n")
	}
	b, _ := json.Marshal(msg + text)
	m["message"] = b
	return json.Marshal(m)
}

// codedError 是带业务错误码的错误（internal/attachments.Error 实现）。
type codedError interface{ Coded() (code, message string) }

// asThreadErr 把附件服务的业务错误转成本包的 *Error（API 据错误码映射状态）。
func asThreadErr(err error) error {
	var c codedError
	if errors.As(err, &c) {
		code, msg := c.Coded()
		return errf(code, msg)
	}
	return err
}

// commandWithAttachments 处理带附件的客户端命令：受理时即在消息顺序队列里占位，记用户交互、必要时唤醒，
// 等 agent 就绪后投递附件、追加附件块，再按受理顺序入队；投递失败时以合成的失败应答结束该命令，并向线程广播一条 notice。
func (s *Service) commandWithAttachments(ctx context.Context, t *thread, cmdType string, frame []byte, ids []string, reply func(json.RawMessage)) error {
	if s.attach == nil {
		return errf("invalid", "attachments are not supported")
	}
	t.mu.Lock()
	if t.orbID == "" {
		t.mu.Unlock()
		return errf("no_orb", "thread has no orb")
	}
	orbID := t.orbID
	if err := s.startDeliveryLocked(t, frame, ids, &pendingCmd{cmdType: cmdType, reply: reply}, false, s.cfg.CallTimeout); err != nil {
		t.mu.Unlock()
		return err
	}
	needWake := !t.agentReady() || s.orbState(orbID) != orbStateReady
	s.clearUnreadLocked(ctx, t)
	s.recompute(ctx, t)
	t.mu.Unlock()
	s.touchUser(orbID)
	if needWake {
		s.wake(t.id, orbID)
	}
	return nil
}

// reserveMsgLocked 在消息顺序队列里占一个位置（front 时插到最前：重排的首条提示词先于其后受理的消息）。
// 调用方持有 t.mu。
func (s *Service) reserveMsgLocked(t *thread, front bool) (*msgSlot, error) {
	if !front && len(t.queue)+len(t.msgOrder) >= queueLimit {
		return nil, errf("queue_full", fmt.Sprintf("thread command queue is full (%d)", queueLimit))
	}
	sl := &msgSlot{}
	if front {
		t.msgOrder = append([]*msgSlot{sl}, t.msgOrder...)
	} else {
		t.msgOrder = append(t.msgOrder, sl)
	}
	return sl, nil
}

// readyMsgLocked 让 sl 可下发（frame 为 nil 表示该消息已失败、出队时跳过），并按受理顺序把队首起连续就绪的
// 消息交给 enqueueLocked（下发或进 t.queue）。调用方持有 t.mu。
func (s *Service) readyMsgLocked(t *thread, sl *msgSlot, frame []byte, p *pendingCmd) {
	sl.frame, sl.p, sl.ready = frame, p, true
	for len(t.msgOrder) > 0 && t.msgOrder[0].ready {
		head := t.msgOrder[0]
		t.msgOrder[0] = nil
		t.msgOrder = t.msgOrder[1:]
		if head.frame == nil {
			continue
		}
		if err := s.enqueueLocked(t, head.frame, head.p); err != nil {
			s.failCmd(head.p, err.Error())
		}
	}
}

// startDeliveryLocked 在消息顺序队列里占位、登记一次在途投递并在后台执行。fallback 为真（新建线程的首条提示词，
// 没有发起方可回复）时占最前的位置，投递失败仍发送命令，message 末尾注明失败原因。调用方持有 t.mu。
// 不在这里清 settled：在途投递由 deliveries 阻止静止，命令入队 / 下发时各自维护 settled。
func (s *Service) startDeliveryLocked(t *thread, frame []byte, ids []string, p *pendingCmd, fallback bool, wait time.Duration) error {
	sl, err := s.reserveMsgLocked(t, fallback)
	if err != nil {
		return err
	}
	t.deliveries++
	s.go_(func(ctx context.Context) { s.runDelivery(ctx, t, sl, frame, ids, p, fallback, wait) })
	return nil
}

func (s *Service) runDelivery(ctx context.Context, t *thread, sl *msgSlot, frame []byte, ids []string, p *pendingCmd, fallback bool, wait time.Duration) {
	text, err := s.deliverAttachments(ctx, t, ids, wait)
	if err != nil && ctx.Err() != nil {
		// 服务关闭取消了投递（不是投递失败）：不发降级提示词、不确认首批命令——持久化的首批命令留待重启后续作；
		// 槽位保持占用，其后的消息不会越过它。
		t.mu.Lock()
		t.deliveries--
		t.mu.Unlock()
		if !fallback {
			s.failCmd(p, "control plane is shutting down")
		}
		return
	}
	var reason string
	if err != nil {
		reason = "attachment delivery failed: " + err.Error()
		slog.Warn("threads: 附件投递失败", "thread", t.id, "err", err)
		if fallback {
			text = "\n\n[Oryn: " + reason + "]"
		}
	}
	final := frame
	if err == nil || fallback {
		if f, aerr := appendMessage(frame, text); aerr == nil {
			final = f
		} else if err == nil {
			err, reason = aerr, "attachment delivery failed: "+aerr.Error()
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deliveries--
	s.mu.Lock()
	live := s.threads[t.id] == t
	s.mu.Unlock()
	if !live {
		s.failCmd(p, "thread deleted")
		return
	}
	if reason != "" {
		notice, _ := json.Marshal(map[string]string{"type": "notice", "level": "error", "message": reason, "source": "oryn"})
		s.out().Frame(t.id, notice)
	}
	if err != nil && !fallback {
		s.failCmd(p, reason)
		s.readyMsgLocked(t, sl, nil, nil)
	} else {
		s.readyMsgLocked(t, sl, final, p)
	}
	needWake := !t.agentReady() || s.orbState(t.orbID) != orbStateReady
	s.recompute(ctx, t)
	if needWake && t.orbID != "" && (len(t.queue) > 0 || len(t.pending) > 0) {
		s.wake(t.id, t.orbID)
	}
}

// failCmd 以合成的失败应答结束尚未入队的命令。
func (s *Service) failCmd(p *pendingCmd, reason string) {
	resp, _ := json.Marshal(map[string]any{"type": "response", "id": s.newCmdID(), "command": p.cmdType, "success": false, "error": reason})
	s.deliver(p, resp)
}

// deliverAttachments 等 agent 与 orb 就绪后把附件投递到 orb，返回要追加到消息末尾的附件块。
func (s *Service) deliverAttachments(ctx context.Context, t *thread, ids []string, wait time.Duration) (string, error) {
	if s.attach == nil {
		return "", errors.New("attachments are not supported")
	}
	if err := s.waitAgentReady(ctx, t, wait); err != nil {
		return "", err
	}
	t.mu.Lock()
	orbID := t.orbID
	t.mu.Unlock()
	dctx, cancel := context.WithTimeout(ctx, deliverTimeout)
	defer cancel()
	d, err := s.attach.Deliver(dctx, orbID, t.id, ids)
	if err != nil {
		return "", err
	}
	return attachmentBlock(d), nil
}

// waitAgentReady 等到线程的 agent 可接收命令且 orb 为 ready（在途投递使线程不静止，此后 orb 不会被冻结）。
// agent 就绪 / 退出时经 readyWaiters 提前醒来；orb 状态变化（解冻）靠周期复查。
func (s *Service) waitAgentReady(ctx context.Context, t *thread, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	var ch chan apigen.AgentStatus
	for {
		t.mu.Lock()
		st := s.orbState(t.orbID)
		switch {
		case t.orbID == "" || st == orbStateDeleted || st == "deleting":
			t.mu.Unlock()
			return errors.New("thread has no orb")
		case st == orbStateFailed:
			t.mu.Unlock()
			return errors.New("orb failed")
		case t.agentReady() && st == orbStateReady:
			t.mu.Unlock()
			return nil
		}
		if ch == nil {
			ch = make(chan apigen.AgentStatus, 1)
			t.readyWaiters = append(t.readyWaiters, ch)
		}
		t.mu.Unlock()
		select {
		case <-ch:
			ch = nil
		case <-tick.C:
		case <-deadline.C:
			return fmt.Errorf("agent not ready within %s", timeout)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
