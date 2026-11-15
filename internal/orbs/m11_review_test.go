package orbs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/link"
)

// M11 全量审查 F3 / F4 / S1 的回归测试。

const commitC2 = "c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2"

// F3：provider 没上报 snapshotUsed 的 orb，snapshot_of 清空、不发布，但磁盘上是快照里的检出与完成标记：
// setup.run 仍须置 repo.update，否则 orbd 跳过 setup、停在快照的旧 HEAD。
func TestProjectSnapshotUnknownUsageRefreshesRepo(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	prj := e.project("app", nil)
	e.createReady(CreateParams{ProjectID: prj})
	s1 := e.snapshots(prj)[0]
	e.drv.mu.Lock()
	e.drv.noSnapshotUsed = true
	e.drv.mu.Unlock()
	e.mode.Store(modeRefresh)
	e.commit.Store(commitC2)

	b := e.createReady(CreateParams{ProjectID: prj})
	assert.Equal(t, s1.ID, *e.createSpawn(b.ID).SnapshotID)
	assert.Nil(t, b.SnapshotOf, "来源未知：不保留 snapshot_of")
	calls := e.setupCalls(b.ID)
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].Repo.Update, "来源未知但可能带着快照的检出：照样 repo.update")
	assert.True(t, *calls[0].Repo.Update)
	run := e.latestSetupRun(b.ID)
	assert.True(t, run.Prepared, "快照带着完成标记")
	assert.EqualValues(t, 1, run.SetupRan, "基线前进：增量重跑")
	assert.Equal(t, commitC2, derefStr(run.BaseCommit))
	assert.Len(t, e.snapshots(prj), 1, "来源未知的 orb 不发布新快照")
	assert.Equal(t, SnapshotReady, e.snapshot(s1.ID).State)
	assert.Equal(t, 1, e.drv.countOps("snapshot"))
}

// F4：首次 spawn 失败、Wake 后的第一次成功 spawn 才第一次确认来源，同样按 snapshotUsed 处理。
func TestWakeAfterFailedSpawnJudgesSnapshotUsed(t *testing.T) {
	for _, tc := range []string{"nil", "false", "true"} {
		t.Run(tc, func(t *testing.T) {
			// 不跑 Service.Run：退役且无引用的快照会被周期回收器删行，断言退役原因前不能让它抢先
			e := newM11Env(t, []string{FeatureRepoUpdate}, false, nil)
			ctx := context.Background()
			prj := e.project("app", nil)
			e.createReady(CreateParams{ProjectID: prj})
			s1 := e.snapshots(prj)[0]

			e.drv.mu.Lock()
			e.drv.failSpawns = jobs.DefaultMaxAttempts
			e.drv.mu.Unlock()
			o, err := e.svc.Create(ctx, CreateParams{ProjectID: prj})
			require.NoError(t, err)
			require.Equal(t, s1.ID, derefStr(o.SnapshotOf))
			failed := e.waitState(o.ID, StateFailed, 10*time.Second)
			require.Equal(t, s1.ID, derefStr(failed.SnapshotOf), "失败的 spawn 不判来源")

			e.drv.mu.Lock()
			switch tc {
			case "nil":
				e.drv.noSnapshotUsed = true
				e.forgeUnprepared.Store(true) // 全量重跑 setup：若不禁发布就会发布新快照
			case "false":
				e.drv.missing[s1.ID] = true
			}
			e.drv.mu.Unlock()
			require.NoError(t, e.svc.Wake(ctx, o.ID))
			cur := e.waitState(o.ID, StateReady, 10*time.Second)

			switch tc {
			case "nil":
				assert.Nil(t, cur.SnapshotOf, "来源未知：清来源")
				assert.True(t, e.svc.snapshotUsageUnknown(ctx, o.ID), "记下来源未知")
				assert.EqualValues(t, 1, e.latestSetupRun(o.ID).SetupRan)
				assert.Len(t, e.snapshots(prj), 1, "来源未知不发布")
				assert.Equal(t, SnapshotReady, e.snapshot(s1.ID).State, "镜像未被证明缺失：不退役")
			case "false":
				assert.Nil(t, cur.SnapshotOf, "退回了基础镜像：清来源")
				assert.Equal(t, RetireImageMissing, retireReason(e.snapshot(s1.ID)))
			case "true":
				assert.Equal(t, s1.ID, derefStr(cur.SnapshotOf), "确认用了快照：保留来源")
				assert.False(t, e.svc.snapshotUsageUnknown(ctx, o.ID))
				assert.Equal(t, SnapshotReady, e.snapshot(s1.ID).State)
			}
		})
	}
}

// S1：发布项目快照之后源 orb 的令牌已轮换：快照里那一代令牌建不了 link，新令牌经 spawn（写令牌文件 + SIGHUP）送达，
// orbd 用它重连并重跑本阶段（setup 跳过、resume 补上），不再发布。
func TestProjectSnapshotRotatesSourceToken(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate, FeatureSetupDeferResume}, true, nil)
	ctx := context.Background()
	prj := e.project("app", nil)
	a := e.createReady(CreateParams{ProjectID: prj})
	require.Len(t, e.snapshots(prj), 1)

	inSnap := e.createSpawn(a.ID).Env.OrynOrbToken
	_, err := e.svc.orbForToken(ctx, inSnap)
	assert.Error(t, err, "快照里的令牌已作废")
	assert.EqualValues(t, 2, a.TokenGen)
	sp := e.lastSpawn(a.ID)
	require.NotEqual(t, inSnap, sp.Env.OrynOrbToken)
	got, err := e.svc.orbForToken(ctx, sp.Env.OrynOrbToken)
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID)
	assert.Equal(t, 2, e.drv.countOps("spawn"), "建 orb 一次 + 送达新令牌一次")
	assert.True(t, e.svc.Connected(a.ID), "orbd 用新令牌重连")
	assert.Contains(t, e.stateEvents(a.ID), "setting_up>connecting")
	assert.Equal(t, []string{"setup.run", "setup.run", "resume.run"}, e.drv.orbd(a.ID).methods())
	assert.Len(t, e.snapshots(prj), 1, "重跑不再发布")
	assert.Equal(t, 1, e.drv.countOps("snapshot"))
}

// S1：fork 快照之后源 orb 的令牌已轮换（Snapshot 返回前）；在跑的 orb 经 spawn 送达新令牌、状态不变；
// 停机的 orb 只轮换，不为送达令牌把机器拉起来，唤醒时照常送达。
func TestForkSnapshotRotatesSourceToken(t *testing.T) {
	e := newM11Env(t, nil, true, nil)
	ctx := context.Background()
	a := e.createReady(CreateParams{})
	before := e.lastSpawn(a.ID).Env.OrynOrbToken

	_, err := e.svc.Snapshot(ctx, a.ID)
	require.NoError(t, err)
	_, err = e.svc.orbForToken(ctx, before)
	assert.Error(t, err, "fork 快照里的令牌已作废")
	cur := mustOrb(t, e, a.ID)
	assert.Equal(t, a.TokenGen+1, cur.TokenGen)
	require.Eventually(t, func() bool {
		tok := e.lastSpawn(a.ID).Env.OrynOrbToken
		o := e.drv.orbd(a.ID)
		o.mu.Lock()
		delivered := o.token == tok
		o.mu.Unlock()
		got, err := e.svc.orbForToken(ctx, tok)
		return tok != before && delivered && err == nil && got.ID == a.ID && e.svc.Connected(a.ID)
	}, 10*time.Second, 20*time.Millisecond, "新令牌经 spawn 送达，orbd 用它重连")
	assert.Equal(t, StateReady, mustOrb(t, e, a.ID).State)

	// 停机的 orb：只轮换
	require.NoError(t, e.svc.Pause(ctx, a.ID, PauseStop))
	stopped := e.waitState(a.ID, StateStopped, 10*time.Second)
	spawns := e.drv.countOps("spawn")
	_, err = e.svc.Snapshot(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, stopped.TokenGen+1, mustOrb(t, e, a.ID).TokenGen)
	assert.Equal(t, spawns, e.drv.countOps("spawn"), "不为送达令牌拉起停机的机器")
	assert.Equal(t, StateStopped, mustOrb(t, e, a.ID).State)
	require.NoError(t, e.svc.Wake(ctx, a.ID))
	e.waitState(a.ID, StateReady, 10*time.Second)
	got, err := e.svc.orbForToken(ctx, e.lastSpawn(a.ID).Env.OrynOrbToken)
	require.NoError(t, err)
	assert.Equal(t, a.ID, got.ID)
}

// S1 失败关闭：快照拍成后轮换令牌的事务失败——fork 快照不交给调用方并入队删除，项目快照不发布、building 行回收；
// 两种情况下源 orb 的令牌都保持原样（快照里的令牌仍有效，所以快照本身不可被消费）。
func TestSnapshotRotateFailureDiscardsSnapshot(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("rotate tx failed")

	t.Run("fork", func(t *testing.T) {
		e := newM11Env(t, nil, true, nil)
		a := e.createReady(CreateParams{})
		e.svc.hookRotateToken = func(string) error { return boom }
		snap, err := e.svc.Snapshot(ctx, a.ID)
		require.ErrorIs(t, err, errTokenRotate)
		assert.Empty(t, snap, "失败时不返回快照 id")
		assert.Equal(t, 1, e.drv.countOps("snapshot"))
		assert.Equal(t, a.TokenGen, mustOrb(t, e, a.ID).TokenGen, "令牌没有轮换")
		require.Eventually(t, func() bool {
			e.drv.mu.Lock()
			defer e.drv.mu.Unlock()
			return len(e.drv.deletedSnaps) == 1
		}, 10*time.Second, 20*time.Millisecond, "带着有效令牌的快照被入队删除")
	})

	t.Run("project", func(t *testing.T) {
		e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
		e.svc.hookRotateToken = func(string) error { return boom }
		prj := e.project("app", nil)
		a := e.createReady(CreateParams{ProjectID: prj})
		assert.Equal(t, 1, e.drv.countOps("snapshot"))
		require.Eventually(t, func() bool {
			e.drv.mu.Lock()
			defer e.drv.mu.Unlock()
			return len(e.drv.deletedSnaps) == 1
		}, 10*time.Second, 20*time.Millisecond, "快照镜像被回收")
		assert.Empty(t, e.snapshots(prj), "不发布、building 行也已删除")
		assert.EqualValues(t, 1, mustOrb(t, e, a.ID).TokenGen, "令牌没有轮换")
	})
}

// S1：轮换开始之后、提交之前，有人用旧令牌建立 link 并通过复核（成为当前 link）——提交后这条旧代 link 也必须关闭，
// 不能继续收发 ctl。送达新令牌的 spawn 这里不让 orbd 重连（noConnect），以免重连顶掉旧代 link、掩盖问题。
func TestRotateClosesOldGenLinkReconnectedMidRotation(t *testing.T) {
	e := newM11Env(t, nil, true, nil)
	ctx := context.Background()
	a := e.createReady(CreateParams{})
	o := e.drv.orbd(a.ID)
	oldTok := e.lastSpawn(a.ID).Env.OrynOrbToken
	e.drv.mu.Lock()
	e.drv.noConnect = true
	e.drv.mu.Unlock()
	var mid *link.Ctl
	e.svc.hookRotateBegin = func(string) {
		e.svc.hookRotateBegin = nil
		rogue := &fakeOrbd{url: o.url, orbID: a.ID, token: oldTok}
		require.NoError(t, rogue.connect())
		t.Cleanup(rogue.disconnect)
		mid = rogue.ctl
		// 请求得到处理器的应答（方法不存在）而不是 unauthorized：这条 link 已发布并通过复核
		err := mid.Call(ctx, "no.such.method", map[string]any{}, nil)
		var ce *link.CtlError
		require.ErrorAs(t, err, &ce)
		require.Equal(t, link.CodeMethodNotFound, ce.Code)
	}
	_, err := e.svc.Snapshot(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, mid)
	select {
	case <-mid.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("轮换进行中用旧令牌建立的 link 没有被关闭")
	}
	assert.Error(t, mid.Call(ctx, "no.such.method", map[string]any{}, nil), "旧代 link 不能再发 ctl")
	_, err = e.svc.orbForToken(ctx, oldTok)
	assert.Error(t, err, "旧令牌已作废")

	// 送达的新令牌可以建立 link
	require.Eventually(t, func() bool { return e.drv.countOps("spawn") == 2 }, 10*time.Second, 20*time.Millisecond)
	e.drv.mu.Lock()
	e.drv.noConnect = false
	e.drv.mu.Unlock()
	o.mu.Lock()
	require.NotEqual(t, oldTok, o.token)
	o.mu.Unlock()
	o.disconnect()
	require.NoError(t, o.connect())
	require.Eventually(t, func() bool { return e.svc.Connected(a.ID) }, 10*time.Second, 20*time.Millisecond)
}
