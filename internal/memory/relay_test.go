package memory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/aiconsent"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/memory/relayroute"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 假上游里出现的真实密钥（只在 orynd 的密钥库和 memory_endpoints 里）。
const (
	keyOpenAI    = "sk-oai-REAL-0123456789"
	keyAnthropic = "sk-ant-REAL-abcdefghij"
	keyGemini    = "AIza-REAL-gemini-key"
	keyEmbed     = "emb-REAL-key-42"
	keyRerank    = "rr-REAL-key-43"
)

// upstreamReq 是假上游收到的一次请求。
type upstreamReq struct {
	Path   string
	Header http.Header
	Body   map[string]any
}

// fakeUpstream 按路径扮演 openai / anthropic / gemini / 嵌入 / 重排上游，并在响应里回显收到的凭据（检验脱敏）。
type fakeUpstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []upstreamReq
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUpstream) last(t *testing.T) upstreamReq {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.reqs)
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeUpstream) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.reqs = append(f.reqs, upstreamReq{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Echo-Auth", r.Header.Get("Authorization")+r.Header.Get("X-Api-Key")+r.Header.Get("X-Goog-Api-Key"))
	echo := r.Header.Get("Authorization") + "|" + r.Header.Get("X-Api-Key") + "|" + r.Header.Get("X-Goog-Api-Key")
	var out map[string]any
	switch r.URL.Path {
	case "/v1/chat/completions":
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":999}}\n\ndata: [DONE]\n\n"))
			return
		}
		out = map[string]any{"model": body["model"], "echo": echo, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7}}
	case "/v1/responses":
		out = map[string]any{"model": body["model"], "usage": map[string]any{"input_tokens": 5, "output_tokens": 3}}
	case "/v1/messages":
		out = map[string]any{"model": body["model"], "echo": echo,
			"usage": map[string]any{"input_tokens": 20, "cache_read_input_tokens": 2, "output_tokens": 9}}
	case "/v1beta/models/gemini-real:generateContent":
		out = map[string]any{"modelVersion": "gemini-real", "echo": echo,
			"usageMetadata": map[string]any{"promptTokenCount": 13, "candidatesTokenCount": 4}}
	case "/emb/embeddings":
		// 仿 Gemini 的 OpenAI 兼容接口：第 0 条省略 index（proto3 零值），其余带上
		data := []any{}
		inputs, _ := body["input"].([]any)
		for i := range inputs {
			item := map[string]any{"object": "embedding", "embedding": []float64{float64(i)}}
			if i > 0 {
				item["index"] = i
			}
			data = append(data, item)
		}
		out = map[string]any{"model": body["model"], "echo": echo, "data": data, "usage": map[string]any{"prompt_tokens": 6, "total_tokens": 6}}
	case "/rr/v1/rerank":
		out = map[string]any{"echo": echo, "results": []any{map[string]any{"index": 0, "relevance_score": 0.9}}}
	default:
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

type relayEnv struct {
	st    *store.Store
	box   *secrets.Box
	svc   *memory.Service
	h     http.Handler
	up    *fakeUpstream
	token string
}

// newRelayEnv 建一个带真实路由解析（models.yml → egress）的记忆服务，并生成 `.env` 取得中转令牌。
// 选定模型：default → oai/gpt-real-1（openai），reflect → claude/claude-real（anthropic），
// consolidation → gem/gemini-real（gemini）；嵌入与重排指向假上游的 /emb、/rr。
// 上游都在本机，不经 AI 数据同意判定（判定见 TestRelayEndpointRequiresConsent）。
func newRelayEnv(t *testing.T) *relayEnv {
	return newRelayEnvWith(t, nil, nil)
}

// newRelayEnvWith 同 newRelayEnv，consent 是 AI 数据同意，client 非 nil 时代替访问嵌入 / 重排接口的客户端。
func newRelayEnvWith(t *testing.T, consent aiconsent.Checker, client *http.Client) *relayEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	vault := secrets.NewVault(st, box)
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)
	up := newFakeUpstream(t)
	models := "providers:\n" +
		"  oai:\n    baseUrl: " + up.srv.URL + "/v1\n    api: openai-completions\n    apiKey: " + keyOpenAI + "\n" +
		"  claude:\n    baseUrl: " + up.srv.URL + "\n    api: anthropic-messages\n    apiKey: " + keyAnthropic + "\n" +
		"  gem:\n    baseUrl: " + up.srv.URL + "/v1beta\n    api: google-generative-ai\n    apiKey: " + keyGemini + "\n"
	_, err = home.Put(ctx, agentcfg.ModelsFile, []byte(models), 0, vault)
	require.NoError(t, err)

	routes := relayroute.New(home, vault, consent)
	now := time.Unix(1_800_000_000, 0)
	svc := memory.New(memory.Config{
		Q: st.Q, Box: box, PublicURL: testPublicURL, Now: func() time.Time { return now },
		Family: routes.Family, Upstream: routes.Upstream, AIConsent: consent, HTTP: client,
	})
	e := &relayEnv{st: st, box: box, svc: svc, h: svc.RelayHandler(), up: up}
	e.selectModel(t, "default", "oai", "gpt-real-1")
	e.selectModel(t, "reflect", "claude", "claude-real")
	e.selectModel(t, "consolidation", "gem", "gemini-real")
	e.endpoint(t, apigen.MemoryEndpointEmbed, up.srv.URL+"/emb", "qwen-embed", keyEmbed)
	e.endpoint(t, apigen.MemoryEndpointRerank, up.srv.URL+"/rr", "bge-rerank", keyRerank)

	out, err := svc.GenerateEnvFile(ctx)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", envValue(t, out.Content, "HINDSIGHT_API_REFLECT_LLM_PROVIDER"))
	assert.Equal(t, "gemini", envValue(t, out.Content, "HINDSIGHT_API_CONSOLIDATION_LLM_PROVIDER"))
	e.token = envValue(t, out.Content, "HINDSIGHT_API_LLM_API_KEY")
	return e
}

func (e *relayEnv) selectModel(t *testing.T, purpose, provider, model string) {
	t.Helper()
	require.NoError(t, e.st.Q.UpsertMemoryModel(context.Background(), db.UpsertMemoryModelParams{
		Purpose: purpose, ProviderKey: provider, ModelID: model, UpdatedAt: 1,
	}))
}

func (e *relayEnv) endpoint(t *testing.T, kind apigen.MemoryEndpointKind, base, model, key string) {
	t.Helper()
	enc, err := e.box.Seal("memory/endpoint/"+string(kind)+"/api-key", []byte(key))
	require.NoError(t, err)
	require.NoError(t, e.st.Q.UpsertMemoryEndpoint(context.Background(), db.UpsertMemoryEndpointParams{
		Kind: string(kind), BaseUrl: base, Model: model, ApiKeyEnc: enc, UpdatedAt: 1,
	}))
}

// post 以 Hindsight 的身份发一条中转请求；authHeader 为空时用 `Authorization: Bearer`。
func (e *relayEnv) post(t *testing.T, path, authHeader, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, memory.RelayPathPrefix+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	switch authHeader {
	case "":
		r.Header.Set("Authorization", "Bearer "+token)
	default:
		r.Header.Set(authHeader, token)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return body.Code
}

// assertNoSecrets 检查中转令牌与任何真实密钥都不出现在响应里。
func assertNoSecrets(t *testing.T, w *httptest.ResponseRecorder, token string) {
	t.Helper()
	dump := w.Body.String()
	for k, vs := range w.Header() {
		dump += k + strings.Join(vs, ",")
	}
	for _, s := range []string{keyOpenAI, keyAnthropic, keyGemini, keyEmbed, keyRerank, token} {
		assert.NotContains(t, dump, s)
	}
}

func TestRelayUnauthorized(t *testing.T) {
	e := newRelayEnv(t)
	body := map[string]any{"model": "oryn-default", "messages": []any{}}
	for _, tc := range []struct{ name, header, token string }{
		{"缺令牌", "", ""},
		{"错令牌", "", "omr_wrong"},
		{"错令牌 x-api-key", "X-Api-Key", "omr_wrong"},
		{"真实密钥不能当中转令牌", "", keyOpenAI},
	} {
		w := e.post(t, "default/chat/completions", tc.header, tc.token, body)
		assert.Equal(t, http.StatusUnauthorized, w.Code, tc.name)
		assert.Equal(t, "unauthorized", errCode(t, w), tc.name)
	}
	assert.Zero(t, e.up.count(), "未鉴权的请求不能到达上游")

	// 不在放行范围的路径：鉴权通过后仍是 404，不转发
	for _, p := range []string{"default/models", "nope/chat/completions", "embed/v1/embeddings", "gemini/v1beta/models/oryn-unknown:generateContent"} {
		w := e.post(t, p, "", e.token, body)
		assert.Equal(t, http.StatusNotFound, w.Code, p)
	}
	assert.Zero(t, e.up.count())
}

func TestRelayOpenAIAliasReplacedAndSecretRedacted(t *testing.T) {
	e := newRelayEnv(t)
	w := e.post(t, "default/chat/completions", "", e.token, map[string]any{"model": "oryn-default", "messages": []any{}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	got := e.up.last(t)
	assert.Equal(t, "/v1/chat/completions", got.Path)
	assert.Equal(t, "gpt-real-1", got.Body["model"], "别名换成真实模型")
	assert.Equal(t, "Bearer "+keyOpenAI, got.Header.Get("Authorization"), "注入 provider 的真实密钥")
	for _, vs := range got.Header {
		for _, v := range vs {
			assert.NotContains(t, v, e.token, "中转令牌不能送到上游")
		}
	}
	// R15：上游在响应头和响应体里回显的真实密钥被换掉
	assertNoSecrets(t, w, e.token)
	assert.Contains(t, w.Body.String(), "oryn-ph-")
	assert.Contains(t, w.Header().Get("X-Echo-Auth"), "oryn-ph-")
}

func TestRelayModelSwitchAndFamilyMismatch(t *testing.T) {
	e := newRelayEnv(t)
	ctx := context.Background()
	req := map[string]any{"model": "oryn-default", "messages": []any{}}

	// R16：同协议换模型，下一条请求就用新模型
	e.selectModel(t, "default", "oai", "gpt-real-2")
	w := e.post(t, "default/chat/completions", "", e.token, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "gpt-real-2", e.up.last(t).Body["model"])

	// 没单独选模型的用途跟随 default
	w = e.post(t, "retain/chat/completions", "", e.token, map[string]any{"model": "oryn-retain"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "gpt-real-2", e.up.last(t).Body["model"])

	// retain 改选 anthropic 模型，而 .env 里部署的仍是 openai：两种协议的请求都 409
	e.selectModel(t, "retain", "claude", "claude-real")
	n := e.up.count()
	w = e.post(t, "retain/chat/completions", "", e.token, map[string]any{"model": "oryn-retain"})
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "family_mismatch", errCode(t, w))
	w = e.post(t, "retain/v1/messages", "X-Api-Key", e.token, map[string]any{"model": "oryn-retain"})
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "family_mismatch", errCode(t, w))
	assert.Equal(t, n, e.up.count(), "协议不一致时不转发")

	// 主人按新片段重启 Hindsight 并点「已重启」后（deployed_families 更新），anthropic 请求放行
	fams, err := json.Marshal(map[string]string{"default": "openai", "retain": "anthropic", "reflect": "anthropic", "consolidation": "gemini", "mental_model": "openai"})
	require.NoError(t, err)
	_, err = e.st.Q.SetMemoryDeployedFamilies(ctx, db.SetMemoryDeployedFamiliesParams{DeployedFamilies: string(fams), UpdatedAt: 2})
	require.NoError(t, err)
	w = e.post(t, "retain/v1/messages", "X-Api-Key", e.token, map[string]any{"model": "oryn-retain"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := e.up.last(t)
	assert.Equal(t, "/v1/messages", got.Path)
	assert.Equal(t, "claude-real", got.Body["model"])
	assert.Equal(t, keyAnthropic, got.Header.Get("X-Api-Key"))
	assertNoSecrets(t, w, e.token)
}

func TestRelayGeminiPurposeFromAlias(t *testing.T) {
	e := newRelayEnv(t)
	body := map[string]any{"contents": []any{}}
	w := e.post(t, "gemini/v1beta/models/oryn-consolidation:generateContent", "X-Goog-Api-Key", e.token, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := e.up.last(t)
	assert.Equal(t, "/v1beta/models/gemini-real:generateContent", got.Path, "别名在 URL 路径里换成真实模型，baseUrl 的版本段不重复")
	assert.Equal(t, keyGemini, got.Header.Get("X-Goog-Api-Key"))
	assertNoSecrets(t, w, e.token)

	// gemini 别名对应的用途部署的不是 gemini：409
	w = e.post(t, "gemini/v1beta/models/oryn-reflect:generateContent", "X-Goog-Api-Key", e.token, body)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "family_mismatch", errCode(t, w))
}

func TestRelayEndpointsForwardWithTheirKeys(t *testing.T) {
	e := newRelayEnv(t)
	w := e.post(t, "embed/embeddings", "", e.token, map[string]any{"model": "oryn-embed", "input": []string{"你好"}, "dimensions": 1024})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got := e.up.last(t)
	assert.Equal(t, "/emb/embeddings", got.Path)
	assert.Equal(t, "qwen-embed", got.Body["model"])
	assert.Equal(t, float64(1024), got.Body["dimensions"])
	assert.Equal(t, "Bearer "+keyEmbed, got.Header.Get("Authorization"))
	assertNoSecrets(t, w, e.token)
	assert.Contains(t, w.Body.String(), "oryn-redacted")

	w = e.post(t, "rerank/v1/rerank", "", e.token, map[string]any{"model": "oryn-rerank", "query": "q", "documents": []string{"a"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	got = e.up.last(t)
	assert.Equal(t, "/rr/v1/rerank", got.Path)
	assert.Equal(t, "bge-rerank", got.Body["model"])
	assert.Equal(t, "Bearer "+keyRerank, got.Header.Get("Authorization"))
	assertNoSecrets(t, w, e.token)
}

// TestRelayEmbedFillsMissingIndex：上游（Gemini）第 0 条省略 index 时，中转按位置补齐，Hindsight 按 index 排序不再出错；
// 嵌入向量与其余字段原样保留。
func TestRelayEmbedFillsMissingIndex(t *testing.T) {
	e := newRelayEnv(t)
	w := e.post(t, "embed/embeddings", "", e.token, map[string]any{"model": "oryn-embed", "input": []string{"甲", "乙", "丙"}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Data []struct {
			Index     *int      `json:"index"`
			Object    string    `json:"object"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage map[string]any `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	require.Len(t, resp.Data, 3)
	for i, d := range resp.Data {
		require.NotNil(t, d.Index, "第 %d 条缺 index", i)
		assert.Equal(t, i, *d.Index)
		assert.Equal(t, "embedding", d.Object)
		assert.Equal(t, []float64{float64(i)}, d.Embedding)
	}
	assert.Equal(t, float64(6), resp.Usage["prompt_tokens"])
}

func TestRelayUsagePerProtocol(t *testing.T) {
	e := newRelayEnv(t)
	ctx := context.Background()
	steps := []struct{ path, header string }{
		{"default/chat/completions", ""},
		{"default/responses", ""},
		{"reflect/v1/messages", "X-Api-Key"},
		{"gemini/v1beta/models/oryn-consolidation:generateContent", "X-Goog-Api-Key"},
		{"embed/embeddings", ""},
		{"rerank/v1/rerank", ""},
	}
	for _, s := range steps {
		w := e.post(t, s.path, s.header, e.token, map[string]any{"model": "x"})
		require.Equal(t, http.StatusOK, w.Code, s.path+": "+w.Body.String())
	}
	// 流式请求原样透传：计调用次数，不解析用量
	w := e.post(t, "mental_model/chat/completions", "", e.token, map[string]any{"model": "oryn-mental-model", "stream": true})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "[DONE]")
	// 失败的调用（409）不计
	e.selectModel(t, "retain", "claude", "claude-real")
	require.Equal(t, http.StatusConflict, e.post(t, "retain/chat/completions", "", e.token, map[string]any{"model": "x"}).Code)

	rows, err := e.st.Q.SumMemoryUsageSince(ctx, 0)
	require.NoError(t, err)
	got := map[string][3]int64{}
	for _, r := range rows {
		got[r.Purpose] = [3]int64{r.Calls, r.InputTokens, r.OutputTokens}
	}
	assert.Equal(t, map[string][3]int64{
		"default":       {2, 11 + 5, 7 + 3},
		"reflect":       {1, 20 + 2, 9},
		"consolidation": {1, 13, 4},
		"embed":         {1, 6, 0},
		"rerank":        {1, 0, 0},
		"mental_model":  {1, 0, 0},
	}, got)
}

// TestRelayEndpointRequiresConsent：第三方嵌入 / 重排接口须主人已同意；没有同意时 403 ai_consent_required、上游 0 个请求，
// 撤回后再次拒绝。本机的 gemini-rerank 适配器（模型 gemini-decision-*）按 Google 判定。
func TestRelayEndpointRequiresConsent(t *testing.T) {
	var mu sync.Mutex
	granted := map[string]bool{}
	grant := func(ids ...string) {
		mu.Lock()
		defer mu.Unlock()
		granted = map[string]bool{}
		for _, id := range ids {
			granted[id] = true
		}
	}
	consent := aiconsent.CheckerFunc(func(_ context.Context, id string) error {
		mu.Lock()
		defer mu.Unlock()
		if !granted[id] {
			return &aiconsent.DeniedError{Recipient: id}
		}
		return nil
	})
	var upAddr string
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "embed.example.com:80" || addr == "rerank.example.com:80" {
			addr = upAddr
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	e := newRelayEnvWith(t, consent, client)
	upAddr = e.up.srv.Listener.Addr().String()
	e.endpoint(t, apigen.MemoryEndpointEmbed, "http://embed.example.com/emb", "qwen-embed", keyEmbed)
	e.endpoint(t, apigen.MemoryEndpointRerank, "http://rerank.example.com/rr", "bge-rerank", keyRerank)
	embed := func() *httptest.ResponseRecorder {
		return e.post(t, "embed/embeddings", "", e.token, map[string]any{"model": "oryn-embed", "input": []string{"你好"}})
	}
	rerank := func() *httptest.ResponseRecorder {
		return e.post(t, "rerank/v1/rerank", "", e.token, map[string]any{"model": "oryn-rerank", "query": "q", "documents": []string{"a"}})
	}

	w := embed()
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Equal(t, aiconsent.Code, errCode(t, w))
	assert.Contains(t, w.Body.String(), "memory-embed:embed.example.com")
	w = rerank()
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Zero(t, e.up.count(), "没有同意时上游一个请求也收不到")

	grant("memory-embed:embed.example.com")
	w = embed()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 1, e.up.count())
	assert.Equal(t, http.StatusForbidden, rerank().Code, "只同意了嵌入")
	assert.Equal(t, 1, e.up.count())

	grant()
	assert.Equal(t, http.StatusForbidden, embed().Code, "撤回之后立即拒绝")
	assert.Equal(t, 1, e.up.count())

	// 本机的 gemini-rerank 适配器：地址在回环上，接收方仍是 Google
	e.endpoint(t, apigen.MemoryEndpointRerank, e.up.srv.URL+"/rr", "gemini-decision-1", keyRerank)
	w = rerank()
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "memory-rerank:generativelanguage.googleapis.com")
	assert.Equal(t, 1, e.up.count())
	grant("memory-rerank:generativelanguage.googleapis.com")
	require.Equal(t, http.StatusOK, rerank().Code)
	assert.Equal(t, 2, e.up.count())
}

// 已同意的嵌入接口 A 返回 307 指向 B：中转不跟随、也不把 3xx 交给 Hindsight（它会自己直连 B），B 收不到记忆文本。
func TestRelayEndpointBlocksRedirect(t *testing.T) {
	var bHits atomic.Int32
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { bHits.Add(1) }))
	t.Cleanup(b.Close)
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://b.example.net/emb/embeddings", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(a.Close)
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		switch addr {
		case "embed.example.com:80":
			addr = a.Listener.Addr().String()
		case "b.example.net:80":
			addr = b.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}}
	consent := aiconsent.CheckerFunc(func(_ context.Context, id string) error {
		if id != "memory-embed:embed.example.com" {
			return &aiconsent.DeniedError{Recipient: id}
		}
		return nil
	})
	e := newRelayEnvWith(t, consent, client)
	e.endpoint(t, apigen.MemoryEndpointEmbed, "http://embed.example.com/emb", "qwen-embed", keyEmbed)
	w := e.post(t, "embed/embeddings", "", e.token, map[string]any{"model": "oryn-embed", "input": []string{"你好"}})
	assert.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Equal(t, "upstream_redirect", errCode(t, w))
	assert.Empty(t, w.Header().Get("Location"))
	assert.Zero(t, bHits.Load(), "重定向目标收不到记忆文本")
}
