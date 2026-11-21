// Package server 组装 orynd 的全部组件（cmd/orynd 与端到端测试共用）。
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/Lynricsy/Oryn/internal/artifacts/sample"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/boxes"
	"github.com/Lynricsy/Oryn/internal/cfgsync"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/coord"
	"github.com/Lynricsy/Oryn/internal/coordhost"
	"github.com/Lynricsy/Oryn/internal/desktop"
	"github.com/Lynricsy/Oryn/internal/github"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/memory/relayroute"
	"github.com/Lynricsy/Oryn/internal/modelcat"
	"github.com/Lynricsy/Oryn/internal/notify"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/orbtls"
	"github.com/Lynricsy/Oryn/internal/ownermem"
	"github.com/Lynricsy/Oryn/internal/portal"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/search"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/sshgw"
	"github.com/Lynricsy/Oryn/internal/stats"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// evalInterval 是 orb 空闲评估周期；0 = orbs 默认（30s）。测试可调小。
var evalInterval time.Duration

// notifyDoneDelay / notifyInputDelay 是通知去抖时长；0 = notify 默认（3s / 2s）。测试可调小。
var notifyDoneDelay, notifyInputDelay time.Duration

// boxesTick / boxesUnlinkedAfter 是 box 对账周期与「ready 却没有 link」多久之后重新拉起；零值 = boxes 默认（30s / 90s）。
// e2e 在 newStack 之前设、t.Cleanup 里还原。
var boxesTick, boxesUnlinkedAfter time.Duration

// scheduleNow / scheduleAfterFunc 是定时（与协调层限速、进度图、主线的静止与收件合并定时器）的时钟；零值 = 真实时钟。e2e 注入。
var (
	scheduleNow       func() time.Time
	scheduleAfterFunc func(d time.Duration, f func()) (stop func())
)

// coordhostCommand 是拉起协调宿主子进程的命令；零值 = coordhost 默认（{os.Executable(), "coordinator-host"}）。e2e 注入。
var coordhostCommand []string

// renderExe 返回带 render-exec 子命令的可执行文件（orynd 自己）；返回空串时控制面渲染停用。
// 测试置空：测试二进制不认 render-exec，被当成 orynd 拉起会把整套测试再跑一遍。
var renderExe = os.Executable

// statsNow / statsBudgetInterval 是统计服务的时钟与预算评估周期；零值 = stats 默认（真实时钟 / 60s）。e2e 注入。
var (
	statsNow            func() time.Time
	statsBudgetInterval time.Duration
)

// Server 持有控制面全部组件。
type Server struct {
	Config      config.Orynd
	Store       *store.Store
	Vault       *secrets.Vault
	Home        *agentcfg.Home
	Auth        *auth.Service
	Tickets     *auth.Tickets
	Jobs        *jobs.Queue
	Orbs        *orbs.Service
	Threads     *threads.Service
	Attachments *attachments.Service
	// Library 是全局助手文件库（M13）。
	Library *library.Service
	// LibrarySync 把文件库同步进协调宿主，并从 orb 取回文件（M13）。
	LibrarySync *library.Syncer
	// Artifacts 是 M14 的 artifact 存储与版本（实时通知经 Hub）。
	Artifacts *artifacts.Service
	// ProjectHomes 是项目级 omp 配置（M12.2）。
	ProjectHomes *agentcfg.ProjectHomes
	// Desktop 是浏览器 Profile 与桌面 WS（M8）。
	Desktop *desktop.Service
	// Host 是协调宿主（M9，内置 provider @host）；Schedules 是定时；Coord 是 Space / 助手 / 宿主工具。
	Host      *coordhost.Host
	Schedules *schedule.Service
	Coord     *coord.Service
	// Stats 是 M10 的统计后台（提交采样、保留期、预算）与 meta / 预算接口。
	Stats *stats.Service
	// GitHub 是 M14 的 GitHub 集成。
	GitHub *github.Service
	// SSH 是 M18 的 SSH 主机目录与控制面 SSH 网关。
	SSH *sshgw.Service
	// Boxes 是 M23 的 box（常驻机器与应用部署）。
	Boxes *boxes.Service
	// Memory 是 M16 的内置 Hindsight 记忆（连接设置、状态与 .env 生成）。
	Memory *memory.Service
	// OwnerMemory 是 M17 全局助手的主人记忆（bank `oryn-owner`，连接与 key 与 Memory 共用）。
	OwnerMemory *ownermem.Service
	Hub         *realtime.Hub
	Notify      *notify.Service
	Share       *share.Service
	// Portal 是 portal 数据面；未配置 portal_domain 时为 nil。
	Portal  *portal.Service
	API     *api.Server
	Handler http.Handler
	// OrbTLS 是 orb link 专用 TLS 监听的服务端配置（orb_tls.listen 为空时为 nil）；OrbCAPEM 是其 CA 证书，
	// 经 spawn 参数下发给 orb。
	OrbTLS   *tls.Config
	OrbCAPEM []byte

	closers []func()
}

// New 打开存储并构造全部组件；调用方负责 Close。
func New(ctx context.Context, cfg config.Orynd) (*Server, error) {
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{Config: cfg, Store: st}
	s.closers = append(s.closers, func() { _ = st.Close() })
	if err := s.init(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Server) init(ctx context.Context) error {
	cfg := s.Config
	base, err := portalhost.NewBase(cfg.PublicURL, cfg.PortalDomain)
	if err != nil {
		return err
	}
	// threadPortalTemplate 是线程的 portal 地址模板（短码已代入、保留 {port}）；未启用 portal 时为空。
	threadPortalTemplate := func(threadID string) string {
		code, ok := portalhost.Code(threadID)
		if !base.Enabled() || !ok {
			return ""
		}
		return portalhost.Expand(base.Template(), code, 0)
	}
	box, err := secrets.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return err
	}
	s.Vault = secrets.NewVault(s.Store, box)
	if s.Home, err = agentcfg.NewHome(cfg.DataDir); err != nil {
		return err
	}
	if s.ProjectHomes, err = agentcfg.NewProjectHomes(cfg.DataDir); err != nil {
		return err
	}
	if s.Auth, err = auth.NewService(s.Store, cfg.DataDir, cfg.PublicURL); err != nil {
		return err
	}
	s.Tickets = auth.NewTickets()

	s.Jobs = jobs.New(s.Store)
	key, err := orbs.LoadOrCreateTokenKey(cfg.DataDir)
	if err != nil {
		return err
	}
	if len(cfg.OrbTLS.Listen) > 0 {
		if s.OrbTLS, s.OrbCAPEM, err = orbtls.Load(cfg.DataDir, cfg.OrbTLS.ListenIPs(), cfg.OrbTLS.Hosts); err != nil {
			return err
		}
	}
	// 协调宿主 /mcp/ 路由的凭据（M19 D10）：每次启动随机生成，只交给全局助手的 agent（协调宿主随 orynd 一起重启）
	mcpToken := ids.Token("mcp")
	setup := &orbs.DefaultSetup{
		Store: s.Store, Home: s.Home, Vault: s.Vault, Projects: s.ProjectHomes,
		CoordinatorEgressAddr: fmt.Sprintf("127.0.0.1:%d", cfg.Coordinator.EgressPort),
		CoordinatorWebAllow:   lowerAll(cfg.Coordinator.WebAllowHosts),
		CoordinatorMCPToken:   mcpToken,
	}
	s.Orbs = orbs.New(orbs.Config{
		Idle: cfg.Idle, Defaults: cfg.OrbDefaults, PublicURL: cfg.PublicURL, TokenKey: key, EvalInterval: evalInterval,
		PortalURLTemplate: base.Template(), Snapshots: cfg.Snapshots, Pool: cfg.Pool, OrbCAPEM: string(s.OrbCAPEM),
	}, s.Store, s.Jobs, setup, s.Vault)
	// 线程服务须在 orb 服务接受 link / 发出事件之前构造：它在构造时注册 entries.* / agent.* 处理器
	s.Threads = threads.New(threads.Config{
		DataDir: cfg.DataDir, WakeOnFocus: cfg.Idle.WakeOnFocus, PortalURLTemplate: threadPortalTemplate, Memory: lazyMemory{s},
		Projects: s.ProjectHomes,
		BoxThreadsChanged: func() {
			if s.Boxes != nil {
				s.Boxes.Touch()
			}
		},
	}, s.Store, orbsAdapter{s.Orbs})
	// M23：box。须在 ctl 方法登记（box.call 与 ssh.call 同处）之前构造，并在 orb 服务接受 link 之前注册闸门；
	// s.Hub 在后面才构造，回调晚绑定
	s.Boxes = boxes.New(boxes.Config{
		Base: base, Box: box,
		Changed: func() {
			if s.Hub != nil {
				s.Hub.BoxesChanged()
			}
		},
		Tick: boxesTick, UnlinkedAfter: boxesUnlinkedAfter,
	}, s.Store, s.Orbs, boxThreads{s.Store})
	// M18：SSH 主机与网关。主机变化经 omp 下发链路推到 orb（ssh.json 摘要参与比较，线程据此 reload_plugins）；
	// 回调在被调用时才取 s.Orbs / s.Hub（s.Hub 在后面才构造）
	if s.SSH, err = sshgw.New(sshgw.Config{
		Box: box, DataDir: cfg.DataDir,
		Apply:   func(projectID string) { s.Orbs.ScheduleOmpApply(projectID) },
		Changed: func() { s.Hub.SshChanged() },
	}, s.Store, s.Orbs); err != nil {
		return err
	}
	setup.SSH = s.SSH
	// M21：runner 的无密钥投递校验与分享前扫描同一来源；runner 线程的 SSH 身份与项目层附加交付
	secretSrc := shareSecrets{s.Vault, s.SSH}
	setup.KnownSecrets = secretSrc.All
	s.Threads.SetRunnerAgent(runnerAgent{Service: s.SSH, orbs: s.Orbs, secrets: secretSrc})
	s.Orbs.HandleStream(link.StreamSSH, s.SSH.ServeStream)
	// ssh.call：orb 内 agent 的 ssh_exec_many（runner 的 link 不放行，见 orbs.runnerRequests）
	s.Orbs.HandleRequest(string(apigen.MethodSSHCall), s.SSH.OnCall)
	// box.call：orb 内 `oryn boxes …`（M23；runner 的 link 不放行）
	s.Orbs.HandleRequest(string(apigen.MethodBoxCall), s.Boxes.OnCall)
	if s.Attachments, err = attachments.New(s.Store, cfg.DataDir, s.Orbs); err != nil {
		return err
	}
	s.Threads.SetAttachments(s.Attachments)
	// M12.2：/omfg 保存的规则由控制面读回并写入中央（全局 omp-home 或项目层）；M13：写入后同样立即下发
	s.Threads.SetConfigSync(&cfgsync.Saver{
		Store: s.Store, Orbs: s.Orbs, Global: s.Home, Projects: s.ProjectHomes, Vault: s.Vault,
		Applied: s.Orbs.ScheduleOmpApply,
	})
	// M13：agent-config 写入后防抖下发到运行中的 orb，再按变化让会话 reload_plugins 或标为需重启
	if err := s.Orbs.EnableOmpApply(orbs.OmpApplyConfig{
		Home: s.Home, Projects: s.ProjectHomes, Dir: filepath.Join(cfg.DataDir, "omp-home-applied"),
		SSHState: s.SSH.LayerState,
	}); err != nil {
		return err
	}
	s.Orbs.OnOmpApplied(func(rep orbs.ApplyReport) { s.Threads.ConfigApplied(context.Background(), rep) })
	if s.Library, err = library.New(s.Store, cfg.DataDir, s.Attachments); err != nil {
		return err
	}
	// 同步器在 orb 服务接受 link 之前注册 library 流的处理器
	s.LibrarySync = library.NewSyncer(s.Library, s.Orbs)
	s.Threads.SetLibrary(library.ThreadFiles{S: s.Library, Sync: s.LibrarySync})
	// 浏览器 Profile（M8）：在 orb 服务接受 link 之前注册生命周期钩子
	if s.Desktop, err = desktop.New(desktop.Config{DataDir: cfg.DataDir, Box: box, Valid: s.Auth}, s.Store, s.Orbs, s.Tickets); err != nil {
		return err
	}
	s.closers = append(s.closers, s.Desktop.Close)
	s.Threads.SetProfiles(s.Desktop)
	s.Auth.Subscribe(s.Desktop.Revoke)
	s.Auth.Subscribe(s.SSH.Revoke)
	// 吊销时同时作废该主体未兑换的 WS 票据，连接登记后再向库复核一次主体（H3）
	s.Auth.Subscribe(s.Tickets.Revoke)
	s.Hub = realtime.New(s.Threads, s.Tickets, revocations{s.Auth}, s.Auth)
	if s.Artifacts, err = artifacts.New(artifacts.Config{DataDir: cfg.DataDir, PublicURL: cfg.PublicURL}, s.Store, s.Hub); err != nil {
		return err
	}
	// 实时画布：观看者变化与 artifact.canvas 经 Hub；关停时把未落版本的画布操作写成版本（在关库之前）
	s.Artifacts.AttachCanvasHub(s.Hub)
	s.closers = append(s.closers, s.Artifacts.FlushCanvases)
	// 在场（步骤 6.7）：观看者与 agent 活动合并成 artifact.activity 发给观看者；回合结束的清理在 RunActivity 里
	s.Artifacts.AttachActivityHub(artifactViewers{s.Hub})
	// 用户同意（步骤 7.3）：files / sample 的同意请求发给观看者；没人在看时直接按拒绝处理
	s.Artifacts.AttachConsentHub(artifactViewers{s.Hub})
	// files 能力（步骤 7.4）：经 orb 本机 API 只读地取创建线程的工作目录
	s.Artifacts.AttachOrbFS(s.Orbs)
	// sample 能力（步骤 7.5）：控制面用钉住的 Core（coordinator.omp_path）一次性回答页面的问题，不经过 orb
	s.Artifacts.AttachSampler(sample.New(sample.Config{OmpPath: cfg.Coordinator.OmpPath, Home: s.Home, Vault: s.Vault}))
	// artifact.call（orb / Runner 内 agent 的 artifact 工具）：须在 orb 服务接受 link 之前注册
	s.Artifacts.RegisterOrbMethod(s.Orbs, s.LibrarySync)
	s.Notify, err = notify.New(notify.Config{
		PublicURL: cfg.PublicURL, DataDir: cfg.DataDir, Box: box, DoneDelay: notifyDoneDelay, InputDelay: notifyInputDelay,
		FCM: notify.FCMConfig{Secret: cfg.Push.FCM.ServiceAccountSecret, Secrets: s.Vault},
	}, s.Store, s.Threads, s.Hub)
	if err != nil {
		return err
	}
	// M9：协调宿主（进程内认领 @host 的任务）→ 定时 → 协调层；观察者按 notify、定时、协调的顺序注册。
	// 协调层在构造时向 orbs 注册 orb 内的 thread.* / schedule.* ctl 方法，须在接受 link 之前。
	hostCfg := coordhost.ConfigFrom(cfg, "")
	if coordhostCommand != nil {
		hostCfg.Command = coordhostCommand
	}
	s.Host = coordhost.New(hostCfg, s.Store, s.Jobs, s.Orbs)
	s.Schedules = schedule.New(schedule.Config{
		Now: scheduleNow, AfterFunc: scheduleAfterFunc,
		DeliverTimeout: cfg.Schedule.DeliverTimeout.D(), MaxFailures: cfg.Schedule.MaxFailures,
		Changed: s.Hub.SchedulesChanged,
		// M19 3.3：交给助手的运行结果进主线（coord 晚于 schedule 构造，用闭包）
		HandOff: func(ctx context.Context, h schedule.HandOff) { s.Coord.PostToMain(ctx, h) },
	}, s.Store, s.Threads, s.Orbs, s.Notify)
	s.Coord = coord.New(coord.Config{Now: scheduleNow, AfterFunc: scheduleAfterFunc, Model: cfg.Coordinator.Model, PublicURL: cfg.PublicURL, MCPToken: mcpToken},
		s.Store, s.Threads, s.Orbs, s.Host, s.Schedules)
	s.Coord.SetLibrary(s.Library, s.LibrarySync)
	s.Coord.SetArtifacts(s.Artifacts)
	s.Coord.SetAgentConfig(s.Home, s.ProjectHomes, s.Vault)
	s.Coord.SetMachines(coord.OrbMachines{Service: s.Orbs, Q: s.Store.Q})
	s.Coord.SetBoxes(s.Boxes)
	s.Threads.SetCoordinator(s.Coord)
	s.Threads.AddObserver(s.Notify)
	s.Threads.AddObserver(s.Schedules)
	s.Threads.AddObserver(s.Coord)
	// M14：GitHub 集成（webhook、轮询、回流在 Run 里；线程按自动化身份给新 orb 取 GitHub 身份）
	s.GitHub = github.New(github.Config{
		PublicURL: cfg.PublicURL, APIURL: cfg.GitHub.APIURL, WebURL: cfg.GitHub.WebURL, Box: box, Changed: s.Hub.GitHubChanged,
		HandOff: func(ctx context.Context, h schedule.HandOff) { s.Coord.PostToMain(ctx, h) },
	}, s.Store, s.Threads, s.Notify, s.Attachments)
	s.Threads.SetGitHub(s.GitHub)
	// M19 第三层：全局助手的触发规则工具
	s.Coord.SetTriggers(s.GitHub)
	// orb 的 setup 计划与出站代理：工作 orb 下发 gh 登录名、提交身份，出站代理注入身份槽位的 OAuth 令牌
	setup.GitHub = s.GitHub
	s.Orbs.SetGitHub(s.GitHub)
	// M15 4.3：issue 任务进入终态时冻结 orb；GitHub 投递的回合结束时检查完成提醒
	s.GitHub.SetOrbs(githubOrbs{s.Orbs})
	s.Threads.AddObserver(s.GitHub.TurnObserver())
	// M16：内置 Hindsight 记忆。Hindsight 的 key 用 Box 加密存库；没有总超时（网关转发的 reflect 可能很慢），
	// 探测与转发各自用 context 限时
	// 模型中转按控制面 models.yml 的出站路由解析模型的协议与上游（密钥注入与脱敏沿用 egress）
	memRoutes := relayroute.New(s.Home, s.Vault)
	s.Memory = memory.New(memory.Config{
		Family: memRoutes.Family, Upstream: memRoutes.Upstream, Restart: s.Threads.MarkMemoryRestart,
		Q: s.Store.Q, Box: box, PublicURL: cfg.PublicURL, HTTP: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   8,
			ForceAttemptHTTP2:     true,
		}},
	})
	// 记忆网关：orb 出站代理的 /memory/t/<threadId>/… 经它强制 bank、标签与放行清单（步骤 2.2）
	s.Orbs.SetMemory(memory.NewGateway(s.Memory))
	// M10：统计（提交采样观察线程转入 idle；预算告警经 notify 的系统通知送达）；M14：采样落库后按提交关联 GitHub PR
	s.Stats = stats.New(stats.Config{
		Now: statsNow, Retention: cfg.Stats.Retention, BudgetInterval: statsBudgetInterval, CommitsSampled: s.GitHub.ThreadCommitsSampled,
	}, s.Store, s.Orbs, s.Notify)
	s.GitHub.SetSampler(s.Stats)
	s.Threads.AddObserver(s.Stats)
	s.Threads.AddObserver(s.Artifacts.TurnObserver())
	home := s.Home
	s.Share = share.New(share.Config{PublicURL: cfg.PublicURL, RoleNames: func() map[string]string {
		// 分享页 role 的显示名（config.yml 的 modelTags.<role>.name）；config.yml 解析失败时不给名字
		roles, err := home.CycleRoles()
		if err != nil {
			return map[string]string{}
		}
		names := make(map[string]string, len(roles))
		for _, r := range roles {
			if r.Name != "" {
				names[r.Role] = r.Name
			}
		}
		return names
	}, RoleKeySecret: share.DeriveRoleKeySecret(key)}, s.Store, s.Threads, s.Hub, s.Attachments, s.Orbs, secretSrc)
	// M14 artifact 渲染帧与发布页（步骤 2.1、8.2）：配置了 portal_domain 时在 a-<令牌> / p-<slug>.<portal_domain>
	// （包在 portal 中间件外层分流），否则挂在主域的 /_af/<令牌>/ 与 /p/<slug>/（帧请求不带 cookie，令牌就是凭证；
	// 发布页的访问由访问级别与授权段决定），SPA 的 CSP 相应放行 frame-src 'self'
	frames := artifacts.NewHost(s.Artifacts, artifacts.HostConfig{Base: base})
	// M14 发布（步骤 8.1）：授权令牌用 orb 令牌主密钥派生的密钥签名；发布为 code / public 前用分享的密钥扫描规则；
	// 访客数来自发布页的心跳（发布状态与在场推送都用它）
	s.Artifacts.ConfigurePublishing(artifacts.PublishConfig{
		Base: base, GrantKey: artifacts.DeriveGrantKey(key), Scan: s.Share.ScanContent, Visitors: frames.Visitors,
	})
	s.Artifacts.SetActivityVisitors(frames.ArtifactVisitors)
	// M17：主人记忆。写入与工具输出先用分享的密钥扫描规则与密钥库值脱敏
	s.OwnerMemory = ownermem.New(ownermem.Config{Q: s.Store.Q, Box: box, Memory: s.Memory, Redact: s.Share.RedactText})
	s.Coord.SetOwnerMemory(s.OwnerMemory, s.Memory)
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	go s.Jobs.Run(runCtx)
	go s.Orbs.Run(runCtx)
	go s.Auth.Run(runCtx)
	go s.Attachments.Run(runCtx)
	go s.Library.Run(runCtx)
	go s.Artifacts.Run(runCtx)
	go s.Artifacts.RunActivity(runCtx)
	go s.LibrarySync.Run(runCtx)
	go s.Notify.Run(runCtx)
	go s.Host.Run(runCtx)
	go s.Schedules.Run(runCtx)
	go s.Coord.Run(runCtx)
	go s.Stats.Run(runCtx)
	go s.GitHub.Run(runCtx)
	go s.SSH.Run(runCtx)
	go s.Boxes.Run(runCtx)
	// M16：重试删除失败的 Hindsight 记忆，并在记忆启用时保证 user-preferences 存在
	go s.Memory.RunTombstones(runCtx)
	// M17：主人记忆写入循环（助手对话与线程空闲 30 分钟、归档或交付后写入 bank oryn-owner）
	go s.OwnerMemory.Run(runCtx)
	// 事实行版本不符时在后台回填（可中断，下次启动从游标续作）
	go func() {
		if err := threads.BackfillFacts(runCtx, s.Store, nil, false); err != nil && runCtx.Err() == nil {
			slog.Warn("统计事实行回填失败", "err", err)
		}
	}()
	// 对话内容索引版本不符时在后台回填（可中断，下次启动从游标续作）；期间新条目照常由触发器入索引
	go func() {
		if err := search.Backfill(runCtx, s.Store); err != nil && runCtx.Err() == nil {
			slog.Warn("对话内容索引回填失败", "err", err)
		}
	}()
	// orb link 与 whoami（M11b）在同一路由组：明文监听与只放行 /orb/v1/* 的 TLS 监听都可达
	mounts := map[string]http.Handler{"/orb/v1/link": s.Orbs.LinkHandler(), "/orb/v1/whoami": s.Orbs.WhoamiHandler(), api.WSPath: s.Hub}
	// 公开面（M5）：匿名 WS、分享页与 .md 导出、公开文件；均不经 /api/v1 的鉴权组
	mounts["/api/v1/public/shares/{slug}/ws"] = s.Share.WSHandler()
	mounts["/s/*"] = s.Share.PageHandler(webui.Handler(base.FrameSrc()))
	mounts["/f/*"] = s.Share.FileHandler()
	mounts[desktop.WSPath] = s.Desktop.WSHandler()
	mounts[sshgw.TerminalWSPath] = s.SSH.TerminalHandler(s.Tickets, s.Auth)
	// M16 模型中转：Hindsight 凭中转令牌调用，不经 /api/v1 的鉴权组
	mounts[memory.RelayPathPrefix+"*"] = s.Memory.RelayHandler()
	frameSrc := base.FrameSrc()
	if !base.Enabled() {
		frameSrc = "'self'"
		mounts[artifacts.FramePathPrefix+"*"] = frames.Handler()
		mounts[artifacts.PublishPathPrefix+"*"] = frames.PublicHandler()
	}
	// M14 控制面渲染（步骤 4.1）：headless Chromium 只经一个放行白名单的代理取 artifact 内容；启动时在后台自检一次
	exe, err := renderExe()
	if err != nil {
		cancel()
		return fmt.Errorf("render: %w", err)
	}
	rc := cfg.Artifacts.Render
	renderer, err := render.New(render.Config{
		Chromium: rc.Chromium, User: rc.User, NoSandbox: rc.NoSandbox, Concurrency: rc.Concurrency,
		Dir: cfg.ArtifactRenderDir(), Exe: exe, CDNs: artifacts.CDNs, Site: frames,
	})
	if err != nil {
		cancel()
		return err
	}
	s.closers = append(s.closers, renderer.Close)
	go renderer.Preflight(runCtx)
	// 缩略图（新版本后延迟入队）、render.png 与 screenshot_artifact 都经 Service 用这个渲染器
	s.Artifacts.UseRenderer(runCtx, renderer)
	deps := api.Deps{
		Config:         cfg,
		Store:          s.Store,
		AuthMiddleware: auth.Middleware(s.Store),
		Mounts:         mounts,
		FrameSrc:       frameSrc,
	}
	if base.Enabled() {
		s.Portal = portal.New(portal.Config{Base: base}, s.Store, s.Orbs)
		go s.Portal.Run(runCtx)
		s.Auth.Subscribe(s.Portal.Revoke)
		deps.Portal = func(next http.Handler) http.Handler { return frames.Middleware(s.Portal.Middleware(next)) }
		mounts["/portal/auth"] = s.Portal.AuthHandler()
	}
	s.closers = append(s.closers, func() { cancel(); s.Threads.Close(); s.Orbs.Close() })

	s.API = &api.Server{Deps: deps}
	// App 会话兑换（M6）：不在 /api/v1 下，壳以顶层导航到达
	mounts[api.AppSessionPath] = s.API.AppSessionHandler()
	s.API.Auth = &api.AuthDeps{Service: s.Auth, Tickets: s.Tickets, Vault: s.Vault, Home: s.Home, ProjectHomes: s.ProjectHomes}
	// M13：中央模型目录（控制面用协调宿主的 omp 跑 `omp models`）；未配置 omp 时不建，接口回 503
	if cfg.Coordinator.OmpPath != "" {
		s.API.Auth.ModelCatalog = modelcat.New(modelcat.Config{OmpPath: cfg.Coordinator.OmpPath, CacheDir: cfg.ModelCatalogDir()}, s.Home, s.Vault)
		// 6.3：config.yml 的键是否要重启会话按钉住 Core 的设置 schema 判断
		s.Threads.SetConfigSchema(s.API.Auth.ModelCatalog)
		// M15 R14：GitHub 的 `/model <别名>` 指令在中央模型目录里查找
		s.GitHub.SetModelCatalog(githubModels{s.API.Auth.ModelCatalog})
	}
	s.API.Orbs = &api.OrbsDeps{Service: s.Orbs}
	s.API.Threads = &api.ThreadsDeps{Service: s.Threads}
	s.API.Attachments = &api.AttachmentsDeps{Service: s.Attachments}
	s.API.Library = &api.LibraryDeps{Service: s.Library}
	s.API.Shares = &api.ShareDeps{Service: s.Share}
	s.API.Notify = &api.NotifyDeps{Service: s.Notify}
	s.API.Desktop = &api.DesktopDeps{Service: s.Desktop}
	s.API.Coord = &api.CoordDeps{Service: s.Coord, Host: s.Host}
	s.API.Schedules = &api.ScheduleDeps{Service: s.Schedules}
	s.API.Stats = &api.StatsDeps{Service: s.Stats}
	s.API.Search = &api.SearchDeps{Service: search.New(s.Store)}
	s.API.GitHub = &api.GitHubDeps{Service: s.GitHub}
	s.API.Memory = &api.MemoryDeps{Service: s.Memory}
	s.API.OwnerMemory = &api.OwnerMemoryDeps{Service: s.OwnerMemory}
	s.API.SSH = &api.SSHDeps{Service: s.SSH}
	s.API.Boxes = &api.BoxesDeps{Service: s.Boxes}
	s.API.Artifacts = &api.ArtifactsDeps{Service: s.Artifacts, Host: frames, Render: renderer}
	if s.Portal != nil {
		s.API.PortalShares = &api.PortalDeps{Service: s.Portal}
	}
	// M19 第三层：全局助手的项目工具与 REST 共用同一套校验与副作用（依赖上面装好的 GitHub、Orbs、Auth、Memory）
	s.Coord.SetProjects(api.ProjectOps{S: s.API})
	// M19 第三层：全局助手的对外分享工具；没有配置 portal_domain 时不给 Portal（避免把 nil 指针装进接口）
	var portals coord.PortalShares
	if s.Portal != nil {
		portals = s.Portal
	}
	s.Coord.SetShares(s.Share, portals)
	s.Handler = api.NewRouter(s.API)
	return nil
}

// AnnounceSetup 在尚无所有者时生成初始化令牌并打印链接。
func (s *Server) AnnounceSetup(ctx context.Context) error {
	u, created, err := auth.EnsureSetupToken(ctx, s.Store, s.Config.DataDir, s.Config.PublicURL)
	if err != nil {
		return err
	}
	if created {
		slog.Info("尚未初始化，打开以下链接注册 Passkey", "url", u)
		fmt.Println("Oryn 初始化链接：" + u)
	}
	return nil
}

// Close 按逆序释放资源。
func (s *Server) Close() {
	for i := len(s.closers) - 1; i >= 0; i-- {
		s.closers[i]()
	}
	s.closers = nil
}

// githubModels 把中央模型目录适配为 github.ModelCatalog（github 不导入 modelcat）：取缓存中的目录（不强制刷新）。
type githubModels struct{ c *modelcat.Catalog }

func (m githubModels) Models(ctx context.Context) ([]apigen.ModelRef, error) {
	r, err := m.c.Get(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.ModelRef, 0, len(r.Models))
	for _, mm := range r.Models {
		out = append(out, apigen.ModelRef{Provider: mm.Provider, ID: mm.ID})
	}
	return out, nil
}

// githubOrbs 把 orbs 服务适配为 github.Orbs：冻结 = orbs.Pause(freeze)。
type githubOrbs struct{ o *orbs.Service }

func (g githubOrbs) Freeze(ctx context.Context, orbID string) error {
	return g.o.Pause(ctx, orbID, orbs.PauseFreeze)
}

// lowerAll 把主机名表转成小写（出站代理按小写比较放行表）。
func lowerAll(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}
