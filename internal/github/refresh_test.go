package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
)

func newExpiringEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newEnv(t)
	e.fake.Lock()
	e.fake.ExpiringTokens = true
	e.fake.Unlock()
	e.oauthApp()
	e.fake.AddUser(1, "owner", "The Owner")
	require.NoError(t, e.connect(slotUser, "c-init", "owner"))
	return e
}

func (e *testEnv) decryptRefresh(slot string) string {
	e.t.Helper()
	row, err := e.st.Q.GetGithubIdentity(e.ctx, slot)
	require.NoError(e.t, err)
	require.NotEmpty(e.t, row.RefreshTokenEnc)
	b, err := e.s.cfg.Box.Open(aadRefresh(slot), row.RefreshTokenEnc)
	require.NoError(e.t, err)
	return string(b)
}

func countRefreshCalls(e *testEnv) int {
	n := 0
	for _, c := range e.fake.CallsTo(http.MethodPost, "/login/oauth/access_token") {
		v, err := url.ParseQuery(string(c.Body))
		if err == nil && v.Get("grant_type") == "refresh_token" {
			n++
		}
	}
	return n
}

// TestOAuthRefreshProactiveAndRotation 覆盖短命令牌保存、提前刷新窗口、多轮轮换，以及重连长命 App 时清理过期列。
func TestOAuthRefreshProactiveAndRotation(t *testing.T) {
	e := newExpiringEnv(t)

	row, err := e.st.Q.GetGithubIdentity(e.ctx, slotUser)
	require.NoError(t, err)
	require.NotNil(t, row.AccessExpiresAt)
	require.NotNil(t, row.RefreshExpiresAt)
	require.NotEmpty(t, row.RefreshTokenEnc)
	assert.NotContains(t, string(row.RefreshTokenEnc), "ghr_")
	assert.Equal(t, row.ConnectedAt+28800*1000, *row.AccessExpiresAt)
	assert.Equal(t, row.ConnectedAt+15897600*1000, *row.RefreshExpiresAt)

	tok1, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	rt1 := e.decryptRefresh(slotUser)

	// 距离过期还有 10 分钟（大于 5 分钟提前窗口）：不刷新
	e.clock.Add(8*time.Hour - 10*time.Minute)
	tokStill, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	assert.Equal(t, tok1, tokStill)
	assert.Equal(t, 0, countRefreshCalls(e))

	// 进入提前刷新窗口（距离过期 4 分钟）：自动刷新并轮换 access_token 与 refresh_token
	e.clock.Add(6 * time.Minute)
	tok2, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	assert.NotEqual(t, tok1, tok2)
	assert.Equal(t, 1, countRefreshCalls(e))

	rt2 := e.decryptRefresh(slotUser)
	assert.NotEqual(t, rt1, rt2)

	// 用刷新后的令牌正常调 API
	var u struct {
		Login string `json:"login"`
	}
	require.NoError(t, e.s.callAs(e.ctx, slotUser, http.MethodGet, "/user", nil, &u))
	assert.Equal(t, "owner", u.Login)

	// 再过 8 小时触发第二轮轮换，证明新的 refresh_token 正确落盘
	e.clock.Add(8 * time.Hour)
	tok3, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	assert.NotEqual(t, tok2, tok3)
	assert.Equal(t, 2, countRefreshCalls(e))

	// 切换回不开启过期的 OAuth App 重连：过期与刷新列必须清空为 NULL
	e.fake.Lock()
	e.fake.ExpiringTokens = false
	e.fake.Unlock()
	require.NoError(t, e.connect(slotUser, "c-long", "owner"))
	row, err = e.st.Q.GetGithubIdentity(e.ctx, slotUser)
	require.NoError(t, err)
	assert.Nil(t, row.AccessExpiresAt)
	assert.Nil(t, row.RefreshTokenEnc)
	assert.Nil(t, row.RefreshExpiresAt)
}

// TestOAuthRefreshSingleflightConcurrent 覆盖并发进入刷新窗口时每槽位只发一次刷新请求（不重复消费一次性 refresh_token），
// 且等待者响应自己的 ctx 取消。
func TestOAuthRefreshSingleflightConcurrent(t *testing.T) {
	e := newExpiringEnv(t)
	tok1, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.fake.Lock()
	e.fake.BeforeRefresh = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	e.fake.Unlock()

	e.clock.Add(8 * time.Hour)

	const total = 8
	tokens := make([]string, total)
	errs := make([]error, total)
	var wg sync.WaitGroup

	// 先启动领跑者，卡在 BeforeRefresh
	wg.Go(func() {
		tokens[0], errs[0] = e.s.tokenFor(e.ctx, slotUser)
	})
	<-entered

	// 启动一个在等待期间主动取消的调用方
	cancelCtx, cancel := context.WithCancel(e.ctx)
	waitErr := make(chan error, 1)
	wg.Go(func() {
		_, err := e.s.tokenFor(cancelCtx, slotUser)
		waitErr <- err
	})

	// 其余并发等待者一起加入 singleflight
	for i := 1; i < total; i++ {
		idx := i
		wg.Go(func() {
			tokens[idx], errs[idx] = e.s.tokenFor(e.ctx, slotUser)
		})
	}

	cancel()
	assert.ErrorIs(t, <-waitErr, context.Canceled)

	close(release)
	wg.Wait()

	assert.Equal(t, 1, countRefreshCalls(e), "同一槽位并发刷新只能发一次请求")
	for i := range total {
		require.NoError(t, errs[i])
		assert.NotEqual(t, tok1, tokens[i])
		assert.Equal(t, tokens[0], tokens[i])
	}
	assert.Equal(t, "ok", e.slotStatus(slotUser))
}

// TestOAuthRefreshLeaderSurvivesCallerCancel 覆盖领跑者发出刷新后调用方取消 ctx：
// 必须脱离调用方取消完成收包与落盘，避免一次性 refresh_token 在 GitHub 侧已作废却未保存新对。
func TestOAuthRefreshLeaderSurvivesCallerCancel(t *testing.T) {
	e := newExpiringEnv(t)
	tok1, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)

	entered := make(chan struct{})
	release := make(chan struct{})
	e.fake.Lock()
	e.fake.BeforeRefresh = func() {
		close(entered)
		<-release
	}
	e.fake.Unlock()

	e.clock.Add(8 * time.Hour)

	callerCtx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	var gotTok string
	var gotErr error
	go func() {
		defer close(done)
		gotTok, gotErr = e.s.tokenFor(callerCtx, slotUser)
	}()

	<-entered
	cancel()
	close(release)
	<-done

	require.NoError(t, gotErr)
	assert.NotEqual(t, tok1, gotTok)

	// 确认新令牌对已落盘，下一次可直接复用且可继续后续轮换
	tok2, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	assert.Equal(t, gotTok, tok2)
	assert.Equal(t, "ok", e.slotStatus(slotUser))
}

// TestOAuthRefreshOn401AndTransparentRetry 覆盖未到刷新窗口但上游回 401 时自动刷新并重放带 body 请求；
// 若刷新后新令牌仍回 401，只重试一次后标为 reauth。
func TestOAuthRefreshOn401AndTransparentRetry(t *testing.T) {
	e := newExpiringEnv(t)
	tok1, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)

	// 吊销当前 access_token，但保留 refresh_token（模拟时钟偏差或提前失效）
	e.fake.RevokeToken(tok1)

	// 带 JSON body 的 POST（GraphQL）必须能重放 body 并透明成功
	var gotBody []byte
	var hits int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") == "Bearer "+tok1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		var body []byte
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"owner"}}}`))
	}))
	defer api.Close()
	e.s.cfg.APIURL = api.URL

	var out struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	require.NoError(t, e.s.graphql(e.ctx, slotUser, "query{viewer{login}}", nil, &out))
	assert.Equal(t, "owner", out.Viewer.Login)
	assert.Equal(t, 2, hits, "401 后用新令牌透明重试一次")
	assert.Contains(t, string(gotBody), "viewer", "POST 请求体在重试时完整保留")
	assert.Equal(t, 1, countRefreshCalls(e))
	assert.Equal(t, "ok", e.slotStatus(slotUser))

	// 新令牌若依旧被上游 401，不再循环刷新，直接置为 reauth
	always401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer always401.Close()
	e.s.cfg.APIURL = always401.URL

	err = e.s.callAs(e.ctx, slotUser, http.MethodGet, "/user", nil, nil)
	require.Error(t, err)
	assert.Equal(t, "reauth", e.slotStatus(slotUser))
	assert.Equal(t, 2, countRefreshCalls(e), "仅刷新一次后放弃并标 reauth")
}

// TestOAuthRefreshThroughEgress 覆盖 orb 经出站代理：到期前自动刷新；遇 401 时 GitHubTokenRejected 刷新恢复而不误标 reauth。
func TestOAuthRefreshThroughEgress(t *testing.T) {
	e := newListenEnv(t)
	e.fake.Lock()
	e.fake.ExpiringTokens = true
	e.fake.Unlock()
	require.NoError(t, e.connect(slotUser, "c-exp", "owner"))

	orbID, _ := e.orbWithThread(e.prj, "user")
	px := httptest.NewServer(egress.NewHandler(orbID, svcResolver{e: e.testEnv, orbID: orbID}))
	defer px.Close()

	get := func() int {
		t.Helper()
		resp, err := http.Get(px.URL + "/gh/api.github.com/user")
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	require.Equal(t, http.StatusOK, get())

	// 拨到 8 小时后：OrbToken 经 tokenFor 主动刷新，orb 无感成功
	e.clock.Add(8 * time.Hour)
	require.Equal(t, http.StatusOK, get())
	assert.Equal(t, 1, countRefreshCalls(e.testEnv))
	assert.Equal(t, "ok", e.slotStatus(slotUser))

	// 模拟未到刷新窗口却被上游回 401：egress 回调 GitHubTokenRejected 自动刷新，下一次请求直接 200
	curTok, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	e.fake.RevokeToken(curTok)
	assert.Equal(t, http.StatusUnauthorized, get())
	assert.Equal(t, "ok", e.slotStatus(slotUser), "有 refresh_token 时 401 刷新恢复，不误标 reauth")
	assert.Equal(t, 2, countRefreshCalls(e.testEnv))
	assert.Equal(t, http.StatusOK, get())
}

// TestOAuthRefreshBadRefreshTokenMarksReauth 覆盖刷新返回 bad_refresh_token 时将槽位标为 reauth 且后续不再打上游。
func TestOAuthRefreshBadRefreshTokenMarksReauth(t *testing.T) {
	e := newExpiringEnv(t)
	rt := e.decryptRefresh(slotUser)
	e.fake.RevokeRefreshToken(rt)

	e.clock.Add(8 * time.Hour)
	_, err := e.s.tokenFor(e.ctx, slotUser)
	assert.Equal(t, "github_identity_required", codeOf(err))
	assert.Equal(t, "reauth", e.slotStatus(slotUser))
	assert.Equal(t, 1, countRefreshCalls(e))

	// 再次调用直接拒绝，不再打上游
	_, err = e.s.tokenFor(e.ctx, slotUser)
	assert.Equal(t, "github_identity_required", codeOf(err))
	assert.Equal(t, 1, countRefreshCalls(e))
}

// TestOAuthRefreshTransientErrorPreservesSlot 覆盖刷新端点临时 502 或错误配置不误标 reauth，恢复后自动刷新成功。
func TestOAuthRefreshTransientErrorPreservesSlot(t *testing.T) {
	e := newExpiringEnv(t)
	tok1, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)

	e.clock.Add(8 * time.Hour)

	// 临时 502：返回错误但保留 ok 状态与 refresh_token
	e.fake.Lock()
	e.fake.Fail = map[string]int{"POST /login/oauth/access_token": http.StatusBadGateway}
	e.fake.Unlock()

	_, err = e.s.tokenFor(e.ctx, slotUser)
	require.Error(t, err)
	assert.True(t, retryable(err))
	assert.Equal(t, "ok", e.slotStatus(slotUser))

	// client secret 暂时不匹配（incorrect_client_credentials）：也不应永久标 reauth
	e.fake.Lock()
	e.fake.Fail = nil
	oldSecret := e.fake.OAuthApp.ClientSecret
	e.fake.OAuthApp.ClientSecret = "wrong-secret"
	e.fake.Unlock()

	_, err = e.s.tokenFor(e.ctx, slotUser)
	assert.Equal(t, "refresh_failed", codeOf(err))
	assert.Equal(t, "ok", e.slotStatus(slotUser))

	// 恢复后自动刷新成功
	e.fake.Lock()
	e.fake.OAuthApp.ClientSecret = oldSecret
	e.fake.Unlock()

	tok2, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	assert.NotEqual(t, tok1, tok2)
	assert.Equal(t, "ok", e.slotStatus(slotUser))
}

// TestOAuthRefreshRaceWithReauthorize 覆盖刷新在途期间用户改绑异账号（以及 singleflight 等待者）：
// 旧刷新既不得覆盖新账号的库记录，也不得把新代际令牌作为旧代际轮换结果返回给原调用方与等待者。
func TestOAuthRefreshRaceWithReauthorize(t *testing.T) {
	e := newExpiringEnv(t)
	e.fake.AddUser(2, "other-owner", "Other Owner")

	entered := make(chan struct{})
	release := make(chan struct{})
	e.fake.Lock()
	e.fake.BeforeRefresh = func() {
		close(entered)
		<-release
	}
	e.fake.Unlock()

	oldRow, err := e.st.Q.GetGithubIdentity(e.ctx, slotUser)
	require.NoError(t, err)
	oldGen := genOf(oldRow)

	e.clock.Add(8 * time.Hour)
	var leaderTok, waiterTok string
	var leaderErr, waiterErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		leaderTok, leaderErr = e.s.tokenFor(e.ctx, slotUser)
	})
	<-entered
	wg.Go(func() {
		waiterTok, waiterErr = e.s.refreshSlot(e.ctx, slotUser, oldRow.AccessTokenEnc, oldGen, false)
	})
	// 刷新在途期间，用户把槽位改绑为另一个账号（新授权代际）
	require.NoError(t, e.connect(slotUser, "c-rebind", "other-owner"))
	rebindRow, err := e.st.Q.GetGithubIdentity(e.ctx, slotUser)
	require.NoError(t, err)
	require.Equal(t, int64(2), rebindRow.GithubID)

	close(release)
	wg.Wait()

	assert.Equal(t, "github_identity_changed", codeOf(leaderErr), "领跑刷新遇代际变化时不得返回新账号令牌")
	assert.Empty(t, leaderTok)
	assert.Equal(t, "github_identity_changed", codeOf(waiterErr), "singleflight 等待者也不得拿到新账号令牌")
	assert.Empty(t, waiterTok)

	afterRow, err := e.st.Q.GetGithubIdentity(e.ctx, slotUser)
	require.NoError(t, err)
	assert.Equal(t, rebindRow.AccessTokenEnc, afterRow.AccessTokenEnc, "数据库中的新账号令牌不得被旧刷新覆盖")
	assert.Equal(t, "ok", afterRow.Status)

	// 之后的新调用正常拿到新账号令牌
	newTok, err := e.s.tokenFor(e.ctx, slotUser)
	require.NoError(t, err)
	rebindPlain, err := e.s.cfg.Box.Open(aadAccess(slotUser), rebindRow.AccessTokenEnc)
	require.NoError(t, err)
	assert.Equal(t, string(rebindPlain), newTok)
}

// TestOAuthRefreshNoCrossAccountReplayOn401 覆盖并发写请求在途或 401 刷新在途期间改绑异账号的回归场景：
// 绝不得替新账号重放旧 POST/GraphQL 写请求，也绝不得把新账号误标为 reauth。
func TestOAuthRefreshNoCrossAccountReplayOn401(t *testing.T) {
	t.Run("rebind_while_write_in_flight", func(t *testing.T) {
		e := newExpiringEnv(t)
		e.fake.AddUser(2, "other-owner", "Other Owner")

		reqArrived := make(chan struct{})
		allowReply := make(chan struct{})
		var unblockOnce sync.Once
		unblock := func() { unblockOnce.Do(func() { close(allowReply) }) }

		var authHeaders []string
		var mu sync.Mutex
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user" {
				e.fake.Handler().ServeHTTP(w, r)
				return
			}
			mu.Lock()
			authHeaders = append(authHeaders, r.Header.Get("Authorization"))
			n := len(authHeaders)
			mu.Unlock()
			if n == 1 {
				close(reqArrived)
				<-allowReply
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		}))
		defer api.Close()
		defer unblock()
		e.s.cfg.APIURL = api.URL

		done := make(chan error, 1)
		go func() {
			done <- e.s.callAs(e.ctx, slotUser, http.MethodPost, "/repos/acme/app/issues/1/comments", map[string]string{"body": "from-old-owner"}, nil)
		}()

		<-reqArrived
		// 写请求尚未返回 401 时，把 user 槽位改绑为异账号
		require.NoError(t, e.connect(slotUser, "c-other", "other-owner"))
		newRow, err := e.st.Q.GetGithubIdentity(e.ctx, slotUser)
		require.NoError(t, err)
		require.Equal(t, int64(2), newRow.GithubID)
		unblock()

		err = <-done
		assert.True(t, isStatus(err, http.StatusUnauthorized), "代际已变应直接返回原 401 错误，实际: %v", err)

		mu.Lock()
		gotCalls := len(authHeaders)
		mu.Unlock()
		assert.Equal(t, 1, gotCalls, "绝不得使用新账号令牌重放旧 POST 写请求")
		assert.Equal(t, "ok", e.slotStatus(slotUser), "新账号槽位绝不得被误标为 reauth")
		assert.Equal(t, 0, countRefreshCalls(e))
	})

	t.Run("rebind_while_refreshing_after_401", func(t *testing.T) {
		e := newExpiringEnv(t)
		e.fake.AddUser(2, "other-owner", "Other Owner")

		entered := make(chan struct{})
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		e.fake.Lock()
		e.fake.BeforeRefresh = func() {
			close(entered)
			<-release
		}
		e.fake.Unlock()

		var hits int
		var mu sync.Mutex
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user" {
				e.fake.Handler().ServeHTTP(w, r)
				return
			}
			mu.Lock()
			hits++
			mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		}))
		defer api.Close()
		defer unblock()
		e.s.cfg.APIURL = api.URL

		done := make(chan error, 1)
		go func() {
			done <- e.s.graphql(e.ctx, slotUser, "mutation{addComment{id}}", nil, nil)
		}()

		// 等待第一次 GraphQL 收到 401 并进入旧代际的 refreshSlot
		<-entered
		// 刷新在途期间改绑异账号
		require.NoError(t, e.connect(slotUser, "c-other-2", "other-owner"))
		newRow, err := e.st.Q.GetGithubIdentity(e.ctx, slotUser)
		require.NoError(t, err)
		require.Equal(t, int64(2), newRow.GithubID)
		unblock()

		err = <-done
		assert.True(t, isStatus(err, http.StatusUnauthorized), "刷新期间改绑异账号后应返回原 401 错误，实际: %v", err)

		mu.Lock()
		gotHits := hits
		mu.Unlock()
		assert.Equal(t, 1, gotHits, "绝不得使用新账号令牌重放旧 GraphQL mutation")
		assert.Equal(t, "ok", e.slotStatus(slotUser), "新账号槽位绝不得因旧请求重试 401 被误标为 reauth")

		afterRow, err := e.st.Q.GetGithubIdentity(e.ctx, slotUser)
		require.NoError(t, err)
		assert.Equal(t, newRow.AccessTokenEnc, afterRow.AccessTokenEnc)
	})
}
