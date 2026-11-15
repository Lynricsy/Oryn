package orbdrt

import (
	"bytes"
	"context"
	"errors"
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
	// defaultDockerdReadyTimeout 是 setup / resume 等 dockerd API 就绪的缺省上限（Config.DockerdReadyTimeout）。
	defaultDockerdReadyTimeout = 2 * time.Minute
	// defaultDockerdProbeInterval 是 `docker info` 探测的缺省起始间隔；失败后翻倍，上限 dockerdProbeMaxInterval。
	defaultDockerdProbeInterval = 500 * time.Millisecond
	dockerdProbeMaxInterval     = 5 * time.Second
	// dockerdProbeTimeout 是单次 `docker info` 的上限。
	dockerdProbeTimeout = 10 * time.Second
)

// dockerdPidFiles 是快照里可能带着的旧 pid 文件：pid 被复用时 dockerd / containerd 会以为已有实例在跑而拒绝启动。
var dockerdPidFiles = []string{"/var/run/docker.pid", "/run/docker/containerd/containerd.pid"}

// dockerd 监督 orb 内的 dockerd：退出后按退避重启（1s 起翻倍、上限 30s），Shutdown 时 TERM 并有界等待。
// 进程启动后以 `docker info` 探测 API，可用时发布「就绪」（OW-1）；Docker 类型的 setup / resume 先等它。
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

	// readyMu 保护 ready 与 probeErr：ready 在当前 dockerd 进程的 API 可用时关闭；进程退出后换成新的未关闭通道
	// （只在已关闭时替换，所以等待者手里的未关闭通道始终就是当前那条）。probeErr 是最近一次探测失败的原因。
	readyMu  sync.Mutex
	ready    chan struct{}
	probeErr string
}

func newDockerd(r *Runtime) *dockerd {
	b := r.cfg.DockerdBackoff
	if b == nil {
		b = defaultDesktopBackoff // 与显示栈同一退避表：1s/2s/4s/8s/16s/30s
	}
	return &dockerd{
		r: r, log: r.log.With("sub", "dockerd"), backoff: b, stopCh: make(chan struct{}),
		ready: make(chan struct{}), probeErr: "dockerd has not started yet",
	}
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

// runOnce 清掉旧 pid 文件、启动一次 dockerd，探测其 API 并等它退出。
func (d *dockerd) runOnce(ctx context.Context) {
	if err := os.MkdirAll(filepath.Dir(d.logPath()), 0o755); err != nil {
		d.log.Warn("dockerd log dir", "err", err)
		d.setProbeErr(err)
		return
	}
	lg, err := os.OpenFile(d.logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		d.log.Warn("open dockerd log", "err", err)
		d.setProbeErr(err)
		return
	}
	env := envList(d.r.baseEnv())
	rm := d.r.sudoCommand(ctx, "rm", append([]string{"-f", "--"}, dockerdPidFiles...)...)
	rm.Env, rm.Stdout, rm.Stderr = env, lg, lg
	if err := rm.Run(); err != nil {
		_, _ = fmt.Fprintf(lg, "[orbd] remove stale pid files: %v\n", err)
	}

	cmd := d.r.sudoCommand(context.Background(), "dockerd")
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
		d.setProbeErr(fmt.Errorf("start dockerd: %w", err))
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
	// 探测在进程退出（或 ctx 结束）时取消；等探测 goroutine 结束后才撤下就绪，免得已退出的进程被标成就绪。
	pctx, pcancel := context.WithCancel(ctx)
	probed := make(chan struct{})
	go func() {
		defer close(probed)
		d.probe(pctx)
	}()
	defer func() {
		pcancel()
		<-probed
		d.markDown()
	}()
	select {
	case <-exited:
	case <-ctx.Done():
		d.terminate(exited, dockerdStopGrace)
	}
}

// probe 反复执行 `docker info`（每次至多 dockerdProbeTimeout，间隔从 DockerdProbeInterval 翻倍到 5s），
// 成功即发布就绪；ctx 结束（进程退出、关机）时放弃。
func (d *dockerd) probe(ctx context.Context) {
	wait := d.r.cfg.DockerdProbeInterval
	env := envList(d.r.baseEnv())
	for {
		pctx, cancel := context.WithTimeout(ctx, dockerdProbeTimeout)
		_, err := d.r.dockerCLI(pctx, env, "info", "--format", "{{.ServerVersion}}")
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			d.markReady()
			return
		}
		d.setProbeErr(err)
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
		wait = min(wait*2, dockerdProbeMaxInterval)
	}
}

func (d *dockerd) setProbeErr(err error) {
	d.readyMu.Lock()
	d.probeErr = strings.TrimSpace(err.Error())
	d.readyMu.Unlock()
}

func (d *dockerd) markReady() {
	d.readyMu.Lock()
	defer d.readyMu.Unlock()
	select {
	case <-d.ready:
	default:
		close(d.ready)
		d.log.Info("dockerd API ready")
	}
}

// markDown 撤下就绪（进程已退出）：已关闭的通道换成新的。
func (d *dockerd) markDown() {
	d.readyMu.Lock()
	defer d.readyMu.Unlock()
	select {
	case <-d.ready:
		d.ready = make(chan struct{})
	default:
	}
	d.probeErr = "dockerd exited"
}

// waitReady 等 dockerd API 就绪（OW-1）：已就绪立即返回；否则先调 onWait（可为 nil），至多等
// DockerdReadyTimeout。ctx 结束或监督停止（Shutdown）时立即返回错误；超时的错误带最近一次探测失败的原因。
func (d *dockerd) waitReady(ctx context.Context, onWait func()) error {
	d.readyMu.Lock()
	ch := d.ready
	d.readyMu.Unlock()
	select {
	case <-ch:
		return nil
	default:
	}
	if onWait != nil {
		onWait()
	}
	if f := d.r.cfg.OnDockerdWait; f != nil {
		f()
	}
	timeout := d.r.cfg.DockerdReadyTimeout
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("dockerd not ready: %w", ctx.Err())
	case <-d.stopCh:
		return errors.New("dockerd not ready: orbd is shutting down")
	case <-t.C:
		d.readyMu.Lock()
		last := d.probeErr
		d.readyMu.Unlock()
		return fmt.Errorf("dockerd not ready after %s (last check: %s); see ~/.cache/oryn/logs/dockerd.log", timeout, last)
	}
}

// waitDockerd 让 Docker 类型 orb 的 setup / resume 脚本先等 dockerd 就绪；未启用嵌套 Docker 时直接返回。
func (r *Runtime) waitDockerd(ctx context.Context, lg *setupLogger, phase apigen.LinkCtlSetupLogParamsPhase) error {
	if r.dockerd == nil {
		return nil
	}
	err := r.dockerd.waitReady(ctx, func() {
		lg.system(phase, "waiting for the Docker daemon (dockerd) to accept requests")
	})
	if err != nil {
		lg.system(phase, err.Error())
	}
	return err
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
	cmd := d.r.sudoCommand(ctx, "pkill", "-"+sig, "-x", "dockerd")
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
	out, err := r.dockerCLI(ctx, env, "ps", "-q")
	if err != nil {
		return apigen.LinkCtlSnapshotPrepareResult{}, ctlErr("failed", "docker ps: %v", err)
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return apigen.LinkCtlSnapshotPrepareResult{Ok: true}, nil
	}
	r.log.Info("stopping inner containers before the project snapshot", "count", len(ids))
	if _, err := r.dockerCLI(ctx, env, append([]string{"stop", "-t", "10"}, ids...)...); err != nil {
		return apigen.LinkCtlSnapshotPrepareResult{}, ctlErr("failed", "docker stop: %v", err)
	}
	return apigen.LinkCtlSnapshotPrepareResult{Ok: true}, nil
}

// dockerCLI 以 user 身份执行 docker（user 在 docker 组），返回 stdout；失败时错误里带 stderr。
func (r *Runtime) dockerCLI(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := r.bins.command(ctx, "docker", args...)
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

// sudoCommand 以 `sudo -n <prog 的绝对路径> args…` 执行 prog：sudo 与 prog 都在可信目录里解析。
func (r *Runtime) sudoCommand(ctx context.Context, prog string, args ...string) *exec.Cmd {
	path, err := r.bins.look(prog)
	cmd := r.bins.command(ctx, "sudo", append([]string{"-n", path}, args...)...)
	if err != nil && cmd.Err == nil {
		cmd.Err = err
	}
	return cmd
}
