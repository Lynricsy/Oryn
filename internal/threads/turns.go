package threads

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// 带标签投递的终态（契约 C1）。
const (
	TurnCompleted = "completed"
	TurnAborted   = "aborted"
	TurnError     = "error"
	TurnUnknown   = "unknown"
)

// TurnEvent 是一次带标签的自动化投递（AutoMessage.Tag / CreateParams.Tag）的终态。
// 标签约定：定时用运行 id（srn_…），协调回报用登记代号（rep_…）；标签为空 = 不跟踪。
type TurnEvent struct {
	ThreadID, Tag string
	Status        string // TurnCompleted / TurnAborted / TurnError / TurnUnknown
	Local         bool   // omp 本地处理，没有开启回合（Status = completed）
	Error         string // Status == error 时的原因
	// Text 是所属回合最后一条有文本的 assistant 消息（至多 8000 字符）；
	// HasText 为假表示控制面没有看到该回合的 agent_end（消费方退回读库）。
	Text    string
	HasText bool
}

// orynTagKey 是首条提示词在持久化帧上携带投递标签的 Oryn 扩展字段（与 orynAttachments 同样的做法）：
// 控制面在下发前删除它；客户端帧上的同名字段一律删除并丢弃（客户端不能伪造标签）。
const orynTagKey = "orynTag"

// maxTurnRefs 是等 prompt_result 的回合登记上限；溢出时丢最早一项（有标签则发 unknown）。
const maxTurnRefs = 64

// maxTurnTextRunes 是 TurnEvent.Text 的上限。
const maxTurnTextRunes = 8000

// defaultProviderError 是 assistant 消息 stopReason == error 却没有 errorMessage 时的原因。
const defaultProviderError = "provider request failed"

// turnRef 是一次已获成功应答、等 prompt_result 的 prompt / abort_and_prompt（含无标签的，按应答顺序）。
type turnRef struct {
	cmdID, tag string
}

// yieldInfo 是最近一次 yield 的 agent_end 推出的回合结局。
type yieldInfo struct {
	status, err, text string
}

// cmdIDPrefix 是本进程的命令 id 随机段（规则 9）：重启后 orbd 重放的旧应答 / 旧 prompt_result 不会撞上新命令的 id。
var cmdIDPrefix = func() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "oryn-" + hex.EncodeToString(b[:]) + "-"
}()

// takeTag 取出并删除帧上的 orynTag，返回标签与去掉该字段后的帧；没有该字段时原样返回。
func takeTag(frame json.RawMessage) (string, json.RawMessage) {
	var m map[string]json.RawMessage
	if json.Unmarshal(frame, &m) != nil {
		return "", frame
	}
	raw, has := m[orynTagKey]
	if !has {
		return "", frame
	}
	delete(m, orynTagKey)
	var tag string
	_ = json.Unmarshal(raw, &tag)
	out, err := json.Marshal(m)
	if err != nil {
		return "", frame
	}
	return tag, out
}

// endTurn 发一次 TurnEvent。调用方通常持有 t.mu（Observer 只入队）。
func (s *Service) endTurn(threadID string, ev TurnEvent) {
	ev.ThreadID = threadID
	s.observer().TurnEnded(ev)
}

// failTagged 以 error 结算一条带标签、尚未得到终态的命令（失败应答，含合成的），并清掉标签保证只结算一次。
func (s *Service) failTagged(threadID string, p *pendingCmd, msg string) {
	if p == nil || p.tag == "" {
		return
	}
	tag := p.tag
	p.tag = ""
	s.endTurn(threadID, TurnEvent{Tag: tag, Status: TurnError, Error: msg})
}

// settleResponseLocked 在应答交给发起方之后结算带标签的投递（规则 3）。调用方持有 t.mu。
func (s *Service) settleResponseLocked(t *thread, p *pendingCmd, env omprpc.Envelope, r responseHead) {
	switch p.cmdType {
	case "prompt", "abort_and_prompt":
		if !r.Success {
			s.failTagged(t.id, p, env.ErrorText())
			return
		}
		var d struct {
			AgentInvoked *bool `json:"agentInvoked"`
		}
		_ = json.Unmarshal(r.Data, &d)
		if d.AgentInvoked != nil && !*d.AgentInvoked {
			if p.tag != "" {
				tag := p.tag
				p.tag = ""
				s.endTurn(t.id, TurnEvent{Tag: tag, Status: TurnCompleted, Local: true})
			}
			return
		}
		s.addTurnLocked(t, turnRef{cmdID: env.ID, tag: p.tag})
		p.tag = ""
	case "steer":
		if p.tag == "" {
			return
		}
		if !r.Success {
			s.failTagged(t.id, p, env.ErrorText())
			return
		}
		t.steers = append(t.steers, p.tag)
		p.tag = ""
	}
}

// addTurnLocked 登记一个等 prompt_result 的回合；超过上限时丢最早一项。调用方持有 t.mu。
func (s *Service) addTurnLocked(t *thread, ref turnRef) {
	if len(t.turns) >= maxTurnRefs {
		old := t.turns[0]
		t.turns = t.turns[1:]
		if old.tag != "" {
			s.endTurn(t.id, TurnEvent{Tag: old.tag, Status: TurnUnknown})
		}
	}
	t.turns = append(t.turns, ref)
}

// trackLostLocked 在 agent.gap 时把已下发、应答可能丢失的带标签消息命令转为等 prompt_result / agent_end：
// gap 只是帧不连续，命令本身已到达 agent，之后的 prompt_result（或重新同步）给出终态，不按失败结算。
// 调用方持有 t.mu，且在 failPendingLocked 之前调用。
func (s *Service) trackLostLocked(t *thread) {
	for id, p := range t.pending {
		if !p.sent || p.tag == "" {
			continue
		}
		switch p.cmdType {
		case "prompt", "abort_and_prompt":
			s.addTurnLocked(t, turnRef{cmdID: id, tag: p.tag})
			p.tag = ""
		case "steer":
			t.steers = append(t.steers, p.tag)
			p.tag = ""
		}
	}
}

// endAllTurnsLocked 以同一终态结束全部仍在跟踪的带标签投递并清空登记（agent 退出、重新同步、线程删除）。
// 调用方持有 t.mu。
func (s *Service) endAllTurnsLocked(t *thread, status, errMsg string) {
	for _, ref := range t.turns {
		if ref.tag != "" {
			s.endTurn(t.id, TurnEvent{Tag: ref.tag, Status: status, Error: errMsg})
		}
	}
	for _, tag := range t.steers {
		s.endTurn(t.id, TurnEvent{Tag: tag, Status: status, Error: errMsg})
	}
	t.turns, t.steers = nil, nil
}

// agentEndFrame 是 agent_end 帧里结算所需的字段。
type agentEndFrame struct {
	IsTerminal *bool             `json:"isTerminal"`
	Yielded    *bool             `json:"yielded"`
	Messages   []json.RawMessage `json:"messages"`
}

// onAgentEndLocked 处理 agent_end（规则 4）：只在有跟踪中的投递时解析。调用方持有 t.mu。
func (s *Service) onAgentEndLocked(t *thread, line []byte) {
	if len(t.turns)+len(t.steers) == 0 {
		return
	}
	var f agentEndFrame
	if json.Unmarshal(line, &f) != nil {
		return
	}
	terminal := f.IsTerminal == nil || *f.IsTerminal
	yielded := terminal
	if f.Yielded != nil {
		yielded = *f.Yielded
	}
	y := runOutcome(f.Messages)
	if yielded {
		t.lastYield = &y
	}
	if terminal && len(t.steers) > 0 {
		for _, tag := range t.steers {
			s.endTurn(t.id, TurnEvent{Tag: tag, Status: y.status, Error: y.err, Text: y.text, HasText: true})
		}
		t.steers = nil
	}
}

// promptResultFrame 是 prompt_result 帧里结算所需的字段。
type promptResultFrame struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	AgentInvoked *bool  `json:"agentInvoked"`
	Error        *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// onPromptResultLocked 处理 prompt_result（规则 5）：按 id 找回合（没有 id 时取最早一项），有标签则发终态。
// 调用方持有 t.mu。
func (s *Service) onPromptResultLocked(t *thread, line []byte) {
	if len(t.turns) == 0 {
		return
	}
	var f promptResultFrame
	if json.Unmarshal(line, &f) != nil {
		return
	}
	idx := -1
	if f.ID == "" {
		idx = 0
	} else {
		for i, ref := range t.turns {
			if ref.cmdID == f.ID {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return
	}
	ref := t.turns[idx]
	t.turns = append(t.turns[:idx], t.turns[idx+1:]...)
	if ref.tag == "" {
		return
	}
	ev := TurnEvent{Tag: ref.tag, Status: promptStatus(f.Status), Local: f.AgentInvoked != nil && !*f.AgentInvoked}
	if f.Error != nil {
		ev.Error = f.Error.Message
	}
	if ev.Status == TurnError && ev.Error == "" {
		ev.Error = defaultProviderError
	}
	if y := t.lastYield; y != nil {
		ev.Text, ev.HasText = y.text, true
	}
	s.endTurn(t.id, ev)
}

// promptStatus 把 prompt_result.status 映射到 TurnEvent.Status（未知取值 → unknown）。
func promptStatus(st string) string {
	switch st {
	case TurnCompleted, TurnAborted, TurnError:
		return st
	}
	return TurnUnknown
}

// assistantHead 是 assistant 消息里判定结局的字段。
type assistantHead struct {
	Role         string `json:"role"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

// assistantOutcome 按 assistant 消息的 stopReason 给出结局：error → {error, errorMessage 或默认原因}，aborted → {aborted}，
// 其余 completed。
func assistantOutcome(h assistantHead) yieldInfo {
	switch h.StopReason {
	case "error":
		msg := h.ErrorMessage
		if msg == "" {
			msg = defaultProviderError
		}
		return yieldInfo{status: TurnError, err: msg}
	case "aborted":
		return yieldInfo{status: TurnAborted}
	}
	return yieldInfo{status: TurnCompleted}
}

// messageText 取一条 omp 消息（agent_end.messages 的元素）的文本，复用 transcript.ParseMessage。
func messageText(msg json.RawMessage) (role, text string) {
	payload, _ := json.Marshal(map[string]json.RawMessage{"message": msg})
	m, ok := transcript.ParseMessage(transcript.Entry{Type: "message", Payload: payload})
	if !ok {
		return "", ""
	}
	return m.Role, m.Text
}

// runOutcome 从回合的消息里推出结局：末尾向前第一条 assistant 决定状态，文本取最后一条有文本的 assistant 消息。
func runOutcome(messages []json.RawMessage) yieldInfo {
	y := yieldInfo{status: TurnCompleted}
	for i := len(messages) - 1; i >= 0; i-- {
		var h assistantHead
		if json.Unmarshal(messages[i], &h) == nil && h.Role == "assistant" {
			y = assistantOutcome(h)
			break
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if role, text := messageText(messages[i]); role == "assistant" && strings.TrimSpace(text) != "" {
			y.text = truncText(text, maxTurnTextRunes)
			break
		}
	}
	return y
}

// truncText 把 s 截到至多 n 个字符。
func truncText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// recoverPage 是 RecoveredOutcome 读条目的分页大小。
const recoverPage = 500

// RecoveredOutcome 从已入库的转录推断一次没有标签关联的投递（重启前已下发）的终态（补充契约 C1.6）。
// anchorPrefix 是该投递首条用户消息正文的开头（来源头行前缀，见 C9.3）；since 是投递时间（毫秒）。
// 等同于以「正文以 anchorPrefix 开头」为匹配函数调用 RecoveredOutcomeMatch。
func (s *Service) RecoveredOutcome(ctx context.Context, threadID, anchorPrefix string, since int64) (ev TurnEvent, ok bool, err error) {
	return s.RecoveredOutcomeMatch(ctx, threadID, func(text string) bool { return strings.HasPrefix(text, anchorPrefix) }, since)
}

// RecoveredOutcomeMatch 是 RecoveredOutcome 的一般形式：锚点是活动分支上 ts >= since、match(正文) 为真的第一条 user 消息。
// 取锚点之后、下一条 user 消息之前，最后一条「结束回合」的 assistant 消息（stopReason 不是 toolUse / 空）：
//
//	stopReason == "error"   → TurnEvent{Status: TurnError, Error: errorMessage 或 "provider request failed"}
//	stopReason == "aborted" → TurnEvent{Status: TurnAborted}
//	其它                     → TurnEvent{Status: TurnCompleted}
//
// Text / HasText 取锚点之后到该消息为止最后一条有文本的 assistant 消息（同 C1 规则 4）。
// ok=false：锚点不存在，或锚点之后还没有结束回合的 assistant 消息（转录复制尚未赶上，或回合未结束）。
func (s *Service) RecoveredOutcomeMatch(ctx context.Context, threadID string, match func(text string) bool, since int64) (ev TurnEvent, ok bool, err error) {
	row, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnEvent{}, false, ErrNotFound
	}
	if err != nil {
		return TurnEvent{}, false, err
	}
	var all []transcript.Entry
	after := int64(0)
	for {
		rows, err := s.st.Q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: threadID, AfterSeq: after, Limit: recoverPage})
		if err != nil {
			return TurnEvent{}, false, err
		}
		for _, e := range rows {
			all = append(all, transcript.Entry{Seq: e.Seq, File: e.File, EntryID: e.EntryID, ParentID: e.ParentID, Type: e.Type, TS: e.Ts, Payload: e.Payload})
			after = e.Seq
		}
		if len(rows) < recoverPage {
			break
		}
	}
	leaf := ""
	if row.LeafEntryID != nil {
		leaf = *row.LeafEntryID
	}
	branch := transcript.ActiveBranch(all, leaf)
	anchor := -1
	for i, e := range branch {
		m, isMsg := transcript.ParseMessage(e)
		if isMsg && m.Role == "user" && entryTime(e) >= since && match(m.Text) {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return TurnEvent{}, false, nil
	}
	var (
		end      *yieldInfo
		lastText string
		hasText  bool
	)
	for _, e := range branch[anchor+1:] {
		m, isMsg := transcript.ParseMessage(e)
		if !isMsg {
			continue
		}
		if m.Role == "user" {
			break
		}
		if m.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(m.Text) != "" {
			lastText, hasText = m.Text, true
		}
		var p struct {
			Message assistantHead `json:"message"`
		}
		if json.Unmarshal(e.Payload, &p) != nil || p.Message.StopReason == "" || p.Message.StopReason == "toolUse" {
			continue
		}
		y := assistantOutcome(p.Message)
		if hasText {
			y.text = truncText(lastText, maxTurnTextRunes)
		}
		end = &y
	}
	if end == nil {
		return TurnEvent{}, false, nil
	}
	return TurnEvent{ThreadID: threadID, Status: end.status, Error: end.err, Text: end.text, HasText: true}, true, nil
}

// entryTime 是条目的时间（毫秒）：取条目的 timestamp，缺失时取 message.timestamp。
func entryTime(e transcript.Entry) int64 {
	if e.TS != nil {
		return *e.TS
	}
	var p struct {
		Message struct {
			Timestamp int64 `json:"timestamp"`
		} `json:"message"`
	}
	_ = json.Unmarshal(e.Payload, &p)
	return p.Message.Timestamp
}
