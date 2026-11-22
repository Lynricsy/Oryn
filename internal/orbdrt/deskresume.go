package orbdrt

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// 桌面 resume 脚本（M25 步骤 1.6，参考 Amp 的 `.agents/desktop/resume`）：每一代显示栈（openbox 起来一次算一代）
// 在工作区也就绪之后执行一次 `<workdir>/.oryn/desktop/resume`，没有就执行 `$HOME/.oryn/desktop/resume`。
// 冻结后唤醒代数不变，不重复执行；显示栈崩溃重启代数加 1，再执行一次；停机再启动是新的 orbd，代数从头算。
// 「工作区已就绪」指本进程里 runSetupLocked 返回过（setup.run 或 resume.run，成功失败都算，见假设 A5）。

// resumeScriptWait 是 orbd 等 resume 脚本退出的上限；到期只记「仍在运行」，不杀进程（脚本可能拉起了常驻应用）。
const resumeScriptWait = 60 * time.Second

// workspaceReady 由 runSetupLocked 在返回前调用：记下工作目录并标记工作区已就绪；显示已就绪时立即执行 resume 脚本。
// 不等脚本退出。
func (d *desktop) workspaceReady(workdir string) {
	d.mu.Lock()
	d.wsReady = true
	d.wsDir = workdir
	d.mu.Unlock()
	d.maybeResume()
}

// maybeResume 在显示已就绪、工作区已就绪且当前显示代数还没执行过时启动 resume 脚本。显示就绪与 workspaceReady
// 两处都调用，谁后到谁触发；代数在 mu 下认领，同一代数只执行一次。只等到进程启动，不等它退出。
func (d *desktop) maybeResume() {
	d.mu.Lock()
	if !d.displayReady || !d.wsReady || d.displayGen <= d.resumedGen {
		d.mu.Unlock()
		return
	}
	d.resumedGen = d.displayGen
	gen, workdir := d.displayGen, d.wsDir
	d.mu.Unlock()
	d.startResume(gen, workdir)
}

// resumeScript 按顺序返回第一个存在且可执行的 resume 脚本及其工作目录：`<workdir>/.oryn/desktop/resume`
// （在 workdir 下执行）、`$HOME/.oryn/desktop/resume`（在 $HOME 下执行）。都没有时 ok 为 false。
func (d *desktop) resumeScript(workdir string) (path, dir string, ok bool) {
	type cand struct{ path, dir string }
	var cands []cand
	if workdir != "" {
		cands = append(cands, cand{filepath.Join(workdir, ".oryn", "desktop", "resume"), workdir})
	}
	cands = append(cands, cand{filepath.Join(d.dir(), "resume"), d.r.home})
	for _, c := range cands {
		st, err := os.Stat(c.path)
		switch {
		case err != nil:
			if !errors.Is(err, os.ErrNotExist) {
				d.log.Warn("stat desktop resume script", "path", c.path, "err", err)
			}
		case !st.Mode().IsRegular():
			d.log.Warn("desktop resume script is not a regular file; skipped", "path", c.path)
		case st.Mode()&0o111 == 0:
			d.log.Warn("desktop resume script is not executable; skipped", "path", c.path)
		default:
			return c.path, c.dir, true
		}
	}
	return "", "", false
}

// startResume 以独立进程组启动第 gen 代显示的 resume 脚本，环境用 baseEnv，输出追加到桌面日志 resume.log。
// 子进程直接继承日志文件（不经管道），它拉起的常驻应用在脚本退出后也能照常写日志。退出由后台 goroutine 等待。
func (d *desktop) startResume(gen uint64, workdir string) {
	path, dir, ok := d.resumeScript(workdir)
	if !ok {
		return
	}
	lg := d.logFor("resume")
	_, _ = fmt.Fprintf(lg, "[orbd] display generation %d: running %s in %s\n", gen, path, dir) // 顺带建好日志目录
	out, err := os.OpenFile(d.logPath("resume"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		d.log.Warn("open desktop resume log failed", "err", err)
		return
	}
	defer func() { _ = out.Close() }() // 子进程已持有自己的副本
	cmd := exec.Command(path)
	cmd.Dir = dir
	cmd.Env = envList(d.r.baseEnv())
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(lg, "[orbd] start failed: %v\n", err)
		d.log.Warn("start desktop resume script failed", "path", path, "err", err)
		return
	}
	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_, _ = fmt.Fprintf(lg, "[orbd] resume exited: %v\n", err)
		exited <- err
	}()
	go d.watchResume(path, exited)
}

// watchResume 至多等 resumeScriptWait：退出码非 0 记警告，到期仍未退出只记「仍在运行」；运行时结束时不再等。
func (d *desktop) watchResume(path string, exited <-chan error) {
	t := time.NewTimer(resumeScriptWait)
	defer t.Stop()
	select {
	case err := <-exited:
		if err != nil {
			d.log.Warn("desktop resume script failed", "path", path, "err", err)
		} else {
			d.log.Info("desktop resume script finished", "path", path)
		}
	case <-t.C:
		d.log.Info("desktop resume script still running; leaving it alone", "path", path, "after", resumeScriptWait)
	case <-d.r.ctx.Done():
	}
}
