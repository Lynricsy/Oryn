package artifacts

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/sample"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeOmp 是按帧序列应答的假 omp：等 ready 之后的 prompt 帧，交给 reply 生成要写回的帧。
type fakeOmp struct {
	mu      sync.Mutex
	prompts []string
	homes   []string
	// reply 生成 prompt 之后要写出的帧；block 非 nil 时先报到 started 再等 block 关闭或 ctx 结束。
	reply   func(prompt string) []string
	started chan struct{}
	block   chan struct{}
}

func (f *fakeOmp) run(ctx context.Context, home string, stdin io.Reader, stdout io.Writer) error {
	in, out := omprpc.NewReader(stdin, 0), omprpc.NewWriter(stdout, 0)
	if err := out.WriteRaw([]byte(`{"type":"ready","protocolVersion":1}`)); err != nil {
		return err
	}
	line, err := in.ReadLine()
	if err != nil {
		return err
	}
	var p struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(line, &p)
	f.mu.Lock()
	f.prompts = append(f.prompts, p.Message)
	f.homes = append(f.homes, home)
	started, block := f.started, f.block
	f.mu.Unlock()
	if block != nil {
		started <- struct{}{}
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, fr := range f.reply(p.Message) {
		if err := out.WriteRaw([]byte(fr)); err != nil {
			return err
		}
	}
	_, _ = io.Copy(io.Discard, stdin)
	return nil
}

func (f *fakeOmp) lastPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prompts[len(f.prompts)-1]
}

func (f *fakeOmp) lastHome() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.homes[len(f.homes)-1]
}

// answerFrames 是一次成功回答的帧：逐段增量、助手消息、prompt_result。
func answerFrames(parts ...string) []string {
	var out []string
	for _, p := range parts {
		b, _ := json.Marshal(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": p}})
		out = append(out, string(b))
	}
	b, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": strings.Join(parts, "")}}}})
	return append(out, string(b), `{"type":"prompt_result","id":"q","status":"completed","agentInvoked":true,"sessionSettled":true}`)
}

type sampleEnv struct {
	*rtEnv
	fake *fakeOmp
}

// newSampleEnv 建一个声明了 sample 的页面；consent 为真时已获同意。执行端用 fakeOmp，omp_path 指向一个临时文件。
func newSampleEnv(t *testing.T, consent bool) *sampleEnv {
	t.Helper()
	r := newRtEnv(t, "sample")
	if consent {
		require.NoError(t, r.e.st.Q.UpsertArtifactConsent(context.Background(), db.UpsertArtifactConsentParams{ArtifactID: r.art.ID, Capability: "sample", GrantedAt: 1}))
	}
	omp := filepath.Join(t.TempDir(), "omp")
	require.NoError(t, os.WriteFile(omp, []byte("#!/bin/sh\n"), 0o755))
	fake := &fakeOmp{reply: func(string) []string { return answerFrames("你", "好") }}
	r.e.s.AttachSampler(sample.New(sample.Config{OmpPath: omp, Run: fake.run}))
	return &sampleEnv{rtEnv: r, fake: fake}
}

// ask 发一次 sample.ask 的 stream，回全部 NDJSON 行。
func (s *sampleEnv) ask(t *testing.T, id string, args ...any) []map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"v": 1, "id": id, "kind": "stream", "cap": "sample", "op": "ask", "args": args})
	rec := postRuntime(s.mw, s.host, "/_oryn/v1", "null", string(b))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	var lines []map[string]any
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
		lines = append(lines, m)
	}
	return lines
}

func lastCode(t *testing.T, lines []map[string]any) string {
	t.Helper()
	require.NotEmpty(t, lines)
	return replyCode(t, lines[len(lines)-1])
}

// 流：先是逐段的 text，最后一条 answer，然后 done；use 列出 ask 是流式操作；临时 HOME 用完被删。
func TestSampleStream(t *testing.T) {
	s := newSampleEnv(t, true)
	assert.Equal(t, map[string]any{"ops": []any{"ask"}, "streams": []any{"ask"}},
		s.send(t, s.host, map[string]any{"kind": "use", "cap": "sample"})["value"])
	lines := s.ask(t, "a1", "打个招呼")
	require.Len(t, lines, 4, lines)
	assert.Equal(t, map[string]any{"type": "text", "text": "你"}, lines[0]["event"])
	assert.Equal(t, map[string]any{"type": "text", "text": "好"}, lines[1]["event"])
	assert.Equal(t, map[string]any{"type": "answer", "text": "你好"}, lines[2]["event"])
	assert.Equal(t, map[string]any{"v": float64(1), "id": "a1", "done": true}, lines[3])
	assert.Equal(t, "打个招呼", s.fake.lastPrompt())
	assert.NoDirExists(t, s.fake.lastHome())
	// 不是流式调用：invalid
	assert.Equal(t, CodeInvalid, replyCode(t, s.send(t, s.host, map[string]any{"kind": "call", "cap": "sample", "op": "ask", "args": []any{"hi"}})))
}

// format: "json"：提示前加要求；围栏里的 JSON 解析后放进 json；解析不了回 failed。
func TestSampleJSON(t *testing.T) {
	s := newSampleEnv(t, true)
	s.fake.reply = func(string) []string { return answerFrames("好的：\n```json\n{\"a\": [1, 2]}\n```\n") }
	lines := s.ask(t, "j1", "给我一个对象", map[string]any{"format": "json"})
	ans := lines[len(lines)-2]["event"].(map[string]any)
	assert.Equal(t, "answer", ans["type"])
	assert.Equal(t, map[string]any{"a": []any{float64(1), float64(2)}}, ans["json"])
	assert.Equal(t, sampleJSONPrefix+"给我一个对象", s.fake.lastPrompt())

	s.fake.reply = func(string) []string { return answerFrames(" 42 ") }
	lines = s.ask(t, "j2", "数字", map[string]any{"format": "json"})
	assert.Equal(t, float64(42), lines[len(lines)-2]["event"].(map[string]any)["json"])

	s.fake.reply = func(string) []string { return answerFrames("这不是 JSON") }
	assert.Equal(t, codeFailed, lastCode(t, s.ask(t, "j3", "x", map[string]any{"format": "json"})))
	// text 格式不加要求、不带 json 字段
	s.fake.reply = func(string) []string { return answerFrames("{}") }
	lines = s.ask(t, "j4", "x", map[string]any{"format": "text"})
	assert.Equal(t, map[string]any{"type": "answer", "text": "{}"}, lines[len(lines)-2]["event"])
	assert.Equal(t, "x", s.fake.lastPrompt())
}

// prompt_result.status 为 error：回 failed，message 带模型给的原因。
func TestSampleModelError(t *testing.T) {
	s := newSampleEnv(t, true)
	s.fake.reply = func(string) []string {
		return []string{`{"type":"prompt_result","id":"q","status":"error","agentInvoked":false,"sessionSettled":true,"error":{"message":"No model selected.","retryable":false}}`}
	}
	lines := s.ask(t, "e1", "hi")
	assert.Equal(t, codeFailed, lastCode(t, lines))
	assert.Contains(t, lines[len(lines)-1]["error"].(map[string]any)["message"], "No model selected.")
}

// 参数校验。
func TestSampleArgs(t *testing.T) {
	s := newSampleEnv(t, true)
	png := "data:image/png;base64,iVBORw0KGgo="
	cases := []struct {
		args []any
		code string
	}{
		{[]any{}, CodeInvalid},
		{[]any{"  "}, CodeInvalid},
		{[]any{1}, CodeInvalid},
		{[]any{strings.Repeat("字", MaxSamplePromptChars+1)}, CodeTooLarge},
		{[]any{"hi", map[string]any{"format": "yaml"}}, CodeInvalid},
		{[]any{"hi", map[string]any{"onText": 1}}, CodeInvalid},
		{[]any{"hi", map[string]any{"images": []any{"data:image/svg+xml;base64,PHN2Zz4="}}}, CodeInvalid},
		{[]any{"hi", map[string]any{"images": []any{"data:image/png;base64,***"}}}, CodeInvalid},
		{[]any{"hi", map[string]any{"images": []any{png, png, png, png, png}}}, CodeInvalid},
		{[]any{"hi", map[string]any{"images": []any{"data:image/png;base64," + strings.Repeat("A", (MaxSampleImageBytes/3+1)*4)}}}, CodeTooLarge},
	}
	for _, c := range cases {
		assert.Equal(t, c.code, lastCode(t, s.ask(t, "x", c.args...)), c.args)
	}
	// 合法的图片原样交给执行端
	q, err := parseSampleArgs(json.RawMessage(`["看图",{"images":["` + png + `"]}]`))
	require.NoError(t, err)
	assert.Equal(t, []sample.Image{{MimeType: "image/png", Data: "iVBORw0KGgo="}}, q.images)
	assert.Equal(t, "hi", lastStreamText(t, s, "ok", "hi", map[string]any{"images": []any{png, png, png, png}}))
}

func lastStreamText(t *testing.T, s *sampleEnv, id string, args ...any) string {
	t.Helper()
	lines := s.ask(t, id, args...)
	require.Equal(t, true, lines[len(lines)-1]["done"], lines)
	return s.fake.lastPrompt()
}

// 限额：同一 artifact 第 3 个并发的提问回 rate_limited；每分钟第 11 个回 rate_limited，一分钟后恢复。
func TestSampleRateLimit(t *testing.T) {
	s := newSampleEnv(t, true)
	srv := httptest.NewServer(s.mw)
	t.Cleanup(srv.Close)
	s.fake.started = make(chan struct{}, 4)
	s.fake.block = make(chan struct{})
	open := func(id string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/_oryn/v1",
			strings.NewReader(`{"v":1,"id":"`+id+`","kind":"stream","cap":"sample","op":"ask","args":["hi"]}`))
		require.NoError(t, err)
		req.Host = s.host
		req.Header.Set("Origin", "null")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		return resp
	}
	r1, r2 := open("c1"), open("c2")
	defer r1.Body.Close()
	defer r2.Body.Close()
	<-s.fake.started
	<-s.fake.started
	assert.Equal(t, CodeRateLimited, lastCode(t, s.ask(t, "c3", "hi")))
	close(s.fake.block)
	for _, r := range []*http.Response{r1, r2} {
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Contains(t, string(b), `"done":true`)
	}
	s.fake.mu.Lock()
	s.fake.block = nil
	s.fake.mu.Unlock()

	// 已经开始了 2 个（被拒的不算），再问 8 个到 10 个，第 11 个被拒
	for i := range MaxSamplePerMinute - 2 {
		assert.Equal(t, true, s.ask(t, "m"+string(rune('a'+i)), "hi")[3]["done"])
	}
	assert.Equal(t, CodeRateLimited, lastCode(t, s.ask(t, "m11", "hi")))
	s.e.clk.add(time.Minute)
	assert.Equal(t, true, s.ask(t, "m12", "hi")[3]["done"])
}

// 客户端断开：执行端的 ctx 结束，临时 HOME 被删，名额还回来。
func TestSampleCancel(t *testing.T) {
	s := newSampleEnv(t, true)
	srv := httptest.NewServer(s.mw)
	t.Cleanup(srv.Close)
	s.fake.started = make(chan struct{}, 1)
	s.fake.block = make(chan struct{}) // 永不关闭：只能被取消
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/_oryn/v1",
		strings.NewReader(`{"v":1,"id":"k1","kind":"stream","cap":"sample","op":"ask","args":["hi"]}`))
	require.NoError(t, err)
	req.Host = s.host
	req.Header.Set("Origin", "null")
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := srv.Client().Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	<-s.fake.started
	home := s.fake.lastHome()
	assert.DirExists(t, home)
	cancel()
	<-done
	require.Eventually(t, func() bool {
		_, err := os.Stat(home)
		s.e.s.samples.mu.Lock()
		defer s.e.s.samples.mu.Unlock()
		return os.IsNotExist(err) && s.e.s.samples.busy[s.art.ID] == 0
	}, 5*time.Second, 5*time.Millisecond)
}

// 未获同意：没有观看者时 use 立刻回 null，stream 回 not_allowed；没接执行端或 omp_path 不在时 use 回 null、stream 回
// unsupported；没声明 sample 时 not_declared。
func TestSampleGates(t *testing.T) {
	s := newSampleEnv(t, false)
	assert.Nil(t, s.send(t, s.host, map[string]any{"kind": "use", "cap": "sample"})["value"])
	assert.Equal(t, codeNotAllowed, lastCode(t, s.ask(t, "g1", "hi")))

	s = newSampleEnv(t, true)
	s.e.s.AttachSampler(sample.New(sample.Config{OmpPath: filepath.Join(t.TempDir(), "nope")}))
	assert.False(t, s.e.s.SampleAvailable())
	assert.Nil(t, s.send(t, s.host, map[string]any{"kind": "use", "cap": "sample"})["value"])
	assert.Equal(t, codeUnsupported, lastCode(t, s.ask(t, "g2", "hi")))
	s.e.s.AttachSampler(nil)
	assert.Equal(t, codeUnsupported, lastCode(t, s.ask(t, "g3", "hi")))

	plain := newRtEnv(t)
	b, _ := json.Marshal(map[string]any{"v": 1, "id": "g4", "kind": "stream", "cap": "sample", "op": "ask", "args": []any{"hi"}})
	assert.Equal(t, codeNotDeclared, replyCode(t, replyOf(t, postRuntime(plain.mw, plain.host, "/_oryn/v1", "null", string(b)))))
}
