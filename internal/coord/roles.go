package coord

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// ModelInfo 是 list_models 返回给协调者的可用模型摘要。
type ModelInfo struct {
	Provider        string   `json:"provider"`
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	Reasoning       bool     `json:"reasoning,omitempty"`
	Thinking        []string `json:"thinking,omitempty"`
	ThinkingDefault string   `json:"thinkingDefault,omitempty"`
	ContextWindow   int      `json:"contextWindow,omitempty"`
}

// ModelCatalog 提供中央模型目录与内置子代理定义（由 server.go 经 *modelcat.Catalog 适配）。
type ModelCatalog interface {
	Models(ctx context.Context) ([]ModelInfo, error)
	BundledAgents(ctx context.Context) ([]agentcfg.AgentSpec, error)
}

// SetModelCatalog 设置中央模型目录与内置子代理来源；须在服务对外可用之前调用。
// 未设置时 list_models 返回 coordinator_unavailable，内置子代理回退到 agentcfg.DefaultBundledTaskAgents。
func (s *Service) SetModelCatalog(c ModelCatalog) { s.models = c }

// agentSpecsProvider 是 GlobalConfig 的可选扩展（*agentcfg.Home 实现）：读取 omp-home/agents/*.md 的完整定义。
type agentSpecsProvider interface {
	AgentSpecs() ([]agentcfg.AgentSpec, []agentcfg.AgentWarning, error)
}

// spaceRoleCatalog 是 Space 协调者可见的可用 agent role（subagent 配置）与排除集合快照。
type spaceRoleCatalog struct {
	specs           []agentcfg.AgentSpec
	byName          map[string]agentcfg.AgentSpec
	cycleRoles      map[string]bool
	disabled        map[string]bool
	knownModelRoles map[string]bool
}

func (c spaceRoleCatalog) namesHint() string {
	names := make([]string, 0, len(c.specs))
	for _, s := range c.specs {
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// loadSpaceRoles 汇集 Space 协调者可用的 agent role（内置 task subagents + 用户 agents/*.md），
// 应用 config.yml 的 task.disabledAgents 与 task.agentModelOverrides，并排除 cycleOrder 里的 cycle role。
func (s *Service) loadSpaceRoles(ctx context.Context) spaceRoleCatalog {
	cat := spaceRoleCatalog{
		byName:          make(map[string]agentcfg.AgentSpec),
		cycleRoles:      make(map[string]bool),
		disabled:        make(map[string]bool),
		knownModelRoles: make(map[string]bool),
	}

	var cfgBytes []byte
	if s.home != nil {
		if b, _, err := s.home.Get(agentcfg.ConfigFile); err == nil {
			cfgBytes = b
		} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, os.ErrNotExist) {
			// 读取失败时按缺省配置继续
			cfgBytes = nil
		}
	}
	cycleList, err := agentcfg.ParseCycleRoles(cfgBytes)
	if err != nil {
		cycleList, _ = agentcfg.ParseCycleRoles(nil)
	}
	for _, cr := range cycleList {
		if cr.Role != "" && cr.Role != "default" {
			cat.cycleRoles[cr.Role] = true
		}
	}
	knownList, err := agentcfg.ParseKnownRoles(cfgBytes)
	if err != nil {
		knownList, _ = agentcfg.ParseKnownRoles(nil)
	}
	for _, kr := range knownList {
		if kr.Role != "" {
			cat.knownModelRoles[kr.Role] = true
		}
	}
	taskCfg := agentcfg.ParseTaskSettings(cfgBytes)
	for _, name := range taskCfg.DisabledAgents {
		if name != "" {
			cat.disabled[name] = true
		}
	}

	merged := make(map[string]agentcfg.AgentSpec)
	var bundled []agentcfg.AgentSpec
	if s.models != nil {
		if got, berr := s.models.BundledAgents(ctx); berr == nil && len(got) > 0 {
			bundled = got
		}
	}
	if len(bundled) == 0 {
		bundled = agentcfg.DefaultBundledTaskAgents()
	}
	for _, sp := range bundled {
		if sp.Name != "" {
			merged[sp.Name] = sp
		}
	}
	if prov, ok := s.home.(agentSpecsProvider); ok {
		if userSpecs, _, uerr := prov.AgentSpecs(); uerr == nil {
			for _, sp := range userSpecs {
				if sp.Name != "" {
					merged[sp.Name] = sp
				}
			}
		}
	}

	cat.specs = make([]agentcfg.AgentSpec, 0, len(merged))
	for name, sp := range merged {
		if name == "default" || cat.disabled[name] || cat.cycleRoles[name] {
			continue
		}
		if ov := taskCfg.AgentModelOverrides[name]; len(ov) > 0 {
			sp.Model = slices.Clone(ov)
		}
		cat.byName[name] = sp
		cat.specs = append(cat.specs, sp)
	}
	slices.SortFunc(cat.specs, func(a, b agentcfg.AgentSpec) int { return strings.Compare(a.Name, b.Name) })
	return cat
}

// applySpaceCreateRole 把 Space 协调者 create_thread 的 role / model / thinkingLevel 映射到 threads.CreateParams：
//   - thinkingLevel 非空时先校验合法档位；
//   - 未指定 role（或 role=="default"）且未指定 model → 显式走 "default"；
//   - 指定了具体 subagent role（不含 cycle role 与被禁用的）→ 以该 subagent 的提示词、工具白名单、模型与思考档位启动普通工作线程；
//   - 显式传入的 model 与 thinkingLevel 优先级高于 subagent 自身的默认配置。
func (s *Service) applySpaceCreateRole(ctx context.Context, a createThreadArgs, p *threads.CreateParams) (string, error) {
	thinking := strings.TrimSpace(a.ThinkingLevel)
	if thinking != "" && !agentcfg.ValidThinkingLevel(thinking) {
		return "", errf(CodeInvalid, "invalid thinkingLevel %q (must be one of: off, minimal, low, medium, high, xhigh, max, auto)", thinking)
	}
	p.ThinkingLevel = thinking

	role := strings.TrimSpace(a.Role)
	if role == "" || role == "default" {
		if p.Model == nil {
			p.Role = "default"
			return "default", nil
		}
		return "", nil
	}

	cat := s.loadSpaceRoles(ctx)
	if cat.cycleRoles[role] {
		return "", errf(CodeInvalid, "role %q is a cycle role and cannot be used here; omit role (or pass \"default\") or choose an agent role: %s", role, cat.namesHint())
	}
	if cat.disabled[role] {
		return "", errf(CodeInvalid, "agent role %q is disabled", role)
	}
	spec, ok := cat.byName[role]
	if !ok {
		return "", errf(CodeInvalid, "unknown agent role %q; omit role (or pass \"default\") or choose one of: %s", role, cat.namesHint())
	}

	p.AgentRole = spec.Name
	p.AppendSystemPrompt = buildRoleAppendPrompt(spec)
	if spec.Tools != nil {
		p.AllowedTools = slices.Clone(spec.Tools)
		if p.AllowedTools == nil {
			p.AllowedTools = []string{}
		}
	}
	modelRole, modelRef, effThinking := resolveRoleModelAndThinking(spec, p.Model, thinking, cat.knownModelRoles)
	p.Role = modelRole
	p.Model = modelRef
	p.ThinkingLevel = effThinking
	return spec.Name, nil
}

// resolveRoleModelAndThinking 结合显式覆盖与 subagent 配置确定工作线程的开场模型、模型角色与思考档位。
// 优先级：显式 model > agentModelOverrides / frontmatter model > "default"；
// 思考档位：显式 thinkingLevel > frontmatter thinkingLevel > model 选择器的 `:level` 后缀。
func resolveRoleModelAndThinking(spec agentcfg.AgentSpec, explicitModel *apigen.ModelRef, explicitThinking string, knownModelRoles map[string]bool) (modelRole string, modelRef *apigen.ModelRef, thinking string) {
	if explicitModel != nil {
		return "", explicitModel, firstValidThinking(explicitThinking, spec.ThinkingLevel)
	}
	for _, raw := range spec.Model {
		for part := range strings.SplitSeq(raw, ",") {
			sel := strings.TrimSpace(part)
			if sel == "" {
				continue
			}
			r, m, lvl, ok := parseRoleModelSelector(sel, knownModelRoles)
			if !ok {
				continue
			}
			return r, m, firstValidThinking(explicitThinking, spec.ThinkingLevel, lvl)
		}
	}
	return "default", nil, firstValidThinking(explicitThinking, spec.ThinkingLevel)
}

// parseRoleModelSelector 解析 subagent 的单条 model 选择器（`@role`、`pi/role`、`*`、已知 role 名或 `provider/id`，
// 均可带可选的 `:thinkingLevel` 后缀）。
func parseRoleModelSelector(sel string, knownModelRoles map[string]bool) (role string, model *apigen.ModelRef, level string, ok bool) {
	base, lvl := splitThinkingSuffix(sel)
	if base == "*" {
		return "default", nil, lvl, true
	}
	if strings.HasPrefix(base, "@") {
		if r := strings.TrimSpace(strings.TrimPrefix(base, "@")); r != "" {
			return r, nil, lvl, true
		}
		return "", nil, "", false
	}
	if strings.HasPrefix(base, "pi/") {
		if r := strings.TrimSpace(strings.TrimPrefix(base, "pi/")); r != "" {
			return r, nil, lvl, true
		}
		return "", nil, "", false
	}
	if m, valid := parseModelRef(base); valid {
		return "", m, lvl, true
	}
	if knownModelRoles[base] {
		return base, nil, lvl, true
	}
	return "", nil, "", false
}

// splitThinkingSuffix 仅在末尾 `:suffix` 属于合法 omp 思考档位时拆分（不误拆 openrouter `:exacto` 等后缀）。
func splitThinkingSuffix(sel string) (base, level string) {
	i := strings.LastIndexByte(sel, ':')
	if i <= 0 {
		return sel, ""
	}
	suffix := strings.TrimSpace(sel[i+1:])
	if agentcfg.ValidThinkingLevel(suffix) {
		return strings.TrimSpace(sel[:i]), suffix
	}
	return sel, ""
}

func firstValidThinking(candidates ...string) string {
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c != "" && agentcfg.ValidThinkingLevel(c) {
			return c
		}
	}
	return ""
}

// buildRoleAppendPrompt 生成按 subagent 配置启动普通工作线程时追加到系统提示词末尾的段落。
func buildRoleAppendPrompt(spec agentcfg.AgentSpec) string {
	var b strings.Builder
	b.WriteString("# Agent Role: ")
	b.WriteString(spec.Name)
	if desc := cleanOneLine(spec.Description, 300); desc != "" {
		b.WriteString(" — ")
		b.WriteString(desc)
	}
	b.WriteString("\n\nYou are running as a standalone Oryn work thread configured with the \"")
	b.WriteString(spec.Name)
	b.WriteString("\" role (not as a nested subagent). Respond directly in this thread rather than calling `yield`.\n")
	if body := strings.TrimSpace(spec.SystemPrompt); body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}

// formatSpaceRolesPrompt 构造注入到 Space 协调者系统提示词末尾的可用 agent role 说明段。
func formatSpaceRolesPrompt(specs []agentcfg.AgentSpec) string {
	var b strings.Builder
	b.WriteString("Agent roles for create_thread:\n")
	b.WriteString("- Most threads you dispatch should use `default` (omit `role` or pass `role: \"default\"`). Only specify a specialist agent role when the task clearly matches its specialty.\n")
	b.WriteString("- Passing an agent role starts a standalone work thread with that subagent's prompt, tool set, and default model (not as a nested subagent). Cycle roles are not allowed.\n")
	b.WriteString("- If you omit both `role` and `model`, the thread uses `default`. You can also pass `model` (as `provider/id`; call `list_models` to inspect available models) and/or `thinkingLevel` (`off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `auto`) to override the role or default settings.\n")
	b.WriteString("- Available roles:\n")
	b.WriteString("  - `default`: Standard full-capability work thread (use this for most tasks).")
	for _, sp := range specs {
		b.WriteString("\n  - `")
		b.WriteString(sp.Name)
		b.WriteString("`")
		if desc := cleanOneLine(sp.Description, 200); desc != "" {
			b.WriteString(": ")
			b.WriteString(desc)
		}
	}
	return b.String()
}

func cleanOneLine(s string, maxRunes int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return truncRunes(s, maxRunes)
}

// ---- list_models ----

type listModelsArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

const (
	defaultListModelsLimit = 50
	maxListModelsLimit     = 200
)

func (s *Service) toolListModels(ctx context.Context, c toolCtx) (any, error) {
	var a listModelsArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	if s.models == nil {
		return nil, errf(CodeCoordinatorUnavailable, "model catalog is not available")
	}
	all, err := s.models.Models(ctx)
	if err != nil {
		return nil, errf(CodeCoordinatorUnavailable, "model catalog failed: %s", err.Error())
	}
	q := strings.ToLower(strings.TrimSpace(a.Query))
	filtered := make([]ModelInfo, 0, len(all))
	for _, m := range all {
		if q != "" {
			selector := strings.ToLower(m.Provider + "/" + m.ID)
			name := strings.ToLower(m.Name)
			if !strings.Contains(selector, q) && !strings.Contains(name, q) {
				continue
			}
		}
		filtered = append(filtered, m)
	}
	limit := a.Limit
	if limit <= 0 {
		limit = defaultListModelsLimit
	}
	limit = min(limit, maxListModelsLimit)
	total := len(filtered)
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return map[string]any{"models": filtered, "total": total}, nil
}
