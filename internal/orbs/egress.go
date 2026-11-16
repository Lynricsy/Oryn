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

// errNoGitHub 是 GitHub 集成未启用时门禁现查的错误（门禁据此拒绝）。
var errNoGitHub = errors.New("orbs: GitHub 集成未启用")

// gateCtx 是门禁现查的超时（HeadSHA / DefaultBranch 会访问 GitHub）。
func gateCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// PrepareOK 实现 egress.GateLookup：sha 在 level 上是否有本 orb 的 github_prepare 通过记录。
func (r egressResolver) PrepareOK(sha, level string) (bool, bool) {
	if r.s.github == nil {
		return false, false
	}
	ctx, cancel := gateCtx()
	defer cancel()
	return r.s.github.PrepareOK(ctx, r.orbID, sha, level)
}

// ImplAuthorized 实现 egress.GateLookup：本 orb 线程的任务是否已获实现授权（每次现查，分类会在线程期间变化）。
func (r egressResolver) ImplAuthorized() bool {
	if r.s.github == nil {
		return false
	}
	ctx, cancel := gateCtx()
	defer cancel()
	return r.s.github.ImplAuthorized(ctx, r.orbID)
}

// HeadSHA 实现 egress.GateLookup：以本 orb 的槽位查询分支指向的提交。
func (r egressResolver) HeadSHA(repo, ref string) (string, error) {
	if r.s.github == nil {
		return "", errNoGitHub
	}
	ctx, cancel := gateCtx()
	defer cancel()
	return r.s.github.HeadSHA(ctx, r.orbID, repo, ref)
}

// DefaultBranch 实现 egress.GateLookup：以本 orb 的槽位查询仓库默认分支（出站代理缓存）。
func (r egressResolver) DefaultBranch(repo string) (string, error) {
	if r.s.github == nil {
		return "", errNoGitHub
	}
	ctx, cancel := gateCtx()
	defer cancel()
	return r.s.github.DefaultBranch(ctx, r.orbID, repo)
}

// IsOrynHook 实现 egress.GateLookup：id 是否是 Oryn 管理的仓库 hook；集成未启用时按是处理（只多拒绝）。
func (r egressResolver) IsOrynHook(repo string, id int64) bool {
	if r.s.github == nil {
		return true
	}
	ctx, cancel := gateCtx()
	defer cancel()
	return r.s.github.IsOrynHook(ctx, repo, id)
}

// ReleaseThread 实现 egress.GateLookup：本 orb 的线程是否为进行中的发布任务。
func (r egressResolver) ReleaseThread() bool {
	if r.s.github == nil {
		return false
	}
	ctx, cancel := gateCtx()
	defer cancel()
	return r.s.github.ReleaseThread(ctx, r.orbID)
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
