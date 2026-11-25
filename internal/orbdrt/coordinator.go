package orbdrt

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// M9 协调宿主模式（协调宿主分册 H3）：`orynd coordinator-host` 在沙箱里以 Config.Coordinator 运行本包。
// 与普通 orb 的区别全部集中在这里的判定与专用实现：link 走继承来的 socketpair（不重连）、
// 只服务白名单里的 ctl 方法、setup 只落 omp 文本配置、agent 以协调档启动（内置工具只有 read / write，M13）且环境从零构造。
// M13 起另接受文件库的 library.sync / library.fetch 与同步用的 library 流（见 library.go）。

// ErrNoRedial 由 Config.Connect 返回，表示 link 不能重建：linkLoop 随即返回，Run 结束。
var ErrNoRedial = errors.New("orbdrt: link cannot be redialed")

// coordinatorOverlay 是协调档 omp 的设置覆盖层（--config）：强制关闭会读写工作区文件或执行代码的功能。
// web（M19 第一层，只给全局助手）时生图开着：结果写进 TMPDIR（~/tmp/），出站与搜索一样经 orynd 的代理；
// Space 协调者（D10）生图保持关闭。web 时另强制开启技能：主人的 config.yml 也会下发到协调宿主，不能让它关掉内置技能。
// mcpExtra 追加在 mcp 映射里（YAML 不能出现两个 mcp 键）。
func coordinatorOverlay(web bool, mcpExtra string) string {
	mcp := "enableProjectConfig: false"
	if mcpExtra != "" {
		mcp += ", " + mcpExtra
	}
	overlay := `autolearn: { enabled: false }
memory: { backend: "off" }
checkpoint: { enabled: false }
compaction: { experimentalContextManagement: false }
goal: { enabled: false }
plan: { enabled: false }
generate_image: { enabled: ` + strconv.FormatBool(web) + ` }
lsp: { enabled: false }
mcp: { ` + mcp + ` }
marketplace: { autoUpdate: "off" }
startup: { checkUpdate: false }
bash: { direnv: "off" }
`
	if web {
		overlay += "skills: { enabled: true, enablePiUser: true, ignoredSkills: [], customDirectories: [] }\n"
	}
	return overlay
}

// coordinatorSkill 是全局助手加载的内置技能（与 coord.SkillName 一致；文件由控制面经 omp-home 下发）。
const coordinatorSkill = "oryn"

// 协调档 omp 开的内置工具（--tools）：Space 协调者只有 read / write（M13）；全局助手（web）另有 web_search 与 generate_image。
const (
	coordinatorTools    = "read,write"
	coordinatorWebTools = "read,write,web_search,generate_image"
)

// coordinatorMode 是协调档一种启动方式的工具、覆盖层与出站环境。
type coordinatorMode struct {
	tools        string // --tools 的值；空 = 不带 --tools（只有 --no-tools）
	overlayName  string // 覆盖层文件名（在协调目录下）；内容不同的覆盖层分文件写，并发启动的 agent 互不覆盖
	overlayExtra string // 追加在 coordinatorOverlay(web) 之后的覆盖层行
	mcpExtra     string // 追加在覆盖层 mcp 映射里的设置
	web          bool   // 全局助手的第一层（M19）：生图与技能打开、加载内置技能 oryn
	proxy        bool   // 设 PI_PROXY（出站经 orynd 的正向代理）
	mcp          bool   // 设 ORYN_MCP_BASE / ORYN_MCP_TOKEN
}

var (
	// 省略 variant 时的两种（与 M26 之前逐字相同）：Space 协调者与全局助手。
	coordinatorSpaceMode = coordinatorMode{tools: coordinatorTools, overlayName: "overlay.yml"}
	coordinatorWebMode   = coordinatorMode{tools: coordinatorWebTools, overlayName: "overlay-web.yml", web: true, proxy: true, mcp: true}
	// coordinatorVariants 是 M26 的三种启动变体，都不加载技能：
	// 公网研究员只能搜索与读公网网址（read.remoteOnly），没有 MCP 凭据；私有研究员没有任何内置工具、连不到公网，
	// 只在控制面给了凭据时能用 MCP；受限的主线只有 read，没有出站与 MCP。两个研究员都关掉提示词里 @路径 的自动读取。
	coordinatorVariants = map[apigen.LinkCtlAgentStartParamsVariant]coordinatorMode{
		apigen.AgentVariantResearchWeb: {
			tools: "web_search,read", overlayName: "overlay-research-web.yml",
			overlayExtra: "read: { remoteOnly: true }\nprompt: { fileMentions: false }\n", proxy: true,
		},
		apigen.AgentVariantResearchPrivate: {
			overlayName: "overlay-research-private.yml", overlayExtra: "prompt: { fileMentions: false }\n", mcp: true,
			mcpExtra: researchPrivateMCP,
		},
		apigen.AgentVariantMainRestricted: {tools: "read", overlayName: "overlay.yml"},
	}
)

// researchPrivateMCP 让私有研究员的第一个回合尽量赶上 MCP 发现：每次运行都是冷启动的新 agent，提示词随即投递，而 Core
// 的首回合最多等发现 1.5 秒。共享模式（缺省）要先等 omp daemon 的套接字（沙箱里没有，超时 10 秒才退回进程内连接），
// 发现必然赶不上；startupTimeoutMs: 0 让发现等各服务器连接落定，而不是 250 毫秒后先交出空工具表。上游握手本身超过
// 1.5 秒时这一回合仍拿不到 MCP 工具（Core 的固定上限）。
const researchPrivateMCP = "shared: false, startupTimeoutMs: 0"

// coordinatorMethods 是协调宿主接受的 ctl 方法；其余一律 method_not_found。
// 包含 blob.fetch：全局助手 / Space 协调者会话里的用户上传图片、生图与工具截图存在 ~/.omp/agent/blobs/<sha256>。
var coordinatorMethods = map[string]bool{
	"setup.run": true, "resume.run": true, "agent.start": true, "agent.stop": true, "shutdown": true,
	string(apigen.MethodOmpHomeApply): true,
	string(apigen.MethodLibrarySync):  true, string(apigen.MethodLibraryFetch): true,
	string(apigen.MethodBlobFetch): true,
}

func (r *Runtime) coordinatorDir() string { return filepath.Join(r.home, ".oryn", "coordinator") }

// coordinatorSetup 是协调宿主的 setup.run / resume.run：只写 omp 文本配置（含清理已移除的文件），
// 不 clone、不跑脚本、不配 git、不 spawn 任何子进程，也不发 setup.result。
func (r *Runtime) coordinatorSetup(p apigen.LinkCtlSetupRunParams) (apigen.LinkCtlSetupRunResult, error) {
	switch {
	case p.Repo != nil, nonEmpty(p.Scripts.Setup), nonEmpty(p.Scripts.Resume), len(p.GitHosts) > 0,
		p.GitIdentity != nil, len(p.Env) > 0:
		return apigen.LinkCtlSetupRunResult{}, ctlErr(link.CodeInvalidParams,
			"the coordinator host accepts only ompHome (no repo, scripts, git hosts, git identity or env)")
	}
	if _, _, err := r.writeOmpConfigAt(r.nextOmpSeq(), p.OmpHome, nil, false, nil); err != nil {
		return apigen.LinkCtlSetupRunResult{}, ctlErr("failed", "%v", err)
	}
	return apigen.LinkCtlSetupRunResult{Ok: true, SetupRan: false}, nil
}

// coordinatorAgentSpec 校验协调档的 agent.start 并构造启动参数与环境（环境从零构造，不继承 orbd 的环境）。
// variant 是本次使用的 M26 启动变体（省略时为空串），由监督器记下并在 agent.start 应答里报回。
func (r *Runtime) coordinatorAgentSpec(p apigen.LinkCtlAgentStartParams, dir string) (args []string, cwd string, env map[string]string, variant string, err error) {
	if p.Profile == nil || *p.Profile != apigen.AgentProfileCoordinator {
		return nil, "", nil, "", ctlErr(link.CodeInvalidParams, "the coordinator host only starts agents with profile %q", apigen.AgentProfileCoordinator)
	}
	if p.SystemPrompt == nil || *p.SystemPrompt == "" || !strings.Contains(*p.SystemPrompt, "\n") {
		return nil, "", nil, "", ctlErr(link.CodeInvalidParams, "systemPrompt must be a non-empty multi-line prompt")
	}
	if len(p.Env) > 0 {
		return nil, "", nil, "", ctlErr(link.CodeInvalidParams, "env is not accepted by the coordinator host")
	}
	web := p.Web != nil && *p.Web
	mode := coordinatorSpaceMode
	if web {
		mode = coordinatorWebMode
	}
	if p.Variant != nil && *p.Variant != "" {
		v, ok := coordinatorVariants[*p.Variant]
		if !ok {
			return nil, "", nil, "", ctlErr(link.CodeInvalidParams, "unknown variant %q", *p.Variant)
		}
		if web {
			return nil, "", nil, "", ctlErr(link.CodeInvalidParams, "variant and web are mutually exclusive")
		}
		mode, variant = v, string(*p.Variant)
	}
	base := r.coordinatorDir()
	cwd = filepath.Join(base, "cwd")
	tmp := filepath.Join(r.home, "tmp")
	prompts := filepath.Join(base, "prompts")
	for _, d := range []string{cwd, tmp, prompts} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, "", nil, "", ctlErr("failed", "%v", err)
		}
	}
	overlay := filepath.Join(base, mode.overlayName)
	if err := writeFileAtomic(overlay, []byte(coordinatorOverlay(mode.web, mode.mcpExtra)+mode.overlayExtra), 0o600); err != nil {
		return nil, "", nil, "", ctlErr("failed", "write overlay: %v", err)
	}
	// 系统提示词写成文件、argv 只带路径（M9 L4）：同 uid 的进程与 /proc/<pid>/cmdline 看不到正文。omp 对不含换行的
	// --system-prompt 按文件读（resolvePromptInput）；路径不含换行（threadId 已校验）。
	promptFile := filepath.Join(prompts, p.ThreadID+".md")
	if err := writeFileAtomic(promptFile, []byte(*p.SystemPrompt), 0o600); err != nil {
		return nil, "", nil, "", ctlErr("failed", "write system prompt: %v", err)
	}
	// 内置工具（M13 起 read / write：读文件库、写 ~/library/out/；M19 第一层给全局助手加 web_search 与 generate_image，
	// read 也能读 URL）：--no-tools 先关掉全部，--tools 再精确列出（私有研究员不带 --tools，一个内置工具都没有）。
	// MCP 与宿主工具不受 --tools 限制。
	// 技能：全局助手只加载内置的 oryn（--skills 是白名单），Space 协调者与 M26 的变体一个都不加载。
	skills := []string{"--no-skills"}
	if mode.web {
		skills = []string{"--skills", coordinatorSkill}
	}
	args = []string{"--mode", "rpc-ui", "--cwd", cwd, "--session-dir", dir, "--no-tools"}
	if mode.tools != "" {
		args = append(args, "--tools", mode.tools)
	}
	args = append(args, skills...)
	args = append(args, "--no-extensions", "--no-rules", "--no-context-files", "--no-lsp",
		"--config", overlay, "--system-prompt", promptFile)
	env = map[string]string{
		"HOME":              r.home,
		"PATH":              "/usr/bin:/bin",
		"LANG":              "C.UTF-8",
		"TMPDIR":            tmp,
		"PI_RPC_EMIT_TITLE": "1",
		"PI_NOTIFICATIONS":  "off",
		"OTEL_SDK_DISABLED": "true",
		// Core 19 起 RPC 默认不要求工具调用带 intent；Web 的工具卡要显示它
		"PI_INTENT_TRACING": "1",
		// Core oryn-v19.0.1-4 起：Web 自带 Symbols Nerd Font，标题卡片用 Nerd 字形而不是 emoji（rpc 宿主缺省只给 emoji）
		"ORYN_NERD_GLYPHS": "1",
		"ORYN_THREAD_ID":   p.ThreadID,
	}
	if mode.mcp {
		// Core 19.2.0 起 --no-tools 默认一并关闭 MCP；用环境变量显式放行（旧版 19.0.1 Core 静默忽略，不会因未知 CLI flag 退出）。
		env["ORYN_ALLOW_RESTRICTED_MCP"] = "1"
	}
	// 由 omp 直连（沙箱里只有回环 7811 连得通）；是否放行、放行哪些目标由控制面的出站策略决定。MCP 的地址前缀与凭据
	// 也只给全局助手（mcp.json 里是 ${ORYN_MCP_BASE}/mcp/m<i> 与 X-Oryn-MCP-Token: ${ORYN_MCP_TOKEN}，出站代理核对凭据）。
	// Space 协调者都没有：连不到公网、用不了 MCP（D10）。M26：公网研究员只有 PI_PROXY；私有研究员只在带了凭据时
	// 两个 MCP 变量一起设；受限的主线都没有。
	if r.egressLn != nil {
		addr := "http://" + r.egressLn.Addr().String()
		token := ""
		if p.McpToken != nil {
			token = *p.McpToken
		}
		if mode.proxy {
			env["PI_PROXY"] = addr
		}
		if mode.mcp && (mode.web || token != "") {
			env[agentcfg.MCPBaseEnv] = addr
		}
		if mode.mcp && token != "" {
			env[agentcfg.MCPTokenEnv] = token
		}
	}
	return args, cwd, env, variant, nil
}
