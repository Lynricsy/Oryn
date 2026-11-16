package sample

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

// conv 是假 omp 的一端：in 读 stdin 的帧，out 写 stdout 的帧。
type conv struct {
	t    *testing.T
	ctx  context.Context
	home string
	in   *omprpc.Reader
	out  *omprpc.Writer
}

func (c *conv) send(frames ...string) {
	for _, f := range frames {
		if err := c.out.WriteRaw([]byte(f)); err != nil {
			return
		}
	}
}

// recv 读下一条 stdin 帧；stdin 关闭时回 nil。
func (c *conv) recv() map[string]any {
	line, err := c.in.ReadLine()
	if err != nil {
		return nil
	}
	var m map[string]any
	require.NoError(c.t, json.Unmarshal(line, &m))
	return m
}

// waitEOF 读到 stdin 关闭（或 ctx 结束）。
func (c *conv) waitEOF() {
	for c.recv() != nil {
	}
}

// fakeRun 把一段脚本包装成 Config.Run，并记下每次运行的临时 HOME。
type fakeRun struct {
	mu    sync.Mutex
	homes []string
	runs  atomic.Int32
}

func (f *fakeRun) run(t *testing.T, script func(c *conv) error) func(context.Context, string, io.Reader, io.Writer) error {
	return func(ctx context.Context, home string, stdin io.Reader, stdout io.Writer) error {
		f.runs.Add(1)
		f.mu.Lock()
		f.homes = append(f.homes, home)
		f.mu.Unlock()
		return script(&conv{t: t, ctx: ctx, home: home, in: omprpc.NewReader(stdin, 0), out: omprpc.NewWriter(stdout, 0)})
	}
}

func (f *fakeRun) lastHome() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.homes[len(f.homes)-1]
}

const (
	readyFrame = `{"type":"ready","protocolVersion":1}`
	okResponse = `{"id":"q","type":"response","command":"prompt","success":true}`
	completed  = `{"type":"prompt_result","id":"q","agentInvoked":true,"status":"completed","sessionSettled":true}`
)

func delta(s string) string {
	b, _ := json.Marshal(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": s}})
	return string(b)
}

func assistantEnd(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "thinking", "thinking": "想一想"}, map[string]any{"type": "text", "text": text}}}})
	return string(b)
}

// 正常的一问一答：等 ready 才写 prompt（带图片）；增量逐段送出；答案取助手消息的文本块；对话框类界面请求回 cancelled；
// 临时 HOME 是 0700、运行期间存在、结束后被删除。
func TestAskStreams(t *testing.T) {
	var f fakeRun
	var gotPrompt map[string]any
	var gotUI map[string]any
	var homeMode os.FileMode
	s := New(Config{Run: f.run(t, func(c *conv) error {
		fi, err := os.Stat(c.home)
		require.NoError(t, err)
		homeMode = fi.Mode().Perm()
		c.send(`{"type":"extension_ui_request","id":"w1","method":"setWidget"}`, readyFrame)
		gotPrompt = c.recv()
		c.send(okResponse, `{"type":"extension_ui_request","id":"u1","method":"select","options":["a"]}`)
		gotUI = c.recv()
		c.send(`{"type":"message_end","message":{"role":"user","content":"别当答案"}}`,
			delta("你"), delta("好"), assistantEnd("你好"), `{"type":"agent_end"}`, completed)
		c.waitEOF()
		return nil
	})})
	var parts []string
	answer, err := s.Ask(context.Background(), "打个招呼", []Image{{MimeType: "image/png", Data: "iVBORw0KGgo="}}, func(p string) error {
		parts = append(parts, p)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "你好", answer)
	assert.Equal(t, []string{"你", "好"}, parts)
	assert.Equal(t, map[string]any{"id": "q", "type": "prompt", "message": "打个招呼",
		"images": []any{map[string]any{"type": "image", "data": "iVBORw0KGgo=", "mimeType": "image/png"}}}, gotPrompt)
	assert.Equal(t, map[string]any{"type": "extension_ui_response", "id": "u1", "cancelled": true}, gotUI)
	assert.Equal(t, os.FileMode(0o700), homeMode)
	assert.NoDirExists(t, f.lastHome())
}

// prompt_result 的 status 不是 completed：回 *ModelError，原因取 error.message。
func TestAskModelError(t *testing.T) {
	var f fakeRun
	s := New(Config{Run: f.run(t, func(c *conv) error {
		c.send(readyFrame)
		c.recv()
		c.send(okResponse,
			`{"id":"q","type":"response","command":"prompt","success":false,"error":"No model selected."}`,
			`{"type":"prompt_result","id":"q","agentInvoked":false,"status":"error","sessionSettled":true,"error":{"message":"No model selected.","retryable":false}}`)
		c.waitEOF()
		return nil
	})})
	_, err := s.Ask(context.Background(), "hi", nil, nil)
	var me *ModelError
	require.ErrorAs(t, err, &me)
	assert.Equal(t, "No model selected.", me.Message)
	assert.NoDirExists(t, f.lastHome())
}

// 取消：Run 的 ctx 跟着结束，Ask 回 context.Canceled，临时 HOME 被删除。
func TestAskCancel(t *testing.T) {
	var f fakeRun
	asked := make(chan struct{})
	runDone := make(chan error, 1)
	s := New(Config{Run: f.run(t, func(c *conv) error {
		c.send(readyFrame)
		c.recv()
		c.send(okResponse, delta("一半"))
		close(asked)
		<-c.ctx.Done()
		runDone <- c.ctx.Err()
		return c.ctx.Err()
	})})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-asked
		cancel()
	}()
	_, err := s.Ask(ctx, "hi", nil, func(string) error { return nil })
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, <-runDone, context.Canceled)
	assert.NoDirExists(t, f.lastHome())
}

// onText 报错（客户端写不进去）：立刻中止并回那个错误。
func TestAskEmitError(t *testing.T) {
	var f fakeRun
	s := New(Config{Run: f.run(t, func(c *conv) error {
		c.send(readyFrame)
		c.recv()
		c.send(delta("x"))
		<-c.ctx.Done()
		return c.ctx.Err()
	})})
	boom := errors.New("写不进去")
	_, err := s.Ask(context.Background(), "hi", nil, func(string) error { return boom })
	require.ErrorIs(t, err, boom)
	assert.NoDirExists(t, f.lastHome())
}

// 超时回 ErrTimeout；omp 在结果前退出回带退出原因的错误。
func TestAskTimeoutAndEarlyExit(t *testing.T) {
	var f fakeRun
	s := New(Config{Timeout: 50 * time.Millisecond, Run: f.run(t, func(c *conv) error {
		c.send(readyFrame)
		<-c.ctx.Done()
		return c.ctx.Err()
	})})
	_, err := s.Ask(context.Background(), "hi", nil, nil)
	require.ErrorIs(t, err, ErrTimeout)

	s = New(Config{Run: f.run(t, func(c *conv) error {
		c.send(readyFrame)
		c.recv()
		return errors.New("exit status 1: 崩了")
	})})
	_, err = s.Ask(context.Background(), "hi", nil, nil)
	require.ErrorIs(t, err, errNoResult)
	assert.Contains(t, err.Error(), "崩了")
}

// 拿到结果后 omp 不肯退出：exitGrace 之后取消运行，答案照常返回。
func TestAskExitGrace(t *testing.T) {
	old := exitGrace
	exitGrace = 50 * time.Millisecond
	t.Cleanup(func() { exitGrace = old })
	var f fakeRun
	s := New(Config{Run: f.run(t, func(c *conv) error {
		c.send(readyFrame)
		c.recv()
		c.send(assistantEnd("ok"), completed)
		<-c.ctx.Done() // 无视 stdin 关闭
		return c.ctx.Err()
	})})
	answer, err := s.Ask(context.Background(), "hi", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", answer)
}

// 全局进程数上限：满了的问题排队，排队受 ctx 约束、不启动进程。
func TestAskMaxProcs(t *testing.T) {
	var f fakeRun
	started := make(chan struct{})
	release := make(chan struct{})
	s := New(Config{MaxProcs: 1, Run: f.run(t, func(c *conv) error {
		c.send(readyFrame)
		c.recv()
		close(started)
		<-release
		c.send(assistantEnd("ok"), completed)
		c.waitEOF()
		return nil
	})})
	first := make(chan error, 1)
	go func() {
		_, err := s.Ask(context.Background(), "hi", nil, nil)
		first <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := s.Ask(ctx, "hi", nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.EqualValues(t, 1, f.runs.Load())
	close(release)
	require.NoError(t, <-first)
}

// 配置渲染与 egress：临时 HOME 里的 models.yml 指向本机回环代理、只有占位符；模型请求经代理注入真实密钥；
// config.yml 原样写入；问完之后代理关闭。
func TestAskEgress(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	vault := secrets.NewVault(st, box)
	meta, err := vault.Put(ctx, agentcfg.ModelKeySecretName("acme"), secrets.ModeProxy, "sk-real", nil)
	require.NoError(t, err)
	var gotAuth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization") + " " + r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)

	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)
	models := "providers:\n  acme:\n    baseUrl: " + upstream.URL + "/v1\n    apiKey: " + agentcfg.SecretRef(agentcfg.ModelKeySecretName("acme")) + "\n"
	_, err = home.Put(ctx, agentcfg.ModelsFile, []byte(models), 0o600, vault)
	require.NoError(t, err)
	_, err = home.Put(ctx, agentcfg.ConfigFile, []byte("modelRoles:\n  default: acme/m1\n"), 0o600, vault)
	require.NoError(t, err)

	var f fakeRun
	var baseURL, apiKey string
	var config []byte
	s := New(Config{Home: home, Vault: vault, Run: f.run(t, func(c *conv) error {
		b, err := os.ReadFile(filepath.Join(AgentDir(c.home), agentcfg.ModelsFile))
		require.NoError(t, err)
		var doc struct {
			Providers map[string]struct {
				BaseURL string `yaml:"baseUrl"`
				APIKey  string `yaml:"apiKey"`
			} `yaml:"providers"`
		}
		require.NoError(t, yaml.Unmarshal(b, &doc))
		baseURL, apiKey = doc.Providers["acme"].BaseURL, doc.Providers["acme"].APIKey
		config, _ = os.ReadFile(filepath.Join(AgentDir(c.home), agentcfg.ConfigFile))
		req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, baseURL+"/chat/completions", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		c.send(readyFrame)
		c.recv()
		c.send(assistantEnd("ok"), completed)
		c.waitEOF()
		return nil
	})})
	_, err = s.Ask(ctx, "hi", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, agentcfg.Placeholder(meta.ID), apiKey, "临时 HOME 里只有占位符")
	assert.Regexp(t, `^http://127\.0\.0\.1:\d+/llm/acme/v1$`, baseURL)
	assert.Equal(t, "Bearer sk-real /v1/chat/completions", gotAuth.Load())
	assert.Equal(t, "modelRoles:\n  default: acme/m1\n", string(config))
	_, err = http.Get(baseURL + "/models")
	assert.Error(t, err, "问完之后代理已关闭")
}

func TestAvailable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "omp")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	assert.True(t, New(Config{OmpPath: exe}).Available())
	assert.False(t, New(Config{OmpPath: dir}).Available())
	assert.False(t, New(Config{OmpPath: filepath.Join(dir, "nope")}).Available())
	assert.False(t, New(Config{}).Available())
	var nilSampler *Sampler
	assert.False(t, nilSampler.Available())
}

// 真实调用（ORYN_TEST_OMP=1）：用开发环境的数据目录（ORYN_TEST_OMP_DATA，缺省仓库的 .dev/data）的 omp-home 与
// 密钥库、钉住的 Core（ORYN_TEST_OMP_PATH，缺省 build/core/x64/omp）问一个问题。数据目录先复制一份再打开，
// 不动原库。
func TestAskRealOmp(t *testing.T) {
	if os.Getenv("ORYN_TEST_OMP") != "1" {
		t.Skip("设 ORYN_TEST_OMP=1 才跑真实的 omp")
	}
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	src := envOr("ORYN_TEST_OMP_DATA", filepath.Join(root, ".dev/data"))
	omp := envOr("ORYN_TEST_OMP_PATH", filepath.Join(root, "build/core/x64/omp"))
	data := t.TempDir()
	for _, name := range []string{store.FileName, store.FileName + "-wal", "master.key"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(data, name), b, 0o600))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(data, agentcfg.HomeDirName), 0o700))
	for _, name := range []string{agentcfg.ModelsFile, agentcfg.ConfigFile} {
		b, err := os.ReadFile(filepath.Join(src, agentcfg.HomeDirName, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(data, agentcfg.HomeDirName, name), b, 0o600))
	}
	ctx := context.Background()
	st, err := store.Open(ctx, data)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.LoadOrCreate(data)
	require.NoError(t, err)
	home, err := agentcfg.NewHome(data)
	require.NoError(t, err)
	s := New(Config{OmpPath: omp, Home: home, Vault: secrets.NewVault(st, box)})
	require.True(t, s.Available())
	var streamed strings.Builder
	answer, err := s.Ask(ctx, "Reply with exactly the word: pong", nil, func(p string) error {
		streamed.WriteString(p)
		return nil
	})
	require.NoError(t, err)
	assert.Contains(t, strings.ToLower(answer), "pong")
	assert.Equal(t, answer, streamed.String())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
