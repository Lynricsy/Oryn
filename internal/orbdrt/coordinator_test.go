package orbdrt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// startCoordinator 以协调宿主模式起一个运行时：link 走 net.Pipe（第一次 Connect 给出会话，之后 ErrNoRedial），
// 对端由 fakeCP.serve 服务。返回运行时、假控制面与 Run 结束的信号。
func startCoordinator(t *testing.T) (*Runtime, *fakeCP, <-chan struct{}, *atomic.Int32) {
	t.Helper()
	cp := newFakeCP(t)
	a, b := net.Pipe()
	var dials atomic.Int32
	go func() {
		sess, err := link.NewConnSession(b, false)
		if err != nil {
			return
		}
		cp.serve(sess)
	}()
	rt, err := New(Config{
		Coordinator:   true,
		OrbID:         "orb-coord",
		Home:          t.TempDir(),
		AgentBin:      fakeompBin,
		EgressAddr:    "127.0.0.1:0",
		Version:       "test-coord",
		PollInterval:  200 * time.Millisecond,
		RestartDelays: []time.Duration{50 * time.Millisecond},
		InitTimeout:   20 * time.Second,
		Connect: func(context.Context) (*link.Session, error) {
			if dials.Add(1) > 1 {
				return nil, ErrNoRedial
			}
			return link.NewConnSession(a, true)
		},
	})
	require.NoError(t, err)
	assert.Nil(t, rt.desk)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rt.Run(ctx)
	}()
	t.Cleanup(func() {
		rt.stopAllAgents(5 * time.Second)
		cancel()
		<-done
	})
	return rt, cp, done, &dials
}

func ctlCode(err error) string {
	var ce *link.CtlError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// H3：hello 的特性恰为 ["coordinator"]；不在白名单里的 ctl 方法回 method_not_found；不写 orb 扩展与技能。
func TestCoordinatorHelloAndRejectedMethods(t *testing.T) {
	rt, cp, _, _ := startCoordinator(t)
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	var hello apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &hello))
	assert.Equal(t, []string{"coordinator"}, hello.Features)
	assert.Equal(t, "test-coord", hello.OrbdVersion)

	for _, m := range []string{"blob.fetch", "fs.copyTree", "fs.move", string(apigen.MethodPortalConfigure), string(apigen.MethodProfileMode)} {
		err := cp.call(t, m, map[string]any{}, nil)
		assert.Equal(t, link.CodeMethodNotFound, ctlCode(err), "%s: %v", m, err)
	}
	_, err := os.Stat(rt.extensionPath())
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(orbsock.Path(rt.home))
	assert.ErrorIs(t, err, os.ErrNotExist, "no local control socket on the coordinator host")
}

// H3：setup.run 带仓库 / 脚本 / git / env 被拒；只带 ompHome 时文件落盘、不跑任何东西、不发 setup.result。
func TestCoordinatorSetup(t *testing.T) {
	rt, cp, _, _ := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	script := "touch /tmp/should-not-run"
	for name, p := range map[string]apigen.LinkCtlSetupRunParams{
		"script":   {Scripts: apigen.LinkCtlSetupScripts{Setup: &script}},
		"resume":   {Scripts: apigen.LinkCtlSetupScripts{Resume: &script}},
		"repo":     {Repo: &apigen.LinkCtlSetupRepo{URL: "https://example.com/r.git"}},
		"gitHosts": {GitHosts: []string{"github.com"}},
		"env":      {Env: map[string]string{"A": "b"}},
		"identity": {GitIdentity: &apigen.GitIdentity{Name: "n", Email: "e@x"}},
	} {
		err := cp.call(t, "setup.run", p, nil)
		assert.Equal(t, link.CodeInvalidParams, ctlCode(err), "%s: %v", name, err)
	}
	models := "providers: {}\n"
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{
		OmpHome: []apigen.LinkCtlOmpHomeFile{{Path: "models.yml", Mode: 0o600, ContentB64: base64.StdEncoding.EncodeToString([]byte(models))}},
	}, &res))
	assert.True(t, res.Ok)
	assert.False(t, res.SetupRan)
	b, err := os.ReadFile(filepath.Join(rt.home, ".omp", "agent", "models.yml"))
	require.NoError(t, err)
	assert.Equal(t, models, string(b))
	// 第二次不再带它：按清单删除。
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{}, &res))
	_, err = os.Stat(filepath.Join(rt.home, ".omp", "agent", "models.yml"))
	assert.ErrorIs(t, err, os.ErrNotExist)

	cp.mu.Lock()
	defer cp.mu.Unlock()
	for _, n := range cp.notifs {
		assert.NotEqual(t, string(apigen.MethodSetupResult), n.method)
		assert.NotEqual(t, "setup.log", n.method)
	}
	_, err = os.Stat(filepath.Join(rt.home, ".cache", "oryn", "logs", "setup.log"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// H3：agent.start 的协调档——参数恰为约定的列表，环境从零构造，覆盖层原文落盘；非法 profile / systemPrompt / env 被拒。
func TestCoordinatorAgentStart(t *testing.T) {
	rt, cp, _, _ := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	const thread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	coord := apigen.AgentProfileCoordinator
	oneLine := "You are the coordinator."
	dump := filepath.Join(rt.home, "dump.json")
	prompt := "#dump " + dump + "\nYou are the Oryn coordinator.\nKeep replies short."
	base := apigen.LinkCtlAgentStartParams{ThreadID: thread, SessionDir: "~/.oryn/threads/" + thread}

	bad := map[string]apigen.LinkCtlAgentStartParams{}
	p := base
	p.SystemPrompt = &prompt
	bad["no profile"] = p
	p = base
	p.Profile = &coord
	bad["no prompt"] = p
	p.SystemPrompt = &oneLine
	bad["one-line prompt"] = p
	p.SystemPrompt = &prompt
	p.Env = map[string]string{"SECRET": "x"}
	bad["env"] = p
	for name, p := range bad {
		err := cp.call(t, "agent.start", p, nil)
		assert.Equal(t, link.CodeInvalidParams, ctlCode(err), "%s: %v", name, err)
	}

	p = base
	p.Profile, p.SystemPrompt = &coord, &prompt
	p.InitFrames = []apigen.OmpFrame{
		{"type": "open_session", "sessionDir": "~/.oryn/threads/" + thread},
		{"type": "set_event_filter", "events": nil, "messageUpdates": "delta"},
		{"type": "set_host_tools", "tools": []any{map[string]any{"name": "create_thread", "description": "d", "parameters": map[string]any{}}}},
	}
	var res apigen.LinkCtlAgentStartResult
	require.NoError(t, cp.call(t, "agent.start", p, &res))
	require.Positive(t, res.Pid)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread, "ready"))

	var rec struct {
		Args []string          `json:"args"`
		Env  map[string]string `json:"env"`
		Cwd  string            `json:"cwd"`
	}
	b, err := os.ReadFile(dump)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &rec))
	cwd := filepath.Join(rt.home, ".oryn", "coordinator", "cwd")
	overlay := filepath.Join(rt.home, ".oryn", "coordinator", "overlay.yml")
	assert.Equal(t, []string{
		"--mode", "rpc-ui", "--cwd", cwd, "--session-dir", filepath.Join(rt.home, ".oryn", "threads", thread),
		"--no-tools", "--no-skills", "--no-extensions", "--no-rules", "--no-context-files", "--no-lsp",
		"--config", overlay, "--system-prompt", prompt,
	}, rec.Args)
	assert.NotContains(t, rec.Args, "-e")
	assert.Equal(t, cwd, rec.Cwd)
	assert.Equal(t, map[string]string{
		"HOME": rt.home, "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "TMPDIR": filepath.Join(rt.home, "tmp"),
		"PI_RPC_EMIT_TITLE": "1", "PI_NOTIFICATIONS": "off", "OTEL_SDK_DISABLED": "true", "ORYN_THREAD_ID": thread,
	}, rec.Env)
	ov, err := os.ReadFile(overlay)
	require.NoError(t, err)
	assert.Equal(t, coordinatorOverlay, string(ov))
	assert.Contains(t, string(ov), `autoUpdate: "off"`)
	entries, err := os.ReadDir(cwd)
	require.NoError(t, err)
	assert.Empty(t, entries, "the coordinator cwd stays empty")
}

// H3：link 断开后 Connect 返回 ErrNoRedial，Run 随即返回（不重连）。
func TestCoordinatorRunReturnsWhenLinkCloses(t *testing.T) {
	_, cp, done, dials := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	cp.killLink()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after the link closed")
	}
	assert.Equal(t, int32(2), dials.Load(), "one dial plus one refused redial")
}

// 普通 orb：hello 带 "threads"、不带 "coordinator"；协调档的 agent.start 被拒。
func TestOrdinaryOrbRejectsCoordinatorProfile(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	var hello apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &hello))
	assert.Contains(t, hello.Features, "threads")
	assert.NotContains(t, hello.Features, "coordinator")

	coord := apigen.AgentProfileCoordinator
	prompt := "a\nb"
	err := cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab", Profile: &coord, SystemPrompt: &prompt,
	}, nil)
	assert.Equal(t, link.CodeInvalidParams, ctlCode(err), "%v", err)
}

// v1 4.1：orbd.sock 的七个转发方法按 ctl 原样往返，控制面错误码原样映射。
func TestSockThreadMethodsForward(t *testing.T) {
	cp := newFakeCP(t)
	type call struct {
		method string
		params map[string]any
	}
	calls := make(chan call, 16)
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if !strings.HasPrefix(method, "thread.") && !strings.HasPrefix(method, "schedule.") {
			return nil, false, nil
		}
		var m map[string]any
		_ = json.Unmarshal(params, &m)
		calls <- call{method, m}
		if m["target"] == "T-forbidden" {
			return nil, true, &link.CtlError{Code: "forbidden", Message: "not a relative"}
		}
		return map[string]any{"echo": method}, true, nil
	}
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	sock := orbsock.Path(rt.home)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitSock(t, sock)

	for _, tc := range []struct{ sock, ctl string }{
		{orbsock.MethodThreadNew, "thread.create"},
		{orbsock.MethodThreadSend, "thread.send"},
		{orbsock.MethodThreadGet, "thread.get"},
		{orbsock.MethodScheduleGet, "schedule.get"},
		{orbsock.MethodScheduleSet, "schedule.put"},
		{orbsock.MethodScheduleDelete, "schedule.delete"},
		{orbsock.MethodScheduleEnable, "schedule.enable"},
	} {
		raw, err := orbsock.Call(ctx, sock, tc.sock, map[string]any{"threadId": "T-a", "target": "T-b", "prompt": "p", "message": "m"}, nil)
		require.NoError(t, err, tc.sock)
		assert.JSONEq(t, `{"echo":"`+tc.ctl+`"}`, string(raw))
		c := <-calls
		assert.Equal(t, tc.ctl, c.method)
		assert.Equal(t, "T-a", c.params["threadId"])
	}
	_, err := orbsock.Call(ctx, sock, orbsock.MethodThreadSend, map[string]any{"threadId": "T-a", "target": "T-forbidden", "message": "m"}, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "forbidden", se.Code)
	<-calls
}

// v1 4.1：thread.wait 在第三次轮询 settled 后返回；到时回 timeout；调用方断开即停止轮询。
func TestSockThreadWait(t *testing.T) {
	cp := newFakeCP(t)
	var polls atomic.Int32
	settleAt := atomic.Int32{}
	settleAt.Store(3)
	polled := make(chan int32, 64)
	cp.extra = func(method string, _ json.RawMessage) (any, bool, error) {
		if method != "thread.get" {
			return nil, false, nil
		}
		n := polls.Add(1)
		select {
		case polled <- n:
		default:
		}
		settled := n >= settleAt.Load()
		state := apigen.ThreadState("working")
		if settled {
			state = "idle"
		}
		return apigen.LinkCtlThreadInfo{ID: "T-b", State: state, Settled: settled, Title: "t", URL: "u", LastAssistantText: new("done")}, true, nil
	}
	rt := startRuntime(t, cp, func(c *Config) { c.ThreadPollInterval = 10 * time.Millisecond })
	cp.waitNotif(t, 0, "hello", nil)
	sock := orbsock.Path(rt.home)
	waitSock(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	raw, err := orbsock.Call(ctx, sock, orbsock.MethodThreadWait, orbsock.ThreadWaitParams{ThreadID: "T-a", Target: "T-b"}, nil)
	require.NoError(t, err)
	var info apigen.LinkCtlThreadInfo
	require.NoError(t, json.Unmarshal(raw, &info))
	assert.True(t, info.Settled)
	assert.Equal(t, "done", *info.LastAssistantText)
	assert.Equal(t, int32(3), polls.Load())
	drain(polled)

	// 超时：永不静止，1 秒上限。
	settleAt.Store(1 << 30)
	_, err = orbsock.Call(ctx, sock, orbsock.MethodThreadWait, orbsock.ThreadWaitParams{ThreadID: "T-a", Target: "T-b", TimeoutSeconds: 1}, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeTimeout, se.Code)
	drain(polled)

	// 调用方断开：取消后不再有新的轮询（等到观察到一次轮询再断开，然后确认轮询停止）。
	cctx, ccancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, err := orbsock.Call(cctx, sock, orbsock.MethodThreadWait, orbsock.ThreadWaitParams{ThreadID: "T-a", Target: "T-b"}, nil)
		errc <- err
	}()
	<-polled
	ccancel()
	require.Error(t, <-errc)
	// 服务端在下一次计时器到点前发现 ctx 取消；给它几个轮询周期的余量后计数不再增长。
	stable := polls.Load()
	require.Eventually(t, func() bool {
		cur := polls.Load()
		if cur != stable {
			stable = cur
			return false
		}
		return true
	}, 5*time.Second, 100*time.Millisecond)
}

func drain(ch chan int32) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func waitSock(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "orbd.sock not created")
}
