package pushgw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Lynricsy/Oryn/internal/clientip"
	"github.com/Lynricsy/Oryn/internal/pushproto"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

// cleanupEvery 是授权清理与过期 challenge 清理的间隔。
const cleanupEvery = time.Hour

// Options 是不进配置文件的运行参数：日志、时钟与上游覆盖（测试指向 httptest）。
type Options struct {
	// Log 为空时用 slog.Default()。
	Log *slog.Logger
	// Now 为空时用 time.Now。
	Now func() time.Time
	// FCMHTTP / APNsHTTP 为空时用带超时、不跟随重定向的客户端（APNs 只走 HTTP/2）。
	FCMHTTP  *http.Client
	APNsHTTP *http.Client
	// 上游地址；为空时用 Google / Apple 的正式地址。
	FCMTokenURL, FCMAPIBase           string
	APNsProductionURL, APNsSandboxURL string
}

// Server 是推送网关：授权表、内存 challenge 与限流、上游客户端。
type Server struct {
	cfg        Config
	log        *slog.Logger
	now        func() time.Time
	store      *Store
	up         *upstream
	challenges *challenges
	limiter    *limiter
	limits     limits
}

// New 打开数据目录（主密钥 master.key、数据库 gateway.db）并读取上游凭据。cfg 必须已通过 Validate。
func New(ctx context.Context, cfg Config, o Options) (*Server, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录 %s: %w", cfg.DataDir, err)
	}
	box, err := secrets.LoadOrCreate(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	up, err := newUpstream(cfg, o, o.Now)
	if err != nil {
		return nil, err
	}
	st, err := OpenStore(ctx, cfg.DataDir, box, o.Log)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, log: o.Log, now: o.Now, store: st, up: up,
		challenges: newChallenges(), limiter: newLimiter(), limits: newLimits(cfg.Limits),
	}, nil
}

// Close 关闭数据库。
func (s *Server) Close() error { return s.store.Close() }

// Handler 是网关的全部路由；最外层按回环反代还原客户端地址（只用于内存限流）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET "+pushproto.PathInfo, s.info)
	mux.HandleFunc("POST "+pushproto.PathChallenges, s.createChallenge)
	mux.HandleFunc("POST "+pushproto.PathGrants, s.createGrant)
	mux.HandleFunc("DELETE "+pushproto.PathGrants+"/{id}", s.deleteGrant)
	mux.HandleFunc("POST "+pushproto.PathVerify, s.verifyGrant)
	mux.HandleFunc("POST "+pushproto.PathPush, s.push)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, pushproto.CodeNotFound, "no such endpoint")
	})
	return clientip.Trusted(s.logRequests(mux))
}

// Run 在 cfg.Listen 上提供服务，并每小时清理一次授权与过期 challenge；ctx 结束时优雅关闭。
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve 在 ln 上提供服务，直到 ctx 结束。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	go s.cleanupLoop(ctx)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	s.log.Info("推送网关已启动", "listen", ln.Addr().String(), "origin", s.cfg.PublicOrigin,
		"apns", s.cfg.APNs.Enabled(), "fcm", s.cfg.FCM.Enabled())
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) cleanupLoop(ctx context.Context) {
	s.cleanup(ctx)
	t := time.NewTicker(cleanupEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.cleanup(ctx)
		}
	}
}

// cleanup 删除 90 天未用与吊销满 7 天的授权，并丢掉过期的 challenge。
func (s *Server) cleanup(ctx context.Context) {
	now := s.now()
	s.challenges.prune(now)
	n, err := s.store.Cleanup(ctx, now)
	if err != nil {
		s.log.Warn("清理授权失败", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("已清理授权", "deleted", n)
	}
}

// hostHash 是主机公钥在日志里的形态：SHA-256 的十六进制前 8 位。
func hostHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// reqLog 收集一次请求可以写进日志的字段：授权 id、主机公钥哈希、上游错误码。
type reqLog struct{ grant, host, upstream string }

type reqLogKey struct{}

func logOf(r *http.Request) *reqLog {
	if l, ok := r.Context().Value(reqLogKey{}).(*reqLog); ok {
		return l
	}
	return &reqLog{}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// logRequests 每个请求记一行：路由、HTTP 结果，以及处理函数登记的授权 id / 主机哈希 / 上游错误码。
// 不记来源 IP、路径参数、请求头与请求体。
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := &reqLog{}
		r = r.WithContext(context.WithValue(r.Context(), reqLogKey{}, l))
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "/" {
			route = "unknown"
		}
		attrs := []any{"route", route, "status", sw.status, "ms", time.Since(start).Milliseconds()}
		if l.grant != "" {
			attrs = append(attrs, "grant", l.grant)
		}
		if l.host != "" {
			attrs = append(attrs, "host", l.host)
		}
		if l.upstream != "" {
			attrs = append(attrs, "upstream", l.upstream)
		}
		level := slog.LevelInfo
		if route == "GET /healthz" {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "request", attrs...)
	})
}
