//go:build linux

// Package guestinit 是 Firecracker guest 里作 PID 1 的 orbd（M11b 计划 8.2）：第一阶段把只读的基础盘与可写盘
// 叠成 overlay 并换根，第二阶段挂好 /dev/pts、/dev/shm、/tmp、cgroup2，在 vsock 端口 fcguest.Port 上服务宿主的
// 控制协议（internal/fcguest），监督 `orbd run`，并回收所有子进程。
//
// 协议处理（Guest）与系统调用分离：Guest 只经 sysOps 与 orbdProc 产生副作用，单元测试用替身；挂载与换根由真机测试覆盖。
package guestinit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/fcguest"
)

const (
	// userUID / userGID 是镜像里 orbd 与 agent 的用户（images/orb 的 `user`）。
	userUID  = 1000
	userGID  = 1000
	userHome = "/home/user"
	// tokenPath 是 orbd 每次拨号前读取的令牌文件（orbdrt.TokenFile）。
	tokenPath = userHome + "/.oryn/orb-token"
	// dataMount 是可写盘在新根里的挂载点（fsfreeze / fsthaw 的对象）。
	dataMount = "/run/oryn-data"
	// imageEnvPath 是构建 rootfs 时写入的镜像配置（docker image inspect 的 Config.Env / WorkingDir / User）。
	imageEnvPath = "/etc/oryn/image-env.json"
	// orbdBin 是 guest 里的 orbd（同时是 init）。
	orbdBin = "/usr/local/bin/orbd"
	// freezeLimit 是 fsfreeze 之后自动解冻的期限（宿主没等来 fsthaw 时不让 guest 永远卡住）。
	freezeLimit = 120 * time.Second
	// defaultShutdownGrace 是 shutdown 未给 graceSeconds 时（以及 SIGINT 兜底）等 orbd 退出的时长。
	defaultShutdownGrace = 30 * time.Second
	// defaultPath 是镜像环境里没有 PATH 时的缺省值。
	defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// imageConfig 是 /etc/oryn/image-env.json：{"env": ["K=V", …], "workingDir": "…", "user": "…"}（env 可为 null）。
type imageConfig struct {
	Env        []string `json:"env"`
	WorkingDir string   `json:"workingDir"`
	User       string   `json:"user"`
}

// readImageConfig 读镜像配置；文件缺失或损坏时返回空配置并记日志（orbd 仍以缺省 PATH 启动）。
func readImageConfig(path string) imageConfig {
	var c imageConfig
	b, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("guest-init: image config unavailable", "path", path, "err", err)
		return c
	}
	if err := json.Unmarshal(b, &c); err != nil {
		slog.Warn("guest-init: image config corrupt", "path", path, "err", err)
		return imageConfig{}
	}
	return c
}

// sysOps 是 Guest 的全部系统副作用（真实现见 sys.go；测试用替身）。
type sysOps struct {
	SetClock    func(t time.Time) error
	SetHostname func(name string) error
	// WriteFile 原子写文件（同目录临时文件 + rename），属主 uid:gid、权限 mode；父目录不存在时以同一属主创建。
	WriteFile func(path string, data []byte, mode os.FileMode, uid, gid int) error
	Sync      func()
	Freeze    func(path string) error
	Thaw      func(path string) error
	// KillAll 向除自己以外的全部进程发 SIGKILL（kill(-1, SIGKILL)）。
	KillAll func()
	// Reboot 重启 guest（Firecracker 以 reboot=k 启动，guest 复位时退出）；正常不返回。
	Reboot func()
	// AfterFunc 在 d 之后调用 f，返回取消函数（同 time.AfterFunc(...).Stop）。
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
}

// orbdProc 是被监督的 `orbd run`（真实现见 supervisor.go）。
type orbdProc interface {
	// Start 启动 orbd 并保持运行：意外退出按 1s 起、上限 10s 的退避重启。
	Start(env []string, dir string) error
	// Running 报告 orbd 当前是否有存活进程。
	Running() bool
	// Signal 给当前 orbd 进程发信号（没有进程时无操作）。
	Signal(sig syscall.Signal) error
	// Stop 停止重启，发 SIGTERM 并至多等 grace 让它退出。
	Stop(grace time.Duration)
}

// Guest 处理宿主经 vsock 发来的 fcguest 请求。除 ping 外的请求串行执行。
type Guest struct {
	sys   sysOps
	orbd  orbdProc
	image imageConfig
	log   *slog.Logger

	mu       sync.Mutex
	started  bool        // orbd 已经由 start 拉起（之后的 start 等同 token）
	thawStop func() bool // fsfreeze 的自动解冻定时器
	down     bool        // 已执行 shutdown
}

func newGuest(sys sysOps, orbd orbdProc, image imageConfig) *Guest {
	return &Guest{sys: sys, orbd: orbd, image: image, log: slog.Default().With("component", "guest-init")}
}

func fail(format string, args ...any) fcguest.Response {
	return fcguest.Response{OK: false, Error: fmt.Sprintf(format, args...)}
}

var okResp = fcguest.Response{OK: true}

// Handle 执行一个请求，返回应答与「应答写出之后」要执行的动作（shutdown 的重启；其余为 nil）。
func (g *Guest) Handle(req fcguest.Request) (fcguest.Response, func()) {
	if req.Op == fcguest.OpPing {
		return okResp, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.down {
		return fail("guest is shutting down"), nil
	}
	switch req.Op {
	case fcguest.OpStart:
		return g.start(req), nil
	case fcguest.OpToken:
		return g.token(req), nil
	case fcguest.OpSync:
		g.sys.Sync()
		return okResp, nil
	case fcguest.OpFSFreeze:
		return g.freeze(), nil
	case fcguest.OpFSThaw:
		return g.thaw(), nil
	case fcguest.OpShutdown:
		grace := time.Duration(req.GraceSeconds) * time.Second
		if grace <= 0 {
			grace = defaultShutdownGrace
		}
		g.shutdownLocked(grace)
		return okResp, g.sys.Reboot
	default:
		return fail("unknown op %q", req.Op), nil
	}
}

// setClock 按宿主时间对时（nowMs ≤ 0 表示不对时）；失败只记日志。
func (g *Guest) setClock(nowMs int64) {
	if nowMs <= 0 {
		return
	}
	if err := g.sys.SetClock(time.UnixMilli(nowMs)); err != nil {
		g.log.Warn("set clock failed", "err", err)
	}
}

func (g *Guest) writeToken(tok string) error {
	return g.sys.WriteFile(tokenPath, []byte(tok+"\n"), 0o600, userUID, userGID)
}

// start：对时、hostname、/etc/hosts、/etc/resolv.conf、令牌文件，然后拉起 orbd run；已拉起过时等同 token。
func (g *Guest) start(req fcguest.Request) fcguest.Response {
	if req.URL == "" || req.OrbID == "" || req.Token == "" {
		return fail("start requires url, orbId and token")
	}
	if g.started {
		return g.token(req)
	}
	g.setClock(req.NowMs)
	if err := g.sys.SetHostname(req.OrbID); err != nil {
		return fail("sethostname: %v", err)
	}
	hosts := fmt.Sprintf("127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n127.0.1.1\t%s\n", req.OrbID)
	if err := g.sys.WriteFile("/etc/hosts", []byte(hosts), 0o644, 0, 0); err != nil {
		return fail("write /etc/hosts: %v", err)
	}
	if len(req.DNS) > 0 {
		var b strings.Builder
		for _, ns := range req.DNS {
			fmt.Fprintf(&b, "nameserver %s\n", ns)
		}
		if err := g.sys.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644, 0, 0); err != nil {
			return fail("write /etc/resolv.conf: %v", err)
		}
	}
	if err := g.writeToken(req.Token); err != nil {
		return fail("write token: %v", err)
	}
	dir := g.image.WorkingDir
	if dir == "" {
		dir = userHome
	}
	if err := g.orbd.Start(orbdEnv(g.image.Env, req), dir); err != nil {
		return fail("start orbd: %v", err)
	}
	g.started = true
	return okResp
}

// token：对时、重写令牌文件、SIGHUP orbd（让它用新令牌重拨）。
func (g *Guest) token(req fcguest.Request) fcguest.Response {
	if req.Token == "" {
		return fail("token requires token")
	}
	g.setClock(req.NowMs)
	if err := g.writeToken(req.Token); err != nil {
		return fail("write token: %v", err)
	}
	if g.orbd.Running() {
		if err := g.orbd.Signal(syscall.SIGHUP); err != nil {
			return fail("signal orbd: %v", err)
		}
	}
	return okResp
}

func (g *Guest) freeze() fcguest.Response {
	if err := g.sys.Freeze(dataMount); err != nil {
		return fail("fsfreeze %s: %v", dataMount, err)
	}
	if g.thawStop != nil {
		g.thawStop()
	}
	g.thawStop = g.sys.AfterFunc(freezeLimit, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.log.Warn("no fsthaw within the limit; thawing", "limit", freezeLimit)
		g.thawLocked()
	})
	return okResp
}

func (g *Guest) thaw() fcguest.Response {
	if err := g.thawLocked(); err != nil {
		return fail("fsthaw %s: %v", dataMount, err)
	}
	return okResp
}

func (g *Guest) thawLocked() error {
	if g.thawStop != nil {
		g.thawStop()
		g.thawStop = nil
	}
	return g.sys.Thaw(dataMount)
}

// shutdownLocked：解冻（冻结的文件系统上 sync 会卡住）、SIGTERM orbd 并至多等 grace、杀掉其余进程、sync。
// 重启由调用方在应答之后进行。
func (g *Guest) shutdownLocked(grace time.Duration) {
	g.down = true
	if err := g.thawLocked(); err != nil {
		g.log.Debug("thaw before shutdown", "err", err)
	}
	g.orbd.Stop(grace)
	g.sys.KillAll()
	g.sys.Sync()
}

// shutdownSignal 是 SIGINT（宿主 SendCtrlAltDel 的兜底）的处理：等同 shutdown{30}，随后重启。
func (g *Guest) shutdownSignal() {
	g.mu.Lock()
	if g.down {
		g.mu.Unlock()
		return
	}
	g.shutdownLocked(defaultShutdownGrace)
	g.mu.Unlock()
	g.sys.Reboot()
}

// serveConn 在一条连接上读一行请求、执行、写一行应答，然后执行应答后的动作。
func (g *Guest) serveConn(rw io.ReadWriter) {
	req, err := fcguest.ReadRequest(bufio.NewReader(rw))
	if err != nil {
		if !errors.Is(err, io.EOF) {
			_ = fcguest.WriteResponse(rw, fail("bad request: %v", err))
		}
		return
	}
	resp, after := g.Handle(req)
	if err := fcguest.WriteResponse(rw, resp); err != nil {
		g.log.Warn("write response failed", "op", req.Op, "err", err)
	}
	if after != nil {
		after()
	}
}

// orbdEnv 是 orbd 的环境：镜像环境，加 HOME、ORYN_URL、ORYN_ORB_ID、ORYN_ORB=1、ORYN_FC=1，caPem 非空时
// ORYN_ORB_CA_PEM。令牌不进环境（orbd 每次拨号前读令牌文件）。
func orbdEnv(image []string, req fcguest.Request) []string {
	var keys []string
	vals := map[string]string{}
	set := func(k, v string) {
		if _, ok := vals[k]; !ok {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	for _, kv := range image {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			set(k, v)
		}
	}
	if _, ok := vals["PATH"]; !ok {
		set("PATH", defaultPath)
	}
	set("HOME", userHome)
	set("ORYN_URL", req.URL)
	set("ORYN_ORB_ID", req.OrbID)
	set("ORYN_ORB", "1")
	set("ORYN_FC", "1")
	if strings.TrimSpace(req.CAPEM) != "" {
		set("ORYN_ORB_CA_PEM", req.CAPEM)
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+vals[k])
	}
	return out
}

// userCredential 由 /etc/passwd 与 /etc/group 的内容求出 uid 1000 的凭据：主组取 passwd 里的 gid
// （缺省 1000），附加组是成员表里列出该用户名的组。
func userCredential(passwd, group string) *syscall.Credential {
	cred := &syscall.Credential{Uid: userUID, Gid: userGID}
	name := ""
	for line := range strings.SplitSeq(passwd, "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 4 && f[2] == strconv.Itoa(userUID) {
			name = f[0]
			if gid, err := strconv.ParseUint(f[3], 10, 32); err == nil {
				cred.Gid = uint32(gid)
			}
			break
		}
	}
	if name == "" {
		return cred
	}
	for line := range strings.SplitSeq(group, "\n") {
		f := strings.Split(line, ":")
		if len(f) < 4 {
			continue
		}
		for m := range strings.SplitSeq(f[3], ",") {
			if strings.TrimSpace(m) == name {
				if gid, err := strconv.ParseUint(f[2], 10, 32); err == nil && uint32(gid) != cred.Gid {
					cred.Groups = append(cred.Groups, uint32(gid))
				}
				break
			}
		}
	}
	return cred
}

// readCredential 读 /etc/passwd 与 /etc/group 求 uid 1000 的凭据（读不到时只有 1000:1000）。
func readCredential(root string) *syscall.Credential {
	passwd, _ := os.ReadFile(filepath.Join(root, "etc", "passwd"))
	group, _ := os.ReadFile(filepath.Join(root, "etc", "group"))
	return userCredential(string(passwd), string(group))
}
