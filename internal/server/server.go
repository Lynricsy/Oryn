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
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// evalInterval 是 orb 空闲评估周期；0 = orbs 默认（30s）。测试可调小。
var evalInterval time.Duration

// Server 持有控制面全部组件。
type Server struct {
	Config  config.Orynd
	Store   *store.Store
	Vault   *secrets.Vault
	Home    *agentcfg.Home
	Auth    *auth.Service
	Tickets *auth.Tickets
	Jobs    *jobs.Queue
	Orbs    *orbs.Service
	Threads *threads.Service
	Hub     *realtime.Hub
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
	s.Orbs = orbs.New(orbs.Config{Idle: cfg.Idle, Defaults: cfg.OrbDefaults, PublicURL: cfg.PublicURL, TokenKey: key, EvalInterval: evalInterval},
		s.Store, s.Jobs, &orbs.DefaultSetup{Store: s.Store, Home: s.Home, Vault: s.Vault}, s.Vault)
	// 线程服务须在 orb 服务接受 link / 发出事件之前构造：它在构造时注册 entries.* / agent.* 处理器
	s.Threads = threads.New(threads.Config{DataDir: cfg.DataDir, WakeOnFocus: cfg.Idle.WakeOnFocus}, s.Store, orbsAdapter{s.Orbs})
	s.Hub = realtime.New(s.Threads, s.Tickets, revocations{s.Auth})
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	go s.Jobs.Run(runCtx)
	go s.Orbs.Run(runCtx)
	go s.Auth.Run(runCtx)
	s.closers = append(s.closers, func() { cancel(); s.Threads.Close(); s.Orbs.Close() })

	s.API = &api.Server{Deps: api.Deps{
		Config:         cfg,
		Store:          s.Store,
		AuthMiddleware: auth.Middleware(s.Store),
		Mounts:         map[string]http.Handler{"/orb/v1/link": s.Orbs.LinkHandler(), api.WSPath: s.Hub},
	}}
	s.API.Auth = &api.AuthDeps{Service: s.Auth, Tickets: s.Tickets, Vault: s.Vault, Home: s.Home}
	s.API.Orbs = &api.OrbsDeps{Service: s.Orbs}
	s.API.Threads = &api.ThreadsDeps{Service: s.Threads}
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
