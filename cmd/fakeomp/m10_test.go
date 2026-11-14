package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 提示词含 `toolfail`：tool_execution_end 帧与 toolResult 条目都带 isError: true；普通回合两者都为 false。
func TestToolFailMarksError(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		want   bool
	}{{"please toolfail now", true}, {"plain run", false}} {
		dir := t.TempDir()
		h := start(t, []string{"--mode", "rpc-ui", "--session-dir", dir, "--delay", "1ms"}, nil)
		open := handshake(t, h, dir)
		r, _ := h.call(map[string]any{"id": "p1", "type": "prompt", "message": tc.prompt})
		require.Equal(t, true, r["success"], r)
		frames := h.until("session_settled")
		var end map[string]any
		for _, f := range frames {
			if f["type"] == "tool_execution_end" {
				end = f
			}
		}
		require.NotNil(t, end, tc.prompt)
		assert.Equal(t, tc.want, end["isError"], tc.prompt)

		_, entries := readSession(t, open["sessionFile"].(string))
		var tool map[string]any
		for _, e := range entries {
			var m map[string]any
			if e.Type == "message" && json.Unmarshal(e.Message, &m) == nil && m["role"] == "toolResult" {
				tool = m
			}
		}
		require.NotNil(t, tool, tc.prompt)
		assert.Equal(t, tc.want, tool["isError"], tc.prompt)
	}
}

// 默认（两个开关都关）：回合不写 model_usage，fork_from 是 Unknown command（与不支持它的 agent 一致）。
func TestM10SwitchesDefaultOff(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--mode", "rpc-ui", "--session-dir", dir, "--delay", "1ms"}, nil)
	open := handshake(t, h, dir)
	r, _ := h.call(map[string]any{"id": "p1", "type": "prompt", "message": "one"})
	require.Equal(t, true, r["success"], r)
	h.until("session_settled")
	_, entries := readSession(t, open["sessionFile"].(string))
	require.Len(t, entries, 2+4)
	for _, e := range entries {
		assert.NotEqual(t, "model_usage", e.Type)
	}
	r, _ = h.call(map[string]any{"id": "f1", "type": "fork_from", "entryId": entries[3].ID})
	assert.Equal(t, false, r["success"])
	assert.Equal(t, "Unknown command: fork_from", r["error"])
}

// FAKEOMP_MODEL_USAGE=1：回合结束前追加分支外的 model_usage{purpose:"title"}：parentId = 本回合 user 条目，
// activeLeafId = 当前叶子（最后一条 assistant）；续接会话时它不是叶子，下一轮接在 assistant 之后。
func TestModelUsageEntry(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--mode", "rpc-ui", "--session-dir", dir, "--delay", "1ms"}, map[string]string{"FAKEOMP_MODEL_USAGE": "1"})
	open := handshake(t, h, dir)
	src := open["sessionFile"].(string)
	r, _ := h.call(map[string]any{"id": "p1", "type": "prompt", "message": "one"})
	require.Equal(t, true, r["success"], r)
	frames := h.until("session_settled")
	for _, f := range frames {
		if f["type"] == "agent_end" {
			assert.Len(t, f["messages"], 4, "model_usage 不计入 agent_end.messages")
		}
	}
	raw, entries := readSession(t, src)
	require.Len(t, entries, 2+5)
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	var mu map[string]any
	require.NoError(t, json.Unmarshal(lines[6], &mu))
	assert.Equal(t, "model_usage", mu["type"])
	assert.Regexp(t, hex8, mu["id"])
	assert.Equal(t, entries[2].ID, mu["parentId"])
	assert.Equal(t, entries[5].ID, mu["activeLeafId"])
	assert.Equal(t, "title", mu["purpose"])
	assert.Equal(t, "fake", mu["api"])
	assert.Equal(t, "stop", mu["stopReason"])
	assert.Equal(t, "fake", mu["provider"])
	assert.Equal(t, "fake-model", mu["model"])
	assert.Equal(t, 0.00081, mu["usage"].(map[string]any)["cost"].(map[string]any)["total"])
	_, err := time.Parse("2006-01-02T15:04:05.000Z", mu["timestamp"].(string))
	assert.NoError(t, err)

	// 重新 open_session 同一目录（从文件续接）：下一轮的 user 接在 assistant 之后，不接在 model_usage 后
	r, _ = h.call(map[string]any{"id": "o2", "type": "open_session", "sessionDir": dir})
	require.Equal(t, true, r["success"], r)
	r, _ = h.call(map[string]any{"id": "p2", "type": "prompt", "message": "two"})
	require.Equal(t, true, r["success"], r)
	h.until("session_settled")
	_, entries = readSession(t, src)
	require.Len(t, entries, 2+10)
	assert.Equal(t, entries[5].ID, *entries[7].ParentID)
}

// FAKEOMP_FORK=1：fork_from 把到 entryId 为止的分支逐字拷进同目录的新会话文件（分支外的 model_usage 不拷），
// 当前会话不变；未知条目报错。
func TestForkFromCopiesBranch(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--mode", "rpc-ui", "--session-dir", dir, "--delay", "1ms"},
		map[string]string{"FAKEOMP_FORK": "1", "FAKEOMP_MODEL_USAGE": "1"})
	open := handshake(t, h, dir)
	src := open["sessionFile"].(string)
	r, _ := h.call(map[string]any{"id": "p1", "type": "prompt", "message": "one"})
	require.Equal(t, true, r["success"], r)
	h.until("session_settled")
	raw, entries := readSession(t, src)
	require.Len(t, entries, 2+5)
	srcLines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))

	r, _ = h.call(map[string]any{"id": "f1", "type": "fork_from", "entryId": entries[5].ID})
	require.Equal(t, true, r["success"], r)
	data := r["data"].(map[string]any)
	dst := data["sessionPath"].(string)
	assert.Equal(t, dir, filepath.Dir(dst))
	assert.NotEqual(t, src, dst)
	assert.FileExists(t, dst)
	assert.NoFileExists(t, dst+".tmp")

	draw, dentries := readSession(t, dst)
	require.Len(t, dentries, 2+4, "user → assistant → toolResult → assistant；分支外的 model_usage 不拷")
	assert.Equal(t, "title", dentries[0].Type)
	assert.Equal(t, "session", dentries[1].Type)
	assert.Equal(t, data["sessionId"], dentries[1].ID)
	dstLines := bytes.Split(bytes.TrimSpace(draw), []byte("\n"))
	for i := range 4 {
		assert.Equal(t, string(srcLines[2+i]), string(dstLines[2+i]), "条目逐字拷贝（同 id、同时间戳）")
	}

	// 源会话不变：下一轮仍写进源文件
	r, _ = h.call(map[string]any{"id": "p2", "type": "prompt", "message": "two"})
	require.Equal(t, true, r["success"], r)
	h.until("session_settled")
	_, entries = readSession(t, src)
	assert.Len(t, entries, 2+10)

	r, _ = h.call(map[string]any{"id": "f2", "type": "fork_from", "entryId": "ffffffff"})
	assert.Equal(t, false, r["success"])
	assert.Equal(t, "fork_from", r["command"])
}

// 加载会话时分支外条目（activeLeafId）不当叶子：叶子取 activeLeafId（null = 清空）。
func TestLoadSessionHonorsActiveLeaf(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s, err := createSession(dir, dir, now)
	require.NoError(t, err)
	u, err := s.appendMessage(map[string]any{"role": "user"}, now)
	require.NoError(t, err)
	a, err := s.appendMessage(map[string]any{"role": "assistant"}, now)
	require.NoError(t, err)
	mu, err := s.appendOffBranch("model_usage", u, map[string]any{"purpose": "title"}, now)
	require.NoError(t, err)
	assert.Equal(t, a, s.leaf, "追加分支外条目不动叶子")

	got, err := loadSession(s.path)
	require.NoError(t, err)
	assert.Equal(t, a, got.leaf)
	assert.True(t, got.ids[mu])

	// activeLeafId: null → 叶子清空
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"model_usage","id":"0000beef","parentId":"` + u + `","timestamp":"2026-11-15T00:00:00.000Z","activeLeafId":null}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	got, err = loadSession(s.path)
	require.NoError(t, err)
	assert.Equal(t, "", got.leaf)
}
