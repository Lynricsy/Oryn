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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
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
	// ResumeWait 是 setup.run / resume.run 等待 resume 脚本的时长，超出后转入后台，默认 10s。
	ResumeWait time.Duration

	// 指标数据源（测试可替换）：默认 /sys/fs/cgroup、/sys/class/net/eth0、/proc/meminfo、/。
	CgroupDir string
	NetDir    string
	MemInfo   string
	DiskPath  string

	// OnLocalRequest 仅供测试：本机 API（fs 流上的 HTTP）收到的每个请求在路由之前交给它（e2e 断言到达 orbd 的请求头）。
	// ConfigFromEnv 在设置了 ORYN_ORBD_REQUEST_LOG=<文件> 时把它设为「每个请求追加一行 JSON」（process 驱动的子进程 orbd 用）。
	OnLocalRequest func(*http.Request)

	// 受管服务的测试钩子（生产为零值）：ServiceBackoff 是第 n 次（从 1 起）连续短命退出后的重启等待
	// （缺省 1s/2s/4s/8s/16s/30s）；ServiceProbeInterval 是就绪探测间隔（缺省 500ms，unhealthy 判定与
	// 慢速探测按它的倍数缩放）；OnServiceStatus 在每次服务状态变化时同步回调。
	ServiceBackoff       func(n int) time.Duration
	ServiceProbeInterval time.Duration
	OnServiceStatus      func(apigen.OrbService)

	// 桌面（M8）：Desktop 为真且找得到 Xtigervnc、openbox、chromium 时启用（ConfigFromEnv 读 ORYN_DESKTOP=1）。
	// DesktopCDPAddr 是 CDP 前门的监听地址（默认 127.0.0.1:7822，ORYN_DESKTOP_CDP_ADDR 覆盖，测试用 127.0.0.1:0）。
	Desktop        bool
	DesktopCDPAddr string
	// 桌面的测试钩子（生产为零值）：OnDesktopState 在每次桌面状态变化时同步回调；DesktopBackoff 是显示栈
	// 第 n 次（从 1 起）连续失败后的重启等待（缺省 1s/2s/4s/8s/16s/30s）；DesktopTmpDir 是启动 Xvnc 前清理
	// X 锁与 X socket 的目录（缺省 /tmp；在宿主机上跑 e2e 时用 ORYN_DESKTOP_X11_TMPDIR 指向临时目录，
	// 免得删掉宿主机自己的 :1 显示的锁）。
	OnDesktopState func(apigen.LinkCtlDesktopStateParams)
	DesktopBackoff func(n int) time.Duration
	DesktopTmpDir  string
	// DesktopUserNS 报告能否建立 user namespace（决定 Chromium 是否加 --no-sandbox）；nil = 真实探测
	// （userNamespacesUsable）。测试用它固定结果。
	DesktopUserNS func() bool

	// Coordinator 为真时以协调宿主的受限模式运行（M9，见 coordinator.go）：不要求 URL / Token，
	// link 只经 Connect 建立且不重连，只服务 setup.run / resume.run / agent.start / agent.stop / shutdown。
	Coordinator bool
	// Connect 非 nil 时取代 link.Dial 建立 link；返回 ErrNoRedial 表示不能再建，Run 随之返回。
	Connect func(ctx context.Context) (*link.Session, error)
	// ThreadPollInterval 是 orbd.sock 的 thread.wait 轮询 thread.get 的间隔，默认 2s（测试可调小）。
	ThreadPollInterval time.Duration
}

// ConfigFromEnv 从环境变量读取 orbd 配置。ORYN_ORB_TOKEN 可缺省：$HOME/.oryn/orb-token 存在时以它为准
// （控制面每次 spawn 轮换令牌，已存在的容器由 provider 写入该文件并发 SIGHUP）。
func ConfigFromEnv(version string) (Config, error) {
	cfg := Config{
		URL:      os.Getenv("ORYN_URL"),
		OrbID:    os.Getenv("ORYN_ORB_ID"),
		Token:    os.Getenv("ORYN_ORB_TOKEN"),
		AgentBin: os.Getenv("ORYN_AGENT_BIN"),
		// ORYN_EGRESS_ADDR 仅供测试：同一主机上并行多个 process 驱动的 orbd 时设 127.0.0.1:0
		// （git insteadOf 按实际端口写；控制面下发的 models.yml 仍指向固定的 7811）。
		EgressAddr: os.Getenv("ORYN_EGRESS_ADDR"),
		Version:    version,
	}
	var missing []string
	for k, v := range map[string]string{"ORYN_URL": cfg.URL, "ORYN_ORB_ID": cfg.OrbID} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if cfg.Token == "" {
		if home, err := os.UserHomeDir(); err != nil || readTokenFile(home) == "" {
			missing = append(missing, "ORYN_ORB_TOKEN (or $HOME/.oryn/orb-token)")
		}
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing environment: %s", strings.Join(missing, ", "))
	}
	if p := os.Getenv("ORYN_ORBD_REQUEST_LOG"); p != "" {
		cfg.OnLocalRequest = requestLogger(p)
	}
	cfg.Desktop = os.Getenv("ORYN_DESKTOP") == "1"
	cfg.DesktopCDPAddr = os.Getenv("ORYN_DESKTOP_CDP_ADDR")
	cfg.DesktopTmpDir = os.Getenv("ORYN_DESKTOP_X11_TMPDIR")
	return cfg, nil
}

// TokenFile 返回令牌文件路径（契约 §3：每次拨号前优先读取）。
func TokenFile(home string) string { return filepath.Join(home, ".oryn", "orb-token") }

// readTokenFile 读取令牌文件并去掉首尾空白；不存在或为空时返回空串。
func readTokenFile(home string) string {
	b, err := os.ReadFile(TokenFile(home))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// dialToken 返回本次拨号使用的令牌：令牌文件优先，缺省时用环境变量给出的令牌。
func (r *Runtime) dialToken() string {
	if tok := readTokenFile(r.home); tok != "" {
		return tok
	}
	return r.cfg.Token
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

	// setupSem（容量 1）让自动 setup / resume（含转入后台的 resume）与手动重跑互斥。
	setupSem chan struct{}
	// autoOp 是最近一次自动 setup / resume 请求；semHolder 是当前持有 setupSem 的自动运行（手动重跑或空闲时为 nil）。
	autoMu    sync.Mutex
	autoOp    *autoSetup
	semHolder *autoSetup
	// 以下为最近一次 setup.run / resume.run 的参数（手动重跑用）。
	envMu       sync.Mutex
	setupEnv    map[string]string
	workdir     string
	lastScripts apigen.LinkCtlSetupScripts
	haveParams  bool

	statusMu sync.Mutex
	status   orbsock.Status
	// pendingResults 是尚未成功发出的 setup.result（断连期间结束的运行，重连后补发）。
	resultsMu      sync.Mutex
	pendingResults []apigen.LinkCtlSetupResultParams

	metrics *metricsReader

	// local 是本机 API 的虚拟监听：控制面打开的 fs 流经它交给 localSrv（http.Server）。
	local    *link.StreamListener
	localSrv *http.Server
	// maxFileBytes / maxAttachmentBytes 是 PUT /fs/file 与 PUT /attachments/file 的上限（测试可调小）。
	maxFileBytes       int64
	maxAttachmentBytes int64

	// svc 是受管服务监督器（M4）。
	svc *serviceSupervisor
	// desk 是桌面（M8）；未启用为 nil。
	desk *desktop

	// syncMu 串行化 /sync/prepare（共用仓库里的临时 index 与 refs/oryn/sync/*）。
	syncMu sync.Mutex
}

// conn 是一条已建立并发过 hello 的 link。
type conn struct {
	sess *link.Session
	ctl  *link.Ctl
	done chan struct{}
}

// New 校验配置、绑定 egress 监听并探测 agent 版本。
func New(cfg Config) (*Runtime, error) {
	if cfg.Coordinator {
		if cfg.OrbID == "" || cfg.Connect == nil {
			return nil, errors.New("orbdrt: OrbID and Connect are required in coordinator mode")
		}
	} else if cfg.URL == "" || cfg.OrbID == "" {
		return nil, errors.New("orbdrt: URL and OrbID are required")
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
	if !cfg.Coordinator && cfg.Token == "" && readTokenFile(home) == "" {
		return nil, errors.New("orbdrt: Token is required (ORYN_ORB_TOKEN or $HOME/.oryn/orb-token)")
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
	if cfg.ResumeWait <= 0 {
		cfg.ResumeWait = 10 * time.Second
	}
	if cfg.ThreadPollInterval <= 0 {
		cfg.ThreadPollInterval = DefaultPollInterval
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
	if cfg.DesktopCDPAddr == "" {
		cfg.DesktopCDPAddr = DefaultDesktopCDPAddr
	}
	if cfg.DesktopUserNS == nil {
		cfg.DesktopUserNS = userNamespacesUsable
	}
	if err := os.MkdirAll(filepath.Join(home, ".oryn", "threads"), 0o755); err != nil {
		return nil, fmt.Errorf("orbdrt: %w", err)
	}
	// 上传临时目录：上次运行中断留下的残片在启动时清空（上传不做断点续传）。
	if err := resetDir(filepath.Join(home, ".oryn", "tmp")); err != nil {
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
		setupSem: make(chan struct{}, 1),

		local:              link.NewStreamListener("oryn-fs"),
		maxFileBytes:       MaxFileBytes,
		maxAttachmentBytes: MaxAttachmentBytes,
	}
	r.localSrv = &http.Server{Handler: r.localHandler(), ReadHeaderTimeout: 30 * time.Second}
	r.svc = newServiceSupervisor(r)
	if !cfg.Coordinator {
		// 协调宿主没有桌面，也不写 orb 扩展与技能（协调档的 omp 以 --no-extensions --no-skills 启动）。
		r.desk = newDesktop(r)
		if err := r.writeAgentAssets(); err != nil {
			_ = ln.Close()
			if r.desk != nil {
				_ = r.desk.cdpLn.Close()
			}
			return nil, fmt.Errorf("orbdrt: %w", err)
		}
	}
	r.loadSetupStatus()
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
// 协调宿主模式只起 egress 与 tailer；link 断开（Connect 返回 ErrNoRedial）后即返回。
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
	wg.Go(func() { r.serveEgress(ctx) })
	if !r.cfg.Coordinator {
		wg.Go(func() { r.metricsLoop(ctx) })
		wg.Go(func() { r.serveSocket(ctx) })
		wg.Go(func() { _ = r.localSrv.Serve(r.local) })
		wg.Go(func() { r.svc.reportLoop(ctx) })
	}
	if d := r.desk; d != nil {
		// 显示栈常驻；ctx 结束时三者的进程组都被 SIGKILL（优雅停浏览器由 Shutdown 负责）。
		wg.Go(func() { d.run(ctx) })
		wg.Go(func() { d.serveCDP(ctx) })
		wg.Go(func() { d.reportLoop(ctx) })
	}
	r.linkLoop(ctx)
	// 仍存活的服务进程组直接 SIGKILL（优雅停止由 Shutdown 负责）。
	r.svc.killAll()
	_ = r.egressLn.Close()
	_ = r.localSrv.Close()
	_ = r.local.Close()
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

// Shutdown 优雅停止全部 agent（关闭 stdin、读完输出，超时 SIGKILL），并行停掉全部受管服务（不改期望状态）
// 与桌面上的浏览器，然后在 link 可用时冲刷 tailer。
func (r *Runtime) Shutdown(grace time.Duration) {
	var wg sync.WaitGroup
	wg.Go(func() { r.svc.StopAll(grace) })
	if d := r.desk; d != nil {
		wg.Go(func() { d.stopBrowser(min(grace, browserStopGrace)) })
	}
	r.stopAllAgents(grace)
	wg.Wait()
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
		if ctx.Err() != nil || errors.Is(err, ErrNoRedial) {
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
	var sess *link.Session
	if r.cfg.Connect != nil {
		sess, err = r.cfg.Connect(dctx)
	} else {
		// 令牌文件每次拨号重新读取：控制面每次 spawn 轮换令牌，provider 写文件后发 SIGHUP 触发重拨。
		sess, err = link.Dial(dctx, link.URL(r.cfg.URL), r.dialToken())
	}
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
	r.resultsMu.Lock()
	r.flushResultsLocked(c)
	r.resultsMu.Unlock()
	// 服务与桌面快照在每条新 link 上补发一次（断连期间的变化被丢弃了）。
	if !r.cfg.Coordinator {
		r.svc.markDirty()
	}
	if r.desk != nil {
		r.desk.markDirty()
	}
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

// acceptLoop 分派控制面主动打开的流：`fs` 流交给本机 API 的 http.Server，`portal` 流对接本机端口，
// `desk` 流对接 Xvnc（桌面未启用时关闭），其余类型直接关闭。协调宿主不接受任何控制面发起的流。
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
		if r.cfg.Coordinator {
			r.log.Warn("coordinator host refuses streams from the control plane", "t", st.Header.T)
			_ = st.Close()
			continue
		}
		switch st.Header.T {
		case link.StreamFS:
			r.local.Push(rollingConn{Conn: st, d: localWriteTimeout})
		case link.StreamPortal:
			go r.servePortal(st)
		case link.StreamDesk:
			if r.desk == nil {
				_ = st.Close()
				continue
			}
			go r.serveDesk(st)
		default:
			r.log.Warn("unexpected stream from control plane", "t", st.Header.T)
			_ = st.Close()
		}
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
	features := []string{"fs", "git", "attachments", "services", "portal", "sync", "threads"}
	if r.desk != nil {
		features = append(features, "desktop")
	}
	if r.cfg.Coordinator {
		features = []string{"coordinator"}
	}
	return apigen.LinkCtlHelloParams{
		OrbdVersion:  r.cfg.Version,
		AgentVersion: r.agentVersion,
		Os:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Threads:      threads,
		Features:     features,
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
	if r.cfg.Coordinator && !coordinatorMethods[method] {
		return nil, link.ErrMethodNotFound
	}
	switch method {
	case "setup.run", "resume.run":
		p, err := decode[apigen.LinkCtlSetupRunParams](params)
		if err != nil {
			return nil, err
		}
		if r.cfg.Coordinator {
			return r.coordinatorSetup(p)
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
	case string(apigen.MethodPortalConfigure):
		p, err := decode[apigen.LinkCtlPortalConfigureParams](params)
		if err != nil {
			return nil, err
		}
		primary := ""
		if p.PrimaryThreadID != nil && validThreadID(*p.PrimaryThreadID) {
			primary = *p.PrimaryThreadID
		}
		r.svc.configure(p.URLTemplate, primary)
		return apigen.LinkCtlOkResult{Ok: true}, nil
	case string(apigen.MethodProfileMode):
		if r.desk == nil {
			return nil, ctlErr("not_supported", "the desktop is not enabled on this orb")
		}
		p, err := decode[apigen.LinkCtlProfileModeParams](params)
		if err != nil {
			return nil, err
		}
		return r.desk.setProfileMode(p)
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

// CodeForeignThread 是控制面对不该由本 orb 续传的线程返回的 ctl 错误码：快照源 orb 的线程，或已被删除的线程。
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

// baseEnv 是 orbd 子进程（agent、setup 脚本、受管服务）的基础环境：继承 orbd 环境但去掉 orb 令牌；
// 桌面启用时设 DISPLAY=:1。
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
	if r.desk != nil {
		env["DISPLAY"] = desktopDisplay
	}
	return env
}

// reservedPort 报告端口是否被 orbd 保留：egress 端口、CDP 前门端口与 Chromium 当前的 DevTools 端口。
// 保留端口不经 portal 暴露，也不能分配给受管服务（M8 计划 2.11）。
func (r *Runtime) reservedPort(port int) bool {
	if port <= 0 {
		return false
	}
	if tcp, ok := r.egressLn.Addr().(*net.TCPAddr); ok && port == tcp.Port {
		return true
	}
	if d := r.desk; d != nil && (port == d.cdpPort() || port == d.devtoolsPort()) {
		return true
	}
	return false
}

func envList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
