package ownermem_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/ownermem"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

// fakeHS 记录对 bank `oryn-owner` 的请求。
type fakeHS struct {
	srv *httptest.Server

	mu         sync.Mutex
	puts       int
	patches    []map[string]any
	models     []map[string]any
	retains    [][]map[string]any // 每次 retain 请求的 items
	deletes    []string           // 删除的 document id
	down       bool
	failRetain bool
	// 主题：reflect 返回的 topics、recall 结果的 mentioned_at（空串 = 无结果）、提交的刷新
	curated   []map[string]any
	reflects  int
	mentioned string
	refreshes []string
	// 事实：id -> 单条记忆（GET /memories/{id} 原样返回），列表按 factOrder；PATCH 记录在 factPatches
	facts       map[string]map[string]any
	factOrder   []string
	factPatches []map[string]any
	recallHits  []map[string]any  // 非空时 recall 直接返回它们
	contents    map[string]string // 主题 id -> 内容（默认「内容:<名称>」）
}

// retainedDocs 返回第 i 次 retain 请求涉及的 document id（去重，保持顺序）。
func (f *fakeHS) retainedDocs(i int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, it := range f.retains[i] {
		if id := it["document_id"].(string); len(out) == 0 || out[len(out)-1] != id {
			out = append(out, id)
		}
	}
	return out
}

func newFakeHS(t *testing.T) *fakeHS {
	t.Helper()
	f := &fakeHS{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, `{"detail":"down"}`, http.StatusServiceUnavailable)
		return
	}
	const root = "/v1/default/banks/" + ownermem.BankID
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPut && r.URL.Path == root:
		f.puts++
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPatch && r.URL.Path == root+"/config":
		f.patches = append(f.patches, body["updates"].(map[string]any))
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPost && r.URL.Path == root+"/mental-models":
		f.models = append(f.models, body)
		_, _ = w.Write([]byte(`{"operation_id":"op"}`))
	case r.Method == http.MethodPost && r.URL.Path == root+"/memories":
		if f.failRetain {
			http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
			return
		}
		var items []map[string]any
		for _, it := range body["items"].([]any) {
			items = append(items, it.(map[string]any))
		}
		f.retains = append(f.retains, items)
		_, _ = w.Write([]byte(`{"operation_id":"op"}`))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, root+"/documents/"):
		f.deletes = append(f.deletes, strings.TrimPrefix(r.URL.Path, root+"/documents/"))
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodGet && r.URL.Path == root+"/mental-models":
		var items []map[string]any
		for _, m := range f.models {
			c := "内容:" + m["name"].(string)
			if v, ok := f.contents[m["id"].(string)]; ok {
				c = v
			}
			items = append(items, map[string]any{"id": m["id"], "content": c})
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		_, _ = w.Write(raw)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, root+"/mental-models/") && strings.HasSuffix(r.URL.Path, "/refresh"):
		f.refreshes = append(f.refreshes, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, root+"/mental-models/"), "/refresh"))
		_, _ = w.Write([]byte(`{"operation_id":"op"}`))
	case r.Method == http.MethodPost && r.URL.Path == root+"/reflect":
		f.reflects++
		raw, _ := json.Marshal(map[string]any{"text": "x", "structured_output": map[string]any{"topics": f.curated}})
		_, _ = w.Write(raw)
	case r.Method == http.MethodPost && r.URL.Path == root+"/memories/recall":
		results := []map[string]any{}
		if f.recallHits != nil {
			results = f.recallHits
		} else if f.mentioned != "" {
			results = append(results, map[string]any{"id": "m", "text": "t", "mentioned_at": f.mentioned})
		}
		raw, _ := json.Marshal(map[string]any{"results": results})
		_, _ = w.Write(raw)
	case r.Method == http.MethodGet && r.URL.Path == root+"/memories/list":
		items := []map[string]any{}
		for _, id := range f.factOrder {
			m := f.facts[id]
			if typ := r.URL.Query().Get("type"); typ == "" || typ == m["type"] {
				items = append(items, map[string]any{"id": id, "text": m["text"], "fact_type": m["type"],
					"document_id": m["document_id"], "metadata": m["metadata"], "state": m["state"]})
			}
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		_, _ = w.Write(raw)
	case strings.HasPrefix(r.URL.Path, root+"/memories/") && f.facts[strings.TrimPrefix(r.URL.Path, root+"/memories/")] != nil:
		m := f.facts[strings.TrimPrefix(r.URL.Path, root+"/memories/")]
		if r.Method == http.MethodPatch {
			f.factPatches = append(f.factPatches, body)
			m["state"] = body["state"]
		}
		raw, _ := json.Marshal(m)
		_, _ = w.Write(raw)
	case r.Method == http.MethodGet && r.URL.Path == root+"/stats":
		_, _ = w.Write([]byte(`{"total_nodes":42,"total_documents":7}`))
	case r.Method == http.MethodGet && r.URL.Path == root+"/llm-requests/stats":
		n := 1
		if r.URL.Query().Get("period") == "7d" {
			n = 3
		}
		bk := `{"time":"t","statuses":{},"total":2,"tokens":{"input":100,"output":10,"cached":0,"total":110}}`
		_, _ = w.Write([]byte(`{"buckets":[` + strings.TrimSuffix(strings.Repeat(bk+",", n), ",") + `]}`))
	default:
		http.NotFound(w, r)
	}
}

type env struct {
	st  *store.Store
	mem *memory.Service
	svc *ownermem.Service
	hs  *fakeHS
	now time.Time
}

// newEnv 构造服务；m16 为真时 M16 记忆已启用并配置到假 Hindsight。
func newEnv(t *testing.T, m16 bool) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	e := &env{st: st, hs: newFakeHS(t), now: time.UnixMilli(1_800_000_000_000)}
	now := func() time.Time { return e.now }
	e.mem = memory.New(memory.Config{Q: st.Q, Box: box, PublicURL: "https://oryn.test/", Now: now})
	e.svc = ownermem.New(ownermem.Config{Q: st.Q, Box: box, Memory: e.mem, Now: now})
	if m16 {
		u := e.hs.srv.URL
		_, err := e.mem.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: true, HindsightURL: &u})
		require.NoError(t, err)
		_, err = e.mem.GenerateEnvFile(ctx)
		require.NoError(t, err)
	}
	return e
}

func (e *env) enable(t *testing.T, names ...string) apigen.OwnerMemorySettings {
	t.Helper()
	out, err := e.svc.PutSettings(context.Background(), apigen.OwnerMemorySettingsUpdate{Enabled: true, OwnerNames: names})
	require.NoError(t, err)
	return out
}

// TestSettingsRequiresM16:M16 记忆未启用时助手记忆不运行，开关打开也一样。
func TestSettingsRequiresM16(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, false)
	out := e.enable(t, "Ling")
	assert.True(t, out.Enabled)
	assert.False(t, out.Available)
	assert.False(t, e.svc.Enabled(ctx))
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryStateUnavailable, st.State)

	e2 := newEnv(t, true)
	assert.False(t, e2.svc.Enabled(ctx), "助手记忆开关默认关")
	st, err = e2.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryStateDisabled, st.State)
}

// TestSettingsNames:名字去空白去重，超过 16 个拒绝；首次开启定下 30 天前的回填起点。
func TestSettingsNames(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	out := e.enable(t, " Ling ", "酒狐", "Ling", "")
	assert.Equal(t, []string{"Ling", "酒狐"}, out.OwnerNames)
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	require.NotNil(t, st.BackfillSince)
	assert.Equal(t, int64(1_800_000_000_000-30*24*3600*1000), *st.BackfillSince)

	many := make([]string, 17)
	for i := range many {
		many[i] = strings.Repeat("x", i+1)
	}
	_, err = e.svc.PutSettings(ctx, apigen.OwnerMemorySettingsUpdate{Enabled: true, OwnerNames: many})
	var me *memory.Error
	require.ErrorAs(t, err, &me)
	assert.Equal(t, http.StatusBadRequest, me.Status)
}

// TestBankConfig:首次访问 bank 时创建 bank、写入带主人别名与三条约束的 mission、关闭自带重排并建种子主题；
// 配置不变时不再 PATCH，改别名后重新 PATCH，种子主题只建一次。
func TestBankConfig(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t, "Ling", "酒狐")

	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryStateOk, st.State, st.Error)
	require.Len(t, e.hs.patches, 1)
	cfg := e.hs.patches[0]
	mission, _ := cfg["retain_mission"].(string)
	assert.Contains(t, mission, "Ling、酒狐")
	assert.Contains(t, mission, "归属")
	assert.Contains(t, mission, "场景")
	assert.Contains(t, mission, "指代词")
	reflect, _ := cfg["reflect_mission"].(string)
	for _, rule := range []string{"归属", "完成状态", "场景"} {
		assert.Contains(t, reflect, rule)
	}
	assert.Equal(t, false, cfg["enable_reranking"])
	assert.Equal(t, true, cfg["enable_observations"])
	require.Len(t, e.hs.models, 1)
	assert.Equal(t, ownermem.ProfileTopicID, e.hs.models[0]["id"])
	assert.EqualValues(t, 400, e.hs.models[0]["max_tokens"])
	assert.Contains(t, e.hs.models[0]["source_query"], "完成状态")

	_, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Len(t, e.hs.patches, 1, "配置未变不重写")

	e.enable(t, "Ling", "小狐")
	_, err = e.svc.Status(ctx)
	require.NoError(t, err)
	require.Len(t, e.hs.patches, 2)
	assert.Contains(t, e.hs.patches[1]["retain_mission"], "Ling、小狐")
	assert.NotContains(t, e.hs.patches[1]["retain_mission"], "酒狐")
	assert.Len(t, e.hs.models, 1, "种子主题只建一次")
}

// TestStatus:状态带 bank 统计与 Hindsight 记录的用量；连不上时 state 为 error，本地计数照常。
func TestStatus(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	e.enable(t)
	st, err := e.svc.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, apigen.OwnerMemoryStateOk, st.State)
	assert.Equal(t, int64(42), *st.MemoryCount)
	assert.Equal(t, int64(7), *st.DocumentCount)
	assert.Equal(t, apigen.MemoryUsageCounts{Calls: 2, InputTokens: 100, OutputTokens: 10}, *st.UsageDay)
	assert.Equal(t, apigen.MemoryUsageCounts{Calls: 6, InputTokens: 300, OutputTokens: 30}, *st.UsageWeek)

	e.hs.mu.Lock()
	e.hs.down = true
	e.hs.mu.Unlock()
	st, err = e.svc.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.OwnerMemoryStateError, st.State)
	require.NotNil(t, st.Error)
	assert.Nil(t, st.MemoryCount)
}

// TestSettingsDecideKey:判断模型 key 加密存库、不回显；省略 key 时保留，空串清除；远端明文 http 拒绝。
func TestSettingsDecideKey(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, true)
	key := "AQ.secret-decide-key"
	out, err := e.svc.PutDecideEndpoint(ctx, apigen.OwnerMemoryDecideUpdate{
		BaseURL: ownermem.DefaultDecideBaseURL, Model: ownermem.DefaultDecideModel, APIKey: &key,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Decide)
	assert.True(t, out.Decide.HasKey)
	raw, _ := json.Marshal(out)
	assert.NotContains(t, string(raw), key)

	out, err = e.svc.PutDecideEndpoint(ctx, apigen.OwnerMemoryDecideUpdate{BaseURL: ownermem.DefaultDecideBaseURL, Model: "other"})
	require.NoError(t, err)
	assert.True(t, out.Decide.HasKey, "省略 key 时保留")
	assert.Equal(t, "other", out.Decide.Model)

	empty := ""
	out, err = e.svc.PutDecideEndpoint(ctx, apigen.OwnerMemoryDecideUpdate{BaseURL: ownermem.DefaultDecideBaseURL, Model: "other", APIKey: &empty})
	require.NoError(t, err)
	assert.False(t, out.Decide.HasKey)

	_, err = e.svc.PutDecideEndpoint(ctx, apigen.OwnerMemoryDecideUpdate{BaseURL: "http://example.com", Model: "m"})
	var me *memory.Error
	require.ErrorAs(t, err, &me)
	assert.Equal(t, "insecure_url", me.Code)

	out, err = e.svc.DeleteDecideEndpoint(ctx)
	require.NoError(t, err)
	assert.Nil(t, out.Decide)
}
