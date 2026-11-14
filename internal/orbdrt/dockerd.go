package orbdrt

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 嵌套 Docker（M11a 4.3）：orb 由带 Docker 的类型（sysbox 运行时）创建时，provider 注入 ORYN_NESTED_DOCKER=1，
// orbd 以 root（sudo -n）监督 dockerd。镜像里装着 Docker 但不自启动，其它类型的 orb 里 docker 命令连不上守护进程。

const (
	// dockerdStopGrace 是关机时 TERM 之后等待 dockerd 退出的上限（它会先停掉内层容器）。
	dockerdStopGrace = 15 * time.Second
	// dockerdKillWait 是 KILL 之后再等的时长；仍不退出就放弃等待（不阻塞 orbd 退出）。
	dockerdKillWait = 5 * time.Second
	// dockerdStableAfter：运行超过这么久才退出的不计入连续失败。
	dockerdStableAfter = 60 * time.Second
	// snapshotPrepareTimeout 是 snapshot.prepare（列出并停止内层容器）的总上限。
	snapshotPrepareTimeout = 2 * time.Minute
)

// dockerdPidFiles 是快照里可能带着的旧 pid 文件：pid 被复用时 dockerd / containerd 会以为已有实例在跑而拒绝启动。
var dockerdPidFiles = []string{"/var/run/docker.pid", "/run/docker/containerd/containerd.pid"}

// dockerd 监督 orb 内的 dockerd：退出后按退避重启（1s 起翻倍、上限 30s），Shutdown 时 TERM 并有界等待。
type dockerd struct {
	r       *Runtime
	log     *slog.Logger
	backoff func(int) time.Duration

	// mu 让「检查 stopCh 后启动进程、登记 exited」与 stop 读取 exited 互斥，stop 之后不会再启动新进程。
	mu sync.Mutex
	// exited 在当前 dockerd 进程退出时关闭；从未启动时为 nil。
	exited   chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once
}

func newDockerd(r *Runtime) *dockerd {
	b := r.cfg.DockerdBackoff
	if b == nil {
		b = defaultDesktopBackoff // 与显示栈同一退避表：1s/2s/4s/8s/16s/30s
	}
	return &dockerd{r: r, log: r.log.With("sub", "dockerd"), backoff: b, stopCh: make(chan struct{})}
}

func (d *dockerd) stopRequested() bool {
	select {
	case <-d.stopCh:
		return true
	default:
		return false
	}
}

func (d *dockerd) logPath() string {
	return filepath.Join(d.r.home, ".cache", "oryn", "logs", "dockerd.log")
}

// run 监督 dockerd 直到 ctx 结束或 stop。ctx 结束而未先 stop 时照常 TERM 并有界等待。
func (d *dockerd) run(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil && !d.stopRequested() {
		start := time.Now()
		d.runOnce(ctx)
		if ctx.Err() != nil || d.stopRequested() {
			return
		}
		if time.Since(start) >= dockerdStableAfter {
			failures = 0
		}
		failures++
		wait := d.backoff(failures)
		d.log.Warn("dockerd exited; restarting", "in", wait, "failures", failures)
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		case <-d.stopCh:
			t.Stop()
			return
		}
	}
}

// runOnce 清掉旧 pid 文件、启动一次 dockerd 并等它退出。
func (d *dockerd) runOnce(ctx context.Context) {
	if err := os.MkdirAll(filepath.Dir(d.logPath()), 0o755); err != nil {
		d.log.Warn("dockerd log dir", "err", err)
		return
	}
	lg, err := os.OpenFile(d.logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		d.log.Warn("open dockerd log", "err", err)
		return
	}
	env := envList(d.r.baseEnv())
	rm := exec.CommandContext(ctx, "sudo", append([]string{"-n", "rm", "-f", "--"}, dockerdPidFiles...)...)
	rm.Env, rm.Stdout, rm.Stderr = env, lg, lg
	if err := rm.Run(); err != nil {
		_, _ = fmt.Fprintf(lg, "[orbd] remove stale pid files: %v\n", err)
	}

	cmd := exec.Command("sudo", "-n", "dockerd")
	cmd.Env, cmd.Stdout, cmd.Stderr = env, lg, lg
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	d.mu.Lock()
	if d.stopRequested() {
		d.mu.Unlock()
		_ = lg.Close()
		return
	}
	if err := cmd.Start(); err != nil {
		d.mu.Unlock()
		_, _ = fmt.Fprintf(lg, "[orbd] start dockerd: %v\n", err)
		_ = lg.Close()
		d.log.Warn("start dockerd", "err", err)
		return
	}
	exited := make(chan struct{})
	d.exited = exited
	d.mu.Unlock()
	_, _ = fmt.Fprintf(lg, "[orbd] dockerd started (pid %d)\n", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		_, _ = fmt.Fprintf(lg, "[orbd] dockerd exited: %v\n", err)
		_ = lg.Close()
		close(exited)
	}()
	select {
	case <-exited:
	case <-ctx.Done():
		d.terminate(exited, dockerdStopGrace)
	}
}

// stop 停止监督并让 dockerd 退出（TERM，至多等 dockerdStopGrace，之后 KILL）。可重复调用。
func (d *dockerd) stop() {
	d.stopOnce.Do(func() { close(d.stopCh) })
	d.mu.Lock()
	exited := d.exited
	d.mu.Unlock()
	if exited == nil {
		return
	}
	select {
	case <-exited:
		return
	default:
	}
	d.terminate(exited, dockerdStopGrace)
}

// terminate 向 dockerd 发 TERM，grace 内未退出则 KILL 再等 dockerdKillWait。dockerd 以 root 运行，
// orbd（user）不能直接给它发信号，经 sudo pkill。
func (d *dockerd) terminate(exited <-chan struct{}, grace time.Duration) {
	d.signal("TERM")
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-exited:
		return
	case <-t.C:
	}
	d.log.Warn("dockerd did not exit after TERM; killing", "grace", grace)
	d.signal("KILL")
	k := time.NewTimer(dockerdKillWait)
	defer k.Stop()
	select {
	case <-exited:
	case <-k.C:
		d.log.Warn("dockerd still running after KILL; giving up waiting")
	}
}

func (d *dockerd) signal(sig string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", "-n", "pkill", "-"+sig, "-x", "dockerd")
	cmd.Env = envList(d.r.baseEnv())
	if out, err := cmd.CombinedOutput(); err != nil {
		// pkill 没匹配到进程时退出码为 1（已经退出），不算错误。
		d.log.Debug("pkill dockerd", "sig", sig, "err", err, "out", strings.TrimSpace(string(out)))
	}
}

// snapshotPrepare 实现 ctl 方法 snapshot.prepare（M11a 4.3）：项目快照发布前停掉运行中的内层容器——
// sysbox 的 docker commit 只在没有运行中的内层容器时才带上内层镜像。未启用嵌套 Docker 时直接成功。
func (r *Runtime) snapshotPrepare(ctx context.Context) (apigen.LinkCtlSnapshotPrepareResult, error) {
	if r.dockerd == nil {
		return apigen.LinkCtlSnapshotPrepareResult{Ok: true}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotPrepareTimeout)
	defer cancel()
	env := envList(r.baseEnv())
	out, err := dockerCLI(ctx, env, "ps", "-q")
	if err != nil {
		return apigen.LinkCtlSnapshotPrepareResult{}, ctlErr("failed", "docker ps: %v", err)
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return apigen.LinkCtlSnapshotPrepareResult{Ok: true}, nil
	}
	r.log.Info("stopping inner containers before the project snapshot", "count", len(ids))
	if _, err := dockerCLI(ctx, env, append([]string{"stop", "-t", "10"}, ids...)...); err != nil {
		return apigen.LinkCtlSnapshotPrepareResult{}, ctlErr("failed", "docker stop: %v", err)
	}
	return apigen.LinkCtlSnapshotPrepareResult{Ok: true}, nil
}

// dockerCLI 以 user 身份执行 docker（user 在 docker 组），返回 stdout；失败时错误里带 stderr。
func dockerCLI(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return "", fmt.Errorf("%w: %s", err, s)
		}
		return "", err
	}
	return string(out), nil
}
