package threads

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 线程种类（M9）。协调对话（Space 的聊天与全局助手）也是 threads 表里的线程。
const (
	KindWork      = "work"
	KindSpace     = "space"
	KindAssistant = "assistant"
)

// CodeCoordinatorRestricted 是协调线程不允许的操作（命令、附件、fork、分享、挂到协调 orb）的错误码。
const CodeCoordinatorRestricted = "coordinator_restricted"

// hostToolTimeout 是单次宿主工具调用的上限。
const hostToolTimeout = 60 * time.Second

// cmdOrigin 区分命令来源：客户端（主人）或自动化投递（定时、线程回报、agent 间消息）。
type cmdOrigin int

const (
	originUser cmdOrigin = iota
	originAuto
)

// AutoMessage 是自动化（定时、线程回报、agent 间消息）投递的一条消息。
type AutoMessage struct {
	Text string
	// Steer 为真且线程正在工作时作为 steer 送达；否则排在当前回合之后。
	Steer bool
}

// HostToolCall 是协调线程里 omp 发来的一次宿主工具调用。
type HostToolCall struct {
	// ThreadID 是协调对话；Kind 为其种类（space / assistant）；SpaceID 为 Space 聊天所属的 Space。
	ThreadID, Kind, SpaceID string
	// CallID 是 host_tool_call 帧的 id；ToolCallID 是模型侧的工具调用 id。
	CallID, ToolCallID, Name string
	Args                     json.RawMessage
	// OwnerTurn 为真表示这段活动里没有下发过自动化消息（只用于审计的 origin，不参与放行判断）。
	OwnerTurn bool
}

// HostToolResult 是宿主工具的结果：Text 写进 content，Details 原样作为 details。
type HostToolResult struct {
	Text    string
	Details any
	IsError bool
}

// AgentProfile 是协调线程启动 agent 时的系统提示词与宿主工具表（set_host_tools 的 tools）。
type AgentProfile struct {
	SystemPrompt string
	Tools        []apigen.OmpFrame
}

// Coordinator 由 internal/coord 实现。
type Coordinator interface {
	// EnsureOrb 返回（必要时创建）协调 orb。
	EnsureOrb(ctx context.Context) (orbID string, err error)
	AgentProfile(ctx context.Context, threadID string) (AgentProfile, error)
	// HostToolCall 执行一次宿主工具调用；ctx 在 omp 取消调用或超时时结束。
	HostToolCall(ctx context.Context, c HostToolCall) HostToolResult
}

// SetCoordinator 设置协调服务；须在服务对外可用之前调用。
func (s *Service) SetCoordinator(c Coordinator) { s.coord = c }

// CoordinatorParams 是新建协调对话的参数。Model 为 "provider/id"（空 = omp 配置的默认模型）。
// Extra 非空时在插入线程的同一事务里执行（Space 用它写 spaces 行：agent 启动时读系统提示词必须已能读到 Space）。
type CoordinatorParams struct {
	Kind, OrbID, Title, SpaceID, Model string
	Extra                              func(q *db.Queries, threadID string) error
}

// CreateCoordinator 新建一个协调对话（kind = space | assistant），挂到协调 orb 上并唤醒它。
func (s *Service) CreateCoordinator(ctx context.Context, p CoordinatorParams) (apigen.Thread, error) {
	if p.Kind != KindSpace && p.Kind != KindAssistant {
		return apigen.Thread{}, errf("invalid", "coordinator kind must be space or assistant")
	}
	if p.OrbID == "" {
		return apigen.Thread{}, errf("invalid", "coordinator orb required")
	}
	id := ids.Thread()
	row := db.CreateThreadParams{
		Kind: p.Kind, ID: id, OrbID: &p.OrbID, TitleSource: "auto", State: StateProvisioning, SessionDir: sessionDirOf(id),
		ShortCode: shortCode(id),
	}
	if p.Title != "" {
		row.Title, row.TitleSource = p.Title, "user"
	}
	if p.SpaceID != "" {
		row.SpaceID = &p.SpaceID
	}
	if p.Model != "" {
		row.Model = &p.Model
	}
	var extra func(q *db.Queries) error
	if p.Extra != nil {
		extra = func(q *db.Queries) error { return p.Extra(q, id) }
	}
	if err := s.insertThread(ctx, row, nil, extra); err != nil {
		return apigen.Thread{}, err
	}
	t, err := s.get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	t.mu.Lock()
	s.recompute(ctx, t)
	t.mu.Unlock()
	out, err := s.Get(ctx, id)
	if err != nil {
		return apigen.Thread{}, err
	}
	s.out().IndexUpsert(out)
	s.wake(id, p.OrbID)
	return out, nil
}

// SetCoordinatorTitle 改协调对话的标题（Space 改名时同步聊天线程），并通知 agent。
func (s *Service) SetCoordinatorTitle(ctx context.Context, id, title string) error {
	_, err := s.Patch(ctx, id, apigen.ThreadPatch{Title: &title})
	return err
}

// RefreshAgent 让协调线程在静止后停掉 agent；下一条命令用新的 AgentProfile 启动。
func (s *Service) RefreshAgent(ctx context.Context, threadID string) {
	t, err := s.get(ctx, threadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.kind == KindWork {
		return
	}
	if t.agent == nil && t.agentStatus != apigen.AgentStatusReady && !t.starting {
		// agent 不在运行：下次启动自然取新的 AgentProfile
		return
	}
	t.refresh = true
	if t.settled && !t.unsynced && len(t.queue) == 0 && len(t.pending) == 0 {
		s.stopForRefreshLocked(t)
	}
}

// stopForRefreshLocked 在后台停掉协调线程的 agent（RefreshAgent）。调用方持有 t.mu。
func (s *Service) stopForRefreshLocked(t *thread) {
	t.refresh = false
	t.stopping = true
	orbID := t.orbID
	s.go_(func(ctx context.Context) {
		ctl, ok := s.orbs.Ctl(orbID)
		if !ok {
			t.mu.Lock()
			t.stopping = false
			t.mu.Unlock()
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := ctl.Call(cctx, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: t.id, GraceSeconds: 5}, nil); err != nil {
			slog.Debug("threads: 刷新协调 agent 时 agent.stop 失败", "thread", t.id, "err", err)
			t.mu.Lock()
			t.stopping = false
			t.mu.Unlock()
		}
	})
}

// Archive 归档线程（等价于 PATCH {archived: true}；不算用户交互）。供定时的保留策略调用。
func (s *Service) Archive(ctx context.Context, id string) error {
	yes := true
	_, err := s.Patch(ctx, id, apigen.ThreadPatch{Archived: &yes})
	return err
}

// Deliver 以自动化身份向线程追加一条消息：不清未读、不通知 Seen、不记用户交互（记 agent 活动），需要时唤醒 orb。
// ack 在 omp 应答该命令时调用一次（nil = 已被 agent 接受）。cancel 在消息仍在控制面排队时撤回它并返回 true
// （撤回后 ack 不会被调用）。同步错误码：not_found、archived、no_orb、queue_full。
func (s *Service) Deliver(ctx context.Context, threadID string, m AutoMessage, ack func(err error)) (cancel func() bool, err error) {
	t, err := s.get(ctx, threadID)
	if err != nil {
		return nil, err
	}
	row, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil {
		return nil, ErrNotFound
	}
	if row.ArchivedAt != nil {
		return nil, errf("archived", "thread is archived")
	}
	if t.kind != KindWork {
		if err := s.ensureCoordOrb(ctx, t); err != nil {
			return nil, err
		}
	}
	t.mu.Lock()
	working := t.state == StateWorking
	t.mu.Unlock()
	msg := map[string]any{"type": "prompt", "message": m.Text, "streamingBehavior": "followUp"}
	if m.Steer && working {
		msg = map[string]any{"type": "steer", "message": m.Text}
	}
	frame, _ := json.Marshal(msg)
	var once sync.Once
	reply := func(resp json.RawMessage) {
		var e error
		env, err := omprpc.ParseEnvelope(resp)
		switch {
		case err != nil:
			e = err
		case !env.OK():
			e = errf("agent_error", env.ErrorText())
		}
		once.Do(func() {
			if ack != nil {
				ack(e)
			}
		})
	}
	p, err := s.command(ctx, threadID, frame, reply, originAuto)
	if err != nil {
		return nil, err
	}
	return func() bool { return s.cancelQueued(t, p) }, nil
}

// cancelQueued 撤回仍在控制面排队（尚未写到 agent 流）的命令。
func (s *Service) cancelQueued(t *thread, p *pendingCmd) bool {
	if p == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p.sent {
		return false
	}
	for _, sl := range t.msgOrder {
		if sl.p == p && sl.frame != nil {
			sl.frame = nil
			return true
		}
	}
	for i, f := range t.queue {
		if _, id, ok := frameHeader(f); ok && id == p.id {
			t.queue = append(t.queue[:i], t.queue[i+1:]...)
			delete(t.pending, p.id)
			s.recompute(context.Background(), t)
			return true
		}
	}
	return false
}

// coordinatorCommands 是协调线程允许客户端下发的命令（`get_*` 另行判定）。
var coordinatorCommands = map[string]bool{
	"prompt": true, "steer": true, "follow_up": true, "abort": true, "abort_and_prompt": true,
	"queue_edit": true, "queue_remove": true, "queue_move": true, "queue_clear": true, "promote_queued_message": true,
	"set_model": true, "set_thinking_level": true, "set_fast_mode": true, "set_slow_mode": true,
	"switch_leaf": true, "navigate_tree": true, "abort_branch_summary": true, "set_label": true, "set_entry_label": true,
	"compact": true, "clear_context": true, "retry": true, "set_session_name": true,
}

// coordinatorDeniedQueries 是协调线程不放行的只读查询（会读宿主上的 git / ssh 状态）。
var coordinatorDeniedQueries = map[string]bool{
	"get_git_changes": true, "get_git_diff": true, "get_git_status": true, "get_ssh_hosts": true,
}

// coordinatorAllowed 报告协调线程是否接受客户端的 cmd。
func coordinatorAllowed(cmdType string) bool {
	if strings.HasPrefix(cmdType, "get_") {
		return !coordinatorDeniedQueries[cmdType]
	}
	return coordinatorCommands[cmdType]
}

// ensureCoordOrb 让没有 orb 的协调线程（协调 orb 被删除后）挂到协调 orb 上：转录保留，模型上下文从新会话开始。
func (s *Service) ensureCoordOrb(ctx context.Context, t *thread) error {
	t.mu.Lock()
	has := t.orbID != ""
	t.mu.Unlock()
	if has {
		return nil
	}
	if s.coord == nil {
		return errf("orb_unavailable", "coordinator is not configured")
	}
	orbID, err := s.coord.EnsureOrb(ctx)
	if err != nil {
		var ce codedError
		if errors.As(err, &ce) {
			code, msg := ce.Coded()
			return errf(code, msg)
		}
		return errf("orb_unavailable", err.Error())
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.orbID != "" {
		return nil
	}
	if err := s.st.Q.SetThreadOrb(ctx, db.SetThreadOrbParams{OrbID: &orbID, UpdatedAt: s.now(), ID: t.id}); err != nil {
		return err
	}
	t.orbID = orbID
	s.recompute(ctx, t)
	s.publish(ctx, t.id)
	return nil
}

// hasQueued 报告线程是否有排队（尚未下发）的命令或消息。
func (s *Service) hasQueued(ctx context.Context, threadID string) bool {
	t, err := s.get(ctx, threadID)
	if err != nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.queue) > 0 || len(t.msgOrder) > 0
}

// hostToolCallFrame 是 omp 的 host_tool_call 帧。
type hostToolCallFrame struct {
	ID         string          `json:"id"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
}

// hostToolCallLocked 在后台执行一次宿主工具调用并把结果写回 agent 流。调用方持有 t.mu。
func (s *Service) hostToolCallLocked(t *thread, line []byte) {
	var f hostToolCallFrame
	if json.Unmarshal(line, &f) != nil || f.ID == "" {
		return
	}
	if _, dup := t.hostCalls[f.ID]; dup {
		// orbd 在新流上重放的同一调用：已在执行
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, hostToolTimeout)
	t.hostCalls[f.ID] = cancel
	call := HostToolCall{
		ThreadID: t.id, Kind: t.kind, SpaceID: t.spaceID,
		CallID: f.ID, ToolCallID: f.ToolCallID, Name: f.ToolName, Args: f.Arguments,
		OwnerTurn: !t.autoActive,
	}
	coord := s.coord
	s.go_(func(context.Context) {
		defer cancel()
		res := HostToolResult{Text: "coordinator_unavailable: coordinator is not configured", IsError: true}
		if coord != nil {
			res = coord.HostToolCall(ctx, call)
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if _, live := t.hostCalls[f.ID]; !live {
			// omp 已取消该调用（host_tool_cancel）或 agent 已退出：结果作废
			return
		}
		delete(t.hostCalls, f.ID)
		if t.agent == nil {
			return
		}
		frame, err := hostToolResultFrame(f.ID, res)
		if err != nil {
			slog.Warn("threads: 编码宿主工具结果失败", "thread", t.id, "tool", f.ToolName, "err", err)
			frame, _ = hostToolResultFrame(f.ID, HostToolResult{Text: "internal_error: " + err.Error(), IsError: true})
		}
		if !t.agent.send(frame) {
			slog.Warn("threads: 宿主工具结果未能写入 agent 流", "thread", t.id, "tool", f.ToolName)
		}
	})
}

// hostToolResultFrame 构造 host_tool_result 帧。
func hostToolResultFrame(id string, r HostToolResult) ([]byte, error) {
	result := map[string]any{"content": []map[string]string{{"type": "text", "text": r.Text}}}
	if r.Details != nil {
		result["details"] = r.Details
	}
	return json.Marshal(map[string]any{"type": "host_tool_result", "id": id, "result": result, "isError": r.IsError})
}

// cancelHostCallLocked 处理 host_tool_cancel{targetId}。调用方持有 t.mu。
func cancelHostCallLocked(t *thread, line []byte) {
	var f struct {
		TargetID string `json:"targetId"`
	}
	if json.Unmarshal(line, &f) != nil {
		return
	}
	if cancel, ok := t.hostCalls[f.TargetID]; ok {
		delete(t.hostCalls, f.TargetID)
		cancel()
	}
}

// cancelHostCallsLocked 取消全部在途的宿主工具调用（agent 退出、线程删除）。调用方持有 t.mu。
func cancelHostCallsLocked(t *thread) {
	for id, cancel := range t.hostCalls {
		delete(t.hostCalls, id)
		cancel()
	}
}
