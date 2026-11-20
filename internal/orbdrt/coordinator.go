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
// Space 协调者（D10）生图保持关闭。
func coordinatorOverlay(web bool) string {
	return `autolearn: { enabled: false }
memory: { backend: "off" }
checkpoint: { enabled: false }
compaction: { experimentalContextManagement: false }
goal: { enabled: false }
plan: { enabled: false }
generate_image: { enabled: ` + strconv.FormatBool(web) + ` }
lsp: { enabled: false }
mcp: { enableProjectConfig: false }
marketplace: { autoUpdate: "off" }
startup: { checkUpdate: false }
bash: { direnv: "off" }
`
}

// 协调档 omp 开的内置工具（--tools）：Space 协调者只有 read / write（M13）；全局助手（web）另有 web_search 与 generate_image。
const (
	coordinatorTools    = "read,write"
	coordinatorWebTools = "read,write,web_search,generate_image"
)

// coordinatorMethods 是协调宿主接受的 ctl 方法；其余一律 method_not_found。
var coordinatorMethods = map[string]bool{
	"setup.run": true, "resume.run": true, "agent.start": true, "agent.stop": true, "shutdown": true,
	string(apigen.MethodOmpHomeApply): true,
	string(apigen.MethodLibrarySync):  true, string(apigen.MethodLibraryFetch): true,
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
func (r *Runtime) coordinatorAgentSpec(p apigen.LinkCtlAgentStartParams, dir string) (args []string, cwd string, env map[string]string, err error) {
	if p.Profile == nil || *p.Profile != apigen.AgentProfileCoordinator {
		return nil, "", nil, ctlErr(link.CodeInvalidParams, "the coordinator host only starts agents with profile %q", apigen.AgentProfileCoordinator)
	}
	if p.SystemPrompt == nil || *p.SystemPrompt == "" || !strings.Contains(*p.SystemPrompt, "\n") {
		return nil, "", nil, ctlErr(link.CodeInvalidParams, "systemPrompt must be a non-empty multi-line prompt")
	}
	if len(p.Env) > 0 {
		return nil, "", nil, ctlErr(link.CodeInvalidParams, "env is not accepted by the coordinator host")
	}
	base := r.coordinatorDir()
	cwd = filepath.Join(base, "cwd")
	tmp := filepath.Join(r.home, "tmp")
	prompts := filepath.Join(base, "prompts")
	for _, d := range []string{cwd, tmp, prompts} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, "", nil, ctlErr("failed", "%v", err)
		}
	}
	// 两种覆盖层分文件写：全局助手与 Space 协调者的 agent 可能同时启动
	web := p.Web != nil && *p.Web
	overlay, tools := filepath.Join(base, "overlay.yml"), coordinatorTools
	if web {
		overlay, tools = filepath.Join(base, "overlay-web.yml"), coordinatorWebTools
	}
	if err := writeFileAtomic(overlay, []byte(coordinatorOverlay(web)), 0o600); err != nil {
		return nil, "", nil, ctlErr("failed", "write overlay: %v", err)
	}
	// 系统提示词写成文件、argv 只带路径（M9 L4）：同 uid 的进程与 /proc/<pid>/cmdline 看不到正文。omp 对不含换行的
	// --system-prompt 按文件读（resolvePromptInput）；路径不含换行（threadId 已校验）。
	promptFile := filepath.Join(prompts, p.ThreadID+".md")
	if err := writeFileAtomic(promptFile, []byte(*p.SystemPrompt), 0o600); err != nil {
		return nil, "", nil, ctlErr("failed", "write system prompt: %v", err)
	}
	// 内置工具（M13 起 read / write：读文件库、写 ~/library/out/；M19 第一层给全局助手加 web_search 与 generate_image，
	// read 也能读 URL）：--no-tools 先关掉全部，--tools 再精确列出。MCP 与宿主工具不受 --tools 限制。
	args = []string{
		"--mode", "rpc-ui", "--cwd", cwd, "--session-dir", dir,
		"--no-tools", "--tools", tools, "--no-skills", "--no-extensions", "--no-rules", "--no-context-files", "--no-lsp",
		"--config", overlay, "--system-prompt", promptFile,
	}
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
		"ORYN_THREAD_ID":    p.ThreadID,
	}
	// 出站（M19，只给全局助手）：omp 的全局 fetch 经 PI_PROXY 走 orynd 的正向代理（https 用 CONNECT），回环与内网地址
	// 由 omp 直连（沙箱里只有回环 7811 连得通）；是否放行、放行哪些目标由控制面的出站策略决定。MCP 的地址前缀与凭据
	// 也只给全局助手（mcp.json 里是 ${ORYN_MCP_BASE}/mcp/m<i> 与 X-Oryn-MCP-Token: ${ORYN_MCP_TOKEN}，出站代理核对凭据）。
	// Space 协调者都没有：连不到公网、用不了 MCP（D10）。
	if web && r.egressLn != nil {
		base := "http://" + r.egressLn.Addr().String()
		env["PI_PROXY"] = base
		env[agentcfg.MCPBaseEnv] = base
		if p.McpToken != nil && *p.McpToken != "" {
			env[agentcfg.MCPTokenEnv] = *p.McpToken
		}
	}
	return args, cwd, env, nil
}
