// Package server 组装 orynd 的全部组件（cmd/orynd 与端到端测试共用）。
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/desktop"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/notify"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/portal"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// evalInterval 是 orb 空闲评估周期；0 = orbs 默认（30s）。测试可调小。
var evalInterval time.Duration

// notifyDoneDelay / notifyInputDelay 是通知去抖时长；0 = notify 默认（3s / 2s）。测试可调小。
var notifyDoneDelay, notifyInputDelay time.Duration

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
	// Desktop 是浏览器 Profile 与桌面 WS（M8）。
	Desktop *desktop.Service
	Hub     *realtime.Hub
	Notify  *notify.Service
	Share   *share.Service
	// Portal 是 portal 数据面；未配置 portal_domain 时为 nil。
	Portal  *portal.Service
	API     *api.Server
	Handler http.Handler

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
	if s.Auth, err = auth.NewService(s.Store, cfg.DataDir, cfg.PublicURL); err != nil {
		return err
	}
	s.Tickets = auth.NewTickets()

	s.Jobs = jobs.New(s.Store)
	key, err := orbs.LoadOrCreateTokenKey(cfg.DataDir)
	if err != nil {
		return err
	}
	s.Orbs = orbs.New(orbs.Config{
		Idle: cfg.Idle, Defaults: cfg.OrbDefaults, PublicURL: cfg.PublicURL, TokenKey: key, EvalInterval: evalInterval,
		PortalURLTemplate: base.Template(),
	}, s.Store, s.Jobs, &orbs.DefaultSetup{Store: s.Store, Home: s.Home, Vault: s.Vault}, s.Vault)
	// 线程服务须在 orb 服务接受 link / 发出事件之前构造：它在构造时注册 entries.* / agent.* 处理器
	s.Threads = threads.New(threads.Config{
		DataDir: cfg.DataDir, WakeOnFocus: cfg.Idle.WakeOnFocus, PortalURLTemplate: threadPortalTemplate,
	}, s.Store, orbsAdapter{s.Orbs})
	if s.Attachments, err = attachments.New(s.Store, cfg.DataDir, s.Orbs); err != nil {
		return err
	}
	s.Threads.SetAttachments(s.Attachments)
	// 浏览器 Profile（M8）：在 orb 服务接受 link 之前注册生命周期钩子
	if s.Desktop, err = desktop.New(desktop.Config{DataDir: cfg.DataDir, Box: box, Valid: s.Auth}, s.Store, s.Orbs, s.Tickets); err != nil {
		return err
	}
	s.closers = append(s.closers, s.Desktop.Close)
	s.Threads.SetProfiles(s.Desktop)
	s.Auth.Subscribe(s.Desktop.Revoke)
	// 吊销时同时作废该主体未兑换的 WS 票据，连接登记后再向库复核一次主体（H3）
	s.Auth.Subscribe(s.Tickets.Revoke)
	s.Hub = realtime.New(s.Threads, s.Tickets, revocations{s.Auth}, s.Auth)
	s.Notify, err = notify.New(notify.Config{
		PublicURL: cfg.PublicURL, DataDir: cfg.DataDir, Box: box, DoneDelay: notifyDoneDelay, InputDelay: notifyInputDelay,
		FCM: notify.FCMConfig{Secret: cfg.Push.FCM.ServiceAccountSecret, Secrets: s.Vault},
	}, s.Store, s.Threads, s.Hub)
	if err != nil {
		return err
	}
	s.Threads.SetObserver(s.Notify)
	s.Share = share.New(share.Config{PublicURL: cfg.PublicURL}, s.Store, s.Threads, s.Hub, s.Attachments, s.Orbs, s.Vault)
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	go s.Jobs.Run(runCtx)
	go s.Orbs.Run(runCtx)
	go s.Auth.Run(runCtx)
	go s.Attachments.Run(runCtx)
	go s.Notify.Run(runCtx)
	mounts := map[string]http.Handler{"/orb/v1/link": s.Orbs.LinkHandler(), api.WSPath: s.Hub}
	// 公开面（M5）：匿名 WS、分享页与 .md 导出、公开文件；均不经 /api/v1 的鉴权组
	mounts["/api/v1/public/shares/{slug}/ws"] = s.Share.WSHandler()
	mounts["/s/*"] = s.Share.PageHandler(webui.Handler(base.FrameSrc()))
	mounts["/f/*"] = s.Share.FileHandler()
	mounts[desktop.WSPath] = s.Desktop.WSHandler()
	deps := api.Deps{
		Config:         cfg,
		Store:          s.Store,
		AuthMiddleware: auth.Middleware(s.Store),
		Mounts:         mounts,
		FrameSrc:       base.FrameSrc(),
	}
	if base.Enabled() {
		s.Portal = portal.New(portal.Config{Base: base}, s.Store, s.Orbs)
		go s.Portal.Run(runCtx)
		s.Auth.Subscribe(s.Portal.Revoke)
		deps.Portal = s.Portal.Middleware
		mounts["/portal/auth"] = s.Portal.AuthHandler()
	}
	s.closers = append(s.closers, func() { cancel(); s.Threads.Close(); s.Orbs.Close() })

	s.API = &api.Server{Deps: deps}
	// App 会话兑换（M6）：不在 /api/v1 下，壳以顶层导航到达
	mounts[api.AppSessionPath] = s.API.AppSessionHandler()
	s.API.Auth = &api.AuthDeps{Service: s.Auth, Tickets: s.Tickets, Vault: s.Vault, Home: s.Home}
	s.API.Orbs = &api.OrbsDeps{Service: s.Orbs}
	s.API.Threads = &api.ThreadsDeps{Service: s.Threads}
	s.API.Attachments = &api.AttachmentsDeps{Service: s.Attachments}
	s.API.Shares = &api.ShareDeps{Service: s.Share}
	s.API.Notify = &api.NotifyDeps{Service: s.Notify}
	s.API.Desktop = &api.DesktopDeps{Service: s.Desktop}
	if s.Portal != nil {
		s.API.PortalShares = &api.PortalDeps{Service: s.Portal}
	}
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
