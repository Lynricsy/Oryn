package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/modelcat"
	"github.com/Lynricsy/Oryn/internal/store"
)

const catalogFakeOutput = `{"models":[
{"provider":"anthropic","kind":"chat","id":"claude-fable-5","selector":"anthropic/claude-fable-5","name":"Claude Fable 5","contextWindow":1000000,"maxTokens":128000,"reasoning":true,"thinking":["low","high"],"input":["text","image"],"cost":{"cacheRead":1,"cacheWrite":12.5,"input":10,"output":50},"pricingStatus":"fixed"},
{"provider":"openai","kind":"embedding","id":"text-embedding-3-small","selector":"openai/text-embedding-3-small","name":"Embedding","contextWindow":8191,"maxTokens":0,"reasoning":false,"thinking":null,"input":["text"]}
]}`

// fakeCatalogRun 是计数的假 `omp models` 运行。
type fakeCatalogRun struct {
	n    atomic.Int32
	fail atomic.Bool
}

func (f *fakeCatalogRun) run(context.Context, string) ([]byte, []byte, error) {
	f.n.Add(1)
	if f.fail.Load() {
		return nil, []byte("boom\n"), errors.New("exit status 1")
	}
	return []byte(catalogFakeOutput), []byte("warn: 某个 provider 发现失败\n"), nil
}

// newCatalogServer 起一个只挂模型目录的服务（cat 可为 nil）。
func newCatalogServer(t *testing.T, home *agentcfg.Home, cat *modelcat.Catalog) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.DefaultOrynd()
	cfg.DataDir = dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Auth = &api.AuthDeps{Home: home, ModelCatalog: cat}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(srv.Close)
	return srv
}

func getCatalog(t *testing.T, srv *httptest.Server, query string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/v1/models/catalog" + query)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, b
}

func TestModelCatalogAPI(t *testing.T) {
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)
	f := &fakeCatalogRun{}
	srv := newCatalogServer(t, home, modelcat.New(modelcat.Config{Run: f.run}, home, nil))

	code, body := getCatalog(t, srv, "")
	require.Equal(t, 200, code, string(body))
	var cat apigen.ModelCatalog
	require.NoError(t, json.Unmarshal(body, &cat))
	assert.False(t, cat.Stale)
	assert.NotZero(t, cat.FetchedAt)
	assert.Equal(t, []string{"warn: 某个 provider 发现失败"}, cat.Warnings)
	require.Len(t, cat.Models, 2)
	m := cat.Models[0]
	assert.Equal(t, "anthropic/claude-fable-5", m.Selector)
	assert.Equal(t, "Claude Fable 5", m.Name)
	assert.Equal(t, "chat", m.Kind)
	require.NotNil(t, m.ContextWindow)
	assert.Equal(t, 1000000, *m.ContextWindow)
	assert.Equal(t, []string{"low", "high"}, m.Thinking)
	require.NotNil(t, m.Cost)
	require.NotNil(t, m.Cost.CacheWrite)
	assert.InDelta(t, 12.5, *m.Cost.CacheWrite, 1e-6)
	var raw struct{ Models []map[string]any }
	require.NoError(t, json.Unmarshal(body, &raw))
	assert.NotContains(t, raw.Models[1], "maxTokens", "零值的可选字段省略")
	assert.NotContains(t, raw.Models[1], "thinking")
	assert.NotContains(t, raw.Models[1], "cost")
	assert.EqualValues(t, 1, f.n.Load())

	// 配置不变：命中缓存
	code, _ = getCatalog(t, srv, "?refresh=false")
	require.Equal(t, 200, code)
	assert.EqualValues(t, 1, f.n.Load())

	// refresh=true 强制重跑
	code, _ = getCatalog(t, srv, "?refresh=true")
	require.Equal(t, 200, code)
	assert.EqualValues(t, 2, f.n.Load())

	// 写入 models.yml 后的下一次 GET 重新运行
	require.NoError(t, os.WriteFile(filepath.Join(home.Dir(), agentcfg.ModelsFile),
		[]byte("providers:\n  custom:\n    baseUrl: https://a.example.com/v1\n"), 0o600))
	code, _ = getCatalog(t, srv, "")
	require.Equal(t, 200, code)
	assert.EqualValues(t, 3, f.n.Load())

	// 运行失败：返回旧结果并标 stale
	f.fail.Store(true)
	code, body = getCatalog(t, srv, "?refresh=true")
	require.Equal(t, 200, code, string(body))
	require.NoError(t, json.Unmarshal(body, &cat))
	assert.True(t, cat.Stale)
	assert.Len(t, cat.Models, 2)
}

func TestModelCatalogUnavailable(t *testing.T) {
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)

	// 从未成功过：503 catalog_unavailable
	f := &fakeCatalogRun{}
	f.fail.Store(true)
	srv := newCatalogServer(t, home, modelcat.New(modelcat.Config{Run: f.run}, home, nil))
	code, body := getCatalog(t, srv, "")
	assert.Equal(t, 503, code, string(body))
	var e struct{ Code string }
	require.NoError(t, json.Unmarshal(body, &e))
	assert.Equal(t, "catalog_unavailable", e.Code)

	// 没有目录（未配置 omp）：同样 503
	code, body = getCatalog(t, newCatalogServer(t, home, nil), "")
	assert.Equal(t, 503, code, string(body))
	require.NoError(t, json.Unmarshal(body, &e))
	assert.Equal(t, "catalog_unavailable", e.Code)
}
