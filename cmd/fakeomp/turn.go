package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// turn 是一次 prompt 的执行上下文。
type turn struct {
	s        *server
	ctx      context.Context
	promptID string
	messages []any // 本回合新增的消息（agent_end.messages）
	openMsgs []string
	settled  bool // finish 已更新会话状态（defer 不再重复）
}

func nowMs() int64 { return time.Now().UnixMilli() }

// ptyWordRe 匹配提示词中独立的单词 `pty`（不误伤 empty 之类的词）。
var ptyWordRe = regexp.MustCompile(`(?i)\bpty\b`)

func (s *server) runTurn(base context.Context, p promptReq) {
	ctx, cancel := context.WithCancel(base)
	s.mu.Lock()
	s.streaming = true
	s.cancelTurn = cancel
	s.mu.Unlock()
	t := &turn{s: s, ctx: ctx, promptID: p.id}
	defer func() {
		cancel()
		t.settle()
	}()

	if base.Err() != nil {
		return
	}
	if s.script != nil {
		t.runScript(p)
		return
	}
	t.runDefault(p)
}

// sleep 等待一个流式间隔；回合被中断时返回 false。
func (t *turn) sleep(d time.Duration) bool {
	if d <= 0 {
		return t.ctx.Err() == nil
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-t.ctx.Done():
		return false
	case <-tm.C:
		return true
	}
}

func (t *turn) step() bool { return t.sleep(t.s.opts.delay) }

// appendEntry 把消息写入会话文件并计入 agent_end.messages。
func (t *turn) appendEntry(msg map[string]any) {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	if _, err := t.s.sess.appendMessage(msg, time.Now()); err != nil {
		t.s.emit(map[string]any{"type": "notice", "level": "error", "message": "fakeomp: append entry: " + err.Error()})
		return
	}
	t.s.messageCount++
	t.messages = append(t.messages, msg)
}

func (t *turn) nextMessageID() string {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	t.s.msgSeq++
	return "msg-" + strconv.Itoa(t.s.msgSeq)
}

// settle 把会话标记为非流式并结束本回合的待办计数；只生效一次。返回此后会话是否静止
// （没有已接受但未结束的回合）。
func (t *turn) settle() bool {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	if !t.settled {
		t.settled = true
		t.s.streaming = false
		t.s.cancelTurn = nil
		t.s.pending--
	}
	return t.s.pending == 0
}

// finish 发出回合结尾帧：agent_end → prompt_result[→ session_settled]。
// 状态在 prompt_result 之前更新：收到 session_settled 后的 get_state 必须已是静止态（与 omp 一致）。
// 还有排队的回合时会话并未静止（omp rpc.md「Yield vs settled」）：sessionSettled=false 且不发 session_settled，
// session_settled 只在最后一个排队回合结束时发出一次。
func (t *turn) finish(status string) {
	t.s.emitEvent(map[string]any{"type": "agent_end", "messages": nonNil(t.messages), "isTerminal": true, "yielded": true})
	settled := t.settle()
	pr := map[string]any{"type": "prompt_result", "agentInvoked": true, "status": status, "sessionSettled": settled}
	if t.promptID != "" {
		pr["id"] = t.promptID
	}
	t.s.emit(pr)
	if settled {
		t.s.emit(map[string]any{"type": "session_settled"})
	}
}

func nonNil(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

func userContent(text string, images []json.RawMessage) []any {
	content := []any{map[string]any{"type": "text", "text": text}}
	for _, img := range images {
		content = append(content, img)
	}
	return content
}

func (t *turn) runDefault(p promptReq) {
	user := map[string]any{
		"role": "user", "content": userContent(p.message, p.images), "attribution": "user", "timestamp": nowMs(),
	}
	t.appendEntry(user)
	t.s.emitEvent(map[string]any{"type": "agent_start"})

	intro := "I'll run a quick command to check the environment."
	if strings.Contains(strings.ToLower(p.message), "ask") {
		answer, ok := t.ask(map[string]any{
			"method": "select", "title": "fakeomp: pick an option", "options": []string{"alpha", "beta", "gamma"},
		})
		if !ok {
			t.finish("aborted")
			return
		}
		intro = "You picked " + answer + ". " + intro
	}
	// 提示词含单词 `pty`：模拟一次 `pty: true` 的 bash 调用，等用户在 reveal 的 command 终端里作答。
	if ptyWordRe.MatchString(p.message) {
		if !t.ptyCommand() {
			t.finish("aborted")
			return
		}
		intro = "The interactive command finished. " + intro
	}

	callID := "call_" + randHex(8)
	args := map[string]any{"command": "echo hello"}
	if !t.streamAssistant(intro, &toolCall{id: callID, name: "bash", args: args}, "toolUse") {
		t.finish("aborted")
		return
	}

	output := "hello\n"
	partial := map[string]any{"content": []any{map[string]any{"type": "text", "text": output}}}
	if !t.step() {
		t.finish("aborted")
		return
	}
	t.s.emitEvent(map[string]any{"type": "tool_execution_start", "toolCallId": callID, "toolName": "bash", "args": args, "intent": "Running echo"})
	if !t.step() {
		t.finish("aborted")
		return
	}
	t.s.emitEvent(map[string]any{"type": "tool_execution_update", "toolCallId": callID, "toolName": "bash", "args": args, "partialResult": partial})
	if !t.step() {
		t.finish("aborted")
		return
	}
	result := map[string]any{"content": []any{map[string]any{"type": "text", "text": output}}, "details": map[string]any{}}
	t.s.emitEvent(map[string]any{"type": "tool_execution_end", "toolCallId": callID, "toolName": "bash", "result": result, "isError": false})
	t.appendEntry(map[string]any{
		"role": "toolResult", "toolCallId": callID, "toolName": "bash",
		"content": result["content"], "details": map[string]any{}, "isError": false, "timestamp": nowMs(),
	})

	if !t.streamAssistant("Done. The command printed hello. You said: "+p.message, nil, "stop") {
		t.finish("aborted")
		return
	}
	t.finish("completed")
}

// ask 发出 extension_ui_request 并等待 extension_ui_response；返回所选值。
func (t *turn) ask(req map[string]any) (string, bool) {
	t.s.mu.Lock()
	t.s.uiSeq++
	id := fmt.Sprintf("ui-%d", t.s.uiSeq)
	ch := make(chan json.RawMessage, 1)
	t.s.uiWait[id] = ch
	t.s.mu.Unlock()

	frame := map[string]any{}
	for k, v := range req {
		frame[k] = v
	}
	frame["type"] = "extension_ui_request"
	frame["id"] = id
	t.s.emit(frame)

	select {
	case raw := <-ch:
		var r struct {
			Value     *string `json:"value"`
			Confirmed *bool   `json:"confirmed"`
			Cancelled bool    `json:"cancelled"`
		}
		_ = json.Unmarshal(raw, &r)
		switch {
		case r.Value != nil:
			return *r.Value, true
		case r.Confirmed != nil:
			return strconv.FormatBool(*r.Confirmed), true
		default:
			return "(cancelled)", true
		}
	case <-t.ctx.Done():
		t.s.mu.Lock()
		delete(t.s.uiWait, id)
		t.s.mu.Unlock()
		t.s.emit(map[string]any{"type": "extension_ui_request", "id": fmt.Sprintf("ui-cancel-%s", id), "method": "cancel", "targetId": id})
		return "", false
	}
}

type toolCall struct {
	id   string
	name string
	args map[string]any
}

func fakeUsage() map[string]any {
	return map[string]any{
		"input": 120, "output": 30, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 150,
		"cost": map[string]any{"input": 0.00036, "output": 0.00045, "cacheRead": 0, "cacheWrite": 0, "total": 0.00081},
	}
}

// streamAssistant 流式发出一条 assistant 消息并在 message_end 时写入会话文件。
// 事件形状：message_start → message_update(text_start, text_delta×N, text_end[, toolcall_start, toolcall_end], done) → message_end。
func (t *turn) streamAssistant(text string, tc *toolCall, stopReason string) bool {
	t.s.mu.Lock()
	provider, model := t.s.modelLocked()
	delta := t.s.messageUpdates == "delta"
	t.s.mu.Unlock()

	started := time.Now()
	content := []any{}
	msg := map[string]any{
		"role": "assistant", "content": content, "api": "fake", "provider": provider, "model": model,
		"usage": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 0,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}},
		"stopReason": "stop", "timestamp": nowMs(),
	}
	mid := t.nextMessageID()

	if !t.step() {
		return false
	}
	t.s.emitEvent(map[string]any{"type": "message_start", "message": msg, "messageId": mid})

	update := func(ev map[string]any, block any) bool {
		if !t.step() {
			return false
		}
		frame := map[string]any{"type": "message_update", "messageId": mid}
		if delta {
			if block != nil {
				ev["block"] = block
			}
			frame["message"] = map[string]any{"role": "assistant"}
		} else {
			ev["partial"] = msg
			frame["message"] = msg
		}
		frame["assistantMessageEvent"] = ev
		t.s.emitEvent(frame)
		return true
	}

	textBlock := map[string]any{"type": "text", "text": ""}
	content = append(content, textBlock)
	msg["content"] = content
	if !update(map[string]any{"type": "text_start", "contentIndex": 0}, map[string]any{"type": "text", "text": ""}) {
		return false
	}
	for _, piece := range splitDeltas(text, 4) {
		textBlock["text"] = textBlock["text"].(string) + piece
		if !update(map[string]any{"type": "text_delta", "contentIndex": 0, "delta": piece}, nil) {
			return false
		}
	}
	if !update(map[string]any{"type": "text_end", "contentIndex": 0, "content": text}, map[string]any{"type": "text"}) {
		return false
	}
	if tc != nil {
		callBlock := map[string]any{"type": "toolCall", "id": tc.id, "name": tc.name, "arguments": map[string]any{}}
		content = append(content, callBlock)
		msg["content"] = content
		if !update(map[string]any{"type": "toolcall_start", "contentIndex": 1},
			map[string]any{"type": "toolCall", "id": tc.id, "name": tc.name, "arguments": map[string]any{}}) {
			return false
		}
		callBlock["arguments"] = tc.args
		if !update(map[string]any{"type": "toolcall_end", "contentIndex": 1, "toolCall": callBlock}, nil) {
			return false
		}
	}
	msg["usage"] = fakeUsage()
	msg["stopReason"] = stopReason
	msg["duration"] = float64(time.Since(started).Microseconds()) / 1000
	msg["ttft"] = float64(t.s.opts.delay.Microseconds()) / 1000
	msg["completedAt"] = nowMs()
	if !t.step() {
		return false
	}
	// done 不带 partial；delta 投影原样透传 done 事件，只把外层 message 缩成 {role}。
	var outer any = msg
	if delta {
		outer = map[string]any{"role": "assistant"}
	}
	t.s.emitEvent(map[string]any{"type": "message_update", "messageId": mid,
		"message": outer, "assistantMessageEvent": map[string]any{"type": "done", "reason": stopReason, "message": msg}})
	if !t.step() {
		return false
	}
	t.s.emitEvent(map[string]any{"type": "message_end", "message": msg, "messageId": mid})
	t.appendEntry(msg)
	return true
}

// splitDeltas 把文本按字符切成至多 n 段（保持 UTF-8 完整）。
func splitDeltas(text string, n int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	size := (len(runes) + n - 1) / n
	var out []string
	for i := 0; i < len(runes); i += size {
		out = append(out, string(runes[i:min(i+size, len(runes))]))
	}
	return out
}
