package orbdrt

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	op.res, op.err = r.runSetupLocked(ctx, p, full)
	return op.res, op.err
}

// runSetupLocked 是 runSetup 的主体；调用方已持有 setup 互斥。
func (r *Runtime) runSetupLocked(ctx context.Context, p apigen.LinkCtlSetupRunParams, full bool) (apigen.LinkCtlSetupRunResult, error) {
	res := apigen.LinkCtlSetupRunResult{}
	// resume 交给后台时连同互斥一起移交，由它在脚本结束时释放。
	handedOff := false
	defer func() {
		if !handedOff {
			r.releaseSetup()
		}
	}()
	reqStart := time.Now()
	phase, phaseStart := apigen.SetupPhaseResume, reqStart
	if full {
		phase = apigen.SetupPhaseSetup
	}
	// 中途出错（omp 配置、git、clone）也上报当前阶段的失败结果，控制面据此留下这次运行与日志。
	fail := func(err *link.CtlError) (apigen.LinkCtlSetupRunResult, error) {
		r.publishResult(apigen.LinkCtlSetupResultParams{
			Phase: phase, Trigger: apigen.SetupTriggerAuto, Ok: false, Error: new(err.Message),
			StartedAt: phaseStart.UnixMilli(), DurationMs: time.Since(phaseStart).Milliseconds(),
		})
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
	env := r.baseEnv()
	maps.Copy(env, p.Env)
	// 记住本次参数，供 orb 内 `oryn orb setup run` 重跑。
	r.envMu.Lock()
	r.setupEnv = maps.Clone(p.Env)
	r.workdir = workdir
	r.lastScripts = cloneScripts(p.Scripts)
	r.haveParams = true
	r.envMu.Unlock()

	// 1. omp 文本配置。
	if err := r.writeOmpHome(p.OmpHome); err != nil {
		lg.system(apigen.OmpHome, "error: "+err.Error())
		return fail(ctlErr("failed", "omp home: %v", err))
	}
	lg.system(apigen.OmpHome, fmt.Sprintf("wrote %d file(s) to ~/.omp/agent", len(p.OmpHome)))

	// 2. git insteadOf：经本机 egress 代理访问 git（控制面注入令牌）。条目为 `host`（整个主机，无项目的 orb 只读）
	// 或 `host/owner/repo`（只覆盖项目仓库；同主机其它地址照常直连公网、不带令牌）。先清掉以前写过的全部条目，
	// 策略收窄（如 orb 改属项目）后旧的整主机条目不会残留。
	for _, h := range p.GitHosts {
		if !validGitEntry(h) {
			return fail(ctlErr(link.CodeInvalidParams, "invalid git host %q", h))
		}
	}
	if err := r.clearGitInsteadOf(ctx, env); err != nil {
		return fail(ctlErr("failed", "git config cleanup: %v", err))
	}
	for _, h := range p.GitHosts {
		from, to := "https://"+h, fmt.Sprintf("http://%s/git/%s", r.EgressAddr(), h)
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

	if full {
		// 3. clone：仓库存在且工作目录尚无检出。
		if err := os.MkdirAll(filepath.Dir(workdir), 0o755); err != nil {
			return fail(ctlErr("failed", "%v", err))
		}
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
			err := lg.run(cctx, apigen.Clone, r.home, envList(env), "git", args...)
			cancel()
			if err != nil {
				return fail(ctlErr(errCode(err), "git clone: %v", err))
			}
		}
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			return fail(ctlErr("failed", "%v", err))
		}

		// 4. setup 脚本（合计 20 分钟），成功后写标记。
		sres := apigen.LinkCtlSetupResultParams{
			Phase: apigen.SetupPhaseSetup, Trigger: apigen.SetupTriggerAuto,
			StartedAt: phaseStart.UnixMilli(), HasProjectScript: nonEmpty(p.Scripts.Setup),
			HasRepoScript: repoScript(workdir, "setup") != "",
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
		sres.Ok, sres.Error = res.SetupError == nil, res.SetupError
		sres.DurationMs = time.Since(phaseStart).Milliseconds()
		r.publishResult(sres)
		// 此后的失败属于 resume 阶段。
		phase, phaseStart = apigen.SetupPhaseResume, time.Now()
	} else if err := os.MkdirAll(workdir, 0o755); err != nil {
		return fail(ctlErr("failed", "%v", err))
	}

	// 5. resume 脚本：至多等 ResumeWait，其余放到后台继续；结果在脚本真正结束时上报。
	handedOff = true
	r.runResume(r.ctx, lg, workdir, env, p.Scripts.Resume, apigen.SetupTriggerAuto, nil, false)

	res.Ok = res.SetupError == nil
	res.DurationMs = time.Since(reqStart).Milliseconds()
	return res, nil
}

func (r *Runtime) setupMarker() string { return filepath.Join(r.home, ".oryn", "setup.done") }

// setupScripts 执行 setup 阶段的脚本（合计 20 分钟）；成功后写 setup.done 标记。
func (r *Runtime) setupScripts(ctx context.Context, lg *setupLogger, workdir string, env map[string]string, project *string) error {
	start := time.Now()
	sctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	cmds, err := r.scriptCmds(workdir, project, "setup")
	if err == nil {
		if len(cmds) == 0 {
			lg.system(apigen.Setup, "no setup script (project settings, .oryn/setup or .agents/setup)")
		}
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

func (r *Runtime) writeOmpHome(files []apigen.LinkCtlOmpHomeFile) error {
	base := filepath.Join(r.home, ".omp", "agent")
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
		if err := writeFileAtomic(path, data, mode); err != nil {
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
		err := runCmds(rctx, lg, apigen.Resume, workdir, env, cmds)
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
	cmd := exec.CommandContext(ctx, name, args...)
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

// clearGitInsteadOf 删除全局 git 配置里所有指向本机 egress 代理的 insteadOf 条目（Oryn 写入的那些）。
func (r *Runtime) clearGitInsteadOf(ctx context.Context, env map[string]string) error {
	re := `^url\.http://` + regexp.QuoteMeta(r.EgressAddr()) + `/git/.*\.insteadof$`
	cmd := exec.CommandContext(ctx, "git", "config", "--global", "--name-only", "--get-regexp", re)
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
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		rm := exec.CommandContext(ctx, "git", "config", "--global", "--unset-all", name)
		rm.Dir, rm.Env = r.home, envList(env)
		if b, err := rm.CombinedOutput(); err != nil {
			return fmt.Errorf("unset %s: %v: %s", name, err, strings.TrimSpace(string(b)))
		}
	}
	return nil
}
