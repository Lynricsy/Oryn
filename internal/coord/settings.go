package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/mcpprobe"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 全局助手的设置（M19）：联网总开关、关掉的 MCP 服务器、助手的电脑的 provider 与类型、默认模型与思考档位、自定义附加系统提示词。
// 联网或 MCP 变更后刷新协调宿主出站策略并让未归档助手对话在静止后重启；自定义系统提示词变更后让未归档助手对话在静止后重启并丢弃旧备用对话；
// 默认模型或思考档位变更后同步切换主线并丢弃旧备用对话，已有其他助手对话不变。

// 设置的上限。
const (
	maxDisabledMCP       = 200
	maxMCPNameRunes      = 200
	maxComputerString    = 200
	maxModelPart         = 200
	maxSystemPromptRunes = 8000
)

// GlobalConfig 是全局 omp 配置（*agentcfg.Home 实现）。
type GlobalConfig interface {
	Get(p string) ([]byte, agentcfg.FileInfo, error)
}

// EnvSecrets 提供 env 模式的密钥（*secrets.Vault 实现），用于展开 MCP 头里的变量。
type EnvSecrets interface {
	EnvSecrets() (map[string]string, error)
}

// SetAgentConfig 设置全局 omp 配置与 env 模式密钥；须在服务对外可用之前调用。任一为 nil 时相应能力不可用。
func (s *Service) SetAgentConfig(home GlobalConfig, env EnvSecrets) {
	s.home, s.envSecrets = home, env
}

// assistantSettings 是解析后的设置行；没有行时为默认值（联网开、无禁用项、模型与思考档位跟随默认、无自定义提示词）。
type assistantSettings struct {
	webAccess        bool
	disabled         []string
	provider, orbTyp *string
	model            *string
	thinkingLevel    *string
	systemPrompt     string
}

func (s *Service) loadSettings(ctx context.Context) (assistantSettings, error) {
	out := assistantSettings{webAccess: true, disabled: []string{}}
	row, err := s.st.Q.GetAssistantSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.webAccess = row.WebAccess == 1
	if err := json.Unmarshal([]byte(row.DisabledMcp), &out.disabled); err != nil || out.disabled == nil {
		out.disabled = []string{}
	}
	out.provider, out.orbTyp = row.ComputerProvider, row.ComputerOrbType
	out.model, out.thinkingLevel = row.Model, row.ThinkingLevel
	out.systemPrompt = row.SystemPrompt
	return out, nil
}

func (a assistantSettings) api() apigen.AssistantSettings {
	out := apigen.AssistantSettings{
		WebAccess: a.webAccess, DisabledMcpServers: a.disabled,
		Computer:      apigen.AssistantComputerSettings{Provider: a.provider, OrbType: a.orbTyp},
		ThinkingLevel: a.thinkingLevel,
	}
	if a.model != nil {
		if ref, ok := parseModelRef(*a.model); ok {
			out.Model = ref
		}
	}
	if a.systemPrompt != "" {
		p := a.systemPrompt
		out.SystemPrompt = &p
	}
	return out
}

// assistantDefaults 返回全局助手新对话与主线的默认模型（"provider/id" 或空）与思考档位（空 = omp 默认）。
// 模型优先级：全局助手设置 → orynd.yaml 的 coordinator.model → 空（config.yml 的 modelRoles.default）。
func (s *Service) assistantDefaults(ctx context.Context) (model, thinking string) {
	model = s.cfg.Model
	a, err := s.loadSettings(ctx)
	if err != nil {
		return model, ""
	}
	if a.model != nil && *a.model != "" {
		model = *a.model
	}
	if a.thinkingLevel != nil && *a.thinkingLevel != "" {
		thinking = *a.thinkingLevel
	}
	return model, thinking
}

// Settings 返回全局助手的设置。
func (s *Service) Settings(ctx context.Context) (apigen.AssistantSettings, error) {
	a, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.AssistantSettings{}, err
	}
	return a.api(), nil
}

// PutSettings 整体替换全局助手的设置。联网或 MCP 禁用表变更时刷新协调宿主出站策略并让助手对话静止后重启；
// 自定义提示词变更时让未归档助手对话静止后重启并丢弃旧备用对话；默认模型或思考档位变更时同步切换主线并丢弃旧备用对话，已有其他助手对话不变。
func (s *Service) PutSettings(ctx context.Context, in apigen.AssistantSettings) (apigen.AssistantSettings, error) {
	if len(in.DisabledMcpServers) > maxDisabledMCP {
		return apigen.AssistantSettings{}, errf(CodeInvalid, "at most %d disabled MCP servers", maxDisabledMCP)
	}
	disabled := make([]string, 0, len(in.DisabledMcpServers))
	seen := map[string]bool{}
	for _, n := range in.DisabledMcpServers {
		if n == "" || utf8.RuneCountInString(n) > maxMCPNameRunes {
			return apigen.AssistantSettings{}, errf(CodeInvalid, "MCP server names must be 1–%d characters", maxMCPNameRunes)
		}
		if !seen[n] {
			seen[n] = true
			disabled = append(disabled, n)
		}
	}
	norm := func(p *string) *string {
		if p == nil || *p == "" {
			return nil
		}
		return p
	}
	provider, orbType := norm(in.Computer.Provider), norm(in.Computer.OrbType)
	for _, v := range []*string{provider, orbType} {
		if v != nil && utf8.RuneCountInString(*v) > maxComputerString {
			return apigen.AssistantSettings{}, errf(CodeInvalid, "computer provider and type must be at most %d characters", maxComputerString)
		}
	}
	var model *string
	if in.Model != nil {
		p, id := strings.TrimSpace(in.Model.Provider), strings.TrimSpace(in.Model.ID)
		if p == "" || id == "" {
			return apigen.AssistantSettings{}, errf(CodeInvalid, "model must have provider and id")
		}
		if strings.Contains(p, "/") {
			return apigen.AssistantSettings{}, errf(CodeInvalid, "model provider must not contain '/'")
		}
		if utf8.RuneCountInString(p) > maxModelPart || utf8.RuneCountInString(id) > maxModelPart {
			return apigen.AssistantSettings{}, errf(CodeInvalid, "model provider and id must be at most %d characters", maxModelPart)
		}
		m := p + "/" + id
		model = &m
	}
	var thinking *string
	if in.ThinkingLevel != nil {
		if v := strings.TrimSpace(*in.ThinkingLevel); v != "" {
			if !agentcfg.ValidThinkingLevel(v) {
				return apigen.AssistantSettings{}, errf(CodeInvalid, "invalid thinkingLevel %q", v)
			}
			thinking = &v
		}
	}
	var systemPrompt string
	if in.SystemPrompt != nil {
		if v := strings.TrimSpace(*in.SystemPrompt); v != "" {
			if n := utf8.RuneCountInString(v); n > maxSystemPromptRunes {
				return apigen.AssistantSettings{}, errf(CodeInvalid, "system prompt has %d characters; the limit is %d", n, maxSystemPromptRunes)
			}
			systemPrompt = v
		}
	}
	prev, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.AssistantSettings{}, err
	}
	b, err := json.Marshal(disabled)
	if err != nil {
		return apigen.AssistantSettings{}, err
	}
	web := int64(0)
	if in.WebAccess {
		web = 1
	}
	if err := s.st.Q.UpsertAssistantSettings(ctx, db.UpsertAssistantSettingsParams{
		WebAccess: web, DisabledMcp: string(b), ComputerProvider: provider, ComputerOrbType: orbType,
		Model: model, ThinkingLevel: thinking, SystemPrompt: systemPrompt,
		UpdatedAt: s.cfg.Now().UnixMilli(),
	}); err != nil {
		return apigen.AssistantSettings{}, err
	}
	policyChanged := prev.webAccess != in.WebAccess || !slices.Equal(prev.disabled, disabled)
	modelChanged := !strPtrEqual(prev.model, model)
	thinkingChanged := !strPtrEqual(prev.thinkingLevel, thinking)
	promptChanged := prev.systemPrompt != systemPrompt
	willRestart := policyChanged || promptChanged
	if modelChanged || thinkingChanged {
		s.syncMainModel(ctx, modelChanged, model, thinkingChanged, thinking, willRestart)
	}
	if modelChanged || thinkingChanged || promptChanged {
		s.resetSpare(ctx)
	}
	if policyChanged {
		s.refreshAssistants(ctx)
	} else if promptChanged {
		s.refreshConversations(ctx)
	}
	return assistantSettings{
		webAccess: in.WebAccess, disabled: disabled, provider: provider, orbTyp: orbType,
		model: model, thinkingLevel: thinking, systemPrompt: systemPrompt,
	}.api(), nil
}

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// defaultThinkingLevel 返回全局 omp-home/config.yml 解析出的 defaultThinkingLevel（未写或读取失败为 omp 缺省值 high）。
func (s *Service) defaultThinkingLevel() string {
	var b []byte
	if s.home != nil {
		if raw, _, err := s.home.Get(agentcfg.ConfigFile); err == nil {
			b = raw
		}
	}
	return agentcfg.ParseDefaultThinkingLevel(b)
}

// syncMainModel 将全局助手默认模型或思考档位的变化同步到已存在的主线对话（已有其他助手对话不切换）。
// 恢复默认时显式传入解析出的默认值（coordinator.model 或 default role，以及 config.yml 的 defaultThinkingLevel），
// 避免有历史会话的主线在 open_session 时恢复磁盘里的旧模型与旧思考档位。
func (s *Service) syncMainModel(ctx context.Context, modelChanged bool, model *string, thinkingChanged bool, thinking *string, willRestart bool) {
	row, err := s.st.Q.GetMainThread(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("coord: 查询主线对话失败", "err", err)
		return
	}
	u := threads.CoordinatorModelUpdate{
		SetModel:    modelChanged,
		SetThinking: thinkingChanged,
		WillRestart: willRestart,
	}
	if modelChanged {
		if model != nil {
			u.Model = *model
		} else {
			u.Model = s.cfg.Model
		}
	}
	if thinkingChanged {
		if thinking != nil {
			u.Thinking = *thinking
		} else {
			u.Thinking = s.defaultThinkingLevel()
			u.ResetToDefaultThinking = true
		}
	}
	if err := s.th.SyncCoordinatorModel(ctx, row.ID, u); err != nil {
		slog.Warn("coord: 同步主线模型或思考档位失败", "thread", row.ID, "err", err)
	}
}

// refreshAssistants 立即以新设置刷新协调宿主的出站策略（联网开关当场生效），并让全部未归档的助手对话在静止后重启
// agent（工具表、系统提示词与下发的 mcp.json 随之更新）。
func (s *Service) refreshAssistants(ctx context.Context) {
	if o, err := s.st.Q.GetCoordinatorOrb(ctx); err == nil && s.refreshPolicy != nil {
		if err := s.refreshPolicy(ctx, o.ID); err != nil {
			slog.Warn("coord: 刷新协调宿主出站策略失败", "orb", o.ID, "err", err)
		}
	}
	s.refreshConversations(ctx)
}

// refreshConversations 让全部未归档的助手对话在静止后重启 agent（下次启动取新的系统提示词与工具表）。
func (s *Service) refreshConversations(ctx context.Context) {
	kind, archived := threads.KindAssistant, false
	rows, err := s.st.Q.ListThreads(ctx, db.ListThreadsParams{Kind: &kind, Archived: &archived})
	if err != nil {
		slog.Warn("coord: 列出助手对话失败", "err", err)
		return
	}
	for _, t := range rows {
		s.th.RefreshAgent(ctx, t.ID)
	}
}

// OnLanguageChanged 在全局语言设置变更后丢弃旧备用对话，并让所有未归档的助手与 Space 协调对话在静止后重启
// 以注入新的 ORYN_LANGUAGE 环境变量。
func (s *Service) OnLanguageChanged(ctx context.Context) {
	s.resetSpare(ctx)
	s.refreshConversations(ctx)
	kind, archived := threads.KindSpace, false
	rows, err := s.st.Q.ListThreads(ctx, db.ListThreadsParams{Kind: &kind, Archived: &archived})
	if err != nil {
		slog.Warn("coord: 列出 Space 协调对话失败", "err", err)
		return
	}
	for _, t := range rows {
		s.th.RefreshAgent(ctx, t.ID)
	}
}

// webAccess 报告全局助手联网是否开启（读设置失败按开启处理：真正的放行由出站代理按同一行决定）。
func (s *Service) webAccess(ctx context.Context) bool {
	a, err := s.loadSettings(ctx)
	return err != nil || a.webAccess
}

// assistantWebAndPrompt 返回全局助手的联网开关与自定义附加系统提示词（读设置失败按默认：联网开、无自定义提示词）。
func (s *Service) assistantWebAndPrompt(ctx context.Context) (web bool, customPrompt string) {
	a, err := s.loadSettings(ctx)
	if err != nil {
		return true, ""
	}
	return a.webAccess, a.systemPrompt
}

// WebAccess 报告全局助手联网是否开启（M26：后台研究在联网关着时不发起公网运行）。
func (s *Service) WebAccess(ctx context.Context) bool { return s.webAccess(ctx) }

// errMCPUnparsable 表示全局 mcp.json 不是合法 JSON。
var errMCPUnparsable = errors.New("global mcp.json cannot be parsed")

// globalMCP 读取并解析全局 mcp.json（头里的变量已用 env 密钥展开）。没有配置时返回空表；解析失败返回包了
// errMCPUnparsable 的错误。
func (s *Service) globalMCP() ([]agentcfg.MCPServer, error) {
	if s.home == nil {
		return nil, nil
	}
	content, _, err := s.home.Get(agentcfg.MCPFile)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var env map[string]string
	if s.envSecrets != nil {
		if env, err = s.envSecrets.EnvSecrets(); err != nil {
			return nil, err
		}
	}
	servers, err := agentcfg.ParseMCP(content, env)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errMCPUnparsable, err)
	}
	return servers, nil
}

// MCPServers 列出全局 mcp.json 里的服务器及其对助手是否可用、是否开启。解析失败返回空表。
func (s *Service) MCPServers(ctx context.Context) ([]apigen.AssistantMcpServer, error) {
	out := []apigen.AssistantMcpServer{}
	servers, err := s.globalMCP()
	if errors.Is(err, errMCPUnparsable) {
		slog.Warn("coord: 全局 mcp.json 无法解析", "err", err)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	settings, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	disabled := map[string]bool{}
	for _, n := range settings.disabled {
		disabled[n] = true
	}
	for _, srv := range servers {
		item := apigen.AssistantMcpServer{
			Name: srv.Name, Transport: apigen.AssistantMcpServerTransport(srv.Transport), Usable: srv.Usable, Enabled: !disabled[srv.Name],
		}
		if srv.Reason != "" {
			r := apigen.AssistantMcpServerReason(srv.Reason)
			item.Reason = &r
		}
		if u, err := url.Parse(srv.URL); err == nil && u.Host != "" {
			h := u.Hostname()
			item.Host = &h
		}
		out = append(out, item)
	}
	return out, nil
}

// MCP 工具列表（M26）的错误码与时限。
const (
	// CodeUnsupportedTransport：服务器是 SSE 传输，orynd 只会以 Streamable HTTP 列出工具。
	CodeUnsupportedTransport = "unsupported_transport"
	// CodeUpstreamFailed：连接 MCP 服务器或它的应答出错。
	CodeUpstreamFailed = "upstream_failed"
	// mcpProbeTimeout 是列出一个服务器工具的整体时限。
	mcpProbeTimeout = 20 * time.Second
)

// MCPServerTools 以 Streamable HTTP 连上全局 mcp.json 里名为 name 的服务器（带它的鉴权头），列出它的工具（M26：主人
// 勾选后台研究可用的只读工具时用）。readOnly 是服务器自报的，不是保证。找不到 → not_found；不可用 → invalid_request；
// SSE → unsupported_transport；上游失败 → upstream_failed（消息不含请求地址与任何请求头的值）。
func (s *Service) MCPServerTools(ctx context.Context, name string) ([]apigen.AssistantMcpTool, error) {
	servers, err := s.globalMCP()
	if errors.Is(err, errMCPUnparsable) {
		return nil, errf(CodeInvalid, "the global mcp.json cannot be parsed")
	}
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(servers, func(srv agentcfg.MCPServer) bool { return srv.Name == name })
	if i < 0 {
		return nil, errf(CodeNotFound, "no MCP server named %q", name)
	}
	srv := servers[i]
	if !srv.Usable {
		return nil, errf(CodeInvalid, "MCP server %q is not usable by the assistant (%s)", name, srv.Reason)
	}
	if srv.Transport == "sse" {
		return nil, errf(CodeUnsupportedTransport, "MCP server %q uses the SSE transport; only Streamable HTTP servers can be listed", name)
	}
	ctx, cancel := context.WithTimeout(ctx, mcpProbeTimeout)
	defer cancel()
	tools, err := mcpprobe.ListTools(ctx, &http.Client{Timeout: mcpProbeTimeout}, srv.URL, srv.Headers)
	if err != nil {
		slog.Warn("coord: 列出 MCP 服务器的工具失败", "server", name, "err", err)
		return nil, errf(CodeUpstreamFailed, "listing the tools of MCP server %q failed: %v", name, err)
	}
	out := make([]apigen.AssistantMcpTool, 0, len(tools))
	for _, t := range tools {
		item := apigen.AssistantMcpTool{Name: t.Name, ReadOnly: t.ReadOnly}
		if t.Description != "" {
			d := t.Description
			item.Description = &d
		}
		out = append(out, item)
	}
	return out, nil
}
