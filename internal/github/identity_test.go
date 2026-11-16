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
	assert.Nil(t, row.AccessExpiresAt, "不再记录过期时间")
	assert.Nil(t, row.RefreshTokenEnc, "不再保存刷新令牌")

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
	assert.Nil(t, row.AccessExpiresAt)
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
	assert.Equal(t, "agent", e.s.AutomationIdentity(e.ctx))

	_, err := e.s.tokenFor(e.ctx, "bogus")
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

	// 401 → 槽位 reauth，自动化身份退回我的账号
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /user": http.StatusUnauthorized}
	e.fake.Unlock()
	err = e.s.callAs(e.ctx, "agent", http.MethodGet, "/user", nil, nil)
	assert.Equal(t, kindReauth, errKind(err))
	assert.True(t, isStatus(err, http.StatusUnauthorized))
	row, err := e.st.Q.GetGithubIdentity(e.ctx, "agent")
	require.NoError(t, err)
	assert.Equal(t, "reauth", row.Status)
	assert.Equal(t, "user", e.s.AutomationIdentity(e.ctx))
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
