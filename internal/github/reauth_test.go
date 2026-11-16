package github

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// R2：带槽位令牌的请求不论走哪条路径（callAs、paged、releaseList、jobLogTail、出站代理），上游回 401 都把槽位置为 reauth，
// 之后不再发往上游。

// revokedTransport 模拟某个令牌在 GitHub 上被撤销：带它的请求一律得到 401，不到达假 GitHub；hits 是被拦下的请求数。
type revokedTransport struct {
	base http.RoundTripper
	auth string
	hits atomic.Int64
}

func (r *revokedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") == r.auth {
		r.hits.Add(1)
		return &http.Response{
			StatusCode: http.StatusUnauthorized, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"message":"Bad credentials"}`)), Request: req,
		}, nil
	}
	return r.base.RoundTrip(req)
}

// revoke 撤销槽位 slot 当前的令牌（控制面的 HTTP 客户端换成 revokedTransport），返回拦截器。
func (e *testEnv) revoke(slot string) *revokedTransport {
	e.t.Helper()
	tok, err := e.s.tokenFor(e.ctx, slot)
	require.NoError(e.t, err)
	c := *e.s.cfg.HTTP
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	rt := &revokedTransport{base: base, auth: "Bearer " + tok}
	c.Transport = rt
	e.s.cfg.HTTP = &c
	return rt
}

func (e *testEnv) slotStatus(slot string) string {
	e.t.Helper()
	row, err := e.st.Q.GetGithubIdentity(e.ctx, slot)
	require.NoError(e.t, err)
	return row.Status
}

// assertReauthed：第一次请求得到 401 并把槽位置为 reauth；再次调用直接报 github_identity_required，不再发往上游。
func (e *testEnv) assertReauthed(slot string, rt *revokedTransport, first, again error) {
	e.t.Helper()
	assert.Equal(e.t, kindReauth, errKind(first), "%v", first)
	assert.Equal(e.t, "reauth", e.slotStatus(slot))
	assert.EqualValues(e.t, 1, rt.hits.Load())
	assert.Equal(e.t, "github_identity_required", codeOf(again))
	assert.EqualValues(e.t, 1, rt.hits.Load(), "reauth 之后不再请求上游")
}

// github_autoclose（原先取出令牌后直接 call / paged）：线程身份的令牌被撤销。
func TestReauthAutoclose(t *testing.T) {
	e, _ := newAutocloseEnv(t)
	slot := e.s.threadSlot(e.ctx, e.thread())
	rt := e.revoke(slot)
	_, first := e.call("github_autoclose", `{}`)
	_, again := e.call("github_autoclose", `{}`)
	e.assertReauthed(slot, rt, first, again)
}

// 发布哨兵的状态读取（原先 releaseRepoAuth 取出令牌后直接 call）：我的账号的令牌被撤销。
func TestReauthReleaseRead(t *testing.T) {
	e := newReleaseEnv(t)
	rt := e.revoke(slotUser)
	_, first := e.s.releasePublished(e.ctx, "acme/widgets", "1.2.0")
	_, again := e.s.releasePublished(e.ctx, "acme/widgets", "1.2.0")
	e.assertReauthed(slotUser, rt, first, again)
}

// job 日志下载（原先自己调 HTTP.Do）：正常时经 302 读到日志末尾；令牌被撤销后同样置 reauth。
func TestReauthJobLogTail(t *testing.T) {
	e := newReleaseEnv(t)
	run := e.addRun(relSHA(1), "release: v1.2.0", "completed", "failure")
	job := e.fake.AddJob(githubtest.WorkflowJob{RunID: run.ID, RepoID: 42, Name: "test", Conclusion: "failure", Log: "line one\nline two\n"})
	tail, err := e.s.jobLogTail(e.ctx, "acme/widgets", job.ID)
	require.NoError(t, err)
	assert.Contains(t, tail, "line two")

	rt := e.revoke(slotUser)
	_, first := e.s.jobLogTail(e.ctx, "acme/widgets", job.ID)
	_, again := e.s.jobLogTail(e.ctx, "acme/widgets", job.ID)
	e.assertReauthed(slotUser, rt, first, again)
}

// redirectTransport 把 API 主机上的 job 日志请求 302 到 to（另一主机的「预签名地址」），其余照常。
type redirectTransport struct {
	base http.RoundTripper
	to   string
}

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/logs") && strings.Contains(req.URL.Path, "/actions/jobs/") {
		return &http.Response{
			StatusCode: http.StatusFound, Header: http.Header{"Location": {r.to}},
			Body: io.NopCloser(strings.NewReader("")), Request: req,
		}, nil
	}
	return r.base.RoundTrip(req)
}

// 302 跨主机后的 401（net/http 已去掉 Authorization）说明不了令牌失效：照常报错，槽位仍是 ok，下一次照常发往上游。
func TestReauthIgnoresRedirectedUnauthorized(t *testing.T) {
	e := newReleaseEnv(t)
	var hits atomic.Int64
	var auth atomic.Value
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer blob.Close()
	// 换成 localhost：主机名与 API 主机（127.0.0.1）不同，跳转时 net/http 去掉 Authorization
	to := strings.Replace(blob.URL, "127.0.0.1", "localhost", 1) + "/job-logs/1"
	c := *e.s.cfg.HTTP
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	c.Transport = redirectTransport{base: base, to: to}
	e.s.cfg.HTTP = &c

	_, err := e.s.jobLogTail(e.ctx, "acme/widgets", 1)
	assert.True(t, isStatus(err, http.StatusUnauthorized), "%v", err)
	assert.Empty(t, auth.Load(), "跨主机跳转不带令牌")
	assert.Equal(t, "ok", e.slotStatus(slotUser))
	_, err = e.s.jobLogTail(e.ctx, "acme/widgets", 1)
	assert.True(t, isStatus(err, http.StatusUnauthorized), "%v", err)
	assert.EqualValues(t, 2, hits.Load(), "槽位未置 reauth，下一次照常发往上游")
}

// 401 只作用于当时的令牌：槽位重新授权换了新令牌之后，旧令牌迟到的 401 不改动槽位；不属于任何槽位的令牌（如 PAT）忽略。
func TestReauthOnlyCurrentToken(t *testing.T) {
	e := newListenEnv(t)
	old, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	require.NoError(t, e.connect(slotUser, "again", "owner"))
	cur, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	require.NotEqual(t, old, cur)

	require.NoError(t, e.s.tokenRejected(e.ctx, old, slotUser))
	e.s.GitHubTokenRejected(e.ctx, "orb-x", old)
	e.s.GitHubTokenRejected(e.ctx, "orb-x", "ghp_some_pat")
	assert.Equal(t, "ok", e.slotStatus(slotUser))

	e.s.GitHubTokenRejected(e.ctx, "orb-x", cur)
	assert.Equal(t, "reauth", e.slotStatus(slotUser))
}

// 端到端：orb 经出站代理 GET /user 收到 401 → 槽位 reauth，之后 OrbToken 不再签发，代理回 502；
// 已换成新令牌的槽位不受旧令牌 401 的影响。
func TestReauthThroughEgress(t *testing.T) {
	e := newListenEnv(t)
	orbID, _ := e.orbWithThread(e.prj, "user")
	get := func(r svcResolver) int {
		t.Helper()
		px := httptest.NewServer(egress.NewHandler(orbID, r))
		defer px.Close()
		resp, err := http.Get(px.URL + "/gh/api.github.com/user")
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	live := svcResolver{e: e.testEnv, orbID: orbID} // tok 为空：每次经 OrbToken 现取
	require.Equal(t, http.StatusOK, get(live))

	// 旧令牌的请求在途时槽位已重新授权：旧令牌的 401 不置 reauth
	old, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	require.NoError(t, e.connect(slotUser, "again", "owner"))
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /user": http.StatusUnauthorized}
	e.fake.Unlock()
	assert.Equal(t, http.StatusUnauthorized, get(svcResolver{e: e.testEnv, orbID: orbID, tok: old}))
	assert.Equal(t, "ok", e.slotStatus(slotUser))

	// 当前令牌被拒
	assert.Equal(t, http.StatusUnauthorized, get(live))
	assert.Equal(t, "reauth", e.slotStatus(slotUser))
	e.fake.Lock()
	e.fake.Fail = nil
	e.fake.Unlock()
	_, st := e.s.OrbToken(e.ctx, orbID)
	assert.NotEqual(t, orbs.GitHubTokenOK, st, "reauth 后 OrbToken 不再签发")
	assert.Equal(t, http.StatusBadGateway, get(live))
}
