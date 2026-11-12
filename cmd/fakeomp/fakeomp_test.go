package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/omprpc"
)

// harness 在进程内运行 fakeomp，经管道驱动 stdin/stdout。
type harness struct {
	t      *testing.T
	stdin  *io.PipeWriter
	frames chan map[string]any
	done   chan int
}

func start(t *testing.T, args []string, env map[string]string) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := &harness{t: t, stdin: inW, frames: make(chan map[string]any, 4096), done: make(chan int, 1)}
	getenv := func(k string) string { return env[k] }
	go func() {
		code := run(context.Background(), args, inR, outW, io.Discard, getenv)
		_ = outW.Close()
		h.done <- code
	}()
	go func() {
		defer close(h.frames)
		d := omprpc.NewDecoder(outR)
		for {
			f, err := d.Next()
			if err != nil {
				if omprpc.IsRecoverable(err) {
					t.Errorf("recoverable frame error: %v", err)
					continue
				}
				return
			}
			var m map[string]any
			if err := json.Unmarshal(f, &m); err != nil {
				t.Errorf("bad frame: %v", err)
				continue
			}
			h.frames <- m
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
	})
	return h
}

func (h *harness) send(frame map[string]any) {
	h.t.Helper()
	b, err := json.Marshal(frame)
	require.NoError(h.t, err)
	_, err = h.stdin.Write(append(b, '\n'))
	require.NoError(h.t, err)
}

func (h *harness) next() map[string]any {
	h.t.Helper()
	select {
	case f, ok := <-h.frames:
		require.True(h.t, ok, "stdout closed")
		return f
	case <-time.After(10 * time.Second):
		h.t.Fatal("timeout waiting for frame")
		return nil
	}
}

// call 发送命令并返回对应 id 的应答（之间的其它帧也一并返回）。
func (h *harness) call(frame map[string]any) (map[string]any, []map[string]any) {
	h.t.Helper()
	h.send(frame)
	var others []map[string]any
	for {
		f := h.next()
		if f["type"] == "response" && f["id"] == frame["id"] {
			return f, others
		}
		others = append(others, f)
	}
}

// until 收集帧直到出现 type == stop 的帧（含）。
func (h *harness) until(stop string) []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for {
		f := h.next()
		out = append(out, f)
		if f["type"] == stop {
			return out
		}
	}
}

func types(frames []map[string]any) []string {
	var out []string
	for _, f := range frames {
		t := f["type"].(string)
		// 连续的 message_update 折叠成一个，便于比较顺序。
		if t == "message_update" && len(out) > 0 && out[len(out)-1] == t {
			continue
		}
		out = append(out, t)
	}
	return out
}

func handshake(t *testing.T, h *harness, sessionDir string) map[string]any {
	t.Helper()
	ready := h.next()
	assert.Equal(t, "ready", ready["type"])
	assert.EqualValues(t, 1, ready["protocolVersion"])
	assert.Equal(t, []any{1.0, 2.0}, ready["supportedProtocolVersions"])
	assert.EqualValues(t, omprpc.MaxFrameBytes, ready["maxFrameBytes"])
	assert.EqualValues(t, omprpc.MaxReassembledBytes, ready["maxReassembledFrameBytes"])

	r, _ := h.call(map[string]any{"id": "n1", "type": "negotiate_protocol", "protocolVersion": 2})
	require.Equal(t, true, r["success"], r)
	assert.Equal(t, map[string]any{"protocolVersion": 2.0}, r["data"])

	open, _ := h.call(map[string]any{"id": "o1", "type": "open_session", "sessionDir": sessionDir})
	require.Equal(t, true, open["success"], open)

	for _, c := range []map[string]any{
		{"id": "f1", "type": "set_event_filter", "events": nil, "messageUpdates": "delta"},
		{"id": "a1", "type": "set_ask_dialog", "enabled": true, "form": true},
		{"id": "s1", "type": "set_subagent_subscription", "level": "progress"},
		{"id": "g1", "type": "get_state"},
	} {
		r, _ := h.call(c)
		require.Equal(t, true, r["success"], r)
		assert.Equal(t, c["type"], r["command"])
	}
	return open["data"].(map[string]any)
}

type entryLine struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

func readSession(t *testing.T, path string) ([]byte, []entryLine) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var entries []entryLine
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var e entryLine
		require.NoError(t, json.Unmarshal(sc.Bytes(), &e))
		entries = append(entries, e)
	}
	return raw, entries
}

var hex8 = regexp.MustCompile(`^[0-9a-f]{8}$`)

func TestPromptSequenceAndSessionFile(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--mode", "rpc-ui", "--cwd", dir, "--session-dir", dir, "--delay", "1ms"}, nil)
	open := handshake(t, h, dir)
	assert.Equal(t, false, open["resumed"])
	sessionFile := open["sessionFile"].(string)
	assert.Equal(t, dir, filepath.Dir(sessionFile))

	// 未知命令。
	r, _ := h.call(map[string]any{"id": "u1", "type": "bogus_command"})
	assert.Equal(t, false, r["success"])
	assert.Equal(t, "bogus_command", r["command"])
	assert.Equal(t, "Unknown command: bogus_command", r["error"])

	r, _ = h.call(map[string]any{"id": "p1", "type": "prompt", "message": "hello there"})
	require.Equal(t, true, r["success"], r)
	frames := h.until("session_settled")
	assert.Equal(t, []string{
		"agent_start",
		"message_start", "message_update", "message_end",
		"tool_execution_start", "tool_execution_update", "tool_execution_end",
		"message_start", "message_update", "message_end",
		"agent_end", "prompt_result", "session_settled",
	}, types(frames))

	var deltas []string
	var ends []map[string]any
	for _, f := range frames {
		switch f["type"] {
		case "message_update":
			ev := f["assistantMessageEvent"].(map[string]any)
			if ev["type"] == "text_delta" {
				deltas = append(deltas, ev["delta"].(string))
			}
			assert.Equal(t, map[string]any{"role": "assistant"}, f["message"], "delta 投影只保留 role")
		case "message_end":
			ends = append(ends, f["message"].(map[string]any))
		case "agent_end":
			assert.Equal(t, true, f["isTerminal"])
			assert.Equal(t, true, f["yielded"])
			assert.Len(t, f["messages"], 4)
		case "prompt_result":
			assert.Equal(t, "p1", f["id"])
			assert.Equal(t, "completed", f["status"])
			assert.Equal(t, true, f["sessionSettled"])
		}
	}
	assert.GreaterOrEqual(t, len(deltas), 2)
	require.Len(t, ends, 2)
	var ids []any
	for _, f := range frames {
		if f["type"] == "message_start" || f["type"] == "message_end" {
			ids = append(ids, f["messageId"])
		}
	}
	require.Len(t, ids, 4)
	assert.Equal(t, ids[0], ids[1])
	assert.Equal(t, ids[2], ids[3])
	assert.NotEqual(t, ids[0], ids[2])

	raw, entries := readSession(t, sessionFile)
	assert.Equal(t, byte('\n'), raw[255], "标题槽恰好 256 字节")
	assert.Equal(t, "title", entries[0].Type)
	assert.Equal(t, "session", entries[1].Type)
	assert.Equal(t, open["sessionId"], entries[1].ID)
	msgs := entries[2:]
	require.Len(t, msgs, 4)
	roles := []string{}
	prev := ""
	for i, e := range msgs {
		assert.Equal(t, "message", e.Type)
		assert.Regexp(t, hex8, e.ID)
		if i == 0 {
			assert.Nil(t, e.ParentID)
		} else {
			require.NotNil(t, e.ParentID)
			assert.Equal(t, prev, *e.ParentID)
		}
		_, err := time.Parse("2006-01-02T15:04:05.000Z", e.Timestamp)
		assert.NoError(t, err)
		prev = e.ID
		var m map[string]any
		require.NoError(t, json.Unmarshal(e.Message, &m))
		roles = append(roles, m["role"].(string))
		if m["role"] == "assistant" {
			assert.Contains(t, m, "usage")
		}
	}
	assert.Equal(t, []string{"user", "assistant", "toolResult", "assistant"}, roles)

	// 条目中 assistant 消息的 (role, timestamp) 与 message_end 帧一致（实时缓冲的匹配键）。
	var a1, a2 map[string]any
	require.NoError(t, json.Unmarshal(msgs[1].Message, &a1))
	require.NoError(t, json.Unmarshal(msgs[3].Message, &a2))
	assert.Equal(t, ends[0]["timestamp"], a1["timestamp"])
	assert.Equal(t, ends[1]["timestamp"], a2["timestamp"])

	state, _ := h.call(map[string]any{"id": "g2", "type": "get_state"})
	st := state["data"].(map[string]any)
	assert.Equal(t, false, st["isStreaming"])
	assert.Equal(t, true, st["isSettled"])
	assert.Equal(t, sessionFile, st["sessionFile"])

	require.NoError(t, h.stdin.Close())
	select {
	case code := <-h.done:
		assert.Equal(t, 0, code)
	case <-time.After(5 * time.Second):
		t.Fatal("fakeomp did not exit on stdin EOF")
	}
}

func TestAskWaitsForUIResponse(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--mode", "rpc-ui", "--session-dir", dir, "--delay", "1ms"}, nil)
	handshake(t, h, dir)

	r, _ := h.call(map[string]any{"id": "p1", "type": "prompt", "message": "please ask me"})
	require.Equal(t, true, r["success"])
	pre := h.until("extension_ui_request")
	assert.Equal(t, []string{"agent_start", "extension_ui_request"}, types(pre))
	req := pre[len(pre)-1]
	assert.Equal(t, "select", req["method"])
	assert.NotEmpty(t, req["title"])
	assert.Contains(t, req["options"], "beta")

	// 未应答前不会继续。
	select {
	case f := <-h.frames:
		t.Fatalf("unexpected frame before ui response: %v", f)
	case <-time.After(100 * time.Millisecond):
	}

	h.send(map[string]any{"type": "extension_ui_response", "id": req["id"], "value": "beta"})
	frames := h.until("session_settled")
	var text strings.Builder
	for _, f := range frames {
		if f["type"] == "message_update" {
			if ev := f["assistantMessageEvent"].(map[string]any); ev["type"] == "text_delta" {
				text.WriteString(ev["delta"].(string))
			}
		}
	}
	assert.Contains(t, text.String(), "You picked beta")
}

func TestAbortDuringAsk(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--session-dir", dir, "--delay", "1ms"}, nil)
	handshake(t, h, dir)
	h.call(map[string]any{"id": "p1", "type": "prompt", "message": "ask"})
	h.until("extension_ui_request")
	r, others := h.call(map[string]any{"id": "x1", "type": "abort"})
	assert.Equal(t, true, r["success"])
	frames := append(others, h.until("session_settled")...)
	var result map[string]any
	var cancel bool
	for _, f := range frames {
		if f["type"] == "prompt_result" {
			result = f
		}
		if f["type"] == "extension_ui_request" && f["method"] == "cancel" {
			cancel = true
		}
	}
	require.NotNil(t, result)
	assert.Equal(t, "aborted", result["status"])
	assert.True(t, cancel, "abort cancels the pending ui request")
}

func TestScriptOverride(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(t.TempDir(), "script.jsonl")
	require.NoError(t, os.WriteFile(script, []byte(strings.Join([]string{
		`# 自定义序列`,
		`{"entry":{"role":"user","content":[{"type":"text","text":"{{prompt}}"}]}}`,
		`{"emit":{"type":"agent_start"}}`,
		`{"emit":{"type":"message_start","message":{"role":"assistant","content":[],"timestamp":1}}}`,
		`{"emit":{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"echo: {{prompt}}"}],"timestamp":1}}}`,
		`{"entry":{"role":"assistant","content":[{"type":"text","text":"echo: {{prompt}}"}],"timestamp":1}}`,
		`{"emit":{"type":"agent_end","messages":[],"isTerminal":true,"yielded":true}}`,
		`{"emit":{"type":"prompt_result","agentInvoked":true,"status":"completed","sessionSettled":true}}`,
		`{"emit":{"type":"session_settled"}}`,
	}, "\n")), 0o644))

	h := start(t, []string{"--session-dir", dir}, map[string]string{"FAKEOMP_SCRIPT": script})
	open := handshake(t, h, dir)
	h.call(map[string]any{"id": "p9", "type": "prompt", "message": `say "hi"`})
	frames := h.until("session_settled")
	assert.Equal(t, []string{"agent_start", "message_start", "message_end", "agent_end", "prompt_result", "session_settled"}, types(frames))
	assert.Equal(t, frames[1]["messageId"], frames[2]["messageId"])
	assert.Equal(t, "p9", frames[4]["id"])

	_, entries := readSession(t, open["sessionFile"].(string))
	require.Len(t, entries, 4)
	var m map[string]any
	require.NoError(t, json.Unmarshal(entries[3].Message, &m))
	assert.Equal(t, `echo: say "hi"`, m["content"].([]any)[0].(map[string]any)["text"])
}

func TestResumeAndChunkedOutput(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--session-dir", dir, "--delay", "0s"}, nil)
	open := handshake(t, h, dir)
	// 1.5MiB 的提示词：agent_end 超过物理行上限，协议 v2 下应分块输出并被解码器重组。
	big := strings.Repeat("x", 3<<19)
	h.call(map[string]any{"id": "p1", "type": "prompt", "message": big})
	frames := h.until("session_settled")
	var agentEnd map[string]any
	for _, f := range frames {
		if f["type"] == "agent_end" {
			agentEnd = f
		}
	}
	require.NotNil(t, agentEnd)
	user := agentEnd["messages"].([]any)[0].(map[string]any)
	assert.Equal(t, big, user["content"].([]any)[0].(map[string]any)["text"])

	// 再次 open_session 同一目录：续接已有文件。
	r, _ := h.call(map[string]any{"id": "o2", "type": "open_session", "sessionDir": dir})
	data := r["data"].(map[string]any)
	assert.Equal(t, true, data["resumed"])
	assert.Equal(t, open["sessionFile"], data["sessionFile"])
	h.call(map[string]any{"id": "p2", "type": "prompt", "message": "again"})
	h.until("session_settled")
	_, entries := readSession(t, open["sessionFile"].(string))
	require.Len(t, entries, 2+8)
	// 第二轮的 user 条目接在第一轮最后一条之后。
	assert.Equal(t, entries[5].ID, *entries[6].ParentID)
}

func TestVersionAndBadMode(t *testing.T) {
	var out bytes.Buffer
	code := run(context.Background(), []string{"--version"}, strings.NewReader(""), &out, io.Discard, func(string) string { return "" })
	assert.Equal(t, 0, code)
	assert.True(t, strings.HasPrefix(out.String(), "omp/"))

	code = run(context.Background(), []string{"--mode", "text"}, strings.NewReader(""), io.Discard, io.Discard, func(string) string { return "" })
	assert.Equal(t, 2, code)
}
