package agentcfg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M19 1.2：全局 mcp.json 的解析规则逐条。
func TestParseMCP(t *testing.T) {
	got, err := ParseMCP(nil, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	_, err = ParseMCP([]byte("{"), nil)
	require.Error(t, err)

	content := []byte(`{
	  "mcpServers": {
	    "zeta": {"type": "http", "url": "https://z.example.com/mcp", "headers": {"Authorization": "Bearer ${TOKEN}", "X-Team": "${TEAM:-core}"}},
	    "alpha": {"url": "https://a.example.com/mcp"},
	    "sse": {"type": "sse", "url": "http://10.0.0.5:9000/sse"},
	    "local": {"command": "npx", "args": ["x"]},
	    "off": {"type": "http", "url": "https://off.example.com", "enabled": false},
	    "hidden": {"type": "http", "url": "https://hidden.example.com"},
	    "oauthy": {"type": "http", "url": "https://o.example.com", "oauth": {"clientId": "x"}},
	    "authy": {"type": "http", "url": "https://o.example.com", "auth": {"type": "apikey"}},
	    "cmd": {"type": "http", "url": "https://c.example.com", "headers": {"Authorization": "!pass show x"}},
	    "unset": {"type": "http", "url": "https://u.example.com", "headers": {"Authorization": "Bearer ${MISSING}"}},
	    "badurl": {"type": "http", "url": "ftp://x"},
	    "nohost": {"type": "http", "url": "https:///path"}
	  },
	  "disabledServers": ["hidden"]
	}`)
	got, err = ParseMCP(content, map[string]string{"TOKEN": "s3cret"})
	require.NoError(t, err)
	byName := map[string]MCPServer{}
	var names []string
	for _, s := range got {
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"alpha", "authy", "badurl", "cmd", "local", "nohost", "oauthy", "sse", "unset", "zeta"}, names, "按名字排序，停用与 disabledServers 里的不列")

	z := byName["zeta"]
	assert.True(t, z.Usable)
	assert.Equal(t, MCPTransportHTTP, z.Transport)
	assert.Equal(t, map[string]string{"Authorization": "Bearer s3cret", "X-Team": "core"}, z.Headers, "环境变量与默认值展开")
	assert.Equal(t, MCPServer{Name: "alpha", Transport: MCPTransportHTTP, URL: "https://a.example.com/mcp", Headers: map[string]string{}, Usable: true}, byName["alpha"], "缺省 type 但有 url 按 http")
	assert.True(t, byName["sse"].Usable)
	assert.Equal(t, MCPTransportSSE, byName["sse"].Transport)

	for name, reason := range map[string]string{
		"local": MCPReasonStdio, "oauthy": MCPReasonOAuth, "authy": MCPReasonOAuth, "cmd": MCPReasonCommandHeader,
		"unset": MCPReasonUnresolvedEnv, "badurl": MCPReasonInvalidURL, "nohost": MCPReasonInvalidURL,
	} {
		s := byName[name]
		assert.False(t, s.Usable, name)
		assert.Equal(t, reason, s.Reason, name)
		assert.Nil(t, s.Headers, "不可用的服务器不带头："+name)
	}
	assert.Equal(t, MCPTransportStdio, byName["local"].Transport)
}
