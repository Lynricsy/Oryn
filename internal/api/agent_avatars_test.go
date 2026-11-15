package api_test

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
)

const agentAvatarsPath = "/api/v1/settings/agent-avatars"

type agentAvatarJSON struct {
	Name        string  `json:"name"`
	Source      string  `json:"source"`
	Description *string `json:"description"`
	AvatarIcon  *string `json:"avatarIcon"`
	AvatarImage *string `json:"avatarImage"`
}

type agentAvatarListJSON struct {
	Agents   []agentAvatarJSON `json:"agents"`
	Warnings []struct {
		Path, Kind, Message string
	} `json:"warnings"`
}

func (e *h2Env) agentAvatars(c h2Cred) agentAvatarListJSON {
	e.t.Helper()
	r := e.do("GET", agentAvatarsPath, c, nil)
	require.Equal(e.t, 200, r.status, string(r.body))
	var out agentAvatarListJSON
	r.json(e.t, &out)
	return out
}

func (e *h2Env) putAgentFile(c h2Cred, name, content string) {
	e.t.Helper()
	r := e.do("PUT", "/api/v1/agent-config/files/agents%2F"+name, c, []byte(content))
	require.Equal(e.t, 200, r.status, string(r.body))
}

func (e *h2Env) deleteAgentFile(c h2Cred, name string) {
	e.t.Helper()
	r := e.do("DELETE", "/api/v1/agent-config/files/agents%2F"+name, c, nil)
	require.Less(e.t, r.status, 300, string(r.body))
}

// agentKeys 把列表压成 "name:source" 便于整体比较顺序。
func agentKeys(l agentAvatarListJSON) []string {
	out := make([]string, len(l.Agents))
	for i, a := range l.Agents {
		out[i] = a.Name + ":" + a.Source
	}
	return out
}

func agentByName(t *testing.T, l agentAvatarListJSON, name string) agentAvatarJSON {
	t.Helper()
	for _, a := range l.Agents {
		if a.Name == name {
			return a
		}
	}
	require.Failf(t, "agent 不在列表里", "%s: %v", name, agentKeys(l))
	return agentAvatarJSON{}
}

func bundledKeys(skip ...string) []string {
	out := []string{}
	for _, n := range agentcfg.BundledAgents {
		if !slices.Contains(skip, n) {
			out = append(out, n+":bundled")
		}
	}
	return out
}

func TestAgentAvatarsAuth(t *testing.T) {
	e := newH2Env(t)
	assert.Equal(t, 401, e.do("GET", agentAvatarsPath, h2Cred{}, nil).status)
	assert.Equal(t, 401, e.do("PUT", agentAvatarsPath, h2Cred{}, map[string]any{"name": "scout"}).status)
	assert.Equal(t, 403, e.do("PUT", agentAvatarsPath, h2Cred{cookie: e.webSession()}, map[string]any{"name": "scout", "avatarIcon": "🤖"}).status)
	assert.Equal(t, 200, e.do("GET", agentAvatarsPath, h2Cred{cookie: e.webSession()}, nil).status)
}

func TestAgentAvatarsListAndPut(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	// 只有内置 agent，按固定顺序，没有头像与描述
	l := e.agentAvatars(c)
	assert.Equal(t, bundledKeys(), agentKeys(l))
	assert.Empty(t, l.Warnings)

	// 用户 agent：按名字排在内置之后；与内置同名的只列一次（source=user）；解析失败的进 warnings
	e.putAgentFile(c, "foo.md", "---\nname: foo\ndescription: 找东西\n---\n正文\n")
	e.putAgentFile(c, "bar.md", "---\nname: Bar\n---\n")
	e.putAgentFile(c, "my-reviewer.md", "---\nname: reviewer\ndescription: 我的审查\n---\n")
	e.putAgentFile(c, "broken.md", "没有 frontmatter\n")
	l = e.agentAvatars(c)
	assert.Equal(t, append(bundledKeys("reviewer"), "Bar:user", "foo:user", "reviewer:user"), agentKeys(l))
	foo := agentByName(t, l, "foo")
	require.NotNil(t, foo.Description)
	assert.Equal(t, "找东西", *foo.Description)
	assert.Nil(t, agentByName(t, l, "scout").Description)
	require.Len(t, l.Warnings, 1)
	assert.Equal(t, "agents/broken.md", l.Warnings[0].Path)
	assert.NotEmpty(t, l.Warnings[0].Kind)

	// 内置 agent 设头像：列表 source=bundled 且带头像
	r := e.do("PUT", agentAvatarsPath, c, map[string]any{"name": "scout", "avatarIcon": " 🔭 "})
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"name":"scout","source":"bundled","avatarIcon":"🔭"}`, string(r.body))
	scout := agentByName(t, e.agentAvatars(c), "scout")
	assert.Equal(t, "bundled", scout.Source)
	require.NotNil(t, scout.AvatarIcon)
	assert.Equal(t, "🔭", *scout.AvatarIcon)

	// 用户 agent 设图片头像：返回带描述
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	r = e.do("PUT", agentAvatarsPath, c, map[string]any{"name": "foo", "avatarImage": png})
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"name":"foo","source":"user","description":"找东西","avatarImage":"`+png+`"}`, string(r.body))

	// 错误：未知名字、名字非法、头像非法；被拒的请求不写库
	big := "data:image/webp;base64," + strings.Repeat("A", 128<<10)
	r = e.do("PUT", agentAvatarsPath, c, map[string]any{"name": "foo", "avatarImage": big})
	assert.Equal(t, 413, r.status)
	for _, bad := range []map[string]any{
		{"name": "nobody", "avatarIcon": "🤖"},
		{"name": "FOO", "avatarIcon": "🤖"}, // 大小写不同即不同 agent
		{"name": "broken", "avatarIcon": "🤖"},
		{"name": "", "avatarIcon": "🤖"},
		{"name": "  "},
		{"name": strings.Repeat("n", 129)},
		{"name": "a\nb"},
		{"name": "foo", "avatarIcon": "🤖", "avatarImage": png},
		{"name": "foo", "avatarIcon": "robot"},
	} {
		r := e.do("PUT", agentAvatarsPath, c, bad)
		assert.Equal(t, 400, r.status, "%v: %s", bad, r.body)
		assert.Equal(t, "invalid_request", r.code(t))
	}
	rows, err := e.st.Q.ListAgentAvatars(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	// 删掉 agents/foo.md：foo 成为遗留项（按名字排在末尾），可清除、不可重设
	e.deleteAgentFile(c, "foo.md")
	l = e.agentAvatars(c)
	assert.Equal(t, append(bundledKeys("reviewer"), "Bar:user", "reviewer:user", "foo:orphan"), agentKeys(l))
	orphan := agentByName(t, l, "foo")
	assert.Nil(t, orphan.Description)
	require.NotNil(t, orphan.AvatarImage)
	r = e.do("PUT", agentAvatarsPath, c, map[string]any{"name": "foo", "avatarIcon": "🤖"})
	assert.Equal(t, 400, r.status, "遗留项不可重设")
	for range 2 {
		r = e.do("PUT", agentAvatarsPath, c, map[string]any{"name": "foo"})
		require.Equal(t, 200, r.status, string(r.body))
		assert.JSONEq(t, `{"name":"foo","source":"orphan"}`, string(r.body))
	}
	assert.NotContains(t, agentKeys(e.agentAvatars(c)), "foo:orphan")

	// 清除内置 agent 的头像：仍是内置项，头像字段省略
	r = e.do("PUT", agentAvatarsPath, c, map[string]any{"name": "scout"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"name":"scout","source":"bundled"}`, string(r.body))
}
