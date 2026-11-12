package orbdrt

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

const (
	setupTimeout = 20 * time.Minute
	resumeWait   = 10 * time.Second
	logChunk     = 16 << 10
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

// runSetup 实现 setup.run（full=true）与 resume.run（H3.3）。同一时刻只运行一个。
func (r *Runtime) runSetup(ctx context.Context, p apigen.LinkCtlSetupRunParams, full bool) (apigen.LinkCtlSetupRunResult, error) {
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	start := time.Now()
	res := apigen.LinkCtlSetupRunResult{}

	logDir := filepath.Join(r.home, ".cache", "oryn", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return res, ctlErr("failed", "%v", err)
	}
	setupLog, err := os.OpenFile(filepath.Join(logDir, "setup.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return res, ctlErr("failed", "%v", err)
	}
	defer setupLog.Close()
	lg := &setupLogger{r: r, file: setupLog}

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
	r.envMu.Lock()
	r.setupEnv = maps.Clone(p.Env)
	r.workdir = workdir
	r.envMu.Unlock()

	// 1. omp 文本配置。
	if err := r.writeOmpHome(p.OmpHome); err != nil {
		lg.system(apigen.OmpHome, "error: "+err.Error())
		return res, ctlErr("failed", "omp home: %v", err)
	}
	lg.system(apigen.OmpHome, fmt.Sprintf("wrote %d file(s) to ~/.omp/agent", len(p.OmpHome)))

	// 2. git insteadOf：经本机 egress 代理访问 git 主机（控制面注入令牌）。
	for _, h := range p.GitHosts {
		if h == "" || strings.ContainsAny(h, "/ \t\n@") {
			return res, ctlErr(link.CodeInvalidParams, "invalid git host %q", h)
		}
		key := fmt.Sprintf("url.http://%s/git/%s/.insteadOf", r.EgressAddr(), h)
		if err := lg.run(ctx, apigen.Git, r.home, envList(env), "git", "config", "--global", key, "https://"+h+"/"); err != nil {
			return res, ctlErr("failed", "git config for %s: %v", h, err)
		}
	}

	if full {
		// 3. clone：仓库存在且工作目录尚无检出。
		if err := os.MkdirAll(filepath.Dir(workdir), 0o755); err != nil {
			return res, ctlErr("failed", "%v", err)
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
				return res, ctlErr(errCode(err), "git clone: %v", err)
			}
		}
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			return res, ctlErr("failed", "%v", err)
		}

		// 4. setup 脚本（合计 20 分钟），成功后写标记。
		marker := filepath.Join(r.home, ".oryn", "setup.done")
		if _, err := os.Stat(marker); err != nil {
			res.SetupRan = true
			sctx, cancel := context.WithTimeout(ctx, setupTimeout)
			err := r.runScripts(sctx, lg, apigen.Setup, workdir, env, p.Scripts.Setup, "setup")
			cancel()
			if err != nil {
				lg.system(apigen.Setup, "setup failed: "+err.Error())
				return res, ctlErr(errCode(err), "setup: %v", err)
			}
			if err := writeFileAtomic(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
				return res, ctlErr("failed", "%v", err)
			}
			lg.system(apigen.Setup, "setup completed")
		} else {
			lg.system(apigen.Setup, "setup already done; skipped")
		}
	} else if err := os.MkdirAll(workdir, 0o755); err != nil {
		return res, ctlErr("failed", "%v", err)
	}

	// 5. resume 脚本：至多等 10 秒，其余放到后台继续。
	r.startResume(lg, workdir, env, p.Scripts.Resume)

	res.Ok = true
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
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
	return nil
}

// scriptCmds 列出某阶段要执行的命令：项目脚本（内容）+ 仓库内 `.oryn/<name>`，缺失时回退 `.agents/<name>`。
func (r *Runtime) scriptCmds(phase apigen.LinkCtlSetupLogParamsPhase, workdir string, project *string, name string) ([][]string, error) {
	var cmds [][]string
	if project != nil && strings.TrimSpace(*project) != "" {
		dir := filepath.Join(r.home, ".cache", "oryn")
		path := filepath.Join(dir, "project-"+name+".sh")
		if err := writeFileAtomic(path, []byte(*project), 0o700); err != nil {
			return nil, err
		}
		if strings.HasPrefix(*project, "#!") {
			cmds = append(cmds, []string{path})
		} else {
			cmds = append(cmds, []string{"bash", path})
		}
	}
	for _, rel := range []string{".oryn/" + name, ".agents/" + name} {
		p := filepath.Join(workdir, rel)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			cmds = append(cmds, []string{p})
			break
		}
	}
	_ = phase
	return cmds, nil
}

func (r *Runtime) runScripts(ctx context.Context, lg *setupLogger, phase apigen.LinkCtlSetupLogParamsPhase, workdir string, env map[string]string, project *string, name string) error {
	cmds, err := r.scriptCmds(phase, workdir, project, name)
	if err != nil {
		return err
	}
	for _, c := range cmds {
		lg.system(phase, "running "+strings.Join(c, " "))
		if err := lg.run(ctx, phase, workdir, envList(env), c[0], c[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) startResume(setupLg *setupLogger, workdir string, env map[string]string, project *string) {
	cmds, err := r.scriptCmds(apigen.Resume, workdir, project, "resume")
	if err != nil {
		setupLg.system(apigen.Resume, "resume: "+err.Error())
		return
	}
	if len(cmds) == 0 {
		return
	}
	f, err := os.OpenFile(filepath.Join(r.home, ".cache", "oryn", "logs", "resume.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		setupLg.system(apigen.Resume, "resume: "+err.Error())
		return
	}
	lg := &setupLogger{r: r, file: f}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer f.Close()
		for _, c := range cmds {
			lg.system(apigen.Resume, "running "+strings.Join(c, " "))
			// resume 脚本不受请求 ctx 约束：超过等待时间后在后台继续运行。
			if err := lg.run(context.Background(), apigen.Resume, workdir, envList(env), c[0], c[1:]...); err != nil {
				lg.system(apigen.Resume, "resume failed: "+err.Error())
				return
			}
		}
	}()
	tm := time.NewTimer(resumeWait)
	defer tm.Stop()
	select {
	case <-done:
	case <-tm.C:
		setupLg.system(apigen.Resume, "resume still running; continuing in background")
	}
}

// setupLogger 把输出写入日志文件并以 setup.log 通知流式上报。
type setupLogger struct {
	r    *Runtime
	mu   sync.Mutex
	file io.Writer
}

func (l *setupLogger) emit(phase apigen.LinkCtlSetupLogParamsPhase, stream apigen.LinkCtlSetupLogParamsStream, data []byte) {
	l.mu.Lock()
	_, _ = l.file.Write(data)
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
