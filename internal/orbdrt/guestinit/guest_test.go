//go:build linux

package guestinit

import (
	"bufio"
	"errors"
	"net"
	"os"
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

// fakeSys 记录 Guest 产生的每个系统副作用（按调用顺序记在 calls 里）。
type fakeSys struct {
	mu       sync.Mutex
	calls    []string
	files    map[string]writtenFile
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
			f.files[path] = writtenFile{string(data), mode, uid, gid}
			return nil
		},
		Sync:    func() { f.record("sync") },
		Freeze:  func(p string) error { f.record("freeze " + p); f.frozen = true; return nil },
		Thaw:    func(p string) error { f.record("thaw " + p); f.frozen = false; return nil },
		KillAll: func() { f.record("killall") },
		Reboot:  func() { f.record("reboot") },
		AfterFunc: func(d time.Duration, fn func()) func() bool {
			f.record("timer " + d.String())
			f.timer = fn
			return func() bool { f.timer = nil; return true }
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
	fs := &fakeSys{files: map[string]writtenFile{}}
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
	assert.Equal(t, writtenFile{"oot_first\n", 0o600, 1000, 1000}, fs.files["/home/user/.oryn/orb-token"])
	assert.Equal(t, writtenFile{"nameserver 1.1.1.1\nnameserver 1.0.0.1\n", 0o644, 0, 0}, fs.files["/etc/resolv.conf"])
	hosts := fs.files["/etc/hosts"]
	assert.Contains(t, hosts.data, "127.0.0.1\tlocalhost\n")
	assert.Contains(t, hosts.data, "127.0.1.1\torb-01jcz8m3w4x5y6z7a8b9c0d1e2\n")
	// 令牌先落盘，再启动 orbd（orbd 拨号前读令牌文件）。
	calls := fs.callList()
	assert.Less(t, slices.Index(calls, "write /home/user/.oryn/orb-token"), slices.Index(calls, "orbd start"))

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
	assert.Equal(t, "oot_second\n", fs.files["/home/user/.oryn/orb-token"].data)
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
	assert.True(t, fs.frozen)
	assert.Equal(t, []string{"sync", "freeze /run/oryn-data", "timer 2m0s"}, fs.callList())
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSThaw}).OK)
	assert.False(t, fs.frozen)
	assert.Nil(t, fs.timer, "thaw cancels the automatic thaw")

	// 冻结后没等来 fsthaw：定时器到期自行解冻。
	require.True(t, call(t, g, fcguest.Request{Op: fcguest.OpFSFreeze}).OK)
	require.NotNil(t, fs.timer)
	fire := fs.timer
	fire()
	assert.False(t, fs.frozen)
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
