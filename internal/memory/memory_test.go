package memory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const testPublicURL = "https://oryn.test"

// fakeHindsight 是只实现 orynd 用到的端点的假 Hindsight。
type fakeHindsight struct {
	srv *httptest.Server

	mu         sync.Mutex
	key        string
	version    string
	ready      int
	count      int64
	bankExists bool
	puts       []map[string]any
}

func newFakeHindsight(t *testing.T) *fakeHindsight {
	t.Helper()
	f := &fakeHindsight{version: "1.2.1", ready: http.StatusOK, bankExists: true}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHindsight) set(fn func(f *fakeHindsight)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeHindsight) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.key || f.key == "" {
		http.Error(w, `{"detail":"invalid api key"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/version":
		_ = json.NewEncoder(w).Encode(map[string]any{"api_version": f.version, "features": map[string]bool{}})
	case r.Method == http.MethodGet && r.URL.Path == "/health/ready":
		w.WriteHeader(f.ready)
		_, _ = w.Write([]byte(`{"status":"x"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/default/banks/oryn/stats":
		if !f.bankExists {
			http.Error(w, `{"detail":"bank not found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"bank_id": "oryn", "total_nodes": f.count, "total_documents": 1})
	case r.Method == http.MethodPut && r.URL.Path == "/v1/default/banks/oryn":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.puts = append(f.puts, body)
		f.bankExists = true
		_, _ = w.Write([]byte(`{"bank_id":"oryn"}`))
	default:
		http.NotFound(w, r)
	}
}

type env struct {
	st       *store.Store
	svc      *memory.Service
	now      time.Time
	restarts []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	e := &env{st: st, now: time.Unix(1_800_000_000, 0)}
	e.svc = memory.New(memory.Config{
		Q: st.Q, Box: box, PublicURL: testPublicURL + "/", Now: func() time.Time { return e.now },
		Restart: func(_ context.Context, projectID *string) {
			if projectID == nil {
				e.restarts = append(e.restarts, "*")
			} else {
				e.restarts = append(e.restarts, *projectID)
			}
		},
	})
	return e
}

func (e *env) project(t *testing.T, id, name string) {
	t.Helper()
	require.NoError(t, e.st.Q.CreateProject(context.Background(), db.CreateProjectParams{ID: id, Name: name, CreatedAt: 1, UpdatedAt: 1}))
}

// envValue 取 .env 文本里某个键的值。
func envValue(t *testing.T, content, key string) string {
	t.Helper()
	for _, l := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	t.Fatalf(".env 里没有 %s:\n%s", key, content)
	return ""
}

// configure 填地址、生成 .env，并让假 Hindsight 接受新 key；返回 .env 文本。
func (e *env) configure(t *testing.T, f *fakeHindsight, enabled bool) string {
	t.Helper()
	ctx := context.Background()
	u := f.srv.URL
	_, err := e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: enabled, HindsightURL: &u})
	require.NoError(t, err)
	out, err := e.svc.GenerateEnvFile(ctx)
	require.NoError(t, err)
	f.set(func(f *fakeHindsight) { f.key = envValue(t, out.Content, "HINDSIGHT_API_TENANT_API_KEY") })
	return out.Content
}

func TestVersionMinor(t *testing.T) {
	assert.Equal(t, "1.2", memory.MinorOf("1.2.1"))
	assert.Equal(t, "1.3", memory.MinorOf("v1.3.0rc1"))
	assert.Equal(t, "", memory.MinorOf("garbage"))
	assert.False(t, memory.VersionMismatch("1.2.9"))
	assert.True(t, memory.VersionMismatch("1.3.0"))
	assert.True(t, memory.VersionMismatch(""))
}

// R2：远端地址必须用 https；本机、私有网段与 docker 网关可以明文。
func TestSettingsRejectsInsecureURL(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, raw := range []string{"http://example.com:8888", "http://8.8.8.8", "http://hindsight.internal", "http://[2001:db8::1]:8888"} {
		_, err := e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: true, HindsightURL: &raw})
		var me *memory.Error
		require.ErrorAs(t, err, &me, raw)
		assert.Equal(t, "insecure_url", me.Code, raw)
		assert.Equal(t, http.StatusBadRequest, me.Status, raw)
	}
	for _, raw := range []string{"ftp://x", "http://", "https://u:p@x.test"} {
		_, err := e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{HindsightURL: &raw})
		var me *memory.Error
		require.ErrorAs(t, err, &me, raw)
		assert.Equal(t, "invalid_request", me.Code, raw)
	}
	for raw, want := range map[string]string{
		"http://127.0.0.1:8888/":       "http://127.0.0.1:8888",
		"http://172.17.0.1:8888":       "http://172.17.0.1:8888",
		"http://localhost:8888":        "http://localhost:8888",
		"http://[::1]:8888":            "http://[::1]:8888",
		"http://192.168.1.20:8888":     "http://192.168.1.20:8888",
		"https://memory.example.com/":  "https://memory.example.com",
		"https://memory.example.com/x": "https://memory.example.com/x",
	} {
		got, err := e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{HindsightURL: &raw})
		require.NoError(t, err, raw)
		require.NotNil(t, got.HindsightURL, raw)
		assert.Equal(t, want, *got.HindsightURL, raw)
		assert.Equal(t, "简体中文", got.OutputLanguage)
	}
	// 省略地址 = 清空
	got, err := e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{})
	require.NoError(t, err)
	assert.Nil(t, got.HindsightURL)
}

func TestStatusStates(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	f := newFakeHindsight(t)

	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateUnconfigured, st.State)
	assert.Equal(t, memory.TestedMinor, st.TestedMinor)

	u := f.srv.URL
	_, err = e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: true, HindsightURL: &u})
	require.NoError(t, err)
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateNoKeys, st.State)

	e.configure(t, f, true)
	f.set(func(f *fakeHindsight) { f.count = 3 })
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateOk, st.State, st.Error)
	require.NotNil(t, st.Version)
	assert.Equal(t, "1.2.1", *st.Version)
	assert.False(t, st.VersionMismatch)
	require.NotNil(t, st.MemoryCount)
	assert.EqualValues(t, 3, *st.MemoryCount)
	assert.True(t, st.EmbedLocked, "bank 有记忆后嵌入锁定")
	assert.Equal(t, e.now.Unix(), st.CheckedAt)

	// R1：服务端小版本与已验证的不同 → 警告
	f.set(func(f *fakeHindsight) { f.version = "1.3.0" })
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateOk, st.State)
	assert.True(t, st.VersionMismatch)
	f.set(func(f *fakeHindsight) { f.version = "1.2.4" })

	// 总开关关闭：照常探测，状态为 disabled
	u = f.srv.URL
	_, err = e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: false, HindsightURL: &u})
	require.NoError(t, err)
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateDisabled, st.State)
	require.NotNil(t, st.MemoryCount)

	// 就绪检查失败
	f.set(func(f *fakeHindsight) { f.ready = http.StatusServiceUnavailable })
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateNotReady, st.State)
	require.NotNil(t, st.Error)
	f.set(func(f *fakeHindsight) { f.ready = http.StatusOK })

	// key 不对
	f.set(func(f *fakeHindsight) { f.key = "other" })
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateUnauthorized, st.State)
}

// 上次有记忆、这次为 0 → suspect_empty（issue #6485），且警告不被这次的 0 覆盖。
func TestStatusSuspectEmpty(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	f := newFakeHindsight(t)
	e.configure(t, f, true)

	// 全新空库：记忆数 0 是正常的
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateOk, st.State)

	f.set(func(f *fakeHindsight) { f.count = 12 })
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateOk, st.State)

	f.set(func(f *fakeHindsight) { f.count = 0 })
	for range 2 {
		st, err = e.svc.Status(ctx)
		require.NoError(t, err)
		assert.Equal(t, apigen.MemoryStateSuspectEmpty, st.State)
		require.NotNil(t, st.Error)
		assert.Contains(t, *st.Error, "12")
	}

	f.set(func(f *fakeHindsight) { f.count = 12 })
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateOk, st.State)
}

// bank 不存在时由 orynd 创建并写入中文 mission。
func TestStatusEnsuresBank(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	f := newFakeHindsight(t)
	f.set(func(f *fakeHindsight) { f.bankExists = false })
	e.configure(t, f, true)
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateOk, st.State, st.Error)
	f.set(func(f *fakeHindsight) {
		require.Len(t, f.puts, 1)
		assert.Contains(t, f.puts[0]["reflect_mission"], "Oryn")
	})
}

func TestStatusUnreachable(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	f := newFakeHindsight(t)
	e.configure(t, f, true)
	f.srv.Close()
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateUnreachable, st.State)
	require.NotNil(t, st.Error)
	assert.Nil(t, st.MemoryCount)
}

// R3：.env 含完整键集；重新生成后旧的 key 与中转令牌立即失效。
func TestEnvFileRotatesKeys(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	f := newFakeHindsight(t)

	first := e.configure(t, f, true)
	for _, k := range memory.EnvKeys() {
		envValue(t, first, k)
	}
	assert.Equal(t, "hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension", envValue(t, first, "HINDSIGHT_API_TENANT_EXTENSION"))
	assert.Equal(t, "oryn-1", envValue(t, first, "HINDSIGHT_API_WORKER_ID"))
	assert.Equal(t, "pgroonga", envValue(t, first, "HINDSIGHT_API_TEXT_SEARCH_EXTENSION"))
	assert.Equal(t, memory.OutputLanguage, envValue(t, first, "HINDSIGHT_API_LLM_OUTPUT_LANGUAGE"))
	assert.Equal(t, "4", envValue(t, first, "HINDSIGHT_API_LLM_MAX_CONCURRENT"))
	assert.Equal(t, "4", envValue(t, first, "HINDSIGHT_API_WORKER_MAX_SLOTS"))
	assert.Equal(t, "openai", envValue(t, first, "HINDSIGHT_API_LLM_PROVIDER"))
	assert.Equal(t, "oryn-default", envValue(t, first, "HINDSIGHT_API_LLM_MODEL"))
	assert.Equal(t, testPublicURL+"/memory-relay/v1/default", envValue(t, first, "HINDSIGHT_API_LLM_BASE_URL"))
	assert.Equal(t, "oryn-mental-model", envValue(t, first, "HINDSIGHT_API_MENTAL_MODEL_REFRESH_LLM_MODEL"))
	assert.Equal(t, testPublicURL+"/memory-relay/v1/mental_model", envValue(t, first, "HINDSIGHT_API_MENTAL_MODEL_REFRESH_LLM_BASE_URL"))
	assert.Equal(t, "oryn-embed", envValue(t, first, "HINDSIGHT_API_EMBEDDINGS_OPENAI_MODEL"))
	assert.Equal(t, testPublicURL+"/memory-relay/v1/embed", envValue(t, first, "HINDSIGHT_API_EMBEDDINGS_OPENAI_BASE_URL"))
	assert.Equal(t, "rrf", envValue(t, first, "HINDSIGHT_API_RERANKER_PROVIDER"), "没配重排接口时用 Hindsight 自带的 rrf")
	assert.Equal(t, testPublicURL+"/memory-relay/v1/rerank/v1/rerank", envValue(t, first, "HINDSIGHT_API_RERANKER_COHERE_BASE_URL"))
	assert.Equal(t, testPublicURL+"/memory-relay/v1/gemini", envValue(t, first, "GOOGLE_GEMINI_BASE_URL"), "gemini 地址总是写出，键集稳定")
	token1 := envValue(t, first, "HINDSIGHT_API_LLM_API_KEY")
	key1 := envValue(t, first, "HINDSIGHT_API_TENANT_API_KEY")
	assert.Equal(t, token1, envValue(t, first, "HINDSIGHT_API_RERANKER_COHERE_API_KEY"))
	assert.NotEqual(t, token1, key1)
	assert.True(t, e.svc.VerifyRelayToken(ctx, token1))
	assert.False(t, e.svc.VerifyRelayToken(ctx, ""))
	assert.False(t, e.svc.VerifyRelayToken(ctx, token1+"x"))

	settings, err := e.svc.Settings(ctx)
	require.NoError(t, err)
	assert.True(t, settings.KeysGenerated)
	require.NotNil(t, settings.KeysGeneratedAt)
	assert.Equal(t, e.now.Unix(), *settings.KeysGeneratedAt)

	// 库里只有密文与哈希
	row, err := e.st.Q.GetMemorySettings(ctx)
	require.NoError(t, err)
	assert.NotContains(t, string(row.HindsightKeyEnc), key1)
	require.NotNil(t, row.RelayTokenHash)
	assert.Equal(t, memory.HashRelayToken(token1), *row.RelayTokenHash)
	assert.JSONEq(t, `{"default":"openai","retain":"openai","reflect":"openai","consolidation":"openai","mental_model":"openai","rerank":"rrf"}`, row.DeployedFamilies)

	e.now = e.now.Add(time.Hour)
	second, err := e.svc.GenerateEnvFile(ctx)
	require.NoError(t, err)
	token2 := envValue(t, second.Content, "HINDSIGHT_API_LLM_API_KEY")
	key2 := envValue(t, second.Content, "HINDSIGHT_API_TENANT_API_KEY")
	assert.NotEqual(t, token1, token2)
	assert.NotEqual(t, key1, key2)
	assert.False(t, e.svc.VerifyRelayToken(ctx, token1), "旧中转令牌立即失效")
	assert.True(t, e.svc.VerifyRelayToken(ctx, token2))

	// orynd 立即改用新 key：Hindsight 还没换 key 时报 unauthorized
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryStateUnauthorized, st.State)
	h, err := e.svc.Client(ctx)
	require.NoError(t, err)
	assert.Equal(t, key2, h.Key())
}

func TestEnvFileFamiliesAndSnippet(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	svc := memory.New(memory.Config{Q: st.Q, Box: box, PublicURL: testPublicURL,
		Family: func(_ context.Context, providerKey, _ string) (apigen.MemoryFamily, error) {
			switch providerKey {
			case "claude":
				return apigen.MemoryFamilyAnthropic, nil
			case "google":
				return apigen.MemoryFamilyGemini, nil
			}
			return apigen.MemoryFamilyOpenai, nil
		}})
	require.NoError(t, st.Q.UpsertMemoryModel(ctx, db.UpsertMemoryModelParams{Purpose: "default", ProviderKey: "axon", ModelID: "gpt", UpdatedAt: 1}))
	require.NoError(t, st.Q.UpsertMemoryModel(ctx, db.UpsertMemoryModelParams{Purpose: "reflect", ProviderKey: "claude", ModelID: "opus", UpdatedAt: 1}))
	require.NoError(t, st.Q.UpsertMemoryModel(ctx, db.UpsertMemoryModelParams{Purpose: "consolidation", ProviderKey: "google", ModelID: "gemini-pro", UpdatedAt: 1}))
	dims := int64(1024)
	require.NoError(t, st.Q.UpsertMemoryEndpoint(ctx, db.UpsertMemoryEndpointParams{Kind: "embed", BaseUrl: "https://e.test", Model: "qwen", Dimensions: &dims, UpdatedAt: 1}))
	require.NoError(t, st.Q.UpsertMemoryEndpoint(ctx, db.UpsertMemoryEndpointParams{Kind: "rerank", BaseUrl: "https://r.test", Model: "bge", UpdatedAt: 1}))

	out, err := svc.GenerateEnvFile(ctx)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", envValue(t, out.Content, "HINDSIGHT_API_REFLECT_LLM_PROVIDER"))
	assert.Equal(t, "gemini", envValue(t, out.Content, "HINDSIGHT_API_CONSOLIDATION_LLM_PROVIDER"))
	assert.Equal(t, testPublicURL+"/memory-relay/v1/gemini", envValue(t, out.Content, "GOOGLE_GEMINI_BASE_URL"))
	assert.Equal(t, "openai", envValue(t, out.Content, "HINDSIGHT_API_RETAIN_LLM_PROVIDER"))
	assert.Equal(t, "1024", envValue(t, out.Content, "HINDSIGHT_API_EMBEDDINGS_OPENAI_DIMENSIONS"))
	assert.Equal(t, "cohere", envValue(t, out.Content, "HINDSIGHT_API_RERANKER_PROVIDER"), "配了重排接口走 Cohere 中转")
	assert.Equal(t, testPublicURL+"/memory-relay/v1/rerank/v1/rerank", envValue(t, out.Content, "HINDSIGHT_API_RERANKER_COHERE_BASE_URL"))
	row, err := st.Q.GetMemorySettings(ctx)
	require.NoError(t, err)
	assert.Contains(t, row.DeployedFamilies, `"rerank":"cohere"`)

	snip, err := svc.EnvSnippet(ctx, []apigen.MemoryPurpose{apigen.MemoryPurposeReflect}, "")
	require.NoError(t, err)
	assert.Equal(t, "HINDSIGHT_API_REFLECT_LLM_PROVIDER=anthropic\n"+
		"HINDSIGHT_API_REFLECT_LLM_MODEL=oryn-reflect\n"+
		"HINDSIGHT_API_REFLECT_LLM_BASE_URL="+testPublicURL+"/memory-relay/v1/reflect\n"+
		"HINDSIGHT_API_REFLECT_LLM_API_KEY=<保持原值>\n", snip)
}

func TestEnvFilePurposeForAlias(t *testing.T) {
	for _, p := range memory.Purposes {
		got, ok := memory.PurposeForAlias(memory.PurposeAlias(p))
		assert.True(t, ok, p)
		assert.Equal(t, p, got)
	}
	p, ok := memory.PurposeForAlias("oryn-mental-model")
	assert.True(t, ok)
	assert.Equal(t, apigen.MemoryPurposeMentalModel, p)
	_, ok = memory.PurposeForAlias("gpt-4o")
	assert.False(t, ok)
}

// deploy/hindsight/.env.example 的键集必须与生成器一致（步骤 4.1）。只给 compose 插值用、不进生成器的键
// （PG 密码：重新生成 .env 不能轮换它，否则已有数据卷失效）不参与比对。
func TestEnvExample(t *testing.T) {
	composeOnly := map[string]bool{"HINDSIGHT_DB_PASSWORD": true}
	raw, err := os.ReadFile("../../deploy/hindsight/.env.example")
	require.NoError(t, err)
	var keys []string
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, _, ok := strings.Cut(l, "=")
		require.True(t, ok, "不是 KEY=VALUE：%q", l)
		if k = strings.TrimSpace(k); !composeOnly[k] {
			keys = append(keys, k)
		}
	}
	assert.ElementsMatch(t, memory.EnvKeys(), keys)
}

// AgentMemory 的启用矩阵：全局关闭、项目单独关闭、未生成密钥都不启用；无项目线程关闭自动写入。
func TestMemoryAgentMemoryMatrix(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	f := newFakeHindsight(t)
	e.project(t, "prj_01HZYAAAA", "alpha")
	e.project(t, "prj_01HZYBBBB", "beta")
	pa, pb := "prj_01HZYAAAA", "prj_01HZYBBBB"

	// 没有设置行
	m, err := e.svc.AgentMemory(ctx, "thr_1", &pa)
	require.NoError(t, err)
	assert.Nil(t, m)

	// 有地址、开关打开，但没有生成密钥
	u := f.srv.URL
	_, err = e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: true, HindsightURL: &u})
	require.NoError(t, err)
	assert.False(t, e.svc.Enabled(ctx))
	m, err = e.svc.AgentMemory(ctx, "thr_1", &pa)
	require.NoError(t, err)
	assert.Nil(t, m, "未生成密钥")
	assert.Empty(t, e.restarts)

	// 生成 .env 后生效：全局生效值由关变开，标记全部线程待重启
	_, err = e.svc.GenerateEnvFile(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, e.restarts)
	assert.True(t, e.svc.Enabled(ctx))
	m, err = e.svc.AgentMemory(ctx, "thr_1", &pa)
	require.NoError(t, err)
	require.NotNil(t, m)
	require.NotNil(t, m.Project)
	assert.Equal(t, "prj_01hzyaaaa", *m.Project)
	assert.True(t, m.AutoRetain)

	// 无项目线程：启用但关闭自动写入
	m, err = e.svc.AgentMemory(ctx, "thr_2", nil)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Nil(t, m.Project)
	assert.False(t, m.AutoRetain)

	// 项目单独关闭：只影响该项目
	tog, err := e.svc.PutProject(ctx, pa, false)
	require.NoError(t, err)
	assert.False(t, tog.Enabled)
	assert.True(t, tog.Overridden)
	assert.Equal(t, []string{"*", pa}, e.restarts)
	assert.False(t, e.svc.ProjectEnabled(ctx, pa))
	assert.True(t, e.svc.ProjectEnabled(ctx, pb))
	m, err = e.svc.AgentMemory(ctx, "thr_1", &pa)
	require.NoError(t, err)
	assert.Nil(t, m)
	m, err = e.svc.AgentMemory(ctx, "thr_3", &pb)
	require.NoError(t, err)
	assert.NotNil(t, m)

	// 同值再设不重复标记
	_, err = e.svc.PutProject(ctx, pa, false)
	require.NoError(t, err)
	assert.Len(t, e.restarts, 2)

	list, err := e.svc.ListProjects(ctx)
	require.NoError(t, err)
	require.Len(t, list.Items, 2)
	assert.Equal(t, apigen.MemoryProjectToggle{ProjectID: pa, Name: "alpha", Enabled: false, Overridden: true}, list.Items[0])
	assert.Equal(t, apigen.MemoryProjectToggle{ProjectID: pb, Name: "beta", Enabled: true, Overridden: false}, list.Items[1])

	// 全局关闭：全部不启用，无项目线程也不启用
	_, err = e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: false, HindsightURL: &u})
	require.NoError(t, err)
	assert.Equal(t, []string{"*", pa, "*"}, e.restarts)
	for _, p := range []*string{&pa, &pb, nil} {
		m, err = e.svc.AgentMemory(ctx, "thr_x", p)
		require.NoError(t, err)
		assert.Nil(t, m)
	}
	assert.False(t, e.svc.ProjectEnabled(ctx, pb))

	// 不存在的项目
	_, err = e.svc.PutProject(ctx, "prj_missing", true)
	var me *memory.Error
	require.ErrorAs(t, err, &me)
	assert.Equal(t, http.StatusNotFound, me.Status)
}
