package orbs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/memory"
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

// GitHubToken 返回 GitHub API 令牌（M15：线程身份槽位的 OAuth 令牌）：只有这台 orb 由 GitHub 集成下发凭据且槽位可用时可用。
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

// GitHubTokenRejected 把出站代理注入后被上游回 401 的令牌交给 GitHub 集成（R2：按令牌找到槽位置为 reauth）。
func (r egressResolver) GitHubTokenRejected(token string) {
	if r.s.github == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.s.github.GitHubTokenRejected(ctx, r.orbID, token)
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

// SetMemory 设置记忆网关（M16）；须在服务对外可用之前调用。
func (s *Service) SetMemory(g *memory.Gateway) { s.memory = g }

// Memory 实现 egress.MemoryLookup：返回记忆网关；没有设置时为 nil（/memory/ 一律 403）。
func (r egressResolver) Memory() http.Handler {
	if r.s.memory == nil {
		return nil
	}
	return r.s.memory
}

// errForeignThread 表示记忆请求路径里的线程不属于发出请求的 orb（R14）。
var errForeignThread = errors.New("orbs: 线程不属于这台 orb")

// Caller 实现 egress.MemoryLookup：按路径里的线程号解析记忆请求的调用方身份。
func (r egressResolver) Caller(threadID string) (memory.Caller, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.s.memoryCaller(ctx, r.orbID, threadID)
}

// memoryCaller 校验 threads.orb_id 等于这台 orb（否则 errForeignThread），再确定项目：
// 普通 orb 取 orb 的项目，runner 按线程区分项目，取 threads.project_id。已删除的 orb 与全局助手不接入记忆。
// 读库失败返回错误（出站代理按拒绝处理）。
func (s *Service) memoryCaller(ctx context.Context, orbID, threadID string) (memory.Caller, error) {
	th, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Caller{}, errForeignThread
	}
	if err != nil {
		return memory.Caller{}, fmt.Errorf("读取线程 %s：%w", threadID, err)
	}
	if th.OrbID == nil || *th.OrbID != orbID {
		return memory.Caller{}, errForeignThread
	}
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return memory.Caller{}, fmt.Errorf("读取 orb %s：%w", orbID, err)
	}
	if orb.DeletedAt != nil || orb.Coordinator {
		return memory.Caller{}, errForeignThread
	}
	c := memory.Caller{OrbID: orbID, ThreadID: threadID, Runner: IsRunner(orb), ProjectID: orb.ProjectID}
	if c.Runner {
		c.ProjectID = th.ProjectID
	}
	return c, nil
}

// errNoGitHub 是 GitHub 集成未启用时门禁现查的错误（门禁据此拒绝）。
var errNoGitHub = errors.New("orbs: GitHub 集成未启用")

// gateCtx 是门禁现查的超时（HeadSHA / DefaultBranch 会访问 GitHub）。
func gateCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// ThreadGates 实现 egress.GateLookup：每次写请求现查本 orb 线程任务的限制（线程任务可能在 orb 完成 setup 之后才写入）。
func (r egressResolver) ThreadGates() (egress.ThreadGates, error) {
	ctx, cancel := gateCtx()
	defer cancel()
	return r.s.threadGates(ctx, r.orbID)
}

// threadGates 读 orb 线程任务的限制：thread_issues.review_only，以及建立任务的规则（rule_id，缺省取 trigger_id）的
// impl_auth / comment_only_reviews。orb 没有唯一的未归档工作线程、线程没有任务或规则已删除时为零值；读库失败返回错误
// （出站代理按最严处理）。
func (s *Service) threadGates(ctx context.Context, orbID string) (egress.ThreadGates, error) {
	var tg egress.ThreadGates
	ths, err := s.st.Q.ListThreadsByOrb(ctx, &orbID)
	if err != nil {
		return tg, fmt.Errorf("读取 orb 的线程: %w", err)
	}
	var tid string
	for _, th := range ths {
		if th.Kind != threadKindWork || th.ArchivedAt != nil {
			continue
		}
		if tid != "" {
			return tg, nil
		}
		tid = th.ID
	}
	if tid == "" {
		return tg, nil
	}
	ti, err := s.st.Q.GetThreadIssueByThread(ctx, tid)
	if errors.Is(err, sql.ErrNoRows) {
		return tg, nil
	}
	if err != nil {
		return tg, fmt.Errorf("读取线程任务: %w", err)
	}
	tg.ReviewOnly = ti.ReviewOnly
	rule := ti.RuleID
	if rule == nil {
		rule = ti.TriggerID
	}
	if rule == nil {
		return tg, nil
	}
	trig, err := s.st.Q.GetGithubTrigger(ctx, *rule)
	if errors.Is(err, sql.ErrNoRows) {
		return tg, nil
	}
	if err != nil {
		return tg, fmt.Errorf("读取触发规则: %w", err)
	}
	tg.ImplAuth, tg.CommentOnlyReviews = trig.ImplAuth, trig.CommentOnlyReviews
	return tg, nil
}

// Prepared 实现 egress.GateLookup：sha 在 level 上本 orb 的 github_prepare 通过记录（须与项目当前门禁配置相符）。
func (r egressResolver) Prepared(sha, level string) egress.PrepareState {
	if r.s.github == nil {
		return egress.PrepareNone
	}
	ctx, cancel := gateCtx()
	defer cancel()
	return r.s.github.Prepared(ctx, r.orbID, sha, level)
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

// policyRebuildRetries 是 policyFor 重建期间撞上门禁作废时的重建次数上限；用尽后本次请求拿拒绝策略（denyPolicy）、
// 不写缓存：每次读到的配置都可能已被之后提交的改动取代，不能拿它放行（下一次请求重新重建）。
const policyRebuildRetries = 3

// denyPolicy 是策略拿不到（orb 读不到、生成失败、重建次数用尽）时用的失败关闭策略：没有模型路由、没有 GitHub 访问，
// 且关闭 git 路由。零值 Policy 不够：git 路由在没有项目仓库时仍放行只读，并会注入主机令牌。
var denyPolicy = egress.Policy{NoGit: true}

// policyFor 返回 orb 的出站策略；缓存缺失（如 orynd 重启、门禁作废后）时按当前配置重新生成。
// 重建读配置之前记下策略代际，写缓存时代际已变（期间有 InvalidateProjectPolicies）就丢弃重来：否则读到的旧门禁
// 会在作废之后写回缓存、一直留到下一次作废。
func (s *Service) policyFor(orbID string) egress.Policy {
	s.mu.Lock()
	pol, ok := s.policies[orbID]
	s.mu.Unlock()
	if ok {
		return pol
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range policyRebuildRetries {
		gen := s.policyGeneration()
		orb, err := s.st.Q.GetOrb(ctx, orbID)
		if err != nil || s.setup == nil {
			return denyPolicy
		}
		plan, err := s.setup.Plan(ctx, orb)
		if err != nil {
			slog.Warn("重建 egress 策略失败", "orb", orbID, "err", err)
			return denyPolicy
		}
		if s.hookPolicyRebuilt != nil {
			s.hookPolicyRebuilt(orbID)
		}
		pol = plan.Egress
		if s.setPolicy(orbID, pol, gen) {
			return pol
		}
	}
	slog.Warn("重建 egress 策略时门禁反复作废，本次请求按拒绝处理", "orb", orbID, "retries", policyRebuildRetries)
	return denyPolicy
}

// policyGeneration 返回出站策略的当前代际：生成策略的一方在读配置之前取一次，交给 setPolicy。
func (s *Service) policyGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policyGen
}

// setPolicy 缓存 orb 的出站策略；gen 是生成它之前取的代际。代际已变（期间项目门禁被改、缓存已作废）时不写缓存，
// 并删掉这台 orb 现有的缓存（它与这份策略谁更新说不清），返回 false：这份策略可能读自改动之前，由下一个出站请求
// 经 policyFor 按当前配置重建。
func (s *Service) setPolicy(orbID string, pol egress.Policy, gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.policyGen {
		delete(s.policies, orbID)
		return false
	}
	s.policies[orbID] = pol
	return true
}

// serveEgress 把一条 egress 流交给该 orb 的代理服务（按需创建）。runner 为真时只放行模型中转 `/llm/…` 与记忆网关
// `/memory/…`（runnerEgressHandler），其余路径一律 403：git、GitHub 与 MCP 的凭据不对 runner 开放（M21 D3）。
func (s *Service) serveEgress(orbID string, st *link.Stream, runner bool) {
	s.mu.Lock()
	es := s.egress[orbID]
	if es == nil {
		var h http.Handler = egress.NewHandler(orbID, egressResolver{s: s, orbID: orbID})
		if runner {
			h = runnerEgressHandler(h)
		}
		es = &egressServer{l: link.NewStreamListener("oryn-egress")}
		es.srv = &http.Server{
			Handler:           h,
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

// runnerEgressHandler 只把 origin-form 的 `/llm/` 与 `/memory/` 路径交给 next，其余（`/git/`、`/gh/`、`/mcp/`、
// CONNECT 与 absolute-form）回 403。absolute-form（代理形式，如 `GET http://github.com/memory/x/info/refs`）一律拒绝：
// 出站代理把这类请求只交给 git 路由，只看路径前缀会让 runner 借 owner=llm / memory 的代理形式请求进 git 路由。
func runnerEgressHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "" || (!strings.HasPrefix(r.URL.Path, "/llm/") && !strings.HasPrefix(r.URL.Path, "/memory/")) {
			memory.WriteError(w, http.StatusForbidden, "runner_forbidden", "oryn egress: runner 只开放模型中转与记忆通道")
			return
		}
		next.ServeHTTP(w, r)
	})
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
