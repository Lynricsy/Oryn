// Package localgate 在控制面本机的沙箱里执行定时的运行前闸门：orynd 以 `orynd schedule-gate` 重新执行自己，子进程降权、
// 施加 Landlock + seccomp（internal/sandbox）后 exec `/bin/sh -c <命令>`。
//
// 沙箱里能看到的：只读的系统目录与可执行文件、一次性的可写目录（HOME / TMPDIR，含数据目录 gates/ 的副本作为工作目录）；
// TCP 只能连本进程开在回环上的正向代理（只放行公网地址，带一次性口令），读不到控制面的数据、配置与密钥，也连不上内网。
// 网络沙箱靠 Landlock 的 TCP 规则（ABI ≥ 4，Linux ≥ 6.7），不建网络命名空间；进程一律放进独立的 PID 命名空间，
// 命令结束或超时时连同全部后台进程（含 setsid 脱离进程组的）一起清理。建不了 PID 命名空间时本机闸门不可用。
package localgate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/headtail"
	"github.com/Lynricsy/Oryn/internal/sandbox"
)

// Command 是 orynd 里子进程的子命令名（`orynd schedule-gate …`）。
const Command = "schedule-gate"

// ExitSandbox 是子进程在 exec 命令之前任何一步失败时的退出码（原因写 stderr）。
const ExitSandbox = 70

const (
	// OutputHalf 是 stdout / stderr 各自保留的头尾字节数（与 SSH 闸门一致）。
	OutputHalf = 16 << 10
	// maxGatesCopy 是复制进沙箱的 gates/ 目录总大小上限。
	maxGatesCopy = 16 << 20
	// proxyEnv 是父进程交给子进程的代理地址（含口令）所在的环境变量；子进程据此设置 HTTP(S)_PROXY。
	proxyEnv = "ORYN_GATE_PROXY"
	// statusFD 是子进程施加沙箱成功、即将 exec 命令时写一行 "ok" 的管道（之后关闭，不带进命令）。
	statusFD = 3
	// waitDelay 是超时杀掉进程组之后等输出管道关闭的上限。
	waitDelay = 2 * time.Second
)

// Config 是本机闸门的参数。
type Config struct {
	// GatesDir 是数据目录里的 gates/：每次执行前复制进沙箱作为工作目录；不存在时工作目录为空。
	GatesDir string
	// TmpDir 是一次性工作目录的父目录；空 = os.TempDir()。
	TmpDir string
	// Command 是子进程命令行前缀，默认 {os.Executable(), Command}（测试用测试二进制）。
	Command []string
	// MaxABI 仅供测试：把子进程使用的 Landlock ABI 封顶（模拟旧内核）。
	MaxABI int
}

// Runner 执行本机闸门；并发安全。Close 之后不可再用。
type Runner struct {
	cfg   Config
	ln    net.Listener
	srv   *http.Server
	token string

	// pidnsOnce / pidnsErr 缓存「能否建 PID 命名空间」的探测结果（内核与权限在进程生命期内不变）。
	pidnsOnce sync.Once
	pidnsErr  error
}

// New 在回环上开正向代理并返回 Runner。
func New(cfg Config) (*Runner, error) {
	if len(cfg.Command) == 0 {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("localgate: %w", err)
		}
		cfg.Command = []string{exe, Command}
	}
	if cfg.TmpDir == "" {
		cfg.TmpDir = os.TempDir()
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("localgate: proxy listen: %w", err)
	}
	r := &Runner{cfg: cfg, ln: ln, token: hex.EncodeToString(b[:])}
	r.srv = &http.Server{Handler: egress.WebProxy(&egress.WebPolicy{}, r.token, "schedule-gate"), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := r.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("localgate: 代理退出", "err", err)
		}
	}()
	return r, nil
}

// Close 关掉正向代理。
func (r *Runner) Close() error { return r.srv.Close() }

// Available 报告本机能否施加闸门的沙箱；不能时返回原因（保存带本机闸门的定义时预检）。
func (r *Runner) Available() error {
	abi, err := sandbox.Probe()
	if err != nil {
		return fmt.Errorf("landlock unavailable: %w", err)
	}
	if r.cfg.MaxABI > 0 {
		abi = min(abi, r.cfg.MaxABI)
	}
	if abi < sandbox.TCPABI {
		return fmt.Errorf("landlock ABI %d < %d: local gates need Linux ≥ 6.7 to restrict TCP", abi, sandbox.TCPABI)
	}
	r.pidnsOnce.Do(func() {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		cmd.SysProcAttr = sysProcAttr()
		r.pidnsErr = cmd.Run()
	})
	if r.pidnsErr != nil {
		return fmt.Errorf("cannot create a PID namespace (needed to clean up background processes): %w", r.pidnsErr)
	}
	return nil
}

// Run 在沙箱里执行 command，分开收集 stdout 与 stderr。err 非空表示命令没有正常结束（沙箱施加失败、超时、执行失败），
// 此时 exitCode 无意义；命令以非 0 退出不算 err。label 只用于日志。
func (r *Runner) Run(ctx context.Context, command, label string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	if err := r.Available(); err != nil {
		return "", "", 0, err
	}
	dir, err := os.MkdirTemp(r.cfg.TmpDir, "oryn-gate-")
	if err != nil {
		return "", "", 0, fmt.Errorf("work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	uid, gid, err := r.prepare(dir)
	if err != nil {
		return "", "", 0, err
	}
	out, errOut := headtail.New(OutputHalf), headtail.New(OutputHalf)
	start := time.Now()
	code, err := r.exec(ctx, dir, command, uid, gid, out, errOut, timeout)
	stdout, stderr = out.String(), errOut.String()
	slog.Info("localgate: 闸门执行完毕", "label", label, "exit", code, "err", err, "ms", time.Since(start).Milliseconds())
	return stdout, stderr, code, err
}

// prepare 在 dir 下建 gates/（数据目录 gates/ 的副本，工作目录）与 tmp/，并返回子进程降权到的 uid / gid：
// orynd 以 root 运行时降到 nobody（目录随之改属主），否则不切换用户（-1）。
func (r *Runner) prepare(dir string) (uid, gid int, err error) {
	gates := filepath.Join(dir, "gates")
	if err := copyTree(r.cfg.GatesDir, gates); err != nil {
		return 0, 0, fmt.Errorf("copy gates dir: %w", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "tmp"), 0o700); err != nil {
		return 0, 0, err
	}
	if os.Geteuid() != 0 {
		return -1, -1, nil
	}
	uid, gid = 65534, 65534
	if u, err := user.Lookup("nobody"); err == nil {
		if n, err := strconv.Atoi(u.Uid); err == nil {
			uid = n
		}
		if n, err := strconv.Atoi(u.Gid); err == nil {
			gid = n
		}
	}
	err = filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
	return uid, gid, err
}

// exec 拉起子进程并等它结束。子进程是 PID 命名空间的 init：超时杀掉它即清理整个命名空间；它正常退出时内核同样杀掉
// 留在后台的进程，输出管道随之关闭。
func (r *Runner) exec(ctx context.Context, dir, command string, uid, gid int, stdout, stderr io.Writer, timeout time.Duration) (int, error) {
	statusR, statusW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer func() { _ = statusR.Close() }()
	args := append(append([]string{}, r.cfg.Command[1:]...),
		"--dir", dir, "--port", strconv.Itoa(r.ln.Addr().(*net.TCPAddr).Port),
		"--uid", strconv.Itoa(uid), "--gid", strconv.Itoa(gid))
	if r.cfg.MaxABI > 0 {
		args = append(args, "--max-landlock-abi", strconv.Itoa(r.cfg.MaxABI))
	}
	args = append(args, "--", command)
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(dctx, r.cfg.Command[0], args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8",
		proxyEnv + "=http://oryn:" + r.token + "@" + r.ln.Addr().String()}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.ExtraFiles = []*os.File{statusW}
	cmd.SysProcAttr = sysProcAttr()
	cmd.WaitDelay = waitDelay
	err = cmd.Start()
	_ = statusW.Close()
	if err != nil {
		return 0, fmt.Errorf("start sandbox: %w", err)
	}
	status, _ := io.ReadAll(statusR)
	werr := cmd.Wait()
	switch {
	case dctx.Err() != nil && ctx.Err() == nil:
		return 0, fmt.Errorf("timed out after %s", timeout)
	case ctx.Err() != nil:
		return 0, errors.New("canceled")
	case strings.TrimSpace(string(status)) != "ok":
		return 0, fmt.Errorf("local sandbox failed: %s", lastLine(stderr))
	}
	var ee *exec.ExitError
	switch {
	case werr == nil:
		return 0, nil
	case errors.As(werr, &ee) && ee.Exited():
		return ee.ExitCode(), nil
	default:
		return 0, fmt.Errorf("exec failed: %w", werr)
	}
}

// lastLine 返回收集器里最后一个非空行（子进程在沙箱失败时写的原因）。
func lastLine(w io.Writer) string {
	s, ok := w.(fmt.Stringer)
	if !ok {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(s.String()), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// copyTree 把 src 下的普通文件与目录（保留权限位，不跟随符号链接）复制到 dst；src 不存在时只建空的 dst。
// 总大小超过 maxGatesCopy 时报错。
func copyTree(src, dst string) error {
	if err := os.Mkdir(dst, 0o755); err != nil {
		return err
	}
	if src == "" {
		return nil
	}
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var total int64
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.Mkdir(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			if total += info.Size(); total > maxGatesCopy {
				return fmt.Errorf("gates dir exceeds %d bytes", maxGatesCopy)
			}
			return copyFile(p, target, info.Mode().Perm())
		}
		return nil // 符号链接、设备等跳过
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
