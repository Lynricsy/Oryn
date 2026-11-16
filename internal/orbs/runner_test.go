package orbs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
)

// fakeRunner 是 runner 形态 orbd 的替身：用 ort_ 令牌拨 link、hello 带 runner 信息，记下收到的 ctl 请求。
// mod 非 nil 时在发送前改写 hello。
type fakeRunner struct {
	url, token string
	features   []string
	mod        func(*apigen.LinkCtlHelloParams)

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
	ctl := link.NewCtl(st, link.CtlHandlers{OnRequest: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		r.mu.Lock()
		r.calls = append(r.calls, method)
		r.mu.Unlock()
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

// captureTimers 把 runner 离线定时器换成手动触发：每次登记的回调送进返回的通道。
func captureTimers(svc *Service) <-chan func() {
	ch := make(chan func(), 16)
	svc.hookAfterFunc = func(d time.Duration, fn func()) func() bool {
		if d != RunnerOfflineGrace {
			panic("unexpected timer")
		}
		ch <- fn
		return func() bool { return true }
	}
	return ch
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

	// egress 流只通往记忆网关（M16）：模型路由等其余路径回 403，连接按 Connection: close 正常结束
	fr.mu.Lock()
	sess := fr.sess
	fr.mu.Unlock()
	est, err := sess.Open(ctx, link.StreamHeader{T: link.StreamEgress})
	require.NoError(t, err)
	_ = est.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(est, "GET /llm/anthropic/v1/models HTTP/1.1\r\nHost: oryn-egress\r\nConnection: close\r\n\r\n")
	require.NoError(t, err)
	raw, err := io.ReadAll(est)
	require.NoError(t, err, "egress 流应在应答后正常关闭（EOF），而不是读超时")
	assert.True(t, strings.HasPrefix(string(raw), "HTTP/1.1 403 "), "runner 的非记忆出站请求应被拒绝：%q", raw)

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
