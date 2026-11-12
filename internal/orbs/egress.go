package orbs

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/link"
)

// EgressSecrets 是 egress 代理需要的密钥查询（*secrets.Vault 实现）。
type EgressSecrets interface {
	SecretByID(id string) (value string, hosts []string, ok bool)
	GitToken(host string) (string, bool)
}

// vListener 是由 egress 流喂入的虚拟 net.Listener。
type vListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newVListener() *vListener {
	return &vListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (l *vListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *vListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *vListener) Addr() net.Addr { return vAddr{} }

// push 把一条 egress 流交给 http.Server；监听已关闭则关闭该流。
func (l *vListener) push(c net.Conn) {
	select {
	case l.ch <- c:
	case <-l.done:
		_ = c.Close()
	}
}

type vAddr struct{}

func (vAddr) Network() string { return "oryn-egress" }
func (vAddr) String() string  { return "oryn-egress" }

// egressServer 是一台 orb 的出站代理服务。
type egressServer struct {
	l   *vListener
	srv *http.Server
}

// egressResolver 组合该 orb 的路由缓存与密钥库。
type egressResolver struct {
	s     *Service
	orbID string
}

func (r egressResolver) LLMRoute(key string) (string, bool) {
	routes := r.s.routesFor(r.orbID)
	origin, ok := routes[key]
	return origin, ok
}

func (r egressResolver) SecretByID(id string) (string, []string, bool) {
	if r.s.secrets == nil {
		return "", nil, false
	}
	return r.s.secrets.SecretByID(id)
}

func (r egressResolver) GitToken(host string) (string, bool) {
	if r.s.secrets == nil {
		return "", false
	}
	return r.s.secrets.GitToken(host)
}

// routesFor 返回 orb 的 egress 路由；缓存缺失（如 orynd 重启后）时按当前配置重新生成。
func (s *Service) routesFor(orbID string) map[string]string {
	s.mu.Lock()
	routes, ok := s.routes[orbID]
	s.mu.Unlock()
	if ok {
		return routes
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil || s.setup == nil {
		return nil
	}
	plan, err := s.setup.Plan(ctx, orb)
	if err != nil {
		slog.Warn("重建 egress 路由失败", "orb", orbID, "err", err)
		return nil
	}
	s.setRoutes(orbID, plan.Routes)
	return plan.Routes
}

func (s *Service) setRoutes(orbID string, routes map[string]string) {
	if routes == nil {
		routes = map[string]string{}
	}
	s.mu.Lock()
	s.routes[orbID] = routes
	s.mu.Unlock()
}

// serveEgress 把一条 egress 流交给该 orb 的代理服务（按需创建）。
func (s *Service) serveEgress(orbID string, st *link.Stream) {
	s.mu.Lock()
	es := s.egress[orbID]
	if es == nil {
		es = &egressServer{l: newVListener()}
		es.srv = &http.Server{
			Handler:           egress.NewHandler(orbID, egressResolver{s: s, orbID: orbID}),
			ReadHeaderTimeout: 30 * time.Second,
			IdleTimeout:       5 * time.Minute,
		}
		s.egress[orbID] = es
		go func() {
			if err := es.srv.Serve(es.l); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
				slog.Warn("egress 服务退出", "orb", orbID, "err", err)
			}
		}()
	}
	s.mu.Unlock()
	es.l.push(st)
}

// closeEgress 关闭 orb 的代理服务（orb 删除时）。
func (s *Service) closeEgress(orbID string) {
	s.mu.Lock()
	es := s.egress[orbID]
	delete(s.egress, orbID)
	delete(s.routes, orbID)
	s.mu.Unlock()
	if es != nil {
		_ = es.srv.Close()
		_ = es.l.Close()
	}
}
