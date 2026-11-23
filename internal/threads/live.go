package threads

import (
	"encoding/json"
	"sort"
	"time"
)

// 线程状态（全端统一枚举，见计划 C4）。
const (
	StateProvisioning  = "provisioning"
	StateAwaitingInput = "awaiting_input"
	StateWorking       = "working"
	StateError         = "error"
	StateIdle          = "idle"
)

// pendingMethods 是需要用户作答、进入 pendingUi 槽的 extension_ui_request 方法
// （`askDialog` / `form` 是 set_ask_dialog{enabled, form} 开启后 ask 工具与表单的请求形态）。
var pendingMethods = map[string]bool{"select": true, "confirm": true, "input": true, "editor": true, "ask": true, "askDialog": true, "form": true}

// displayMethods 是只做展示、按 key 覆盖的 extension_ui_request 方法（扩展的页脚状态行与编辑器上下方的小部件）。
// 它们不待答，但重新订阅的客户端需要当前值：按 key 保留最新帧，空内容即删除。
var displayMethods = map[string]bool{"setStatus": true, "setWidget": true}

type msgKey struct {
	role       string
	ts         int64
	customType string // 注入消息（custom / hookMessage）落盘为 custom_message 条目，按它与时间戳认
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

// subSlot 是一个子 agent 的最新帧。Core 的子 agent 帧把字段放在 `payload` 里：lifecycle 的 id 在
// `payload.id`，progress 的在 `payload.progress.id`；`index` 只是该 agent 在自己那次 task 调用里的序号，
// 嵌套派发的子 agent 也从 0 编号，不能当键。
type subSlot struct {
	seq        int // 首次出现的顺序，快照按此排序
	frame      json.RawMessage
	parentTool string // 派发它的 task 工具调用 id（嵌套时属于上级子 agent 的会话）
	parentID   string // 嵌套派发时上级子 agent 的 id；根上为空
}

type subagentPayload struct {
	ID               string `json:"id"`
	ParentToolCallID string `json:"parentToolCallId"`
	ParentSubagentID string `json:"parentSubagentId"`
	Progress         struct {
		ID string `json:"id"`
	} `json:"progress"`
}

// Live 是单个线程的实时缓冲：只保存尚未被已提交条目覆盖的在途帧，以及推导线程状态所需的标志。
// 非并发安全，由调用方加锁。
type Live struct {
	seq int

	openMessage []json.RawMessage
	openKey     *msgKey

	tools     map[string]*toolSlot
	subagents map[string]*subSlot // 子 agent id → 最新帧
	pendingUI map[string]uiSlot
	displays  map[string]uiSlot // 方法 + key → 最新的 setStatus / setWidget 帧

	PlanProposal json.RawMessage
	Queue        json.RawMessage
	// ComposerPrediction 是最近一次 oryn_composer_prediction 帧的 choices（空表示没有预测），进快照供迟到的客户端显示。
	ComposerPrediction []string
	State              json.RawMessage

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
		subagents: map[string]*subSlot{},
		pendingUI: map[string]uiSlot{},
		displays:  map[string]uiSlot{},
	}
}

// frameHead 是帧里实时缓冲关心的字段。
type frameHead struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	TargetID    string            `json:"targetId"`
	ToolCallID  string            `json:"toolCallId"`
	Status      string            `json:"status"`
	IsTerminal  *bool             `json:"isTerminal"`
	Payload     json.RawMessage   `json:"payload"`
	Message     json.RawMessage   `json:"message"`
	StatusKey   string            `json:"statusKey"`
	StatusText  string            `json:"statusText"`
	WidgetKey   string            `json:"widgetKey"`
	WidgetLines []json.RawMessage `json:"widgetLines"`
}

type messageHead struct {
	Role       string `json:"role"`
	Timestamp  int64  `json:"timestamp"`
	ToolCallID string `json:"toolCallId"`
	CustomType string `json:"customType"`
}

// Effect 描述一帧对外可见的副作用，供线程服务决定是否刷新状态 / 广播。
type Effect struct {
	// UIResolved 为被 cancel 撤销的 pendingUi id。
	UIResolved string
	// UIRequested 为本帧新加入 pendingUi 的请求 id（已存在的 id 重复到达不算）。
	UIRequested string
	// PlanProposed 为真表示本帧是 plan_proposal 且此前没有未决提案。
	PlanProposed bool
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
			l.openKey = &msgKey{m.Role, m.Timestamp, m.CustomType}
		}
	case "message_update", "message_end":
		if l.openMessage != nil {
			l.openMessage = append(l.openMessage, frame)
		}
		if h.Type == "message_end" && l.openKey != nil {
			// message_end 携带完整消息，时间戳以它为准
			var m messageHead
			if json.Unmarshal(h.Message, &m) == nil && m.Role != "" {
				l.openKey = &msgKey{m.Role, m.Timestamp, m.CustomType}
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
		var p subagentPayload
		if json.Unmarshal(h.Payload, &p) != nil {
			break
		}
		id := p.ID
		if id == "" {
			id = p.Progress.ID
		}
		if id == "" {
			break
		}
		s := l.subagents[id]
		if s == nil {
			s = &subSlot{seq: l.seq}
			l.subagents[id] = s
		}
		s.frame = frame
		if p.ParentToolCallID != "" {
			s.parentTool = p.ParentToolCallID
		}
		if p.ParentSubagentID != "" {
			s.parentID = p.ParentSubagentID
		}
	case "extension_ui_request":
		switch {
		case pendingMethods[h.Method] && h.ID != "":
			if _, ok := l.pendingUI[h.ID]; !ok {
				eff.UIRequested = h.ID
			}
			l.pendingUI[h.ID] = uiSlot{seq: l.seq, frame: frame}
		case h.Method == "cancel" && h.TargetID != "":
			if _, ok := l.pendingUI[h.TargetID]; ok {
				delete(l.pendingUI, h.TargetID)
				eff.UIResolved = h.TargetID
			}
		case displayMethods[h.Method]:
			// 与客户端一致：空的 statusText / widgetLines 表示移除该 key
			key, empty := h.Method+"\x00"+h.StatusKey, h.StatusText == ""
			if h.Method == "setWidget" {
				key, empty = h.Method+"\x00"+h.WidgetKey, len(h.WidgetLines) == 0
			}
			if empty {
				delete(l.displays, key)
			} else {
				l.displays[key] = uiSlot{seq: l.seq, frame: frame}
			}
		}
	case "plan_proposal":
		eff.PlanProposed = l.PlanProposal == nil
		l.PlanProposal = frame
	case "queue_update":
		l.Queue = frame
	case "oryn_composer_prediction":
		var p struct {
			Choices []string `json:"choices"`
		}
		if json.Unmarshal(frame, &p) == nil {
			l.ComposerPrediction = p.Choices
		}
	case "agent_start":
		l.working = true
		l.promptError = false
		// 新回合让旧预测失效；Core 也会推撤回帧，这里不依赖它的先后。
		l.ComposerPrediction = nil
	case "agent_end":
		if h.IsTerminal == nil || *h.IsTerminal {
			l.working = false
		}
		eff.RefreshState = true
	case "prompt_result":
		l.promptError = h.Status == "error"
	case "session_settled":
		eff.Settled = true
		// session_settled 与 get_state.isSettled 同一判定：此刻已没有能再唤醒会话的后台任务。后台任务在回合之间
		// 结束（完成后结果被 await 消费、被 oryn_cancel_job 取消等）不会再有 state 帧，pendingAsync 在这里复位；
		// 再刷新一次 state，让快照里的 state 也跟上（只在此前判为有后台任务时，不给每个回合多加一次查询）。
		if l.pendingAsync {
			l.pendingAsync = false
			eff.RefreshState = true
		}
	case "model_changed", "thinking_level_changed", "goal_updated", "config_update":
		eff.RefreshState = true
	default:
		eff.AgentActivity = h.Type != "notice"
	}
	return eff
}

// ApplyEntry 处理一条已提交的会话条目，清除被它覆盖的在途帧。
func (l *Live) ApplyEntry(entryType string, payload json.RawMessage) {
	if entryType == "custom_message" {
		l.applyCustomEntry(payload)
		return
	}
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

// applyCustomEntry：注入消息（role custom / hookMessage）落盘为 custom_message 条目，没有 message 包裹；
// Core 用消息自己的时间戳写条目的 ISO `timestamp`，与 customType 一起认出它覆盖的在途消息。
func (l *Live) applyCustomEntry(payload json.RawMessage) {
	k := l.openKey
	if k == nil || (k.role != "custom" && k.role != "hookMessage") {
		return
	}
	var e struct {
		CustomType string    `json:"customType"`
		Timestamp  time.Time `json:"timestamp"`
	}
	if json.Unmarshal(payload, &e) != nil || e.Timestamp.IsZero() {
		return
	}
	if k.customType == e.CustomType && k.ts == e.Timestamp.UnixMilli() {
		l.openMessage, l.openKey = nil, nil
	}
}

func (l *Live) clearTool(id string) {
	delete(l.tools, id)
	// 父工具结束时清掉它派发的子 agent，以及这些子 agent 再派发的各级下级（下级的 task 调用在子会话里，
	// 主会话不会有它们的 toolResult）
	gone := map[string]bool{}
	for sid, s := range l.subagents {
		if s.parentTool == id && s.parentID == "" {
			gone[sid] = true
		}
	}
	for changed := len(gone) > 0; changed; {
		changed = false
		for sid, s := range l.subagents {
			if !gone[sid] && gone[s.parentID] {
				gone[sid] = true
				changed = true
			}
		}
	}
	for sid := range gone {
		delete(l.subagents, sid)
	}
}

// HasUI 报告 UI 请求是否仍待答。
func (l *Live) HasUI(id string) bool {
	_, ok := l.pendingUI[id]
	return ok
}

// Seq 返回已处理的帧序号：单调递增，Reset 不归零。
func (l *Live) Seq() int { return l.seq }

// UIRequest 返回仍待答的 UI 请求帧及其到达时的帧序号。
func (l *Live) UIRequest(id string) (json.RawMessage, int, bool) {
	s, ok := l.pendingUI[id]
	return s.frame, s.seq, ok
}

// WidgetLines 返回 key 当前 setWidget 帧的 widgetLines；没有该 key 或行不全是字符串时 ok=false。
func (l *Live) WidgetLines(key string) ([]string, bool) {
	s, ok := l.displays["setWidget\x00"+key]
	if !ok {
		return nil, false
	}
	var w struct {
		WidgetLines []string `json:"widgetLines"`
	}
	if json.Unmarshal(s.frame, &w) != nil {
		return nil, false
	}
	return w.WidgetLines, true
}

// ResolveUI 移除一个待答 UI 请求；返回是否存在（用于「先到先得」）。
func (l *Live) ResolveUI(id string) bool {
	if _, ok := l.pendingUI[id]; !ok {
		return false
	}
	delete(l.pendingUI, id)
	return true
}

// PendingUIIDs 返回全部待答请求 id（无序）。
func (l *Live) PendingUIIDs() []string {
	out := make([]string, 0, len(l.pendingUI))
	for id := range l.pendingUI {
		out = append(out, id)
	}
	return out
}

// RunState 是 get_state 应答里与运行 / 静止判定相关的字段（缺失为 nil）。
type RunState struct {
	IsStreaming *bool `json:"isStreaming"`
	IsSettled   *bool `json:"isSettled"`
}

// SetState 记录最新 get_state 应答的 data，返回其中的运行 / 静止字段。
// 「回合进行中」只由 agent_start / agent_end 驱动，不看这里的 isStreaming：agent_end 之后的刷新可能在 omp 清掉
// isStreaming 之前作答，据此置 working 会让线程永远停在 working（之后没有帧再纠正）。漏掉 agent_start 只会发生在
// gap / 重启 / agent 重启，那时由线程服务按 thread.unsynced 以这份快照双向恢复。
func (l *Live) SetState(state json.RawMessage) RunState {
	l.State = state
	var s struct {
		HasPendingAsyncWork bool `json:"hasPendingAsyncWork"`
		RunState
	}
	_ = json.Unmarshal(state, &s)
	l.pendingAsync = s.HasPendingAsyncWork
	return s.RunState
}

// SetWorking 以权威快照（重新同步后的 get_state）覆盖「回合进行中」标志。
func (l *Live) SetWorking(w bool) { l.working = w }

// AgentReady 标记 agent 至少就绪过一次。
func (l *Live) AgentReady() { l.agentEverReady = true; l.restartExhausted = false }

// RestartExhausted 标记 agent 重启耗尽。
func (l *Live) RestartExhausted() { l.restartExhausted = true; l.working = false }

// Reset 丢弃全部在途帧（live.reset / agent.gap / agent 重启）；保留就绪与错误标志。展示类 UI 一并丢弃：agent 重启后
// 新进程从零开始；gap 后同一进程由 orbd 在新流上重放当前值（见 orbdrt pendingUI）。
func (l *Live) Reset() {
	l.openMessage, l.openKey = nil, nil
	l.tools = map[string]*toolSlot{}
	l.subagents = map[string]*subSlot{}
	l.pendingUI = map[string]uiSlot{}
	l.displays = map[string]uiSlot{}
	l.PlanProposal = nil
	l.ComposerPrediction = nil
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
func (l *Live) PendingUI() []json.RawMessage { return slotFrames(l.pendingUI) }

// DisplayUI 按到达顺序返回各 key 当前的 setStatus / setWidget 帧。
func (l *Live) DisplayUI() []json.RawMessage { return slotFrames(l.displays) }

func slotFrames(m map[string]uiSlot) []json.RawMessage {
	s := make([]uiSlot, 0, len(m))
	for _, v := range m {
		s = append(s, v)
	}
	sort.Slice(s, func(i, j int) bool { return s[i].seq < s[j].seq })
	out := make([]json.RawMessage, len(s))
	for i, v := range s {
		out[i] = v.frame
	}
	return out
}

// Subagents 按首次出现顺序返回各子 agent 的最新帧。
func (l *Live) Subagents() []json.RawMessage {
	slots := make([]*subSlot, 0, len(l.subagents))
	for _, s := range l.subagents {
		slots = append(slots, s)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].seq < slots[j].seq })
	out := make([]json.RawMessage, len(slots))
	for i, s := range slots {
		out[i] = s.frame
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
