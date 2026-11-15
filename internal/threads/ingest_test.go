package threads

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLine(t *testing.T) {
	p, err := ParseLine(`{"type":"title","v":1,"title":"你好","pad":"   "}`)
	require.NoError(t, err)
	assert.Equal(t, EntryIDTitle, p.EntryID)
	assert.Equal(t, "你好", p.Title)

	p, err = ParseLine(`{"type":"session","version":3,"id":"0190-uuid","timestamp":"2026-11-12T10:00:00.000Z","cwd":"/x"}`)
	require.NoError(t, err)
	assert.Equal(t, EntryIDSession, p.EntryID)
	require.NotNil(t, p.TS)
	assert.Equal(t, int64(1794477600000), *p.TS)

	line := `{"type":"message","id":"a1b2c3d4","parentId":"00000001","timestamp":"2026-11-12T10:00:01.000Z","message":{"role":"assistant","timestamp":1794477601000,"provider":"anthropic","model":"claude","duration":1200.4,"ttft":300,"usage":{"input":10,"output":5,"cacheRead":2,"cacheWrite":1,"cost":{"total":0.01}},"content":[{"type":"image","data":"blob:sha256:` + strings.Repeat("a", 64) + `"}]}}`
	p, err = ParseLine(line + "\n")
	require.NoError(t, err)
	assert.Equal(t, "a1b2c3d4", p.EntryID)
	assert.Equal(t, "00000001", *p.ParentID)
	require.NotNil(t, p.Usage)
	assert.Equal(t, int64(10), p.Usage.Input)
	assert.InDelta(t, 0.01, p.Usage.CostUSD, 1e-9)
	assert.Equal(t, int64(300), *p.Usage.TTFTMs)
	assert.Equal(t, []string{strings.Repeat("a", 64)}, p.BlobRefs)
	assert.Equal(t, line, string(p.Payload))

	p, err = ParseLine(`{"type":"message","id":"b","parentId":"a","timestamp":"2026-11-12T10:00:02Z","message":{"role":"toolResult","toolName":"bash","isError":true,"timestamp":5}}`)
	require.NoError(t, err)
	require.NotNil(t, p.Tool)
	assert.Equal(t, ToolCall{TS: 5, Tool: "bash", IsError: true}, *p.Tool)

	p, err = ParseLine(`{"type":"model_usage","id":"u1","parentId":"a","activeLeafId":"c","timestamp":"2026-11-12T10:00:03Z"}`)
	require.NoError(t, err)
	assert.True(t, p.OffBranch)
	require.NotNil(t, p.ActiveLeaf)
	assert.Equal(t, "c", *p.ActiveLeaf)
	p, err = ParseLine(`{"type":"model_usage","id":"u2","parentId":"a","activeLeafId":null}`)
	require.NoError(t, err)
	assert.True(t, p.OffBranch)
	assert.Nil(t, p.ActiveLeaf)
	p, err = ParseLine(`{"type":"model_usage","id":"u3","parentId":"a"}`)
	require.NoError(t, err)
	assert.False(t, p.OffBranch)

	_, err = ParseLine(`not json`)
	assert.Error(t, err)
	assert.True(t, IsSubagentFile("2026_x/sub.jsonl"))
	assert.False(t, IsSubagentFile("2026_x.jsonl"))
}

// 经 write xd://<tool> 派发的工具结果按 details.xdev.tool 归属；只认 execute 模式，details 形状异常不影响解析。
func TestParseLineXdevTool(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    ToolCall
	}{
		{"execute 派发归到真实工具", `{"role":"toolResult","toolName":"write","timestamp":7,"details":{"xdev":{"tool":"lsp","mode":"execute","args":{"op":"hover"},"inner":{"x":1}}}}`, ToolCall{TS: 7, Tool: "lsp"}},
		{"失败的派发也归到真实工具", `{"role":"toolResult","toolName":"write","isError":true,"timestamp":7,"details":{"xdev":{"tool":"lsp","mode":"execute","args":{"op":"hover"}}}}`, ToolCall{TS: 7, Tool: "lsp", IsError: true}},
		{"设备不存在（无 args）仍记为 write", `{"role":"toolResult","toolName":"write","isError":true,"timestamp":7,"details":{"xdev":{"tool":"ast_grep","mode":"execute"}}}`, ToolCall{TS: 7, Tool: "write", IsError: true}},
		{"help 模式仍记为 write", `{"role":"toolResult","toolName":"write","timestamp":7,"details":{"xdev":{"tool":"lsp","mode":"help"}}}`, ToolCall{TS: 7, Tool: "write"}},
		{"details 不是对象", `{"role":"toolResult","toolName":"write","timestamp":7,"details":"x"}`, ToolCall{TS: 7, Tool: "write"}},
		{"xdev.tool 为空", `{"role":"toolResult","toolName":"write","timestamp":7,"details":{"xdev":{"tool":"","mode":"execute","args":{}}}}`, ToolCall{TS: 7, Tool: "write"}},
		{"非 write 工具不改名", `{"role":"toolResult","toolName":"bash","timestamp":7,"details":{"xdev":{"tool":"lsp","mode":"execute","args":{}}}}`, ToolCall{TS: 7, Tool: "bash"}},
		{"没有 details", `{"role":"toolResult","toolName":"write","timestamp":7}`, ToolCall{TS: 7, Tool: "write"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := ParseLine(`{"type":"message","id":"t1","parentId":"a","message":` + c.message + `}`)
			require.NoError(t, err)
			require.NotNil(t, p.Tool)
			assert.Equal(t, c.want, *p.Tool)
		})
	}
}
