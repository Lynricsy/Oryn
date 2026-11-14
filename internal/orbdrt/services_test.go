package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// testBin 是测试二进制自身的路径；受管服务测试以 ORBD_TEST_HELPER=<mode> 把它当服务进程运行。
var testBin string

// runServiceHelper 是测试二进制的辅助模式（TestMain 见到 ORBD_TEST_HELPER 时调用，不返回）：
// serve 在 127.0.0.1:$PORT 上起 HTTP 服务；crash 立即以 3 退出；env 把环境写到 $ORBD_TEST_ENV_OUT 后照常 serve；
// gated 先以只读打开 FIFO $ORBD_TEST_GATE（测试以写打开它之前一直阻塞），之后照常 serve。
func runServiceHelper(mode string) {
	switch mode {
	case "crash":
		os.Exit(3)
	case "env":
		out := os.Getenv("ORBD_TEST_ENV_OUT")
		if err := os.WriteFile(out+".tmp", []byte(strings.Join(os.Environ(), "\n")), 0o644); err != nil {
			os.Exit(1)
		}
		if err := os.Rename(out+".tmp", out); err != nil {
			os.Exit(1)
		}
		serveHelper()
	case "gated":
		f, err := os.Open(os.Getenv("ORBD_TEST_GATE"))
		if err != nil {
			os.Exit(1)
		}
		_ = f.Close()
		serveHelper()
	case "serve":
		serveHelper()
	}
	fmt.Fprintln(os.Stderr, "unknown ORBD_TEST_HELPER", mode)
	os.Exit(2)
}

func serveHelper() {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", os.Getenv("PORT")))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "hello") })
	fmt.Println("serving on", ln.Addr())
	_ = http.Serve(ln, mux)
	os.Exit(1)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// helperCmd 返回以辅助模式运行测试二进制的服务命令。
func helperCmd(mode string, env ...string) string {
	return fmt.Sprintf("ORBD_TEST_HELPER=%s %s exec %s", mode, strings.Join(env, " "), shellQuote(testBin))
}

const sleepCmd = "exec sleep 100000"

const testThread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

// ---- 夹具 ----

// svcEnv 是一个带服务钩子的 orbd 运行时：OnServiceStatus 的每次回调按顺序记入 events。
type svcEnv struct {
	t    *testing.T
	cp   *fakeCP
	rt   *Runtime
	home string
	stop func()

	mu     sync.Mutex
	events []apigen.OrbService
	cursor map[string]int // 每个服务已检查到的 events 下标
	sig    chan struct{}
}

func newSvcEnv(t *testing.T, cp *fakeCP, home string) *svcEnv {
	t.Helper()
	if cp == nil {
		cp = newFakeCP(t)
	}
	if home == "" {
		home = t.TempDir()
	}
	e := &svcEnv{t: t, cp: cp, home: home, cursor: map[string]int{}, sig: make(chan struct{}, 1)}
	rt, err := New(Config{
		URL: cp.srv.URL, OrbID: "orb-test", Token: testToken, Home: home, AgentBin: fakeompBin,
		EgressAddr: "127.0.0.1:0", Version: "test-orbd", PollInterval: 200 * time.Millisecond,
		MetricsInterval: time.Hour, InitTimeout: 20 * time.Second,
		ServiceBackoff:       func(int) time.Duration { return 5 * time.Millisecond },
		ServiceProbeInterval: 5 * time.Millisecond,
		OnServiceStatus: func(o apigen.OrbService) {
			e.mu.Lock()
			e.events = append(e.events, o)
			e.mu.Unlock()
			select {
			case e.sig <- struct{}{}:
			default:
			}
		},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rt.Run(ctx)
	}()
	var once sync.Once
	e.stop = func() {
		once.Do(func() {
			rt.stopAllAgents(5 * time.Second)
			cancel()
			<-done
		})
	}
	t.Cleanup(e.stop)
	e.rt = rt
	return e
}

// setup 发一次 setup.run（工作区根 = $HOME/workspace），之后服务操作可用；返回后台 Ensure 已被触发。
func (e *svcEnv) setup() {
	e.t.Helper()
	var res apigen.LinkCtlSetupRunResult
	require.NoError(e.t, e.cp.call(e.t, "setup.run", apigen.LinkCtlSetupRunParams{}, &res))
}

func (e *svcEnv) workspace() string { return filepath.Join(e.home, "workspace") }

func (e *svcEnv) writeServices(yaml string) {
	e.t.Helper()
	p := filepath.Join(e.workspace(), ".oryn", "services.yaml")
	require.NoError(e.t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(e.t, os.WriteFile(p, []byte(yaml), 0o644))
}

func (e *svcEnv) start(p orbsock.ServiceStartParams) apigen.OrbService {
	e.t.Helper()
	o, err := e.rt.svc.Start(p)
	require.NoError(e.t, err)
	return o
}

// wait 按顺序检查 name 的状态回调，直到某次快照满足 pred；检查过的快照不再参与之后的 wait。
func (e *svcEnv) wait(name string, pred func(apigen.OrbService) bool) apigen.OrbService {
	e.t.Helper()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	for {
		e.mu.Lock()
		for i := e.cursor[name]; i < len(e.events); i++ {
			if o := e.events[i]; o.Name == name && pred(o) {
				e.cursor[name] = i + 1
				e.mu.Unlock()
				return o
			}
		}
		e.cursor[name] = len(e.events)
		e.mu.Unlock()
		select {
		case <-e.sig:
		case <-timeout.C:
			e.t.Fatalf("service %s: expected state not reached; now %+v", name, e.rt.svc.snapshot(name))
		}
	}
}

func statusIsSvc(s apigen.OrbServiceStatus) func(apigen.OrbService) bool {
	return func(o apigen.OrbService) bool { return o.Status == s }
}

func runningWithPid(o apigen.OrbService) bool {
	return o.Status == apigen.OrbServiceStatusRunning && o.Pid != nil
}

func find(list []apigen.OrbService, name string) apigen.OrbService {
	for _, o := range list {
		if o.Name == name {
			return o
		}
	}
	return apigen.OrbService{}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return p
}

func groupGone(pid int) bool { return errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) }

// ---- 解析 ----

const ampSample = `services:
  web:
    command: pnpm dev -- --host 0.0.0.0 --port "$PORT"
    cwd: app
    port: 3000
    env: { API_MODE: development, RETRIES: 3 }
    health: /healthz
    review: true
    platforms: [linux]
    portal:
      url: /
      title: App
      description: Use the seeded development account.
    portals:
      - url: /admin
        title: Admin
      - folder: Marketing site
        links: [ {url: /home, title: Homepage} ]
      - url: https://example.com/docs
        title: External
  mac-only:
    command: open -a Safari
    platforms: [darwin]
`

func TestServiceFileParseAmpSample(t *testing.T) {
	var warnings []string
	entries, err := parseServiceFile([]byte(ampSample), func(m string) { warnings = append(warnings, m) })
	require.NoError(t, err)
	require.Len(t, entries, 1, "platforms 不含 linux 的服务被跳过")
	e := entries[0]
	require.Empty(t, e.err)
	assert.Equal(t, "web", e.spec.Name)
	assert.Equal(t, apigen.OrbServiceSourceFile, e.spec.Source)
	assert.Equal(t, `pnpm dev -- --host 0.0.0.0 --port "$PORT"`, e.spec.Command)
	assert.Equal(t, "app", e.spec.Cwd)
	assert.Equal(t, 3000, e.spec.Port)
	assert.True(t, e.spec.PortExplicit)
	assert.Equal(t, map[string]string{"API_MODE": "development", "RETRIES": "3"}, e.spec.Env)
	assert.Equal(t, "/healthz", e.spec.Health)
	assert.Equal(t, []apigen.OrbPortalLink{
		{Title: "App", Path: "/"},
		{Title: "Admin", Path: "/admin"},
		{Title: "Homepage", Path: "/home", Folder: new("Marketing site")},
	}, e.spec.Links)
	require.Len(t, warnings, 1, "绝对 URL 的链接跳过并记日志")
	assert.Contains(t, warnings[0], "https://example.com/docs")
}

func TestServiceFileParsePortalForms(t *testing.T) {
	entries, err := parseServiceFile([]byte(`services:
  a: {command: x, portal: true}
  b: {command: x, portal: /docs}
  c: {command: x, portal: {url: "/x?y=1", title: X}}
  d: {command: x, portal: {title: Only}}
  e: {command: x, portal: false}
  f: {command: x}
`), func(string) {})
	require.NoError(t, err)
	want := map[string][]apigen.OrbPortalLink{
		"a": {{Title: "a", Path: "/"}},
		"b": {{Title: "b", Path: "/docs"}},
		"c": {{Title: "X", Path: "/x?y=1"}},
		"d": {{Title: "Only", Path: "/"}},
		"e": {},
		"f": {},
	}
	require.Len(t, entries, len(want))
	for _, e := range entries {
		require.Empty(t, e.err, e.spec.Name)
		assert.Equal(t, want[e.spec.Name], e.spec.Links, e.spec.Name)
	}
}

func TestServiceFileParseInvalid(t *testing.T) {
	entries, err := parseServiceFile([]byte(`services:
  reserved: {command: x, env: {PORT: "1"}}
  url-env: {command: x, env: {PUBLIC_URL: a}}
  escape: {command: x, cwd: ../outside}
  absolute: {command: x, cwd: /etc}
  nocmd: {cwd: app}
  badport: {command: x, port: 70000}
  Bad_Name: {command: x}
  badhealth: {command: x, health: healthz}
  badportal: {command: x, portal: docs}
  ok: {command: x, cwd: ./app/../app}
`), func(string) {})
	require.NoError(t, err)
	errs := map[string]string{}
	for _, e := range entries {
		errs[e.spec.Name] = e.err
	}
	assert.Contains(t, errs["reserved"], "PORT")
	assert.Contains(t, errs["url-env"], "PUBLIC_URL")
	assert.Contains(t, errs["escape"], "escapes")
	assert.Contains(t, errs["absolute"], "relative")
	assert.Contains(t, errs["nocmd"], "command")
	assert.Contains(t, errs["badport"], "port")
	assert.Contains(t, errs["Bad_Name"], "name")
	assert.Contains(t, errs["badhealth"], "health")
	assert.Contains(t, errs["badportal"], "portal")
	assert.Empty(t, errs["ok"])

	_, err = parseServiceFile([]byte("services: [unclosed"), func(string) {})
	require.Error(t, err, "语法错误是整份文件的错误")
	_, err = parseServiceFile([]byte("services: just-a-string"), func(string) {})
	require.Error(t, err)
	entries, err = parseServiceFile([]byte("other: 1\n"), func(string) {})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestServiceFileAmpFallback(t *testing.T) {
	dir := t.TempDir()
	_, found, err := loadServiceFile(dir, func(string) {})
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".amp"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".amp", "services.yaml"), []byte("services:\n  amp: {command: x}\n"), 0o644))
	entries, found, err := loadServiceFile(dir, func(string) {})
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, entries, 1)
	assert.Equal(t, "amp", entries[0].spec.Name, "只有 .amp/services.yaml 时用它")

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".oryn"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".oryn", "services.yaml"), []byte("services:\n  oryn: {command: x}\n"), 0o644))
	entries, _, err = loadServiceFile(dir, func(string) {})
	require.NoError(t, err)
	require.Len(t, entries, 1, "两份都在时只用 .oryn，不合并")
	assert.Equal(t, "oryn", entries[0].spec.Name)
}

func TestServiceLogRotationAndTail(t *testing.T) {
	old := serviceLogMax
	serviceLogMax = 32
	t.Cleanup(func() { serviceLogMax = old })
	path := filepath.Join(t.TempDir(), "logs", "x.log")
	l := &serviceLog{path: path}
	defer l.close()
	_, err := l.Write([]byte("0123456789abcdef0123\n"))
	require.NoError(t, err)
	_, err = l.Write([]byte("second line that rotates\n"))
	require.NoError(t, err)
	rotated, err := os.ReadFile(path + ".1")
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123\n", string(rotated))
	cur, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second line that rotates\n", string(cur))

	b, err := readLogTail(path, 10)
	require.NoError(t, err)
	assert.Empty(t, string(b), "起点落在行中间时丢掉残片")
	require.NoError(t, os.WriteFile(path, []byte("aaa\nbbb\nccc\n"), 0o644))
	b, err = readLogTail(path, 8)
	require.NoError(t, err)
	assert.Equal(t, "bbb\nccc\n", string(b), "起点恰在行首")
	b, err = readLogTail(path, 6)
	require.NoError(t, err)
	assert.Equal(t, "ccc\n", string(b))
	b, err = readLogTail(path, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "aaa\nbbb\nccc\n", string(b))
}

// ---- 监督 ----
// ---- 监督 ----

func TestServiceSupervision(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	e.setup()

	// TCP 探测：有 portal 的服务先 starting，端口连通后 running。helper 在测试打开门控 FIFO 之前不监听端口，
	// 因此 Start 返回时一定仍是 starting；starting → running 的顺序由状态回调验证。
	gate := filepath.Join(t.TempDir(), "gate")
	require.NoError(t, syscall.Mkfifo(gate, 0o600))
	o := e.start(orbsock.ServiceStartParams{Name: "tcp", Command: new(helperCmd("gated", "ORBD_TEST_GATE="+shellQuote(gate))), Portal: true})
	assert.Equal(t, apigen.OrbServiceStatusStarting, o.Status)
	e.wait("tcp", statusIsSvc(apigen.OrbServiceStatusStarting))
	gw, err := os.OpenFile(gate, os.O_WRONLY, 0) // 阻塞到 helper 以读打开
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	o = e.wait("tcp", statusIsSvc(apigen.OrbServiceStatusRunning))
	require.NotNil(t, o.Port)
	assert.GreaterOrEqual(t, *o.Port, servicePortMin)
	assert.LessOrEqual(t, *o.Port, servicePortMax)

	// health 探测：2xx 通过；一直 404 则（探测间隔 × 120 之后）unhealthy。
	e.start(orbsock.ServiceStartParams{Name: "hp", Command: new(helperCmd("serve")), Health: "/healthz"})
	e.wait("hp", statusIsSvc(apigen.OrbServiceStatusRunning))
	e.start(orbsock.ServiceStartParams{Name: "sick", Command: new(helperCmd("serve")), Health: "/missing"})
	e.wait("sick", statusIsSvc(apigen.OrbServiceStatusUnhealthy))

	// 无需探测的服务一拉起即 running；进程被杀 → backoff → running 且 restarts=1。
	e.start(orbsock.ServiceStartParams{Name: "plain", Command: new(helperCmd("serve"))})
	first := e.wait("plain", runningWithPid)
	require.NoError(t, syscall.Kill(*first.Pid, syscall.SIGKILL))
	e.wait("plain", func(o apigen.OrbService) bool { return o.Status == apigen.OrbServiceStatusBackoff && o.Restarts == 1 })
	second := e.wait("plain", func(o apigen.OrbService) bool { return runningWithPid(o) && *o.Pid != *first.Pid })
	assert.Equal(t, 1, second.Restarts)
	assert.Equal(t, *first.Port, *second.Port, "重启不换端口")

	// 连续 5 次短命 → failed。
	e.start(orbsock.ServiceStartParams{Name: "crash", Command: new("exit 3")})
	f := e.wait("crash", statusIsSvc(apigen.OrbServiceStatusFailed))
	assert.Equal(t, 4, f.Restarts)
	require.NotNil(t, f.ExitCode)
	assert.Equal(t, 3, *f.ExitCode)
	require.NotNil(t, f.Error)
	assert.Contains(t, *f.Error, "5 times")

	logb, err := os.ReadFile(filepath.Join(e.home, ".cache", "oryn", "logs", "services", "plain.log"))
	require.NoError(t, err)
	assert.Contains(t, string(logb), "[orbd] starting plain (port")
	assert.Contains(t, string(logb), "[orbd] exited code=-1 (signal: killed)")
	assert.Contains(t, string(logb), "serving on 127.0.0.1:", "服务输出写进日志")
}

func TestServiceStopAndShutdown(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	e.setup()
	e.start(orbsock.ServiceStartParams{Name: "a", Command: new(helperCmd("serve"))})
	a := e.wait("a", runningWithPid)

	o, err := e.rt.svc.Stop("a", false)
	require.NoError(t, err)
	assert.Equal(t, apigen.OrbServiceStatusStopped, o.Status)
	assert.Nil(t, o.Pid)
	assert.True(t, groupGone(*a.Pid), "Stop 后进程组消失")

	// Stop 记入 stopped：Ensure 不再拉起，状态文件里也有。
	list, _ := e.rt.svc.Ensure()
	assert.Equal(t, apigen.OrbServiceStatusStopped, find(list, "a").Status)
	st, err := os.ReadFile(filepath.Join(e.home, ".oryn", "services", "state.json"))
	require.NoError(t, err)
	assert.Contains(t, string(st), `"stopped": [`)
	fi, err := os.Stat(filepath.Join(e.home, ".oryn", "services", "state.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	// Start（无 command）移出 stopped 并拉起。
	e.start(orbsock.ServiceStartParams{Name: "a"})
	a2 := e.wait("a", runningWithPid)
	assert.NotEqual(t, *a.Pid, *a2.Pid)

	// Shutdown 并行停掉服务：进程组消失，但不改期望状态（之后的 Ensure 会重新拉起）。
	e.rt.Shutdown(2 * time.Second)
	assert.True(t, groupGone(*a2.Pid), "Shutdown 后进程组消失")
	assert.Equal(t, apigen.OrbServiceStatusStopped, e.rt.svc.snapshot("a").Status)
	e.rt.svc.Ensure()
	a3 := e.wait("a", runningWithPid)

	// 运行时结束（ctx 取消）时仍存活的进程组被 SIGKILL。
	e.stop()
	// 组长已被回收，组里后代的 SIGKILL 是异步生效的
	assert.Eventually(t, func() bool { return groupGone(*a3.Pid) }, 5*time.Second, 10*time.Millisecond, "Run 结束后进程组消失")
}

// 新 Runtime（同一 $HOME，模拟容器重启）启动时不自行拉起服务；resume.run 之后按 state.json 恢复动态服务，端口不变。
func TestServiceRestoreAfterRestart(t *testing.T) {
	cp := newFakeCP(t)
	home := t.TempDir()
	e1 := newSvcEnv(t, cp, home)
	e1.setup()
	e1.start(orbsock.ServiceStartParams{Name: "dyn", Command: new(helperCmd("serve"))})
	o1 := e1.wait("dyn", runningWithPid)
	e1.stop()
	require.True(t, groupGone(*o1.Pid))

	before := cp.notifCount()
	e2 := newSvcEnv(t, cp, home)
	cp.waitNotif(t, before, "hello", nil)
	assert.Empty(t, e2.rt.svc.List(), "orbd 启动时不自行拉起服务")
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{}, &res))
	o2 := e2.wait("dyn", runningWithPid)
	assert.Equal(t, apigen.OrbServiceSourceDynamic, o2.Source)
	assert.Equal(t, *o1.Port, *o2.Port, "自动分配的端口跨 Runtime 不变")
	assert.NotEqual(t, *o1.Pid, *o2.Pid)
}

// ---- 合并与端口 ----

func TestServiceMergeAndPorts(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	e.setup()
	e.start(orbsock.ServiceStartParams{Name: "web", Command: new(sleepCmd)})
	e.wait("web", runningWithPid)
	e.start(orbsock.ServiceStartParams{Name: "keep", Command: new(sleepCmd)})
	e.wait("keep", runningWithPid)

	x := freePort(t)
	_, egress, err := net.SplitHostPort(e.rt.EgressAddr())
	require.NoError(t, err)
	e.writeServices(fmt.Sprintf(`services:
  web: {command: %q}
  a: {command: %q, port: %d}
  b: {command: %q, port: %d}
  c: {command: %q, port: %s}
  d: {command: %q}
`, sleepCmd, sleepCmd, x, sleepCmd, x, sleepCmd, egress, sleepCmd))
	list, fileErr := e.rt.svc.Ensure()
	assert.Empty(t, fileErr)
	names := make([]string, 0, len(list))
	for _, o := range list {
		names = append(names, o.Name)
	}
	assert.Equal(t, []string{"web", "a", "b", "c", "d", "keep"}, names, "文件顺序在前，其后动态服务")

	assert.Equal(t, apigen.OrbServiceSourceFile, find(list, "web").Source, "同名时文件服务胜出")
	assert.Equal(t, x, *find(list, "a").Port)
	b := find(list, "b")
	assert.Equal(t, apigen.OrbServiceStatusFailed, b.Status)
	assert.Equal(t, "port_conflict", *b.Error)
	c := find(list, "c")
	assert.Equal(t, apigen.OrbServiceStatusFailed, c.Status)
	assert.Equal(t, "forbidden_port", *c.Error)
	d := find(list, "d")
	require.NotNil(t, d.Port)
	assert.GreaterOrEqual(t, *d.Port, servicePortMin)
	assert.NotEqual(t, *find(list, "keep").Port, *d.Port)

	st, err := os.ReadFile(filepath.Join(e.home, ".oryn", "services", "state.json"))
	require.NoError(t, err)
	var state svcState
	require.NoError(t, json.Unmarshal(st, &state))
	assert.NotContains(t, state.Dynamic, "web", "文件新增同名条目 → 丢弃动态定义")
	assert.Contains(t, state.Dynamic, "keep")
	assert.Equal(t, *d.Port, state.Ports["d"])

	_, err = e.rt.svc.Start(orbsock.ServiceStartParams{Name: "web", Command: new(sleepCmd)})
	var se *svcError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeNameConflict, se.code)

	p, _ := strconv.Atoi(egress)
	assert.False(t, e.rt.svc.Exposed(p), "egress 端口永不暴露")
}

func TestServiceFileErrorKeepsServices(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	e.writeServices("services:\n  a: {command: " + strconvQuote(helperCmd("serve")) + "}\n")
	e.setup() // 后台 Ensure 拉起 a
	a := e.wait("a", runningWithPid)

	e.writeServices("services: [unclosed")
	list, fileErr := e.rt.svc.Ensure()
	assert.Contains(t, fileErr, "services.yaml")
	got := find(list, "a")
	assert.Equal(t, apigen.OrbServiceStatusRunning, got.Status, "语法错误时保持现有文件服务不动")
	assert.Equal(t, *a.Pid, *got.Pid)
	assert.Equal(t, fileErr, e.rt.svc.fileError())
}

func strconvQuote(s string) string { return strconv.Quote(s) }

// ---- 环境 ----

func TestServiceEnv(t *testing.T) {
	t.Setenv("ORYN_ORB_TOKEN", "oot_must_not_leak")
	e := newSvcEnv(t, nil, "")
	out := t.TempDir()
	require.NoError(t, e.cp.call(t, "portal.configure", apigen.LinkCtlPortalConfigureParams{
		URLTemplate: "https://t-{code}-p{port}.orb.test/", PrimaryThreadID: new(testThread),
	}, nil))
	webCmd := helperCmd("env", "ORBD_TEST_ENV_OUT="+shellQuote(filepath.Join(out, "web.env")))
	workerCmd := helperCmd("env", "ORBD_TEST_ENV_OUT="+shellQuote(filepath.Join(out, "worker.env")))
	e.writeServices(fmt.Sprintf(`services:
  web:
    command: %s
    portal: true
  worker:
    command: %s
    health: /healthz
    env:
      API: "${services.web.publicURL}/api"
      PLAIN: hello
  broken:
    command: %q
    env: {X: "${services.worker.publicURL}"}
`, strconv.Quote(webCmd), strconv.Quote(workerCmd), sleepCmd))
	e.setup()
	web := e.wait("web", runningWithPid)
	e.wait("worker", runningWithPid)

	readEnv := func(name string) map[string]string {
		b, err := os.ReadFile(filepath.Join(out, name+".env"))
		require.NoError(t, err)
		m := map[string]string{}
		for _, kv := range strings.Split(string(b), "\n") {
			k, v, _ := strings.Cut(kv, "=")
			m[k] = v
		}
		return m
	}
	port := strconv.Itoa(*web.Port)
	wantURL := "https://t-0123456789ab-p" + port + ".orb.test"
	we := readEnv("web")
	assert.Equal(t, port, we["PORT"])
	assert.Equal(t, testThread, we["ORYN_THREAD_ID"])
	assert.Equal(t, wantURL, we["PUBLIC_URL"])
	assert.NotContains(t, we, "ORYN_ORB_TOKEN")
	assert.Equal(t, "1", we["ORYN_ORB"])
	require.NotNil(t, web.PublicURL)
	assert.Equal(t, wantURL, *web.PublicURL)
	assert.Equal(t, testThread, *web.ThreadID)

	wk := readEnv("worker")
	assert.NotContains(t, wk, "PUBLIC_URL", "无 portal 的服务没有 PUBLIC_URL")
	assert.Equal(t, wantURL+"/api", wk["API"], "${services.web.publicURL} 已替换")
	assert.Equal(t, "hello", wk["PLAIN"])
	assert.NotContains(t, wk, "ORYN_ORB_TOKEN")

	broken := e.rt.svc.snapshot("broken")
	assert.Equal(t, apigen.OrbServiceStatusFailed, broken.Status, "引用无 portal 的服务 → 无效")
	assert.Contains(t, *broken.Error, "worker")
}

// 动态服务的 threadId 只在已登记时生效；否则退回主线程 / 字典序最小的已登记线程。
func TestServiceThreadSelection(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	e.setup()
	const other = "T-01932f6e-8d1a-7c3b-9f2e-aaaaaaaaaaaa"
	require.NoError(t, e.rt.reg.add(testThread, filepath.Join(e.home, "t1")))
	require.NoError(t, e.rt.reg.add(other, filepath.Join(e.home, "t2")))
	o := e.start(orbsock.ServiceStartParams{Name: "mine", Command: new(sleepCmd), ThreadID: other})
	_ = o
	mine := e.wait("mine", runningWithPid)
	assert.Equal(t, other, *mine.ThreadID)
	e.start(orbsock.ServiceStartParams{Name: "stranger", Command: new(sleepCmd), ThreadID: "T-unknown"})
	st := e.wait("stranger", runningWithPid)
	assert.Equal(t, testThread, *st.ThreadID, "未登记的 threadId 被忽略，取字典序最小的已登记线程")
}

// ---- 上报 ----

func servicesStateHas(name string, status apigen.OrbServiceStatus) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		var p apigen.LinkCtlServicesStateParams
		_ = json.Unmarshal(raw, &p)
		return find(p.Services, name).Status == status
	}
}

func TestServicesStateReport(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	e.setup()
	e.start(orbsock.ServiceStartParams{Name: "a", Command: new(helperCmd("serve")), Portal: true})
	e.wait("a", statusIsSvc(apigen.OrbServiceStatusRunning))
	iRun, _ := e.cp.waitNotif(t, 0, "services.state", servicesStateHas("a", apigen.OrbServiceStatusRunning))

	// 收到的快照里 a 的状态按生命周期单调前进（中间态可能被合并，但不会倒退）。
	order := map[apigen.OrbServiceStatus]int{"": 0, apigen.OrbServiceStatusStarting: 1, apigen.OrbServiceStatusRunning: 2}
	last := 0
	// 先在锁内拷出再断言：require 失败会 FailNow，持锁退出会让后台通知写入方永远卡住。
	e.cp.mu.Lock()
	notifs := append([]notif(nil), e.cp.notifs[:iRun+1]...)
	e.cp.mu.Unlock()
	for _, n := range notifs {
		if n.method != "services.state" {
			continue
		}
		var p apigen.LinkCtlServicesStateParams
		require.NoError(t, json.Unmarshal(n.params, &p))
		st := find(p.Services, "a").Status
		rank, ok := order[st]
		require.True(t, ok, "意外状态 %q", st)
		assert.GreaterOrEqual(t, rank, last)
		last = rank
	}

	// 状态文件与快照一致。
	b, err := os.ReadFile(filepath.Join(e.home, ".oryn", "services-status.json"))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"name": "a"`)

	_, err = e.rt.svc.Stop("a", false)
	require.NoError(t, err)
	e.cp.waitNotif(t, iRun, "services.state", servicesStateHas("a", apigen.OrbServiceStatusStopped))

	// 重连后补发。
	before := e.cp.notifCount()
	e.cp.killLink()
	iHello, _ := e.cp.waitNotif(t, before, "hello", nil)
	e.cp.waitNotif(t, iHello, "services.state", servicesStateHas("a", apigen.OrbServiceStatusStopped))

	// portal.configure 之后再发一次。
	before = e.cp.notifCount()
	require.NoError(t, e.cp.call(t, "portal.configure", apigen.LinkCtlPortalConfigureParams{URLTemplate: ""}, nil))
	e.cp.waitNotif(t, before, "services.state", nil)
}

// ---- portal 流 ----

func openPortal(t *testing.T, cp *fakeCP, port int) (*link.Stream, apigen.LinkPortalReply) {
	t.Helper()
	cp.ctlNow(t)
	cp.mu.Lock()
	sess := cp.sess
	cp.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamPortal, Port: port})
	require.NoError(t, err)
	line, err := st.ReadLine(4096)
	require.NoError(t, err)
	var rep apigen.LinkPortalReply
	require.NoError(t, json.Unmarshal(line, &rep))
	return st, rep
}

func echoListener(t *testing.T, network, addr string) (int, bool) {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		return 0, false
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, true
}

// v6OnlyEchoListener 起一个只听 [::1] 的回显服务，且同号的 127.0.0.1 端口当前无人监听（否则 dialLoopback
// 会先连上那个无关的 IPv4 服务；全量并行测试时偶发撞号）。IPv6 回环不可用时返回 false。
func v6OnlyEchoListener(t *testing.T) (int, bool) {
	t.Helper()
	for range 20 {
		ln, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			return 0, false
		}
		port := ln.Addr().(*net.TCPAddr).Port
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
		if c != nil {
			_ = c.Close()
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			_ = ln.Close()
			continue
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					_, _ = io.Copy(c, c)
				}()
			}
		}()
		return port, true
	}
	t.Fatal("找不到 127.0.0.1 上空闲的 [::1] 端口")
	return 0, false
}

func TestPortalStream(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	e.setup()

	echo, _ := echoListener(t, "tcp", "127.0.0.1:0")
	e.start(orbsock.ServiceStartParams{Name: "echo", Command: new(sleepCmd), Port: echo, Portal: true})
	st, rep := openPortal(t, e.cp, echo)
	require.True(t, rep.Ok, "%+v", rep)
	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	go func() {
		_, _ = st.Write(payload)
		_ = st.Close() // 半关闭：回显服务读到 EOF 后关闭，orbd 把 EOF 传回来
	}()
	got, err := io.ReadAll(st)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "1MiB 原样往返")

	// 未声明的端口。
	_, rep = openPortal(t, e.cp, freePort(t))
	assert.False(t, rep.Ok)
	assert.Equal(t, "not_exposed", *rep.Code)
	// 声明了但没有 portal 的服务端口同样不暴露。
	plain, _ := echoListener(t, "tcp", "127.0.0.1:0")
	e.start(orbsock.ServiceStartParams{Name: "noportal", Command: new(sleepCmd), Port: plain})
	_, rep = openPortal(t, e.cp, plain)
	assert.Equal(t, "not_exposed", *rep.Code)

	// egress 端口。
	_, egress, err := net.SplitHostPort(e.rt.EgressAddr())
	require.NoError(t, err)
	ep, _ := strconv.Atoi(egress)
	_, rep = openPortal(t, e.cp, ep)
	assert.Equal(t, "forbidden_port", *rep.Code)

	// 已声明但无人监听。
	dead := freePort(t)
	e.start(orbsock.ServiceStartParams{Name: "dead", Command: new(sleepCmd), Port: dead, Portal: true})
	_, rep = openPortal(t, e.cp, dead)
	assert.Equal(t, "connection_refused", *rep.Code)

	// 只听 [::1] 的监听经回退可达。
	v6, ok := v6OnlyEchoListener(t)
	if !ok {
		t.Log("IPv6 loopback unavailable; skipping [::1] case")
		return
	}
	e.start(orbsock.ServiceStartParams{Name: "v6", Command: new(sleepCmd), Port: v6, Portal: true})
	st, rep = openPortal(t, e.cp, v6)
	require.True(t, rep.Ok, "%+v", rep)
	_, err = st.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err = io.ReadFull(st, buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf))
	_ = st.Close()
}

// ---- orbd.sock 与本机 API ----

func TestServiceSock(t *testing.T) {
	e := newSvcEnv(t, nil, "")

	// 还没收到 setup 参数。
	_, err := sockCall(t, e.rt, orbsock.MethodServiceStart, orbsock.ServiceStartParams{Name: "x", Command: new(sleepCmd)}, nil)
	assert.Equal(t, orbsock.CodeNotReady, sockErrCode(err))
	_, err = sockCall(t, e.rt, orbsock.MethodServiceEnsure, nil, nil)
	assert.Equal(t, orbsock.CodeNotReady, sockErrCode(err))

	e.writeServices("services:\n  filed: {command: " + strconv.Quote(helperCmd("serve")) + ", health: /healthz}\n")
	e.setup()

	// ensure：等待离开 starting 后返回全量列表，[orbd] 状态行流回。
	var lg sockLog
	raw, err := sockCall(t, e.rt, orbsock.MethodServiceEnsure, nil, &lg)
	require.NoError(t, err)
	var lr orbsock.ServiceListResult
	require.NoError(t, json.Unmarshal(raw, &lr))
	assert.Equal(t, apigen.OrbServiceStatusRunning, find(lr.Services, "filed").Status)

	// start（动态，portal）：等就绪，流回 [orbd] 行。
	lg = sockLog{}
	raw, err = sockCall(t, e.rt, orbsock.MethodServiceStart, orbsock.ServiceStartParams{Name: "dyn", Command: new(helperCmd("serve")), Portal: true}, &lg)
	require.NoError(t, err)
	var sr orbsock.ServiceResult
	require.NoError(t, json.Unmarshal(raw, &sr))
	assert.Equal(t, apigen.OrbServiceStatusRunning, sr.Service.Status)
	assert.Equal(t, []apigen.OrbPortalLink{{Title: "dyn", Path: "/"}}, sr.Service.Links)
	assert.Contains(t, lg.get(orbsock.StreamSystem), "[orbd] starting dyn")
	pid := *sr.Service.Pid

	// list。
	raw, err = sockCall(t, e.rt, orbsock.MethodServiceList, nil, nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &lr))
	assert.Len(t, lr.Services, 2)

	// restart：新进程。
	raw, err = sockCall(t, e.rt, orbsock.MethodServiceRestart, orbsock.ServiceNameParams{Name: "dyn"}, nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &sr))
	assert.Equal(t, apigen.OrbServiceStatusRunning, sr.Service.Status)
	assert.NotEqual(t, pid, *sr.Service.Pid)

	// stop --remove：定义一并删除。
	raw, err = sockCall(t, e.rt, orbsock.MethodServiceStop, orbsock.ServiceStopParams{Name: "dyn", Remove: true}, nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &sr))
	assert.Equal(t, apigen.OrbServiceStatusStopped, sr.Service.Status)
	assert.Len(t, e.rt.svc.List(), 1)

	// 错误码。
	_, err = sockCall(t, e.rt, orbsock.MethodServiceStart, orbsock.ServiceStartParams{Name: "nope"}, nil)
	assert.Equal(t, orbsock.CodeServiceNotFound, sockErrCode(err))
	_, err = sockCall(t, e.rt, orbsock.MethodServiceStart, orbsock.ServiceStartParams{Name: "Bad Name", Command: new(sleepCmd)}, nil)
	assert.Equal(t, orbsock.CodeInvalidService, sockErrCode(err))
	_, err = sockCall(t, e.rt, orbsock.MethodServiceStart, orbsock.ServiceStartParams{Name: "x", Command: new(sleepCmd), Env: map[string]string{"PORT": "1"}}, nil)
	assert.Equal(t, orbsock.CodeInvalidService, sockErrCode(err))
	_, err = sockCall(t, e.rt, orbsock.MethodServiceStart, orbsock.ServiceStartParams{Name: "filed", Command: new(sleepCmd)}, nil)
	assert.Equal(t, orbsock.CodeNameConflict, sockErrCode(err))
	_, err = sockCall(t, e.rt, orbsock.MethodServiceStop, orbsock.ServiceStopParams{Name: "filed", Remove: true}, nil)
	assert.Equal(t, orbsock.CodeInvalidService, sockErrCode(err))
	_, err = sockCall(t, e.rt, orbsock.MethodServiceRestart, orbsock.ServiceNameParams{Name: "nope"}, nil)
	assert.Equal(t, orbsock.CodeServiceNotFound, sockErrCode(err))
	_, err = sockCall(t, e.rt, orbsock.MethodServiceStop, orbsock.ServiceStopParams{Name: "nope"}, nil)
	assert.Equal(t, orbsock.CodeServiceNotFound, sockErrCode(err))
}

func TestServiceLocalAPI(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	h := newOrbHTTP(t, e.cp, e.rt)

	resp, b := h.do(http.MethodPost, "/services/web/start", nil, nil, nil)
	assert.Equal(t, "services_not_ready", h.errCode(resp, b, http.StatusConflict))

	e.writeServices("services:\n  web: {command: " + strconv.Quote(helperCmd("serve")) + ", portal: true}\n")
	e.setup()
	e.wait("web", statusIsSvc(apigen.OrbServiceStatusRunning))

	var o apigen.OrbService
	h.json(http.MethodPost, "/services/web/stop", nil, nil, http.StatusOK, &o)
	assert.Equal(t, apigen.OrbServiceStatusStopped, o.Status)
	h.json(http.MethodPost, "/services/web/start", nil, nil, http.StatusOK, &o)
	assert.Equal(t, apigen.OrbServiceStatusRunning, o.Status, "start 等到就绪")
	pid := *o.Pid
	h.json(http.MethodPost, "/services/web/restart", nil, nil, http.StatusOK, &o)
	assert.Equal(t, apigen.OrbServiceStatusRunning, o.Status)
	assert.NotEqual(t, pid, *o.Pid)

	resp, b = h.do(http.MethodGet, "/services/web/log", url.Values{"tail": {"100000"}}, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(b), "[orbd] starting web")
	assert.Contains(t, string(b), "[orbd] exited code=")
	resp, b = h.do(http.MethodGet, "/services/web/log", url.Values{"tail": {"0"}}, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, b)
	resp, b = h.do(http.MethodGet, "/services/ghost/log", nil, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "没有日志返回空体")
	assert.Empty(t, b)
	resp, b = h.do(http.MethodGet, "/services/web/log", url.Values{"tail": {"-1"}}, nil, nil)
	assert.Equal(t, "invalid_request", h.errCode(resp, b, http.StatusBadRequest))

	resp, b = h.do(http.MethodPost, "/services/ghost/start", nil, nil, nil)
	assert.Equal(t, "service_not_found", h.errCode(resp, b, http.StatusNotFound))
	resp, b = h.do(http.MethodPost, "/services/ghost/stop", nil, nil, nil)
	assert.Equal(t, "service_not_found", h.errCode(resp, b, http.StatusNotFound))
	resp, b = h.do(http.MethodPost, "/services/ghost/restart", nil, nil, nil)
	assert.Equal(t, "service_not_found", h.errCode(resp, b, http.StatusNotFound))
	resp, b = h.do(http.MethodPost, "/services/Bad_Name/start", nil, nil, nil)
	assert.Equal(t, "invalid_service", h.errCode(resp, b, http.StatusBadRequest))
	resp, b = h.do(http.MethodGet, "/services/..%2Fx/log", nil, nil, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(b))
}

// B3：portal.configure 晚于 setup 拉起服务到达（且没有之后的 agent.start）时，迟到的配置自己补齐：
// 有 portal 却没拿到 PUBLIC_URL 的服务、以及 ${services.X.publicURL} 被替换成空串的服务都被重启并拿到 URL。
func TestServiceLatePortalConfigure(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	out := t.TempDir()
	webCmd := helperCmd("env", "ORBD_TEST_ENV_OUT="+shellQuote(filepath.Join(out, "web.env")))
	workerCmd := helperCmd("env", "ORBD_TEST_ENV_OUT="+shellQuote(filepath.Join(out, "worker.env")))
	e.writeServices(fmt.Sprintf(`services:
  web:
    command: %s
    portal: true
  worker:
    command: %s
    health: /healthz
    env:
      API: "${services.web.publicURL}/api"
`, strconv.Quote(webCmd), strconv.Quote(workerCmd)))
	e.setup() // 还没有 portal.configure，也没有线程：两个服务都拿不到 URL
	web1 := e.wait("web", runningWithPid)
	worker1 := e.wait("worker", runningWithPid)
	assert.Nil(t, web1.PublicURL)
	readEnv := func(name string) map[string]string {
		b, err := os.ReadFile(filepath.Join(out, name+".env"))
		require.NoError(t, err)
		m := map[string]string{}
		for _, kv := range strings.Split(string(b), "\n") {
			k, v, _ := strings.Cut(kv, "=")
			m[k] = v
		}
		return m
	}
	assert.NotContains(t, readEnv("web"), "PUBLIC_URL")
	assert.Equal(t, "/api", readEnv("worker")["API"])

	require.NoError(t, e.cp.call(t, "portal.configure", apigen.LinkCtlPortalConfigureParams{
		URLTemplate: "https://t-{code}-p{port}.orb.test/", PrimaryThreadID: new(testThread),
	}, nil))
	wantURL := "https://t-0123456789ab-p" + strconv.Itoa(*web1.Port) + ".orb.test"
	web2 := e.wait("web", func(o apigen.OrbService) bool { return runningWithPid(o) && *o.Pid != *web1.Pid })
	require.NotNil(t, web2.PublicURL)
	assert.Equal(t, wantURL, *web2.PublicURL)
	assert.Equal(t, wantURL, readEnv("web")["PUBLIC_URL"])
	e.wait("worker", func(o apigen.OrbService) bool { return runningWithPid(o) && *o.Pid != *worker1.Pid })
	assert.Equal(t, wantURL+"/api", readEnv("worker")["API"], "引用的 URL 迟到后也补上")
}

// B4：runLoop 不再读可被 reload 整体替换的 sv.spec，只用 run 创建时的名字快照。钩子在进程自然退出后、
// 决定重启之前把定义换成另一个名字（模拟并发的定义替换），退避与放弃的日志仍写到原服务名下。
func TestServiceRunLoopUsesRunSnapshot(t *testing.T) {
	e := newSvcEnv(t, nil, "")
	e.setup()
	var once sync.Once
	e.rt.svc.hookRunExited = func(sv *svc) {
		once.Do(func() {
			e.rt.svc.mu.Lock()
			sp := sv.spec
			sp.Name = "replaced"
			sv.spec = sp
			e.rt.svc.mu.Unlock()
		})
	}
	e.start(orbsock.ServiceStartParams{Name: "exiter", Command: new("exit 3")})
	// 定义被换掉之后状态快照以新名字出现；放弃之前的日志行都已写出。
	e.wait("replaced", statusIsSvc(apigen.OrbServiceStatusFailed))
	logDir := filepath.Join(e.home, ".cache", "oryn", "logs", "services")
	orig, err := os.ReadFile(filepath.Join(logDir, "exiter.log"))
	require.NoError(t, err)
	assert.Equal(t, serviceMaxShort-1, strings.Count(string(orig), "restarting in"))
	assert.Contains(t, string(orig), "giving up")
	if b, err := os.ReadFile(filepath.Join(logDir, "replaced.log")); err == nil {
		assert.NotContains(t, string(b), "restarting in", "runLoop 读了被替换的定义")
		assert.NotContains(t, string(b), "giving up", "runLoop 读了被替换的定义")
	}
}
