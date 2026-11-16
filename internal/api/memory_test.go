package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

	// 模型目录未装配：选项为空，五种用途照常返回
	r = e.do("GET", "/api/v1/memory/models", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var models struct{ Items, Options []any }
	r.json(t, &models)
	assert.Len(t, models.Items, 5)
	assert.Empty(t, models.Options)
}

func TestMemoryAPIUnavailable(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	r := e.do("GET", "/api/v1/memory/settings", c, nil)
	assert.Equal(t, 501, r.status)
	assert.Equal(t, "memory_unavailable", r.code(t))
}

// configureMemory 把记忆指向 base 并生成 .env，返回 Hindsight API key。
func configureMemory(t *testing.T, e *h2Env, c h2Cred, base string) string {
	t.Helper()
	r := e.do("PUT", "/api/v1/memory/settings", c, map[string]any{"enabled": true, "hindsightUrl": base})
	require.Equal(t, 200, r.status, string(r.body))
	r = e.do("POST", "/api/v1/memory/env-file", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var env struct{ Content string }
	r.json(t, &env)
	for _, l := range strings.Split(env.Content, "\n") {
		if v, ok := strings.CutPrefix(l, "HINDSIGHT_API_TENANT_API_KEY="); ok {
			return v
		}
	}
	t.Fatal(".env 里没有 HINDSIGHT_API_TENANT_API_KEY")
	return ""
}

// R22：导出经 orynd 代理下载，归档原样转发，Hindsight 的 key 与响应头都不外泄。
func TestMemoryAdminExportAPI(t *testing.T) {
	e, _ := newMemoryEnv(t)
	c := h2DeviceToken(t, e)
	zip := []byte("PK\x03\x04archive-bytes")
	var key string
	var completed bool
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"detail":"Authentication failed"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/default/banks/oryn/transfer/export":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation_id":"e87d7390-2c07","status":"pending"}`))
		case "/v1/default/banks/oryn/operations/e87d7390-2c07":
			st := "pending"
			if completed {
				st = "completed"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operation_id": "e87d7390-2c07", "status": st, "operation_type": "export_bank", "created_at": "2027-01-15T08:00:00.5+00:00",
				"result_metadata": map[string]any{"download_url": "/v1/default/files/download/tenants/public/banks/oryn/exports/x/transfer.zip"},
			})
		case "/v1/default/files/download/tenants/public/banks/oryn/exports/x/transfer.zip":
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="oryn-documents.zip"`)
			w.Header().Set("X-Upstream", key)
			_, _ = w.Write(zip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hs.Close)
	key = configureMemory(t, e, c, hs.URL)

	r := e.do("POST", "/api/v1/memory/exports", c, nil)
	require.Equal(t, 202, r.status, string(r.body))
	var exp struct{ ID, State string }
	r.json(t, &exp)
	assert.Equal(t, "e87d7390-2c07", exp.ID)
	assert.Equal(t, "running", exp.State)
	r = e.do("GET", "/api/v1/memory/exports/"+exp.ID+"/download", c, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "export_not_ready", r.code(t))

	completed = true
	r = e.do("GET", "/api/v1/memory/exports/"+exp.ID, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	r.json(t, &exp)
	assert.Equal(t, "done", exp.State)

	req, err := http.NewRequest("GET", e.srv.URL+"/api/v1/memory/exports/"+exp.ID+"/download", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+c.bearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, string(body))
	assert.Equal(t, zip, body)
	assert.Equal(t, "application/zip", resp.Header.Get("Content-Type"))
	assert.Equal(t, `attachment; filename="oryn-memory-e87d7390-2c07.zip"`, resp.Header.Get("Content-Disposition"))
	assert.Empty(t, resp.Header.Get("X-Upstream"), "Hindsight 的响应头不转发")
	for k, vs := range resp.Header {
		for _, v := range vs {
			assert.NotContains(t, v, key, k)
		}
	}
	assert.NotContains(t, string(body), key)
}

// R23：Hindsight 不可达时删除项目照常成功，项目的 document 与 mental model 进入重试队列。
func TestPurgeOnDeleteProjectHindsightDown(t *testing.T) {
	e, _ := newMemoryEnv(t)
	c := h2DeviceToken(t, e)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProject(ctx, db.CreateProjectParams{ID: "prj_a", Name: "alpha", CreatedAt: 1, UpdatedAt: 1}))
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	configureMemory(t, e, c, dead.URL)
	p := "prj_a"
	require.NoError(t, e.st.Q.UpsertMemoryDocument(ctx, db.UpsertMemoryDocumentParams{DocumentID: "sess-1", ProjectID: &p, ThreadID: new("T-1"), Now: 1}))

	r := e.do("DELETE", "/api/v1/projects/prj_a", c, nil)
	require.Equal(t, 204, r.status, string(r.body))

	r = e.do("GET", "/api/v1/memory/pending-deletes", c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var pend struct {
		Items []struct {
			DocumentID string `json:"documentId"`
			ProjectID  string `json:"projectId"`
			Attempts   int
			LastError  string `json:"lastError"`
		}
	}
	r.json(t, &pend)
	var ids []string
	for _, it := range pend.Items {
		ids = append(ids, it.DocumentID)
		assert.Equal(t, "prj_a", it.ProjectID)
		assert.NotEmpty(t, it.LastError)
	}
	assert.ElementsMatch(t, []string{"sess-1", "mental-model:project-conventions-prj_a", "mental-model:project-decisions-prj_a"}, ids)

	// 记忆服务未配置时删除项目同样不受影响
	e2, _ := newMemoryEnv(t)
	c2 := h2DeviceToken(t, e2)
	require.NoError(t, e2.st.Q.CreateProject(ctx, db.CreateProjectParams{ID: "prj_b", Name: "beta", CreatedAt: 1, UpdatedAt: 1}))
	assert.Equal(t, 204, e2.do("DELETE", "/api/v1/projects/prj_b", c2, nil).status)
}
