package memory_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
)

// gwFake 是网关测试用的假 Hindsight：校验 Bearer、记录每个转发来的请求，并按 bank `oryn` 应答。
type gwFake struct {
	srv *httptest.Server

	mu     sync.Mutex
	key    string
	calls  []gwCall
	models []map[string]any
}

type gwCall struct {
	method, path, query, auth string
	body                      map[string]any
}

func newGWFake(t *testing.T) *gwFake {
	t.Helper()
	f := &gwFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *gwFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := r.Header.Get("Authorization")
	if f.key == "" || auth != "Bearer "+f.key {
		http.Error(w, `{"detail":"Authentication failed: Invalid API key"}`, http.StatusUnauthorized)
		return
	}
	c := gwCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: auth}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.body)
	}
	f.calls = append(f.calls, c)
	w.Header().Set("Content-Type", "application/json")
	const root = "/v1/default/banks/oryn"
	enc := json.NewEncoder(w)
	switch rest := strings.TrimPrefix(r.URL.Path, root); {
	case !strings.HasPrefix(r.URL.Path, root+"/"):
		http.NotFound(w, r)
	case r.Method == http.MethodPost && rest == "/memories":
		_ = enc.Encode(map[string]any{"success": true, "bank_id": "oryn", "items_count": 1, "async": true, "operation_id": "real-op"})
	case r.Method == http.MethodPost && rest == "/memories/recall":
		// 把收到的鉴权头回显进应答，验证网关的脱敏
		_ = enc.Encode(map[string]any{"results": []map[string]any{{"text": "echo " + auth, "type": "world"}}})
	case r.Method == http.MethodPost && rest == "/reflect":
		_ = enc.Encode(map[string]any{"text": "反思结果"})
	case r.Method == http.MethodGet && rest == "/mental-models":
		items := []map[string]any{}
		if r.URL.Query().Get("offset") == "0" {
			items = f.models
		}
		_ = enc.Encode(map[string]any{"items": items, "total": len(f.models), "limit": 1000, "offset": 0})
	case r.Method == http.MethodPost && rest == "/mental-models":
		_ = enc.Encode(map[string]any{"mental_model_id": c.body["id"], "operation_id": "op-mm"})
	case strings.HasPrefix(rest, "/mental-models/"):
		parts := strings.Split(strings.TrimPrefix(rest, "/mental-models/"), "/")
		var m map[string]any
		for _, x := range f.models {
			if x["id"] == parts[0] {
				m = x
			}
		}
		if m == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = enc.Encode(map[string]any{"detail": "Mental model not found"})
			return
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			_ = enc.Encode(m)
		case len(parts) == 2 && parts[1] == "refresh" && r.Method == http.MethodPost:
			_ = enc.Encode(map[string]any{"operation_id": "op-refresh", "status": "queued"})
		case len(parts) == 2 && parts[1] == "history" && r.Method == http.MethodGet:
			_ = enc.Encode([]map[string]any{{"previous_content": "旧", "changed_at": "2026-11-16T00:00:00Z"}})
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *gwFake) take() []gwCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

// gwEnv 是配置好的网关：假 Hindsight 接受生成的 key，项目 prj_a、prj_b 已存在。
type gwEnv struct {
	*env
	f   *gwFake
	gw  *memory.Gateway
	key string
}

const (
	prjA = "prj_A"
	prjB = "prj_B"
)

func newGWEnv(t *testing.T) *gwEnv {
	t.Helper()
	e := newEnv(t)
	f := newGWFake(t)
	e.project(t, prjA, "A")
	e.project(t, prjB, "B")
	ctx := context.Background()
	u := f.srv.URL
	_, err := e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: true, HindsightURL: &u})
	require.NoError(t, err)
	out, err := e.svc.GenerateEnvFile(ctx)
	require.NoError(t, err)
	key := envValue(t, out.Content, "HINDSIGHT_API_TENANT_API_KEY")
	f.mu.Lock()
	f.key = key
	f.mu.Unlock()
	return &gwEnv{env: e, f: f, gw: memory.NewGateway(e.svc), key: key}
}

func caller(thread string, project *string) memory.Caller {
	return memory.Caller{OrbID: "orb-" + thread, ThreadID: thread, ProjectID: project}
}

// do 以 c 的身份向网关发请求（target 是 Hindsight 路径），客户端带一个伪造的 Authorization。
func (g *gwEnv) do(t *testing.T, c *memory.Caller, method, target string, body any) (int, map[string]any, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		require.NoError(t, err)
		rd = strings.NewReader(string(buf))
	}
	req := httptest.NewRequest(method, target, rd)
	req.Header.Set("Authorization", "Bearer forged-client-token")
	if c != nil {
		req = req.WithContext(memory.WithCaller(req.Context(), *c))
	}
	rec := httptest.NewRecorder()
	g.gw.ServeHTTP(rec, req)
	raw := rec.Body.String()
	assert.NotContains(t, raw, g.key, "应答里不得出现真实 key")
	var out map[string]any
	_ = json.Unmarshal([]byte(raw), &out)
	return rec.Code, out, raw
}

func tagsOf(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// R5、R6：召回与 reflect 的 bank、标签与匹配方式由网关强制；伪造的标签、bank 名与可绕过隔离的字段都被丢弃。
func TestGatewayRecallReflectForceScope(t *testing.T) {
	g := newGWEnv(t)
	a := caller("T-A", new(prjA))
	code, out, _ := g.do(t, &a, http.MethodPost, "/v1/default/banks/omp-evil/memories/recall", map[string]any{
		"query": "迁移规范", "types": []string{"world"}, "max_tokens": 1024, "budget": "mid",
		"tags": []string{"project:prj_b"}, "tags_match": "any",
		"tag_groups": []any{map[string]any{"tags": []string{"project:prj_b"}}}, "trace": true,
	})
	require.Equal(t, http.StatusOK, code)
	results := out["results"].([]any)
	assert.Equal(t, "echo Bearer oryn-redacted", results[0].(map[string]any)["text"], "应答里的 key 换成 oryn-redacted")
	calls := g.f.take()
	require.Len(t, calls, 1)
	c := calls[0]
	assert.Equal(t, "/v1/default/banks/oryn/memories/recall", c.path)
	assert.Equal(t, "Bearer "+g.key, c.auth, "客户端的 Authorization 被换成真实 key")
	assert.Equal(t, []string{"project:prj_a", memory.GlobalTag}, tagsOf(c.body["tags"]))
	assert.Equal(t, "any_strict", c.body["tags_match"])
	assert.Equal(t, "迁移规范", c.body["query"])
	assert.EqualValues(t, 1024, c.body["max_tokens"])
	assert.NotContains(t, c.body, "tag_groups")
	assert.NotContains(t, c.body, "trace")

	// 无项目线程只召回全局记忆
	n := caller("T-N", nil)
	code, _, _ = g.do(t, &n, http.MethodPost, "/v1/default/banks/oryn/memories/recall", map[string]any{"query": "q", "tags": []string{"project:prj_a"}})
	require.Equal(t, http.StatusOK, code)
	calls = g.f.take()
	require.Len(t, calls, 1)
	assert.Equal(t, []string{memory.GlobalTag}, tagsOf(calls[0].body["tags"]))
	assert.Equal(t, "any_strict", calls[0].body["tags_match"])

	// reflect 同样强制，apply_all_directives / exclude 等字段丢弃
	b := caller("T-B", new(prjB))
	code, out, _ = g.do(t, &b, http.MethodPost, "/v1/default/banks/x/reflect", map[string]any{
		"query": "总结", "context": "c", "tags": []string{"project:prj_a"}, "tags_match": "all", "apply_all_directives": true,
	})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "反思结果", out["text"])
	calls = g.f.take()
	require.Len(t, calls, 1)
	assert.Equal(t, "/v1/default/banks/oryn/reflect", calls[0].path)
	assert.Equal(t, []string{"project:prj_b", memory.GlobalTag}, tagsOf(calls[0].body["tags"]))
	assert.Equal(t, "any_strict", calls[0].body["tags_match"])
	assert.Equal(t, "c", calls[0].body["context"])
	assert.NotContains(t, calls[0].body, "apply_all_directives")
}

// R5：项目写入强制 tags 与 document_tags 为 [project:P]，登记 document 归属；别的项目不能覆盖已归属的 document。
func TestGatewayRetainProject(t *testing.T) {
	g := newGWEnv(t)
	ctx := context.Background()
	a := caller("T-A", new(prjA))
	code, out, _ := g.do(t, &a, http.MethodPost, "/v1/default/banks/omp/memories", map[string]any{
		"items": []map[string]any{{
			"content": "用 goose 写迁移", "context": "omp", "timestamp": "2026-11-16T00:00:00+08:00",
			"metadata": map[string]string{"session_id": "S-sub"}, "document_id": "omp-retain-1",
			"tags": []string{"project:prj_b", memory.GlobalTag}, "update_mode": "replace",
			"observation_scopes": "all", "entities": []any{map[string]any{"text": "x"}},
		}},
		"async": true, "document_tags": []string{"project:prj_b"}, "operation_id": "11111111-1111-1111-1111-111111111111",
	})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "real-op", out["operation_id"])
	calls := g.f.take()
	require.Len(t, calls, 1)
	c := calls[0]
	assert.Equal(t, "/v1/default/banks/oryn/memories", c.path)
	assert.Equal(t, []string{"project:prj_a"}, tagsOf(c.body["document_tags"]))
	assert.Equal(t, true, c.body["async"])
	assert.NotContains(t, c.body, "operation_id")
	items := c.body["items"].([]any)
	require.Len(t, items, 1)
	it := items[0].(map[string]any)
	assert.Equal(t, []string{"project:prj_a"}, tagsOf(it["tags"]))
	assert.Equal(t, "omp-retain-1", it["document_id"])
	assert.Equal(t, "用 goose 写迁移", it["content"])
	assert.Equal(t, "replace", it["update_mode"])
	assert.Equal(t, map[string]any{"session_id": "S-sub"}, it["metadata"])
	assert.NotContains(t, it, "observation_scopes")
	assert.NotContains(t, it, "entities")

	doc, err := g.st.Q.GetMemoryDocument(ctx, "omp-retain-1")
	require.NoError(t, err)
	require.NotNil(t, doc.ProjectID)
	require.NotNil(t, doc.ThreadID)
	assert.Equal(t, prjA, *doc.ProjectID)
	assert.Equal(t, "T-A", *doc.ThreadID, "document 按 URL 里的线程归属，不按 metadata.session_id")

	// 项目 B 用同一个 document_id 写入：拒绝，不转发，归属不变
	b := caller("T-B", new(prjB))
	code, _, _ = g.do(t, &b, http.MethodPost, "/v1/default/banks/oryn/memories", map[string]any{
		"items": []map[string]any{{"content": "覆盖", "document_id": "omp-retain-1", "tags": []string{"project:prj_b"}, "update_mode": "replace"}},
	})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Empty(t, g.f.take())
	doc, err = g.st.Q.GetMemoryDocument(ctx, "omp-retain-1")
	require.NoError(t, err)
	assert.Equal(t, prjA, *doc.ProjectID)

	// 缺 document_id 时由网关补一个并登记
	code, _, _ = g.do(t, &b, http.MethodPost, "/v1/default/banks/oryn/memories", map[string]any{
		"items": []map[string]any{{"content": "B 的约定", "tags": []string{"project:prj_b"}}},
	})
	require.Equal(t, http.StatusOK, code)
	calls = g.f.take()
	require.Len(t, calls, 1)
	gen := calls[0].body["items"].([]any)[0].(map[string]any)["document_id"].(string)
	require.NotEmpty(t, gen)
	docs, err := g.st.Q.ListMemoryDocumentsByThread(ctx, new("T-B"))
	require.NoError(t, err)
	require.Len(t, docs, 1)
	assert.Equal(t, gen, docs[0].DocumentID)
}

// R8：不带 tags 键的写入（Core 的 scope:"global"）与无项目线程的写入进候选队列，不写入 Hindsight，
// 应答是附录 A 的异步写入成功格式；Core 重发同一事件不会重复入队。
func TestGatewayRetainCandidates(t *testing.T) {
	g := newGWEnv(t)
	ctx := context.Background()
	a := caller("T-A", new(prjA))
	body := map[string]any{
		"items": []map[string]any{{"content": "提交信息一律用简体中文", "context": "偏好", "document_id": "omp-retain-g1", "update_mode": "replace"}},
		"async": true,
	}
	for range 2 {
		code, out, raw := g.do(t, &a, http.MethodPost, "/v1/default/banks/omp/memories", body)
		require.Equal(t, http.StatusOK, code, raw)
		assert.Equal(t, true, out["success"])
		assert.Equal(t, "oryn", out["bank_id"])
		assert.EqualValues(t, 1, out["items_count"])
		assert.Equal(t, true, out["async"])
		assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, out["operation_id"])
		for _, k := range []string{"operation_ids", "usage"} {
			v, ok := out[k]
			assert.True(t, ok, k)
			assert.Nil(t, v, k)
		}
	}
	assert.Empty(t, g.f.take(), "全局写入不进 Hindsight")
	cands, err := g.st.Q.ListMemoryCandidates(ctx)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "提交信息一律用简体中文", cands[0].Content)
	assert.Equal(t, "偏好", *cands[0].Context)
	assert.Equal(t, "T-A", *cands[0].SourceThreadID)
	assert.Equal(t, prjA, *cands[0].SourceProjectID)
	_, err = g.st.Q.GetMemoryDocument(ctx, "omp-retain-g1")
	assert.Error(t, err, "候选不登记 document")

	// 无项目线程：即使带了项目标签，也进候选
	n := caller("T-N", nil)
	code, out, _ := g.do(t, &n, http.MethodPost, "/v1/default/banks/oryn/memories", map[string]any{
		"items": []map[string]any{
			{"content": "一", "document_id": "d-n1", "tags": []string{"project:prj_a"}},
			{"content": "二", "document_id": "d-n2"},
		},
	})
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 2, out["items_count"])
	assert.Empty(t, g.f.take())
	cands, err = g.st.Q.ListMemoryCandidates(ctx)
	require.NoError(t, err)
	require.Len(t, cands, 3)
	for _, c := range cands {
		if *c.SourceThreadID == "T-N" {
			assert.Nil(t, c.SourceProjectID)
		}
	}
}

// R9：mental model 只列出 / 读取本项目与全局的模型，全局模型的 tags 改写为 []；只能创建本项目的两个 seed，
// 只能刷新本项目的模型，DELETE 一律 403，detail=full 降级为 content。
func TestGatewayMentalModels(t *testing.T) {
	g := newGWEnv(t)
	ownID, decID := memory.ProjectModelIDs(prjA)[0], memory.ProjectModelIDs(prjA)[1]
	// 与 Core 的 seed id 清洗一致：项目 ID 转小写，`[^A-Za-z0-9._-]` 换成 `-`
	assert.Equal(t, "project-conventions-prj_a", ownID)
	assert.Equal(t, "project-decisions-prj_a", decID)
	assert.Equal(t, []string{"project-conventions-a-b", "project-decisions-a-b"}, memory.ProjectModelIDs("A:B"))
	// 真实形态：prj_<26 位 ULID>，下划线保留，ULID 转小写
	assert.Equal(t, []string{"project-conventions-prj_01jd8x3k7q9v2m4n6p8r0s1t2w", "project-decisions-prj_01jd8x3k7q9v2m4n6p8r0s1t2w"},
		memory.ProjectModelIDs("prj_01JD8X3K7Q9V2M4N6P8R0S1T2W"))
	assert.Equal(t, "project-conventions-x", memory.ProjectModelIDs(" -x!- ")[0], "trim 后去首尾 -")
	assert.Equal(t, "project-conventions-project", memory.ProjectModelIDs("!!")[0], "清洗后为空时取 project")
	g.f.models = []map[string]any{
		{"id": ownID, "name": "约定", "tags": []string{"project:prj_a"}, "content": "A 的约定", "reflect_response": map[string]any{"based_on": []any{}}},
		{"id": "user-preferences", "name": "偏好", "tags": []string{memory.GlobalTag}, "content": "简体中文", "reflect_response": map[string]any{"based_on": []any{}}},
		{"id": "project-conventions-prj-b", "name": "B", "tags": []string{"project:prj_b"}, "content": "B 的约定"},
		{"id": "mixed", "name": "混合", "tags": []string{"project:prj_a", "project:prj_b"}, "content": "x"},
		{"id": "untagged", "name": "无标签", "tags": []string{}, "content": "y"},
	}
	a := caller("T-A", new(prjA))
	const root = "/v1/default/banks/omp"

	code, out, _ := g.do(t, &a, http.MethodGet, root+"/mental-models?detail=full&limit=1000&offset=0", nil)
	require.Equal(t, http.StatusOK, code)
	items := out["items"].([]any)
	require.Len(t, items, 2)
	assert.EqualValues(t, 2, out["total"])
	ids := []string{}
	for _, x := range items {
		m := x.(map[string]any)
		ids = append(ids, m["id"].(string))
		assert.NotContains(t, m, "reflect_response")
		if m["id"] == "user-preferences" {
			assert.Equal(t, []any{}, m["tags"], "全局模型的 tags 改写为 []")
		} else {
			assert.Equal(t, []any{"project:prj_a"}, m["tags"])
		}
	}
	assert.ElementsMatch(t, []string{ownID, "user-preferences"}, ids)
	calls := g.f.take()
	require.Len(t, calls, 1)
	assert.Equal(t, "/v1/default/banks/oryn/mental-models", calls[0].path)
	assert.Contains(t, calls[0].query, "detail=content")
	assert.NotContains(t, calls[0].query, "full")

	// 无项目线程只看到全局模型
	n := caller("T-N", nil)
	_, out, _ = g.do(t, &n, http.MethodGet, root+"/mental-models?detail=metadata", nil)
	require.Len(t, out["items"].([]any), 1)
	g.f.take()

	// 单个读取：自己的与全局的可读，其他项目的 404
	code, out, _ = g.do(t, &a, http.MethodGet, root+"/mental-models/user-preferences?detail=content", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []any{}, out["tags"])
	code, _, _ = g.do(t, &a, http.MethodGet, root+"/mental-models/project-conventions-prj-b", nil)
	assert.Equal(t, http.StatusNotFound, code)
	code, _, _ = g.do(t, &a, http.MethodGet, root+"/mental-models/project-conventions-prj-b/history", nil)
	assert.Equal(t, http.StatusNotFound, code)
	code, _, raw := g.do(t, &a, http.MethodGet, root+"/mental-models/"+ownID+"/history", nil)
	assert.Equal(t, http.StatusOK, code, raw)
	calls = g.f.take()
	assert.False(t, slices.ContainsFunc(calls, func(c gwCall) bool {
		return c.path == "/v1/default/banks/oryn/mental-models/project-conventions-prj-b/history"
	}),
		"其他项目模型的 history 不转发")

	// 创建：只允许本项目的两个 seed，且 tags 恰为 [project:P]
	seed := func(id string, tags []string) map[string]any {
		return map[string]any{"id": id, "name": "n", "source_query": "q", "tags": tags, "max_tokens": 800,
			"trigger": map[string]any{"mode": "delta", "refresh_after_consolidation": true, "tags_match": "any", "tag_groups": []any{}}}
	}
	code, _, _ = g.do(t, &a, http.MethodPost, root+"/mental-models", seed(decID, []string{"project:prj_a"}))
	require.Equal(t, http.StatusOK, code)
	calls = g.f.take()
	require.Len(t, calls, 1)
	assert.Equal(t, "/v1/default/banks/oryn/mental-models", calls[0].path)
	assert.Equal(t, decID, calls[0].body["id"])
	assert.Equal(t, []string{"project:prj_a"}, tagsOf(calls[0].body["tags"]))
	assert.Equal(t, map[string]any{"mode": "delta", "refresh_after_consolidation": true, "tags_match": "all_strict"}, calls[0].body["trigger"])
	for _, bad := range []map[string]any{
		seed("user-preferences", nil),
		seed("user-preferences", []string{memory.GlobalTag}),
		seed("project-conventions-prj-b", []string{"project:prj_b"}),
		seed(ownID, []string{"project:prj_a", memory.GlobalTag}),
		seed(ownID, []string{"project:prj_b"}),
		seed("custom", []string{"project:prj_a"}),
	} {
		code, _, _ = g.do(t, &a, http.MethodPost, root+"/mental-models", bad)
		assert.Equal(t, http.StatusForbidden, code, bad)
	}
	code, _, _ = g.do(t, &n, http.MethodPost, root+"/mental-models", seed(ownID, []string{"project:prj_a"}))
	assert.Equal(t, http.StatusForbidden, code, "无项目线程不能创建")

	// 刷新：只允许本项目的模型
	code, _, _ = g.do(t, &a, http.MethodPost, root+"/mental-models/user-preferences/refresh", nil)
	assert.Equal(t, http.StatusForbidden, code)
	code, _, _ = g.do(t, &a, http.MethodPost, root+"/mental-models/project-conventions-prj-b/refresh", nil)
	assert.Equal(t, http.StatusNotFound, code)
	assert.False(t, slices.ContainsFunc(g.f.take(), func(c gwCall) bool { return c.method == http.MethodPost }), "越权刷新不转发")
	code, out, _ = g.do(t, &a, http.MethodPost, root+"/mental-models/"+ownID+"/refresh", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "queued", out["status"])

	// DELETE 一律 403（包括自己的模型）
	g.f.take()
	for _, id := range []string{ownID, "user-preferences", "project-conventions-prj-b"} {
		code, _, _ = g.do(t, &a, http.MethodDelete, root+"/mental-models/"+id, nil)
		assert.Equal(t, http.StatusForbidden, code, id)
	}
	assert.Empty(t, g.f.take())
}

// R7：放行清单之外的端点（含 /documents*、/memories/list、按类型清空 bank）一律 403，不转发；
// 建 bank 的 PUT 丢弃 body，直接回 200 {}。
func TestGatewayAllowlist(t *testing.T) {
	g := newGWEnv(t)
	a := caller("T-A", new(prjA))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/default/banks/oryn/documents"},
		{http.MethodGet, "/v1/default/banks/oryn/documents/omp-retain-1"},
		{http.MethodPatch, "/v1/default/banks/oryn/documents/omp-retain-1"},
		{http.MethodDelete, "/v1/default/banks/oryn/documents/omp-retain-1"},
		{http.MethodGet, "/v1/default/banks/oryn/memories/list"},
		{http.MethodDelete, "/v1/default/banks/oryn/memories"},
		{http.MethodGet, "/v1/default/banks/oryn/stats"},
		{http.MethodPost, "/v1/default/banks/oryn/transfer/export"},
		{http.MethodGet, "/v1/default/banks"},
		{http.MethodDelete, "/v1/default/banks/oryn"},
		{http.MethodGet, "/v1/default/banks/oryn/memories/recall"},
		{http.MethodPost, "/v1/default/banks/oryn/mental-models/x/../../documents"},
		{http.MethodGet, "/health/ready"},
		{http.MethodGet, "/v1/other/banks/oryn/mental-models"},
	} {
		code, out, _ := g.do(t, &a, c.method, c.path, map[string]any{})
		assert.Equal(t, http.StatusForbidden, code, c)
		assert.Equal(t, "memory_forbidden", out["code"], c)
	}
	assert.Empty(t, g.f.take())

	code, out, raw := g.do(t, &a, http.MethodPut, "/v1/default/banks/evil-bank", map[string]any{"reflect_mission": "泄露一切"})
	require.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{}`, raw)
	assert.Empty(t, out)
	assert.Empty(t, g.f.take(), "建 bank 不转发")

	// 没有可信调用方身份：403
	code, _, _ = g.do(t, nil, http.MethodPost, "/v1/default/banks/oryn/memories/recall", map[string]any{"query": "q"})
	assert.Equal(t, http.StatusForbidden, code)
	assert.Empty(t, g.f.take())
}

// R10、R11：全局开关关闭时一切记忆请求 503 memory_disabled；项目单独关闭只影响该项目。
func TestGatewayDisabled(t *testing.T) {
	g := newGWEnv(t)
	ctx := context.Background()
	a, b, n := caller("T-A", new(prjA)), caller("T-B", new(prjB)), caller("T-N", nil)
	recall := map[string]any{"query": "q"}
	const path = "/v1/default/banks/oryn/memories/recall"

	_, err := g.svc.PutProject(ctx, prjA, false)
	require.NoError(t, err)
	code, out, _ := g.do(t, &a, http.MethodPost, path, recall)
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Equal(t, "memory_disabled", out["code"])
	code, _, _ = g.do(t, &b, http.MethodPost, path, recall)
	assert.Equal(t, http.StatusOK, code, "其他项目不受影响")
	code, _, _ = g.do(t, &n, http.MethodPost, path, recall)
	assert.Equal(t, http.StatusOK, code)
	require.Len(t, g.f.take(), 2)

	u := g.f.srv.URL
	_, err = g.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: false, HindsightURL: &u})
	require.NoError(t, err)
	for _, c := range []memory.Caller{a, b, n} {
		for _, m := range []string{http.MethodPost, http.MethodPut} {
			code, out, _ := g.do(t, &c, m, map[string]string{http.MethodPost: path, http.MethodPut: "/v1/default/banks/oryn"}[m], recall)
			assert.Equal(t, http.StatusServiceUnavailable, code, c.ThreadID)
			assert.Equal(t, "memory_disabled", out["code"], c.ThreadID)
		}
	}
	assert.Empty(t, g.f.take())
}
