package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionLines 读会话文件的全部行（含标题槽与会话头），各解成 map。
func sessionLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		out = append(out, m)
	}
	return out
}

func sources(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["type"] == "custom" && l["customType"] == modelSourceType {
			out = append(out, l["data"].(map[string]any))
		}
	}
	return out
}

// 全新会话：mark 写入 startup 记录且在首条 user 消息之前；第二次 mark 与 prompt 之后的 mark 都 recorded:false、文件行数不变。
func TestMarkSessionStartStartup(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--cwd", dir, "--session-dir", dir, "--delay", "1ms"}, nil)
	file := handshake(t, h, dir)["sessionFile"].(string)

	r, _ := h.call(map[string]any{"id": "m1", "type": "oryn_mark_session_start", "explicitModel": false})
	require.Equal(t, true, r["success"], r)
	assert.Equal(t, map[string]any{"recorded": true, "source": "startup", "role": "default", "model": "fake/fake-model"}, r["data"])
	lines := sessionLines(t, file)
	src := sources(lines)
	require.Len(t, src, 1)
	assert.Equal(t, map[string]any{"v": 1.0, "source": "startup", "role": "default", "model": "fake/fake-model"}, src[0])
	rec := lines[len(lines)-1]
	assert.Regexp(t, hex8, rec["id"])
	assert.NotEmpty(t, rec["timestamp"])

	r, _ = h.call(map[string]any{"id": "m2", "type": "oryn_mark_session_start"})
	assert.Equal(t, map[string]any{"recorded": false}, r["data"], "已有记录：不再写")
	assert.Len(t, sessionLines(t, file), len(lines))

	r, _ = h.call(map[string]any{"id": "p1", "type": "prompt", "message": "hi"})
	require.Equal(t, true, r["success"], r)
	h.until("session_settled")
	lines = sessionLines(t, file)
	var firstUser, recordAt = -1, -1
	for i, l := range lines {
		if l["type"] == "message" && firstUser < 0 {
			firstUser = i
		}
		if l["type"] == "custom" {
			recordAt = i
		}
	}
	require.GreaterOrEqual(t, firstUser, 0)
	assert.Less(t, recordAt, firstUser, "记录在首条 user 消息之前")
	assert.Equal(t, rec["id"], lines[firstUser]["parentId"], "user 消息接在记录之后")

	r, _ = h.call(map[string]any{"id": "m3", "type": "oryn_mark_session_start"})
	assert.Equal(t, map[string]any{"recorded": false}, r["data"], "已有消息：不写")
	assert.Len(t, sessionLines(t, file), len(lines))
}

// explicitModel 为真 → manual、没有 role 键；会话里已有 message（无记录）时也不写。
func TestMarkSessionStartExplicitAndAfterMessage(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--cwd", dir, "--session-dir", dir, "--delay", "1ms"}, nil)
	file := handshake(t, h, dir)["sessionFile"].(string)
	r, _ := h.call(map[string]any{"id": "m1", "type": "oryn_mark_session_start", "explicitModel": true})
	assert.Equal(t, map[string]any{"recorded": true, "source": "manual", "model": "fake/fake-model"}, r["data"])
	src := sources(sessionLines(t, file))
	require.Len(t, src, 1)
	_, hasRole := src[0]["role"]
	assert.False(t, hasRole, "manual 记录没有 role")

	dir2 := t.TempDir()
	h2 := start(t, []string{"--cwd", dir2, "--session-dir", dir2, "--delay", "1ms"}, nil)
	file2 := handshake(t, h2, dir2)["sessionFile"].(string)
	r, _ = h2.call(map[string]any{"id": "p1", "type": "prompt", "message": "hi"})
	require.Equal(t, true, r["success"], r)
	h2.until("session_settled")
	n := len(sessionLines(t, file2))
	r, _ = h2.call(map[string]any{"id": "m1", "type": "oryn_mark_session_start"})
	assert.Equal(t, map[string]any{"recorded": false}, r["data"])
	assert.Len(t, sessionLines(t, file2), n)
	assert.Empty(t, sources(sessionLines(t, file2)))
}

// role 形式：模型不变、追加 model_change{role} + role 记录，不发 model_changed；手动形式：换模型、model_change{role:default}
// + manual 记录（没有 role 键），发 model_changed；非法参数不写条目。
func TestSelectModel(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--cwd", dir, "--session-dir", dir, "--delay", "1ms"}, nil)
	file := handshake(t, h, dir)["sessionFile"].(string)

	r, others := h.call(map[string]any{"id": "s1", "type": "oryn_select_model", "role": "Mare"})
	require.Equal(t, true, r["success"], r)
	data := r["data"].(map[string]any)
	assert.Equal(t, "role", data["source"])
	assert.Equal(t, "Mare", data["role"])
	assert.Equal(t, "fake", data["model"].(map[string]any)["provider"])
	assert.Empty(t, types(others), "模型没变：不发 model_changed")
	lines := sessionLines(t, file)
	require.GreaterOrEqual(t, len(lines), 2)
	mc, rec := lines[len(lines)-2], lines[len(lines)-1]
	assert.Equal(t, "model_change", mc["type"])
	assert.Equal(t, "fake/fake-model", mc["model"])
	assert.Equal(t, "Mare", mc["role"])
	assert.Equal(t, mc["id"], rec["parentId"])
	assert.Equal(t, map[string]any{"v": 1.0, "source": "role", "role": "Mare", "model": "fake/fake-model"}, rec["data"])

	r, others = h.call(map[string]any{"id": "s2", "type": "oryn_select_model", "provider": "openai", "modelId": "gpt-6.1-sol"})
	require.Equal(t, true, r["success"], r)
	data = r["data"].(map[string]any)
	assert.Equal(t, "manual", data["source"])
	_, hasRole := data["role"]
	assert.False(t, hasRole)
	assert.Equal(t, []string{"model_changed"}, types(others))
	lines = sessionLines(t, file)
	mc, rec = lines[len(lines)-2], lines[len(lines)-1]
	assert.Equal(t, "openai/gpt-6.1-sol", mc["model"])
	assert.Equal(t, "default", mc["role"])
	assert.Equal(t, map[string]any{"v": 1.0, "source": "manual", "model": "openai/gpt-6.1-sol"}, rec["data"])
	st, _ := h.call(map[string]any{"id": "g", "type": "get_state"})
	assert.Equal(t, "gpt-6.1-sol", st["data"].(map[string]any)["model"].(map[string]any)["id"])

	n := len(lines)
	for frame, msg := range map[string]string{
		`{}`: "oryn_select_model requires either role or provider+modelId",
		`{"role":"A","provider":"p","modelId":"m"}`: "oryn_select_model requires either role or provider+modelId",
		`{"provider":"p"}`:                          "provider and modelId must be given together",
		`{"role":""}`:                               "oryn_select_model requires either role or provider+modelId",
	} {
		var c map[string]any
		require.NoError(t, json.Unmarshal([]byte(frame), &c))
		c["id"], c["type"] = "bad", "oryn_select_model"
		r, _ := h.call(c)
		assert.Equal(t, false, r["success"], frame)
		assert.Equal(t, msg, r["error"], frame)
	}
	assert.Len(t, sessionLines(t, file), n, "非法参数不写条目")
}

// FAKEOMP_LEGACY_CORE=1：两条命令都是 Unknown command，不写任何条目。
func TestLegacyCoreUnknownCommands(t *testing.T) {
	dir := t.TempDir()
	h := start(t, []string{"--cwd", dir, "--session-dir", dir}, map[string]string{"FAKEOMP_LEGACY_CORE": "1"})
	file := handshake(t, h, dir)["sessionFile"].(string)
	n := len(sessionLines(t, file))
	for _, typ := range []string{"oryn_mark_session_start", "oryn_select_model"} {
		r, _ := h.call(map[string]any{"id": "x", "type": typ, "role": "Mare"})
		assert.Equal(t, false, r["success"])
		assert.Equal(t, typ, r["command"])
		assert.Equal(t, "Unknown command: "+typ, r["error"])
	}
	assert.Len(t, sessionLines(t, file), n)
}
