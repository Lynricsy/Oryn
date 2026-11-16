package modelcat

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

// egressID 是目录运行在 egress 日志里的标识（代替 orb id）。
const egressID = "model-catalog"

// vaultResolver 把可为 nil 的密钥库适配为 agentcfg.Resolver：nil 时任何密钥都查不到。
type vaultResolver struct{ v *secrets.Vault }

func (r vaultResolver) Lookup(name string) (string, string, bool) {
	if r.v == nil {
		return "", "", false
	}
	return r.v.Lookup(name)
}

func (r vaultResolver) SecretByID(id string) (string, []string, bool) {
	if r.v == nil {
		return "", nil, false
	}
	return r.v.SecretByID(id)
}

// policyResolver 是一次目录运行的 egress.Resolver：只放行本次渲染出的模型路由，没有任何 git 访问。
type policyResolver struct {
	vaultResolver
	egress.NoGateLookup // 没有 GitHub 路由，门禁查询不会被调用
	egress.NoMemory     // 没有记忆通道
	routes              map[string]egress.Route
}

func (p policyResolver) Policy() egress.Policy { return egress.Policy{Routes: p.routes, NoGit: true} }

func (policyResolver) GitToken(string) (string, bool) { return "", false }

func (policyResolver) GitHubToken() (string, bool) { return "", false }

// 目录运行不注入 GitHub 令牌，不会有令牌被上游拒绝。
func (policyResolver) GitHubTokenRejected(string) {}

// 目录运行没有 GitHub 路由，不会出现 GitHub 写操作。
func (policyResolver) GitHubWriteBegin(egress.GitHubWrite) string       { return "" }
func (policyResolver) GitHubWriteEnd(egress.GitHubWrite, int, []string) {}

// serveEgress 在 ln 上提供 egress 代理；调用方负责 Close 返回的 server（同时关闭 ln）。
func serveEgress(ln net.Listener, r vaultResolver, routes map[string]egress.Route) *http.Server {
	srv := &http.Server{
		Handler:           egress.NewHandler(egressID, policyResolver{vaultResolver: r, routes: routes}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("模型目录 egress 代理退出", "err", err)
		}
	}()
	return srv
}
