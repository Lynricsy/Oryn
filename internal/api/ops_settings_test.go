package api_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/opsconf"
	"github.com/Lynricsy/Oryn/internal/store"
)

// TestOpsSettings 走真实路由：读写权限、覆盖与撤销、校验失败。
func TestOpsSettings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = "https://oryn.test", dir
	conf, err := opsconf.New(ctx, st, cfg)
	require.NoError(t, err)
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Ops = &api.OpsDeps{Conf: conf}
	e := &h2Env{t: t, st: st, srv: httptest.NewServer(api.NewRouter(s))}
	t.Cleanup(func() { e.srv.Close(); _ = st.Close() })

	_, dev, err := auth.CreateDevice(ctx, st, "t", "cli", "")
	require.NoError(t, err)
	web := e.webSession()
	const path = "/api/v1/server/ops-settings"

	assert.Equal(t, 401, e.do("GET", path, h2Cred{}, nil).status)

	r := e.do("GET", path, h2Cred{bearer: dev}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var got apigen.OpsSettingsState
	r.json(t, &got)
	assert.Equal(t, int64(300000), got.Effective.Idle.AgentIdleMs)
	assert.Equal(t, []string{}, got.Overridden)
	assert.Nil(t, got.UpdatedAt)

	patch := map[string]any{"idle": map[string]any{"agentIdleMs": 600000}}
	r = e.do("PATCH", path, h2Cred{bearer: dev}, patch)
	assert.Equal(t, 403, r.status)
	assert.Equal(t, "forbidden", r.code(t))
	assert.Equal(t, 403, e.do("PATCH", path, h2Cred{cookie: web}, patch).status)

	r = e.do("PATCH", path, h2Cred{cookie: web, csrf: true}, patch)
	require.Equal(t, 200, r.status, string(r.body))
	got = apigen.OpsSettingsState{}
	r.json(t, &got)
	assert.Equal(t, int64(600000), got.Effective.Idle.AgentIdleMs)
	assert.Equal(t, int64(300000), got.Yaml.Idle.AgentIdleMs)
	assert.Equal(t, []string{"idle.agent_idle"}, got.Overridden)
	assert.NotNil(t, got.UpdatedAt)

	r = e.do("PATCH", path, h2Cred{cookie: web, csrf: true}, map[string]any{"statsRetention": map[string]any{"samplesMs": 1000}})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "invalid_request", r.code(t))
	r = e.do("PATCH", path, h2Cred{cookie: web, csrf: true}, map[string]any{"idle": map[string]any{"bogus": 1}})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "invalid_request", r.code(t))

	r = e.do("PATCH", path, h2Cred{cookie: web, csrf: true}, map[string]any{"idle": map[string]any{"agentIdleMs": nil}})
	require.Equal(t, 200, r.status, string(r.body))
	got = apigen.OpsSettingsState{}
	r.json(t, &got)
	assert.Equal(t, int64(300000), got.Effective.Idle.AgentIdleMs)
	assert.Equal(t, []string{}, got.Overridden)
	assert.Nil(t, got.UpdatedAt)
}
