// Package orbdrt 是 orbd（orb 内守护进程）的运行时（计划 C2 / C4 / H3.3 / H4 / H6.1）：
// 维持到控制面的出站 link、服务 ctl 请求、监督每线程的 omp 进程、tail 会话 JSONL、
// 执行 setup / resume、提供本机 egress 代理入口并上报指标。
//
// 本包中所有计划里写作 `/home/user/…` 的路径一律以 $HOME（Config.Home）为根。
package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// 计划中固定的默认值。
const (
	DefaultEgressAddr      = "127.0.0.1:7811"
	DefaultMetricsInterval = 15 * time.Second
	DefaultPollInterval    = 2 * time.Second
	// DefaultStopGrace 是 SIGTERM / shutdown 时等待 omp 退出的默认时长（C4 第 6 步）。
	DefaultStopGrace = 20 * time.Second

	backoffMin = 500 * time.Millisecond
	backoffMax = 15 * time.Second
)

// Config 是 orbd 运行时配置。
type Config struct {
	URL   string // ORYN_URL：控制面基础地址
	OrbID string // ORYN_ORB_ID
	Token string // ORYN_ORB_TOKEN（oot_…）

	Home       string // 默认 $HOME
	AgentBin   string // 默认 ORYN_AGENT_BIN，再缺省为 "omp"
	EgressAddr string // 默认 127.0.0.1:7811；测试用 127.0.0.1:0
	Version    string // orbd 版本（hello.orbdVersion）

	MetricsInterval time.Duration // 默认 15s
	PollInterval    time.Duration // tailer 轮询兜底间隔，默认 2s
	// RestartDelays 是 agent 崩溃后的重启间隔，默认 1s/2s/4s（长度即最大重启次数）。
	RestartDelays []time.Duration
	// InitTimeout 是等待 `ready` 与每个初始化命令应答的上限，默认 60s。
	InitTimeout time.Duration

	// 指标数据源（测试可替换）：默认 /sys/fs/cgroup、/sys/class/net/eth0、/proc/meminfo、/。
	CgroupDir string
	NetDir    string
	MemInfo   string
	DiskPath  string
}

// ConfigFromEnv 从环境变量读取 orbd 配置。
func ConfigFromEnv(version string) (Config, error) {
	cfg := Config{
		URL:      os.Getenv("ORYN_URL"),
		OrbID:    os.Getenv("ORYN_ORB_ID"),
		Token:    os.Getenv("ORYN_ORB_TOKEN"),
		AgentBin: os.Getenv("ORYN_AGENT_BIN"),
		Version:  version,
	}
	var missing []string
	for k, v := range map[string]string{"ORYN_URL": cfg.URL, "ORYN_ORB_ID": cfg.OrbID, "ORYN_ORB_TOKEN": cfg.Token} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing environment: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

// Runtime 是一个 orbd 实例。
type Runtime struct {
	cfg          Config
	home         string
	agentVersion string
	log          *slog.Logger
	egressLn     net.Listener

	// ctx 是运行时寿命：New 创建，Run 的 ctx 结束时取消。后台 goroutine 一律挂在它上面。
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	cur     *conn
	changed chan struct{} // 连接变化时关闭并替换
	redial  chan struct{}

	threadsMu sync.Mutex
	threads   map[string]*thread
	reg       *registry

	setupMu  sync.Mutex
	envMu    sync.Mutex
	setupEnv map[string]string
	workdir  string

	metrics *metricsReader
}

// conn 是一条已建立并发过 hello 的 link。
type conn struct {
	sess *link.Session
	ctl  *link.Ctl
	done chan struct{}
}

// New 校验配置、绑定 egress 监听并探测 agent 版本。
func New(cfg Config) (*Runtime, error) {
	if cfg.URL == "" || cfg.OrbID == "" || cfg.Token == "" {
		return nil, errors.New("orbdrt: URL, OrbID and Token are required")
	}
	if cfg.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("orbdrt: resolve $HOME: %w", err)
		}
		cfg.Home = h
	}
	home, err := filepath.Abs(cfg.Home)
	if err != nil {
		return nil, err
	}
	if cfg.AgentBin == "" {
		cfg.AgentBin = "omp"
	}
	if cfg.EgressAddr == "" {
		cfg.EgressAddr = DefaultEgressAddr
	}
	if cfg.MetricsInterval <= 0 {
		cfg.MetricsInterval = DefaultMetricsInterval
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.RestartDelays == nil {
		cfg.RestartDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	}
	if cfg.InitTimeout <= 0 {
		cfg.InitTimeout = 60 * time.Second
	}
	if cfg.CgroupDir == "" {
		cfg.CgroupDir = "/sys/fs/cgroup"
	}
	if cfg.NetDir == "" {
		cfg.NetDir = "/sys/class/net/eth0"
	}
	if cfg.MemInfo == "" {
		cfg.MemInfo = "/proc/meminfo"
	}
	if cfg.DiskPath == "" {
		cfg.DiskPath = "/"
	}
	if err := os.MkdirAll(filepath.Join(home, ".oryn", "threads"), 0o755); err != nil {
		return nil, fmt.Errorf("orbdrt: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.EgressAddr)
	if err != nil {
		return nil, fmt.Errorf("orbdrt: egress listen %s: %w", cfg.EgressAddr, err)
	}
	r := &Runtime{
		cfg:      cfg,
		home:     home,
		log:      slog.Default().With("component", "orbd", "orb", cfg.OrbID),
		egressLn: ln,
		changed:  make(chan struct{}),
		redial:   make(chan struct{}, 1),
		threads:  map[string]*thread{},
		reg:      loadRegistry(filepath.Join(home, ".oryn", "orbd", "threads.json"), cfg.OrbID),
		metrics:  newMetricsReader(cfg),
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.agentVersion = detectAgentVersion(cfg.AgentBin)
	return r, nil
}

// EgressAddr 返回 egress 实际监听地址。
func (r *Runtime) EgressAddr() string { return r.egressLn.Addr().String() }

// AgentVersion 返回 `<agent> --version` 的输出（探测失败为空）。
func (r *Runtime) AgentVersion() string { return r.agentVersion }

func detectAgentVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		slog.Warn("orbd: agent --version failed", "bin", bin, "err", err)
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}

// Run 运行到 ctx 结束：恢复已登记线程的 tailer、egress、指标与 link 重连循环。
// 优雅退出（停 agent、冲刷 tailer）由调用方在取消 ctx 之前调用 Shutdown 完成。
func (r *Runtime) Run(ctx context.Context) error {
	stop := context.AfterFunc(ctx, r.cancel)
	defer stop()
	defer r.cancel()
	ctx = r.ctx
	for id, dir := range r.reg.list() {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			r.thread(id, dir)
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.serveEgress(ctx) }()
	go func() { defer wg.Done(); r.metricsLoop(ctx) }()
	r.linkLoop(ctx)
	_ = r.egressLn.Close()
	wg.Wait()
	return nil
}

// Redial 立即丢弃当前 link 并重连（SIGHUP）。
func (r *Runtime) Redial() {
	select {
	case r.redial <- struct{}{}:
	default:
	}
}

// Shutdown 优雅停止全部 agent（关闭 stdin、读完输出，超时 SIGKILL），然后在 link 可用时冲刷 tailer。
func (r *Runtime) Shutdown(grace time.Duration) {
	r.stopAllAgents(grace)
	r.flushTailers(5 * time.Second)
}

// ---- 连接管理 ----

func (r *Runtime) current() *conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

// changedCh 返回当前的连接变化信号；须在检查状态之前取得，避免漏掉唤醒。
func (r *Runtime) changedCh() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changed
}

func (r *Runtime) setConn(c *conn) {
	r.mu.Lock()
	r.cur = c
	close(r.changed)
	r.changed = make(chan struct{})
	r.mu.Unlock()
}

// waitConn 等待一条可用 link。
func (r *Runtime) waitConn(ctx context.Context) (*conn, error) {
	for {
		ch := r.changedCh()
		if c := r.current(); c != nil {
			return c, nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// notify 在 link 可用时发出通知；断连时丢弃（hello 与状态重发负责补齐）。
func (r *Runtime) notify(method string, params any) {
	if c := r.current(); c != nil {
		if err := c.ctl.Notify(method, params); err != nil {
			r.log.Debug("notify failed", "method", method, "err", err)
		}
	}
}

var errRedial = errors.New("redial requested")

func (r *Runtime) linkLoop(ctx context.Context) {
	backoff := backoffMin
	for ctx.Err() == nil {
		connected, err := r.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRedial) {
			backoff = backoffMin
			continue
		}
		if connected {
			backoff = backoffMin
		}
		r.log.Warn("link down; reconnecting", "err", err, "in", backoff)
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-r.redial:
			t.Stop()
			backoff = backoffMin
			continue
		case <-ctx.Done():
			t.Stop()
			return
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// connectOnce 建立一条 link 并阻塞到它断开；connected 报告是否曾成功建立。
func (r *Runtime) connectOnce(ctx context.Context) (connected bool, err error) {
	// 丢弃断连期间积压的重连信号。
	select {
	case <-r.redial:
	default:
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	sess, err := link.Dial(dctx, link.URL(r.cfg.URL), r.cfg.Token)
	if err != nil {
		cancel()
		return false, err
	}
	st, err := sess.Open(dctx, link.StreamHeader{T: link.StreamCtl})
	cancel()
	if err != nil {
		_ = sess.Close()
		return false, err
	}
	c := &conn{sess: sess, done: make(chan struct{})}
	c.ctl = link.NewCtl(st, link.CtlHandlers{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			return r.onRequest(ctx, c, method, params)
		},
	})
	defer func() {
		r.mu.Lock()
		if r.cur == c {
			r.cur = nil
			close(r.changed)
			r.changed = make(chan struct{})
		}
		r.mu.Unlock()
		close(c.done)
		_ = c.ctl.Close()
		_ = sess.Close()
	}()
	// hello 必须是 ctl 上的第一条消息；其后重发每个线程最近一次非 ready 的 agent.status
	// （ready 由转发器在新 agent 流打开后重发，保证控制面收到 ready 时流已可用）。
	if err := c.ctl.Notify("hello", r.hello()); err != nil {
		return false, err
	}
	for _, t := range r.threadList() {
		if s := t.lastStatus(); s != nil && s.Status != apigen.LinkCtlAgentStatusParamsStatusReady {
			_ = c.ctl.Notify("agent.status", s)
		}
	}
	r.log.Info("link up")
	r.setConn(c)
	go r.acceptLoop(sess)

	select {
	case <-sess.CloseChan():
		return true, errors.New("session closed")
	case <-c.ctl.Done():
		return true, fmt.Errorf("ctl closed: %w", c.ctl.Err())
	case <-r.redial:
		return true, errRedial
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

// acceptLoop 关闭控制面主动打开的流（M2 没有 orynd→orbd 的流类型）。
func (r *Runtime) acceptLoop(sess *link.Session) {
	for {
		st, err := sess.AcceptStream(context.Background())
		if err != nil {
			var he *link.HeaderError
			if errors.As(err, &he) {
				continue
			}
			return
		}
		r.log.Warn("unexpected stream from control plane", "t", st.Header.T)
		_ = st.Close()
	}
}

func (r *Runtime) hello() apigen.LinkCtlHelloParams {
	threads := []apigen.LinkCtlHelloThread{}
	seen := map[string]bool{}
	for _, t := range r.threadList() {
		seen[t.id] = true
		threads = append(threads, apigen.LinkCtlHelloThread{ThreadID: t.id, AgentRunning: t.agentRunning()})
	}
	for id := range r.reg.list() {
		if !seen[id] {
			threads = append(threads, apigen.LinkCtlHelloThread{ThreadID: id})
		}
	}
	return apigen.LinkCtlHelloParams{
		OrbdVersion:  r.cfg.Version,
		AgentVersion: r.agentVersion,
		Os:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Threads:      threads,
	}
}

// ---- ctl 请求分派 ----

func ctlErr(code, format string, args ...any) *link.CtlError {
	return &link.CtlError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func decode[T any](params json.RawMessage) (T, error) {
	var v T
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	if err := json.Unmarshal(params, &v); err != nil {
		return v, ctlErr(link.CodeInvalidParams, "%v", err)
	}
	return v, nil
}

func (r *Runtime) onRequest(_ context.Context, c *conn, method string, params json.RawMessage) (any, error) {
	// 长操作使用运行时 ctx：link 断开不应中断 setup 或 agent 停止。
	ctx := r.ctx
	switch method {
	case "setup.run", "resume.run":
		p, err := decode[apigen.LinkCtlSetupRunParams](params)
		if err != nil {
			return nil, err
		}
		return r.runSetup(ctx, p, method == "setup.run")
	case "agent.start":
		p, err := decode[apigen.LinkCtlAgentStartParams](params)
		if err != nil {
			return nil, err
		}
		return r.agentStart(p)
	case "agent.stop":
		p, err := decode[apigen.LinkCtlAgentStopParams](params)
		if err != nil {
			return nil, err
		}
		return r.agentStop(p)
	case "blob.fetch":
		p, err := decode[apigen.LinkCtlBlobFetchParams](params)
		if err != nil {
			return nil, err
		}
		return r.blobFetch(ctx, c, p)
	case "fs.copyTree":
		p, err := decode[apigen.LinkCtlFsCopyTreeParams](params)
		if err != nil {
			return nil, err
		}
		return r.fsCopyTree(p.From, p.To)
	case "fs.move":
		p, err := decode[apigen.LinkCtlFsMoveParams](params)
		if err != nil {
			return nil, err
		}
		return r.fsMove(p.From, p.To)
	case "shutdown":
		p, err := decode[apigen.LinkCtlShutdownParams](params)
		if err != nil {
			return nil, err
		}
		grace := time.Duration(p.GraceSeconds) * time.Second
		if grace <= 0 {
			grace = DefaultStopGrace
		}
		r.Shutdown(grace)
		return apigen.LinkCtlOkResult{Ok: true}, nil
	default:
		return nil, link.ErrMethodNotFound
	}
}

// ---- 线程表 ----

func (r *Runtime) threadList() []*thread {
	r.threadsMu.Lock()
	defer r.threadsMu.Unlock()
	out := make([]*thread, 0, len(r.threads))
	for _, t := range r.threads {
		out = append(out, t)
	}
	return out
}

func (r *Runtime) lookupThread(id string) *thread {
	r.threadsMu.Lock()
	defer r.threadsMu.Unlock()
	return r.threads[id]
}

// thread 返回（必要时创建并启动）线程对象；dir 为会话目录。
func (r *Runtime) thread(id, dir string) *thread {
	r.threadsMu.Lock()
	defer r.threadsMu.Unlock()
	if t := r.threads[id]; t != nil {
		if t.dir != dir {
			r.log.Warn("thread session dir change ignored", "thread", id, "have", t.dir, "want", dir)
		}
		return t
	}
	t := newThread(r, id, dir)
	t.ctx, t.cancel = context.WithCancel(r.ctx)
	r.threads[id] = t
	go t.forward(t.ctx)
	go t.tail.run(t.ctx)
	return t
}

// CodeForeignThread 是控制面对不属于本 orb 的线程（来自快照源 orb）返回的 ctl 错误码。
const CodeForeignThread = "foreign_thread"

func isForeignThread(err error) bool {
	var ce *link.CtlError
	return errors.As(err, &ce) && ce.Code == CodeForeignThread
}

// dropThread 放弃一个控制面声明不属于本 orb 的线程：移出登记表与线程表，
// 停止其 tailer 与转发器，停掉 agent（不再重启）。之后的 agent.start 会重新登记。
func (r *Runtime) dropThread(t *thread) {
	r.threadsMu.Lock()
	if r.threads[t.id] == t {
		delete(r.threads, t.id)
	}
	r.threadsMu.Unlock()
	if err := r.reg.remove(t.id); err != nil {
		r.log.Warn("persist thread registry failed", "err", err)
	}
	r.log.Warn("control plane reports foreign thread; dropped", "thread", t.id)
	t.cancel()
	go t.stopAgent(5 * time.Second)
}

func (r *Runtime) stopAllAgents(grace time.Duration) {
	var wg sync.WaitGroup
	for _, t := range r.threadList() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.stopAgent(grace)
		}()
	}
	wg.Wait()
}

func (r *Runtime) flushTailers(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, t := range r.threadList() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.tail.flush(ctx)
		}()
	}
	wg.Wait()
}

// baseEnv 是 orbd 子进程（agent、setup 脚本）的基础环境：继承 orbd 环境但去掉 orb 令牌。
func (r *Runtime) baseEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "ORYN_ORB_TOKEN" {
			continue
		}
		env[k] = v
	}
	env["HOME"] = r.home
	env["ORYN_ORB"] = "1"
	env["ORYN_ORB_ID"] = r.cfg.OrbID
	return env
}

func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
