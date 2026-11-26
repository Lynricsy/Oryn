package orbs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// sandboxOrb 直接以给定状态写一台沙箱 orb。
func (b *bare) sandboxOrb(state string, changedAt int64) db.Orb {
	b.t.Helper()
	ctx := context.Background()
	id := ids.Orb()
	require.NoError(b.t, b.st.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: id, ProviderID: b.provID, Type: "small", State: state, StateChangedAt: changedAt,
		TokenHash: ids.Hash(orbToken(b.svc.cfg.TokenKey, id, 0)), CreatedAt: 1, SandboxNetwork: new(SandboxNetNone),
	}))
	require.NoError(b.t, b.st.Q.InsertOrbEvent(ctx, db.InsertOrbEventParams{OrbID: id, Ts: changedAt, FromState: "ready", ToState: state, Detail: "{}"}))
	orb, err := b.st.Q.GetOrb(ctx, id)
	require.NoError(b.t, err)
	return orb
}

// M30 3.1：Create 带 SandboxNetwork 写入标记，spawn 任务带 sandbox，View 带 sandbox；与其余种类、项目、快照、Profile 互斥，且须指定 provider。
func TestCreateSandboxOrb(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, func(c *Config) { c.Defaults.Type = "small" })
	orb, err := b.svc.Create(ctx, CreateParams{Provider: "local", SandboxNetwork: SandboxNetEgress})
	require.NoError(t, err)
	require.NotNil(t, orb.SandboxNetwork)
	assert.Equal(t, SandboxNetEgress, *orb.SandboxNetwork)
	assert.True(t, IsSandbox(orb))

	js, err := b.st.Q.ListUnfinishedJobsByOrb(ctx, &orb.ID)
	require.NoError(t, err)
	require.Len(t, js, 1)
	var p apigen.JobSpawnParams
	require.NoError(t, json.Unmarshal([]byte(js[0].Params), &p))
	require.NotNil(t, p.Sandbox)
	assert.Equal(t, apigen.SandboxNetworkEgress, p.Sandbox.Network)

	v, err := View(ctx, b.st.Q, orb)
	require.NoError(t, err)
	require.NotNil(t, v.Sandbox)
	assert.True(t, *v.Sandbox)

	for _, p := range []CreateParams{
		{Provider: "local", SandboxNetwork: "open"},
		{SandboxNetwork: SandboxNetNone},
		{Provider: "local", SandboxNetwork: SandboxNetNone, Pool: true},
		{Provider: "local", SandboxNetwork: SandboxNetNone, Box: true},
		{Provider: "local", SandboxNetwork: SandboxNetNone, Computer: true},
		{Provider: "local", SandboxNetwork: SandboxNetNone, Coordinator: true},
		{Provider: "local", SandboxNetwork: SandboxNetNone, ProjectID: "prj_x"},
		{Provider: "local", SandboxNetwork: SandboxNetNone, SnapshotID: "snap_x"},
		{Provider: "local", SandboxNetwork: SandboxNetNone, SnapshotOf: "orb-x"},
		{Provider: "local", SandboxNetwork: SandboxNetNone, BrowserProfileID: "bp_x"},
	} {
		_, err = b.svc.Create(ctx, p)
		require.ErrorIs(t, err, ErrInvalidState, "%+v", p)
	}

	plain, err := b.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	assert.False(t, IsSandbox(plain))
	v, err = View(ctx, b.st.Q, plain)
	require.NoError(t, err)
	assert.Nil(t, v.Sandbox, "普通 orb 省略 sandbox")
	js, err = b.st.Q.ListUnfinishedJobsByOrb(ctx, &plain.ID)
	require.NoError(t, err)
	require.Len(t, js, 1)
	p = apigen.JobSpawnParams{}
	require.NoError(t, json.Unmarshal([]byte(js[0].Params), &p))
	assert.Nil(t, p.Sandbox)
}

// 沙箱不冻结、不停机、不算退役、不随线程回收；不能唤醒、暂停或拍快照（生命周期归 sandboxes 服务）。
func TestSandboxOrbExemptions(t *testing.T) {
	ctx := context.Background()
	b := newBare(t, func(c *Config) {
		c.Idle = config.Idle{AgentIdle: config.Duration(time.Millisecond), UserIdle: config.Duration(time.Millisecond), StopAfterFrozen: config.Duration(time.Millisecond)}
	})
	old := time.Now().Add(-time.Hour).UnixMilli()
	sbx := b.sandboxOrb(StateReady, old)
	frozen := b.sandboxOrb(StateFrozen, old)
	plain := b.orb(StateReady, old)

	b.svc.EvaluateIdle(ctx)
	assert.Equal(t, StateFreezing, b.state(plain.ID), "对照：阈值在生效")
	assert.Equal(t, StateReady, b.state(sbx.ID), "沙箱不冻结")
	assert.Equal(t, StateFrozen, b.state(frozen.ID), "沙箱不因冻结过久停机")
	assert.Empty(t, b.pending(sbx.ID))

	assert.False(t, b.svc.retired(ctx, sbx))
	deleted, err := b.svc.DeleteUnused(ctx, sbx.ID, "")
	require.NoError(t, err)
	assert.False(t, deleted)
	assert.Equal(t, StateReady, b.state(sbx.ID))

	stopped := b.sandboxOrb(StateStopped, old)
	require.ErrorIs(t, b.svc.Wake(ctx, stopped.ID), ErrInvalidState)
	assert.Equal(t, StateStopped, b.state(stopped.ID))
	require.ErrorIs(t, b.svc.Pause(ctx, sbx.ID, PauseFreeze), ErrInvalidState)
	_, err = b.svc.Snapshot(ctx, sbx.ID)
	require.ErrorIs(t, err, ErrInvalidState)
	assert.Empty(t, b.pending(sbx.ID))
	assert.Empty(t, b.pending(stopped.ID))

	require.ErrorIs(t, b.svc.SetSandboxNetwork(ctx, plain.ID, SandboxNetNone), ErrInvalidState, "不是沙箱")
	require.ErrorIs(t, b.svc.SetSandboxNetwork(ctx, sbx.ID, "open"), ErrInvalidState, "网络模式非法")
}

// 沙箱 link：hello 后直接 ready（不发 setup.run）；ctl 请求一律 sandbox_unsupported；orbd 发起的流（含 egress）被关；
// metrics.sample 限速到 10 秒一条。
func TestSandboxLink(t *testing.T) {
	drv := newFakeDriver()
	drv.features = []string{FeatureFS, FeatureFSTar, FeatureSandbox}
	h := newHarness(t, drv, fakeSetup{upstream: "http://127.0.0.1:1"}, nil)
	ctx := context.Background()
	h.svc.HandleRequest("test.ping", func(context.Context, string, json.RawMessage) (any, error) {
		return apigen.LinkCtlOkResult{Ok: true}, nil
	})

	orb, err := h.svc.Create(ctx, CreateParams{Provider: "local", SandboxNetwork: SandboxNetNone})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)
	assert.Equal(t, []string{">requested", "requested>spawning", "spawning>connecting", "connecting>ready"}, h.eventSeq(orb.ID))
	evs, err := h.st.Q.ListOrbEvents(ctx, orb.ID)
	require.NoError(t, err)
	assert.Contains(t, evs[len(evs)-1].Detail, "sandbox link")

	o := drv.orbd(orb.ID)
	require.NotNil(t, o)
	assert.Empty(t, o.methods(), "沙箱不收 setup.run / portal.configure")
	drv.mu.Lock()
	require.Len(t, drv.spawns, 1)
	require.NotNil(t, drv.spawns[0].Sandbox)
	assert.Equal(t, apigen.SandboxNetworkNone, drv.spawns[0].Sandbox.Network)
	drv.mu.Unlock()
	assert.True(t, h.svc.HasFeature(orb.ID, FeatureSandbox))

	o.mu.Lock()
	ctl, sess := o.ctl, o.sess
	o.mu.Unlock()

	// connect 已发过一条 metrics.sample；紧接着的两条被限速丢弃。之后的 ctl 请求在读循环里排在它们之后，应答到达时通知已处理。
	now := time.Now().UnixMilli()
	for i := int64(1); i <= 2; i++ {
		require.NoError(t, ctl.Notify("metrics.sample", apigen.OrbMetricSample{TS: now + i, CPUPct: 1, MemLimitMB: 1}))
	}
	for _, m := range []string{"test.ping", "entries.cursors", "github.call", "no.such.method"} {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var res json.RawMessage
		err := ctl.Call(cctx, m, map[string]any{}, &res)
		cancel()
		require.Error(t, err, m)
		assert.Contains(t, err.Error(), CodeSandboxUnsupported, m)
	}
	samples, err := h.st.Q.ListOrbSamples(ctx, db.ListOrbSamplesParams{OrbID: orb.ID, FromTs: 0, ToTs: now + time.Hour.Milliseconds()})
	require.NoError(t, err)
	assert.Len(t, samples, 1, "metrics.sample 限速")

	for _, typ := range []string{link.StreamEgress, "agent"} {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := sess.Open(sctx, link.StreamHeader{T: typ})
		cancel()
		require.NoError(t, err, typ)
		done := make(chan error, 1)
		go func() {
			_, err := st.Read(make([]byte, 1))
			done <- err
		}()
		select {
		case err := <-done:
			assert.Error(t, err, "%s 流应被关闭", typ)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s 流没有被关闭", typ)
		}
		_ = st.Close()
	}
	assert.True(t, h.svc.Connected(orb.ID), "拒绝请求与流不断开 link")
}

// provider 镜像里的 orbd 没有精简模式（hello 不含 sandbox）：沙箱 orb 进入 failed，同样不发 setup.run。
func TestSandboxLinkWithoutFeatureFails(t *testing.T) {
	drv := newFakeDriver()
	drv.features = []string{FeatureFS, FeaturePortal}
	h := newHarness(t, drv, fakeSetup{upstream: "http://127.0.0.1:1"}, nil)
	ctx := context.Background()
	orb, err := h.svc.Create(ctx, CreateParams{Provider: "local", SandboxNetwork: SandboxNetEgress})
	require.NoError(t, err)
	h.waitState(orb.ID, StateFailed, 10*time.Second)
	evs, err := h.st.Q.ListOrbEvents(ctx, orb.ID)
	require.NoError(t, err)
	assert.Contains(t, evs[len(evs)-1].Detail, "no sandbox mode")
	assert.Empty(t, drv.orbd(orb.ID).methods())
}

// SetSandboxNetwork 入队 set_network 并等待：成功时 provider 收到目标模式；失败时返回任务错误原文（保留 network_unavailable: 前缀）。
func TestSetSandboxNetwork(t *testing.T) {
	drv := newFakeDriver()
	drv.features = []string{FeatureSandbox}
	h := newHarness(t, drv, fakeSetup{upstream: "http://127.0.0.1:1"}, nil)
	ctx := context.Background()
	orb, err := h.svc.Create(ctx, CreateParams{Provider: "local", SandboxNetwork: SandboxNetEgress})
	require.NoError(t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)

	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, h.svc.SetSandboxNetwork(wctx, orb.ID, SandboxNetNone))
	require.NoError(t, h.svc.SetSandboxNetwork(wctx, orb.ID, SandboxNetEgress))
	drv.mu.Lock()
	assert.Equal(t, []string{SandboxNetNone, SandboxNetEgress}, drv.networks)
	drv.netErr = "network_unavailable: sandbox was created without a network"
	drv.mu.Unlock()

	err = h.svc.SetSandboxNetwork(wctx, orb.ID, SandboxNetEgress)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "network_unavailable:"), err.Error())
	assert.Equal(t, StateReady, h.waitState(orb.ID, StateReady, time.Second).State, "set_network 失败不改变 orb 状态")
}
