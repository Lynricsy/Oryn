package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// termOutputs 从帧中取出某终端的 terminal_output，返回拼接的 data 与最后一帧的 seq。
func termOutputs(frames []map[string]any, id string) (string, int64) {
	var data strings.Builder
	var seq int64
	for _, f := range frames {
		if f["type"] == "terminal_output" && f["terminalId"] == id {
			data.WriteString(f["data"].(string))
			seq = int64(f["seq"].(float64))
		}
	}
	return data.String(), seq
}

func TestTerminalOpenInputSnapshot(t *testing.T) {
	h := start(t, []string{"--mode", "rpc-ui"}, nil)
	handshake(t, h, t.TempDir())

	r, _ := h.call(map[string]any{"id": "c1", "type": "terminal_configure", "interactiveCommands": true, "agentTools": true})
	require.Equal(t, true, r["success"], r)
	assert.Equal(t, map[string]any{"interactiveCommands": true, "agentTools": true}, r["data"])

	r, frames := h.call(map[string]any{"id": "t1", "type": "terminal_open", "cols": 80, "rows": 24})
	require.Equal(t, true, r["success"], r)
	info := r["data"].(map[string]any)
	id := info["terminalId"].(string)
	assert.Equal(t, "term-fake-1", id)
	assert.Equal(t, "shell", info["kind"])
	assert.EqualValues(t, 80, info["cols"])
	assert.EqualValues(t, 24, info["rows"])
	assert.Equal(t, true, info["running"])
	require.Len(t, frames, 2)
	assert.Equal(t, "terminal_opened", frames[0]["type"])
	assert.Equal(t, id, frames[0]["terminal"].(map[string]any)["terminalId"])
	assert.Equal(t, "terminal_output", frames[1]["type"])
	assert.Equal(t, "fake$ ", frames[1]["data"])

	all := append([]map[string]any{}, frames...)
	r, frames = h.call(map[string]any{"id": "i1", "type": "terminal_input", "terminalId": id, "data": "echo hi"})
	require.Equal(t, true, r["success"], r)
	all = append(all, frames...)
	r, frames = h.call(map[string]any{"id": "i2", "type": "terminal_input", "terminalId": id, "data": "\r"})
	require.Equal(t, true, r["success"], r)
	all = append(all, frames...)

	r, frames = h.call(map[string]any{"id": "r1", "type": "terminal_resize", "terminalId": id, "cols": 120, "rows": 40})
	require.Equal(t, true, r["success"], r)
	assert.Empty(t, frames, "resize 不发帧")

	r, _ = h.call(map[string]any{"id": "s1", "type": "terminal_snapshot", "terminalId": id})
	require.Equal(t, true, r["success"], r)
	snap := r["data"].(map[string]any)
	data, lastSeq := termOutputs(all, id)
	assert.Equal(t, "fake$ echo hi\r\nfake$ ", data)
	assert.Equal(t, data, snap["data"])
	assert.EqualValues(t, len(data), snap["seq"], "seq = 各 terminal_output.data 长度之和")
	assert.EqualValues(t, lastSeq, snap["seq"])
	assert.EqualValues(t, 120, snap["terminal"].(map[string]any)["cols"])

	r, _ = h.call(map[string]any{"id": "l1", "type": "terminal_list"})
	terms := r["data"].(map[string]any)["terminals"].([]any)
	require.Len(t, terms, 1)
	assert.Equal(t, id, terms[0].(map[string]any)["terminalId"])

	// exit 结束进程但终端保留；之后的输入失败。
	_, frames = h.call(map[string]any{"id": "i3", "type": "terminal_input", "terminalId": id, "data": "exit\r"})
	var exit map[string]any
	for _, f := range frames {
		if f["type"] == "terminal_exit" {
			exit = f
		}
	}
	require.NotNil(t, exit, "exit 发出 terminal_exit")
	assert.EqualValues(t, 0, exit["exitCode"])
	assert.Equal(t, false, exit["cancelled"])
	assert.Equal(t, false, exit["timedOut"])
	r, _ = h.call(map[string]any{"id": "i4", "type": "terminal_input", "terminalId": id, "data": "x"})
	assert.Equal(t, false, r["success"])

	r, frames = h.call(map[string]any{"id": "x1", "type": "terminal_close", "terminalId": id})
	require.Equal(t, true, r["success"], r)
	require.Len(t, frames, 1)
	assert.Equal(t, map[string]any{"type": "terminal_closed", "terminalId": id}, frames[0])
	r, _ = h.call(map[string]any{"id": "l2", "type": "terminal_list"})
	assert.Empty(t, r["data"].(map[string]any)["terminals"])
	r, _ = h.call(map[string]any{"id": "s2", "type": "terminal_snapshot", "terminalId": id})
	assert.Equal(t, false, r["success"])
}

// flood：每帧 32768 个 x，seq 连续；快照只保留最近 262144 个字符且 seq 等于累计字符数。
func TestTerminalFlood(t *testing.T) {
	h := start(t, []string{"--mode", "rpc-ui"}, nil)
	handshake(t, h, t.TempDir())
	r, _ := h.call(map[string]any{"id": "t1", "type": "terminal_open"})
	id := r["data"].(map[string]any)["terminalId"].(string)
	assert.EqualValues(t, 100, r["data"].(map[string]any)["cols"])
	assert.EqualValues(t, 30, r["data"].(map[string]any)["rows"])

	const n = 300000
	h.send(map[string]any{"id": "i1", "type": "terminal_input", "terminalId": id, "data": "flood 300000\r"})
	var seq int64 = int64(len("fake$ "))
	xs := 0
	sawResp := false
	for {
		f := h.next()
		if f["type"] == "response" && f["id"] == "i1" {
			sawResp = true
			continue
		}
		require.Equal(t, "terminal_output", f["type"], f)
		d := f["data"].(string)
		seq += int64(len(d))
		require.EqualValues(t, seq, f["seq"], "seq 累加且无缺口")
		if strings.HasSuffix(d, "fake$ ") {
			break
		}
		if strings.Trim(d, "x") == "" {
			assert.LessOrEqual(t, len(d), 32768)
			xs += len(d)
		}
	}
	if !sawResp {
		r := h.next()
		assert.Equal(t, "response", r["type"])
		assert.Equal(t, "i1", r["id"])
	}
	assert.Equal(t, n, xs)

	r, _ = h.call(map[string]any{"id": "s1", "type": "terminal_snapshot", "terminalId": id})
	snap := r["data"].(map[string]any)
	assert.EqualValues(t, seq, snap["seq"])
	data := snap["data"].(string)
	assert.Len(t, data, 262144)
	assert.True(t, strings.HasSuffix(data, "x\r\nfake$ "))
}

// 提示词含 pty：reveal 的 command 终端提问，收到 y 之后终端退出、回合照常结束。
func TestPtyPromptWaitsForTerminalInput(t *testing.T) {
	h := start(t, []string{"--mode", "rpc-ui"}, map[string]string{"FAKEOMP_DELAY_MS": "0"})
	handshake(t, h, t.TempDir())
	h.send(map[string]any{"id": "p1", "type": "prompt", "message": "run it with pty please"})
	frames := h.until("terminal_opened")
	opened := frames[len(frames)-1]
	assert.Equal(t, true, opened["reveal"])
	term := opened["terminal"].(map[string]any)
	assert.Equal(t, "command", term["kind"])
	id := term["terminalId"].(string)
	out := h.next()
	assert.Equal(t, "terminal_output", out["type"])
	assert.Equal(t, "Continue? [y/N] ", out["data"])

	// 作答之前回合不前进。
	r, others := h.call(map[string]any{"id": "l1", "type": "terminal_list"})
	for _, f := range others {
		assert.NotEqual(t, "message_start", f["type"], "回合在作答前不得继续")
	}
	assert.Len(t, r["data"].(map[string]any)["terminals"], 1)

	h.send(map[string]any{"id": "i1", "type": "terminal_input", "terminalId": id, "data": "y\r"})
	rest := h.until("prompt_result")
	var exitIdx, msgIdx = -1, -1
	for i, f := range rest {
		switch f["type"] {
		case "terminal_exit":
			exitIdx = i
			assert.EqualValues(t, 0, f["exitCode"])
		case "message_start":
			if msgIdx < 0 {
				msgIdx = i
			}
		}
	}
	require.GreaterOrEqual(t, exitIdx, 0, "terminal_exit")
	require.Greater(t, msgIdx, exitIdx, "作答后回合继续")
	assert.Equal(t, "completed", rest[len(rest)-1]["status"])
}

// 不含独立单词 pty 的提示词（如 empty）不进入终端分支。
func TestPtyTriggerIsWordOnly(t *testing.T) {
	assert.True(t, ptyWordRe.MatchString("use pty:true"))
	assert.True(t, ptyWordRe.MatchString("PTY"))
	assert.False(t, ptyWordRe.MatchString("an empty file"))
}

// omp 的 terminal_output 帧以 `{"type":"terminal_output"` 起头（orbd 据此判断可丢弃）：替身保持同样的字节形状。
func TestTerminalOutputFramePrefix(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = run(context.Background(), []string{"--mode", "rpc-ui"}, inR, outW, io.Discard, func(string) string { return "" })
		_ = outW.Close()
	}()
	t.Cleanup(func() { _ = inW.Close(); _ = outR.Close() })
	sc := bufio.NewScanner(outR)
	deadline := time.After(10 * time.Second)
	lines := make(chan []byte, 16)
	go func() {
		for sc.Scan() {
			lines <- append([]byte(nil), sc.Bytes()...)
		}
		close(lines)
	}()
	_, err := inW.Write([]byte(`{"id":"t1","type":"terminal_open"}` + "\n"))
	require.NoError(t, err)
	for {
		select {
		case l, ok := <-lines:
			require.True(t, ok, "stdout closed")
			var m map[string]any
			require.NoError(t, json.Unmarshal(l, &m))
			if m["type"] == "terminal_output" {
				assert.True(t, bytes.HasPrefix(l, []byte(`{"type":"terminal_output"`)), string(l))
				return
			}
		case <-deadline:
			t.Fatal("no terminal_output")
		}
	}
}

// seq 与快照回滚按 JavaScript 字符串长度（UTF-16 码元）计，与 omp 的 rpc-terminal.ts 一致：
// 非 ASCII 输入不得造成 seq 空洞。
func TestTerminalSeqUsesUTF16Units(t *testing.T) {
	h := start(t, []string{"--mode", "rpc-ui"}, nil)
	handshake(t, h, t.TempDir())
	r, _ := h.call(map[string]any{"id": "t1", "type": "terminal_open"})
	id := r["data"].(map[string]any)["terminalId"].(string)
	_, frames := h.call(map[string]any{"id": "i1", "type": "terminal_input", "terminalId": id, "data": "你好🙂\x7f"})
	r, _ = h.call(map[string]any{"id": "s1", "type": "terminal_snapshot", "terminalId": id})
	snap := r["data"].(map[string]any)
	data, seq := termOutputs(frames, id)
	want := "fake$ 你好🙂\b \b"
	assert.Equal(t, want, snap["data"])
	assert.Equal(t, "你好🙂\b \b", data, "退格删掉整个字符")
	assert.EqualValues(t, len(utf16.Encode([]rune(want))), seq)
	assert.EqualValues(t, 13, snap["seq"])
}

func TestTailUnitsKeepsCharacterBoundaries(t *testing.T) {
	rs := []rune("a🙂b")
	assert.EqualValues(t, 4, utf16Len(rs))
	assert.Equal(t, "b", string(tailUnits(rs, 2)), "不拆开代理对")
	assert.Equal(t, "🙂b", string(tailUnits(rs, 3)))
	assert.Equal(t, "a🙂b", string(tailUnits(rs, 10)))
}
