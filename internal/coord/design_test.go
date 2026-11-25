package coord

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M29 C5：助手经 oryn 技能的原生操作发牌、选定方向（HTML 画框画布回 :root）、跑 check_artifact；这些操作不在顶层工具表里，
// Space 协调者调用回 unknown_tool；artifacts 域文件带「Design canvases」专题。
func TestDesignAssistantTools(t *testing.T) {
	e := newEnv(t, true)
	e.withArtifacts()
	asst := e.assistant()

	id := details(t, e.call(asst, ToolCreateArtifact, `{"title":"画布","files":[{"path":"canvas.json","content":"{\"format\":1,\"canvases\":[{\"id\":\"c1\",\"name\":\"画布\"}],\"pages\":[],\"notes\":[]}"}]}`, true))["id"].(string)
	r := e.call(asst, ToolOryn, `{"op":"design_directions","args":{"id":"`+id+`","surface":"landing","industry":"咖啡店"}}`, true)
	require.False(t, r.IsError, r.Text)
	assert.Contains(t, r.Text, "1. ")
	assert.Contains(t, r.Text, "design_direction")
	r = e.call(asst, ToolOryn, `{"op":"design_direction","args":{"id":"`+id+`","pick":2}}`, true)
	require.False(t, r.IsError, r.Text)
	assert.Contains(t, r.Text, ":root {")

	page := details(t, e.call(asst, ToolCreateArtifact, `{"title":"页","html":"<p>x</p>"}`, true))["id"].(string)
	errText(t, e.call(asst, ToolOryn, `{"op":"design_directions","args":{"id":"`+page+`","surface":"landing"}}`, true), "invalid")

	names := map[string]bool{}
	for _, d := range ToolDefs(ToolSet{}) {
		names[d["name"].(string)] = true
	}
	chat := e.spaceChat(e.space("Alpha", ""))
	for _, n := range []string{ToolCheckArtifact, ToolDesignDirections, ToolDesignDirection, ToolDesignNotes, ToolDesignResolveNote} {
		assert.False(t, names[n], "%s 是 oryn 技能的原生操作，不在顶层", n)
		assert.Equal(t, "artifacts", skillNativeDomain[n], n)
		errText(t, e.call(chat, n, `{"id":"`+id+`"}`, true), "unknown_tool")
	}

	dom := string(SkillFiles()[skillDir+"domains/artifacts.md"])
	assert.Contains(t, dom, "## Design canvases")
	assert.True(t, strings.Index(dom, "## Design canvases") < strings.Index(dom, "## Operations"))
	assert.Contains(t, dom, ":root")
	assert.Contains(t, dom, "@theme")
}
