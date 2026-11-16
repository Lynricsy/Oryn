package github

import (
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// connect 让 login 授权 slot（假服务登记授权码后走一遍回调）。
func (e *testEnv) connect(slot, code, login string) error {
	e.t.Helper()
	e.fake.GrantCode(code, login)
	raw, err := e.s.BeginAuthorize(e.ctx, slot)
	require.NoError(e.t, err)
	got, err := e.s.FinishAuthorize(e.ctx, code, stateOf(e.t, raw))
	if err == nil {
		assert.Equal(e.t, slot, got)
	}
	return err
}

func (e *testEnv) owner() {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.CreateOwner(e.ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
}

func (e *testEnv) refreshCalls() int {
	n := 0
	for _, c := range e.fake.CallsTo("POST", "/login/oauth/access_token") {
		if strings.Contains(string(c.Body), "grant_type=refresh_token") {
			n++
		}
	}
	return n
}

func TestAuthorizeAndSameAccount(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.BeginAuthorize(e.ctx, "user")
	assert.Equal(t, "github_not_connected", codeOf(err))

	e.installApp()
	e.fake.AddUser(1, "owner", "The Owner")
	e.fake.AddUser(7, "oryn-bot", "")

	raw, err := e.s.BeginAuthorize(e.ctx, "user")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, e.fake.URL()+"/login/oauth/authorize", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, "Iv1.test", u.Query().Get("client_id"))
	assert.Equal(t, testPublicURL+"/api/v1/public/github/oauth/callback", u.Query().Get("redirect_uri"))
	assert.Equal(t, "select_account", u.Query().Get("prompt"))

	require.NoError(t, e.connect("user", "c1", "owner"))
	assert.Equal(t, "same_account", codeOf(e.connect("agent", "c2", "owner")))
	require.NoError(t, e.connect("agent", "c3", "oryn-bot"))

	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, st.Identities.User)
	require.NotNil(t, st.Identities.Agent)
	assert.Equal(t, "owner", st.Identities.User.Login)
	assert.Equal(t, "The Owner", *st.Identities.User.Name)
	assert.Equal(t, "oryn-bot", st.Identities.Agent.Login)
	assert.Nil(t, st.Identities.Agent.Name)
	assert.EqualValues(t, "ok", st.Identities.Agent.Status)

	row, err := e.st.Q.GetGithubIdentity(e.ctx, "user")
	require.NoError(t, err)
	assert.NotContains(t, string(row.AccessTokenEnc), "ghu_")
	require.NotNil(t, row.AccessExpiresAt)

	// 授权码兑换失败
	e.fake.Lock()
	e.fake.App.ClientSecret = "rotated"
	e.fake.Unlock()
	raw, err = e.s.BeginAuthorize(e.ctx, "user")
	require.NoError(t, err)
	_, err = e.s.FinishAuthorize(e.ctx, "nope", stateOf(t, raw))
	assert.Equal(t, "exchange_failed", codeOf(err))
}

func TestFinishAuthorizeStateKinds(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	e.fake.AddUser(1, "owner", "")
	e.fake.GrantCode("c1", "owner")

	// manifest 种类的 state 不能用于身份回调，且不被消费
	mstate := ids.Token("ogs")
	require.NoError(t, e.st.Q.InsertGithubState(e.ctx, db.InsertGithubStateParams{
		StateHash: ids.Hash(mstate), Kind: "manifest", ExpiresAt: e.clock.Now().Add(stateTTL).UnixMilli(),
	}))
	_, err := e.s.FinishAuthorize(e.ctx, "c1", mstate)
	assert.Equal(t, "state_invalid", codeOf(err))
	_, err = e.st.Q.GetGithubState(e.ctx, ids.Hash(mstate))
	require.NoError(t, err)

	// 身份 state 先被 FinishManifest 拒绝，之后仍可用于身份回调
	raw, err := e.s.BeginAuthorize(e.ctx, "user")
	require.NoError(t, err)
	state := stateOf(t, raw)
	assert.Equal(t, "state_invalid", codeOf(e.s.FinishManifest(e.ctx, "manifest-code", state)))
	slot, err := e.s.FinishAuthorize(e.ctx, "c1", state)
	require.NoError(t, err)
	assert.Equal(t, "user", slot)
	_, err = e.s.FinishAuthorize(e.ctx, "c1", state)
	assert.Equal(t, "state_invalid", codeOf(err))

	// 过期的 state
	raw, err = e.s.BeginAuthorize(e.ctx, "user")
	require.NoError(t, err)
	e.clock.Add(stateTTL + time.Second)
	_, err = e.s.FinishAuthorize(e.ctx, "c1", stateOf(t, raw))
	assert.Equal(t, "state_invalid", codeOf(err))
}

func TestBaseTokenRefresh(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	e.fake.AddUser(1, "owner", "")
	require.NoError(t, e.connect("user", "c1", "owner"))
	first, err := e.s.baseToken(e.ctx, "user")
	require.NoError(t, err)
	assert.Zero(t, e.refreshCalls())
	before, err := e.st.Q.GetGithubIdentity(e.ctx, "user")
	require.NoError(t, err)
	oldRefresh, err := e.s.cfg.Box.Open(aadRefresh("user"), before.RefreshTokenEnc)
	require.NoError(t, err)

	// 过期前 10 分钟：刷新
	e.clock.Add(8*time.Hour - 10*time.Minute)
	tok, err := e.s.baseToken(e.ctx, "user")
	require.NoError(t, err)
	assert.NotEqual(t, first, tok)
	assert.Equal(t, 1, e.refreshCalls())
	after, err := e.st.Q.GetGithubIdentity(e.ctx, "user")
	require.NoError(t, err)
	newRefresh, err := e.s.cfg.Box.Open(aadRefresh("user"), after.RefreshTokenEnc)
	require.NoError(t, err)
	assert.NotEqual(t, string(oldRefresh), string(newRefresh))

	// 刷新后不再刷新
	again, err := e.s.baseToken(e.ctx, "user")
	require.NoError(t, err)
	assert.Equal(t, tok, again)
	assert.Equal(t, 1, e.refreshCalls())
}

func TestBaseTokenConcurrentRefresh(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	e.fake.AddUser(1, "owner", "")
	require.NoError(t, e.connect("user", "c1", "owner"))
	e.clock.Add(8*time.Hour - 10*time.Minute)
	var wg sync.WaitGroup
	toks := make([]string, 2)
	for i := range toks {
		wg.Go(func() {
			tok, err := e.s.baseToken(e.ctx, "user")
			assert.NoError(t, err)
			toks[i] = tok
		})
	}
	wg.Wait()
	assert.Equal(t, 1, e.refreshCalls())
	assert.Equal(t, toks[0], toks[1])
}

func TestBaseTokenBadRefresh(t *testing.T) {
	e := newEnv(t)
	e.owner()
	e.installApp()
	e.fake.AddUser(1, "owner", "")
	bot := e.fake.AddUser(7, "oryn-bot", "")
	require.NoError(t, e.connect("user", "c1", "owner"))
	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))
	require.NoError(t, e.s.SetAutomationIdentity(e.ctx, "agent"))
	assert.Equal(t, "agent", e.s.AutomationIdentity(e.ctx))

	e.fake.Lock()
	bot.FailRefresh = true
	e.fake.Unlock()
	e.clock.Add(8*time.Hour - 10*time.Minute)
	_, err := e.s.baseToken(e.ctx, "agent")
	assert.Equal(t, "github_identity_required", codeOf(err))
	row, err := e.st.Q.GetGithubIdentity(e.ctx, "agent")
	require.NoError(t, err)
	assert.Equal(t, "reauth", row.Status)
	assert.Equal(t, "user", e.s.AutomationIdentity(e.ctx))
	// reauth 后不再请求上游
	n := e.refreshCalls()
	_, err = e.s.baseToken(e.ctx, "agent")
	assert.Equal(t, "github_identity_required", codeOf(err))
	assert.Equal(t, n, e.refreshCalls())

	// 未连接的槽位
	require.NoError(t, e.s.Disconnect(e.ctx, "agent"))
	_, err = e.s.baseToken(e.ctx, "agent")
	assert.Equal(t, "github_identity_required", codeOf(err))
}

func TestDisconnectAndAutomation(t *testing.T) {
	e := newEnv(t)
	e.owner()
	e.installApp()
	assert.Equal(t, "github_agent_required", codeOf(e.s.SetAutomationIdentity(e.ctx, "agent")))
	assert.Equal(t, "invalid_request", codeOf(e.s.SetAutomationIdentity(e.ctx, "bogus")))
	e.fake.AddUser(7, "oryn-bot", "")
	require.NoError(t, e.connect("agent", "c1", "oryn-bot"))
	require.NoError(t, e.s.SetAutomationIdentity(e.ctx, "agent"))
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, "agent", st.AutomationIdentity)

	require.NoError(t, e.s.Disconnect(e.ctx, "agent"))
	st, err = e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, "user", st.AutomationIdentity)
	assert.Nil(t, st.Identities.Agent)
	assert.Equal(t, "not_found", codeOf(e.s.Disconnect(e.ctx, "agent")))
}
