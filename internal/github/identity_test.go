package github

import (
	"net/http"
	"net/url"
	"strings"
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

func TestAuthorizeAndSameAccount(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.BeginAuthorize(e.ctx, "user")
	assert.Equal(t, "github_not_configured", codeOf(err))

	e.oauthApp()
	e.fake.AddUser(1, "owner", "The Owner")
	e.fake.AddUser(7, "oryn-bot", "")

	raw, err := e.s.BeginAuthorize(e.ctx, "user")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, e.fake.URL()+"/login/oauth/authorize", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, "Ov23.test", u.Query().Get("client_id"))
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
	assert.NotContains(t, string(row.AccessTokenEnc), "gho_")

	// 授权码兑换失败
	e.fake.Lock()
	e.fake.OAuthApp.ClientSecret = "rotated"
	e.fake.Unlock()
	raw, err = e.s.BeginAuthorize(e.ctx, "user")
	require.NoError(t, err)
	_, err = e.s.FinishAuthorize(e.ctx, "nope", stateOf(t, raw))
	assert.Equal(t, "exchange_failed", codeOf(err))
}

func TestFinishAuthorizeStateKinds(t *testing.T) {
	e := newEnv(t)
	e.oauthApp()
	e.fake.AddUser(1, "owner", "")
	e.fake.GrantCode("c1", "owner")

	// 不存在的 state
	_, err := e.s.FinishAuthorize(e.ctx, "c1", ids.Token("ogs"))
	assert.Equal(t, "state_invalid", codeOf(err))

	// 身份 state 只能用一次
	raw, err := e.s.BeginAuthorize(e.ctx, "user")
	require.NoError(t, err)
	state := stateOf(t, raw)
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

func TestAuthorizeScopes(t *testing.T) {
	e := newEnv(t)
	e.oauthApp()
	e.fake.AddUser(1, "owner", "")
	e.fake.AddUser(7, "oryn-bot", "")

	raw, err := e.s.BeginAuthorize(e.ctx, "user")
	require.NoError(t, err)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, oauthScopes, u.Query().Get("scope"))
	assert.Equal(t, e.fake.OAuthApp.ClientID, u.Query().Get("client_id"))

	// 授予全部 scope：missingScopes 为空
	require.NoError(t, e.connect("user", "c1", "owner"))
	calls := e.fake.CallsTo("POST", "/login/oauth/access_token")
	require.NotEmpty(t, calls)
	assert.Contains(t, string(calls[len(calls)-1].Body), "client_id="+e.fake.OAuthApp.ClientID)
	row, err := e.st.Q.GetGithubIdentity(e.ctx, "user")
	require.NoError(t, err)
	assert.Equal(t, oauthScopes, row.Scopes)
	tok, err := e.s.tokenFor(e.ctx, "user")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(tok, "gho_"), tok)

	// 授予较少：missingScopes 非空（`user` 覆盖 `user:email`）
	e.fake.Lock()
	e.fake.Scopes = "repo, gist, user"
	e.fake.Unlock()
	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, st.Identities.User)
	require.NotNil(t, st.Identities.Agent)
	assert.Equal(t, strings.Fields(oauthScopes), st.Identities.User.Scopes)
	assert.Empty(t, st.Identities.User.MissingScopes)
	assert.Equal(t, []string{"repo", "gist", "user"}, st.Identities.Agent.Scopes)
	assert.Equal(t, []string{"workflow", "read:org", "admin:repo_hook", "write:packages"}, st.Identities.Agent.MissingScopes)
}

func TestTokenForAndReauth(t *testing.T) {
	e := newEnv(t)
	e.owner()
	e.oauthApp()
	e.fake.AddUser(1, "owner", "")
	e.fake.AddUser(7, "oryn-bot", "")
	require.NoError(t, e.connect("user", "c1", "owner"))
	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))
	require.NoError(t, e.s.SetAutomationIdentity(e.ctx, "agent"))
	slot, note, err := e.s.ResolveAutomationSlot(e.ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "agent", slot)
	assert.Empty(t, note)

	_, err = e.s.tokenFor(e.ctx, "bogus")
	assert.Equal(t, "invalid_request", codeOf(err))
	// 令牌不过期：拨很久的时钟仍直接解密，不请求令牌端点
	n := len(e.fake.CallsTo("POST", "/login/oauth/access_token"))
	e.clock.Add(365 * 24 * time.Hour)
	tok, err := e.s.tokenFor(e.ctx, "agent")
	require.NoError(t, err)
	assert.NotEmpty(t, tok)
	assert.Len(t, e.fake.CallsTo("POST", "/login/oauth/access_token"), n)

	var me struct{ Login string }
	require.NoError(t, e.s.callAs(e.ctx, "agent", http.MethodGet, "/user", nil, &me))
	assert.Equal(t, "oryn-bot", me.Login)

	// 401 → 槽位 reauth，自动化身份回退到我的账号（带回退提示）
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /user": http.StatusUnauthorized}
	e.fake.Unlock()
	err = e.s.callAs(e.ctx, "agent", http.MethodGet, "/user", nil, nil)
	assert.Equal(t, kindReauth, errKind(err))
	assert.True(t, isStatus(err, http.StatusUnauthorized))
	row, err := e.st.Q.GetGithubIdentity(e.ctx, "agent")
	require.NoError(t, err)
	assert.Equal(t, "reauth", row.Status)
	slot, note, err = e.s.ResolveAutomationSlot(e.ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "user", slot)
	assert.Equal(t, agentFallbackNote, note)
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, "reauth", st.Identities.Agent.Status)
	assert.EqualValues(t, "ok", st.Identities.User.Status)

	// reauth 后不再请求上游
	calls := len(e.fake.CallsTo("GET", "/user"))
	err = e.s.callAs(e.ctx, "agent", http.MethodGet, "/user", nil, nil)
	assert.Equal(t, "github_identity_required", codeOf(err))
	assert.Len(t, e.fake.CallsTo("GET", "/user"), calls)

	// 重新授权后恢复
	e.fake.Lock()
	e.fake.Fail = nil
	e.fake.Unlock()
	require.NoError(t, e.connect("agent", "c3", "oryn-bot"))
	_, err = e.s.tokenFor(e.ctx, "agent")
	require.NoError(t, err)

	// 未连接的槽位
	require.NoError(t, e.s.Disconnect(e.ctx, "agent"))
	_, err = e.s.tokenFor(e.ctx, "agent")
	assert.Equal(t, "github_identity_required", codeOf(err))
}

// 1.6：断开 Agent 账号不改设置；Agent 不可用时可信自动化回退到我的账号并带提示，不可信的拒绝；设置为 user 时不回退也不提示。
func TestDisconnectAndAutomation(t *testing.T) {
	e := newEnv(t)
	e.owner()
	e.oauthApp()
	st, err := e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, "agent", st.AutomationIdentity, "默认 agent")
	assert.Equal(t, "invalid_request", codeOf(e.s.SetAutomationIdentity(e.ctx, "bogus")))
	require.NoError(t, e.s.SetAutomationIdentity(e.ctx, "agent"), "Agent 未连接也可设为 agent")

	// Agent 未连接：回退与拒绝
	slot, note, err := e.s.ResolveAutomationSlot(e.ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "user", slot)
	assert.Equal(t, agentFallbackNote, note)
	_, _, err = e.s.ResolveAutomationSlot(e.ctx, true)
	assert.ErrorIs(t, err, errAgentRequired)
	assert.Equal(t, "github_agent_required", codeOf(err))

	e.fake.AddUser(7, "oryn-bot", "")
	require.NoError(t, e.connect("agent", "c1", "oryn-bot"))
	slot, note, err = e.s.ResolveAutomationSlot(e.ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "agent", slot)
	assert.Empty(t, note)

	// 设为 user：可信自动化用 user、无提示；不可信的仍只用 Agent
	require.NoError(t, e.s.SetAutomationIdentity(e.ctx, "user"))
	slot, note, err = e.s.ResolveAutomationSlot(e.ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "user", slot)
	assert.Empty(t, note)
	slot, _, err = e.s.ResolveAutomationSlot(e.ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "agent", slot)

	// 断开 Agent 后设置值不变
	require.NoError(t, e.s.SetAutomationIdentity(e.ctx, "agent"))
	require.NoError(t, e.s.Disconnect(e.ctx, "agent"))
	st, err = e.s.Status(e.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, "agent", st.AutomationIdentity)
	assert.Nil(t, st.Identities.Agent)
	assert.Equal(t, "not_found", codeOf(e.s.Disconnect(e.ctx, "agent")))
	_, _, err = e.s.ResolveAutomationSlot(e.ctx, true)
	assert.ErrorIs(t, err, errAgentRequired)
}
