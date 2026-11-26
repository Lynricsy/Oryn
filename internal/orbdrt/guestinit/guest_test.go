//go:build linux

package guestinit

import (
	"bufio"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/fcguest"
)

type writtenFile struct {
	data     string
	mode     os.FileMode
	uid, gid int
}

// fakeSys 记录 Guest 产生的每个系统副作用（按调用顺序记在 calls 里）。和真盘一样，冻结期间写可写盘
// （/run 这个 tmpfs 之外的路径）的 WriteFile / Symlink 一直阻塞到解冻。
type fakeSys struct {
	mu       sync.Mutex
	thawed   *sync.Cond // frozen 变为 false 时广播
	calls    []string
	files    map[string]writtenFile
	links    map[string]string // 路径 → 链接目标
	clock    time.Time
	hostname string
	frozen   bool
	timer    func() // 已安排、尚未取消的自动解冻
	failHost error
}

func (f *fakeSys) record(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeSys) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeSys) isFrozen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frozen
}

func (f *fakeSys) pendingTimer() func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.timer
}

// waitDiskLocked 在可写盘冻结期间阻塞（持 f.mu 调用）。
func (f *fakeSys) waitDiskLocked(path string) {
	for f.frozen && !strings.HasPrefix(path, "/run/") {
		f.thawed.Wait()
	}
}

func (f *fakeSys) ops() sysOps {
	return sysOps{
		SetClock: func(t time.Time) error { f.record("clock"); f.clock = t; return nil },
		SetHostname: func(name string) error {
			f.record("hostname")
			if f.failHost != nil {
				return f.failHost
			}
			f.hostname = name
			return nil
		},
		WriteFile: func(path string, data []byte, mode os.FileMode, uid, gid int) error {
			f.record("write " + path)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.waitDiskLocked(path)
			f.files[path] = writtenFile{string(data), mode, uid, gid}
			return nil
		},
		Symlink: func(target, path string, uid, gid int) error {
			f.record("link " + path + " -> " + target)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.waitDiskLocked(path)
			delete(f.files, path)
			f.links[path] = target
			return nil
		},
		Sync: func() { f.record("sync") },
		Freeze: func(p string) error {
			f.record("freeze " + p)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.frozen = true
			return nil
		},
		Thaw: func(p string) error {
			f.record("thaw " + p)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.frozen = false
			f.thawed.Broadcast()
			return nil
		},
		KillAll: func() { f.record("killall") },
		Reboot:  func() { f.record("reboot") },
		AfterFunc: func(d time.Duration, fn func()) func() bool {
			f.record("timer " + d.String())
			f.mu.Lock()
			defer f.mu.Unlock()
			f.timer = fn
			return func() bool {
				f.mu.Lock()
				defer f.mu.Unlock()
				f.timer = nil
				return true
			}
		},
	}
}

// fakeOrbd 是 orbdProc 的替身。
type fakeOrbd struct {
	sys     *fakeSys
	starts  int
	env     []string
	dir     string
	running bool
	signals []syscall.Signal
	stopped time.Duration
}

func (o *fakeOrbd) Start(env []string, dir string) error {
	o.sys.record("orbd start")
	o.starts++
	o.env, o.dir, o.running = env, dir, true
	return nil
}
func (o *fakeOrbd) Running() bool { return o.running }
func (o *fakeOrbd) Signal(sig syscall.Signal) error {
	o.sys.record("orbd " + sig.String())
	o.signals = append(o.signals, sig)
	return nil
}
func (o *fakeOrbd) Stop(grace time.Duration) {
	o.sys.record("orbd stop")
	o.stopped, o.running = grace, false
}

func newTestGuest(img imageConfig) (*Guest, *fakeSys, *fakeOrbd) {
	fs := &fakeSys{files: map[string]writtenFile{}, links: map[string]string{}}
	fs.thawed = sync.NewCond(&fs.mu)
	o := &fakeOrbd{sys: fs}
	return newGuest(fs.ops(), o, img), fs, o
}

// call 经一条内存连接走完整的一问一答（请求行 → serveConn → 应答行）。
func call(t *testing.T, g *Guest, req fcguest.Request) fcguest.Response {
	t.Helper()
	host, guest := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer guest.Close()
		g.serveConn(guest)
	}()
	require.NoError(t, fcguest.WriteRequest(host, req))
	resp, err := fcguest.ReadResponse(bufio.NewReader(host))
	require.NoError(t, err)
	_ = host.Close()
	<-done
	return resp
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func startReq() fcguest.Request {
	return fcguest.Request{
		Op: fcguest.OpStart, URL: "https://172.31.255.1:7643", OrbID: "orb-01jcz8m3w4x5y6z7a8b9c0d1e2",
		Token: "oot_first", DNS: []string{"1.1.1.1", "1.0.0.1"}, NowMs: 1_800_000_000_000, CAPEM: "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n",
	}
}

func TestStart(t *testing.T) {
	g, fs, o := newTestGuest(imageConfig{
		Env:        []string{"PATH=/home/user/.local/bin:/usr/bin:/bin", "ORYN_DESKTOP=1", "HOME=/root"},
		WorkingDir: "/home/user/workspace", User: "user",
	})
	resp := call(t, g, startReq())
	require.True(t, resp.OK, resp.Error)

	assert.Equal(t, time.UnixMilli(1_800_000_000_000), fs.clock)
	assert.Equal(t, "orb-01jcz8m3w4x5y6z7a8b9c0d1e2", fs.hostname)
	assert.Equal(t, writtenFile{"oot_first\n", 0o600, 1000, 1000}, fs.files["/run/oryn/orb-token"])
	assert.Equal(t, writtenFile{"nameserver 1.1.1.1\nnameserver 1.0.0.1\n", 0o644, 0, 0}, fs.files["/etc/resolv.conf"])
	hosts := fs.files["/etc/hosts"]
	assert.Contains(t, hosts.data, "127.0.0.1\tlocalhost\n")
	assert.Contains(t, hosts.data, "127.0.1.1\torb-01jcz8m3w4x5y6z7a8b9c0d1e2\n")
	// 令牌与链接先就位，再启动 orbd（orbd 拨号前读令牌文件）。
	calls := fs.callList()
	assert.Less(t, slices.Index(calls, "write /run/oryn/orb-token"), slices.Index(calls, "orbd start"))
	assert.Less(t, slices.Index(calls, "link /home/user/.oryn/orb-token -> /run/oryn/orb-token"), slices.Index(calls, "orbd start"))

	require.Equal(t, 1, o.starts)
	assert.Equal(t, "/home/user/workspace", o.dir)
	env := envMap(o.env)
	assert.Equal(t, map[string]string{
		"PATH":            "/home/user/.local/bin:/usr/bin:/bin",
		"ORYN_DESKTOP":    "1",
		"HOME":            "/home/user",
		"ORYN_URL":        "https://172.31.255.1:7643",
		"ORYN_ORB_ID":     "orb-01jcz8m3w4x5y6z7a8b9c0d1e2",
		"ORYN_ORB":        "1",
		"ORYN_FC":         "1",
		"ORYN_ORB_CA_PEM": startReq().CAPEM,
	}, env)
	assert.Len(t, o.env, len(env), "no duplicate keys")

	// 再次 start 等同 token：重写令牌、SIGHUP，不再起第二个 orbd。
	req := startReq()
	req.Token, req.NowMs = "oot_second", 1_800_000_060_000
	require.True(t, call(t, g, req).OK)
	assert.Equal(t, 1, o.starts)
	assert.Equal(t, []syscall.Signal{syscall.SIGHUP}, o.signals)
	assert.Equal(t, "oot_second\n", fs.files["/run/oryn/orb-token"].data)
	assert.Equal(t, time.UnixMilli(1_800_000_060_000), fs.clock)
}

func TestStartWithoutCAAndImageConfig(t *testing.T) {
	g, _, o := newTestGuest(imageConfig{})
	req := startReq()
	req.CAPEM = "  \n"
	require.True(t, call(t, g, req).OK)
	env := envMap(o.env)
	assert.NotContains(t, env, "ORYN_ORB_CA_PEM", "a blank caPem is not passed on")
	assert.Equal(t, defaultPath, env["PATH"])
	assert.Equal(t, "/home/user", o.dir)
}

// M30：沙箱的 start 让 orbd 以精简模式运行、link 经 vsock 中继；普通 orb 没有这两项（见 TestStart）。
func TestStartSandboxEnv(t *testing.T) {
	g, _, o := newTestGuest(imageConfig{})
	req := startReq()
	req.Sandbox, req.LinkVia, req.DNS = true, "vsock:5006", nil
	require.True(t, call(t, g, req).OK)
	env := envMap(o.env)
	assert.Equal(t, "1", env["ORYN_SANDBOX"])
	assert.Equal(t, "vsock:5006", env["ORYN_LINK_VIA"])
	assert.Len(t, o.env, len(env), "no duplicate keys")
}

func TestStartRejectsIncompleteAndFailures(t *testing.T) {
	g, fs, o := newTestGuest(imageConfig{})
	resp := call(t, g, fcguest.Request{Op: fcguest.OpStart, URL: "http://x", OrbID: "orb-x"})
	assert.False(t, resp.OK)
	assert.Contains(t, resp.Error, "token")
	assert.Zero(t, o.starts)

	fs.failHost = errors.New("EPERM")
	resp = call(t, g, startReq())
	assert.False(t, resp.OK)
	assert.Contains(t, resp.Error, "sethostname")
	assert.Zero(t, o.starts)
	fs.failHost = nil
	require.True(t, call(t, g, startReq()).OK, "a failed start can be retried")
	assert.Equal(t, 1, o.starts)
}

func TestToken(t *testing.T) {
	g, fs, o := newTestGuest(imageConfig{})
	require.True(t, call(t, g, startReq()).OK)
	resp := call(t, g, fcguest.Request{Op: fcguest.OpToken, Token: "oot_rotated", NowMs: 1_900_000_000_000})
	require.True(t, resp.OK, resp.Error)
	assert.Equal(t, writtenFile{"oot_rotated\n", 0o600, 1000, 1000}, fs.files[tokenPath])
	assert.Equal(t, []syscall.Signal{syscall.SIGHUP}, o.signals)
	assert.Equal(t, time.UnixMilli(1_900_000_000_000), fs.clock)

	// orbd 不在运行（重启退避中）：只写令牌，不发信号。
	o.running = false
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpToken, Token: "oot_again"}).OK)
	assert.Len(t, o.signals, 1)
	assert.False(t, call(t, g, fcguest.Request{Op: fcguest.OpToken}).OK, "empty token")
}

func TestSyncFreezeThaw(t *testing.T) {
	g, fs, _ := newTestGuest(imageConfig{})
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpSync}).OK)
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	assert.True(t, fs.isFrozen())
	assert.Equal(t, []string{"sync", "freeze /run/oryn-data", "timer 2m0s"}, fs.callList())
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSThaw}).OK)
	assert.False(t, fs.isFrozen())
	assert.Nil(t, fs.pendingTimer(), "thaw cancels the automatic thaw")

	// 冻结后没等来 fsthaw：定时器到期自行解冻。
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	fire := fs.pendingTimer()
	require.NotNil(t, fire)
	fire()
	assert.False(t, fs.isFrozen())

	// 过期的定时器（被后一次 fsfreeze 取代）不解冻新的冻结。
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	stale := fs.pendingTimer()
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	stale()
	assert.True(t, fs.isFrozen(), "a superseded timer does not thaw")
	fs.pendingTimer()()
	assert.False(t, fs.isFrozen())
}

// S1：令牌只写在 /run（tmpfs）下；$HOME 下（可写盘）只有指向它的符号链接，快照盘里没有令牌。
func TestTokenOnlyOnTmpfs(t *testing.T) {
	g, fs, _ := newTestGuest(imageConfig{})
	require.True(t, call(t, g, startReq()).OK)
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpToken, Token: "oot_rotated"}).OK)

	assert.Equal(t, map[string]string{"/home/user/.oryn/orb-token": "/run/oryn/orb-token"}, fs.links)
	assert.Equal(t, "oot_rotated\n", fs.files["/run/oryn/orb-token"].data)
	for p, f := range fs.files {
		if strings.Contains(f.data, "oot_") {
			assert.True(t, strings.HasPrefix(p, "/run/") && !strings.HasPrefix(p, dataMount+"/"),
				"token written to %s, which is not on the tmpfs", p)
		}
		assert.False(t, strings.HasPrefix(p, userHome+"/"), "nothing is written under $HOME: %s", p)
	}
}

// 真实的 symlinkAs：可写盘上旧版本留下的令牌普通文件被原子替换成链接；已是链接时不动；父目录缺失时创建。
func TestSymlinkAsReplacesOldTokenFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "run", "orb-token")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("oot_new\n"), 0o600))
	link := filepath.Join(dir, "home", ".oryn", "orb-token")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.WriteFile(link, []byte("oot_old\n"), 0o600))

	uid, gid := os.Getuid(), os.Getgid()
	require.NoError(t, symlinkAs(target, link, uid, gid))
	fi, err := os.Lstat(link)
	require.NoError(t, err)
	assert.True(t, fi.Mode()&os.ModeSymlink != 0, "the old regular file was replaced by a symlink")
	b, err := os.ReadFile(link)
	require.NoError(t, err)
	assert.Equal(t, "oot_new\n", string(b))
	require.NoError(t, symlinkAs(target, link, uid, gid), "idempotent")
	des, err := os.ReadDir(filepath.Dir(link))
	require.NoError(t, err)
	assert.Len(t, des, 1, "no temporary link left behind")

	fresh := filepath.Join(dir, "fresh", ".oryn", "orb-token")
	require.NoError(t, symlinkAs(target, fresh, uid, gid))
	got, err := os.Readlink(fresh)
	require.NoError(t, err)
	assert.Equal(t, target, got)
}

// waitClosed 等 ch 关闭；超时即判定为死锁。
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s is stuck (deadlock)", what)
	}
}

// R2：fsfreeze 之后，token 与自动解冻并发，都能结束；自动解冻不依赖请求锁（一个卡在冻结盘上的请求占着它
// 时照样解冻）；冻结时到达的 start 先解冻再写盘；之后的 shutdown 也能结束。
func TestAutoThawNotBlockedByFrozenDiskWrites(t *testing.T) {
	g, fs, o := newTestGuest(imageConfig{})
	require.True(t, call(t, g, startReq()).OK)
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	fire := fs.pendingTimer()
	require.NotNil(t, fire)

	tokenDone, fireDone := make(chan struct{}), make(chan struct{})
	var tokenResp fcguest.Response
	go func() {
		defer close(tokenDone)
		tokenResp, _ = g.Handle(fcguest.Request{Op: fcguest.OpToken, Token: "oot_after_freeze"})
	}()
	go func() {
		defer close(fireDone)
		fire()
	}()
	waitClosed(t, tokenDone, "token")
	waitClosed(t, fireDone, "automatic thaw")
	assert.True(t, tokenResp.OK, tokenResp.Error)
	assert.False(t, fs.isFrozen())
	assert.Equal(t, "oot_after_freeze\n", fs.files[tokenPath].data)

	// 一个卡在冻结盘上的请求占着 mu：自动解冻照样执行，并让它返回。
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	fire = fs.pendingTimer()
	held, stuckDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stuckDone)
		g.mu.Lock()
		defer g.mu.Unlock()
		close(held)
		_ = g.sys.WriteFile("/home/user/notes", []byte("x"), 0o644, userUID, userGID)
	}()
	<-held
	fireDone = make(chan struct{})
	go func() {
		defer close(fireDone)
		fire()
	}()
	waitClosed(t, fireDone, "automatic thaw while a request holds the lock")
	waitClosed(t, stuckDone, "the write on the frozen disk")

	// 冻结后到达的 start（尚未拉起 orbd 的 guest）先解冻，再写 /etc/hosts 等。
	g2, fs2, o2 := newTestGuest(imageConfig{})
	require.True(t, call(t, g2, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	startDone := make(chan struct{})
	var startResp fcguest.Response
	go func() {
		defer close(startDone)
		startResp, _ = g2.Handle(startReq())
	}()
	waitClosed(t, startDone, "start on a frozen disk")
	assert.True(t, startResp.OK, startResp.Error)
	assert.Equal(t, 1, o2.starts)
	assert.False(t, fs2.isFrozen())
	assert.Nil(t, fs2.pendingTimer(), "the stale freeze's automatic thaw is cancelled")

	// 冻结中、mu 被一个卡在冻结盘上的请求占着：shutdown 先解冻，请求返回，shutdown 随之完成。
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	held, stuckDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stuckDone)
		g.mu.Lock()
		defer g.mu.Unlock()
		close(held)
		_ = g.sys.WriteFile("/home/user/notes", []byte("y"), 0o644, userUID, userGID)
	}()
	<-held
	shutDone := make(chan struct{})
	var shutResp fcguest.Response
	go func() {
		defer close(shutDone)
		shutResp, _ = g.Handle(fcguest.Request{Op: fcguest.OpShutdown, GraceSeconds: 1})
	}()
	waitClosed(t, shutDone, "shutdown")
	waitClosed(t, stuckDone, "the write on the frozen disk")
	assert.True(t, shutResp.OK, shutResp.Error)
	assert.Equal(t, time.Second, o.stopped)
	assert.False(t, fs.isFrozen())
}

func TestShutdown(t *testing.T) {
	g, fs, o := newTestGuest(imageConfig{})
	require.True(t, call(t, g, startReq()).OK)
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	before := len(fs.callList())
	resp := call(t, g, fcguest.Request{Op: fcguest.OpShutdown, GraceSeconds: 7})
	require.True(t, resp.OK)
	assert.Equal(t, 7*time.Second, o.stopped)
	// 先解冻（冻结的盘上 sync 会卡住），停 orbd，杀其余进程，sync；应答写出后才重启。
	assert.Equal(t, []string{"thaw /run/oryn-data", "orbd stop", "killall", "sync", "reboot"}, fs.callList()[before:])
	assert.False(t, call(t, g, fcguest.Request{Op: fcguest.OpStart}).OK, "no further ops after shutdown")
	assert.True(t, call(t, g, fcguest.Request{Op: fcguest.OpPing}).OK, "ping still answers")
}

func TestShutdownDefaultsAndSignal(t *testing.T) {
	g, fs, o := newTestGuest(imageConfig{})
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpShutdown}).OK)
	assert.Equal(t, defaultShutdownGrace, o.stopped)

	g, fs, o = newTestGuest(imageConfig{})
	g.shutdownSignal() // SIGINT（SendCtrlAltDel）
	assert.Equal(t, defaultShutdownGrace, o.stopped)
	assert.Equal(t, []string{"thaw /run/oryn-data", "orbd stop", "killall", "sync", "reboot"}, fs.callList())
	g.shutdownSignal()
	assert.Len(t, fs.callList(), 5, "second signal is a no-op")
}

func TestPingUnknownAndBadLine(t *testing.T) {
	g, fs, _ := newTestGuest(imageConfig{})
	assert.Equal(t, fcguest.Response{OK: true}, call(t, g, fcguest.Request{Op: fcguest.OpPing}))
	resp := call(t, g, fcguest.Request{Op: "format-disk"})
	assert.False(t, resp.OK)
	assert.Contains(t, resp.Error, "unknown op")
	assert.Empty(t, fs.callList())

	host, guest := net.Pipe()
	go func() { defer guest.Close(); g.serveConn(guest) }()
	_, err := host.Write([]byte("not json\n"))
	require.NoError(t, err)
	resp, err = fcguest.ReadResponse(bufio.NewReader(host))
	require.NoError(t, err)
	assert.False(t, resp.OK)
	assert.Contains(t, resp.Error, "bad request")
}

func TestUserCredential(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\nuser:x:1000:1000::/home/user:/bin/bash\n"
	group := "root:x:0:\nsudo:x:27:user\ndocker:x:999:alice,user\nuser:x:1000:\nother:x:50:alice\n"
	assert.Equal(t, &syscall.Credential{Uid: 1000, Gid: 1000, Groups: []uint32{27, 999}}, userCredential(passwd, group))
	assert.Equal(t, &syscall.Credential{Uid: 1000, Gid: 1000}, userCredential("", group), "no passwd entry")
}

func TestReadImageConfig(t *testing.T) {
	p := t.TempDir() + "/image-env.json"
	require.NoError(t, os.WriteFile(p, []byte(`{"env":["PATH=/bin","A=b=c"],"workingDir":"/home/user/workspace","user":"user"}`), 0o644))
	assert.Equal(t, imageConfig{Env: []string{"PATH=/bin", "A=b=c"}, WorkingDir: "/home/user/workspace", User: "user"}, readImageConfig(p))
	require.NoError(t, os.WriteFile(p, []byte(`{"env":null,"workingDir":"","user":""}`), 0o644))
	assert.Equal(t, imageConfig{}, readImageConfig(p))
	assert.Equal(t, imageConfig{}, readImageConfig(p+".missing"))
}
