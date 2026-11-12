package threads

import (
	"encoding/json"
	"sort"
)

// 线程状态（全端统一枚举，见计划 C4）。
const (
	StateProvisioning  = "provisioning"
	StateAwaitingInput = "awaiting_input"
	StateWorking       = "working"
	StateError         = "error"
	StateIdle          = "idle"
)

// pendingMethods 是需要用户作答、进入 pendingUi 槽的 extension_ui_request 方法。
var pendingMethods = map[string]bool{"select": true, "confirm": true, "input": true, "editor": true, "ask": true}

type msgKey struct {
	role string
	ts   int64
}

type toolSlot struct {
	seq    int // 首次出现的顺序，快照按此排序
	start  json.RawMessage
	update json.RawMessage
	stream json.RawMessage
	end    json.RawMessage
}

type uiSlot struct {
	seq   int
	frame json.RawMessage
}

// Live 是单个线程的实时缓冲：只保存尚未被已提交条目覆盖的在途帧，以及推导线程状态所需的标志。
// 非并发安全，由调用方加锁。
type Live struct {
	seq int

	openMessage []json.RawMessage
	openKey     *msgKey

	tools     map[string]*toolSlot
	subagents map[int]json.RawMessage
	subParent map[int]string // 子 agent index → 父 toolCallId
	pendingUI map[string]uiSlot

	PlanProposal json.RawMessage
	Queue        json.RawMessage
	State        json.RawMessage

	agentEverReady   bool
	working          bool
	promptError      bool
	restartExhausted bool
	pendingAsync     bool
}

// NewLive 返回空缓冲。
func NewLive() *Live {
	return &Live{
		tools:     map[string]*toolSlot{},
		subagents: map[int]json.RawMessage{},
		subParent: map[int]string{},
		pendingUI: map[string]uiSlot{},
	}
}

// frameHead 是帧里实时缓冲关心的字段。
type frameHead struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Method     string          `json:"method"`
	TargetID   string          `json:"targetId"`
	ToolCallID string          `json:"toolCallId"`
	Status     string          `json:"status"`
	IsTerminal *bool           `json:"isTerminal"`
	Index      *int            `json:"index"`
	ParentTool string          `json:"parentToolCallId"`
	Message    json.RawMessage `json:"message"`
}

type messageHead struct {
	Role       string `json:"role"`
	Timestamp  int64  `json:"timestamp"`
	ToolCallID string `json:"toolCallId"`
}

// Effect 描述一帧对外可见的副作用，供线程服务决定是否刷新状态 / 广播。
type Effect struct {
	// UIResolved 为被 cancel 撤销的 pendingUi id。
	UIResolved string
	// RefreshState 为真时应向 agent 发 get_state 刷新 State。
	RefreshState bool
	// AgentActivity 为真表示 agent 有活动（用于 orb 空闲计时）。
	AgentActivity bool
	// Settled 为真表示收到 session_settled。
	Settled bool
}

// ApplyFrame 处理一条 omp 逻辑帧（response 帧不应传入）。
func (l *Live) ApplyFrame(frame json.RawMessage) Effect {
	var h frameHead
	if json.Unmarshal(frame, &h) != nil {
		return Effect{}
	}
	l.seq++
	eff := Effect{AgentActivity: true}
	switch h.Type {
	case "message_start":
		l.openMessage = []json.RawMessage{frame}
		l.openKey = nil
		var m messageHead
		if json.Unmarshal(h.Message, &m) == nil && m.Role != "" {
			l.openKey = &msgKey{m.Role, m.Timestamp}
		}
	case "message_update", "message_end":
		if l.openMessage != nil {
			l.openMessage = append(l.openMessage, frame)
		}
		if h.Type == "message_end" && l.openKey != nil {
			// message_end 携带完整消息，时间戳以它为准
			var m messageHead
			if json.Unmarshal(h.Message, &m) == nil && m.Role != "" {
				l.openKey = &msgKey{m.Role, m.Timestamp}
			}
		}
	case "tool_execution_start", "tool_execution_update", "tool_stream_update", "tool_execution_end":
		if h.ToolCallID == "" {
			break
		}
		t := l.tools[h.ToolCallID]
		if t == nil {
			t = &toolSlot{seq: l.seq}
			l.tools[h.ToolCallID] = t
		}
		switch h.Type {
		case "tool_execution_start":
			t.start = frame
		case "tool_execution_update":
			t.update = frame
		case "tool_stream_update":
			t.stream = frame
		case "tool_execution_end":
			t.end = frame
		}
	case "subagent_lifecycle", "subagent_progress":
		if h.Index != nil {
			l.subagents[*h.Index] = frame
			if h.ParentTool != "" {
				l.subParent[*h.Index] = h.ParentTool
			}
		}
	case "extension_ui_request":
		switch {
		case pendingMethods[h.Method] && h.ID != "":
			l.pendingUI[h.ID] = uiSlot{seq: l.seq, frame: frame}
		case h.Method == "cancel" && h.TargetID != "":
			if _, ok := l.pendingUI[h.TargetID]; ok {
				delete(l.pendingUI, h.TargetID)
				eff.UIResolved = h.TargetID
			}
		}
	case "plan_proposal":
		l.PlanProposal = frame
	case "queue_update":
		l.Queue = frame
	case "agent_start":
		l.working = true
		l.promptError = false
	case "agent_end":
		if h.IsTerminal == nil || *h.IsTerminal {
			l.working = false
		}
		eff.RefreshState = true
	case "prompt_result":
		l.promptError = h.Status == "error"
	case "session_settled":
		eff.Settled = true
	case "model_changed", "thinking_level_changed", "goal_updated", "config_update":
		eff.RefreshState = true
	default:
		eff.AgentActivity = h.Type != "notice"
	}
	return eff
}

// ApplyEntry 处理一条已提交的会话条目，清除被它覆盖的在途帧。
func (l *Live) ApplyEntry(entryType string, payload json.RawMessage) {
	if entryType != "message" {
		return
	}
	var e struct {
		Message messageHead `json:"message"`
	}
	if json.Unmarshal(payload, &e) != nil {
		return
	}
	m := e.Message
	if l.openKey != nil && l.openKey.role == m.Role && l.openKey.ts == m.Timestamp {
		l.openMessage, l.openKey = nil, nil
	}
	if m.Role == "toolResult" && m.ToolCallID != "" {
		l.clearTool(m.ToolCallID)
	}
}

func (l *Live) clearTool(id string) {
	delete(l.tools, id)
	for idx, parent := range l.subParent {
		if parent == id {
			delete(l.subagents, idx)
			delete(l.subParent, idx)
		}
	}
}

// ResolveUI 移除一个待答 UI 请求；返回是否存在（用于「先到先得」）。
func (l *Live) ResolveUI(id string) bool {
	if _, ok := l.pendingUI[id]; !ok {
		return false
	}
	delete(l.pendingUI, id)
	return true
}

// SetState 记录最新 get_state 应答的 data。
func (l *Live) SetState(state json.RawMessage) {
	l.State = state
	var s struct {
		HasPendingAsyncWork bool `json:"hasPendingAsyncWork"`
	}
	_ = json.Unmarshal(state, &s)
	l.pendingAsync = s.HasPendingAsyncWork
}

// AgentReady 标记 agent 至少就绪过一次。
func (l *Live) AgentReady() { l.agentEverReady = true; l.restartExhausted = false }

// RestartExhausted 标记 agent 重启耗尽。
func (l *Live) RestartExhausted() { l.restartExhausted = true; l.working = false }

// Reset 丢弃全部在途帧（live.reset / agent.gap / agent 重启）；保留就绪与错误标志。
func (l *Live) Reset() {
	l.openMessage, l.openKey = nil, nil
	l.tools = map[string]*toolSlot{}
	l.subagents = map[int]json.RawMessage{}
	l.subParent = map[int]string{}
	l.pendingUI = map[string]uiSlot{}
	l.PlanProposal = nil
	l.working = false
}

// Status 按 C4 规则推导线程状态。orbReady = orb 当前处于 ready。
func (l *Live) Status(orbReady bool) string {
	switch {
	case !orbReady && !l.agentEverReady:
		return StateProvisioning
	case len(l.pendingUI) > 0 || l.PlanProposal != nil:
		return StateAwaitingInput
	case l.working || l.pendingAsync:
		return StateWorking
	case l.promptError || l.restartExhausted:
		return StateError
	}
	return StateIdle
}

// Quiescent 表示没有待答 UI 与计划提案（停机前提之一）。
func (l *Live) Quiescent() bool { return len(l.pendingUI) == 0 && l.PlanProposal == nil && !l.working }

// PendingUI 按到达顺序返回待答请求帧。
func (l *Live) PendingUI() []json.RawMessage {
	s := make([]uiSlot, 0, len(l.pendingUI))
	for _, v := range l.pendingUI {
		s = append(s, v)
	}
	sort.Slice(s, func(i, j int) bool { return s[i].seq < s[j].seq })
	out := make([]json.RawMessage, len(s))
	for i, v := range s {
		out[i] = v.frame
	}
	return out
}

// Subagents 按 index 返回子 agent 最新帧。
func (l *Live) Subagents() []json.RawMessage {
	idx := make([]int, 0, len(l.subagents))
	for i := range l.subagents {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]json.RawMessage, len(idx))
	for i, k := range idx {
		out[i] = l.subagents[k]
	}
	return out
}

// LiveFrames 返回叠加层帧（在途消息全部帧 + 运行中工具的各槽），供迟到订阅者重放。
func (l *Live) LiveFrames() []json.RawMessage {
	out := append([]json.RawMessage(nil), l.openMessage...)
	ts := make([]*toolSlot, 0, len(l.tools))
	for _, t := range l.tools {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].seq < ts[j].seq })
	for _, t := range ts {
		for _, f := range []json.RawMessage{t.start, t.update, t.stream, t.end} {
			if f != nil {
				out = append(out, f)
			}
		}
	}
	return out
}
