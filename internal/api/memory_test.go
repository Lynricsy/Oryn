package api_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// newMemoryEnv 是只装配记忆接口的控制面；restarts 记录 Restart 回调（"*" = 全部线程）。
func newMemoryEnv(t *testing.T) (*h2Env, *[]string) {
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
	restarts := &[]string{}
	svc := memory.New(memory.Config{Q: st.Q, Box: box, PublicURL: h2PublicURL, Restart: func(_ context.Context, p *string) {
		if p == nil {
			*restarts = append(*restarts, "*")
			return
		}
		*restarts = append(*restarts, *p)
	}})
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Memory = &api.MemoryDeps{Service: svc}
	e := &h2Env{t: t, st: st, dataDir: dir}
	e.srv = httptest.NewServer(api.NewRouter(s))
	t.Cleanup(e.srv.Close)
	return e, restarts
}

func TestMemoryAPI(t *testing.T) {
	e, restarts := newMemoryEnv(t)
	c := h2DeviceToken(t, e)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProject(ctx, db.CreateProjectParams{ID: "prj_a", Name: "alpha", CreatedAt: 1, UpdatedAt: 1}))

	assert.Equal(t, 401, e.do("GET", "/api/v1/memory/settings", h2Cred{}, nil).status)
	r := e.do("GET", "/api/v1/memory/settings", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var settings struct {
		Enabled, KeysGenerated bool
		HindsightURL           *string `json:"hindsightUrl"`
		OutputLanguage         string
	}
	r.json(t, &settings)
	assert.False(t, settings.Enabled)
	assert.False(t, settings.KeysGenerated)
	assert.Nil(t, settings.HindsightURL)
	assert.Equal(t, "简体中文", settings.OutputLanguage)

	var status struct{ State, TestedMinor string }
	r = e.do("GET", "/api/v1/memory/status", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &status)
	assert.Equal(t, "unconfigured", status.State)
	assert.Equal(t, memory.TestedMinor, status.TestedMinor)

	// R2：远端明文地址拒绝保存
	r = e.do("PUT", "/api/v1/memory/settings", c, map[string]any{"enabled": true, "hindsightUrl": "http://memory.example.com:8888"})
	assert.Equal(t, 400, r.status)
	assert.Equal(t, "insecure_url", r.code(t))
	r = e.do("PUT", "/api/v1/memory/settings", c, map[string]any{"enabled": true, "hindsightUrl": "http://172.17.0.1:8888/"})
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &settings)
	require.NotNil(t, settings.HindsightURL)
	assert.Equal(t, "http://172.17.0.1:8888", *settings.HindsightURL)
	assert.Empty(t, *restarts, "未生成密钥时生效值不变")

	r = e.do("POST", "/api/v1/memory/env-file", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var envFile struct {
		Content     string
		GeneratedAt int64
	}
	r.json(t, &envFile)
	assert.Contains(t, envFile.Content, "HINDSIGHT_API_TENANT_API_KEY=hsk_")
	assert.Contains(t, envFile.Content, "HINDSIGHT_API_LLM_BASE_URL="+h2PublicURL+"/memory-relay/v1/default\n")
	assert.NotZero(t, envFile.GeneratedAt)
	assert.Equal(t, []string{"*"}, *restarts)

	r = e.do("GET", "/api/v1/memory/settings", c, nil)
	r.json(t, &settings)
	assert.True(t, settings.Enabled)
	assert.True(t, settings.KeysGenerated)
	assert.NotContains(t, string(r.body), "hsk_", "设置接口不回显密钥")

	// 项目开关
	var list struct {
		Items []struct {
			ProjectID           string `json:"projectId"`
			Name                string
			Enabled, Overridden bool
		}
	}
	r = e.do("GET", "/api/v1/memory/projects", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &list)
	require.Len(t, list.Items, 1)
	assert.True(t, list.Items[0].Enabled)
	assert.False(t, list.Items[0].Overridden)

	r = e.do("PUT", "/api/v1/memory/projects/prj_a", c, map[string]bool{"enabled": false})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, []string{"*", "prj_a"}, *restarts)
	r = e.do("GET", "/api/v1/memory/projects", c, nil)
	r.json(t, &list)
	assert.False(t, list.Items[0].Enabled)
	assert.True(t, list.Items[0].Overridden)
	assert.Equal(t, 404, e.do("PUT", "/api/v1/memory/projects/prj_zz", c, map[string]bool{"enabled": false}).status)

	// 关闭总开关
	r = e.do("PUT", "/api/v1/memory/settings", c, map[string]any{"enabled": false, "hindsightUrl": "http://172.17.0.1:8888"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Equal(t, []string{"*", "prj_a", "*"}, *restarts)

	// 其余记忆接口由后续步骤实现
	assert.Equal(t, 501, e.do("GET", "/api/v1/memory/models", c, nil).status)
}

func TestMemoryAPIUnavailable(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	r := e.do("GET", "/api/v1/memory/settings", c, nil)
	assert.Equal(t, 501, r.status)
	assert.Equal(t, "memory_unavailable", r.code(t))
}
