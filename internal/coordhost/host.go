// Package coordhost 是 M9 的协调宿主（协调宿主分册 H5）：orynd 进程内认领内置 provider「@host」的任务，
// 为协调 orb 拉起受沙箱约束的子进程 `orynd coordinator-host`（受限的 orbd 运行时），经继承的 socketpair
// 交给 orbs.ServeConn 建立 link，并监督它的退出。
//
// 宿主上的冻结就是停进程：pause（freeze / stop）都结束子进程，会话文件留在 <dir>/home，唤醒后接续。
package coordhost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/core"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/sandbox"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/version"
	"golang.org/x/sys/unix"
)

// ProviderName 是内置 provider 的名称（与 orbs.HostProviderName 同值；API 的名称正则建不出它）。
const ProviderName = "@host"

// CodeUnavailable 是协调功能不可用时的错误码。
const CodeUnavailable = "coordinator_unavailable"

const (
	defaultStopGrace  = 25 * time.Second
	defaultEgressPort = 7811
	leaseInterval     = 15 * time.Second
	leaseTTL          = 45 * time.Second
	claimWait         = 30 * time.Second
	statusTimeout     = 10 * time.Second
	hostLogMax        = 5 << 20
	hostLogTail       = 2 << 10
	crashWindow       = 5 * time.Minute
	crashLimit        = 3
	shutdownGrace     = 5 * time.Second
	instanceID        = "orynd"
)

// Config 是协调宿主配置（来自 orynd.yaml 的 coordinator 段）。
type Config struct {
	Dir, OmpPath, User string
	EgressPort         int
	VerifyOmp          bool
	// DataDir / ConfigPath 只用于自检的 --deny-read。
	DataDir, ConfigPath string
	// Command 是子进程命令行前缀，默认 {os.Executable(), "coordinator-host"}。
	Command []string
	// StopGrace 是 pause / delete 时 SIGTERM 后等待的时长，默认 25s。
	StopGrace         time.Duration
	Version, LogLevel string
}

// Orbs 是协调宿主需要的 orb 服务（*orbs.Service）。
type Orbs interface {
	ServeConn(ctx context.Context, orbID string, conn net.Conn) error
	Pause(ctx context.Context, orbID, mode string) error
	Wake(ctx context.Context, orbID string) error
}

// Error 是带错误码的协调宿主错误（Check 返回 Code = coordinator_unavailable）。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func unavailable(format string, a ...any) *Error {
	return &Error{Code: CodeUnavailable, Message: fmt.Sprintf(format, a...)}
}

// Host 是协调宿主。
type Host struct {
	cfg Config
	st  *store.Store
	q   *jobs.Queue
	o   Orbs
	log *slog.Logger

	probeOnce sync.Once
	useNS     bool
	abi       int
	probeErr  error

	ompMu   sync.Mutex
	ompKey  string // 上次校验通过的 omp 的「大小:mtime」
	ompHash string

	mu         sync.Mutex
	runCtx     context.Context
	providerID string
	children   map[string]*child
	crashes    map[string][]time.Time
	paused     map[string]chan struct{} // 等待某 orb 的 pause / delete 任务执行完（意外退出后的 Pause → Wake）
	report     *sandbox.Report

	// noNS 仅供测试：跳过命名空间探测，固定为端口限制模式（模拟建不了命名空间的环境）。
	noNS bool
	// onRecovered 仅供测试：意外退出的善后（Pause、可能的 Wake）结束时调用。
	onRecovered func(orbID string, woke bool)
}

// child 是一个存活（或刚退出）的 coordinator-host 子进程。
type child struct {
	orbID    string
	pid      int
	useNS    bool
	done     chan struct{} // Wait 返回后关闭
	linkDone chan struct{} // ServeConn 返回后关闭
	expected bool          // 由 pause / delete / 启动失败引起的退出（受 Host.mu 保护）
}

func (c *child) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *child) linkUp() bool {
	select {
	case <-c.linkDone:
		return false
	default:
		return true
	}
}

// New 创建协调宿主；Run 之前不做任何事。
func New(cfg Config, st *store.Store, q *jobs.Queue, o Orbs) *Host {
	if len(cfg.Command) == 0 {
		exe, err := os.Executable()
		if err != nil {
			exe = os.Args[0]
		}
		cfg.Command = []string{exe, "coordinator-host"}
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = defaultStopGrace
	}
	if cfg.EgressPort <= 0 {
		cfg.EgressPort = defaultEgressPort
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return &Host{
		cfg:      cfg,
		st:       st,
		q:        q,
		o:        o,
		log:      slog.Default().With("component", "coordhost"),
		children: map[string]*child{},
		crashes:  map[string][]time.Time{},
		paused:   map[string]chan struct{}{},
	}
}

// ---- 可用性 ----

// probe 探测一次（Check / Status / spawn 共用）：能否建网络命名空间并拉起回环，以及 Landlock ABI。
func (h *Host) probe() {
	h.probeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
		defer cancel()
		if h.noNS {
			// 测试：直接走下面的不带命名空间的探测。
		} else if res, err := h.runProbe(ctx, true); err == nil && res.Loopback {
			h.useNS, h.abi = true, res.LandlockABI
			return
		} else if err != nil {
			h.log.Info("network namespace unavailable; falling back to landlock port rules", "err", err)
		}
		res, err := h.runProbe(ctx, false)
		if err != nil {
			h.probeErr = err
			return
		}
		h.abi = res.LandlockABI
	})
}

func (h *Host) runProbe(ctx context.Context, ns bool) (probeResult, error) {
	args := append(append([]string{}, h.cfg.Command[1:]...), "--probe")
	if ns {
		args = append(args, "--netns")
	}
	cmd := exec.CommandContext(ctx, h.cfg.Command[0], args...)
	cmd.Env = childEnv()
	cmd.SysProcAttr = sysProcAttr(ns)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return probeResult{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var res probeResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil {
		return probeResult{}, fmt.Errorf("probe output %q: %w", out, err)
	}
	return res, nil
}

// childEnv 是子进程的环境（从零构造，不继承 orynd 的任何变量）。
func childEnv() []string { return []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"} }

// sysProcAttr 返回子进程的属性：总是独立进程组 + 父进程死亡即 SIGKILL；ns 为真时建网络 / PID / IPC / UTS
// 命名空间（非 root 时另建 user namespace，把自己映射为其中的 root）。
func sysProcAttr(ns bool) *syscall.SysProcAttr {
	a := &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if !ns {
		return a
	}
	a.Cloneflags = syscall.CLONE_NEWNET | syscall.CLONE_NEWPID | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
	if os.Geteuid() != 0 {
		a.Cloneflags |= syscall.CLONE_NEWUSER
		a.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}}
		a.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}}
		a.GidMappingsEnableSetgroups = false
	}
	return a
}

// archKey 是 core.Pin() 里本机架构的键。
func archKey() string {
	switch runtime.GOARCH {
	case "amd64":
		return "linux-x64"
	default:
		return "linux-" + runtime.GOARCH
	}
}

// Check 报告协调功能是否可用；不可用时返回 *Error{Code: coordinator_unavailable}，Message 为第一条不满足的原因。
func (h *Host) Check(_ context.Context) error {
	st, err := os.Stat(h.cfg.OmpPath)
	if err != nil {
		return unavailable("omp not found at %s: %v (install it with: install -D -m 0755 build/core/x64/omp %s)", h.cfg.OmpPath, err, h.cfg.OmpPath)
	}
	if !st.Mode().IsRegular() {
		return unavailable("omp at %s is not a regular file", h.cfg.OmpPath)
	}
	if h.cfg.VerifyOmp {
		if err := h.verifyOmp(st); err != nil {
			return err
		}
	} else if f, err := os.Open(h.cfg.OmpPath); err != nil {
		return unavailable("omp at %s is not readable: %v", h.cfg.OmpPath, err)
	} else {
		_ = f.Close()
	}
	h.probe()
	if h.abi < sandbox.MinABI {
		reason := fmt.Sprintf("landlock unavailable (abi %d): kernel >= 5.19 with the landlock LSM enabled is required", h.abi)
		if h.probeErr != nil {
			reason += fmt.Sprintf(" (probe failed: %v)", h.probeErr)
		}
		return unavailable("%s", reason)
	}
	if !h.useNS && h.abi < 4 {
		return unavailable("network isolation unavailable: cannot create a network namespace and landlock abi %d < 4 (kernel >= 6.7 required)", h.abi)
	}
	if os.Geteuid() == 0 {
		if _, _, err := h.target(); err != nil {
			return unavailable("%v", err)
		}
		if err := reachable(h.cfg.Dir, h.cfg.User); err != nil {
			return unavailable("%v", err)
		}
	}
	return nil
}

// verifyOmp 校验 omp 的 sha256 与 core/source.json 一致；大小与 mtime 不变时复用上次的结果。
func (h *Host) verifyOmp(st os.FileInfo) error {
	key := fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
	want := core.Pin()[archKey()]
	h.ompMu.Lock()
	defer h.ompMu.Unlock()
	if h.ompKey != key {
		f, err := os.Open(h.cfg.OmpPath)
		if err != nil {
			return unavailable("omp at %s is not readable: %v", h.cfg.OmpPath, err)
		}
		sum := sha256.New()
		_, err = io.Copy(sum, f)
		_ = f.Close()
		if err != nil {
			return unavailable("read omp at %s: %v", h.cfg.OmpPath, err)
		}
		h.ompKey, h.ompHash = key, hex.EncodeToString(sum.Sum(nil))
	}
	if want == "" {
		return unavailable("core/source.json has no sha256 for %s", archKey())
	}
	if !strings.EqualFold(h.ompHash, want) {
		return unavailable("omp at %s does not match core/source.json (want %s, got %s)", h.cfg.OmpPath, want, h.ompHash)
	}
	return nil
}

// target 返回降权目标：orynd 的 euid 为 0 时是 coordinator.user 的 uid/gid，否则 (-1, -1)（不切换用户）。
func (h *Host) target() (int, int, error) {
	if os.Geteuid() != 0 {
		return -1, -1, nil
	}
	u, err := user.Lookup(h.cfg.User)
	if err != nil {
		return 0, 0, fmt.Errorf("coordinator.user %q: %v", h.cfg.User, err)
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("coordinator.user %q has non-numeric ids %s/%s", h.cfg.User, u.Uid, u.Gid)
	}
	if uid == 0 {
		return 0, 0, fmt.Errorf("coordinator.user %q must not be root", h.cfg.User)
	}
	return uid, gid, nil
}

// reachable 检查 dir 本身（已存在时）与它的每一级父目录对其他用户可进入（other 的 x 位）。
func reachable(dir, who string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for p := abs; ; p = filepath.Dir(p) {
		if st, err := os.Stat(p); err == nil && st.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf("%s is not reachable by user %s (%s is %v): set coordinator.dir outside private directories", abs, who, p, st.Mode().Perm())
		}
		if p == "/" || p == filepath.Dir(p) {
			return nil
		}
	}
}

// Status 是 GET /api/v1/coordinator 的内容。
func (h *Host) Status(ctx context.Context) apigen.CoordinatorStatus {
	if err := h.Check(ctx); err != nil {
		return apigen.CoordinatorStatus{Available: false, Reason: new(err.Error())}
	}
	st := apigen.CoordinatorStatus{Available: true}
	h.mu.Lock()
	rep := h.report
	h.mu.Unlock()
	if rep != nil {
		network := apigen.SandboxNetworkPort
		if rep.NetNS {
			network = apigen.SandboxNetworkNamespace
		}
		st.Sandbox = &apigen.CoordinatorSandbox{
			LandlockAbi: rep.LandlockABI, Network: network, PidNamespace: rep.PIDNS,
			IpcScope: rep.IPCScope, UID: rep.UID, Seccomp: rep.Seccomp,
		}
	}
	if h.st != nil {
		if o, err := h.st.Q.GetCoordinatorOrb(ctx); err == nil && o.AgentVersion != nil && *o.AgentVersion != "" {
			st.OmpVersion = new(*o.AgentVersion)
		}
	}
	return st
}

// ---- 目录 ----

func (h *Host) homeDir() string     { return filepath.Join(h.cfg.Dir, "home") }
func (h *Host) ompCopy() string     { return filepath.Join(h.cfg.Dir, "bin", "omp") }
func (h *Host) hostLogPath() string { return filepath.Join(h.cfg.Dir, "host.log") }

// ensureDirs 准备 <dir>（0755，须属于 orynd 且不可被组与其他人写）、<dir>/bin/omp（omp 的 0555 副本，
// 源的大小或 mtime 变了才重拷）、<dir>/home（0700，root 部署时递归交给目标用户）并轮转 host.log。
func (h *Host) ensureDirs(uid, gid int) error {
	dir := h.cfg.Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("coordinator.dir %s is not a directory", dir)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Geteuid() {
		return fmt.Errorf("coordinator.dir %s is owned by uid %d, not by orynd (uid %d)", dir, sys.Uid, os.Geteuid())
	}
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("coordinator.dir %s is writable by group or others (%v)", dir, st.Mode().Perm())
	}
	if err := os.MkdirAll(filepath.Dir(h.ompCopy()), 0o755); err != nil {
		return err
	}
	if err := copyIfChanged(h.cfg.OmpPath, h.ompCopy()); err != nil {
		return fmt.Errorf("copy omp: %w", err)
	}
	if err := prepareHome(h.homeDir(), uid, gid); err != nil {
		return err
	}
	if st, err := os.Stat(h.hostLogPath()); err == nil && st.Size() > hostLogMax {
		_ = os.Rename(h.hostLogPath(), h.hostLogPath()+".1")
	}
	return nil
}

// prepareHome 建（或修正）一个 0700 的目录；uid ≥ 0 时把整棵树交给 uid/gid。
func prepareHome(dir string, uid, gid int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if uid < 0 {
		return nil
	}
	return filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}

func copyIfChanged(src, dst string) error {
	ss, err := os.Stat(src)
	if err != nil {
		return err
	}
	if ds, err := os.Stat(dst); err == nil && ds.Size() == ss.Size() && ds.ModTime().Equal(ss.ModTime()) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".omp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o555); err != nil {
		return err
	}
	if err := os.Chtimes(tmp.Name(), ss.ModTime(), ss.ModTime()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// logTail 返回 host.log 末尾至多 n 字节。
func (h *Host) logTail(n int64) string {
	f, err := os.Open(h.hostLogPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(st.Size()-n, 0)
	b := make([]byte, st.Size()-off)
	_, _ = f.ReadAt(b, off)
	return strings.TrimSpace(string(b))
}

// childArgs 是子进程的命令行（Command 之后的部分）。
func (h *Host) childArgs(uid, gid int, ns bool, extra ...string) []string {
	args := append([]string{}, h.cfg.Command[1:]...)
	args = append(args,
		"--omp", h.ompCopy(),
		"--egress-addr", net.JoinHostPort("127.0.0.1", strconv.Itoa(h.cfg.EgressPort)),
		"--log-level", h.cfg.LogLevel,
	)
	if uid >= 0 {
		args = append(args, "--uid", strconv.Itoa(uid), "--gid", strconv.Itoa(gid))
	}
	if ns {
		args = append(args, "--netns", "--pidns")
	}
	return append(args, extra...)
}

// prepare 做 spawn 与自检共用的前置：可用性检查、降权目标、目录。
func (h *Host) prepare(ctx context.Context) (uid, gid int, err error) {
	if err := h.Check(ctx); err != nil {
		return 0, 0, err
	}
	if uid, gid, err = h.target(); err != nil {
		return 0, 0, err
	}
	if err := h.ensureDirs(uid, gid); err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// ---- 运行 ----

// Run 注册 @host provider、每 15 秒续租、单 goroutine 认领并执行任务；ctx 结束时停掉全部子进程。
func (h *Host) Run(ctx context.Context) {
	h.mu.Lock()
	h.runCtx = ctx
	h.mu.Unlock()
	defer h.stopAll()
	pid, err := h.register(ctx)
	if err != nil {
		h.log.Error("register host provider failed; coordinator host disabled", "err", err)
		return
	}
	go h.leaseLoop(ctx, pid)
	if err := h.Check(ctx); err != nil {
		h.log.Warn("coordinator unavailable", "reason", err.Error())
	}
	h.recover(ctx)
	for ctx.Err() == nil {
		job, ok, err := h.q.Claim(ctx, pid, claimWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			h.log.Warn("claim failed", "err", err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}
		if !ok {
			continue
		}
		start := time.Now()
		res := h.execute(ctx, job)
		res.DurationMs = time.Since(start).Milliseconds()
		if err := h.q.Complete(ctx, pid, job.ID, job.ClaimGen, res); err != nil && ctx.Err() == nil {
			h.log.Warn("complete job failed", "job", job.ID, "err", err)
		}
	}
}

func (h *Host) register(ctx context.Context) (string, error) {
	caps, _ := json.Marshal(map[string]bool{"freeze": true, "snapshot": false})
	types, _ := json.Marshal([]map[string]any{{"name": "host", "cpus": 0, "memoryMB": 0}})
	err := h.st.Q.UpsertHostProvider(ctx, db.UpsertHostProviderParams{
		ID:           ids.New("prov"),
		TokenHash:    ids.Hash(ids.Token("opv")), // 令牌随即丢弃：HTTP 的 provider 接口永远无法以它的身份认证
		Version:      h.cfg.Version,
		Capabilities: string(caps),
		OrbTypes:     string(types),
		CreatedAt:    time.Now().UnixMilli(),
	})
	if err != nil {
		return "", err
	}
	p, err := h.st.Q.GetProviderByName(ctx, ProviderName)
	if err != nil {
		return "", err
	}
	h.mu.Lock()
	h.providerID = p.ID
	h.mu.Unlock()
	h.renew(ctx, p.ID)
	return p.ID, nil
}

func (h *Host) renew(ctx context.Context, pid string) {
	inst, until := instanceID, time.Now().Add(leaseTTL).UnixMilli()
	if err := h.st.Q.SetProviderLease(ctx, db.SetProviderLeaseParams{ID: pid, InstanceID: &inst, LeaseUntil: &until}); err != nil && ctx.Err() == nil {
		h.log.Warn("renew host provider lease failed", "err", err)
	}
}

func (h *Host) leaseLoop(ctx context.Context, pid string) {
	t := time.NewTicker(leaseInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			h.renew(ctx, pid)
		case <-ctx.Done():
			return
		}
	}
}

// recover 处理 orynd 启动时的协调 orb：ready → 停机（子进程已随上一个 orynd 消失，下一条消息经 resume 回来）；
// connecting / setting_up / resuming → 直接拉起子进程。其余状态交给 orbs 补发的任务。
func (h *Host) recover(ctx context.Context) {
	o, err := h.st.Q.GetCoordinatorOrb(ctx)
	if err != nil {
		return
	}
	switch o.State {
	case "ready":
		if err := h.o.Pause(ctx, o.ID, "stop"); err != nil {
			h.log.Warn("stop stale coordinator orb", "orb", o.ID, "err", err)
		}
	case "connecting", "setting_up", "resuming":
		if err := h.spawn(ctx, o.ID); err != nil {
			h.log.Warn("respawn coordinator host", "orb", o.ID, "err", err)
		}
	}
}

func (h *Host) execute(ctx context.Context, job db.Job) jobs.Result {
	orbID := ""
	if job.OrbID != nil {
		orbID = *job.OrbID
	}
	var err error
	switch job.Op {
	case "spawn":
		err = h.spawn(ctx, orbID)
	case "pause":
		// freeze 与 stop 相同：宿主上的冻结就是停进程（内存即刻释放，会话文件留在 home）。
		err = h.stop(orbID)
		h.notePaused(orbID)
	case "delete":
		if err = h.stop(orbID); err == nil {
			err = os.RemoveAll(h.homeDir())
		}
		h.notePaused(orbID)
	case "snapshot", "delete_snapshot":
		err = errors.New("host provider does not support snapshots")
	default:
		err = fmt.Errorf("unknown op %q", job.Op)
	}
	if err != nil {
		h.log.Warn("job failed", "op", job.Op, "orb", orbID, "err", err)
		return jobs.Result{OK: false, Error: err.Error(), Output: err.Error()}
	}
	return jobs.Result{OK: true}
}

// spawn 确保该 orb 有一个存活且 link 未断的子进程。
func (h *Host) spawn(ctx context.Context, orbID string) error {
	if orbID == "" {
		return errors.New("spawn: job has no orb")
	}
	h.mu.Lock()
	c := h.children[orbID]
	h.mu.Unlock()
	if c != nil && c.alive() && c.linkUp() {
		return nil
	}
	if c != nil {
		_ = h.stop(orbID)
	}
	uid, gid, err := h.prepare(ctx)
	if err != nil {
		return err
	}
	return h.start(orbID, uid, gid)
}

// start 拉起子进程并等它报告沙箱生效（上限 10 秒）。
func (h *Host) start(orbID string, uid, gid int) error {
	h.mu.Lock()
	runCtx := h.runCtx
	h.mu.Unlock()
	if runCtx == nil {
		runCtx = context.Background()
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("socketpair: %w", err)
	}
	parentF, childF := os.NewFile(uintptr(fds[0]), "link-parent"), os.NewFile(uintptr(fds[1]), "link-child")
	pr, pw, err := os.Pipe()
	if err != nil {
		_ = parentF.Close()
		_ = childF.Close()
		return err
	}
	logF, err := os.OpenFile(h.hostLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = parentF.Close()
		_ = childF.Close()
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	useNS := h.useNS
	cmd := exec.Command(h.cfg.Command[0], h.childArgs(uid, gid, useNS, "--orb", orbID, "--home", h.homeDir())...)
	cmd.Env = childEnv()
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = logF, logF
	cmd.ExtraFiles = []*os.File{childF, pw}
	cmd.SysProcAttr = sysProcAttr(useNS)

	c := &child{orbID: orbID, useNS: useNS, done: make(chan struct{}), linkDone: make(chan struct{})}
	// 先登记再启动：子进程哪怕立刻退出，onExit 也能找到并移除它。
	h.mu.Lock()
	h.children[orbID] = c
	h.mu.Unlock()
	started := make(chan error, 1)
	// Pdeathsig 跟随的是创建子进程的线程：Start 与 Wait 在同一个锁定且不解锁的 goroutine 里，
	// 子进程退出前这个线程不会结束（goroutine 返回时被锁定的线程随之退出）。
	go func() {
		runtime.LockOSThread()
		if err := cmd.Start(); err != nil {
			started <- err
			close(c.done)
			return
		}
		c.pid = cmd.Process.Pid
		started <- nil
		_ = cmd.Wait()
		close(c.done)
		h.onExit(c)
	}()
	err = <-started
	_ = childF.Close()
	_ = pw.Close()
	_ = logF.Close()
	if err != nil {
		h.mu.Lock()
		if h.children[orbID] == c {
			delete(h.children, orbID)
		}
		h.mu.Unlock()
		_ = parentF.Close()
		_ = pr.Close()
		return fmt.Errorf("start coordinator host: %w", err)
	}

	conn, err := net.FileConn(parentF)
	_ = parentF.Close()
	if err != nil {
		close(c.linkDone)
		h.abort(c)
		_ = pr.Close()
		return fmt.Errorf("link conn: %w", err)
	}
	go func() {
		defer close(c.linkDone)
		if err := h.o.ServeConn(runCtx, orbID, conn); err != nil {
			h.log.Info("coordinator link ended", "orb", orbID, "err", err)
		}
	}()

	line := make(chan []byte, 1)
	go func() {
		defer pr.Close()
		b, _ := bufio.NewReader(pr).ReadBytes('\n')
		line <- b
	}()
	timer := time.NewTimer(statusTimeout)
	defer timer.Stop()
	var b []byte
	select {
	case b = <-line:
	case <-timer.C:
	}
	var rep sandbox.Report
	if len(b) == 0 || json.Unmarshal(b, &rep) != nil {
		h.abort(c)
		return fmt.Errorf("coordinator host did not report a sandbox: %s", h.logTail(hostLogTail))
	}
	if useNS && os.Geteuid() != 0 {
		// 子进程在只映射了 orynd 自己的 user namespace 里看到的是 uid 0；对外报告宿主上的真实 uid。
		rep.UID = os.Geteuid()
	}
	h.mu.Lock()
	h.report = &rep
	h.mu.Unlock()
	h.log.Info("coordinator host started", "orb", orbID, "pid", c.pid, "netns", rep.NetNS, "landlockAbi", rep.LandlockABI, "uid", rep.UID)
	return nil
}

// abort 杀掉一个启动失败的子进程（不算意外退出）。
func (h *Host) abort(c *child) {
	h.mu.Lock()
	c.expected = true
	if h.children[c.orbID] == c {
		delete(h.children, c.orbID)
	}
	h.mu.Unlock()
	kill(c, syscall.SIGKILL)
	<-c.done
}

func kill(c *child, sig syscall.Signal) {
	if c.pid <= 0 {
		return
	}
	_ = syscall.Kill(c.pid, sig)
	if !c.useNS || sig == syscall.SIGKILL {
		_ = syscall.Kill(-c.pid, sig)
	}
}

// stop 结束该 orb 的子进程：SIGTERM，等 StopGrace，再 SIGKILL（有 PID 命名空间时杀 1 号即全灭，否则杀进程组）。
func (h *Host) stop(orbID string) error {
	h.mu.Lock()
	c := h.children[orbID]
	if c != nil {
		c.expected = true
	}
	h.mu.Unlock()
	if c == nil || !c.alive() {
		return nil
	}
	kill(c, syscall.SIGTERM)
	t := time.NewTimer(h.cfg.StopGrace)
	defer t.Stop()
	select {
	case <-c.done:
		return nil
	case <-t.C:
	}
	kill(c, syscall.SIGKILL)
	select {
	case <-c.done:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("coordinator host %d did not exit after SIGKILL", c.pid)
	}
}

// stopAll 在 Run 结束时停掉全部子进程：SIGTERM、等 5 秒、SIGKILL。
func (h *Host) stopAll() {
	h.mu.Lock()
	var list []*child
	for _, c := range h.children {
		c.expected = true
		list = append(list, c)
	}
	h.mu.Unlock()
	for _, c := range list {
		kill(c, syscall.SIGTERM)
	}
	deadline := time.After(shutdownGrace)
	for _, c := range list {
		select {
		case <-c.done:
		case <-deadline:
			kill(c, syscall.SIGKILL)
			<-c.done
		}
	}
}

// onExit 在子进程退出后调用：不是 pause / delete / 启动失败引起的退出视为意外，停机并（5 分钟内不足 3 次时）唤醒。
func (h *Host) onExit(c *child) {
	h.mu.Lock()
	if h.children[c.orbID] == c {
		delete(h.children, c.orbID)
	}
	expected := c.expected
	ctx := h.runCtx
	var n int
	if !expected {
		now := time.Now()
		list := h.crashes[c.orbID][:0]
		for _, t := range h.crashes[c.orbID] {
			if now.Sub(t) < crashWindow {
				list = append(list, t)
			}
		}
		list = append(list, now)
		h.crashes[c.orbID] = list
		n = len(list)
	}
	h.mu.Unlock()
	if expected || ctx == nil || ctx.Err() != nil {
		return
	}
	h.log.Warn("coordinator host exited unexpectedly", "orb", c.orbID, "pid", c.pid, "crashes", n, "log", h.logTail(hostLogTail))
	go h.recoverCrash(ctx, c.orbID, n < crashLimit)
}

// recoverCrash 把意外退出的协调 orb 停机，wake 为真时等停机任务执行完再唤醒（让正在工作的协调线程重新同步）。
func (h *Host) recoverCrash(ctx context.Context, orbID string, wake bool) {
	woke := false
	defer func() {
		if h.onRecovered != nil {
			h.onRecovered(orbID, woke)
		}
	}()
	ch := h.pauseWaiter(orbID)
	if err := h.o.Pause(ctx, orbID, "stop"); err != nil {
		h.log.Warn("stop crashed coordinator orb", "orb", orbID, "err", err)
		h.dropPauseWaiter(orbID, ch)
		return
	}
	if !wake {
		h.dropPauseWaiter(orbID, ch)
		h.log.Warn("coordinator host crashed repeatedly; leaving the orb stopped until the next message", "orb", orbID)
		return
	}
	select {
	case <-ch:
	case <-time.After(h.cfg.StopGrace + 30*time.Second):
		h.dropPauseWaiter(orbID, ch)
	case <-ctx.Done():
		return
	}
	if err := h.o.Wake(ctx, orbID); err != nil {
		h.log.Warn("wake crashed coordinator orb", "orb", orbID, "err", err)
		return
	}
	woke = true
}

func (h *Host) pauseWaiter(orbID string) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan struct{})
	h.paused[orbID] = ch
	return ch
}

func (h *Host) dropPauseWaiter(orbID string, ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.paused[orbID] == ch {
		delete(h.paused, orbID)
	}
}

func (h *Host) notePaused(orbID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch := h.paused[orbID]; ch != nil {
		close(ch)
		delete(h.paused, orbID)
	}
}

// ConfigFrom 把 orynd.yaml 的 coordinator 段转成协调宿主配置（Command 取默认值；ConfigPath 只用于自检）。
func ConfigFrom(c config.Orynd, configPath string) Config {
	return Config{
		Dir:        c.CoordinatorDir(),
		OmpPath:    c.Coordinator.OmpPath,
		User:       c.Coordinator.User,
		EgressPort: c.Coordinator.EgressPort,
		VerifyOmp:  c.Coordinator.Verify(),
		DataDir:    c.DataDir,
		ConfigPath: configPath,
		Version:    version.Version,
		LogLevel:   c.LogLevel,
	}
}
