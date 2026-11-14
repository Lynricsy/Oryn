package coordhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/sandbox"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

var (
	// testBin 是本测试二进制：以 `coordinator-host …` 参数重新执行时充当子进程（见 TestMain）。
	testBin    string
	fakeompBin string
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "coordinator-host" {
		os.Exit(RunChild(os.Args[2:], os.Stdout, os.Stderr))
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testBin = exe
	// fakeomp 放在 nobody 进得去的目录里（它被复制到 <dir>/bin/omp，源路径本身不需要可达）。
	dir, err := os.MkdirTemp("", "coordhost-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// FAKEOMP_BIN 指向预先构建好的 fakeomp 时直接用它（以非 root 用户运行测试二进制、没有 go 工具链可用时）。
	if fakeompBin = os.Getenv("FAKEOMP_BIN"); fakeompBin == "" {
		fakeompBin = filepath.Join(dir, "fakeomp")
		cmd := exec.Command("go", "build", "-o", fakeompBin, "github.com/Lynricsy/Oryn/cmd/fakeomp")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build fakeomp:", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// requireSandbox 在内核不满足沙箱条件时跳过；ORYN_REQUIRE_SANDBOX=1 下改为失败。
func requireSandbox(t *testing.T) {
	t.Helper()
	abi, err := sandbox.Probe()
	if abi >= sandbox.MinABI {
		return
	}
	if os.Getenv("ORYN_REQUIRE_SANDBOX") == "1" {
		t.Fatalf("landlock ABI %d (%v) but ORYN_REQUIRE_SANDBOX=1", abi, err)
	}
	t.Skipf("landlock unavailable (abi %d, %v)", abi, err)
}

// hostDir 建一个降权后的用户进得去的协调宿主目录（/tmp 下，0755）。
func hostDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "oryn-coordhost-")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// ---- 假 orbs 服务：在 socketpair 上当控制面 ----

type streamListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *streamListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *streamListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

type fakeOrbs struct {
	t   *testing.T
	q   *jobs.Queue
	st  *store.Store
	pid string

	hellos chan apigen.LinkCtlHelloParams
	ctls   chan *link.Ctl
	agents chan *link.Stream
	egress *streamListener
	pauses chan string
	wakes  chan db.Job
	notifs chan string
}

func newFakeOrbs(t *testing.T, q *jobs.Queue, st *store.Store, pid string) *fakeOrbs {
	return &fakeOrbs{
		t: t, q: q, st: st, pid: pid,
		hellos: make(chan apigen.LinkCtlHelloParams, 8),
		ctls:   make(chan *link.Ctl, 8),
		agents: make(chan *link.Stream, 8),
		egress: &streamListener{ch: make(chan net.Conn, 64), done: make(chan struct{})},
		pauses: make(chan string, 8),
		wakes:  make(chan db.Job, 8),
		notifs: make(chan string, 1024),
	}
}

func (f *fakeOrbs) ServeConn(ctx context.Context, _ string, conn net.Conn) error {
	sess, err := link.NewConnSession(conn, false)
	if err != nil {
		return err
	}
	defer sess.Close()
	st, err := sess.AcceptStream(ctx)
	if err != nil {
		return err
	}
	cursors := map[string]int64{}
	var mu sync.Mutex
	ctl := link.NewCtl(st, link.CtlHandlers{
		OnNotify: func(_ context.Context, method string, params json.RawMessage) {
			select {
			case f.notifs <- method:
			default:
			}
			if method == "hello" {
				var h apigen.LinkCtlHelloParams
				_ = json.Unmarshal(params, &h)
				f.hellos <- h
			}
		},
		OnRequest: func(_ context.Context, method string, params json.RawMessage) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			switch method {
			case "entries.cursors":
				return apigen.LinkCtlEntriesCursorsResult{Cursors: []apigen.LinkCtlEntryCursor{}}, nil
			case "entries.append":
				var p apigen.LinkCtlEntriesAppendParams
				_ = json.Unmarshal(params, &p)
				k := p.ThreadID + "/" + p.File
				if p.BaseOffset != cursors[k] {
					return apigen.LinkCtlEntriesAppendResult{Accepted: false, Offset: cursors[k]}, nil
				}
				cursors[k] = p.EndOffset
				return apigen.LinkCtlEntriesAppendResult{Accepted: true, Offset: p.EndOffset}, nil
			}
			return nil, link.ErrMethodNotFound
		},
	})
	f.ctls <- ctl
	for {
		s, err := sess.AcceptStream(ctx)
		if err != nil {
			var he *link.HeaderError
			if errors.As(err, &he) {
				continue
			}
			return nil
		}
		switch s.Header.T {
		case link.StreamAgent:
			f.agents <- s
		case link.StreamEgress:
			f.egress.ch <- s
		default:
			_ = s.Close()
		}
	}
}

// Pause 同 orbs.Pause 的状态限制：只有 ready / frozen 能停机（尝试本身照样记录）。
func (f *fakeOrbs) Pause(ctx context.Context, orbID, mode string) error {
	f.pauses <- orbID
	o, err := f.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return err
	}
	if o.State != "ready" && o.State != "frozen" {
		return fmt.Errorf("orb %s is %s, cannot %s", orbID, o.State, mode)
	}
	_, err = f.q.Enqueue(ctx, f.pid, orbID, "pause", map[string]string{"mode": mode})
	return err
}

func (f *fakeOrbs) Wake(ctx context.Context, orbID string) error {
	job, err := f.q.Enqueue(ctx, f.pid, orbID, "spawn", map[string]any{})
	if err == nil {
		f.wakes <- job
	}
	return err
}

// ---- 测试环境 ----

type testEnv struct {
	t     *testing.T
	h     *Host
	f     *fakeOrbs
	q     *jobs.Queue
	st    *store.Store
	pid   string
	orbID string
	cfg   Config
}

func newEnv(t *testing.T, mutate func(*Config), hostOpts ...func(*Host)) *testEnv {
	t.Helper()
	requireSandbox(t)
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	q := jobs.New(st)
	now := time.Now().UnixMilli()
	// 预先登记 @host（Run 的 upsert 保留已有 id），以便在 Run 之前建 orb 行。
	require.NoError(t, st.Q.UpsertHostProvider(ctx, db.UpsertHostProviderParams{
		ID: ids.New("prov"), TokenHash: "x", Version: "test", Capabilities: "{}", OrbTypes: "[]", CreatedAt: now,
	}))
	prov, err := st.Q.GetProviderByName(ctx, ProviderName)
	require.NoError(t, err)
	orbID := ids.Orb()
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: orbID, ProviderID: prov.ID, Type: "host", State: "spawning", StateChangedAt: now, TokenHash: "x",
		CreatedAt: now, Coordinator: true,
	}))
	cfg := Config{
		Dir:        hostDir(t),
		OmpPath:    fakeompBin,
		User:       "nobody",
		EgressPort: freePort(t),
		Command:    []string{testBin, "coordinator-host"},
		StopGrace:  5 * time.Second,
		Version:    "test",
		LogLevel:   "debug",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	f := newFakeOrbs(t, q, st, prov.ID)
	h := New(cfg, st, q, f)
	for _, o := range hostOpts {
		o(h)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return &testEnv{t: t, h: h, f: f, q: q, st: st, pid: prov.ID, orbID: orbID, cfg: h.cfg}
}

func (e *testEnv) runJob(op string, params any) db.Job {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	job, err := e.q.Enqueue(ctx, e.pid, e.orbID, op, params)
	require.NoError(e.t, err)
	return e.wait(job)
}

func (e *testEnv) wait(job db.Job) db.Job {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done, err := e.q.Wait(ctx, job.ID)
	require.NoError(e.t, err)
	return done
}

func (e *testEnv) hello() apigen.LinkCtlHelloParams {
	e.t.Helper()
	select {
	case h := <-e.f.hellos:
		return h
	case <-time.After(30 * time.Second):
		e.t.Fatalf("no hello; host.log:\n%s", e.h.logTail(4096))
		return apigen.LinkCtlHelloParams{}
	}
}

func (e *testEnv) ctl() *link.Ctl {
	e.t.Helper()
	select {
	case c := <-e.f.ctls:
		return c
	case <-time.After(30 * time.Second):
		e.t.Fatal("no ctl stream")
		return nil
	}
}

func (e *testEnv) child() *child {
	e.h.mu.Lock()
	defer e.h.mu.Unlock()
	return e.h.children[e.orbID]
}

func succeeded(t *testing.T, j db.Job, h *Host) {
	t.Helper()
	errText := ""
	if j.Error != nil {
		errText = *j.Error
	}
	require.Equal(t, "succeeded", j.State, "job %s %s failed: %s\nhost.log:\n%s", j.Op, j.ID, errText, h.logTail(4096))
}

// H5：spawn 后假 ServeConn 收到 hello（特性 coordinator）、状态带沙箱报告；pause 后进程消失；delete 清掉 home。
func TestSpawnPauseDelete(t *testing.T) {
	e := newEnv(t, nil)
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	h := e.hello()
	assert.Equal(t, []string{"coordinator"}, h.Features)
	assert.Equal(t, "omp/0.0.0-fakeomp", h.AgentVersion)
	c := e.child()
	require.NotNil(t, c)
	require.True(t, c.alive())

	st := e.h.Status(context.Background())
	require.True(t, st.Available, "%v", st.Reason)
	require.NotNil(t, st.Sandbox)
	assert.True(t, st.Sandbox.Seccomp)
	assert.GreaterOrEqual(t, st.Sandbox.LandlockAbi, 2)
	if os.Geteuid() == 0 {
		assert.Equal(t, 65534, st.Sandbox.UID)
		assert.Equal(t, apigen.SandboxNetworkNamespace, st.Sandbox.Network, "root can always create namespaces")
		assert.True(t, st.Sandbox.PidNamespace)
	}
	// <dir>/bin/omp 是 0555 的副本，home 是 0700 且属于目标用户。
	fi, err := os.Stat(e.h.ompCopy())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o555), fi.Mode().Perm())
	hi, err := os.Stat(e.h.homeDir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), hi.Mode().Perm())

	// 再次 spawn：已有存活且 link 未断的子进程 → 直接成功，不起新进程。
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	assert.Same(t, c, e.child())

	succeeded(t, e.runJob("pause", map[string]string{"mode": "freeze"}), e.h)
	<-c.done
	assert.ErrorIs(t, syscall.Kill(c.pid, 0), syscall.ESRCH)
	assert.Nil(t, e.child())

	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	e.hello()
	c2 := e.child()
	require.NotNil(t, c2)
	succeeded(t, e.runJob("delete", map[string]any{}), e.h)
	<-c2.done
	_, err = os.Stat(e.h.homeDir())
	assert.ErrorIs(t, err, os.ErrNotExist)

	j := e.runJob("snapshot", map[string]any{"snapshotId": "x"})
	assert.Equal(t, "failed", j.State)
	require.NotNil(t, j.Error)
	assert.Contains(t, *j.Error, "does not support snapshots")
	assert.Empty(t, e.f.pauses, "expected exits must not trigger crash recovery")
}

// H5：kill -9 子进程 → Pause + Wake 各一次；连续三次后不再 Wake。
func TestCrashRecovery(t *testing.T) {
	recovered := make(chan bool, 8)
	e := newEnv(t, nil, func(h *Host) { h.onRecovered = func(_ string, woke bool) { recovered <- woke } })
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	e.hello()
	e.setState("ready")
	for i := 1; i <= 3; i++ {
		c := e.child()
		require.NotNil(t, c, "crash %d", i)
		require.NoError(t, syscall.Kill(c.pid, syscall.SIGKILL))
		select {
		case id := <-e.f.pauses:
			assert.Equal(t, e.orbID, id)
		case <-time.After(30 * time.Second):
			t.Fatalf("crash %d: no Pause", i)
		}
		woke := <-recovered
		if i < crashLimit {
			require.True(t, woke, "crash %d should wake", i)
			job := <-e.f.wakes
			succeeded(t, e.wait(job), e.h)
			e.hello()
		} else {
			assert.False(t, woke, "crash %d must not wake", i)
		}
	}
	assert.Empty(t, e.f.wakes)
}

// 启动阶段（orb 还没 ready，orbs 不接受 stop）子进程意外退出：不走 Pause → Wake，而是原地重新拉起子进程并重新 hello；
// 短时间内崩溃到上限后不再拉起（留给 orbs 的连接超时转 failed）。CI 上即「egress 端口被占、子进程刚报完沙箱就退出」。
func TestCrashWhileStartingRespawns(t *testing.T) {
	recovered := make(chan bool, 8)
	e := newEnv(t, nil, func(h *Host) { h.onRecovered = func(_ string, woke bool) { recovered <- woke } })
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	e.hello()
	for i, state := range []string{"connecting", "setting_up", "resuming"} {
		e.setState(state)
		c := e.child()
		require.NotNil(t, c, state)
		require.NoError(t, syscall.Kill(c.pid, syscall.SIGKILL))
		select {
		case woke := <-recovered:
			if i+1 < crashLimit {
				require.True(t, woke, "%s：应原地重启", state)
				e.hello()
				n := e.child()
				require.NotNil(t, n)
				assert.NotEqual(t, c.pid, n.pid)
			} else {
				assert.False(t, woke, "%s：崩溃达到上限后不再拉起", state)
				assert.Nil(t, e.child())
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s：没有善后", state)
		}
	}
	assert.Empty(t, e.f.wakes, "启动阶段不走 Wake")
	assert.Empty(t, e.f.pauses, "启动阶段不尝试停机")
}

func (e *testEnv) setState(state string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.SetOrbState(context.Background(), db.SetOrbStateParams{ID: e.orbID, State: state, StateChangedAt: time.Now().UnixMilli()}))
}

// H5：Check 的各条原因。
func TestCheckReasons(t *testing.T) {
	ctx := context.Background()
	missing := New(Config{Dir: hostDir(t), OmpPath: "/nonexistent/omp", User: "nobody"}, nil, nil, nil)
	err := missing.Check(ctx)
	var he *Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, CodeUnavailable, he.Code)
	assert.Contains(t, he.Message, "omp not found at /nonexistent/omp")

	pinned := New(Config{Dir: hostDir(t), OmpPath: fakeompBin, User: "nobody", VerifyOmp: true}, nil, nil, nil)
	err = pinned.Check(ctx)
	require.ErrorAs(t, err, &he)
	assert.Contains(t, he.Message, "does not match core/source.json")
	assert.Contains(t, he.Message, "want ")

	if os.Geteuid() != 0 {
		return
	}
	requireSandbox(t)
	private := filepath.Join(t.TempDir(), "coordinator") // t.TempDir 的父目录是 0700
	err = New(Config{Dir: private, OmpPath: fakeompBin, User: "nobody", Command: []string{testBin, "coordinator-host"}}, nil, nil, nil).Check(ctx)
	require.ErrorAs(t, err, &he)
	assert.Contains(t, he.Message, "is not reachable by user nobody")

	err = New(Config{Dir: hostDir(t), OmpPath: fakeompBin, User: "no-such-user-oryn", Command: []string{testBin, "coordinator-host"}}, nil, nil, nil).Check(ctx)
	require.ErrorAs(t, err, &he)
	assert.Contains(t, he.Message, "no-such-user-oryn")

	ok := New(Config{Dir: hostDir(t), OmpPath: fakeompBin, User: "nobody", Command: []string{testBin, "coordinator-host"}}, nil, nil, nil)
	require.NoError(t, ok.Check(ctx))
}

// H5：ensureDirs 拒绝可被组或他人写的目录。
func TestEnsureDirsRejectsWritableDir(t *testing.T) {
	dir := hostDir(t)
	require.NoError(t, os.Chmod(dir, 0o777))
	h := New(Config{Dir: dir, OmpPath: fakeompBin, User: "nobody"}, nil, nil, nil)
	err := h.ensureDirs(-1, -1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writable by group or others")
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, h.ensureDirs(-1, -1))
	// 源不变时不重拷：mtime 与副本一致。
	before, err := os.Stat(h.ompCopy())
	require.NoError(t, err)
	require.NoError(t, h.ensureDirs(-1, -1))
	after, err := os.Stat(h.ompCopy())
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
}

// H4 / H5：SelfTest 全部通过（必须失败的都失败、必须成功的都成功、进程状态正确）。本机两种网络模式各跑一遍，
// 另外强制两种旧内核：没有命名空间、ABI 4（TCP 只靠 Landlock 的端口规则），以及有命名空间、ABI 2（TCP 只靠
// 网络命名空间）。AF_UNIX 在每种模式下都一律被拒（含 socketpair），宿主上的监听端什么也收不到。
func TestSelfTest(t *testing.T)               { selfTestMode(t, false, 0) }
func TestSelfTestPortMode(t *testing.T)       { selfTestMode(t, true, 0) }
func TestSelfTestPortModeABI4(t *testing.T)   { selfTestMode(t, true, 4) }
func TestSelfTestNamespacesABI2(t *testing.T) { selfTestMode(t, false, 2) }

func selfTestMode(t *testing.T, noNS bool, maxABI int) {
	requireSandbox(t)
	if abi, _ := sandbox.Probe(); abi < max(maxABI, 4) && noNS {
		t.Skipf("landlock abi %d < %d：没有命名空间时协调功能 fail-closed（见 TestFailClosedWithoutTCPIsolation）", abi, max(maxABI, 4))
	} else if abi < maxABI {
		t.Skipf("landlock abi %d < %d", abi, maxABI)
	}
	data := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(data, "master.key"), []byte("k"), 0o600))
	cfgFile := filepath.Join(t.TempDir(), "orynd.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("listen: []\n"), 0o644))
	var out bytes.Buffer
	h := New(Config{
		Dir: hostDir(t), OmpPath: fakeompBin, User: "nobody", EgressPort: freePort(t),
		DataDir: data, ConfigPath: cfgFile, Command: []string{testBin, "coordinator-host"},
	}, nil, nil, nil)
	h.noNS, h.maxABI = noNS, maxABI
	err := selfTest(context.Background(), h, &out)
	if noNS && maxABI > 0 && maxABI < sandbox.SignalABI && os.Geteuid() != 0 {
		// 非 root：沙箱与 orynd 同一用户、没有 PID 命名空间，低于 ABI 6 拦不住信号。
		require.Error(t, err)
		assert.Contains(t, out.String(), "没有 PID 命名空间且沙箱与 orynd 是同一用户")
		return
	}
	require.NoError(t, err, out.String())
	var checks []sandbox.Check
	var sawReport bool
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		line := sc.Bytes()
		var c sandbox.Check
		if json.Unmarshal(line, &c) == nil && c.Name != "" {
			checks = append(checks, c)
			assert.True(t, c.OK, "%s: %s", c.Name, c.Detail)
			continue
		}
		if bytes.Contains(line, []byte(`"sandbox"`)) {
			sawReport = true
			if maxABI > 0 {
				assert.Contains(t, string(line), fmt.Sprintf(`"landlockAbi":%d`, maxABI))
			}
		}
	}
	assert.True(t, sawReport, out.String())
	names := map[string]bool{}
	abstract, pathDgram, hostTCP := 0, 0, 0
	for _, c := range checks {
		names[c.Name] = true
		switch {
		case strings.HasPrefix(c.Name, "connect abstract unix "):
			abstract++
		case strings.HasPrefix(c.Name, "connect unix dgram /"), strings.HasPrefix(c.Name, "sendto unix dgram /"):
			pathDgram++
		case strings.HasPrefix(c.Name, "tcp host listener 127.0.0.1:"):
			hostTCP++
		}
	}
	assert.Equal(t, 3, pathDgram, "白名单外的 0666 unix datagram socket：connect、sendto 与经 stream socketpair 的 sendto 都必须失败\n%s", out.String())
	assert.Equal(t, 1, hostTCP, "宿主上真实在监听的白名单外 TCP 端口必须连不上\n%s", out.String())
	for _, want := range []string{
		"read " + filepath.Join(data, "master.key"), "read " + cfgFile, "read /etc/shadow", "readdir /",
		"exec /bin/sh", "exec copy in home", "tcp 1.1.1.1:443", "tcp 127.0.0.1:1", "udp socket", "unix socket",
		"unix dgram socket", "unix socketpair stream (Bun)", "unix socketpair dgram", "unix socketpair seqpacket", "truncate(2) by path",
		"netlink socket", "unshare(CLONE_NEWUSER)", "mount", "clone3", "write and rename in home",
		"flock and OFD lock in home", fmt.Sprintf("listen and dial 127.0.0.1:%d", h.cfg.EgressPort),
		"status NoNewPrivs", "status Seccomp", "status CapEff", "status CapBnd",
	} {
		assert.True(t, names[want], "missing check %q in\n%s", want, out.String())
	}
	assert.Equal(t, 4, abstract, "宿主上的 stream / datagram abstract socket 各以两种类型 connect，都必须失败\n%s", out.String())
}

// 没有网络命名空间且 Landlock ABI < 4 时 TCP 出站关不住（端口规则需要 ABI 4，BestEffort 会悄悄丢掉它）：协调功能
// fail-closed，Check / Status / SelfTest 都给出原因，spawn 不拉起子进程。ABI 2 / 3 各跑一遍。
func TestFailClosedWithoutTCPIsolation(t *testing.T) {
	requireSandbox(t)
	real, _ := sandbox.Probe()
	for _, abi := range []int{2, 3} {
		t.Run(fmt.Sprintf("abi=%d", abi), func(t *testing.T) {
			if real < abi {
				t.Skipf("landlock abi %d < %d", real, abi)
			}
			want := fmt.Sprintf("内核 Landlock ABI %d < 4 且没有网络命名空间，无法限制 TCP 出站", abi)
			e := newEnv(t, nil, func(h *Host) { h.noNS, h.maxABI = true, abi })
			err := e.h.Check(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
			st := e.h.Status(context.Background())
			assert.False(t, st.Available)
			require.NotNil(t, st.Reason)
			assert.Contains(t, *st.Reason, want)
			j := e.runJob("spawn", map[string]any{})
			assert.Equal(t, "failed", j.State)
			assert.Nil(t, e.child())

			h := New(Config{Dir: hostDir(t), OmpPath: fakeompBin, User: "nobody", EgressPort: freePort(t),
				Command: []string{testBin, "coordinator-host"}}, nil, nil, nil)
			h.noNS, h.maxABI = true, abi
			var out bytes.Buffer
			err = selfTest(context.Background(), h, &out)
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
			assert.Contains(t, out.String(), `"available":false`)
			assert.NotContains(t, out.String(), `"sandbox"`)
		})
	}
}

// ---- 在沙箱里走一个回合 ----

// agentConn 是控制面一侧的 agent 流。
type agentConn struct {
	t       *testing.T
	st      *link.Stream
	ch      chan json.RawMessage
	hostLog func() string // 失败时附上协调宿主的日志尾部（含 omp 的 stderr）
}

func (e *testEnv) agent() *agentConn {
	e.t.Helper()
	select {
	case st := <-e.f.agents:
		a := &agentConn{t: e.t, st: st, ch: make(chan json.RawMessage, 4096), hostLog: func() string { return e.h.logTail(8192) }}
		go func() {
			defer close(a.ch)
			rd := omprpc.NewReader(st, omprpc.MaxReassembledBytes+1)
			for {
				line, err := rd.ReadLine()
				if err != nil {
					return
				}
				a.ch <- append(json.RawMessage(nil), line...)
			}
		}()
		return a
	case <-time.After(60 * time.Second):
		e.t.Fatalf("no agent stream; host.log:\n%s", e.h.logTail(8192))
		return nil
	}
}

func (a *agentConn) send(frame map[string]any) {
	a.t.Helper()
	b, err := json.Marshal(frame)
	require.NoError(a.t, err)
	_, err = a.st.Write(append(b, '\n'))
	require.NoError(a.t, err)
}

// until 读帧直到 pred 成立（含该帧）。
func (a *agentConn) until(what string, pred func(map[string]any) bool) []map[string]any {
	a.t.Helper()
	var out []map[string]any
	tm := time.NewTimer(90 * time.Second)
	defer tm.Stop()
	for {
		select {
		case raw, ok := <-a.ch:
			if !ok {
				a.t.Fatalf("agent stream ended before %s; host.log:\n%s", what, a.hostLog())
			}
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			out = append(out, m)
			if pred(m) {
				return out
			}
		case <-tm.C:
			a.t.Fatalf("timed out waiting for %s; host.log:\n%s", what, a.hostLog())
		}
	}
}

func typeIs(typ string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == typ }
}

func responseTo(id string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == "response" && m["id"] == id }
}

// lastAssistantText 取 agent_end.messages 里最后一条 assistant 消息的文本。
func lastAssistantText(frames []map[string]any) string {
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i]["type"] != "agent_end" {
			continue
		}
		msgs, _ := frames[i]["messages"].([]any)
		for j := len(msgs) - 1; j >= 0; j-- {
			m, _ := msgs[j].(map[string]any)
			if m["role"] != "assistant" {
				continue
			}
			var b strings.Builder
			content, _ := m["content"].([]any)
			for _, c := range content {
				if cm, _ := c.(map[string]any); cm["type"] == "text" {
					s, _ := cm["text"].(string)
					b.WriteString(s)
				}
			}
			return b.String()
		}
	}
	return ""
}

const coordThread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

func ompHome(files map[string]string) []apigen.LinkCtlOmpHomeFile {
	var out []apigen.LinkCtlOmpHomeFile
	for p, c := range files {
		out = append(out, apigen.LinkCtlOmpHomeFile{Path: p, Mode: 0o600, ContentB64: base64.StdEncoding.EncodeToString([]byte(c))})
	}
	return out
}

// startCoordinatorAgent 走一遍控制面对协调线程的启动：setup.run（只下发 omp 文本配置）→ agent.start（协调档）。
func (e *testEnv) startCoordinatorAgent(files map[string]string, prompt string, initFrames []apigen.OmpFrame) (*agentConn, int) {
	e.t.Helper()
	ctl := e.ctl()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var sr apigen.LinkCtlSetupRunResult
	require.NoError(e.t, ctl.Call(ctx, "setup.run", apigen.LinkCtlSetupRunParams{OmpHome: ompHome(files)}, &sr))
	require.True(e.t, sr.Ok)
	coord := apigen.AgentProfileCoordinator
	var res apigen.LinkCtlAgentStartResult
	require.NoError(e.t, ctl.Call(ctx, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: coordThread, SessionDir: "~/.oryn/threads/" + coordThread, Profile: &coord, SystemPrompt: &prompt,
		InitFrames: initFrames,
	}, &res), "host.log:\n%s", e.h.logTail(8192))
	return e.agent(), res.Pid
}

// H8 / 入口验证：fakeomp 在真实沙箱里——协调档参数（含 --no-tools、不含 -e）、环境键只有白名单、
// 读控制面文件 / 执行 /bin/sh / 连控制面端口 / 连 unix socket / 写 home 之外全部失败。
func TestProbesInsideSandbox(t *testing.T) { probesInsideSandbox(t, false) }

// 同上，但固定为建不了命名空间的环境：网络只靠 Landlock 的端口规则。
func TestProbesInsideSandboxPortMode(t *testing.T) { probesInsideSandbox(t, true) }

func probesInsideSandbox(t *testing.T, portMode bool) {
	e := newEnv(t, nil, func(h *Host) { h.noNS = portMode })
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	e.hello()

	// 「控制面」的秘密与端点：DAC 放行（0755 目录 + 0644 文件 / 0777 socket），只有沙箱能挡住。
	secretDir, err := os.MkdirTemp("", "oryn-secret-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(secretDir) })
	require.NoError(t, os.Chmod(secretDir, 0o777))
	key := filepath.Join(secretDir, "master.key")
	require.NoError(t, os.WriteFile(key, []byte("secret"), 0o644))
	sockPath := filepath.Join(secretDir, "cp.sock")
	uln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = uln.Close() })
	require.NoError(t, os.Chmod(sockPath, 0o777))
	tln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = tln.Close() })

	a, _ := e.startCoordinatorAgent(nil, "You are a probe.\nReply briefly.", []apigen.OmpFrame{
		{"type": "open_session", "sessionDir": "~/.oryn/threads/" + coordThread},
		{"type": "set_event_filter", "events": nil, "messageUpdates": "delta"},
	})
	a.send(map[string]any{"id": "p1", "type": "prompt", "message": strings.Join([]string{
		"probe read " + key,
		"probe exec /bin/sh",
		"probe tcp " + tln.Addr().String(),
		"probe unix " + sockPath,
		"probe write " + filepath.Join(secretDir, "escape"),
		"probe write " + filepath.Join(e.h.homeDir(), "inside"),
		"probe args",
	}, "\n")})
	frames := a.until("agent_end", typeIs("agent_end"))
	lines := strings.Split(lastAssistantText(frames), "\n")
	require.Len(t, lines, 7, lines)
	for _, l := range lines[:5] {
		assert.False(t, strings.HasSuffix(l, ":ok"), "probe must fail inside the sandbox: %s", l)
	}
	assert.Equal(t, "probe:write:ok", lines[5])
	raw, ok := strings.CutPrefix(lines[6], "probe:args:")
	require.True(t, ok, lines[6])
	var got struct {
		Args    []string `json:"args"`
		EnvKeys []string `json:"envKeys"`
		UID     int      `json:"uid"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &got))
	assert.Contains(t, got.Args, "--no-tools")
	assert.Contains(t, got.Args, "--config")
	assert.NotContains(t, got.Args, "-e")
	assert.ElementsMatch(t, []string{"HOME", "PATH", "LANG", "TMPDIR", "PI_RPC_EMIT_TITLE", "PI_NOTIFICATIONS", "OTEL_SDK_DISABLED", "PI_FILE_LOCK_BACKEND", "ORYN_THREAD_ID"}, got.EnvKeys)
	if os.Geteuid() == 0 {
		assert.Equal(t, 65534, got.UID)
	}
	_, err = os.Stat(filepath.Join(secretDir, "escape"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	st := e.h.Status(context.Background())
	require.NotNil(t, st.Sandbox)
	if portMode {
		assert.Equal(t, apigen.SandboxNetworkPort, st.Sandbox.Network)
		assert.Contains(t, lines[2], "permission denied", "landlock refuses connecting to any port but the model proxy")
	}
}

// fakeLLM 在 egress 流上扮演 OpenAI 兼容的模型端：任何 chat/completions 都流式回一句固定文本。
func fakeLLM(t *testing.T, l net.Listener, reply string) *[]string {
	var mu sync.Mutex
	paths := []string{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
		mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		chunk := func(delta map[string]any, finish any, usage any) string {
			m := map[string]any{"id": "c1", "object": "chat.completion.chunk", "created": 0, "model": "fake-model",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			if usage != nil {
				m["usage"] = usage
			}
			b, _ := json.Marshal(m)
			return "data: " + string(b) + "\n\n"
		}
		usage := map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13}
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "c1", "object": "chat.completion", "created": 0, "model": "fake-model",
				"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
				"usage":   usage})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, s := range []string{
			chunk(map[string]any{"role": "assistant", "content": reply}, nil, nil),
			chunk(map[string]any{}, "stop", nil),
			chunk(map[string]any{}, nil, usage),
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, s)
			if fl != nil {
				fl.Flush()
			}
		}
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &paths
}

// 真 omp（build/core/<arch>/omp）在沙箱里走完一个回合：模型请求经 egress 流到假模型端，不花真实费用；
// 宿主工具注册后活动工具只有它（--no-tools 下没有任何内置工具）。
func TestRealOmpTurnInSandbox(t *testing.T) { realOmpTurn(t, false, 0) }

// 同上，固定为没有命名空间的端口限制模式（omp 启动时探测本机端口、解析域名、连 443 都被沙箱拒绝，仍须正常工作）。
func TestRealOmpTurnInSandboxPortMode(t *testing.T) { realOmpTurn(t, true, 0) }

// 同上，再把 Landlock 封顶到 ABI 6（Debian 13 默认内核）：AF_UNIX 一律禁止，会话照样持久化。
func TestRealOmpTurnInSandboxPortModeABI6(t *testing.T) { realOmpTurn(t, true, 6) }

// realOmpPath 是真实 omp 的路径：ORYN_REAL_OMP（测试尚未钉进 build/core 的新构建时用），缺省 build/core/<arch>/omp。
func realOmpPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("ORYN_REAL_OMP"); p != "" {
		abs, err := filepath.Abs(p)
		require.NoError(t, err)
		return abs
	}
	p, err := filepath.Abs(filepath.Join("..", "..", "build", "core", strings.TrimPrefix(archKey(), "linux-"), "omp"))
	require.NoError(t, err)
	return p
}

func realOmpTurn(t *testing.T, portMode bool, maxABI int) {
	omp := realOmpPath(t)
	if _, err := os.Stat(omp); err != nil {
		t.Skipf("real omp not available at %s (scripts/fetch-core.sh, or set ORYN_REAL_OMP): %v", omp, err)
	}
	if abi, _ := sandbox.Probe(); abi < maxABI {
		t.Skipf("landlock abi %d < %d", abi, maxABI)
	}
	e := newEnv(t, func(c *Config) { c.OmpPath = omp; c.VerifyOmp = true }, func(h *Host) { h.noNS, h.maxABI = portMode, maxABI })
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	h := e.hello()
	assert.NotEmpty(t, h.AgentVersion, "omp --version ran inside the sandbox")
	const reply = "pong from the fake model"
	paths := fakeLLM(t, e.f.egress, reply)

	models := fmt.Sprintf(`providers:
  fake:
    baseUrl: http://127.0.0.1:%d/llm/fake/v1
    apiKey: oryn-ph-test
    api: openai-completions
    models:
      - id: fake-model
        name: Fake Model
        reasoning: false
        input: [text]
        contextWindow: 32000
        maxTokens: 1000
`, e.cfg.EgressPort)
	a, ompPid := e.startCoordinatorAgent(map[string]string{"models.yml": models},
		"You are the Oryn coordinator in a test.\nReply briefly.",
		[]apigen.OmpFrame{
			{"type": "open_session", "sessionDir": "~/.oryn/threads/" + coordThread, "provider": "fake", "modelId": "fake-model"},
			{"type": "set_event_filter", "events": nil, "messageUpdates": "delta"},
			{"type": "set_host_tools", "tools": []any{map[string]any{
				"name": "create_thread", "description": "Start a thread", "parameters": map[string]any{"type": "object", "properties": map[string]any{}},
			}}},
		})

	if portMode {
		// 没有 PID 命名空间时 agent.start 回的是宿主 pid：真 omp 进程继承了全部限制。
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", ompPid))
		require.NoError(t, err)
		status := string(b)
		assert.Contains(t, status, "NoNewPrivs:\t1")
		assert.Contains(t, status, "Seccomp:\t2")
		assert.Contains(t, status, "CapEff:\t0000000000000000")
		if os.Geteuid() == 0 {
			assert.Contains(t, status, "Uid:\t65534\t65534\t65534\t65534")
		}
	}
	a.send(map[string]any{"id": "t1", "type": "get_active_tools"})
	resp := a.until("get_active_tools response", responseTo("t1"))
	last := resp[len(resp)-1]
	require.Equal(t, true, last["success"], last)
	var names []string
	for _, tl := range last["data"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	assert.Contains(t, names, "create_thread")
	for _, banned := range []string{"bash", "read", "write", "edit", "grep", "glob", "find", "eval", "task", "web_search", "github", "lsp", "ast_grep", "ast_edit", "debug"} {
		assert.NotContains(t, names, banned)
	}

	a.send(map[string]any{"id": "p1", "type": "prompt", "message": "Say pong."})
	frames := a.until("agent_end", typeIs("agent_end"))
	assert.Contains(t, lastAssistantText(frames), reply)
	for _, f := range frames {
		assert.NotEqual(t, "session-persistence", f["source"], "会话持久化失败：%v", f)
	}
	// 会话持久化：omp 的原生文件锁（PI_FILE_LOCK_BACKEND=sidecar，home 里的旁路文件）在沙箱里可用，会话 .jsonl 写进 home 且有条目。
	sessDir := filepath.Join(e.h.homeDir(), ".oryn", "threads", coordThread)
	var jsonl []string
	require.Eventually(t, func() bool {
		jsonl = nil
		_ = filepath.WalkDir(sessDir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
				jsonl = append(jsonl, p)
			}
			return nil
		})
		for _, p := range jsonl {
			if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), reply) {
				return true
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond, "会话文件里没有回合的条目（%v）", jsonl)
	// 会话的所有权租约是 ~/.omp/run/session-owners 下的旁路文件（abstract socket 后端不会留下文件）。
	owners, err := os.ReadDir(filepath.Join(e.h.homeDir(), ".omp", "run", "session-owners"))
	require.NoError(t, err, "sidecar 文件锁后端没有生效")
	assert.NotEmpty(t, owners)
	assert.NotEmpty(t, *paths)
	for _, p := range *paths {
		assert.Contains(t, p, "/llm/fake/v1/", p)
		assert.Contains(t, p, "auth=Bearer oryn-ph-test", "the agent only ever sees the placeholder key")
	}
	t.Logf("model requests: %v", *paths)
}

// 没有网络命名空间时出站代理端口落在宿主回环上：被别的进程占着时 Check / Status 立刻报不可用并写明原因，spawn 任务
// 失败且不拉起子进程（不再走「子进程启动即退出 → 崩溃 3 次 → 连接超时」）；端口释放后照常启动，自己的子进程占着
// 端口时仍是可用。
func TestEgressPortBusyWithoutNetns(t *testing.T) {
	requireSandbox(t)
	if abi, _ := sandbox.Probe(); abi < 4 {
		t.Skipf("landlock abi %d < 4：没有命名空间时端口限制不可用", abi)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := busy.Addr().(*net.TCPAddr).Port
	e := newEnv(t, func(c *Config) { c.EgressPort = port }, func(h *Host) { h.noNS = true })
	want := fmt.Sprintf("出站代理端口 127.0.0.1:%d 已被占用（无网络命名空间时绑定在宿主回环上），在 coordinator.egress_port 换一个端口", port)

	err = e.h.Check(context.Background())
	var he *Error
	require.ErrorAs(t, err, &he)
	assert.Equal(t, CodeUnavailable, he.Code)
	assert.Equal(t, want, he.Message)
	st := e.h.Status(context.Background())
	assert.False(t, st.Available)
	require.NotNil(t, st.Reason)
	assert.Equal(t, want, *st.Reason)

	j := e.runJob("spawn", map[string]any{})
	assert.Equal(t, "failed", j.State)
	require.NotNil(t, j.Error)
	assert.Contains(t, *j.Error, want)
	assert.Nil(t, e.child(), "端口被占时不启动子进程")
	assert.Empty(t, e.f.hellos)

	require.NoError(t, busy.Close())
	require.NoError(t, e.h.Check(context.Background()))
	succeeded(t, e.runJob("spawn", map[string]any{}), e.h)
	e.hello()
	assert.NoError(t, e.h.Check(context.Background()), "自己的子进程占着端口不算冲突")
	assert.True(t, e.h.Status(context.Background()).Available)
}

// `orynd coordinator check`（SelfTest）在没有网络命名空间且代理端口被占时报同一条原因，不起沙箱子进程。
func TestSelfTestEgressPortBusy(t *testing.T) {
	requireSandbox(t)
	if abi, _ := sandbox.Probe(); abi < 4 {
		t.Skipf("landlock abi %d < 4：没有命名空间时端口限制不可用", abi)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	h := New(Config{Dir: hostDir(t), OmpPath: fakeompBin, User: "nobody", EgressPort: port,
		Command: []string{testBin, "coordinator-host"}}, nil, nil, nil)
	h.noNS = true
	var out bytes.Buffer
	err = selfTest(context.Background(), h, &out)
	want := fmt.Sprintf("出站代理端口 127.0.0.1:%d 已被占用", port)
	require.Error(t, err)
	assert.Contains(t, err.Error(), want)
	assert.Contains(t, out.String(), `"available":false`)
	assert.Contains(t, out.String(), want)
	assert.NotContains(t, out.String(), `"sandbox"`, "没有起沙箱子进程")
}
