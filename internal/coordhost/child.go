package coordhost

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbdrt"
	"github.com/Lynricsy/Oryn/internal/sandbox"
	"github.com/Lynricsy/Oryn/internal/version"
)

// 子进程 `orynd coordinator-host` 的约定：fd 3 = link（socketpair 的子端），fd 4 = 状态管道（写一行 sandbox.Report 后关闭）。
const (
	linkFD   = 3
	statusFD = 4
	// ExitSandbox 是沙箱或命名空间准备失败时的退出码（原因写 stderr）。
	ExitSandbox = 70
)

// ReadOnlyDirs / ReadOnlyFiles 是协调宿主沙箱里只读可见的系统路径（docs/protocol.md「M9」的 P2 视图）。
var (
	ReadOnlyDirs = []string{
		"/usr/lib", "/usr/lib64", "/lib", "/lib64", "/usr/share/zoneinfo", "/proc",
		"/sys/devices/system/cpu", "/sys/fs/cgroup",
	}
	ReadOnlyFiles = []string{
		"/etc/ld.so.cache", "/etc/localtime", "/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/hosts",
		"/dev/urandom", "/dev/random", "/dev/zero",
	}
)

// probeResult 是 `--probe` 打到 stdout 的一行。
type probeResult struct {
	LandlockABI int  `json:"landlockAbi"`
	Loopback    bool `json:"loopback"`
}

type multiFlag []string

func (m *multiFlag) String() string     { return fmt.Sprint(*m) }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// RunChild 是 `orynd coordinator-host` 的主体（不读配置文件）。顺序固定：探测 → 拉起回环 → 取 link →
// 降权 → 施加沙箱 → 报告 → 自检或运行受限的 orbd。返回进程退出码。
func RunChild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coordinator-host", flag.ContinueOnError)
	fs.SetOutput(stderr)
	orbID := fs.String("orb", "", "orb id")
	home := fs.String("home", "", "沙箱内唯一可写的目录（协调 agent 的 HOME）")
	omp := fs.String("omp", "", "omp 可执行文件")
	egressAddr := fs.String("egress-addr", "127.0.0.1:7811", "模型代理监听地址")
	uid := fs.Int("uid", -1, "降权到的 uid（-1 = 不切换用户）")
	gid := fs.Int("gid", -1, "降权到的 gid")
	netns := fs.Bool("netns", false, "运行在独立的网络命名空间里（先拉起回环）")
	pidns := fs.Bool("pidns", false, "运行在独立的 PID 命名空间里（只用于报告）")
	logLevel := fs.String("log-level", "info", "日志级别")
	probe := fs.Bool("probe", false, "只探测 Landlock ABI 与回环，打印一行 JSON 后退出")
	selftest := fs.Bool("selftest", false, "施加沙箱后跑自检表并退出")
	var denyRead multiFlag
	fs.Var(&denyRead, "deny-read", "自检时必须读不到的文件（可重复）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var lv slog.Level
	_ = lv.UnmarshalText([]byte(*logLevel))
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lv})).With("component", "coordinator-host")
	slog.SetDefault(log)

	// 1. 探测。
	if *probe {
		res := probeResult{}
		if *netns {
			res.Loopback = sandbox.LoopbackUp() == nil
		}
		res.LandlockABI, _ = sandbox.Probe()
		b, _ := json.Marshal(res)
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "coordinator-host: "+format+"\n", a...)
		return ExitSandbox
	}
	if *home == "" || *omp == "" || (!*selftest && *orbID == "") {
		return fail("--home, --omp and --orb are required")
	}
	_, portStr, err := net.SplitHostPort(*egressAddr)
	if err != nil {
		return fail("--egress-addr: %v", err)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		return fail("--egress-addr: invalid port %q", portStr)
	}
	// 2. 回环。
	if *netns {
		if err := sandbox.LoopbackUp(); err != nil {
			return fail("%v", err)
		}
	}
	// 3. link。
	var conn net.Conn
	if !*selftest {
		f := os.NewFile(linkFD, "link")
		if f == nil {
			return fail("fd %d (link) is missing", linkFD)
		}
		conn, err = net.FileConn(f)
		_ = f.Close()
		if err != nil {
			return fail("link fd: %v", err)
		}
	}
	// 4. 降权。
	if err := sandbox.DropPrivileges(*uid, *gid); err != nil {
		return fail("%v", err)
	}
	// 5. 沙箱。
	execs, err := sandbox.ExecPaths(*omp)
	if err != nil {
		return fail("%v", err)
	}
	rep, err := sandbox.Apply(sandbox.Policy{
		ReadWrite:     []string{*home},
		Exec:          execs,
		ReadOnlyDirs:  ReadOnlyDirs,
		ReadOnlyFiles: ReadOnlyFiles,
		TCPPort:       uint16(port),
	})
	if err != nil {
		return fail("sandbox: %v", err)
	}
	rep.NetNS, rep.PIDNS = *netns, *pidns
	// 6. 报告。
	if sf := os.NewFile(statusFD, "status"); sf != nil {
		b, _ := json.Marshal(rep)
		_, _ = sf.Write(append(b, '\n'))
		_ = sf.Close()
	}
	// 7. 自检。
	if *selftest {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		checks := sandbox.Checks(ctx, sandbox.CheckOptions{
			Home:     *home,
			DenyRead: denyRead,
			DenyWrite: []string{
				filepath.Join(filepath.Dir(*home), "selftest-escape-"+strconv.Itoa(os.Getpid())),
				filepath.Join(os.TempDir(), "oryn-selftest-escape-"+strconv.Itoa(os.Getpid())),
			},
			DenyDial: []string{"1.1.1.1:443", "127.0.0.1:1"},
			Exec:     []string{*omp, "--version"},
			Port:     uint16(port),
		})
		code := 0
		enc := json.NewEncoder(stdout)
		for _, c := range checks {
			_ = enc.Encode(c)
			if !c.OK {
				code = 1
			}
		}
		return code
	}
	return runOrbd(*orbID, *home, *omp, *egressAddr, conn, log)
}

// runOrbd 以协调宿主模式运行 orbd：link 只用继承来的 socketpair，断了就退出（由 orynd 重新拉起）。
func runOrbd(orbID, home, omp, egressAddr string, conn net.Conn, log *slog.Logger) int {
	var once sync.Once
	rt, err := orbdrt.New(orbdrt.Config{
		Coordinator: true,
		OrbID:       orbID,
		Home:        home,
		AgentBin:    omp,
		EgressAddr:  egressAddr,
		Version:     version.Version,
		Connect: func(context.Context) (*link.Session, error) {
			var (
				sess  *link.Session
				err   = orbdrt.ErrNoRedial
				first bool
			)
			once.Do(func() {
				first = true
				sess, err = link.NewConnSession(conn, true)
			})
			if !first {
				return nil, orbdrt.ErrNoRedial
			}
			return sess, err
		},
	})
	if err != nil {
		log.Error("start orbd", "err", err)
		_ = conn.Close()
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case s := <-sigs:
			log.Info("signal received; stopping agents", "signal", s.String())
			rt.Shutdown(orbdrt.DefaultStopGrace)
			cancel()
		case <-ctx.Done():
		}
	}()
	err = rt.Run(ctx)
	rt.Shutdown(2 * time.Second)
	if err != nil {
		log.Error("orbd exited", "err", err)
		return 1
	}
	log.Info("link closed; exiting")
	return 0
}
