package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/providerrt"
)

// 计划 V1：全栈端到端——真实 orynd 组装 + provider 运行时（process 驱动）+ 真实 orbd + fakeomp。

var binDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oryn-e2e-*")
	if err != nil {
		panic(err)
	}
	binDir = dir
	for _, c := range []string{"orbd", "fakeomp"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(dir, c), "../../cmd/"+c).CombinedOutput()
		if err != nil {
			panic(fmt.Sprintf("构建 %s 失败: %v\n%s", c, err, out))
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type stack struct {
	t      *testing.T
	cfg    config.Orynd
	srv    *Server
	http   *httptest.Server
	token  string // 设备令牌
	cancel context.CancelFunc
	drv    *providerrt.Process
}

// serve 在固定地址上启动 HTTP 服务（addr 为空时随机端口），便于模拟控制面重启。
func serve(t *testing.T, h http.Handler, addr string) *httptest.Server {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	var ln net.Listener
	var err error
	for range 50 { // 刚关闭的端口可能短暂不可用
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NoError(t, err)
	hs := httptest.NewUnstartedServer(h)
	hs.Listener = ln
	hs.Start()
	return hs
}

// restart 模拟 orynd 重启：关闭全部组件后用同一数据目录与地址重新组装。
func (s *stack) restart() {
	s.t.Helper()
	addr := s.http.Listener.Addr().String()
	s.http.CloseClientConnections()
	s.http.Close()
	s.srv.Close()
	srv, err := New(context.Background(), s.cfg)
	require.NoError(s.t, err)
	s.srv = srv
	s.http = serve(s.t, srv.Handler, addr)
	s.t.Cleanup(func() { s.http.Close(); srv.Close() })
}

func newStack(t *testing.T, idle config.Idle) *stack {
	t.Helper()
	t.Setenv("ORYN_AGENT_BIN", filepath.Join(binDir, "fakeomp"))
	t.Setenv("FAKEOMP_DELAY_MS", "15")
	cfg := config.DefaultOrynd()
	cfg.DataDir = t.TempDir()
	cfg.PublicURL = "http://localhost"
	cfg.Idle = idle
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := New(ctx, cfg)
	require.NoError(t, err)
	hs := serve(t, srv.Handler, "")
	s := &stack{t: t, cfg: cfg, srv: srv, http: hs, cancel: cancel}
	t.Cleanup(func() { cancel(); s.http.Close(); s.srv.Close() })

	_, tok, err := auth.CreateDevice(ctx, srv.Store, "e2e", "cli", "linux")
	require.NoError(t, err)
	s.token = tok
	var prov struct {
		Token string `json:"token"`
	}
	s.rest("POST", "/api/v1/providers", map[string]string{"name": "local"}, &prov, 201)

	drv := providerrt.NewProcess(t.TempDir(), filepath.Join(binDir, "orbd"), config.Docker{Types: config.DefaultDockerTypes(), DefaultType: "small"})
	t.Cleanup(drv.Close)
	s.drv = drv
	r := &providerrt.Runner{URL: hs.URL, OrbURL: hs.URL, Token: prov.Token, Driver: drv, ClaimWait: time.Second, Version: "e2e"}
	go func() { _ = r.Run(ctx) }()
	require.Eventually(t, func() bool {
		var list struct {
			Providers []struct {
				Online bool `json:"online"`
			} `json:"providers"`
		}
		s.rest("GET", "/api/v1/providers", nil, &list, 200)
		return len(list.Providers) == 1 && list.Providers[0].Online
	}, 10*time.Second, 20*time.Millisecond, "provider 上线")
	return s
}

func (s *stack) rest(method, path string, body, out any, want int) {
	s.t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, s.http.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(s.t, err)
	defer resp.Body.Close()
	if resp.StatusCode != want {
		b, _ := io.ReadAll(resp.Body)
		s.t.Fatalf("%s %s: 状态码 %d（期望 %d）：%s", method, path, resp.StatusCode, want, b)
	}
	if out != nil {
		require.NoError(s.t, json.NewDecoder(resp.Body).Decode(out))
	}
}

type wsMsg struct {
	Type     string            `json:"type"`
	ThreadID string            `json:"threadId"`
	Frame    json.RawMessage   `json:"frame"`
	Entries  []json.RawMessage `json:"entries"`
	Agent    struct {
		Live      []json.RawMessage `json:"live"`
		PendingUI []json.RawMessage `json:"pendingUi"`
	} `json:"agent"`
	RequestID string            `json:"requestId"`
	Upsert    []json.RawMessage `json:"upsert"`
	Orb       json.RawMessage   `json:"orb"`
}

func (m wsMsg) frameType() string {
	var f struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(m.Frame, &f)
	return f.Type
}

type wsClient struct {
	t  *testing.T
	c  *websocket.Conn
	ch chan wsMsg
}

func (s *stack) dial() *wsClient {
	s.t.Helper()
	var tk struct {
		Ticket string `json:"ticket"`
	}
	s.rest("POST", "/api/v1/ws-ticket", nil, &tk, 200)
	c, _, err := websocket.Dial(context.Background(), strings.Replace(s.http.URL, "http", "ws", 1)+"/api/v1/ws?ticket="+tk.Ticket, nil)
	require.NoError(s.t, err)
	c.SetReadLimit(64 << 20)
	w := &wsClient{t: s.t, c: c, ch: make(chan wsMsg, 4096)}
	go func() {
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				close(w.ch)
				return
			}
			var m wsMsg
			if json.Unmarshal(b, &m) == nil {
				w.ch <- m
			}
		}
	}()
	s.t.Cleanup(func() { _ = c.CloseNow() })
	return w
}

func (w *wsClient) send(v any) {
	b, _ := json.Marshal(v)
	require.NoError(w.t, w.c.Write(context.Background(), websocket.MessageText, b))
}

// until 消费消息直到 fn 返回 true；超时失败。
func (w *wsClient) until(timeout time.Duration, fn func(wsMsg) bool) {
	w.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-w.ch:
			require.True(w.t, ok, "WS 连接被关闭")
			if fn(m) {
				return
			}
		case <-deadline:
			w.t.Fatal("等待 WS 消息超时")
		}
	}
}

type threadView struct {
	ID    string `json:"id"`
	State string `json:"state"`
	OrbID string `json:"orbId"`
	Error *struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	} `json:"error"`
}

// upserted 返回 index.patch 中 id 线程的摘要。
func (m wsMsg) upserted(id string) (threadView, bool) {
	if m.Type != "index.patch" {
		return threadView{}, false
	}
	for _, raw := range m.Upsert {
		var v threadView
		if json.Unmarshal(raw, &v) == nil && v.ID == id {
			return v, true
		}
	}
	return threadView{}, false
}

func (s *stack) waitThread(id string, timeout time.Duration, cond func(threadView) bool) threadView {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var v threadView
		s.rest("GET", "/api/v1/threads/"+id, nil, &v, 200)
		if cond(v) {
			return v
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("线程 %s 未达到期望状态，当前 %+v", id, v)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFullStackThread(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	a := s.dial()

	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt":   "hello from e2e",
		"executor": map[string]any{"kind": "new_orb", "provider": "local", "type": "small"},
	}, &created, 201)
	th := created.Thread
	assert.Equal(t, "provisioning", th.State)
	a.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": 0})

	var frames []string
	var b *wsClient
	var bLive int
	a.until(60*time.Second, func(m wsMsg) bool {
		if m.Type == "frame" && m.ThreadID == th.ID {
			ft := m.frameType()
			frames = append(frames, ft)
			if ft == "message_update" && b == nil {
				b = s.dial()
				b.send(map[string]any{"type": "sub", "threadId": th.ID})
				b.until(10*time.Second, func(m wsMsg) bool {
					if m.Type == "thread.snapshot" {
						bLive = len(m.Agent.Live)
						return true
					}
					return false
				})
			}
			return ft == "agent_end"
		}
		return false
	})
	assert.Equal(t, []string{"message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_end", "agent_end"},
		subseq(frames, "message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_end", "agent_end"))
	require.NotNil(t, b)
	assert.Positive(t, bLive, "流式中途加入的订阅者从 thread.snapshot.live 拿到在途帧")

	final := s.waitThread(th.ID, 30*time.Second, func(v threadView) bool { return v.State == "idle" })
	require.NotEmpty(t, final.OrbID)

	var page struct {
		Entries []struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"entries"`
	}
	s.rest("GET", "/api/v1/threads/"+th.ID+"/entries?limit=500", nil, &page, 200)
	roles := map[string]int{}
	for _, e := range page.Entries {
		var p struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		if e.Type == "message" {
			roles[p.Message.Role]++
		}
	}
	assert.GreaterOrEqual(t, roles["user"], 1)
	assert.GreaterOrEqual(t, roles["assistant"], 1)

	// 统计：fakeomp 的 assistant 消息带 usage
	var usage struct {
		Groups []struct {
			InputTokens int64 `json:"inputTokens"`
		} `json:"groups"`
	}
	s.rest("GET", "/api/v1/stats/usage?groupBy=model", nil, &usage, 200)
	require.NotEmpty(t, usage.Groups)
	assert.Positive(t, usage.Groups[0].InputTokens)

	// 第二条消息经 WS cmd 下发并完成
	a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "c1", "frame": map[string]any{"type": "prompt", "message": "second"}})
	a.until(30*time.Second, func(m wsMsg) bool { return m.Type == "frame" && m.frameType() == "agent_end" })
	s.waitThread(th.ID, 30*time.Second, func(v threadView) bool { return v.State == "idle" })
}

func TestFullStackFreezeAndWake(t *testing.T) {
	evalInterval = 200 * time.Millisecond
	t.Cleanup(func() { evalInterval = 0 })
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Second), UserIdle: config.Duration(time.Second), StopAfterFrozen: config.Duration(24 * time.Hour)})
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt":   "freeze me",
		"executor": map[string]any{"kind": "new_orb", "provider": "local"},
	}, &created, 201)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })

	orbState := func() string {
		var o struct {
			State string `json:"state"`
		}
		s.rest("GET", "/api/v1/orbs/"+th.OrbID, nil, &o, 200)
		return o.State
	}
	require.Eventually(t, func() bool { return orbState() == "frozen" }, 30*time.Second, 100*time.Millisecond, "空闲后冻结")

	// 冻结后发消息：唤醒并正常应答
	a := s.dial()
	a.send(map[string]any{"type": "sub", "threadId": th.ID})
	a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "w1", "frame": map[string]any{"type": "prompt", "message": "wake up"}})
	a.until(60*time.Second, func(m wsMsg) bool { return m.Type == "frame" && m.frameType() == "agent_end" })
	assert.Equal(t, "ready", orbState())

	evs, err := s.srv.Store.Q.ListOrbEvents(context.Background(), th.OrbID)
	require.NoError(t, err)
	var seq []string
	for _, e := range evs {
		if e.FromState != "" {
			seq = append(seq, e.FromState+">"+e.ToState)
		}
	}
	assert.Equal(t, []string{"ready>freezing", "freezing>frozen", "frozen>waking", "waking>connecting", "connecting>resuming", "resuming>ready"},
		subseq(seq, "ready>freezing", "freezing>frozen", "frozen>waking", "waking>connecting", "connecting>resuming", "resuming>ready"))
}

// subseq 返回 xs 中按顺序首次出现的 want 元素序列。
func subseq(xs []string, want ...string) []string {
	var out []string
	i := 0
	for _, x := range xs {
		if i < len(want) && x == want[i] {
			out = append(out, x)
			i++
		}
	}
	return out
}

// 控制面重启后：待答的 UI 请求由 orbd 在新 agent 流上重放，作答后 agent 继续；条目不重复。
func TestFullStackRestartKeepsPendingAsk(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt":   "please ask me",
		"executor": map[string]any{"kind": "new_orb", "provider": "local"},
	}, &created, 201)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "awaiting_input" })

	s.restart()

	a := s.dial()
	a.send(map[string]any{"type": "sub", "threadId": th.ID})
	var reqID string
	a.until(60*time.Second, func(m wsMsg) bool {
		frames := m.Agent.PendingUI
		if m.Type == "frame" && m.frameType() == "extension_ui_request" {
			frames = append(frames, m.Frame)
		}
		for _, f := range frames {
			var r struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(f, &r) == nil && r.Method == "select" {
				reqID = r.ID
				return true
			}
		}
		return false
	})
	s.waitThread(th.ID, 30*time.Second, func(v threadView) bool { return v.State == "awaiting_input" })
	a.send(map[string]any{"type": "ui.response", "threadId": th.ID, "frame": map[string]any{"type": "extension_ui_response", "id": reqID, "value": "yes"}})
	s.waitThread(th.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" })

	var dup int
	require.NoError(t, s.srv.Store.DB.QueryRow(
		`SELECT COUNT(*) - COUNT(DISTINCT file || '/' || entry_id) FROM entries WHERE thread_id = ?`, th.ID).Scan(&dup))
	assert.Zero(t, dup, "重启后条目无重复")
}

// setup 脚本失败不让 orb 失败：orb 进入 ready、agent 照常回合（才能读日志并修复脚本），失败原因记入 orb_events。
func TestFullStackSetupFailureStillStartsAgent(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	var prj struct {
		ID string `json:"id"`
	}
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "broken-setup", "setupScript": "echo boom; exit 3"}, &prj, 201)
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"projectId": prj.ID,
		"prompt":    "hello despite broken setup",
		"executor":  map[string]any{"kind": "new_orb", "provider": "local", "type": "small"},
	}, &created, 201)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" || v.State == "error" })
	require.Equal(t, "idle", th.State)

	var orbState, detail string
	require.NoError(t, s.srv.Store.DB.QueryRow(`SELECT state FROM orbs WHERE id = ?`, th.OrbID).Scan(&orbState))
	assert.Equal(t, "ready", orbState)
	require.NoError(t, s.srv.Store.DB.QueryRow(
		`SELECT detail FROM orb_events WHERE orb_id = ? AND from_state = 'setting_up' AND to_state = 'ready'`, th.OrbID).Scan(&detail))
	assert.Contains(t, detail, `"ok":false`)
	assert.Contains(t, detail, "exit status 3")

	// setup 运行记录：ok=0，日志含脚本输出，Orb.lastSetup 经 REST 可见
	var orb struct {
		LastSetup *struct {
			ID      string `json:"id"`
			Ok      bool   `json:"ok"`
			Trigger string `json:"trigger"`
			Error   string `json:"error"`
		} `json:"lastSetup"`
	}
	require.Eventually(t, func() bool {
		s.rest("GET", "/api/v1/orbs/"+th.OrbID, nil, &orb, 200)
		return orb.LastSetup != nil
	}, 10*time.Second, 50*time.Millisecond, "Orb.lastSetup")
	assert.False(t, orb.LastSetup.Ok)
	assert.Equal(t, "auto", orb.LastSetup.Trigger)
	assert.Contains(t, orb.LastSetup.Error, "exit status 3")
	req, _ := http.NewRequest("GET", s.http.URL+"/api/v1/orbs/"+th.OrbID+"/setup-runs/"+orb.LastSetup.ID+"/log", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	logText, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(logText), "boom")
}

// orb 进入 failed：线程 error 并带 orb_failed 原因（REST 与 index.patch 都可见）；修好配置后 wake → orb 离开 failed，
// 线程按正常规则恢复并完成首条提示词。
func TestFullStackOrbFailedThreadErrorAndWake(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	var prj struct {
		ID string `json:"id"`
	}
	// clone 不存在的仓库 → setup.run 失败 → orb failed
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "bad-repo", "repoUrl": "file://" + filepath.Join(t.TempDir(), "missing.git")}, &prj, 201)
	a := s.dial()
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"projectId": prj.ID,
		"prompt":    "hello after repair",
		"executor":  map[string]any{"kind": "new_orb", "provider": "local", "type": "small"},
	}, &created, 201)
	id := created.Thread.ID

	var pushed threadView
	a.until(60*time.Second, func(m wsMsg) bool {
		v, ok := m.upserted(id)
		if ok && v.State == "error" {
			pushed = v
			return true
		}
		return false
	})
	require.NotNil(t, pushed.Error, "index.patch 带 Thread.error")
	assert.Equal(t, "orb_failed", pushed.Error.Kind)
	assert.Contains(t, pushed.Error.Reason, "clone")

	th := s.waitThread(id, 10*time.Second, func(v threadView) bool { return v.State == "error" })
	require.NotNil(t, th.Error)
	var orb struct {
		State         string `json:"state"`
		FailureReason string `json:"failureReason"`
	}
	s.rest("GET", "/api/v1/orbs/"+th.OrbID, nil, &orb, 200)
	assert.Equal(t, "failed", orb.State)
	assert.Equal(t, orb.FailureReason, th.Error.Reason, "线程错误原因与 Orb.failureReason 一致")

	// 修好项目（去掉仓库）后重试
	s.rest("PATCH", "/api/v1/projects/"+prj.ID, map[string]any{"repoUrl": ""}, nil, 200)
	s.rest("POST", "/api/v1/threads/"+id+"/wake", nil, nil, 202)
	a.until(60*time.Second, func(m wsMsg) bool {
		v, ok := m.upserted(id)
		return ok && v.State != "error" && v.Error == nil
	})
	done := s.waitThread(id, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	assert.Nil(t, done.Error)
	s.rest("GET", "/api/v1/orbs/"+th.OrbID, nil, &orb, 200)
	assert.Equal(t, "ready", orb.State)

	evs, err := s.srv.Store.Q.ListOrbEvents(context.Background(), th.OrbID)
	require.NoError(t, err)
	var seq []string
	for _, e := range evs {
		seq = append(seq, e.FromState+">"+e.ToState)
	}
	assert.Equal(t, []string{"setting_up>failed", "failed>waking", "setting_up>ready"}, subseq(seq, "setting_up>failed", "failed>waking", "setting_up>ready"))
}

// orb 内经 orbd.sock：手动重跑 setup 记为 trigger=manual 并推送 Orb.lastSetup；project.scripts.set 只改本 orb 的项目，
// 无项目的 orb 得到 no_project。（测试名保持简短：unix socket 路径有长度上限。）
func TestE2ESetupSock(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	ctx := context.Background()
	type project struct {
		ID          string  `json:"id"`
		SetupScript *string `json:"setupScript"`
	}
	var pa, pb project
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "a", "setupScript": "echo a-setup-ran"}, &pa, 201)
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "b", "setupScript": "echo b-setup"}, &pb, 201)
	newThread := func(projectID string) threadView {
		body := map[string]any{"prompt": "hi", "executor": map[string]any{"kind": "new_orb", "provider": "local", "type": "small"}}
		if projectID != "" {
			body["projectId"] = projectID
		}
		var created struct {
			Thread threadView `json:"thread"`
		}
		s.rest("POST", "/api/v1/threads", body, &created, 201)
		return s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })
	}
	th := newThread(pa.ID)
	sock := orbsock.Path(s.drv.HomeDir(th.OrbID))
	a := s.dial()

	var logs strings.Builder
	raw, err := orbsock.Call(ctx, sock, orbsock.MethodSetupRerun, orbsock.RerunParams{}, func(_, data string) { logs.WriteString(data) })
	require.NoError(t, err)
	var res struct {
		Ok      bool   `json:"ok"`
		Trigger string `json:"trigger"`
	}
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.True(t, res.Ok)
	assert.Equal(t, "manual", res.Trigger)
	assert.Contains(t, logs.String(), "a-setup-ran")

	// WS 推送带新 lastSetup 的 orb，REST 同样可见
	a.until(30*time.Second, func(m wsMsg) bool {
		var o struct {
			ID        string `json:"id"`
			LastSetup *struct {
				Trigger string `json:"trigger"`
			} `json:"lastSetup"`
		}
		return m.Type == "orb" && json.Unmarshal(m.Orb, &o) == nil && o.ID == th.OrbID && o.LastSetup != nil && o.LastSetup.Trigger == "manual"
	})
	var runs struct {
		Runs []struct {
			ID      string `json:"id"`
			Phase   string `json:"phase"`
			Trigger string `json:"trigger"`
			Ok      bool   `json:"ok"`
		} `json:"runs"`
	}
	s.rest("GET", "/api/v1/orbs/"+th.OrbID+"/setup-runs", nil, &runs, 200)
	require.NotEmpty(t, runs.Runs)
	assert.Equal(t, "manual", runs.Runs[0].Trigger)
	assert.Equal(t, "setup", runs.Runs[0].Phase)
	assert.True(t, runs.Runs[0].Ok)

	// project.scripts.set 只作用于本 orb 的项目
	raw, err = orbsock.Call(ctx, sock, orbsock.MethodProjectScriptsSet, map[string]any{"setup": "echo a-v2"}, nil)
	require.NoError(t, err)
	var scripts struct {
		ProjectID string  `json:"projectId"`
		Setup     *string `json:"setup"`
	}
	require.NoError(t, json.Unmarshal(raw, &scripts))
	assert.Equal(t, pa.ID, scripts.ProjectID)
	var got project
	s.rest("GET", "/api/v1/projects/"+pa.ID, nil, &got, 200)
	require.NotNil(t, got.SetupScript)
	assert.Equal(t, "echo a-v2", *got.SetupScript)
	got = project{}
	s.rest("GET", "/api/v1/projects/"+pb.ID, nil, &got, 200)
	require.NotNil(t, got.SetupScript)
	assert.Equal(t, "echo b-setup", *got.SetupScript, "其它项目不受影响")

	// 无项目的 orb（process 驱动下各 orbd 共用 egress 端口，先删掉第一台）
	s.rest("DELETE", "/api/v1/orbs/"+th.OrbID, nil, nil, 202)
	require.Eventually(t, func() bool {
		var o struct {
			State string `json:"state"`
		}
		s.rest("GET", "/api/v1/orbs/"+th.OrbID, nil, &o, 200)
		return o.State == "deleted"
	}, 30*time.Second, 100*time.Millisecond)
	free := newThread("")
	_, err = orbsock.Call(ctx, orbsock.Path(s.drv.HomeDir(free.OrbID)), orbsock.MethodProjectScriptsGet, nil, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeNoProject, se.Code)
}
