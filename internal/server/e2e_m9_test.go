package server

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

var m9Idle = config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)}

// newM9Stack 起一套带真实协调宿主的栈：协调宿主是 orynd coordinator-host 子进程（沙箱内），omp 换成 fakeomp。
// 沙箱条件不满足时跳过，除非设了 ORYN_REQUIRE_SANDBOX=1（CI）。返回栈与协调宿主目录。
func newM9Stack(t *testing.T) (*stack, string) {
	t.Helper()
	// 以 root 运行时协调宿主降权：目录及每级父目录都要对降权用户可达（t.TempDir 的父目录是 0700）
	dir, err := os.MkdirTemp("", "oryn-e2e-coord-*")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	coordhostCommand = []string{filepath.Join(binDir, "orynd"), "coordinator-host"}
	t.Cleanup(func() { coordhostCommand = nil })
	no := false
	s := newStack(t, m9Idle, func(c *config.Orynd) {
		c.Coordinator.OmpPath = filepath.Join(binDir, "fakeomp")
		c.Coordinator.Dir = dir
		c.Coordinator.VerifyOmp = &no
		// 没有网络命名空间的环境（如 CI runner）里代理端口落在宿主回环上：用空闲端口，避免与并行测试或本机服务撞上 7811
		c.Coordinator.EgressPort = freeTCPPort(t)
	})
	if err := s.srv.Host.Check(context.Background()); err != nil {
		if os.Getenv("ORYN_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("协调宿主不可用（ORYN_REQUIRE_SANDBOX=1）：%v", err)
		}
		t.Skipf("协调宿主不可用：%v", err)
	}
	return s, dir
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// schedClock 是注入定时服务（与协调层）的假时钟：定时器按「登记时刻 + 时长」排队，Advance 拨动时钟并触发到期的。
type schedClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*schedTimer
}

type schedTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (c *schedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *schedClock) AfterFunc(d time.Duration, f func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &schedTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, tm)
	return func() {
		c.mu.Lock()
		tm.stopped = true
		c.mu.Unlock()
	}
}

// Advance 把时钟前拨 d，并在锁外调用全部到期且未停止的定时器。
func (c *schedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	rest := c.timers[:0]
	for _, tm := range c.timers {
		switch {
		case tm.stopped:
		case !tm.at.After(c.now):
			due = append(due, tm.f)
		default:
			rest = append(rest, tm)
		}
	}
	c.timers = rest
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// useSchedClock 让之后组装的 Server 使用假时钟（须在 newStack / newM9Stack 之前调用）。
func useSchedClock(t *testing.T) *schedClock {
	t.Helper()
	c := &schedClock{now: time.Now()}
	scheduleNow, scheduleAfterFunc = c.Now, c.AfterFunc
	t.Cleanup(func() { scheduleNow, scheduleAfterFunc = nil, nil })
	return c
}

// lastAssistant 返回线程活动分支上最后一条 assistant 文本。
func lastAssistant(t *testing.T, s *stack, threadID string) string {
	t.Helper()
	rows, err := s.srv.Store.Q.ListEntriesAfter(context.Background(), db.ListEntriesAfterParams{ThreadID: threadID, AfterSeq: 0, Limit: 1000})
	require.NoError(t, err)
	out := ""
	for _, e := range rows {
		var p struct {
			Message *struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if e.Type != "message" || json.Unmarshal(e.Payload, &p) != nil || p.Message == nil || p.Message.Role != "assistant" {
			continue
		}
		var blocks []struct {
			Type, Text string
		}
		if json.Unmarshal(p.Message.Content, &blocks) == nil {
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" {
					parts = append(parts, b.Text)
				}
			}
			out = strings.Join(parts, "\n")
		}
	}
	return out
}

func entriesContain(t *testing.T, s *stack, threadID, needle string) bool {
	t.Helper()
	rows, err := s.srv.Store.Q.ListEntriesAfter(context.Background(), db.ListEntriesAfterParams{ThreadID: threadID, AfterSeq: 0, Limit: 1000})
	require.NoError(t, err)
	for _, e := range rows {
		if strings.Contains(string(e.Payload), needle) {
			return true
		}
	}
	return false
}

// waitEntry 等线程条目里出现 needle。
func (s *stack) waitEntry(threadID, needle string, timeout time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for !entriesContain(s.t, s, threadID, needle) {
		if time.Now().After(deadline) {
			s.t.Fatalf("线程 %s 没有出现 %q", threadID, needle)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// prompt 以主人身份向线程发一条消息（等价于 WS cmd）并等它回到 idle。调用方须保证此时没有别的回合（如回报）在途。
func (s *stack) prompt(threadID, text string) {
	s.t.Helper()
	frame, _ := json.Marshal(map[string]string{"type": "prompt", "message": text})
	before := lastAssistant(s.t, s, threadID)
	require.NoError(s.t, s.srv.Threads.Command(context.Background(), threadID, frame, func(json.RawMessage) {}))
	deadline := time.Now().Add(60 * time.Second)
	for {
		var v threadView
		s.rest("GET", "/api/v1/threads/"+threadID, nil, &v, 200)
		if v.State == threads.StateIdle && lastAssistant(s.t, s, threadID) != before {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("线程 %s 没有回到 idle（%+v）", threadID, v)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *stack) idle(threadID string) threadView {
	s.t.Helper()
	return s.waitThread(threadID, 60*time.Second, func(v threadView) bool { return v.State == threads.StateIdle })
}

// waitOrbState 等 orb 进入 state。
func (s *stack) waitOrbState(orbID, state string, timeout time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		o, err := s.srv.Store.Q.GetOrb(context.Background(), orbID)
		require.NoError(s.t, err)
		if o.State == state {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("orb %s 没有进入 %s（当前 %s）", orbID, state, o.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *stack) orbEvents(orbID string) []string {
	s.t.Helper()
	evs, err := s.srv.Store.Q.ListOrbEvents(context.Background(), orbID)
	require.NoError(s.t, err)
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.FromState+">"+e.ToState)
	}
	return out
}

// M9 全链路：建 Space → 协调 orb（@host）ready、聊天线程 idle；协调档参数含 --no-tools、不含 -e、环境只有白名单；
// 沙箱里读密钥文件、执行 shell、连控制面、连 unix socket 全部失败；hosttool create_thread → 新线程进 Space、
// 父线程正确 → 静止后 Space 聊天收到回报；协调者对 Space 外线程 send_message 放行（无闸门），对协调对话
// invalid_target；审计可经 REST 查询。
func TestE2EM9Coordinator(t *testing.T) {
	s, coordDir := newM9Stack(t)

	var prj apigen.Project
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "alpha"}, &prj, 201)
	var sp apigen.Space
	s.rest("POST", "/api/v1/spaces", map[string]any{"name": "Alpha", "goal": "ship M9", "projectId": prj.ID}, &sp, 201)
	chat := s.idle(sp.ThreadID)
	var orb apigen.Orb
	s.rest("GET", "/api/v1/orbs/"+chat.OrbID, nil, &orb, 200)
	assert.Equal(t, apigen.OrbState("ready"), orb.State)
	require.NotNil(t, orb.Coordinator)
	assert.True(t, *orb.Coordinator)
	host, err := s.srv.Store.Q.GetProviderByName(context.Background(), "@host")
	require.NoError(t, err)
	assert.Equal(t, host.ID, orb.ProviderID)

	// @host 不出现在 provider 列表里，也不能用来建工作线程
	var provs apigen.ProviderList
	s.rest("GET", "/api/v1/providers", nil, &provs, 200)
	for _, p := range provs.Providers {
		assert.NotEqual(t, "@host", p.Name)
	}
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": map[string]any{"kind": "new_orb", "provider": "@host"}}, nil, 400)
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": map[string]any{"kind": "orb", "orbId": chat.OrbID}}, nil, 400)

	// 协调档的参数与环境（真实沙箱里的 fakeomp 自报）
	s.prompt(sp.ThreadID, "probe args")
	text := lastAssistant(t, s, sp.ThreadID)
	raw, ok := strings.CutPrefix(text, "probe:args:")
	require.True(t, ok, text)
	var probe struct {
		Args    []string `json:"args"`
		EnvKeys []string `json:"envKeys"`
		UID     int      `json:"uid"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &probe), raw)
	assert.Contains(t, probe.Args, "--no-tools")
	assert.NotContains(t, probe.Args, "-e")
	assert.Contains(t, probe.Args, "--system-prompt")
	assert.Contains(t, probe.Args, "--config")
	allowed := []string{"HOME", "PATH", "LANG", "TMPDIR", "PI_RPC_EMIT_TITLE", "PI_NOTIFICATIONS", "OTEL_SDK_DISABLED", "PI_FILE_LOCK_BACKEND", "ORYN_THREAD_ID"}
	for _, k := range probe.EnvKeys {
		assert.Contains(t, allowed, k, "协调 agent 的环境变量只有白名单")
	}
	assert.Contains(t, probe.EnvKeys, "ORYN_THREAD_ID")
	assert.Contains(t, probe.EnvKeys, "PI_FILE_LOCK_BACKEND", "omp 的原生文件锁走旁路文件（沙箱禁止 AF_UNIX socket）")
	if os.Getuid() == 0 {
		assert.Equal(t, 65534, probe.UID, "root 部署时协调宿主降权到 nobody")
	}

	// 沙箱：读密钥文件、执行 shell、连控制面、连 unix socket 全部失败；home 内写入成功（对照）
	masterKey := filepath.Join(s.cfg.DataDir, "master.key")
	_, err = os.Stat(masterKey)
	require.NoError(t, err)
	sockDir, err := os.MkdirTemp("", "oryn-e2e-sock-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	require.NoError(t, os.Chmod(sockDir, 0o755))
	sockPath := filepath.Join(sockDir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	require.NoError(t, os.Chmod(sockPath, 0o777))
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	ctlAddr := s.http.Listener.Addr().String()
	s.prompt(sp.ThreadID, strings.Join([]string{
		"probe read " + masterKey,
		"probe exec /bin/sh",
		"probe tcp " + ctlAddr,
		"probe unix " + sockPath,
		"probe write " + filepath.Join(coordDir, "home", "probe.txt"),
	}, "\n"))
	lines := strings.Split(lastAssistant(t, s, sp.ThreadID), "\n")
	require.Len(t, lines, 5, lines)
	for _, l := range lines[:4] {
		assert.False(t, strings.HasSuffix(l, ":ok"), "沙箱里必须失败：%s", l)
		assert.True(t, strings.HasPrefix(l, "probe:"), l)
	}
	assert.Equal(t, "probe:write:ok", lines[4], "home 内可写（证明探测本身可用）")

	// create_thread：新线程进 Space、父线程 = 聊天、项目缺省取 Space 的
	s.prompt(sp.ThreadID, `hosttool create_thread {"prompt":"hi"}`)
	require.True(t, strings.HasPrefix(lastAssistant(t, s, sp.ThreadID), "host:create_thread:ok:"), lastAssistant(t, s, sp.ThreadID))
	var list apigen.ThreadList
	s.rest("GET", "/api/v1/threads?space="+sp.ID, nil, &list, 200)
	require.Len(t, list.Threads, 1)
	child := list.Threads[0]
	require.NotNil(t, child.SpaceID)
	assert.Equal(t, sp.ID, *child.SpaceID)
	require.NotNil(t, child.ParentThreadID)
	assert.Equal(t, sp.ThreadID, *child.ParentThreadID)
	require.NotNil(t, child.ProjectID)
	assert.Equal(t, prj.ID, *child.ProjectID)
	s.idle(child.ID)
	s.waitEntry(sp.ThreadID, "[Oryn thread report · automatic] "+child.ID, 60*time.Second)
	s.idle(sp.ThreadID)

	// 无闸门：对 Space 外的线程 send_message 照样成功，目标收到带来源头行的消息，space_id 不变
	var outside apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "outside", "executor": map[string]any{"kind": "new_orb"}}, &outside, 201)
	s.idle(outside.Thread.ID)
	s.prompt(sp.ThreadID, `hosttool send_message {"threadId":"`+outside.Thread.ID+`","message":"please rebase"}`)
	assert.True(t, strings.HasPrefix(lastAssistant(t, s, sp.ThreadID), "host:send_message:ok:"), lastAssistant(t, s, sp.ThreadID))
	s.waitEntry(outside.Thread.ID, `[Oryn message from Space \"Alpha\" (`+sp.ThreadID+`)`, 60*time.Second)
	// 目标静止后回报投递给 Space 聊天：等它处理完，下一条主人消息才不会与回报的回合交错
	s.waitEntry(sp.ThreadID, "[Oryn thread report · automatic] "+outside.Thread.ID, 60*time.Second)
	s.idle(sp.ThreadID)
	var got apigen.Thread
	s.rest("GET", "/api/v1/threads/"+outside.Thread.ID, nil, &got, 200)
	assert.Nil(t, got.SpaceID)

	// 协调对话之间不能互相操作
	var asst apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &asst, 201)
	s.prompt(sp.ThreadID, `hosttool send_message {"threadId":"`+asst.Thread.ID+`","message":"loop"}`)
	assert.True(t, strings.HasPrefix(lastAssistant(t, s, sp.ThreadID), "host:send_message:error:invalid_target"), lastAssistant(t, s, sp.ThreadID))

	// 协调线程不可 fork / 分享
	s.rest("POST", "/api/v1/threads/"+sp.ThreadID+"/fork", map[string]any{"env": "share"}, nil, 400)

	// 审计
	var acts apigen.AutomationActionList
	s.rest("GET", "/api/v1/automation/actions?thread="+sp.ThreadID, nil, &acts, 200)
	require.Len(t, acts.Actions, 3)
	byAction := map[string]apigen.AutomationAction{}
	for _, a := range acts.Actions {
		byAction[string(a.Action)+"/"+map[bool]string{true: "ok", false: "fail"}[a.Ok]] = a
	}
	c := byAction["create_thread/ok"]
	assert.Equal(t, apigen.AutomationActionOrigin("owner"), c.Origin)
	assert.Equal(t, apigen.AutomationActionActorKind("space"), c.ActorKind)
	require.NotNil(t, c.TargetThreadID)
	assert.Equal(t, child.ID, *c.TargetThreadID)
	sm := byAction["send_message/ok"]
	require.NotNil(t, sm.TargetThreadID)
	assert.Equal(t, outside.Thread.ID, *sm.TargetThreadID)
	assert.Equal(t, apigen.AutomationActionOrigin("owner"), sm.Origin)
	f := byAction["send_message/fail"]
	require.NotNil(t, f.Error)
	assert.Equal(t, "invalid_target", *f.Error)

	// 进度图与删除
	var prog apigen.SpaceProgress
	s.rest("GET", "/api/v1/spaces/"+sp.ID+"/progress?days=7", nil, &prog, 200)
	require.Len(t, prog.Days, 7)
	assert.Equal(t, 1, prog.Days[6].Open)
	s.rest("DELETE", "/api/v1/threads/"+sp.ThreadID, nil, nil, 409)
	s.rest("DELETE", "/api/v1/spaces/"+sp.ID, nil, nil, 204)
	s.rest("GET", "/api/v1/threads/"+child.ID, nil, &got, 200)
	assert.Nil(t, got.SpaceID)
}

// coordHostPID 在 /proc 里找服务 orbID 的 coordinator-host 进程。
func coordHostPID(t *testing.T, orbID string) int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	require.NoError(t, err)
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if slices.Contains(args, "coordinator-host") && slices.Contains(args, orbID) {
			return pid
		}
	}
	return 0
}

// 杀掉 coordinator-host（kill -9）→ 协调 orb 经 stopping / stopped / waking 回到 ready，下一条提示词照常得到回复。
func TestE2EM9CoordinatorHostCrash(t *testing.T) {
	s, _ := newM9Stack(t)
	var asst apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &asst, 201)
	s.prompt(asst.Thread.ID, "hello")
	conv := s.idle(asst.Thread.ID)
	s.waitOrbState(conv.OrbID, "ready", 30*time.Second)

	pid := coordHostPID(t, conv.OrbID)
	require.NotZero(t, pid, "找不到 coordinator-host 进程")
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	deadline := time.Now().Add(60 * time.Second)
	want := []string{"ready>stopping", "stopping>stopped", "stopped>waking", "connecting>resuming", "resuming>ready"}
	for !slices.Equal(subseq(s.orbEvents(conv.OrbID), want...), want) {
		if time.Now().After(deadline) {
			t.Fatalf("协调 orb 没有恢复：%v", s.orbEvents(conv.OrbID))
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.waitOrbState(conv.OrbID, "ready", 30*time.Second)
	newPID := coordHostPID(t, conv.OrbID)
	assert.NotZero(t, newPID)
	assert.NotEqual(t, pid, newPID)

	s.prompt(asst.Thread.ID, "hello again")
	assert.NotEmpty(t, lastAssistant(t, s, asst.Thread.ID))
}

// 定时触发的回合（自动化，OwnerTurn = false）里助手照样能归档别的线程；审计行 origin = auto。
func TestE2EM9AssistantScheduledArchive(t *testing.T) {
	clk := useSchedClock(t)
	s, _ := newM9Stack(t)
	var target apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "work", "executor": map[string]any{"kind": "new_orb"}}, &target, 201)
	s.idle(target.Thread.ID)
	var asst apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &asst, 201)

	at := clk.Now().Add(2 * time.Minute).UnixMilli()
	var sc apigen.Schedule
	s.rest("PUT", "/api/v1/threads/"+asst.Thread.ID+"/schedule", map[string]any{
		"kind": "once", "at": at, "prompt": `hosttool archive_thread {"threadId":"` + target.Thread.ID + `"}`,
	}, &sc, 200)
	clk.Advance(2 * time.Minute)

	s.waitEntry(asst.Thread.ID, "[Oryn scheduled run", 60*time.Second)
	s.waitThread(target.Thread.ID, 60*time.Second, func(threadView) bool {
		row, err := s.srv.Store.Q.GetThread(context.Background(), target.Thread.ID)
		require.NoError(t, err)
		return row.ArchivedAt != nil
	})
	s.idle(asst.Thread.ID)
	assert.True(t, strings.HasPrefix(lastAssistant(t, s, asst.Thread.ID), "host:archive_thread:ok:"), lastAssistant(t, s, asst.Thread.ID))

	var acts apigen.AutomationActionList
	s.rest("GET", "/api/v1/automation/actions?thread="+target.Thread.ID, nil, &acts, 200)
	require.Len(t, acts.Actions, 1)
	a := acts.Actions[0]
	assert.Equal(t, apigen.AutomationActionOrigin("auto"), a.Origin)
	assert.Equal(t, apigen.AutomationActionActorKind("assistant"), a.ActorKind)
	assert.True(t, a.Ok)
	require.NotNil(t, a.ActorThreadID)
	assert.Equal(t, asst.Thread.ID, *a.ActorThreadID)
}

func (s *stack) scheduleRuns(id string) []db.ScheduleRun {
	s.t.Helper()
	runs, err := s.srv.Store.Q.ListScheduleRuns(context.Background(), db.ListScheduleRunsParams{ScheduleID: id, Limit: 100})
	require.NoError(s.t, err)
	return runs
}

// waitRun 等定时的第 n 次运行（按开始顺序）进入 status。
func (s *stack) waitRun(id string, n int, status string) db.ScheduleRun {
	s.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		runs := s.scheduleRuns(id)
		if len(runs) >= n {
			r := runs[len(runs)-n] // 新的在前
			if r.Status == status {
				return r
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("定时 %s 的第 %d 次运行没有进入 %s：%+v", id, n, status, runs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 线程内定时在冻结的普通 orb 上到点：orb 被唤醒、消息以自动化身份送达、运行 completed。
func TestE2EM9ThreadScheduleWakesFrozenOrb(t *testing.T) {
	clk := useSchedClock(t)
	s := newStack(t, m9Idle)
	var th apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "work", "executor": map[string]any{"kind": "new_orb"}}, &th, 201)
	v := s.idle(th.Thread.ID)
	s.rest("POST", "/api/v1/orbs/"+v.OrbID+"/pause", map[string]any{"mode": "freeze"}, nil, 202)
	s.waitOrbState(v.OrbID, "frozen", 30*time.Second)

	var sc apigen.Schedule
	s.rest("PUT", "/api/v1/threads/"+th.Thread.ID+"/schedule", map[string]any{"kind": "every", "every": "1h", "name": "hourly", "prompt": "check in"}, &sc, 200)
	clk.Advance(time.Hour)

	s.waitEntry(th.Thread.ID, `[Oryn scheduled run \"hourly\" #1`, 60*time.Second)
	run := s.waitRun(sc.ID, 1, "completed")
	require.NotNil(t, run.ThreadID)
	assert.Equal(t, th.Thread.ID, *run.ThreadID)
	assert.Equal(t, "schedule", run.Trigger)
	assert.Equal(t, []string{"ready>freezing", "freezing>frozen", "frozen>waking", "resuming>ready"},
		subseq(s.orbEvents(v.OrbID), "ready>freezing", "freezing>frozen", "frozen>waking", "resuming>ready"))
	row, err := s.srv.Store.Q.GetThread(context.Background(), th.Thread.ID)
	require.NoError(t, err)
	assert.True(t, row.Unread, "自动化回合结束后线程未读（无人聚焦）")
}

// 项目定时（每次新建线程）连续三次触发，keepLast = 1：三个带 schedule_id / schedule_run 的线程；较早且静止的被归档，
// 其独占的 orb 被删除；provider 指向不存在的名字 → 运行 failed 且产生一条没有线程的通知。
func TestE2EM9ProjectSchedule(t *testing.T) {
	clk := useSchedClock(t)
	s := newStack(t, m9Idle)
	ctx := context.Background()
	var prj apigen.Project
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "sandbox"}, &prj, 201)
	var sc apigen.Schedule
	s.rest("POST", "/api/v1/schedules", map[string]any{
		"mode": "new_thread", "projectId": prj.ID, "name": "tick", "kind": "every", "every": "1m", "keepLast": 1,
		"prompt": "only reply run {{run_number}}",
	}, &sc, 201)

	orbOf := map[int64]string{} // 删除 orb 后线程的 orb_id 置空，所以在运行完成时记下
	for n := 1; n <= 3; n++ {
		clk.Advance(time.Minute)
		run := s.waitRun(sc.ID, n, "completed")
		require.NotNil(t, run.ThreadID)
		th, err := s.srv.Store.Q.GetThread(ctx, *run.ThreadID)
		require.NoError(t, err)
		require.NotNil(t, th.OrbID)
		orbOf[int64(n)] = *th.OrbID
	}
	rows, err := s.srv.Store.Q.ListThreads(ctx, db.ListThreadsParams{ProjectID: &prj.ID})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	byRun := map[int64]db.Thread{}
	for _, r := range rows {
		require.NotNil(t, r.ScheduleID)
		assert.Equal(t, sc.ID, *r.ScheduleID)
		require.NotNil(t, r.ScheduleRun)
		byRun[*r.ScheduleRun] = r
		assert.True(t, strings.HasPrefix(r.Title, "tick · "), r.Title)
	}
	require.Len(t, byRun, 3)
	assert.True(t, entriesContain(t, s, byRun[3].ID, "only reply run 3"))
	for _, n := range []int64{1, 2} {
		r := byRun[n]
		assert.NotNil(t, r.ArchivedAt, "第 %d 次运行的线程已归档", n)
		s.waitOrbState(orbOf[n], "deleted", 30*time.Second)
	}
	assert.Nil(t, byRun[3].ArchivedAt)
	o, err := s.srv.Store.Q.GetOrb(ctx, orbOf[3])
	require.NoError(t, err)
	assert.Equal(t, "ready", o.State)

	// provider 不存在：运行 failed，通知没有线程
	var bad apigen.Schedule
	s.rest("POST", "/api/v1/schedules", map[string]any{
		"mode": "new_thread", "projectId": prj.ID, "name": "bad", "kind": "every", "every": "1h", "provider": "nope", "prompt": "x",
	}, &bad, 201)
	var run apigen.ScheduleRun
	s.rest("POST", "/api/v1/schedules/"+bad.ID+"/run", nil, &run, 200)
	assert.Equal(t, apigen.ScheduleRunStatusFailed, run.Status)
	require.NotNil(t, run.Error)
	assert.Contains(t, *run.Error, "nope")
	var notes apigen.NotificationList
	found := false
	deadline := time.Now().Add(10 * time.Second)
	for !found {
		s.rest("GET", "/api/v1/notifications", nil, &notes, 200)
		for _, n := range notes.Notifications {
			if strings.Contains(n.Title, "bad") && n.ThreadID == nil {
				found = true
				assert.Equal(t, apigen.NotificationKind("error"), n.Kind)
			}
		}
		if !found && time.Now().After(deadline) {
			t.Fatalf("没有没有线程的失败通知：%+v", notes.Notifications)
		}
		if !found {
			time.Sleep(50 * time.Millisecond)
		}
	}
	rows, err = s.srv.Store.Q.ListThreads(ctx, db.ListThreadsParams{ProjectID: &prj.ID})
	require.NoError(t, err)
	assert.Len(t, rows, 3, "失败的运行不建线程")
}
