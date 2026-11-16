package orbs

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/link"
)

// EgressSecrets 是 egress 代理需要的密钥查询（*secrets.Vault 实现）。
type EgressSecrets interface {
	SecretByID(id string) (value string, hosts []string, ok bool)
	GitToken(host string) (string, bool)
}

// egressServer 是一台 orb 的出站代理服务。
type egressServer struct {
	l   *link.StreamListener
	srv *http.Server
}

// egressResolver 组合该 orb 的路由缓存与密钥库。
type egressResolver struct {
	s     *Service
	orbID string
}

func (r egressResolver) Policy() egress.Policy { return r.s.policyFor(r.orbID) }

func (r egressResolver) SecretByID(id string) (string, []string, bool) {
	if r.s.secrets == nil {
		return "", nil, false
	}
	return r.s.secrets.SecretByID(id)
}

// GitToken 返回 git 主机的令牌：github.com 且这台 orb 由 GitHub 集成下发凭据时用身份槽位的 OAuth 令牌（拿不到即失败，
// 不回落到 PAT）；否则用密钥库的 git/<host>/token。
func (r egressResolver) GitToken(host string) (string, bool) {
	if strings.EqualFold(host, "github.com") && r.s.github != nil {
		tok, st := r.githubToken()
		switch st {
		case GitHubTokenOK:
			return tok, true
		case GitHubTokenUnavailable:
			return "", false
		}
	}
	if r.s.secrets == nil {
		return "", false
	}
	return r.s.secrets.GitToken(host)
}

// GitHubToken 返回 GitHub API 令牌（M14）：只有这台 orb 由 GitHub 集成下发凭据且槽位可用时可用。
func (r egressResolver) GitHubToken() (string, bool) {
	if r.s.github == nil {
		return "", false
	}
	tok, st := r.githubToken()
	return tok, st == GitHubTokenOK
}

func (r egressResolver) githubToken() (string, GitHubTokenState) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return r.s.github.OrbToken(ctx, r.orbID)
}

// GitHubWriteBegin 把出站代理观察到的 GitHub 写操作交给 GitHub 集成预登记（M15）；返回非空 = 拒绝原因。
func (r egressResolver) GitHubWriteBegin(w egress.GitHubWrite) string {
	if r.s.github == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.s.github.GitHubWriteBegin(ctx, r.orbID, w)
}

// GitHubWriteEnd 把写操作的结果交给 GitHub 集成记补充键与审计（M15）。
func (r egressResolver) GitHubWriteEnd(w egress.GitHubWrite, status int, respKeys []string) {
	if r.s.github == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.s.github.GitHubWriteEnd(ctx, r.orbID, w, status, respKeys)
}

// policyFor 返回 orb 的出站策略；缓存缺失（如 orynd 重启后）时按当前配置重新生成。
func (s *Service) policyFor(orbID string) egress.Policy {
	s.mu.Lock()
	pol, ok := s.policies[orbID]
	s.mu.Unlock()
	if ok {
		return pol
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil || s.setup == nil {
		return egress.Policy{}
	}
	plan, err := s.setup.Plan(ctx, orb)
	if err != nil {
		slog.Warn("重建 egress 策略失败", "orb", orbID, "err", err)
		return egress.Policy{}
	}
	s.setPolicy(orbID, plan.Egress)
	return plan.Egress
}

func (s *Service) setPolicy(orbID string, pol egress.Policy) {
	s.mu.Lock()
	s.policies[orbID] = pol
	s.mu.Unlock()
}

// serveEgress 把一条 egress 流交给该 orb 的代理服务（按需创建）。
func (s *Service) serveEgress(orbID string, st *link.Stream) {
	s.mu.Lock()
	es := s.egress[orbID]
	if es == nil {
		es = &egressServer{l: link.NewStreamListener("oryn-egress")}
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
	es.l.Push(st)
}

// closeEgress 关闭 orb 的代理服务（orb 删除时）。
func (s *Service) closeEgress(orbID string) {
	s.mu.Lock()
	es := s.egress[orbID]
	delete(s.egress, orbID)
	delete(s.policies, orbID)
	s.mu.Unlock()
	if es != nil {
		_ = es.srv.Close()
		_ = es.l.Close()
	}
}
