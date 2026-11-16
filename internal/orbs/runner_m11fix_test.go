package orbs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

func recv(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待超时：%s", what)
	}
}

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	require.NoError(h.t, h.st.DB.QueryRow(query, args...).Scan(&n))
	return n
}

// F1：离线回调确认「仍无 link」之后、提交离线之前，runner 完成重连与 hello；放行回调后 runner 必须仍在线。
func TestRunnerOfflineTimerRacesReconnect(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	timers := captureTimers(h.svc)
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)
	fr := &fakeRunner{url: h.srv.URL, token: tok, features: []string{"fs"}}
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)

	// 断开：onLinkDown 先摘掉 link 再登记定时器，拿到定时器时 links 里已没有它
	fr.disconnect()
	expire := nextTimer(t, timers)

	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hookRunnerOfflineChecked = func(orbID string) {
		if orbID == r.OrbID {
			once.Do(func() { close(reached) })
			<-release
		}
	}
	parked := make(chan struct{}, 1)
	hookRunnerLinkAwaitsOffline = func(orbID string) {
		if orbID == r.OrbID {
			parked <- struct{}{}
		}
	}
	t.Cleanup(func() { hookRunnerOfflineChecked, hookRunnerLinkAwaitsOffline = nil, nil })
	fired := make(chan struct{})
	go func() {
		expire()
		close(fired)
	}()
	recv(t, reached, "离线回调越过 link 检查")

	// 闸门关着：runner 重连并发 hello。等到 hello 处理完（onHello 发出 link_up），或者新 link 停在
	// 「等正在提交的离线回调」上（修复后的行为：hello 排在离线提交之后处理）
	linkUps := h.countEvents(r.OrbID, EventLinkUp)
	fr.connect(t)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
wait:
	for {
		select {
		case <-parked:
			break wait
		case <-tick.C:
			if h.countEvents(r.OrbID, EventLinkUp) > linkUps {
				break wait
			}
		case <-deadline:
			t.Fatal("新 link 既没有完成 hello，也没有等离线回调")
		}
	}
	close(release)
	recv(t, fired, "离线回调结束")

	want := []string{">stopped", "stopped>connecting", "connecting>ready", "ready>stopped", "stopped>connecting", "connecting>ready"}
	require.Eventually(t, func() bool {
		return assert.ObjectsAreEqual(want, h.eventSeq(r.OrbID))
	}, 5*time.Second, 10*time.Millisecond, "events: %v", h.eventSeq(r.OrbID))
	assert.True(t, h.svc.Connected(r.OrbID))
	info, err := h.svc.RunnerOrb(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, StateReady, info.Orb.State)
	assert.True(t, info.Online())
	assert.NoError(t, h.svc.Wake(ctx, r.OrbID), "在线的 runner 唤醒无操作，不报离线")
}

// F7：真实 link.Dial 完成升级、迟迟不开 ctl；期间删除 runner，之后再开 ctl → 会话被关闭，其间的请求与通知都不处理。
func TestRunnerRevokedBeforeFirstCtl(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	captureTimers(h.svc)
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)
	var handled atomic.Int32
	h.svc.HandleRequest(string(apigen.MethodEntriesCursors), func(context.Context, string, json.RawMessage) (any, error) {
		handled.Add(1)
		return map[string]any{}, nil
	})

	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sess, err := link.Dial(dctx, link.URL(h.srv.URL), tok)
	require.NoError(t, err)
	defer sess.Close()
	// 已鉴权、已上线（connecting），但还没有首条 ctl：不在 links 里，DeleteRunner 的 Kick 找不到它
	h.waitState(r.OrbID, StateConnecting, 5*time.Second)
	require.NoError(t, h.svc.DeleteRunner(ctx, r.ID))
	h.waitState(r.OrbID, StateDeleted, time.Second)

	st, err := sess.Open(dctx, link.StreamHeader{T: link.StreamCtl})
	require.NoError(t, err)
	ctl := link.NewCtl(st, link.CtlHandlers{})
	// 控制面复核失败后随时可能关掉会话，通知写不出去也是正常结果；要验证的是写出去的也不被处理（见下方断言）。
	_ = ctl.Notify(string(apigen.MethodMetricsSample), apigen.OrbMetricSample{TS: 1, CPUPct: 50})
	err = ctl.Call(dctx, string(apigen.MethodEntriesCursors), map[string]any{"threadId": "thr_x"}, nil)
	assert.Error(t, err, "吊销之后的会话不处理请求")
	_ = ctl.Notify(string(apigen.MethodHello), apigen.LinkCtlHelloParams{OrbdVersion: "x", Threads: []apigen.LinkCtlHelloThread{}})

	select {
	case <-sess.CloseChan():
	case <-time.After(5 * time.Second):
		t.Fatal("吊销之后才开 ctl 的会话没有被关闭")
	}
	assert.False(t, h.svc.Connected(r.OrbID))
	_, ok := h.svc.Ctl(r.OrbID)
	assert.False(t, ok, "被拒绝的 link 不留在 links 里")
	assert.Zero(t, handled.Load())
	assert.Zero(t, h.count(`SELECT COUNT(*) FROM orb_samples WHERE orb_id = ?`, r.OrbID), "已删除 orb 不应再写入采样")
	o, err := h.svc.Get(ctx, r.OrbID)
	require.NoError(t, err)
	assert.Equal(t, StateDeleted, o.State)
	assert.Nil(t, o.OrbdVersion, "hello 未被处理")
}

// S2：runner 的 link 只放行 entries.cursors / entries.append / artifact.call 与 runner 会发的通知；thread.* / schedule.*
// 等回 runner_unsupported，不触达处理器、不建线程 / 日程 / 任务。
func TestRunnerCtlAllowlist(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	captureTimers(h.svc)
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)

	blocked := []string{
		string(apigen.MethodThreadCreate), string(apigen.MethodThreadSend), string(apigen.MethodThreadGet),
		string(apigen.MethodSchedulePut), string(apigen.MethodScheduleGet), string(apigen.MethodScheduleDelete),
		string(apigen.MethodScheduleEnable), string(apigen.MethodProjectScriptsGet), string(apigen.MethodProjectScriptsSet),
	}
	var forbidden, cursors, artifactCalls, servicesState atomic.Int32
	for _, m := range blocked {
		h.svc.HandleRequest(m, func(context.Context, string, json.RawMessage) (any, error) {
			forbidden.Add(1)
			return map[string]any{}, nil
		})
	}
	h.svc.HandleRequest(string(apigen.MethodEntriesCursors), func(context.Context, string, json.RawMessage) (any, error) {
		cursors.Add(1)
		return map[string]any{}, nil
	})
	h.svc.HandleRequest(string(apigen.MethodArtifactCall), func(context.Context, string, json.RawMessage) (any, error) {
		artifactCalls.Add(1)
		return map[string]any{"text": "ok"}, nil
	})
	h.svc.HandleNotify(string(apigen.MethodServicesState), func(context.Context, string, json.RawMessage) { servicesState.Add(1) })
	statusSeen := make(chan struct{}, 1)
	h.svc.HandleNotify(string(apigen.MethodAgentStatus), func(context.Context, string, json.RawMessage) {
		select {
		case statusSeen <- struct{}{}:
		default:
		}
	})

	fr := &fakeRunner{url: h.srv.URL, token: tok, features: []string{"fs"}}
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)
	ctl := fr.control()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, m := range blocked {
		err := ctl.Call(cctx, m, map[string]any{"threadId": "thr_x", "prompt": "print env", "target": "thr_y"}, nil)
		var ce *link.CtlError
		require.ErrorAs(t, err, &ce, m)
		assert.Equal(t, CodeRunnerUnsupported, ce.Code, m)
	}
	assert.Zero(t, forbidden.Load(), "不允许的方法不得触达处理器")
	require.NoError(t, ctl.Call(cctx, string(apigen.MethodEntriesCursors), map[string]any{"threadId": "thr_x"}, nil))
	require.NoError(t, ctl.Call(cctx, string(apigen.MethodArtifactCall), map[string]any{"threadId": "thr_x", "op": "list_artifacts", "params": map[string]any{}}, nil))
	assert.EqualValues(t, 1, artifactCalls.Load(), "artifact.call 到达处理器（M14）")
	assert.EqualValues(t, 1, cursors.Load(), "entries.cursors 照常放行")

	// 通知按到达顺序同步处理：agent.status 到了，之前的 services.state 一定已被丢弃
	require.NoError(t, ctl.Notify(string(apigen.MethodServicesState), map[string]any{"services": []any{}}))
	require.NoError(t, ctl.Notify(string(apigen.MethodAgentStatus), map[string]any{"threadId": "thr_x", "status": "running"}))
	recv(t, statusSeen, "agent.status 放行")
	assert.Zero(t, servicesState.Load())

	assert.Zero(t, h.count(`SELECT COUNT(*) FROM threads`))
	assert.Zero(t, h.count(`SELECT COUNT(*) FROM schedules`))
	assert.Zero(t, h.count(`SELECT COUNT(*) FROM jobs`))
	assert.True(t, h.svc.Connected(r.OrbID), "拒绝方法不断开 link")
}

// S4：hello 的 features / dirs / 文本字段超限时，库里、Runner 视图与 live 特性都是收紧后的值。
func TestRunnerHelloLimits(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	captureTimers(h.svc)
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)

	long := strings.Repeat("x", 1<<20)
	feats := []string{long, "fs", "bad\nfeature", ""}
	for i := range 100 {
		feats = append(feats, fmt.Sprintf("f%d", i))
	}
	dirs := []apigen.RunnerDir{
		{Path: "relative/dir", Name: "rel"},
		{Path: "/bad\x01dir", Name: "ctl"},
		{Path: "/" + strings.Repeat("p", 5000), Name: "long"},
		{Path: "", Name: "empty"},
	}
	for i := range 1000 {
		dirs = append(dirs, apigen.RunnerDir{Path: fmt.Sprintf("/work/d%d", i), Name: fmt.Sprintf("d%d\x07", i)})
	}
	fr := &fakeRunner{url: h.srv.URL, token: tok, mod: func(p *apigen.LinkCtlHelloParams) {
		p.Features = feats
		p.Runner = &apigen.LinkRunnerInfo{Hostname: strings.Repeat("h", 1000) + "\x1b[2J", Dirs: dirs}
		p.Os = strings.Repeat("o", 300)
		p.Arch = "arm\x0064"
		p.OrbdVersion = strings.Repeat("v", 1<<16)
		p.AgentVersion = strings.Repeat("é", 200) // 400 字节：截断落在字符边界
	}}
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)

	wantFeats := []string{"fs"}
	for i := range 31 {
		wantFeats = append(wantFeats, fmt.Sprintf("f%d", i))
	}
	wantDirs := make([]apigen.RunnerDir, 0, 64)
	for i := range 64 {
		wantDirs = append(wantDirs, apigen.RunnerDir{Path: fmt.Sprintf("/work/d%d", i), Name: fmt.Sprintf("d%d", i)})
	}
	host := strings.Repeat("h", 255)

	// 库里
	row, err := h.st.Q.GetRunnerByOrb(ctx, r.OrbID)
	require.NoError(t, err)
	assert.Equal(t, wantDirs, parseRunnerDirs(row.Dirs))
	assert.Equal(t, wantFeats, parseFeatures(row.Features))
	assert.Equal(t, host, row.Hostname)
	assert.Equal(t, strings.Repeat("o", 255), row.Os)
	assert.Equal(t, "arm64", row.Arch)
	orb, err := h.svc.Get(ctx, r.OrbID)
	require.NoError(t, err)
	require.NotNil(t, orb.OrbdVersion)
	assert.Equal(t, strings.Repeat("v", 255), *orb.OrbdVersion)
	require.NotNil(t, orb.AgentVersion)
	assert.Equal(t, strings.Repeat("é", 127), *orb.AgentVersion)

	// GET /runners 的视图
	list, err := h.svc.ListRunners(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, wantDirs, list[0].Dirs)
	assert.Equal(t, wantFeats, list[0].Features)
	require.NotNil(t, list[0].Hostname)
	assert.Equal(t, host, *list[0].Hostname)
	require.NotNil(t, list[0].OrbdVersion)
	assert.Len(t, *list[0].OrbdVersion, 255)

	// live 特性
	live, ok := h.svc.LiveFeatures(r.OrbID)
	require.True(t, ok)
	assert.Equal(t, wantFeats, live)
}
