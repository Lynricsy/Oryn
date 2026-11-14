package orbdrt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// M9 协调宿主模式（协调宿主分册 H3）：`orynd coordinator-host` 在沙箱里以 Config.Coordinator 运行本包。
// 与普通 orb 的区别全部集中在这里的判定与专用实现：link 走继承来的 socketpair（不重连）、
// 只服务五个 ctl 方法、setup 只落 omp 文本配置、agent 以无工具的协调档启动且环境从零构造。

// ErrNoRedial 由 Config.Connect 返回，表示 link 不能重建：linkLoop 随即返回，Run 结束。
var ErrNoRedial = errors.New("orbdrt: link cannot be redialed")

// coordinatorOverlay 是协调档 omp 的设置覆盖层（--config）：强制关闭会读写文件、联网或执行代码的功能。
const coordinatorOverlay = `autolearn: { enabled: false }
memory: { backend: "off" }
checkpoint: { enabled: false }
compaction: { experimentalContextManagement: false }
goal: { enabled: false }
plan: { enabled: false }
generate_image: { enabled: false }
lsp: { enabled: false }
mcp: { enableProjectConfig: false }
marketplace: { autoUpdate: "off" }
startup: { checkUpdate: false }
bash: { direnv: "off" }
`

// coordinatorMethods 是协调宿主接受的 ctl 方法；其余一律 method_not_found。
var coordinatorMethods = map[string]bool{
	"setup.run": true, "resume.run": true, "agent.start": true, "agent.stop": true, "shutdown": true,
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
	if err := r.writeOmpHome(p.OmpHome); err != nil {
		return apigen.LinkCtlSetupRunResult{}, ctlErr("failed", "omp home: %v", err)
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
	overlay := filepath.Join(base, "overlay.yml")
	if err := writeFileAtomic(overlay, []byte(coordinatorOverlay), 0o600); err != nil {
		return nil, "", nil, ctlErr("failed", "write overlay: %v", err)
	}
	// 系统提示词写成文件、argv 只带路径（M9 L4）：同 uid 的进程与 /proc/<pid>/cmdline 看不到正文。omp 对不含换行的
	// --system-prompt 按文件读（resolvePromptInput）；路径不含换行（threadId 已校验）。
	promptFile := filepath.Join(prompts, p.ThreadID+".md")
	if err := writeFileAtomic(promptFile, []byte(*p.SystemPrompt), 0o600); err != nil {
		return nil, "", nil, ctlErr("failed", "write system prompt: %v", err)
	}
	args = []string{
		"--mode", "rpc-ui", "--cwd", cwd, "--session-dir", dir,
		"--no-tools", "--no-skills", "--no-extensions", "--no-rules", "--no-context-files", "--no-lsp",
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
		// omp 的原生文件锁改用 home 里的旁路文件（OFD 锁）：沙箱禁止一切 AF_UNIX，默认的 abstract socket 锁用不了。
		"PI_FILE_LOCK_BACKEND": "sidecar",
		"ORYN_THREAD_ID":       p.ThreadID,
	}
	return args, cwd, env, nil
}
