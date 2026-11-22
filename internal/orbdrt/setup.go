package orbdrt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/gitident"
	"github.com/Lynricsy/Oryn/internal/link"
)

const (
	setupTimeout = 20 * time.Minute
	// resumeTimeout 是 resume 脚本（含转入后台的部分）的总上限：resume 应在数秒内完成，
	// 卡死的 resume 不能一直占住 setup 互斥。
	resumeTimeout = 10 * time.Minute
	logChunk      = 16 << 10
)

func (r *Runtime) setupEnvSnapshot() map[string]string {
	r.envMu.Lock()
	defer r.envMu.Unlock()
	return maps.Clone(r.setupEnv)
}

// currentWorkdir 返回最近一次 setup / resume 的工作目录（缺省 $HOME/workspace）。
func (r *Runtime) currentWorkdir() string {
	r.envMu.Lock()
	defer r.envMu.Unlock()
	if r.workdir != "" {
		return r.workdir
	}
	return filepath.Join(r.home, "workspace")
}

// currentGitIdentity 返回最近一次下发的提交身份（M15）；从未下发时为 nil。
func (r *Runtime) currentGitIdentity() *apigen.GitIdentity {
	r.envMu.Lock()
	defer r.envMu.Unlock()
	if r.gitIdentity == nil {
		return nil
	}
	id := *r.gitIdentity
	return &id
}

// autoSetup 是一次进行中的自动 setup.run / resume.run。link 重连后控制面会重发同样的请求：
// 等价（同方法、同参数）的重试直接等这次运行的请求结果，而不是再跑一遍、也不排在转入后台的 resume 之后。
type autoSetup struct {
	key  [sha256.Size]byte
	done chan struct{} // 请求结果就绪（不含转入后台的 resume）
	res  apigen.LinkCtlSetupRunResult
	err  error
}

func (op *autoSetup) finished() bool {
	select {
	case <-op.done:
		return true
	default:
		return false
	}
}

func setupKey(p apigen.LinkCtlSetupRunParams, full bool) [sha256.Size]byte {
	b, _ := json.Marshal(p)
	if full {
		b = append(b, "|setup"...)
	} else {
		b = append(b, "|resume"...)
	}
	return sha256.Sum256(b)
}

// runSetup 实现 setup.run（full=true）与 resume.run（H3.3）。与手动重跑互斥：已有 setup / resume
// （含转入后台的 resume）运行时排队等待。每个阶段结束发一条 setup.result（trigger=auto）。
// 与进行中（或其 resume 仍在后台占着互斥）的自动运行等价的请求合并到那次运行上。
func (r *Runtime) runSetup(ctx context.Context, p apigen.LinkCtlSetupRunParams, full bool) (apigen.LinkCtlSetupRunResult, error) {
	ompSeq := r.nextOmpSeq() // 到达即编号，等互斥期间到达的 omphome.apply 排在它后面
	key := setupKey(p, full)
	r.autoMu.Lock()
	if op := r.autoOp; op != nil && op.key == key && (!op.finished() || r.semHolder == op) {
		r.autoMu.Unlock()
		r.log.Info("setup request repeated (link reconnected); joining the in-flight run", "full", full)
		select {
		case <-op.done:
			return op.res, op.err
		case <-ctx.Done():
			return apigen.LinkCtlSetupRunResult{}, ctlErr("failed", "%v", ctx.Err())
		}
	}
	op := &autoSetup{key: key, done: make(chan struct{})}
	r.autoOp = op
	r.autoMu.Unlock()
	defer close(op.done)
	if err := r.acquireSetup(ctx); err != nil {
		op.err = ctlErr("failed", "%v", err)
		return op.res, op.err
	}
	r.autoMu.Lock()
	r.semHolder = op
	r.autoMu.Unlock()
	op.res, op.err = r.runSetupLocked(ctx, p, full, ompSeq)
	return op.res, op.err
}

// runSetupLocked 是 runSetup 的主体；调用方已持有 setup 互斥。ompSeq 是请求到达时取得的 omp 配置写入编号。
func (r *Runtime) runSetupLocked(ctx context.Context, p apigen.LinkCtlSetupRunParams, full bool, ompSeq uint64) (apigen.LinkCtlSetupRunResult, error) {
	res := apigen.LinkCtlSetupRunResult{}
	// resume 交给后台时连同互斥一起移交，由它在脚本结束时释放。
	handedOff := false
	defer func() {
		if !handedOff {
			r.releaseSetup()
		}
	}()
	// M25：工作区已就绪（成功、失败都算；resume 交给后台时 checkout 也早已完成），桌面据此执行 resume 脚本。
	if r.desk != nil {
		defer func() { r.desk.workspaceReady(r.currentWorkdir()) }()
	}
	reqStart := time.Now()
	phase, phaseStart := apigen.SetupPhaseResume, reqStart
	if full {
		phase = apigen.SetupPhaseSetup
	}
	// prepared：本次开始时完成标记已在（快照或此前的 setup 留下的），M11a 2.3。
	_, statErr := os.Stat(r.setupMarker())
	prepared := statErr == nil
	res.Prepared = new(prepared)
	// 中途出错（omp 配置、git、clone）也上报当前阶段的失败结果，控制面据此留下这次运行与日志。
	fail := func(err *link.CtlError) (apigen.LinkCtlSetupRunResult, error) {
		fres := apigen.LinkCtlSetupResultParams{
			Phase: phase, Trigger: apigen.SetupTriggerAuto, Ok: false, Error: new(err.Message),
			StartedAt: phaseStart.UnixMilli(), DurationMs: time.Since(phaseStart).Milliseconds(),
		}
		if phase == apigen.SetupPhaseSetup {
			fres.Prepared = new(prepared)
		}
		r.publishResult(fres)
		return res, err
	}

	lg, closeLog, err := r.openLogger("setup.log", nil)
	if err != nil {
		return fail(ctlErr("failed", "%v", err))
	}
	defer closeLog()

	// 纵深防御：身份写进 ~/.gitconfig，含换行 / 控制字符的值可能注入额外配置项。先于任何写入校验。
	if id := p.GitIdentity; id != nil {
		if err := gitident.Validate(id.Name, id.Email); err != nil {
			return fail(ctlErr(link.CodeInvalidParams, "invalid gitIdentity: %v", err))
		}
	}

	workdir := p.Workdir
	switch {
	case workdir != "":
		workdir = r.resolvePath(workdir)
	case p.Repo != nil:
		workdir = filepath.Join(r.home, "workspace", "repo")
	default:
		workdir = filepath.Join(r.home, "workspace")
	}
	// M14：先写 / 删 gh 配置，本次脚本环境里的 GH_CONFIG_DIR 随之确定。
	if err := r.applyGh(p.Gh); err != nil {
		return fail(ctlErr("failed", "gh config: %v", err))
	}
	env := r.baseEnv()
	maps.Copy(env, p.Env)
	// 记住本次参数，供 orb 内 `oryn orb setup run` 重跑。
	r.envMu.Lock()
	r.setupEnv = maps.Clone(p.Env)
	r.workdir = workdir
	r.lastScripts = cloneScripts(p.Scripts)
	r.haveParams = true
	// 与写 ~/.gitconfig 的规则一致：缺省时沿用上一次的身份
	if id := p.GitIdentity; id != nil {
		cp := *id
		r.gitIdentity = &cp
	}
	r.envMu.Unlock()

	// 1. omp 文本配置：用户层 ~/.omp/agent，与项目层（M12.2，严格镜像到 ~/.oryn/project-config/.omp；不属于项目时删除整个目录）。
	wroteHome, wroteProject, err := r.writeOmpConfigAt(ompSeq, p.OmpHome, p.ProjectConfig, true, p.SSH)
	if err != nil {
		lg.system(apigen.OmpHome, "error: "+err.Error())
		return fail(ctlErr("failed", "%v", err))
	}
	const newer = "skipped: a newer omp config was applied while waiting"
	if wroteHome {
		lg.system(apigen.OmpHome, fmt.Sprintf("wrote %d file(s) to ~/.omp/agent", len(p.OmpHome)))
	} else {
		lg.system(apigen.OmpHome, newer+" (~/.omp/agent)")
	}
	switch {
	case !wroteProject:
		lg.system(apigen.OmpHome, newer+" (project config)")
	case p.ProjectConfig != nil:
		lg.system(apigen.OmpHome, fmt.Sprintf("wrote %d project config file(s) to ~/.oryn/project-config/.omp", len(p.ProjectConfig.Files)))
	}

	// 2. git：经本机 egress 代理访问 git（控制面注入令牌）。条目为 `host`（整个主机，无项目的 orb 只读）
	// 或 `host/owner/repo`（只覆盖项目仓库；同主机其它地址照常直连公网、不带令牌）。
	// insteadOf 只把 https 换成 http（`https://github.com/acme/widgets` → `http://github.com/acme/widgets`），
	// `http.http://<host>/.proxy` 再把这个主机的 http 请求以代理形式交给 egress：`git remote -v` 显示的仍是真实主机与
	// owner/repo，gh 才能从 remote 推断仓库（R5）。先清掉以前写过的全部条目，策略收窄（如 orb 改属项目）后旧条目不会残留。
	for _, h := range p.GitHosts {
		if !validGitEntry(h) {
			return fail(ctlErr(link.CodeInvalidParams, "invalid git host %q", h))
		}
	}
	if err := r.clearGitEgress(ctx, env); err != nil {
		return fail(ctlErr("failed", "git config cleanup: %v", err))
	}
	// NO_PROXY / no_proxy 覆盖的主机失败关闭：git 会绕过代理，把改写出来的 http 地址明文直连公网（中间人可替换 refs 与对象），
	// 所以这些主机不写改写也不写代理，remote 保持 https 直连（TLS、不带令牌）：公开仓库照常可读，私有仓库与推送明确失败。
	// 不改 NO_PROXY 本身，以免改变其它流量的代理语义。
	var hosts []string
	bypassed := map[string]bool{}
	for _, h := range p.GitHosts {
		host, _, _ := strings.Cut(h, "/")
		if slices.Contains(hosts, host) || bypassed[host] {
			continue
		}
		if noProxyCovers(host, env["NO_PROXY"], env["no_proxy"]) {
			bypassed[host] = true
			msg := fmt.Sprintf("warning: NO_PROXY/no_proxy covers %s, so git would bypass the Oryn egress proxy; "+
				"git for %s is not routed through egress (remotes stay https without credentials: public repos still fetch, "+
				"private repos and pushes fail). Remove %s from NO_PROXY and no_proxy to enable it.", host, host, host)
			lg.system(apigen.Git, msg)
			r.log.Warn("git host covered by NO_PROXY; not routing it through egress", "host", host)
			continue
		}
		hosts = append(hosts, host)
	}
	// 先写代理再写 insteadOf：任何时候改写出来的 http 地址都有代理接住。
	for _, host := range hosts {
		if err := lg.run(ctx, apigen.Git, r.home, envList(env), "git", "config", "--global", "http.http://"+host+"/.proxy", "http://"+r.EgressAddr()); err != nil {
			return fail(ctlErr("failed", "git proxy config for %s: %v", host, err))
		}
	}
	for _, h := range p.GitHosts {
		if host, _, _ := strings.Cut(h, "/"); bypassed[host] {
			continue
		}
		from, to := "https://"+h, "http://"+h
		if !strings.Contains(h, "/") {
			from, to = from+"/", to+"/"
		}
		if err := lg.run(ctx, apigen.Git, r.home, envList(env), "git", "config", "--global", "url."+to+".insteadOf", from); err != nil {
			return fail(ctlErr("failed", "git config for %s: %v", h, err))
		}
	}
	// git 提交身份（setup 与 resume 都写，已有 orb 唤醒时也能拿到新设置）；缺省时不动现有配置。
	if id := p.GitIdentity; id != nil {
		for _, kv := range [][2]string{{"user.name", id.Name}, {"user.email", id.Email}} {
			if err := lg.run(ctx, apigen.Git, r.home, envList(env), "git", "config", "--global", "--", kv[0], kv[1]); err != nil {
				return fail(ctlErr("failed", "git config %s: %v", kv[0], err))
			}
		}
	}
	// M14：仓库改名 / 转移后把检出的 origin 改到新地址（setup 与 resume 都走）。
	r.followOrigin(ctx, workdir, p.Repo)

	if full {
		// 3. clone：仓库存在且工作目录尚无检出。
		if err := os.MkdirAll(filepath.Dir(workdir), 0o755); err != nil {
			return fail(ctlErr("failed", "%v", err))
		}
		cloned := false
		if p.Repo != nil && p.Repo.URL != "" && !hasCheckout(workdir) {
			args := []string{"clone"}
			if p.Repo.Shallow {
				args = append(args, "--depth", "1")
			}
			if p.Repo.Branch != nil && *p.Repo.Branch != "" {
				args = append(args, "--branch", *p.Repo.Branch)
			}
			args = append(args, "--", p.Repo.URL, workdir)
			cctx, cancel := context.WithTimeout(ctx, setupTimeout)
			// 检出时不下载 LFS 对象（filter.lfs.required=true 时取不下对象会让 clone 以失败退出、停在半检出状态），
			// 下面统一 git lfs pull。
			err := lg.run(cctx, apigen.Clone, r.home, skipSmudge(env), "git", args...)
			cancel()
			if err != nil {
				return fail(ctlErr(errCode(err), "git clone: %v", err))
			}
			cloned = true
		}
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			return fail(ctlErr("failed", "%v", err))
		}
		// 3b. repo.update：已有检出（快照或池 orb）移动到基线最新提交；HEAD 变了就删掉完成标记，下面增量重跑 setup。
		repoUpdated, setupChanged := false, false
		if p.Repo != nil && p.Repo.Update != nil && *p.Repo.Update && !cloned && hasCheckout(workdir) {
			repoUpdated, setupChanged = r.updateRepo(ctx, lg, workdir, skipSmudge(env), p.Repo)
		}
		// 3c. Git LFS（M24）：检出之后取对象；失败并入 SetupError，但不阻止 setup 脚本运行。
		var lfsErr error
		if p.Repo != nil && p.Repo.URL != "" && hasCheckout(workdir) {
			lfsErr = r.lfsPull(ctx, lg, workdir, envList(env))
		}
		res.RepoUpdated, res.SetupChanged = new(repoUpdated), new(setupChanged)

		// 4. setup 脚本（合计 20 分钟），成功后写标记。
		sres := apigen.LinkCtlSetupResultParams{
			Phase: apigen.SetupPhaseSetup, Trigger: apigen.SetupTriggerAuto,
			StartedAt: phaseStart.UnixMilli(), HasProjectScript: nonEmpty(p.Scripts.Setup),
			HasRepoScript: repoScript(workdir, "setup") != "",
			Prepared:      new(prepared), RepoUpdated: new(repoUpdated), SetupChanged: new(setupChanged),
		}
		if _, err := os.Stat(r.setupMarker()); err != nil {
			res.SetupRan, sres.SetupRan = true, true
			// 脚本失败不让 orb 失败：agent 仍要启动，才能读日志并修复脚本。不写标记，下次 setup 重跑。
			if err := r.setupScripts(ctx, lg, workdir, env, p.Scripts.Setup); err != nil {
				res.SetupError = new(err.Error())
			}
		} else {
			lg.system(apigen.Setup, "setup already done; skipped")
		}
		if lfsErr != nil {
			msg := lfsErr.Error()
			if res.SetupError != nil {
				msg = *res.SetupError + "; " + msg
			}
			res.SetupError = new(msg)
		}
		sres.Ok, sres.Error = res.SetupError == nil, res.SetupError
		sres.DurationMs = time.Since(phaseStart).Milliseconds()
		if hasCheckout(workdir) {
			if c := r.gitRev(ctx, workdir, envList(env), "HEAD"); c != "" {
				res.BaseCommit, sres.BaseCommit = new(c), new(c)
			}
		}
		r.publishResult(sres)
		if p.DeferResume != nil && *p.DeferResume {
			// S-P1-2：控制面要在 resume 与受管服务之前拍快照，随后单独发 resume.run。互斥照常在返回时释放。
			lg.system(apigen.Resume, "resume and managed services deferred until resume.run")
			res.Ok = res.SetupError == nil
			res.DurationMs = time.Since(reqStart).Milliseconds()
			return res, nil
		}
		// 此后的失败属于 resume 阶段。
		phase, phaseStart = apigen.SetupPhaseResume, time.Now()
	} else {
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			return fail(ctlErr("failed", "%v", err))
		}
		if hasCheckout(workdir) {
			if c := r.gitRev(ctx, workdir, envList(env), "HEAD"); c != "" {
				res.BaseCommit = new(c)
			}
		}
	}

	// 5. resume 脚本：至多等 ResumeWait，其余放到后台继续；结果在脚本真正结束时上报。
	handedOff = true
	r.runResume(r.ctx, lg, workdir, env, p.Scripts.Resume, apigen.SetupTriggerAuto, nil, false)

	res.Ok = res.SetupError == nil
	res.DurationMs = time.Since(reqStart).Milliseconds()
	// 受管服务：setup 与 resume 之后（脚本失败也一样）按 services.yaml 与 state.json 确保运行。
	go r.svc.Ensure()
	return res, nil
}

func (r *Runtime) setupMarker() string { return filepath.Join(r.home, ".oryn", "setup.done") }

// setupScripts 执行 setup 阶段的脚本（合计 20 分钟）；成功后写 setup.done 标记。
// Docker 类型的 orb 先等 dockerd API 就绪（OW-1），未就绪则以明确的错误失败、不运行脚本。
func (r *Runtime) setupScripts(ctx context.Context, lg *setupLogger, workdir string, env map[string]string, project *string) error {
	start := time.Now()
	sctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	cmds, err := r.scriptCmds(workdir, project, "setup")
	if err == nil {
		if len(cmds) == 0 {
			lg.system(apigen.Setup, "no setup script (project settings, .oryn/setup or .agents/setup)")
		} else {
			err = r.waitDockerd(sctx, lg, apigen.Setup)
		}
	}
	if err == nil {
		err = runCmds(sctx, lg, apigen.Setup, workdir, env, cmds)
	}
	if err == nil {
		err = writeFileAtomic(r.setupMarker(), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
	}
	if err != nil {
		lg.system(apigen.Setup, "setup failed: "+err.Error())
		return err
	}
	lg.system(apigen.Setup, fmt.Sprintf("setup completed in %s", time.Since(start).Round(time.Millisecond)))
	return nil
}

func errCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "failed"
}

func hasCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// setupInputFiles 是决定 setup 产物的仓库内文件（S-P2-1）：它们在 repo.update 前后有任何变化，
// 旧快照上的增量刷新就不可信，控制面改从基础镜像开新血统。
var setupInputFiles = []string{".oryn/setup", ".agents/setup", ".oryn/services.yaml", ".amp/services.yaml"}

// setupInputsDigest 摘要工作区里 setupInputFiles 的现状（类型、可执行位、内容或链接目标；缺失也算一种状态）。
// 看磁盘而不是提交：未提交的改动同样影响上一次 setup 跑的是什么。
func setupInputsDigest(workdir string) [sha256.Size]byte {
	h := sha256.New()
	for _, rel := range setupInputFiles {
		p := filepath.Join(workdir, rel)
		fmt.Fprintf(h, "%s\x00", rel)
		st, err := os.Lstat(p)
		switch {
		case err != nil:
			fmt.Fprint(h, "missing\x00")
		case st.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			fmt.Fprintf(h, "symlink\x00%s\x00", target)
		case st.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				fmt.Fprintf(h, "unreadable\x00%v\x00", err)
				continue
			}
			fmt.Fprintf(h, "file\x00%o\x00%d\x00", st.Mode().Perm()&0o111, len(b))
			h.Write(b)
		default:
			fmt.Fprintf(h, "other\x00%v\x00", st.Mode().Type())
		}
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// skipSmudge 是 clone 与 repo.update 用的环境：GIT_LFS_SKIP_SMUDGE=1 让检出只写指针文件（M24），对象由 lfsPull 统一取。
// 跳过 smudge 照样会装 git-lfs 的 pre-push 钩子；agent 自己运行的 git 不受影响。
func skipSmudge(env map[string]string) []string {
	e := maps.Clone(env)
	e["GIT_LFS_SKIP_SMUDGE"] = "1"
	return envList(e)
}

// lfsPull 在检出之后取 Git LFS 对象（M24）；仓库没有 filter=lfs 的文件时什么都不做。返回值并入 SetupError。
func (r *Runtime) lfsPull(ctx context.Context, lg *setupLogger, workdir string, env []string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cmd := r.bins.command(cctx, "git", "-C", workdir, "ls-files", "-z", "--", ":(attr:filter=lfs)")
	cmd.Env = env
	out, err := cmd.Output()
	cancel()
	if err != nil {
		lg.system(apigen.Git, "warning: could not list Git LFS files; skipping git lfs pull: "+err.Error())
		return nil
	}
	n := 0
	for f := range bytes.SplitSeq(out, []byte{0}) {
		if len(f) > 0 {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	vctx, vcancel := context.WithTimeout(ctx, 30*time.Second)
	ver := r.bins.command(vctx, "git", "lfs", "version")
	ver.Dir, ver.Env = workdir, env
	err = ver.Run()
	vcancel()
	if err != nil {
		lg.system(apigen.Git, fmt.Sprintf("warning: the repository has %d Git LFS file(s) but git-lfs is not installed; they stay pointer files", n))
		return fmt.Errorf("git-lfs is not installed; %d LFS file(s) are pointer files", n)
	}
	// 没有 lfs filter 配置时（宿主或镜像没跑过 git lfs install），git lfs pull 只下载不检出、照样退出 0，
	// 文件留作指针却像成功了一样：先在本仓库装上 filter（同时让 agent 之后的检出也走 smudge）。
	fctx, fcancel := context.WithTimeout(ctx, 30*time.Second)
	probe := r.bins.command(fctx, "git", "-C", workdir, "config", "--get", "filter.lfs.process")
	probe.Env = env
	hasFilter := probe.Run() == nil
	fcancel()
	if !hasFilter {
		ictx, icancel := context.WithTimeout(ctx, 30*time.Second)
		err = lg.run(ictx, apigen.Git, workdir, env, "git", "-C", workdir, "lfs", "install", "--local")
		icancel()
		if err != nil {
			lg.system(apigen.Git, fmt.Sprintf("warning: could not configure Git LFS for the repository; %d LFS file(s) stay pointer files", n))
			return fmt.Errorf("git lfs install --local: %v", err)
		}
	}
	pctx, pcancel := context.WithTimeout(ctx, setupTimeout)
	err = lg.run(pctx, apigen.Git, workdir, env, "git", "-C", workdir, "lfs", "pull")
	pcancel()
	if err != nil {
		lg.system(apigen.Git, "warning: git lfs pull failed; files that could not be downloaded are still pointer files. "+
			"Fix the cause, then run `git lfs pull` in "+workdir)
		return fmt.Errorf("git lfs pull: %v", err)
	}
	lg.system(apigen.Git, fmt.Sprintf("git lfs pull: %d LFS file(s) checked out", n))
	return nil
}

// updateRepo 实现 repo.update（M11a 2.3）：fetch 基线（分支名，缺省远端 HEAD）；HEAD 不等于 fetch 到的提交时，
// 快进（完整克隆且原 HEAD 是其祖先）用 merge --ff-only，否则 reset --hard，之后以实际 HEAD 核对（OW-2：merge
// 退出码为 0 也可能没动，例如 HEAD 领先于基线），仍不等就 reset --hard；移动成功后删掉 setup 完成标记。
// updated 报告 HEAD 是否真的移到了基线；setupChanged（S-P2-1）在基线不是原 HEAD 的快进、或 setupInputFiles
// 有变化时为真。fetch 或移动失败只记日志：沿用当前检出（快照里的提交），不让 orb 失败。
func (r *Runtime) updateRepo(ctx context.Context, lg *setupLogger, workdir string, env []string, repo *apigen.LinkCtlSetupRepo) (updated, setupChanged bool) {
	ref := "HEAD"
	if repo.Branch != nil && *repo.Branch != "" {
		ref = *repo.Branch
	}
	args := []string{"-C", workdir, "fetch"}
	if repo.Shallow {
		args = append(args, "--depth", "1")
	}
	args = append(args, "--", "origin", ref)
	cctx, cancel := context.WithTimeout(ctx, setupTimeout)
	err := lg.run(cctx, apigen.Git, workdir, env, "git", args...)
	cancel()
	if err != nil {
		lg.system(apigen.Git, "repo update: fetch failed; keeping the current checkout: "+err.Error())
		return false, false
	}
	head, fetched := r.gitRev(ctx, workdir, env, "HEAD"), r.gitRev(ctx, workdir, env, "FETCH_HEAD")
	if fetched == "" {
		lg.system(apigen.Git, "repo update: FETCH_HEAD unreadable; keeping the current checkout")
		return false, false
	}
	if head == fetched {
		lg.system(apigen.Git, "repo update: already at "+shortRev(head))
		return false, false
	}
	before := setupInputsDigest(workdir)
	ff := head != "" && r.fastForward(ctx, lg, workdir, env, repo, ref, head, fetched)
	// 移动一律指向已解析的提交（而不是 FETCH_HEAD：判定快进时可能又 fetch 过一次）。
	if ff && !repo.Shallow {
		_ = lg.run(ctx, apigen.Git, workdir, env, "git", "-C", workdir, "merge", "--ff-only", fetched)
	}
	if now := r.gitRev(ctx, workdir, env, "HEAD"); now != fetched {
		if err := lg.run(ctx, apigen.Git, workdir, env, "git", "-C", workdir, "reset", "--hard", fetched); err != nil {
			lg.system(apigen.Git, "repo update: reset failed; keeping the current checkout: "+err.Error())
			return false, false
		}
	}
	if now := r.gitRev(ctx, workdir, env, "HEAD"); now != fetched {
		lg.system(apigen.Git, fmt.Sprintf("repo update: HEAD is %s, not %s; keeping the current checkout", shortRev(now), shortRev(fetched)))
		return false, false
	}
	if err := os.Remove(r.setupMarker()); err != nil && !errors.Is(err, os.ErrNotExist) {
		lg.system(apigen.Git, "repo update: remove setup marker: "+err.Error())
	}
	inputsChanged := setupInputsDigest(workdir) != before
	setupChanged = !ff || inputsChanged
	lg.system(apigen.Git, fmt.Sprintf("repo update: %s -> %s (fast-forward: %t, setup inputs changed: %t); setup scripts will rerun",
		shortRev(head), shortRev(fetched), ff, inputsChanged))
	return true, setupChanged
}

// fastForward 报告 fetched 是否是 head 的后代（基线快进）。浅克隆里祖先关系常被截断：判不出时按 head 的提交时间
// 补一次 `fetch --shallow-since`（只取 head 之后的历史）再判；仍判不出、或基线在此期间又动了，一律当作非快进。
func (r *Runtime) fastForward(ctx context.Context, lg *setupLogger, workdir string, env []string, repo *apigen.LinkCtlSetupRepo, ref, head, fetched string) bool {
	if r.isAncestor(ctx, workdir, env, head, fetched) {
		return true
	}
	if !repo.Shallow {
		return false
	}
	cmd := r.bins.command(ctx, "git", "-C", workdir, "log", "-1", "--format=%ct", head)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	ct, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return false
	}
	since := time.Unix(ct-1, 0).UTC().Format(time.RFC3339)
	cctx, cancel := context.WithTimeout(ctx, setupTimeout)
	err = lg.run(cctx, apigen.Git, workdir, env, "git", "-C", workdir, "fetch", "--shallow-since="+since, "--", "origin", ref)
	cancel()
	if err != nil {
		lg.system(apigen.Git, "repo update: could not deepen the shallow history; treating the update as not a fast-forward")
		return false
	}
	if again := r.gitRev(ctx, workdir, env, "FETCH_HEAD"); again != fetched {
		return false
	}
	return r.isAncestor(ctx, workdir, env, head, fetched)
}

// isAncestor 报告 a 是否是 b 的祖先；判不出（对象缺失、浅克隆截断）时为假。
func (r *Runtime) isAncestor(ctx context.Context, dir string, env []string, a, b string) bool {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := r.bins.command(cctx, "git", "-C", dir, "merge-base", "--is-ancestor", a, b)
	cmd.Env = env
	return cmd.Run() == nil
}

// commitHashRe 是完整的提交哈希（SHA-1 40 位或 SHA-256 64 位小写十六进制）。
var commitHashRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// gitRev 解析 rev 为提交的完整哈希；失败或输出不是合法的提交哈希（S-P2-5）时返回空串。
func (r *Runtime) gitRev(ctx context.Context, dir string, env []string, rev string) string {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := r.bins.command(cctx, "git", "-C", dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if c := strings.TrimSpace(string(out)); commitHashRe.MatchString(c) {
		return c
	}
	return ""
}

func shortRev(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// nextOmpSeq 取下一个 omp 配置写入编号（见 Runtime.ompSeq）。
func (r *Runtime) nextOmpSeq() uint64 {
	r.ompMu.Lock()
	defer r.ompMu.Unlock()
	r.ompSeq++
	return r.ompSeq
}

// writeOmpConfigAt 以编号 seq 写入 omp 文本配置：用户层 files，setProject 时连同项目层 project（nil = 删除项目层），
// ssh 非 nil 时连同 orb 内的 SSH 配置（M18，nil = 不动）。
// 各部分各自记已写入的最大编号，某部分已有更晚到达的请求写过时跳过；返回两层各自是否写了。
// 写入全程持有 ompMu，几路写入不会交错，清单（manifest）始终与该层最后一次写入一致。
func (r *Runtime) writeOmpConfigAt(seq uint64, files []apigen.LinkCtlOmpHomeFile, project *apigen.LinkCtlProjectConfig, setProject bool, ssh *apigen.LinkCtlSSHConfig) (wroteHome, wroteProject bool, err error) {
	r.ompMu.Lock()
	defer r.ompMu.Unlock()
	if seq > r.ompWritten {
		if err := r.writeOmpHome(files); err != nil {
			return false, false, fmt.Errorf("omp home: %w", err)
		}
		r.ompWritten, wroteHome = seq, true
	}
	if setProject && seq > r.projWritten {
		if err := r.writeProjectOmp(project); err != nil {
			return wroteHome, false, fmt.Errorf("project omp config: %w", err)
		}
		r.projWritten, wroteProject = seq, true
	}
	// 写在用户层之后：用户层的清单清理不会删掉刚写的 ssh.json
	if ssh != nil && seq > r.sshWritten {
		if err := r.writeSSH(ssh); err != nil {
			return wroteHome, wroteProject, fmt.Errorf("ssh config: %w", err)
		}
		r.sshWritten = seq
	}
	return wroteHome, wroteProject, nil
}

// runnerOmpConfigDir 是 runner 上 agent 的 omp 配置根（相对 $HOME，即 agent 环境的 PI_CONFIG_DIR，M21）：与那台机器上
// 用户自己的 ~/.omp 互不相干。
const runnerOmpConfigDir = ".oryn/omp"

// ompConfigRoot 是 agent 的 omp 配置根：orb 与协调宿主为 ~/.omp，runner 为 ~/.oryn/omp。
func (r *Runtime) ompConfigRoot() string {
	if r.runner != nil {
		return filepath.Join(r.home, filepath.FromSlash(runnerOmpConfigDir))
	}
	return filepath.Join(r.home, ".omp")
}

// ompAgentDir 是下发的 omp 用户层所在的 agent 目录（<配置根>/agent）。
func (r *Runtime) ompAgentDir() string { return filepath.Join(r.ompConfigRoot(), "agent") }

// ompHomeApplied 报告本进程是否已写入过控制面下发的用户层（runner 在此之前拒绝 agent.start，M21）。
func (r *Runtime) ompHomeApplied() bool {
	r.ompMu.Lock()
	defer r.ompMu.Unlock()
	return r.ompWritten > 0
}

// writeOmpHome 把 files 写进 agent 目录（见 ompAgentDir）并清理上次下发、这次已不在清单里的文件。调用方经 writeOmpConfigAt 串行化。
// runner 的 models.yml 按实际出站地址改写后再写（见 runnerModelsContent）。
func (r *Runtime) writeOmpHome(files []apigen.LinkCtlOmpHomeFile) error {
	base := r.ompAgentDir()
	for _, f := range files {
		path, err := relUnder(base, f.Path)
		if err != nil {
			return err
		}
		data, err := base64.StdEncoding.DecodeString(f.ContentB64)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		mode := os.FileMode(f.Mode) & os.ModePerm
		if mode == 0 {
			mode = 0o644
		}
		if err := writeFileAtomic(path, r.runnerModelsContent(f.Path, data), mode); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	return r.pruneOmpHome(base, files)
}

// pruneOmpHome 删除上次由 Oryn 下发、这次已不在配置中的文件（配置删除要能传播到 orb，
// 例如移除某个扩展）。只动清单里记录过的路径，omp 自己产生的文件不受影响。
func (r *Runtime) pruneOmpHome(base string, files []apigen.LinkCtlOmpHomeFile) error {
	manifest := filepath.Join(r.home, ".oryn", "omp-home.manifest.json")
	var prev []string
	if b, err := os.ReadFile(manifest); err == nil {
		_ = json.Unmarshal(b, &prev)
	}
	cur := make(map[string]bool, len(files))
	list := make([]string, 0, len(files))
	for _, f := range files {
		cur[f.Path] = true
		list = append(list, f.Path)
	}
	for _, p := range prev {
		if cur[p] {
			continue
		}
		path, err := relUnder(base, p)
		if err != nil {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除已移除的配置文件 %s: %w", p, err)
		}
	}
	b, _ := json.Marshal(list)
	return writeFileAtomic(manifest, b, 0o600)
}

func nonEmpty(s *string) bool { return s != nil && strings.TrimSpace(*s) != "" }

// repoScript 返回工作目录内可执行的 `.oryn/<name>`，缺失时回退 `.agents/<name>`；都没有返回空串。
func repoScript(workdir, name string) string {
	for _, rel := range []string{".oryn/" + name, ".agents/" + name} {
		p := filepath.Join(workdir, rel)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// scriptCmd 是一条要执行的脚本命令；label 用于日志与错误信息。
type scriptCmd struct {
	label string
	argv  []string
}

// scriptCmds 列出某阶段要执行的命令：项目脚本（内容）+ 仓库内 `.oryn/<name>`，缺失时回退 `.agents/<name>`。
func (r *Runtime) scriptCmds(workdir string, project *string, name string) ([]scriptCmd, error) {
	var cmds []scriptCmd
	if nonEmpty(project) {
		path := filepath.Join(r.home, ".cache", "oryn", "project-"+name+".sh")
		if err := writeFileAtomic(path, []byte(*project), 0o700); err != nil {
			return nil, err
		}
		argv := []string{"bash", path}
		if strings.HasPrefix(*project, "#!") {
			argv = []string{path}
		}
		cmds = append(cmds, scriptCmd{label: "project " + name + " script", argv: argv})
	}
	if p := repoScript(workdir, name); p != "" {
		rel, _ := filepath.Rel(workdir, p)
		cmds = append(cmds, scriptCmd{label: rel, argv: []string{p}})
	}
	return cmds, nil
}

// runCmds 依次执行命令，遇错即停；错误带上脚本名。
func runCmds(ctx context.Context, lg *setupLogger, phase apigen.LinkCtlSetupLogParamsPhase, workdir string, env map[string]string, cmds []scriptCmd) error {
	for _, c := range cmds {
		// 每个脚本都在登录 shell 环境下运行（逐个求值）：前一个脚本写进 profile 的 PATH 等对后面的脚本可见。
		lg.system(phase, "running "+strings.Join(c.argv, " "))
		if err := lg.run(ctx, phase, workdir, envList(loginEnv(ctx, workdir, env)), c.argv[0], c.argv[1:]...); err != nil {
			return fmt.Errorf("%s: %w", c.label, err)
		}
	}
	return nil
}

// runResume 执行 resume 阶段并在脚本真正结束时上报 setup.result、释放 setup 互斥（调用方须已持有）。
// foreground=false 时至多等 ResumeWait，超时把余下部分留在后台（"still running" 记入 setupLg，可为 nil）；
// foreground=true 时等到结束（手动重跑）。返回值在后台情形下只是尚未完成的部分结果。
func (r *Runtime) runResume(ctx context.Context, setupLg *setupLogger, workdir string, env map[string]string, project *string,
	trigger apigen.SetupTrigger, tee teeFunc, foreground bool,
) apigen.LinkCtlSetupResultParams {
	start := time.Now()
	res := apigen.LinkCtlSetupResultParams{
		Phase: apigen.SetupPhaseResume, Trigger: trigger, StartedAt: start.UnixMilli(),
		HasProjectScript: nonEmpty(project), HasRepoScript: repoScript(workdir, "resume") != "",
	}
	finish := func(err error) apigen.LinkCtlSetupResultParams {
		res.Ok = err == nil
		if err != nil {
			res.Error = new(err.Error())
		}
		res.DurationMs = time.Since(start).Milliseconds()
		r.publishResult(res)
		r.releaseSetup()
		return res
	}
	lg, closeLog, err := r.openLogger("resume.log", tee)
	if err != nil {
		r.log.Warn("open resume log", "err", err)
		return finish(err)
	}
	cmds, err := r.scriptCmds(workdir, project, "resume")
	if err != nil || len(cmds) == 0 {
		if err != nil {
			lg.system(apigen.Resume, "resume: "+err.Error())
		} else {
			lg.system(apigen.Resume, "no resume script (project settings, .oryn/resume or .agents/resume)")
		}
		closeLog()
		return finish(err)
	}
	done := make(chan apigen.LinkCtlSetupResultParams, 1)
	go func() {
		defer closeLog()
		// 不受 ctl 请求约束：超过等待时间后在后台继续运行（上限 resumeTimeout）。
		rctx, cancel := context.WithTimeout(ctx, resumeTimeout)
		defer cancel()
		// Docker 类型先等 dockerd 就绪（OW-1）；在后台 goroutine 里等，不拖过 ResumeWait。
		err := r.waitDockerd(rctx, lg, apigen.Resume)
		if err == nil {
			err = runCmds(rctx, lg, apigen.Resume, workdir, env, cmds)
		}
		if err != nil {
			lg.system(apigen.Resume, "resume failed: "+err.Error())
		} else {
			lg.system(apigen.Resume, fmt.Sprintf("resume completed in %s", time.Since(start).Round(time.Millisecond)))
		}
		done <- finish(err)
	}()
	if foreground {
		return <-done
	}
	tm := time.NewTimer(r.cfg.ResumeWait)
	defer tm.Stop()
	select {
	case res := <-done:
		return res
	case <-tm.C:
		if setupLg != nil {
			setupLg.system(apigen.Resume, "resume still running; continuing in background")
		}
		return res
	}
}

// teeFunc 把日志另外交给一个调用方（orb 内 `oryn orb setup run` 的实时输出）。
type teeFunc func(stream apigen.LinkCtlSetupLogParamsStream, data []byte)

// openLogger 以追加方式打开 $HOME/.cache/oryn/logs/<name>。
func (r *Runtime) openLogger(name string, tee teeFunc) (*setupLogger, func(), error) {
	logDir := filepath.Join(r.home, ".cache", "oryn", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return &setupLogger{r: r, file: f, tee: tee}, func() { _ = f.Close() }, nil
}

// setupLogger 把输出写入日志文件并以 setup.log 通知流式上报。
type setupLogger struct {
	r    *Runtime
	mu   sync.Mutex
	file io.Writer
	tee  teeFunc
}

func (l *setupLogger) emit(phase apigen.LinkCtlSetupLogParamsPhase, stream apigen.LinkCtlSetupLogParamsStream, data []byte) {
	l.mu.Lock()
	_, _ = l.file.Write(data)
	if l.tee != nil {
		l.tee(stream, data)
	}
	l.mu.Unlock()
	for len(data) > 0 {
		n := min(len(data), logChunk)
		l.r.notify("setup.log", apigen.LinkCtlSetupLogParams{Phase: phase, Stream: stream, Data: string(data[:n])})
		data = data[n:]
	}
}

func (l *setupLogger) system(phase apigen.LinkCtlSetupLogParamsPhase, msg string) {
	l.emit(phase, apigen.System, []byte("[orbd] "+msg+"\n"))
}

type logWriter struct {
	l      *setupLogger
	phase  apigen.LinkCtlSetupLogParamsPhase
	stream apigen.LinkCtlSetupLogParamsStream
}

func (w logWriter) Write(p []byte) (int, error) {
	w.l.emit(w.phase, w.stream, append([]byte(nil), p...))
	return len(p), nil
}

// run 执行命令（独立进程组；ctx 结束时杀掉整个组），输出流式记录。
func (l *setupLogger) run(ctx context.Context, phase apigen.LinkCtlSetupLogParamsPhase, dir string, env []string, name string, args ...string) error {
	cmd := l.r.bins.command(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = logWriter{l, phase, apigen.Stdout}
	cmd.Stderr = logWriter{l, phase, apigen.Stderr}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		return fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return err
}

// gitEntryRe 是 GitHosts 条目：主机（可带端口），可选地跟仓库路径段（不含 `.`/`..`、空段与引号等）。
var (
	gitHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	gitSegRe  = regexp.MustCompile(`^[A-Za-z0-9_~][A-Za-z0-9._~-]*$`)
)

// validGitEntry 校验 `host` 或 `host/seg/…` 形式的 GitHosts 条目（写进 git 配置键，不能夹带额外语法）。
func validGitEntry(e string) bool {
	host, rest, hasPath := strings.Cut(e, "/")
	if !gitHostRe.MatchString(host) {
		return false
	}
	if !hasPath {
		return true
	}
	segs := strings.Split(rest, "/")
	if len(segs) < 2 {
		return false
	}
	for _, s := range segs {
		if !gitSegRe.MatchString(s) || s == "." || s == ".." {
			return false
		}
	}
	return true
}

// noProxyCovers 报告 NO_PROXY / no_proxy 的任一取值是否会让发往 `http://<host>/` 的请求绕过代理（host 可带端口，
// 缺省端口 80）。取两种客户端判定的并集，宁可多判：多判只会让该主机退回 https 直连。
//   - libcurl（git 经 CURLOPT_NOPROXY 传入，curl 8.23 lib/proxy.c proxy_check_noproxy）：整串恰为 `*` 时全部命中；
//     条目以逗号或空白分隔，去掉开头与结尾的 `.` 后等于主机名，或是主机名以 `.` 分界的后缀即命中；条目不认端口，
//     带端口的条目按字面比较（因此与主机名不会相等）；主机是 IP 时按 IP 或 CIDR 比较。
//   - Go（git-lfs 经 golang.org/x/net/http/httpproxy）：任一条目为 `*` 时全部命中；`host:port` 只在端口与请求端口相同时命中；
//     `*.domain` 与 `.domain` 只命中子域。
func noProxyCovers(host string, values ...string) bool {
	name, port := host, "80"
	if h, p, err := net.SplitHostPort(host); err == nil {
		name, port = h, p
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	ip := net.ParseIP(name)
	for _, v := range values {
		if v == "*" {
			return true
		}
		for _, tok := range strings.FieldsFunc(strings.ToLower(v), func(c rune) bool { return c == ',' || c == ' ' || c == '\t' }) {
			if tok == "*" {
				return true
			}
			if h, p, err := net.SplitHostPort(tok); err == nil {
				if p != port {
					continue
				}
				tok = h
			}
			if ip != nil {
				if _, cidr, err := net.ParseCIDR(tok); err == nil && cidr.Contains(ip) {
					return true
				}
				if t := net.ParseIP(tok); t != nil && t.Equal(ip) {
					return true
				}
				continue
			}
			if sub, ok := strings.CutPrefix(tok, "*."); ok {
				if strings.HasSuffix(name, "."+sub) {
					return true
				}
				continue
			}
			if tok = strings.Trim(tok, "."); tok != "" && (name == tok || strings.HasSuffix(name, "."+tok)) {
				return true
			}
		}
	}
	return false
}

// clearGitEgress 删除全局 git 配置里 Oryn 为 egress 写入的条目，用户自己的 insteadOf 与代理设置不动：
//   - `url.http://<entry>.insteadOf = https://<entry>`：只把 https 换成 http 的同址改写（setup 第 2 步写的形式）；
//   - `http.http://<host>/.proxy`，值是回环地址上任一端口或当前 egress 地址；
//   - M15 之前的 `url.http://127.0.0.1:<port>/git/<entry>.insteadOf`（老 orb 在下一次 setup / resume 时清掉）。
//
// 回环地址上的其它端口也算：process 驱动下 orbd 每次启动的 egress 端口不同，旧端口的条目会让 git 连到已不存在的代理。
func (r *Runtime) clearGitEgress(ctx context.Context, env map[string]string) error {
	addr := `(127\.0\.0\.1:[0-9]+|` + regexp.QuoteMeta(r.EgressAddr()) + `)`
	legacy := regexp.MustCompile(`^url\.http://` + addr + `/git/.*\.insteadof$`)
	proxyVal := regexp.MustCompile(`^http://` + addr + `$`)
	cmd := r.bins.command(ctx, "git", "config", "--global", "-z", "--get-regexp", `^(url\.http://.*\.insteadof|http\.http://.*\.proxy)$`)
	cmd.Dir, cmd.Env = r.home, envList(env)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 { // 没有匹配项（或尚无 ~/.gitconfig）
			return nil
		}
		return err
	}
	seen := map[string]bool{}
	for _, rec := range strings.Split(string(out), "\x00") {
		name, value, _ := strings.Cut(rec, "\n") // -z：`名\n值`，记录以 NUL 结尾
		var ours bool
		switch {
		case legacy.MatchString(name):
			ours = true
		case strings.HasPrefix(name, "url.http://") && strings.HasSuffix(name, ".insteadof"):
			ours = value == "https://"+strings.TrimSuffix(strings.TrimPrefix(name, "url.http://"), ".insteadof")
		case strings.HasPrefix(name, "http.http://") && strings.HasSuffix(name, ".proxy"):
			ours = proxyVal.MatchString(value)
		}
		if !ours || seen[rec] {
			continue
		}
		seen[rec] = true
		rm := r.bins.command(ctx, "git", "config", "--global", "--unset-all", name, "^"+regexp.QuoteMeta(value)+"$")
		rm.Dir, rm.Env = r.home, envList(env)
		if b, err := rm.CombinedOutput(); err != nil {
			return fmt.Errorf("unset %s: %v: %s", name, err, strings.TrimSpace(string(b)))
		}
	}
	return nil
}
