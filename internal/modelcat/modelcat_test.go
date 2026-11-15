package modelcat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

const fakeOutput = `{"models":[
{"provider":"anthropic","kind":"chat","id":"claude-fable-5","selector":"anthropic/claude-fable-5","name":"Claude Fable 5","contextWindow":1000000,"maxTokens":128000,"reasoning":true,"thinking":["low","medium","high","xhigh","max"],"input":["text","image"],"cost":{"cacheRead":1,"cacheWrite":12.5,"input":10,"output":50},"pricingStatus":"fixed","extra":"忽略"},
{"provider":"openai","kind":"embedding","id":"text-embedding-3-small","selector":"openai/text-embedding-3-small","name":"Embedding","contextWindow":8191,"maxTokens":0,"reasoning":false,"thinking":null,"input":["text"]}
]}`

func newHome(t *testing.T) *agentcfg.Home {
	t.Helper()
	h, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)
	return h
}

func writeFile(t *testing.T, h *agentcfg.Home, rel, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(h.Dir(), rel), []byte(content), 0o600))
}

// counter 是计数的假 Run：每次返回 out（fail 为真时失败）。
type counter struct {
	n    atomic.Int32
	fail atomic.Bool
	gate chan struct{} // 非 nil 时运行前等它关闭
}

func (f *counter) run(ctx context.Context, home string) ([]byte, []byte, error) {
	f.n.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	if f.fail.Load() {
		return nil, []byte("boom\n"), errors.New("exit status 1")
	}
	return []byte(fakeOutput), nil, nil
}

func TestFakeOmpScript(t *testing.T) {
	t.Setenv("ORYN_LEAK", "1")
	h := newHome(t)
	writeFile(t, h, agentcfg.ModelsFile, "providers:\n  custom:\n    baseUrl: https://api.example.com/v1\n    apiKey: MY_KEY\n")
	writeFile(t, h, agentcfg.ConfigFile, "modelRoles:\n  default: anthropic/claude-fable-5\n")
	dir := t.TempDir()
	homeFile := filepath.Join(dir, "home")
	script := `#!/bin/sh
[ "$*" = "models --json --kind all" ] || { echo "bad args: $*" >&2; exit 2; }
[ "$PI_CODING_AGENT_DIR" = "$HOME/.omp/agent" ] || { echo "bad agent dir" >&2; exit 3; }
[ -f "$PI_CODING_AGENT_DIR/models.yml" ] && [ -f "$PI_CODING_AGENT_DIR/config.yml" ] || { echo "missing config" >&2; exit 3; }
[ -z "$ORYN_LEAK" ] || { echo "env leaked" >&2; exit 4; }
[ "$(pwd -P)" = "$(cd "$HOME" && pwd -P)" ] || { echo "bad cwd" >&2; exit 5; }
printf '%s' "$HOME" > '` + homeFile + `'
echo "注意：provider custom 发现失败" >&2
cat <<'EOF'
` + fakeOutput + `
EOF
`
	omp := filepath.Join(dir, "omp")
	require.NoError(t, os.WriteFile(omp, []byte(script), 0o755))

	c := New(Config{OmpPath: omp}, h, nil)
	r, err := c.Get(t.Context(), false)
	require.NoError(t, err)
	assert.False(t, r.Stale)
	assert.WithinDuration(t, time.Now(), r.FetchedAt, time.Minute)
	assert.Equal(t, []string{"注意：provider custom 发现失败"}, r.Warnings)
	require.Len(t, r.Models, 2)
	assert.Equal(t, Model{
		Provider: "anthropic", ID: "claude-fable-5", Selector: "anthropic/claude-fable-5", Name: "Claude Fable 5", Kind: "chat",
		ContextWindow: 1000000, MaxTokens: 128000, Reasoning: true,
		Thinking: []string{"low", "medium", "high", "xhigh", "max"}, Input: []string{"text", "image"},
		Cost:          &Cost{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
		PricingStatus: "fixed",
	}, r.Models[0])
	assert.Nil(t, r.Models[1].Thinking)
	assert.Nil(t, r.Models[1].Cost)
	assert.Equal(t, "embedding", r.Models[1].Kind)

	tmpHome, err := os.ReadFile(homeFile)
	require.NoError(t, err)
	require.NotEmpty(t, tmpHome)
	assert.NoDirExists(t, string(tmpHome), "临时 HOME 用完即删")
}

func TestParseErrors(t *testing.T) {
	_, err := parse([]byte(`{"items":[]}`))
	assert.ErrorContains(t, err, "models")
	_, err = parse([]byte(`not json`))
	assert.Error(t, err)
	m, err := parse([]byte(`{"models":[]}`))
	require.NoError(t, err)
	assert.Empty(t, m)
}

// 运行期间模型请求经本机回环 egress 代理注入真实密钥；临时 HOME 里只有占位符；运行后代理关闭。
func TestEgressDuringRun(t *testing.T) {
	st, err := store.Open(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	vault := secrets.NewVault(st, box)
	meta, err := vault.Put(t.Context(), agentcfg.ModelKeySecretName("acme"), secrets.ModeProxy, "sk-real", nil)
	require.NoError(t, err)

	var gotAuth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization") + " " + r.URL.Path)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(upstream.Close)

	h := newHome(t)
	writeFile(t, h, agentcfg.ModelsFile, "providers:\n  acme:\n    baseUrl: "+upstream.URL+"/v1\n    apiKey: "+agentcfg.SecretRef(agentcfg.ModelKeySecretName("acme"))+"\n")

	var baseURL, apiKey string
	run := func(ctx context.Context, home string) ([]byte, []byte, error) {
		b, err := os.ReadFile(filepath.Join(AgentDir(home), agentcfg.ModelsFile))
		if err != nil {
			return nil, nil, err
		}
		var doc struct {
			Providers map[string]struct {
				BaseURL string `yaml:"baseUrl"`
				APIKey  string `yaml:"apiKey"`
			} `yaml:"providers"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, nil, err
		}
		baseURL, apiKey = doc.Providers["acme"].BaseURL, doc.Providers["acme"].APIKey
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, nil, err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, nil, errors.New(resp.Status)
		}
		return []byte(fakeOutput), nil, nil
	}

	c := New(Config{Run: run}, h, vault)
	r, err := c.Get(t.Context(), false)
	require.NoError(t, err)
	assert.Len(t, r.Models, 2)
	assert.Equal(t, agentcfg.Placeholder(meta.ID), apiKey, "临时 HOME 里只有占位符")
	assert.Regexp(t, `^http://127\.0\.0\.1:\d+/llm/acme/v1$`, baseURL)
	assert.Equal(t, "Bearer sk-real /v1/models", gotAuth.Load())

	_, err = http.Get(baseURL + "/models")
	assert.Error(t, err, "运行结束后代理已关闭")
}

func TestCacheKeyFollowsConfig(t *testing.T) {
	h := newHome(t)
	writeFile(t, h, agentcfg.ModelsFile, "providers:\n  custom:\n    baseUrl: https://a.example.com/v1\n    apiKey: KEY_A\n")
	f := &counter{}
	c := New(Config{Run: f.run}, h, nil)

	key1, _, err := c.loadConfig()
	require.NoError(t, err)
	key1b, _, err := c.loadConfig()
	require.NoError(t, err)
	assert.Equal(t, key1, key1b, "配置不变时缓存键稳定")

	_, err = c.Get(t.Context(), false)
	require.NoError(t, err)
	_, err = c.Get(t.Context(), false)
	require.NoError(t, err)
	assert.EqualValues(t, 1, f.n.Load(), "键不变且未过期：命中缓存")

	writeFile(t, h, agentcfg.ModelsFile, "providers:\n  custom:\n    baseUrl: https://b.example.com/v1\n    apiKey: KEY_A\n")
	key2, _, err := c.loadConfig()
	require.NoError(t, err)
	assert.NotEqual(t, key1, key2, "models.yml 变了，缓存键随之变化")
	r, err := c.Get(t.Context(), false)
	require.NoError(t, err)
	assert.False(t, r.Stale)
	assert.EqualValues(t, 2, f.n.Load())

	writeFile(t, h, agentcfg.ConfigFile, "modelRoles:\n  default: openai/gpt\n")
	_, err = c.Get(t.Context(), false)
	require.NoError(t, err)
	assert.EqualValues(t, 3, f.n.Load(), "config.yml 也进入缓存键")

	_, err = c.Get(t.Context(), true)
	require.NoError(t, err)
	assert.EqualValues(t, 4, f.n.Load(), "force 总是重跑")
}

func TestConcurrentGetsRunOnce(t *testing.T) {
	f := &counter{gate: make(chan struct{})}
	c := New(Config{Run: f.run}, newHome(t), nil)
	var wg sync.WaitGroup
	results := make([]Result, 10)
	errs := make([]error, 10)
	for i := range 10 {
		wg.Go(func() { results[i], errs[i] = c.Get(t.Context(), false) })
	}
	time.Sleep(100 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	assert.EqualValues(t, 1, f.n.Load())
	for i := range 10 {
		require.NoError(t, errs[i])
		assert.Len(t, results[i].Models, 2)
		assert.False(t, results[i].Stale)
	}
}

func TestFailureReturnsStale(t *testing.T) {
	f := &counter{}
	f.fail.Store(true)
	c := New(Config{Run: f.run}, newHome(t), nil)
	_, err := c.Get(t.Context(), false)
	require.Error(t, err, "从未成功过：返回错误")
	assert.ErrorContains(t, err, "boom")

	f.fail.Store(false)
	good, err := c.Get(t.Context(), false)
	require.NoError(t, err)
	require.False(t, good.Stale)

	f.fail.Store(true)
	r, err := c.Get(t.Context(), true)
	require.NoError(t, err)
	assert.True(t, r.Stale)
	assert.Equal(t, good.Models, r.Models)
	assert.Equal(t, good.FetchedAt, r.FetchedAt)
	require.NotEmpty(t, r.Warnings)
	assert.Contains(t, r.Warnings[len(r.Warnings)-1], "刷新模型目录失败")
	assert.EqualValues(t, 3, f.n.Load())

	again, err := c.Get(t.Context(), false)
	require.NoError(t, err)
	assert.False(t, again.Stale, "失败不覆盖上一份结果")
	assert.Empty(t, again.Warnings, "回退时附加的说明不污染缓存")
}

// 落盘的结果在重启后立即返回（Stale），同时后台按当前配置重跑；过期结果同理。
func TestDiskCacheAndTTL(t *testing.T) {
	h := newHome(t)
	cacheDir := filepath.Join(t.TempDir(), "model-catalog")
	first := &counter{}
	_, err := New(Config{Run: first.run, CacheDir: cacheDir}, h, nil).Get(t.Context(), false)
	require.NoError(t, err)
	fi, err := os.Stat(filepath.Join(cacheDir, cacheFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	f := &counter{gate: make(chan struct{})}
	c := New(Config{Run: f.run, CacheDir: cacheDir, TTL: time.Hour}, h, nil)
	r, err := c.Get(t.Context(), false)
	require.NoError(t, err)
	assert.True(t, r.Stale, "重启后的磁盘结果在重跑前标 Stale")
	assert.Len(t, r.Models, 2)
	close(f.gate)
	require.Eventually(t, func() bool {
		r, err := c.Get(t.Context(), false)
		return err == nil && !r.Stale
	}, 5*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, f.n.Load())

	// 磁盘缓存的键与当前配置不符：阻塞重跑而不是返回旧目录
	writeFile(t, h, agentcfg.ModelsFile, "providers: {}\n")
	g := &counter{}
	r, err = New(Config{Run: g.run, CacheDir: cacheDir}, h, nil).Get(t.Context(), false)
	require.NoError(t, err)
	assert.False(t, r.Stale)
	assert.EqualValues(t, 1, g.n.Load())

	// 过期：先回旧结果，后台重跑
	e := &counter{}
	ce := New(Config{Run: e.run, TTL: time.Nanosecond}, h, nil)
	_, err = ce.Get(t.Context(), false)
	require.NoError(t, err)
	r, err = ce.Get(t.Context(), false)
	require.NoError(t, err)
	assert.True(t, r.Stale)
	require.Eventually(t, func() bool {
		ce.mu.Lock()
		defer ce.mu.Unlock()
		return e.n.Load() == 2 && ce.inflight == nil
	}, 5*time.Second, 10*time.Millisecond)
}
