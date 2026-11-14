package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lastAssistantText 从 agent_end.messages 里取最后一条 assistant 文本。
func lastAssistantText(t *testing.T, end map[string]any) string {
	t.Helper()
	msgs, _ := end["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i].(map[string]any)
		if m["role"] != "assistant" {
			continue
		}
		var b strings.Builder
		for _, c := range m["content"].([]any) {
			if cm := c.(map[string]any); cm["type"] == "text" {
				b.WriteString(cm["text"].(string))
			}
		}
		return b.String()
	}
	t.Fatal("no assistant message in agent_end")
	return ""
}

func findType(frames []map[string]any, typ string) map[string]any {
	for _, f := range frames {
		if f["type"] == typ {
			return f
		}
	}
	return nil
}

// M9：set_host_tools / get_active_tools，以及 hosttool 回合的帧序列与结果文本。
func TestHostToolTurn(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--session-dir", dir, "--delay", "0s", "--no-tools", "--no-skills", "--system-prompt", "a\nb"}, nil)
	handshake(t, h, dir)

	r, _ := h.call(map[string]any{"id": "s1", "type": "set_host_tools", "tools": []any{
		map[string]any{"name": "create_thread", "description": "make", "parameters": map[string]any{"type": "object"}},
		map[string]any{"name": "list_threads", "description": "list", "parameters": map[string]any{"type": "object"}},
	}})
	require.Equal(t, true, r["success"], r)
	assert.Equal(t, map[string]any{"toolNames": []any{"create_thread", "list_threads"}}, r["data"])
	r, _ = h.call(map[string]any{"id": "g1", "type": "get_active_tools"})
	require.Equal(t, true, r["success"], r)
	tools := r["data"].(map[string]any)["tools"].([]any)
	require.Len(t, tools, 2)
	assert.Equal(t, "create_thread", tools[0].(map[string]any)["name"])

	h.send(map[string]any{"id": "p1", "type": "prompt", "message": "[Oryn header]\nhosttool create_thread {\"prompt\":\"x\"}"})
	frames := h.until("host_tool_call")
	call := frames[len(frames)-1]
	assert.Equal(t, "h-1", call["id"])
	assert.Equal(t, "create_thread", call["toolName"])
	assert.Equal(t, map[string]any{"prompt": "x"}, call["arguments"])
	start := findType(frames, "tool_execution_start")
	require.NotNil(t, start)
	assert.Equal(t, call["toolCallId"], start["toolCallId"])

	h.send(map[string]any{"type": "host_tool_result", "id": "h-1", "result": map[string]any{
		"content": []any{map[string]any{"type": "text", "text": `{"id":"T-1"}`}}, "details": map[string]any{"threadIds": []any{"T-1"}},
	}})
	frames = h.until("session_settled")
	end := findType(frames, "tool_execution_end")
	require.NotNil(t, end)
	assert.Equal(t, false, end["isError"])
	assert.Equal(t, `host:create_thread:ok:{"id":"T-1"}`, lastAssistantText(t, findType(frames, "agent_end")))
	assert.Equal(t, "completed", findType(frames, "prompt_result")["status"])

	// 会话文件里有 toolResult 条目。
	b := readSessionFiles(t, dir)
	assert.Contains(t, b, `"role":"toolResult"`)
	assert.Contains(t, b, `"toolName":"create_thread"`)
}

// 回合被中断时发 host_tool_cancel 并以 aborted 收尾。
func TestHostToolCancel(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--session-dir", dir, "--delay", "0s"}, nil)
	handshake(t, h, dir)
	h.send(map[string]any{"id": "p1", "type": "prompt", "message": "hosttool archive_thread {\"threadId\":\"T-9\"}"})
	h.until("host_tool_call")
	h.send(map[string]any{"id": "a1", "type": "abort"})
	frames := h.until("prompt_result")
	cancel := findType(frames, "host_tool_cancel")
	require.NotNil(t, cancel)
	assert.Equal(t, "h-1", cancel["targetId"])
	end := findType(frames, "tool_execution_end")
	require.NotNil(t, end)
	assert.Equal(t, true, end["isError"])
	assert.Equal(t, "aborted", findType(frames, "prompt_result")["status"])
}

// probe 行在本进程内执行；`probe args` 给出参数、环境键与 uid。
func TestProbeTurn(t *testing.T) {
	dir := t.TempDir()
	readable := filepath.Join(dir, "r.txt")
	require.NoError(t, os.WriteFile(readable, []byte("x"), 0o600))
	args := []string{"--session-dir", dir, "--delay", "0s", "--no-tools", "--config", "o.yml"}
	h := start(t, args, nil)
	handshake(t, h, dir)
	h.send(map[string]any{"id": "p1", "type": "prompt", "message": strings.Join([]string{
		"probe read " + readable,
		"probe read " + filepath.Join(dir, "missing"),
		"probe write " + filepath.Join(dir, "w.txt"),
		"probe tcp 127.0.0.1:1",
		"probe args",
	}, "\n")})
	frames := h.until("session_settled")
	lines := strings.Split(lastAssistantText(t, findType(frames, "agent_end")), "\n")
	require.Len(t, lines, 5)
	assert.Equal(t, "probe:read:ok", lines[0])
	assert.True(t, strings.HasPrefix(lines[1], "probe:read:") && lines[1] != "probe:read:ok", lines[1])
	assert.Equal(t, "probe:write:ok", lines[2])
	assert.NotEqual(t, "probe:tcp:ok", lines[3])
	raw, ok := strings.CutPrefix(lines[4], "probe:args:")
	require.True(t, ok, lines[4])
	var got struct {
		Args    []string `json:"args"`
		EnvKeys []string `json:"envKeys"`
		UID     int      `json:"uid"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &got))
	assert.Equal(t, args, got.Args)
	assert.Equal(t, os.Getuid(), got.UID)
	assert.NotEmpty(t, got.EnvKeys)
}

// FAKEOMP_DUMP_DIR 与 --system-prompt 首行 `#dump <路径>` 写出参数与环境；HOME 之外的路径被忽略。
func TestDumpInvocation(t *testing.T) {
	home := t.TempDir()
	dumpDir := t.TempDir()
	inside := filepath.Join(home, "d", "dump.json")
	outside := filepath.Join(t.TempDir(), "dump.json")
	for _, tc := range []struct {
		prompt string
		want   string
	}{
		{"#dump " + inside + "\nYou are a probe.", inside},
		{"#dump " + outside + "\nYou are a probe.", ""},
	} {
		env := map[string]string{"HOME": home, "FAKEOMP_DUMP_DIR": dumpDir, "ORYN_THREAD_ID": "T-dump"}
		args := []string{"--session-dir", t.TempDir(), "--no-tools", "--system-prompt", tc.prompt}
		h := start(t, args, env)
		require.Equal(t, "ready", h.next()["type"])

		var rec dumpRecord
		b, err := os.ReadFile(filepath.Join(dumpDir, "T-dump.json"))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(b, &rec))
		assert.Equal(t, args, rec.Args)
		assert.Equal(t, os.Getuid(), rec.UID)
		assert.NotEmpty(t, rec.Env)
		if tc.want != "" {
			_, err := os.Stat(tc.want)
			assert.NoError(t, err)
		}
	}
	_, err := os.Stat(outside)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func readSessionFiles(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	require.NoError(t, err)
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		b.Write(data)
	}
	return b.String()
}
