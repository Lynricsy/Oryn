package agentcfg

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// MCPFile 是 omp-home 里的 MCP 服务器配置文件。
const MCPFile = "mcp.json"

// MCPBaseEnv 是协调宿主 mcp.json 里服务器地址的前缀变量（M19，D10）：只有全局助手的 agent 环境里有它
// （值为出站代理地址 http://127.0.0.1:<port>），Space 协调者没有，因而连不上 MCP。
const MCPBaseEnv = "ORYN_MCP_BASE"

// MCP 传输方式。
const (
	MCPTransportHTTP  = "http"
	MCPTransportSSE   = "sse"
	MCPTransportStdio = "stdio"
)

// MCP 服务器对全局助手不可用的原因（M19）。
const (
	MCPReasonStdio         = "stdio"
	MCPReasonOAuth         = "oauth"
	MCPReasonCommandHeader = "command_header"
	MCPReasonUnresolvedEnv = "unresolved_env"
	MCPReasonInvalidURL    = "invalid_url"
)

// MCPServer 是全局 mcp.json 里一个启用的服务器。Usable 为真时 URL 与 Headers 可直接用于出站（头里的环境变量已展开）；
// 为假时 Reason 给出原因，Headers 为空。
type MCPServer struct {
	Name, Transport, URL string
	Headers              map[string]string
	Usable               bool
	Reason               string
}

// mcpEnvRe 匹配 ${VAR} 与 ${VAR:-默认}。
var mcpEnvRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// ParseMCP 解析全局 mcp.json（M19，全局助手的 HTTP MCP）：读顶层 mcpServers，跳过 enabled: false 与列在 disabledServers
// 里的条目；有 command 的是 stdio（不可用）；type 为 http / sse 取其值，缺省但有 url 按 http；有 auth / oauth 字段、头的值
// 以 ! 开头（命令生成）、头引用了 env 里没有且无默认的变量、url 不是带主机的 http(s) 地址时不可用。结果按名字排序。
// 内容为空返回空表；不是合法 JSON 返回错误。
func ParseMCP(content []byte, env map[string]string) ([]MCPServer, error) {
	if len(strings.TrimSpace(string(content))) == 0 {
		return []MCPServer{}, nil
	}
	var doc struct {
		MCPServers      map[string]json.RawMessage `json:"mcpServers"`
		DisabledServers []string                   `json:"disabledServers"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("mcp.json: %w", err)
	}
	disabled := map[string]bool{}
	for _, n := range doc.DisabledServers {
		disabled[n] = true
	}
	out := []MCPServer{}
	for name, raw := range doc.MCPServers {
		if disabled[name] {
			continue
		}
		var c struct {
			Enabled *bool             `json:"enabled"`
			Type    string            `json:"type"`
			Command string            `json:"command"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Auth    json.RawMessage   `json:"auth"`
			OAuth   json.RawMessage   `json:"oauth"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("mcp.json: server %q: %w", name, err)
		}
		if c.Enabled != nil && !*c.Enabled {
			continue
		}
		srv := MCPServer{Name: name}
		switch {
		case c.Command != "" || c.Type == MCPTransportStdio:
			srv.Transport, srv.Reason = MCPTransportStdio, MCPReasonStdio
		case c.Type == MCPTransportHTTP || c.Type == MCPTransportSSE:
			srv.Transport = c.Type
		case c.Type == "" && c.URL != "":
			srv.Transport = MCPTransportHTTP
		default:
			// 未知传输：omp 自己也不认，不列出
			continue
		}
		if srv.Reason == "" {
			srv.URL = c.URL
			srv.Reason = mcpUnusable(c.URL, c.Headers, len(c.Auth) > 0 && string(c.Auth) != "null" || len(c.OAuth) > 0 && string(c.OAuth) != "null", env)
		}
		if srv.Reason == "" {
			srv.Usable = true
			srv.Headers = expandMCPHeaders(c.Headers, env)
		}
		out = append(out, srv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// mcpUnusable 返回 HTTP / SSE 服务器不能交给全局助手的原因；可用时为空。
func mcpUnusable(raw string, headers map[string]string, oauth bool, env map[string]string) string {
	if oauth {
		return MCPReasonOAuth
	}
	for _, v := range headers {
		if strings.HasPrefix(v, "!") {
			return MCPReasonCommandHeader
		}
	}
	for _, v := range headers {
		for _, m := range mcpEnvRe.FindAllStringSubmatch(v, -1) {
			if _, ok := env[m[1]]; !ok && !strings.Contains(m[0], ":-") {
				return MCPReasonUnresolvedEnv
			}
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return MCPReasonInvalidURL
	}
	return ""
}

// expandMCPHeaders 展开头里的 ${VAR} 与 ${VAR:-默认}（调用方已确认变量都有值或默认）。
func expandMCPHeaders(headers map[string]string, env map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = mcpEnvRe.ReplaceAllStringFunc(v, func(s string) string {
			m := mcpEnvRe.FindStringSubmatch(s)
			if val, ok := env[m[1]]; ok {
				return val
			}
			return m[2]
		})
	}
	return out
}
