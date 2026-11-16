package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/memory"
)

// M16 端到端（计划步骤 5.1）：真实的 orynd、orbd（process 驱动与 runner）与 fakeomp，假 Hindsight 与假模型上游。
// 从 orb 出站代理（agent 拿到的 HINDSIGHT_API_URL）和 REST 两个入口驱动，验证项目隔离、runner、候选、
// 别名换模型、开关与待重启、项目删除级联。

// hsCall 是假 Hindsight 收到的一次请求。
type hsCall struct {
	method, path, auth string
	body               map[string]any
}

// fakeHindsight 只认生成的 API key，记录每次调用；document 写入按 document_id 记下标签，统计数随之变化。
type fakeHindsight struct {
	srv *httptest.Server

	mu    sync.Mutex
	key   string
	calls []hsCall
	docs  map[string][]string
}

func newFakeHindsight(t *testing.T) *fakeHindsight {
	t.Helper()
	f := &fakeHindsight{docs: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHindsight) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/version" {
		_, _ = io.WriteString(w, `{"api_version":"1.2.1"}`)
		return
	}
	if r.URL.Path == "/health/ready" {
		_, _ = io.WriteString(w, `{"status":"ready"}`)
		return
	}
	if f.key == "" || r.Header.Get("Authorization") != "Bearer "+f.key {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"Authentication failed: Invalid API key"}`)
		return
	}
	c := hsCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.body)
	}
	f.calls = append(f.calls, c)
	const bank = "/v1/default/banks/oryn"
	rest := strings.TrimPrefix(r.URL.Path, bank)
	switch {
	case r.Method == http.MethodGet && rest == "/stats":
		_ = json.NewEncoder(w).Encode(map[string]any{"total_nodes": len(f.docs), "total_documents": len(f.docs)})
	case r.Method == http.MethodPost && rest == "/memories":
		items, _ := c.body["items"].([]any)
		for _, it := range items {
			m, _ := it.(map[string]any)
			id, _ := m["document_id"].(string)
			var tags []string
			for _, x := range asSlice(m["tags"]) {
				tags = append(tags, x.(string))
			}
			f.docs[id] = tags
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "bank_id": "oryn", "items_count": len(items), "async": true, "operation_id": "op-1"})
	case r.Method == http.MethodPost && (rest == "/memories/recall"):
		_, _ = io.WriteString(w, `{"results":[]}`)
	case r.Method == http.MethodDelete && strings.HasPrefix(rest, "/documents/"):
		delete(f.docs, strings.TrimPrefix(rest, "/documents/"))
		_, _ = io.WriteString(w, `{"success":true}`)
	case r.Method == http.MethodGet && (rest == "/documents" || rest == "/mental-models"):
		_, _ = io.WriteString(w, `{"items":[],"total":0,"limit":100,"offset":0}`)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func (f *fakeHindsight) setKey(k string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.key = k
}

// take 取走并清空已记录的调用。
func (f *fakeHindsight) take() []hsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

// find 返回已记录调用里满足 pred 的（不清空）。
func (f *fakeHindsight) find(pred func(hsCall) bool) []hsCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hsCall
	for _, c := range f.calls {
		if pred(c) {
			out = append(out, c)
		}
	}
	return out
}

// upCall 是假模型上游收到的一次请求。
type upCall struct {
	path, auth, apiKey, model string
}

// fakeUpstream 同时扮演 openai 兼容与 anthropic 上游，按请求的 model 回一个带 usage 的应答。
type fakeUpstream struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []upCall
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		u.calls = append(u.calls, upCall{path: r.URL.Path, auth: r.Header.Get("Authorization"), apiKey: r.Header.Get("X-Api-Key"), model: body.Model})
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/messages") {
			_ = json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "content": []any{map[string]any{"type": "text", "text": "ok"}},
				"usage": map[string]any{"input_tokens": 11, "output_tokens": 3}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": body.Model, "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 2}})
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *fakeUpstream) take() []upCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.calls
	u.calls = nil
	return out
}

// m16Thread 是一条线程在记忆上的视角：agent 收到的 HINDSIGHT_API_URL 与环境。
type m16Thread struct {
	id  string
	url string // HINDSIGHT_API_URL（…/memory/t/<threadId>）
	env map[string]string
	arg []string
}

type m16Env struct {
	*stack
	hs   *fakeHindsight
	up   *fakeUpstream
	dump string

	relayToken string
	prjP, prjQ string
	tP, tQ, tN m16Thread
	runner     m16Thread
	docP       string
}

const (
	m16RealKey  = "sk-m16-upstream-real-key"
	m16AntKey   = "sk-ant-m16-real-key"
	m16OpenAI   = "m16oai"
	m16Anthropc = "m16ant"
)

func TestE2EM16(t *testing.T) {
	dump := t.TempDir()
	// process 驱动的 orbd 继承测试进程的环境：fakeomp 把每条线程的启动参数与环境写到 dump/<线程 id>.json
	t.Setenv("FAKEOMP_DUMP_DIR", dump)
	// 中央模型目录跑 `<OmpPath> models --json --kind all`：用一个脚本给出 models.yml 里的三个 chat 模型，
	// 其余命令（协调宿主）照常交给 fakeomp
	omp := filepath.Join(t.TempDir(), "omp")
	catalog := `{"models":[` +
		`{"provider":"` + m16OpenAI + `","id":"m1","selector":"` + m16OpenAI + `/m1","name":"M1","kind":"chat"},` +
		`{"provider":"` + m16OpenAI + `","id":"m2","selector":"` + m16OpenAI + `/m2","name":"M2","kind":"chat"},` +
		`{"provider":"` + m16Anthropc + `","id":"claude-x","selector":"` + m16Anthropc + `/claude-x","name":"Claude X","kind":"chat"}]}`
	script := "#!/bin/sh\nif [ \"$1\" = models ]; then\n  printf '%s\\n' '" + catalog + "'\n  exit 0\nfi\nexec " + filepath.Join(binDir, "fakeomp") + " \"$@\"\n"
	require.NoError(t, os.WriteFile(omp, []byte(script), 0o755))
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)},
		func(c *config.Orynd) { c.Coordinator.OmpPath = omp })
	e := &m16Env{stack: s, hs: newFakeHindsight(t), up: newFakeUpstream(t), dump: dump}
	steps := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"配置与生成env", e.stepConfigure},
		{"两个项目与无项目线程", e.stepThreads},
		{"项目隔离与越权", e.stepIsolation},
		{"runner线程", e.stepRunner},
		{"候选批准与拒绝", e.stepCandidates},
		{"别名换模型", e.stepRelay},
		{"开关与待重启", e.stepToggle},
		{"删除项目级联", e.stepPurge},
	}
	for _, st := range steps {
		ok := t.Run(st.name, func(sub *testing.T) {
			e.stack.t = sub
			defer func() { e.stack.t = t }()
			st.fn(sub)
		})
		if !ok {
			t.Fatalf("%s 失败，后续步骤依赖它，停止", st.name)
		}
	}
}

// ---------------------------------------------------------------- 步骤

// R1–R3：设置 Hindsight 地址、生成 .env；假 Hindsight 接受生成的 key 后状态为 ok。
func (e *m16Env) stepConfigure(t *testing.T) {
	u := e.hs.srv.URL
	e.rest("PUT", "/api/v1/memory/settings", map[string]any{"enabled": true, "hindsightUrl": u}, nil, 200)
	var env struct {
		Content string `json:"content"`
	}
	e.rest("POST", "/api/v1/memory/env-file", nil, &env, 200)
	vals := map[string]string{}
	for _, l := range strings.Split(env.Content, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(k, "#") {
			vals[k] = v
		}
	}
	require.NotEmpty(t, vals["HINDSIGHT_API_TENANT_API_KEY"])
	e.relayToken = vals["HINDSIGHT_API_LLM_API_KEY"]
	require.NotEmpty(t, e.relayToken)
	assert.Equal(t, e.cfg.PublicURL+"/memory-relay/v1/default", vals["HINDSIGHT_API_LLM_BASE_URL"])
	e.hs.setKey(vals["HINDSIGHT_API_TENANT_API_KEY"])

	var st struct {
		State string `json:"state"`
	}
	e.rest("GET", "/api/v1/memory/status", nil, &st, 200)
	assert.Equal(t, "ok", st.State)
	e.hs.take()
}

// R10–R13：项目线程带记忆参数与环境；无项目线程不带项目、不自动写入。
func (e *m16Env) stepThreads(t *testing.T) {
	var p, q struct {
		ID string `json:"id"`
	}
	e.rest("POST", "/api/v1/projects", map[string]any{"name": "m16-p"}, &p, 201)
	e.rest("POST", "/api/v1/projects", map[string]any{"name": "m16-q"}, &q, 201)
	e.prjP, e.prjQ = p.ID, q.ID
	e.tP = e.newOrbThread(t, map[string]any{"prompt": "p", "projectId": e.prjP})
	e.tQ = e.newOrbThread(t, map[string]any{"prompt": "q", "projectId": e.prjQ})
	e.tN = e.newOrbThread(t, map[string]any{"prompt": "n"})

	for _, c := range []struct {
		th      m16Thread
		project string
	}{{e.tP, e.prjP}, {e.tQ, e.prjQ}, {e.tN, ""}} {
		assert.True(t, strings.HasSuffix(c.th.url, "/memory/t/"+c.th.id), c.th.url)
		assert.Equal(t, "oryn", c.th.env["HINDSIGHT_BANK_ID"])
		assert.Equal(t, "per-project-tagged", c.th.env["HINDSIGHT_SCOPING"])
		assert.Empty(t, c.th.env["HINDSIGHT_API_TOKEN"], "agent 拿不到任何 Hindsight 凭据")
		assert.Contains(t, c.th.env["NO_PROXY"], "127.0.0.1")
		assert.True(t, slices.ContainsFunc(c.th.arg, func(a string) bool { return strings.HasSuffix(a, "memory-overlay.yml") }), "带 --config 记忆覆盖：%v", c.th.arg)
		if c.project != "" {
			assert.Equal(t, memory.ProjectLabel(c.project), c.th.env["ORYN_HINDSIGHT_PROJECT"])
			assert.Empty(t, c.th.env["HINDSIGHT_AUTO_RETAIN"])
		} else {
			assert.NotContains(t, c.th.env, "ORYN_HINDSIGHT_PROJECT")
			assert.Equal(t, "false", c.th.env["HINDSIGHT_AUTO_RETAIN"])
		}
	}
	for _, v := range e.tP.env {
		assert.NotContains(t, v, e.hs.key, "Hindsight key 不进 orb 环境")
		assert.NotContains(t, v, e.relayToken, "中转令牌不进 orb 环境")
	}
}

// R5–R7、R14：从 orb 出站代理发召回与写入，标签与 bank 由网关强制；冒用别的线程 403。
func (e *m16Env) stepIsolation(t *testing.T) {
	code, _ := e.memPost(t, e.tP.url, "/v1/default/banks/omp-evil/memories/recall", map[string]any{"query": "q", "tags": []string{"project:other"}, "tags_match": "any"})
	require.Equal(t, http.StatusOK, code)
	code, _ = e.memPost(t, e.tN.url, "/v1/default/banks/oryn/memories/recall", map[string]any{"query": "q"})
	require.Equal(t, http.StatusOK, code)
	calls := e.hs.take()
	require.Len(t, calls, 2)
	assert.Equal(t, "/v1/default/banks/oryn/memories/recall", calls[0].path)
	assert.Equal(t, []any{memory.ProjectTag(e.prjP), memory.GlobalTag}, calls[0].body["tags"])
	assert.Equal(t, "any_strict", calls[0].body["tags_match"])
	assert.Equal(t, []any{memory.GlobalTag}, calls[1].body["tags"])

	// 项目写入：标签强制为 [project:P]
	e.docP = "omp-retain-m16-p"
	code, _ = e.memPost(t, e.tP.url, "/v1/default/banks/oryn/memories", map[string]any{
		"items": []any{map[string]any{"content": "P 用 goose 写迁移", "document_id": e.docP, "tags": []string{memory.ProjectTag(e.prjQ)}}}, "async": true,
	})
	require.Equal(t, http.StatusOK, code)
	calls = e.hs.take()
	require.Len(t, calls, 1)
	item := asSlice(calls[0].body["items"])[0].(map[string]any)
	assert.Equal(t, []any{memory.ProjectTag(e.prjP)}, item["tags"])

	// Q 不能用同一个 document_id 覆盖 P 的记忆
	code, _ = e.memPost(t, e.tQ.url, "/v1/default/banks/oryn/memories", map[string]any{
		"items": []any{map[string]any{"content": "覆盖", "document_id": e.docP, "tags": []string{}, "update_mode": "replace"}},
	})
	assert.Equal(t, http.StatusForbidden, code)

	// 用 P 的代理冒用 Q 的线程；不在放行清单的路径
	cross := strings.TrimSuffix(e.tP.url, e.tP.id) + e.tQ.id
	code, _ = e.memPost(t, cross, "/v1/default/banks/oryn/memories/recall", map[string]any{"query": "q"})
	assert.Equal(t, http.StatusForbidden, code)
	code, _ = e.memDo(t, http.MethodGet, e.tP.url+"/v1/default/banks/oryn/documents", nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Empty(t, e.hs.take(), "被拒绝的请求不转发")
}

// R13：runner 线程走 runner 上懒开的记忆通道，项目取线程自己的 project_id。
func (e *m16Env) stepRunner(t *testing.T) {
	var created struct {
		Runner struct {
			ID string `json:"id"`
		} `json:"runner"`
		Token string `json:"token"`
	}
	e.rest("POST", "/api/v1/runners", map[string]string{"name": "m16-laptop"}, &created, 201)
	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "orbd"), "run", "--runner", "--dir", work, "--allow-root")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "ORYN_URL="+e.http.URL, "ORYN_RUNNER_TOKEN="+created.Token,
		"ORYN_AGENT_BIN="+filepath.Join(binDir, "fakeomp"))
	var logs strings.Builder
	cmd.Stdout, cmd.Stderr = &logs, &logs
	require.NoError(t, cmd.Start())
	// runner 进程要活到后面的步骤结束：挂在顶层用例上
	e.stack.t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("runner orbd 日志：\n%s", logs.String())
		}
	})
	require.Eventually(t, func() bool {
		var list struct {
			Runners []struct {
				Online bool `json:"online"`
			} `json:"runners"`
		}
		e.rest("GET", "/api/v1/runners", nil, &list, 200)
		return len(list.Runners) == 1 && list.Runners[0].Online
	}, 30*time.Second, 50*time.Millisecond, "runner 上线")

	var th struct {
		Thread threadView `json:"thread"`
	}
	e.rest("POST", "/api/v1/threads", map[string]any{"prompt": "r", "projectId": e.prjP,
		"executor": map[string]any{"kind": "runner", "runnerId": created.Runner.ID, "dir": work}}, &th, 201)
	e.waitThread(th.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	e.runner = e.readDump(t, th.Thread.ID)
	assert.Equal(t, "1", e.runner.env["ORYN_RUNNER"])
	assert.Equal(t, memory.ProjectLabel(e.prjP), e.runner.env["ORYN_HINDSIGHT_PROJECT"])

	code, body := e.memPost(t, e.runner.url, "/v1/default/banks/oryn/memories/recall", map[string]any{"query": "q"})
	require.Equal(t, http.StatusOK, code, body)
	calls := e.hs.take()
	require.Len(t, calls, 1)
	assert.Equal(t, []any{memory.ProjectTag(e.prjP), memory.GlobalTag}, calls[0].body["tags"])
	// runner 的记忆通道只转发记忆请求
	code, _ = e.memDo(t, http.MethodGet, strings.SplitN(e.runner.url, "/memory/", 2)[0]+"/llm/x/v1/models", nil)
	assert.Equal(t, http.StatusForbidden, code)
}

// R8、R19–R21：全局写入与无项目写入进候选；批准（带编辑）写入 oryn:global，拒绝不碰 Hindsight。
func (e *m16Env) stepCandidates(t *testing.T) {
	code, body := e.memPost(t, e.tN.url, "/v1/default/banks/oryn/memories", map[string]any{
		"items": []any{map[string]any{"content": "主人喜欢中文注释", "document_id": "omp-retain-n"}}, "async": true,
	})
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"async":true`, "候选分流回附录 A 的异步应答")
	code, _ = e.memPost(t, e.tP.url, "/v1/default/banks/oryn/memories", map[string]any{
		"items": []any{map[string]any{"content": "所有项目都用 pnpm", "document_id": "omp-retain-g"}}, "async": true,
	})
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, e.hs.take(), "候选不写进 Hindsight")

	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"items"`
	}
	e.rest("GET", "/api/v1/memory/candidates", nil, &list, 200)
	require.Len(t, list.Items, 2)
	var keep, drop string
	for _, c := range list.Items {
		if strings.Contains(c.Content, "中文注释") {
			keep = c.ID
		} else {
			drop = c.ID
		}
	}
	require.NotEmpty(t, keep)
	require.NotEmpty(t, drop)
	e.rest("POST", "/api/v1/memory/candidates/"+keep+"/approve", map[string]any{"content": "主人偏好：代码注释用简体中文"}, nil, 200)
	e.rest("DELETE", "/api/v1/memory/candidates/"+drop, nil, nil, 204)

	writes := e.hs.find(func(c hsCall) bool { return c.method == http.MethodPost && c.path == "/v1/default/banks/oryn/memories" })
	require.Len(t, writes, 1, "只有批准的候选写进 Hindsight")
	item := asSlice(writes[0].body["items"])[0].(map[string]any)
	assert.Equal(t, "主人偏好：代码注释用简体中文", item["content"])
	assert.Equal(t, []any{memory.GlobalTag}, item["tags"])
	e.rest("GET", "/api/v1/memory/candidates", nil, &list, 200)
	assert.Empty(t, list.Items)
	e.hs.take()
}

// R15–R18：Hindsight 经中转调用模型：别名换成选定的真实模型、注入真实 key；同协议换模型下一条生效；
// 换协议在确认重启前 409 family_mismatch；用量计入。
func (e *m16Env) stepRelay(t *testing.T) {
	models := "providers:\n" +
		"  " + m16OpenAI + ":\n    baseUrl: " + e.up.srv.URL + "/v1\n    apiKey: " + m16RealKey + "\n    models:\n      - id: m1\n      - id: m2\n" +
		"  " + m16Anthropc + ":\n    baseUrl: " + e.up.srv.URL + "\n    api: anthropic-messages\n    apiKey: " + m16AntKey + "\n    models:\n      - id: claude-x\n"
	e.raw(t, "PUT", "/api/v1/agent-config/files/models.yml", models, 200)
	selectModel := func(purpose, provider, model string) {
		e.rest("PUT", "/api/v1/memory/models", map[string]any{"items": []any{
			map[string]any{"purpose": "default", "providerKey": m16OpenAI, "modelId": "m1"},
			map[string]any{"purpose": purpose, "providerKey": provider, "modelId": model},
		}}, nil, 200)
	}
	chat := func(purpose string) (int, string) {
		return e.relay(t, "/memory-relay/v1/"+purpose+"/chat/completions", map[string]any{"model": memory.PurposeAlias(apigen.MemoryPurpose(purpose)), "messages": []any{}})
	}

	selectModel("retain", m16OpenAI, "m1")
	code, body := chat("retain")
	require.Equal(t, http.StatusOK, code, body)
	assert.NotContains(t, body, m16RealKey)
	calls := e.up.take()
	require.Len(t, calls, 1)
	assert.Equal(t, "m1", calls[0].model, "别名换成真实模型")
	assert.Equal(t, "Bearer "+m16RealKey, calls[0].auth, "上游收到真实 key，不是中转令牌")

	// 同协议换模型：下一条请求生效，不需要重启
	selectModel("retain", m16OpenAI, "m2")
	code, body = chat("retain")
	require.Equal(t, http.StatusOK, code, body)
	calls = e.up.take()
	require.Len(t, calls, 1)
	assert.Equal(t, "m2", calls[0].model)

	// 换协议：确认重启前 409，确认后按新协议转发
	selectModel("retain", m16Anthropc, "claude-x")
	var ms struct {
		Items []struct {
			Purpose      string `json:"purpose"`
			NeedsRestart bool   `json:"needsRestart"`
		} `json:"items"`
		EnvSnippet string `json:"envSnippet"`
	}
	e.rest("GET", "/api/v1/memory/models", nil, &ms, 200)
	assert.Contains(t, ms.EnvSnippet, "HINDSIGHT_API_RETAIN_LLM_PROVIDER=anthropic")
	code, body = e.relay(t, "/memory-relay/v1/retain/v1/messages", map[string]any{"model": "oryn-retain", "messages": []any{}, "max_tokens": 16})
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, body, "family_mismatch")
	assert.Empty(t, e.up.take(), "409 不转发")
	e.rest("POST", "/api/v1/memory/models/deployed", nil, nil, 200)
	code, body = e.relay(t, "/memory-relay/v1/retain/v1/messages", map[string]any{"model": "oryn-retain", "messages": []any{}, "max_tokens": 16})
	require.Equal(t, http.StatusOK, code, body)
	calls = e.up.take()
	require.Len(t, calls, 1)
	assert.Equal(t, "claude-x", calls[0].model)
	assert.Equal(t, m16AntKey, calls[0].apiKey)

	// 错误令牌 401
	req, _ := http.NewRequest("POST", e.http.URL+"/memory-relay/v1/default/chat/completions", strings.NewReader(`{"model":"oryn-default"}`))
	req.Header.Set("Authorization", "Bearer "+m16RealKey)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var usage struct {
		Items []struct {
			Purpose string `json:"purpose"`
			Day     struct {
				Calls       int64 `json:"calls"`
				InputTokens int64 `json:"inputTokens"`
			} `json:"day"`
		} `json:"items"`
	}
	e.rest("GET", "/api/v1/memory/usage", nil, &usage, 200)
	var retain int64
	for _, it := range usage.Items {
		if it.Purpose == "retain" {
			retain = it.Day.Calls
			assert.EqualValues(t, 7+7+11, it.Day.InputTokens)
		}
	}
	assert.EqualValues(t, 3, retain, "三次成功调用计入用量，409 不计")
}

// R11、R12：关掉 Q 后它的线程进待重启列表（原因 memory），网关对 Q 返回 503；P 不受影响。
func (e *m16Env) stepToggle(t *testing.T) {
	e.rest("PUT", "/api/v1/memory/projects/"+e.prjQ, map[string]any{"enabled": false}, nil, 200)
	var st struct {
		Threads []struct {
			ThreadID string   `json:"threadId"`
			Reasons  []string `json:"reasons"`
		} `json:"threads"`
	}
	e.rest("GET", "/api/v1/agent-config/apply-status", nil, &st, 200)
	var flagged []string
	for _, th := range st.Threads {
		if slices.Contains(th.Reasons, "memory") {
			flagged = append(flagged, th.ThreadID)
		}
	}
	assert.Contains(t, flagged, e.tQ.id)
	assert.NotContains(t, flagged, e.tP.id)

	code, _ := e.memPost(t, e.tQ.url, "/v1/default/banks/oryn/memories/recall", map[string]any{"query": "q"})
	assert.Equal(t, http.StatusServiceUnavailable, code)
	code, _ = e.memPost(t, e.tP.url, "/v1/default/banks/oryn/memories/recall", map[string]any{"query": "q"})
	assert.Equal(t, http.StatusOK, code)
	e.hs.take()
}

// R23：删除项目 P 后，P 的 document 逐个从 Hindsight 删除，全局记忆不受影响。
func (e *m16Env) stepPurge(t *testing.T) {
	e.rest("DELETE", "/api/v1/projects/"+e.prjP, nil, nil, 204)
	require.Eventually(t, func() bool {
		return len(e.hs.find(func(c hsCall) bool {
			return c.method == http.MethodDelete && c.path == "/v1/default/banks/oryn/documents/"+e.docP
		})) == 1
	}, 10*time.Second, 50*time.Millisecond, "P 的 document 被删除")
	e.hs.mu.Lock()
	defer e.hs.mu.Unlock()
	assert.NotContains(t, e.hs.docs, e.docP)
	for id, tags := range e.hs.docs {
		assert.NotContains(t, tags, memory.ProjectTag(e.prjP), "P 的 document %s 仍在", id)
	}
	var global int
	for _, tags := range e.hs.docs {
		if slices.Contains(tags, memory.GlobalTag) {
			global++
		}
	}
	assert.Equal(t, 1, global, "批准的全局记忆保留")
}

// ---------------------------------------------------------------- 辅助

// newOrbThread 在新 orb 上建线程，等它空闲后读 fakeomp 的转储。
func (e *m16Env) newOrbThread(t *testing.T, body map[string]any) m16Thread {
	t.Helper()
	body["executor"] = map[string]any{"kind": "new_orb", "provider": "local", "type": "small"}
	var created struct {
		Thread threadView `json:"thread"`
	}
	e.rest("POST", "/api/v1/threads", body, &created, 201)
	e.waitThread(created.Thread.ID, 90*time.Second, func(v threadView) bool { return v.State == "idle" })
	return e.readDump(t, created.Thread.ID)
}

func (e *m16Env) readDump(t *testing.T, threadID string) m16Thread {
	t.Helper()
	var rec struct {
		Args []string          `json:"args"`
		Env  map[string]string `json:"env"`
	}
	path := filepath.Join(e.dump, threadID+".json")
	require.Eventually(t, func() bool { _, err := os.Stat(path); return err == nil }, 10*time.Second, 20*time.Millisecond, "fakeomp 转储 %s", path)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &rec))
	u := rec.Env["HINDSIGHT_API_URL"]
	require.NotEmpty(t, u, "agent 环境里没有 HINDSIGHT_API_URL：%v", rec.Env)
	return m16Thread{id: threadID, url: u, env: rec.Env, arg: rec.Args}
}

// memPost 以 agent 的身份经 orb 出站代理发一个 Hindsight 请求（path 是 Hindsight 路径）。
func (e *m16Env) memPost(t *testing.T, base, path string, body any) (int, string) {
	t.Helper()
	return e.memDo(t, http.MethodPost, base+path, body)
}

func (e *m16Env) memDo(t *testing.T, method, u string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, u, rd)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer forged-by-agent")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	assert.NotContains(t, string(b), e.hs.key, "应答里不得出现 Hindsight key")
	return resp.StatusCode, string(b)
}

// relay 以 Hindsight 的身份（中转令牌）调用模型中转。
func (e *m16Env) relay(t *testing.T, path string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", e.http.URL+path, strings.NewReader(string(b)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if strings.HasSuffix(path, "/messages") {
		req.Header.Set("X-Api-Key", e.relayToken)
	} else {
		req.Header.Set("Authorization", "Bearer "+e.relayToken)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// raw 以设备令牌发一个原文请求体（agent-config 文件写入是 octet-stream）。
func (e *m16Env) raw(t *testing.T, method, path, body string, want int) {
	t.Helper()
	req, err := http.NewRequest(method, e.http.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, want, resp.StatusCode, "%s %s：%s", method, path, b)
}
