package orbs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
)

// fakeRunner 是 runner 形态 orbd 的替身：用 ort_ 令牌拨 link、hello 带 runner 信息，记下收到的 ctl 请求。
// mod 非 nil 时在发送前改写 hello；onReq 非 nil 时由它应答控制面的 ctl 请求（否则一律回 not_supported）。
type fakeRunner struct {
	url, token string
	features   []string
	mod        func(*apigen.LinkCtlHelloParams)
	onReq      func(method string, params json.RawMessage) (any, error)

	mu    sync.Mutex
	sess  *link.Session
	ctl   *link.Ctl
	calls []string
}

func (r *fakeRunner) connect(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := link.Dial(ctx, link.URL(r.url), r.token)
	require.NoError(t, err)
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamCtl})
	require.NoError(t, err)
	ctl := link.NewCtl(st, link.CtlHandlers{OnRequest: func(_ context.Context, method string, params json.RawMessage) (any, error) {
		r.mu.Lock()
		r.calls = append(r.calls, method)
		on := r.onReq
		r.mu.Unlock()
		if on != nil {
			return on(method, params)
		}
		return nil, &link.CtlError{Code: "not_supported", Message: method}
	}})
	r.mu.Lock()
	r.sess = sess
	r.ctl = ctl
	r.mu.Unlock()
	hello := apigen.LinkCtlHelloParams{
		OrbdVersion: "fake-runner", AgentVersion: "omp/test", Os: "linux", Arch: "arm64", Threads: []apigen.LinkCtlHelloThread{},
		Features: r.features,
		Runner:   &apigen.LinkRunnerInfo{Hostname: "box", Dirs: []apigen.RunnerDir{{Path: "/work/app", Name: "app", Git: true}}},
	}
	if r.mod != nil {
		r.mod(&hello)
	}
	require.NoError(t, ctl.Notify("hello", hello))
}

func (r *fakeRunner) control() *link.Ctl {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctl
}

func (r *fakeRunner) disconnect() {
	r.mu.Lock()
	sess := r.sess
	r.sess = nil
	r.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

func (r *fakeRunner) methods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func whoami(t *testing.T, svc *Service, token string) (int, apigen.OrbWhoami) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/orb/v1/whoami", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	svc.WhoamiHandler().ServeHTTP(rec, req)
	var out apigen.OrbWhoami
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// captureTimers 把 runner 离线定时器换成手动触发：每次登记的回调送进返回的通道（配置下发的重试定时器见 captureAllTimers）。
func captureTimers(svc *Service) <-chan func() {
	offline, _ := captureAllTimers(svc)
	return offline
}

// capturedTimer 是截获的一次定时器登记：时长与回调。
type capturedTimer struct {
	d  time.Duration
	fn func()
}

// captureAllTimers 同 captureTimers，另把 runner 配置下发的重试定时器（连同时长）送进第二个通道。退避序列
// （5s、10s、20s、40s、60s）不含 RunnerOfflineGrace，按时长即可区分两种定时器。
func captureAllTimers(svc *Service) (<-chan func(), <-chan capturedTimer) {
	offline := make(chan func(), 16)
	retry := make(chan capturedTimer, 16)
	svc.hookAfterFunc = func(d time.Duration, fn func()) func() bool {
		switch {
		case d == RunnerOfflineGrace:
			offline <- fn
		case d >= RunnerApplyRetryBase && d <= RunnerApplyRetryMax:
			retry <- capturedTimer{d: d, fn: fn}
		default:
			panic("unexpected timer")
		}
		return func() bool { return true }
	}
	return offline, retry
}

func nextTimer(t *testing.T, ch <-chan func()) func() {
	t.Helper()
	select {
	case fn := <-ch:
		return fn
	case <-time.After(5 * time.Second):
		t.Fatal("runner 离线定时器没有登记")
		return nil
	}
}

func TestRunnerLifecycle(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	timers := captureTimers(h.svc)
	ctx := context.Background()

	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)
	assert.Regexp(t, `^ort_`, tok)
	assert.False(t, r.Online)
	orb := h.waitState(r.OrbID, StateStopped, time.Second)
	assert.Equal(t, KindRunner, orb.Kind)

	// whoami：ort_ → runner 的 orb；未知令牌、缺令牌 → 401
	code, who := whoami(t, h.svc, tok)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, apigen.OrbWhoami{OrbID: r.OrbID, Kind: apigen.OrbKindRunner}, who)
	code, _ = whoami(t, h.svc, ids.RunnerToken())
	assert.Equal(t, http.StatusUnauthorized, code)
	code, _ = whoami(t, h.svc, "")
	assert.Equal(t, http.StatusUnauthorized, code)

	// 用 ort_ 接入：stopped → connecting → ready，没有 setup.run、没有任务
	fr := &fakeRunner{url: h.srv.URL, token: tok, features: []string{"fs", "git", "metrics"}}
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)
	assert.Equal(t, []string{">stopped", "stopped>connecting", "connecting>ready"}, h.eventSeq(r.OrbID))
	assert.Empty(t, fr.methods(), "runner 不收 setup.run / resume.run")
	jobsOf, err := h.st.Q.ListJobsByOrb(ctx, &r.OrbID)
	require.NoError(t, err)
	assert.Empty(t, jobsOf)
	info, err := h.svc.RunnerOrb(ctx, r.ID)
	require.NoError(t, err)
	assert.True(t, info.Online())
	assert.Equal(t, []apigen.RunnerDir{{Path: "/work/app", Name: "app", Git: true}}, info.Dirs)
	assert.Equal(t, "box", info.Row.Hostname)
	kind, feats := h.svc.Features(r.OrbID)
	assert.Equal(t, KindRunner, kind)
	assert.Equal(t, []string{"fs", "git", "metrics"}, feats)
	list, err := h.svc.ListRunners(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].Online)
	assert.Equal(t, "box", *list[0].Hostname)

	// egress 流只通往模型中转与记忆网关（M16 / M21）：git 等其余路径回 403，连接按 Connection: close 正常结束
	fr.mu.Lock()
	sess := fr.sess
	fr.mu.Unlock()
	est, err := sess.Open(ctx, link.StreamHeader{T: link.StreamEgress})
	require.NoError(t, err)
	_ = est.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(est, "GET /git/github.com/a/b/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: oryn-egress\r\nConnection: close\r\n\r\n")
	require.NoError(t, err)
	raw, err := io.ReadAll(est)
	require.NoError(t, err, "egress 流应在应答后正常关闭（EOF），而不是读超时")
	assert.True(t, strings.HasPrefix(string(raw), "HTTP/1.1 403 "), "runner 的 git 出站请求应被拒绝：%q", raw)

	// 守卫
	assert.ErrorIs(t, h.svc.Pause(ctx, r.OrbID, PauseFreeze), ErrRunnerUnsupported)
	assert.ErrorIs(t, h.svc.Pause(ctx, r.OrbID, PauseStop), ErrRunnerUnsupported)
	assert.ErrorIs(t, h.svc.Delete(ctx, r.OrbID), ErrRunnerUnsupported)
	_, err = h.svc.Snapshot(ctx, r.OrbID)
	assert.ErrorIs(t, err, ErrRunnerUnsupported)
	_, err = h.svc.Create(ctx, CreateParams{SnapshotOf: r.OrbID, SnapshotID: "snap-x"})
	assert.ErrorIs(t, err, ErrRunnerUnsupported)
	assert.NoError(t, h.svc.Wake(ctx, r.OrbID), "在线时唤醒无操作")
	// 空闲评估不冻结它（时钟拨快到远超空闲时长）
	h.skew.Store(int64(10 * time.Hour))
	h.svc.EvaluateIdle(ctx)
	h.skew.Store(0)
	h.waitState(r.OrbID, StateReady, time.Second)
	// resolveProvider：显式指名 runner → ErrNoProvider；「唯一 provider」兜底只看到 local
	_, err = h.svc.resolveProvider(ctx, "laptop", nil, nil)
	assert.ErrorIs(t, err, ErrNoProvider)
	p, err := h.svc.resolveProvider(ctx, "", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, h.provID, p.ID)

	// 断开：30 秒内状态不变；期间重连，过期的定时器不再生效
	fr.disconnect()
	stale := nextTimer(t, timers)
	h.waitState(r.OrbID, StateReady, time.Second)
	fr.connect(t)
	require.Eventually(t, func() bool { return h.svc.Connected(r.OrbID) }, 5*time.Second, 10*time.Millisecond)
	stale()
	h.waitState(r.OrbID, StateReady, time.Second)

	// 断开且到期 → stopped；Wake 报离线
	fr.disconnect()
	expire := nextTimer(t, timers)
	require.Eventually(t, func() bool { return !h.svc.Connected(r.OrbID) }, 5*time.Second, 10*time.Millisecond)
	expire()
	h.waitState(r.OrbID, StateStopped, time.Second)
	assert.ErrorIs(t, h.svc.Wake(ctx, r.OrbID), ErrRunnerOffline)
	// 离线时 Features 取库里最近一次上报的
	_, feats = h.svc.Features(r.OrbID)
	assert.Equal(t, []string{"fs", "git", "metrics"}, feats)

	// 重连回到 ready
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)

	// 控制面重启：ready 的 runner 回到 stopped（等它重连）
	fr.disconnect()
	nextTimer(t, timers)
	require.Eventually(t, func() bool { return !h.svc.Connected(r.OrbID) }, 5*time.Second, 10*time.Millisecond)
	h.svc.recover(ctx)
	h.waitState(r.OrbID, StateStopped, time.Second)
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)

	// 吊销：orb → deleted、link 被关、令牌失效
	require.NoError(t, h.svc.DeleteRunner(ctx, r.ID))
	h.waitState(r.OrbID, StateDeleted, time.Second)
	require.Eventually(t, func() bool { return !h.svc.Connected(r.OrbID) }, 5*time.Second, 10*time.Millisecond)
	code, _ = whoami(t, h.svc, tok)
	assert.Equal(t, http.StatusUnauthorized, code)
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = link.Dial(dctx, link.URL(h.srv.URL), tok)
	assert.Error(t, err, "吊销后的 ort_ 令牌不能再接入")
	assert.ErrorIs(t, h.svc.DeleteRunner(ctx, r.ID), ErrNotFound)
	_, err = h.svc.RunnerOrb(ctx, r.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	list, err = h.svc.ListRunners(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)
	prov, err := h.st.Q.GetProvider(ctx, r.ID)
	require.NoError(t, err)
	assert.NotNil(t, prov.DisabledAt)
	assert.NotEqual(t, ids.Hash(tok), prov.TokenHash)
}

func TestRunnerNameSharesProviderUnique(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	_, _, err := h.svc.CreateRunner(context.Background(), "local")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")
}

func TestRunnerViewAndOrbWhoami(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	ctx := context.Background()
	r, _, err := h.svc.CreateRunner(ctx, "box")
	require.NoError(t, err)
	orb, err := h.svc.Get(ctx, r.OrbID)
	require.NoError(t, err)
	v, err := View(ctx, h.st.Q, orb)
	require.NoError(t, err)
	require.NotNil(t, v.Kind)
	assert.Equal(t, apigen.OrbKindRunner, *v.Kind)
	assert.Equal(t, []string{}, v.Features, "从未接入：特性为空表")

	// 普通 orb：View 不给 kind / features；whoami 用 oot_ 令牌得到 kind = orb
	o, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	v, err = View(ctx, h.st.Q, o)
	require.NoError(t, err)
	assert.Nil(t, v.Kind)
	assert.Nil(t, v.Features)
	code, who := whoami(t, h.svc, orbToken(h.svc.cfg.TokenKey, o.ID, o.TokenGen))
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, apigen.OrbWhoami{OrbID: o.ID, Kind: apigen.OrbKindOrb}, who)
	// 普通 provider 不能当 runner 删除
	assert.ErrorIs(t, h.svc.DeleteRunner(ctx, o.ProviderID), ErrNotFound)
}

// M21：hello 带 omphome-apply 的 runner 先同步收到 omphome.apply（此时仍是 connecting），之后才 ready；出站策略随之
// 生效：/llm/<route>/… 注入真实密钥后转发，/git/、/gh/、absolute-form 与 CONNECT 一律 403。离线宽限期内重连时退回
// connecting 重新下发；下发失败停在 connecting 并记一条事件旁注（定时重试见 TestRunnerConfigApplyRetry），重连成功后照常 ready。ready 的 runner 是全局层
// 立即下发的目标，不是任何项目层的目标。
func TestRunnerConfigApplyBeforeReady(t *testing.T) {
	var upstreamKey atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamKey.Store(r.Header.Get("X-Api-Key") + " " + r.URL.Path)
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{upstream: upstream.URL}, nil, false)
	timers := captureTimers(h.svc)
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)

	var mu sync.Mutex
	var applyStates []string // 每次收到 omphome.apply 时 runner 的 orb 状态
	fail := false
	fr := &fakeRunner{url: h.srv.URL, token: tok, features: []string{"fs", FeatureOmpHomeApply, "ssh", "memory"}}
	fr.onReq = func(method string, params json.RawMessage) (any, error) {
		if method != string(apigen.MethodOmpHomeApply) {
			return nil, &link.CtlError{Code: "not_supported", Message: method}
		}
		var p apigen.LinkCtlOmpHomeApplyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		o, err := h.svc.Get(ctx, r.OrbID)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		applyStates = append(applyStates, o.State)
		if fail {
			return nil, &link.CtlError{Code: "failed", Message: "disk full"}
		}
		return apigen.LinkCtlOkResult{Ok: true}, nil
	}
	states := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), applyStates...)
	}
	reconnect := func() {
		t.Helper()
		fr.disconnect()
		nextTimer(t, timers) // 不触发：宽限期内重连
		require.Eventually(t, func() bool { return !h.svc.Connected(r.OrbID) }, 5*time.Second, 10*time.Millisecond)
		fr.connect(t)
	}

	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)
	assert.Equal(t, []string{">stopped", "stopped>connecting", "connecting>ready"}, h.eventSeq(r.OrbID))
	assert.Equal(t, []string{string(apigen.MethodOmpHomeApply)}, fr.methods(), "只收 omphome.apply，没有 setup.run")
	assert.Equal(t, []string{StateConnecting}, states(), "omphome.apply 在进入 ready 之前送达")

	egressDo := func(req string) (int, string) {
		t.Helper()
		fr.mu.Lock()
		sess := fr.sess
		fr.mu.Unlock()
		est, err := sess.Open(ctx, link.StreamHeader{T: link.StreamEgress})
		require.NoError(t, err)
		defer est.Close()
		_ = est.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = io.WriteString(est, req)
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(est), nil)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(body)
	}
	code, body := egressDo("GET /llm/anth/v1/messages HTTP/1.1\r\nHost: 127.0.0.1:7811\r\nX-Api-Key: oryn-ph-sid1\r\nConnection: close\r\n\r\n")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "upstream-ok", body)
	assert.Equal(t, "real-key /v1/messages", upstreamKey.Load(), "占位符在控制面换成真实密钥")
	for _, req := range []string{
		"GET /git/github.com/a/b/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: 127.0.0.1:7811\r\nConnection: close\r\n\r\n",
		"GET /gh/api.github.com/user HTTP/1.1\r\nHost: 127.0.0.1:7811\r\nConnection: close\r\n\r\n",
		"POST /mcp/m0/ HTTP/1.1\r\nHost: 127.0.0.1:7811\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
		"GET http://github.com/a/b/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: github.com\r\nConnection: close\r\n\r\n",
		"GET " + upstream.URL + "/llm/anth/v1/messages HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Api-Key: oryn-ph-sid1\r\nConnection: close\r\n\r\n",
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n",
	} {
		code, body := egressDo(req)
		assert.Equal(t, http.StatusForbidden, code, req)
		assert.Contains(t, body, "runner_forbidden", req)
	}

	// 全局层立即下发的目标含在线 runner；项目层不含（runner 的 orb 不属于任何项目）
	targets, err := h.svc.applyTargets(ctx, "")
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, r.OrbID, targets[0].ID)
	targets, err = h.svc.applyTargets(ctx, "prj_x")
	require.NoError(t, err)
	assert.Empty(t, targets)

	// 宽限期内重连（对端可能是重启过的 orbd）：ready → connecting，重新下发后再 ready
	reconnect()
	require.Eventually(t, func() bool { return len(states()) == 2 }, 5*time.Second, 10*time.Millisecond)
	h.waitState(r.OrbID, StateReady, 5*time.Second)
	seq := h.eventSeq(r.OrbID)
	assert.Equal(t, []string{"ready>connecting", "connecting>ready"}, seq[len(seq)-2:])
	assert.Equal(t, []string{StateConnecting, StateConnecting}, states())

	// 下发失败：停在 connecting，记一条事件旁注（重试定时器在这里不触发）
	mu.Lock()
	fail = true
	mu.Unlock()
	reconnect()
	require.Eventually(t, func() bool {
		evs, err := h.st.Q.ListOrbEvents(ctx, r.OrbID)
		return err == nil && strings.Contains(evs[len(evs)-1].Detail, "runner config apply failed") &&
			strings.Contains(evs[len(evs)-1].Detail, "disk full")
	}, 5*time.Second, 10*time.Millisecond)
	h.waitState(r.OrbID, StateConnecting, time.Second)

	// 修好后重连：connecting 直接下发并 ready
	mu.Lock()
	fail = false
	mu.Unlock()
	reconnect()
	h.waitState(r.OrbID, StateReady, 5*time.Second)
	assert.Equal(t, []string{StateConnecting, StateConnecting, StateConnecting, StateConnecting}, states())
}

// 下发失败后按退避重试（M21）：首次失败记旁注并起 5s 定时器；同样的错误再失败只记日志、退避翻倍；成功即 ready。
// link 断开后到期的定时器不再下发。
func TestRunnerConfigApplyRetry(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	offline, retries := captureAllTimers(h.svc)
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)

	var failures atomic.Int32 // 还要失败的次数
	failures.Store(2)
	fr := &fakeRunner{url: h.srv.URL, token: tok, features: []string{FeatureOmpHomeApply}}
	fr.onReq = func(method string, _ json.RawMessage) (any, error) {
		if method != string(apigen.MethodOmpHomeApply) {
			return nil, &link.CtlError{Code: "not_supported", Message: method}
		}
		if failures.Add(-1) >= 0 {
			return nil, &link.CtlError{Code: "failed", Message: "disk full"}
		}
		return apigen.LinkCtlOkResult{Ok: true}, nil
	}
	nextRetry := func() capturedTimer {
		t.Helper()
		select {
		case rt := <-retries:
			return rt
		case <-time.After(5 * time.Second):
			t.Fatal("配置下发的重试定时器没有登记")
			return capturedTimer{}
		}
	}
	notes := func() int {
		evs, err := h.st.Q.ListOrbEvents(ctx, r.OrbID)
		require.NoError(t, err)
		n := 0
		for _, e := range evs {
			if strings.Contains(e.Detail, "runner config apply failed") {
				n++
			}
		}
		return n
	}
	applies := func() int { return len(fr.methods()) }

	fr.connect(t)
	rt := nextRetry()
	assert.Equal(t, 5*time.Second, rt.d)
	assert.Equal(t, StateConnecting, h.waitState(r.OrbID, StateConnecting, time.Second).State)
	assert.Equal(t, 1, notes())
	assert.Equal(t, 1, applies())

	// 同样的错误再失败：退避翻倍，不再写旁注
	rt.fn()
	rt = nextRetry()
	assert.Equal(t, 10*time.Second, rt.d)
	assert.Equal(t, 2, applies())
	assert.Equal(t, 1, notes())

	// 第三次成功 → ready，不再起定时器
	rt.fn()
	h.waitState(r.OrbID, StateReady, time.Second)
	assert.Equal(t, 3, applies())
	select {
	case rt := <-retries:
		t.Fatalf("成功后仍起了重试定时器（%v）", rt.d)
	default:
	}

	// 宽限期内重连，下发又失败：起定时器；link 随后断开，定时器到期不再下发
	failures.Store(1)
	fr.disconnect()
	nextTimer(t, offline)
	require.Eventually(t, func() bool { return !h.svc.Connected(r.OrbID) }, 5*time.Second, 10*time.Millisecond)
	fr.connect(t)
	rt = nextRetry()
	assert.Equal(t, 5*time.Second, rt.d, "新 link 的退避从头开始")
	assert.Equal(t, 4, applies())
	fr.disconnect()
	nextTimer(t, offline)
	require.Eventually(t, func() bool { return !h.svc.Connected(r.OrbID) }, 5*time.Second, 10*time.Millisecond)
	rt.fn()
	assert.Equal(t, 4, applies(), "link 断开后重试不再下发")
	select {
	case rt := <-retries:
		t.Fatalf("link 断开后仍起了重试定时器（%v）", rt.d)
	default:
	}
}

func TestRunnerApplyBackoff(t *testing.T) {
	var got []time.Duration
	for i := range 7 {
		got = append(got, runnerApplyBackoff(i))
	}
	s := time.Second
	assert.Equal(t, []time.Duration{5 * s, 10 * s, 20 * s, 40 * s, 60 * s, 60 * s, 60 * s}, got)
}

// runner 线程可经 runner.sock 的 library.put 把文件存进全局助手文件库：runner 的 link 受理 library.put，控制面经同一
// link 的 library.fetch 与 `library` 流取回字节；其余文件库方法（library.sync）仍回 runner_unsupported。
func TestRunnerLibraryPut(t *testing.T) {
	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	captureTimers(h.svc)
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)

	// 控制面一侧按 library.Syncer.PutFrom 的流程：对发起的 orb 发 library.fetch，再收同 id 的 `library` 流
	got := make(chan []byte, 1)
	h.svc.HandleStream(link.StreamLibrary, func(_ context.Context, orbID string, hd link.StreamHeader, st *link.Stream) {
		if orbID != r.OrbID || hd.ID != "lfx-1" {
			return
		}
		b, _ := io.ReadAll(io.LimitReader(st, hd.Size))
		got <- b
	})
	var synced atomic.Int32
	h.svc.HandleRequest(string(apigen.MethodLibrarySync), func(context.Context, string, json.RawMessage) (any, error) {
		synced.Add(1)
		return map[string]any{}, nil
	})
	h.svc.HandleRequest(string(apigen.MethodLibraryPut), func(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
		var p apigen.LinkCtlLibraryPutParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		ctl, ok := h.svc.Ctl(orbID)
		if !ok {
			return nil, errors.New("no link")
		}
		var res apigen.LinkCtlLibraryFetchResult
		if err := ctl.Call(ctx, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: "lfx-1", Path: p.Path}, &res); err != nil {
			return nil, err
		}
		select {
		case b := <-got:
			return map[string]any{"content": string(b), "size": res.Size, "thread": p.ThreadID}, nil
		case <-time.After(5 * time.Second):
			return nil, errors.New("library 流没有到达")
		}
	})

	content := "a,b\n1,2\n"
	fr := &fakeRunner{url: h.srv.URL, token: tok, features: []string{"fs"}}
	fr.onReq = func(method string, params json.RawMessage) (any, error) {
		if method != string(apigen.MethodLibraryFetch) {
			return nil, &link.CtlError{Code: "not_supported", Message: method}
		}
		var p apigen.LinkCtlLibraryFetchParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Path != "/work/app/out.csv" {
			return nil, &link.CtlError{Code: "not_found", Message: p.Path}
		}
		fr.mu.Lock()
		sess := fr.sess
		fr.mu.Unlock()
		st, err := sess.Open(context.Background(), link.StreamHeader{T: link.StreamLibrary, ID: p.ID, Size: int64(len(content))})
		if err != nil {
			return nil, err
		}
		defer st.Close()
		if _, err := io.WriteString(st, content); err != nil {
			return nil, err
		}
		return apigen.LinkCtlLibraryFetchResult{Size: int64(len(content))}, nil
	}
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out map[string]any
	require.NoError(t, fr.control().Call(cctx, string(apigen.MethodLibraryPut), apigen.LinkCtlLibraryPutParams{ThreadID: "thr_x", Path: "/work/app/out.csv"}, &out))
	assert.Equal(t, content, out["content"], "字节经 runner 的 library.fetch 取回")
	assert.EqualValues(t, len(content), out["size"])
	assert.Equal(t, "thr_x", out["thread"])
	assert.Equal(t, []string{string(apigen.MethodLibraryFetch)}, fr.methods())

	err = fr.control().Call(cctx, string(apigen.MethodLibrarySync), map[string]any{"files": []any{}}, nil)
	var ce *link.CtlError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, CodeRunnerUnsupported, ce.Code)
	assert.Zero(t, synced.Load(), "library.sync 不触达处理器")
}
