package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/sshgw"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// noOrbs 满足 sshgw.Orbs：REST 测试不走网关。
type noOrbs struct{}

func (noOrbs) Get(context.Context, string) (db.Orb, error) { return db.Orb{}, nil }

// newSSHEnv 是只装配 SSH 接口的控制面。
func newSSHEnv(t *testing.T) *h2Env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.LoadOrCreate(dir)
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	svc, err := sshgw.New(sshgw.Config{Box: box, DataDir: dir}, st, noOrbs{})
	require.NoError(t, err)
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.SSH = &api.SSHDeps{Service: svc}
	e := &h2Env{t: t, st: st, dataDir: dir}
	e.srv = httptest.NewServer(api.NewRouter(s))
	t.Cleanup(e.srv.Close)
	return e
}

func TestSSHAPIKeysAndHosts(t *testing.T) {
	e := newSSHEnv(t)
	c := h2DeviceToken(t, e)

	assert.Equal(t, 401, e.do("GET", "/api/v1/ssh/hosts", h2Cred{}, nil).status)

	// 钥匙：生成 201，同名 409，改名，响应不带私钥
	r := e.do("POST", "/api/v1/ssh/keys", c, map[string]any{"name": "k1", "mode": "generate"})
	require.Equal(t, 201, r.status, string(r.body))
	var key apigen.SSHKey
	r.json(t, &key)
	assert.True(t, strings.HasPrefix(key.PublicKey, "ssh-ed25519 "))
	assert.NotContains(t, strings.ToLower(string(r.body)), "private")
	r = e.do("POST", "/api/v1/ssh/keys", c, map[string]any{"name": "k1", "mode": "generate"})
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "conflict", r.code(t))
	r = e.do("POST", "/api/v1/ssh/keys", c, map[string]any{"name": "k2", "mode": "import", "privateKey": "garbage"})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "invalid_request", r.code(t))
	r = e.do("PATCH", "/api/v1/ssh/keys/"+key.ID, c, map[string]any{"name": "main"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, 404, e.do("PATCH", "/api/v1/ssh/keys/sshk_nope", c, map[string]any{"name": "x"}).status)

	// 主机：跳板 + 密码登录；响应永不带密码
	r = e.do("POST", "/api/v1/ssh/hosts", c, map[string]any{
		"name": "jump", "address": "10.0.0.1", "username": "root", "authType": "key", "keyId": key.ID,
	})
	require.Equal(t, 201, r.status, string(r.body))
	var jump apigen.SSHHost
	r.json(t, &jump)
	r = e.do("POST", "/api/v1/ssh/hosts", c, map[string]any{
		"name": "web", "address": "10.0.0.2", "username": "deploy", "authType": "password",
		"password": "s3cr3t-pw", "jumpHostId": jump.ID,
	})
	require.Equal(t, 201, r.status, string(r.body))
	var web apigen.SSHHost
	r.json(t, &web)
	assert.NotContains(t, string(r.body), "s3cr3t-pw")
	assert.NotContains(t, string(r.body), `"password":`)
	require.NotNil(t, web.JumpHostID)
	assert.True(t, web.HasPassword)

	r = e.do("POST", "/api/v1/ssh/hosts", c, map[string]any{"name": "web", "address": "x", "username": "u", "authType": "key", "keyId": key.ID})
	assert.Equal(t, 409, r.status, "同层同名")
	r = e.do("POST", "/api/v1/ssh/hosts", c, map[string]any{"name": "bad", "address": "x", "username": "u", "authType": "password"})
	assert.Equal(t, 400, r.status, "密码登录缺密码")

	// 被引用的钥匙删不掉；被当跳板的主机删不掉
	assert.Equal(t, 409, e.do("DELETE", "/api/v1/ssh/keys/"+key.ID, c, nil).status)
	assert.Equal(t, 409, e.do("DELETE", "/api/v1/ssh/hosts/"+jump.ID, c, nil).status)

	// PATCH：缺省不动跳板，null 清掉
	r = e.do("PATCH", "/api/v1/ssh/hosts/"+web.ID, c, map[string]any{"description": "front"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &web)
	require.NotNil(t, web.JumpHostID, "缺省 = 不改")
	r = e.do("PATCH", "/api/v1/ssh/hosts/"+web.ID, c, map[string]any{"jumpHostId": nil})
	require.Equal(t, 200, r.status, string(r.body))
	web = apigen.SSHHost{}
	r.json(t, &web)
	assert.Nil(t, web.JumpHostID, "null = 清掉")
	assert.Equal(t, "front", web.Description)

	r = e.do("GET", "/api/v1/ssh/hosts", c, nil)
	require.Equal(t, 200, r.status)
	var list apigen.SSHHostList
	r.json(t, &list)
	assert.Len(t, list.Hosts, 2)
	assert.NotContains(t, string(r.body), "s3cr3t-pw")

	r = e.do("POST", "/api/v1/ssh/hosts/"+web.ID+"/reset-host-key", c, nil)
	assert.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, 404, e.do("GET", "/api/v1/ssh/hosts/ssh_nope", c, nil).status)

	require.Equal(t, 204, e.do("DELETE", "/api/v1/ssh/hosts/"+web.ID, c, nil).status)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/ssh/hosts/"+jump.ID, c, nil).status)
	require.Equal(t, 204, e.do("DELETE", "/api/v1/ssh/keys/"+key.ID, c, nil).status)
	assert.Equal(t, 404, e.do("DELETE", "/api/v1/ssh/keys/"+key.ID, c, nil).status)
}

func TestSSHAPIAuditAndThreadHosts(t *testing.T) {
	e := newSSHEnv(t)
	c := h2DeviceToken(t, e)

	r := e.do("GET", "/api/v1/ssh/audit", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var list apigen.SSHAuditList
	r.json(t, &list)
	assert.Empty(t, list.Entries)
	assert.Equal(t, 400, e.do("GET", "/api/v1/ssh/audit?before=0", c, nil).status)
	assert.Equal(t, 400, e.do("GET", "/api/v1/ssh/audit?limit=0", c, nil).status)

	r = e.do("GET", "/api/v1/threads/thr_nope/ssh-hosts", c, nil)
	assert.Equal(t, 404, r.status)
	assert.Equal(t, "not_found", r.code(t))
}
