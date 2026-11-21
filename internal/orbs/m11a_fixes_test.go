package orbs

// M11a 审查修复（控制面）的回归测试：CP-1…CP-5、S-P1-1、S-P1-2、S-P2-1…S-P2-5、orb TLS CA 下发。

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// hookVault 是 DefaultSetup 用的假密钥库：每次 EnvSecrets 调用 hook（第几次调用），用来在 Plan 读取期间插入并发修改。
type hookVault struct {
	calls atomic.Int32
	hook  func(n int)
}

func (v *hookVault) Lookup(string) (string, string, bool)       { return "", "", false }
func (v *hookVault) SecretByID(string) (string, []string, bool) { return "", nil, false }
func (v *hookVault) GitToken(string) (string, bool)             { return "", false }
func (v *hookVault) GitHosts() ([]string, error)                { return nil, nil }
func (v *hookVault) ProjectEnv(string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (v *hookVault) ProjectGitHosts(string) ([]string, error) { return nil, nil }
func (v *hookVault) EnvSecrets() (map[string]string, error) {
	n := int(v.calls.Add(1))
	if v.hook != nil {
		v.hook(n)
	}
	return map[string]string{}, nil
}

// putSecret 写一条全局密钥（已存在就覆盖，id 不变），值不重要，只看名字与修改时间。
func (e *m11Env) putSecret(name, mode string, updatedAt int64) {
	e.t.Helper()
	ctx := context.Background()
	old, err := e.st.Q.GetSecretByName(ctx, name)
	if err == nil {
		require.NoError(e.t, e.st.Q.UpdateSecret(ctx, db.UpdateSecretParams{
			ID: old.ID, Mode: mode, Secret: true, ValueEnc: []byte("x"), Hosts: "[]", UpdatedAt: updatedAt,
		}))
		return
	}
	require.ErrorIs(e.t, err, sql.ErrNoRows)
	require.NoError(e.t, e.st.Q.InsertSecret(ctx, db.InsertSecretParams{
		ID: ids.New("sec"), Name: name, Mode: mode, Secret: true, ValueEnc: []byte("x"), Hosts: "[]", CreatedAt: 1, UpdatedAt: updatedAt,
	}))
}

// putProjectSecret 写一条项目级条目（已存在就覆盖，id 不变），值不重要，只看名字与修改时间（M24 阶段 ④）。
func (e *m11Env) putProjectSecret(prj, name, mode string, updatedAt int64) {
	e.t.Helper()
	ctx := context.Background()
	old, err := e.st.Q.GetProjectSecret(ctx, db.GetProjectSecretParams{ProjectID: prj, Name: name})
	if err == nil {
		require.NoError(e.t, e.st.Q.UpdateSecret(ctx, db.UpdateSecretParams{
			ID: old.ID, Mode: mode, Secret: true, ValueEnc: []byte("x"), Hosts: "[]", UpdatedAt: updatedAt,
		}))
		return
	}
	require.ErrorIs(e.t, err, sql.ErrNoRows)
	require.NoError(e.t, e.st.Q.InsertSecret(ctx, db.InsertSecretParams{
		ID: ids.New("sec"), ProjectID: &prj, Name: name, Mode: mode, Secret: true, ValueEnc: []byte("x"), Hosts: "[]",
		CreatedAt: 1, UpdatedAt: updatedAt,
	}))
}

func (e *m11Env) currentHash(prjID, typ string) string {
	e.t.Helper()
	ctx := context.Background()
	prj, err := e.st.Q.GetProject(ctx, prjID)
	require.NoError(e.t, err)
	prov, err := e.st.Q.GetProvider(ctx, e.provID)
	require.NoError(e.t, err)
	h, err := e.svc.configHash(ctx, e.st.Q, prj, prov, typ)
	require.NoError(e.t, err)
	return h
}

// deleteJobs 返回为 snapID 入队过的 delete_snapshot 任务数（同步入队，不必等待）。
func (e *m11Env) deleteJobs(snapID string) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.st.DB.QueryRow(`SELECT COUNT(*) FROM jobs WHERE op = 'delete_snapshot' AND params LIKE '%' || ? || '%'`, snapID).Scan(&n))
	return n
}

func (e *m11Env) waitBuilding(prj string) db.ProjectSnapshot {
	e.t.Helper()
	var row db.ProjectSnapshot
	require.Eventually(e.t, func() bool {
		for _, r := range e.snapshots(prj) {
			if r.State == SnapshotBuilding && e.drv.countOps("snapshot") == 1 {
				row = r
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond)
	return row
}

// gateSnapshots 让此后的 snapshot 任务阻塞到返回的 release 被调用；测试失败提前退出时由 Cleanup 放行
// （否则 provider 等任务结束，清理会卡住）。
func (e *m11Env) gateSnapshots() (release func()) {
	gate := make(chan struct{})
	e.drv.mu.Lock()
	e.drv.snapGate = gate
	e.drv.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	e.t.Cleanup(release)
	return release
}

// CP-1：真实 DefaultSetup 读完项目之后、返回参数之前项目 setup 被并发修改。旧环境的快照只能带旧哈希，
// 发布时复核到配置已变即退役；orb 记下的已应用哈希也是旧的。
func TestCP1PlanHashFromSameInputs(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, false, nil)
	prj := e.project("app", nil)
	v := &hookVault{hook: func(n int) {
		if n == 1 {
			e.setSetupScript(prj, "make deps NEW")
		}
	}}
	e.svc.setup = &DefaultSetup{Store: e.st, Vault: v}
	o := e.createReady(CreateParams{ProjectID: prj})
	calls := e.setupCalls(o.ID)
	require.Equal(t, "make deps", *calls[0].Scripts.Setup)
	cur := e.currentHash(prj, o.Type)
	rows := e.snapshots(prj)
	require.Len(t, rows, 1)
	assert.Equal(t, SnapshotRetired, rows[0].State, "旧脚本拍出的快照不能以当前配置的身份就绪")
	assert.Equal(t, RetireConfigChanged, retireReason(rows[0]))
	assert.NotEqual(t, cur, rows[0].ConfigHash)
	assert.NotEqual(t, cur, derefStr(mustOrb(t, e, o.ID).ConfigHash))
}

// CP-1：密钥在 Plan 读值期间被改 → 读值前后的修改时间不一致，重读；哈希与最终读到的值同一份。
func TestCP1PlanRetriesOnSecretChange(t *testing.T) {
	e := newM11Env(t, nil, false, nil)
	ctx := context.Background()
	prj := e.project("app", nil)
	e.putSecret("TOKEN", "env", 10)
	v := &hookVault{hook: func(n int) {
		if n == 1 {
			e.putSecret("TOKEN", "env", 20)
		}
	}}
	d := &DefaultSetup{Store: e.st, Vault: v}
	prov, err := e.st.Q.GetProvider(ctx, e.provID)
	require.NoError(t, err)
	orb := db.Orb{ID: "orb-x", ProjectID: &prj, Type: "small", BaseImageID: prov.BaseImageID}
	plan, err := d.Plan(ctx, orb)
	require.NoError(t, err)
	assert.EqualValues(t, 2, v.calls.Load(), "修改时间前后不一致时重读")
	assert.Equal(t, e.currentHash(prj, "small"), plan.ConfigHash)
}

// CP-1：orb 建立之后、setup 之前 provider 换了基础镜像：哈希按 orb 建立时的镜像身份算，发布复核时退役。
func TestCP1BaseImageFromOrb(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, false, nil)
	prj := e.project("app", nil)
	e.svc.hookHelloStart = func(string) {
		_, err := e.st.DB.Exec(`UPDATE providers SET base_image_id = 'sha256:base-2' WHERE id = ?`, e.provID)
		require.NoError(t, err)
	}
	o := e.createReady(CreateParams{ProjectID: prj})
	assert.Equal(t, "sha256:base-1", o.BaseImageID)
	rows := e.snapshots(prj)
	require.Len(t, rows, 1)
	assert.Equal(t, SnapshotRetired, rows[0].State)
	assert.Equal(t, RetireConfigChanged, retireReason(rows[0]))
}

// CP-2：回收器看到 building 之后、清理之前构建者已发布：清理方没赢得 building 行，不删镜像。
func TestCP2DiscardAfterPublishKeepsImage(t *testing.T) {
	e := newM11Env(t, nil, false, nil)
	ctx := context.Background()
	prj := e.project("app", nil)
	orb := e.createReady(CreateParams{ProjectID: prj})
	snap := e.snapshots(prj)[0]
	require.Equal(t, SnapshotReady, snap.State)
	e.svc.discardBuild(ctx, e.provID, orb.ID, snap.ID)
	assert.Zero(t, e.deleteJobs(snap.ID))
	assert.Equal(t, SnapshotReady, e.snapshot(snap.ID).State)
}

// CP-3：冻结中的池 orb 被认领、冻结随后失败：不能直接回到 ready，要走刷新路径（setup.run + repo.update）再离池。
func TestCP3ClaimDuringFailedFreeze(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	o := e.warmOne(prj)
	_, moved, err := e.svc.transition(ctx, o.ID, StateReady, StateFreezing, nil)
	require.NoError(t, err)
	require.True(t, moved)
	claimed, ok := e.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
	require.True(t, ok)
	require.Equal(t, PoolClaimed, derefStr(claimed.PoolState))
	e.svc.onJobFinished(ctx, db.Job{Op: jobs.OpPause, OrbID: &o.ID, Params: `{"mode":"freeze"}`}, false)
	require.Eventually(t, func() bool {
		cur := mustOrb(t, e, o.ID)
		return cur.State == StateReady && cur.PoolState == nil
	}, 10*time.Second, 20*time.Millisecond)
	evs := e.stateEvents(o.ID)
	assert.Contains(t, evs, "freezing>connecting")
	assert.NotContains(t, evs, "freezing>ready")
	calls := e.setupCalls(o.ID)
	// warming：setup.run + 发布后令牌轮换、重连重跑的 setup.run（跳过）；认领：setup.run
	require.Len(t, calls, 3)
	require.NotNil(t, calls[2].Repo.Update)
	assert.True(t, *calls[2].Repo.Update)
}

// CP-3：重启时已认领却停在 ready 的池 orb（刷新被打断）由 recoverPool 补刷新。
func TestCP3RecoverClaimedReady(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	o := e.warmOne(prj)
	n, err := e.st.Q.SetOrbPoolState(ctx, db.SetOrbPoolStateParams{PoolState: new(PoolClaimed), ID: o.ID, Seen: PoolIdle})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	e.svc.recoverPool(ctx)
	require.Eventually(t, func() bool {
		cur := mustOrb(t, e, o.ID)
		return cur.State == StateReady && cur.PoolState == nil
	}, 10*time.Second, 20*time.Millisecond)
	assert.Len(t, e.setupCalls(o.ID), 3, "warming 两次（发布后重连重跑一次）+ 补刷新一次")
}

// CP-4：调低 pool.max_total 后，对账跨项目回收超额的池 orb（每个项目都未超过自己的目标）。
func TestCP4LoweredGlobalCap(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	a := e.warmProject("a", 1, nil)
	b := e.warmProject("b", 1, nil)
	e.svc.poolReconcile(ctx)
	all := append(e.pool(a), e.pool(b)...)
	require.Len(t, all, 2)
	for _, o := range all {
		e.waitState(o.ID, StateReady, 10*time.Second)
	}
	e.svc.cfg.Pool.MaxTotal = 1
	e.svc.poolReconcile(ctx)
	assert.Len(t, append(e.pool(a), e.pool(b)...), 1)
	var gone int
	for _, o := range all {
		if derefStr(mustOrb(t, e, o.ID).PoolState) == PoolRetiring {
			gone++
			e.waitState(o.ID, StateDeleted, 10*time.Second)
		}
	}
	assert.Equal(t, 1, gone)
	// 再对账：不超额，也不再新建
	e.svc.poolReconcile(ctx)
	assert.Len(t, append(e.pool(a), e.pool(b)...), 1)
}

// CP-5：冻结的池 orb 在项目 setup 改变后被普通唤醒（resume.run）：已应用的配置哈希不变，不能被当作新配置认领，
// 对账把它当作过期回收。
func TestCP5ResumeDoesNotRestamp(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	o := e.warmOne(prj)
	oldHash := derefStr(o.ConfigHash)
	require.NoError(t, e.svc.Pause(ctx, o.ID, PauseFreeze))
	e.waitState(o.ID, StateFrozen, 10*time.Second)
	e.setSetupScript(prj, "new setup dependency")
	require.NoError(t, e.svc.Wake(ctx, o.ID))
	e.waitState(o.ID, StateReady, 10*time.Second)
	assert.Contains(t, e.drv.orbd(o.ID).methods(), "resume.run")
	assert.Equal(t, oldHash, derefStr(mustOrb(t, e, o.ID).ConfigHash))
	_, ok := e.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
	assert.False(t, ok, "resume 不能证明旧 setup 符合新配置")
	e.svc.poolReconcile(ctx)
	assert.Equal(t, PoolRetiring, derefStr(mustOrb(t, e, o.ID).PoolState))
}

// S-P1-2：支持 setup-defer-resume 的 orbd：可能发布快照时 setup.run 只跑 setup，快照拍完再 resume.run，然后 ready。
func TestDeferResumeAroundSnapshot(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate, FeatureSetupDeferResume}, true, nil)
	ctx := context.Background()
	prj := e.project("app", nil)
	release := e.gateSnapshots()
	a, err := e.svc.Create(ctx, CreateParams{ProjectID: prj})
	require.NoError(t, err)
	e.waitBuilding(prj)
	o := e.drv.orbd(a.ID)
	assert.Equal(t, []string{"setup.run"}, o.methods(), "快照拍完之前不跑 resume")
	assert.Equal(t, StateSettingUp, mustOrb(t, e, a.ID).State)
	calls := e.setupCalls(a.ID)
	require.NotNil(t, calls[0].DeferResume)
	assert.True(t, *calls[0].DeferResume)
	release()
	e.waitState(a.ID, StateReady, 10*time.Second)
	// 发布后令牌轮换、旧 link 断开：新 link 重跑本阶段（setup 跳过、仍推迟），随后 resume
	assert.Equal(t, []string{"setup.run", "setup.run", "resume.run"}, o.methods())
	assert.True(t, *e.setupCalls(a.ID)[1].DeferResume)
	assert.Nil(t, e.setupCalls(a.ID)[2].DeferResume)
	require.Len(t, e.snapshots(prj), 1)
	assert.Equal(t, SnapshotReady, e.snapshots(prj)[0].State)

	// 快照发布失败也照样 resume
	e.drv.mu.Lock()
	e.drv.snapGate, e.drv.snapFail = nil, true
	e.drv.mu.Unlock()
	e.setSetupScript(prj, "make deps v2")
	b := e.createReady(CreateParams{ProjectID: prj})
	assert.Equal(t, []string{"setup.run", "resume.run"}, e.drv.orbd(b.ID).methods())
}

// S-P1-2：旧 orbd（无 setup-defer-resume）保持原样；认领的池 orb 不推迟（它不发布）。
func TestDeferResumeNotUsed(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	prj := e.project("app", nil)
	a := e.createReady(CreateParams{ProjectID: prj})
	assert.Equal(t, []string{"setup.run", "setup.run"}, e.drv.orbd(a.ID).methods(), "发布后令牌轮换、重连重跑一次")
	assert.Nil(t, e.setupCalls(a.ID)[0].DeferResume)

	p := newPoolEnv(t, nil)
	p.drv.mu.Lock()
	p.drv.features = []string{FeatureRepoUpdate, FeatureSetupDeferResume}
	p.drv.mu.Unlock()
	ctx := context.Background()
	wp := p.warmProject("app", 1, nil)
	o := p.warmOne(wp)
	p.mode.Store(modeRefresh)
	p.commit.Store("c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2")
	before := len(p.snapshots(wp))
	_, ok := p.svc.ClaimPool(ctx, CreateParams{ProjectID: wp})
	require.True(t, ok)
	require.Eventually(t, func() bool {
		cur := mustOrb(t, p, o.ID)
		return cur.State == StateReady && cur.PoolState == nil
	}, 10*time.Second, 20*time.Millisecond)
	calls := p.setupCalls(o.ID)
	require.Len(t, calls, 4, "warming：setup.run（推迟）+ 发布后重连重跑的 setup.run + resume.run；认领：setup.run")
	assert.True(t, *calls[0].DeferResume, "从未 ready 的池 orb 可能发布，推迟 resume")
	assert.Nil(t, calls[3].DeferResume, "认领的池 orb 不发布，不推迟 resume")
	assert.Len(t, p.snapshots(wp), before, "认领的池 orb 不发布刷新快照")
}

// S-P1-2 + X22：拍快照期间 link 断开（sysbox 停机快照）：发布一次后退回 connecting，新 link 重跑本阶段时不再发布，
// resume 随后补上。
func TestDeferResumeLinkLostDuringSnapshot(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate, FeatureSetupDeferResume}, true, nil)
	ctx := context.Background()
	prj := e.project("app", nil)
	release := e.gateSnapshots()
	a, err := e.svc.Create(ctx, CreateParams{ProjectID: prj})
	require.NoError(t, err)
	e.waitBuilding(prj)
	o := e.drv.orbd(a.ID)
	o.disconnect() // 容器停机：link 断开
	require.NoError(t, o.connect())
	release() // 提交完成、容器重新启动
	e.waitState(a.ID, StateReady, 10*time.Second)
	assert.Contains(t, e.stateEvents(a.ID), "setting_up>connecting")
	assert.Equal(t, []string{"setup.run", "setup.run", "resume.run"}, o.methods())
	calls := e.setupCalls(a.ID)
	require.Len(t, calls, 3)
	assert.True(t, *calls[1].DeferResume, "重跑仍推迟 resume")
	assert.Equal(t, 1, e.drv.countOps("snapshot"), "重跑不再发布")
	rows := e.snapshots(prj)
	require.Len(t, rows, 1)
	assert.Equal(t, SnapshotReady, rows[0].State)
}

// S-P2-1：增量刷新上报 setupChanged：来源快照退役 setup_changed，不以它为父发布；orb 照常 ready。
func TestSetupChangedRetiresSource(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	prj := e.project("app", nil)
	e.createReady(CreateParams{ProjectID: prj})
	s1 := e.snapshots(prj)[0]
	e.mode.Store(modeRefresh)
	e.setupChanged.Store(true)
	e.commit.Store("c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2")
	b := e.createReady(CreateParams{ProjectID: prj})
	assert.Equal(t, s1.ID, derefStr(b.SnapshotOf))
	assert.Equal(t, RetireSetupChanged, retireReason(e.snapshot(s1.ID)))
	assert.Len(t, e.snapshots(prj), 1, "不以 src 为父发布")
	assert.Equal(t, 1, e.drv.countOps("snapshot"))
	// 下一个 orb 从基础镜像起
	c := e.createReady(CreateParams{ProjectID: prj})
	assert.Nil(t, c.SnapshotOf)
}

// S-P2-2：构建期间清除快照 / 关闭项目快照：building 被作废，放行后不发布，构建者清理镜像，行随之删除。
func TestCancelBuildingSnapshot(t *testing.T) {
	for name, cancel := range map[string]func(e *m11Env, prj string){
		"clear": func(e *m11Env, prj string) {
			require.NoError(e.t, e.st.Q.RetireProjectSnapshotsByProject(context.Background(), db.RetireProjectSnapshotsByProjectParams{
				RetiredAt: time.Now().UnixMilli(), RetireReason: RetireManual, ProjectID: prj,
			}))
		},
		"disable": func(e *m11Env, prj string) {
			_, err := e.st.DB.Exec(`UPDATE projects SET snapshots = 0 WHERE id = ?`, prj)
			require.NoError(e.t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newM11Env(t, []string{FeatureRepoUpdate}, false, nil)
			ctx := context.Background()
			prj := e.project("app", nil)
			release := e.gateSnapshots()
			a, err := e.svc.Create(ctx, CreateParams{ProjectID: prj})
			require.NoError(t, err)
			row := e.waitBuilding(prj)
			cancel(e, prj)
			release()
			e.waitState(a.ID, StateReady, 10*time.Second)
			for _, r := range e.snapshots(prj) {
				assert.NotEqual(t, SnapshotReady, r.State)
			}
			require.Eventually(t, func() bool { return slices.Contains(e.deletedSnaps(), row.ID) }, 5*time.Second, 20*time.Millisecond)
			require.Eventually(t, func() bool {
				_, err := e.st.Q.GetProjectSnapshot(ctx, row.ID)
				return err != nil
			}, 5*time.Second, 20*time.Millisecond)
			assert.Equal(t, 1, e.deleteJobs(row.ID))
			// 回收器不再为它重复入队
			e.svc.sweepSnapshots(ctx)
			assert.Equal(t, 1, e.deleteJobs(row.ID))
		})
	}
}

// S-P2-3：认领 ready 的池 orb 轮换令牌（经 spawn）。
func TestPoolClaimReadyRotatesToken(t *testing.T) {
	e := newPoolEnv(t, nil)
	ctx := context.Background()
	prj := e.warmProject("app", 1, nil)
	o := e.warmOne(prj)
	spawns := e.drv.countOps("spawn")
	_, ok := e.svc.ClaimPool(ctx, CreateParams{ProjectID: prj})
	require.True(t, ok)
	require.Eventually(t, func() bool {
		cur := mustOrb(t, e, o.ID)
		return cur.State == StateReady && cur.PoolState == nil
	}, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, o.TokenGen+1, mustOrb(t, e, o.ID).TokenGen)
	assert.Equal(t, spawns+1, e.drv.countOps("spawn"))
}

// S-P2-3：可能被降级为环境变量的模型 key 进配置哈希；git 令牌等不进 orb 环境的密钥不进。
func TestConfigHashModelKeyStamps(t *testing.T) {
	// 不跑 Service.Run：退役且无引用的快照会被周期回收器删行，断言退役原因前不能让它抢先（同 TestProjectSnapshotImageMissing）
	e := newM11Env(t, []string{FeatureRepoUpdate}, false, nil)
	prj := e.project("app", nil)
	e.createReady(CreateParams{ProjectID: prj})
	s1 := e.snapshots(prj)[0]
	h0 := e.currentHash(prj, "small")
	e.putSecret("git/github.com/token", "proxy", time.Now().UnixMilli())
	assert.Equal(t, h0, e.currentHash(prj, "small"))
	e.putSecret("omp/models/custom/apiKey", "proxy", time.Now().UnixMilli())
	assert.NotEqual(t, h0, e.currentHash(prj, "small"))
	b := e.createReady(CreateParams{ProjectID: prj})
	assert.Nil(t, b.SnapshotOf)
	assert.Equal(t, RetireConfigChanged, retireReason(e.snapshot(s1.ID)))
}

// M24 阶段 ④：项目的 env 条目只改变本项目的配置哈希；项目的 git 令牌不进哈希（只经出站代理注入，不改 orb 环境）。
func TestConfigHashProjectEnvStamps(t *testing.T) {
	e := newM11Env(t, nil, false, nil)
	a, b := e.project("a", nil), e.project("b", nil)
	ha, hb := e.currentHash(a, "small"), e.currentHash(b, "small")
	e.putProjectSecret(a, "git/x.example/token", "proxy", time.Now().UnixMilli())
	assert.Equal(t, ha, e.currentHash(a, "small"), "项目 git 令牌不改变哈希")
	e.putProjectSecret(a, "FOO", "env", time.Now().UnixMilli())
	assert.NotEqual(t, ha, e.currentHash(a, "small"), "项目 env 条目改变本项目的哈希")
	assert.Equal(t, hb, e.currentHash(b, "small"), "别的项目的哈希不变")
}

// S-P2-4：单个快照超过 max_snapshot_gb：不发布、退役 quota 并删除，其它项目的快照不动。
func TestSnapshotOverSingleLimit(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, func(c *Config) { c.Snapshots.MaxSnapshotGB = 1 })
	a := e.project("a", nil)
	e.createReady(CreateParams{ProjectID: a})
	sa := e.snapshots(a)[0]
	require.Equal(t, SnapshotReady, sa.State)
	e.drv.mu.Lock()
	e.drv.snapBytes = new(int64(2) << 30)
	e.drv.mu.Unlock()
	var building atomic.Value
	e.svc.hookSnapshotBuilding = func(_, snapID string) { building.Store(snapID) }
	b := e.project("b", nil)
	ob := e.createReady(CreateParams{ProjectID: b})
	id, _ := building.Load().(string)
	require.NotEmpty(t, id)
	for _, r := range e.snapshots(b) {
		assert.NotEqual(t, SnapshotReady, r.State)
	}
	assert.Equal(t, 1, e.deleteJobs(id))
	require.Eventually(t, func() bool { return slices.Contains(e.deletedSnaps(), id) }, 5*time.Second, 20*time.Millisecond)
	evs, err := e.st.Q.ListOrbEvents(context.Background(), ob.ID)
	require.NoError(t, err)
	var cancelled bool
	for _, ev := range evs {
		cancelled = cancelled || (strings.Contains(ev.Detail, `"snapshot":"cancelled"`) && strings.Contains(ev.Detail, RetireQuota))
	}
	assert.True(t, cancelled, "构建以 quota 作废")
	assert.Equal(t, SnapshotReady, e.snapshot(sa.ID).State, "不挤掉其它项目")
}

// S-P2-5：orbd 上报的 baseCommit 不是十六进制提交 id 时入库为空。
func TestBaseCommitValidated(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	prj := e.project("app", nil)
	e.commit.Store("\x1b[2J\x1b[Hdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	o := e.createReady(CreateParams{ProjectID: prj})
	assert.Empty(t, e.snapshots(prj)[0].BaseCommit)
	assert.Nil(t, e.latestSetupRun(o.ID).BaseCommit)
	assert.Equal(t, "", validCommit(new("ABCDEF0123456789abcdef0123456789abcdef01")))
	sha256c := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assert.Equal(t, sha256c, validCommit(&sha256c))
}

// orb TLS：配置了 CA 时 spawn 参数带 orbCaPem；没配置时不带。
func TestSpawnCarriesOrbCA(t *testing.T) {
	e := newM11Env(t, nil, false, func(c *Config) { c.OrbCAPEM = "-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----\n" })
	o, err := e.svc.Create(context.Background(), CreateParams{})
	require.NoError(t, err)
	e.waitState(o.ID, StateReady, 10*time.Second)
	sp := e.lastSpawn(o.ID)
	require.NotNil(t, sp.OrbCaPem)
	assert.Equal(t, e.svc.cfg.OrbCAPEM, *sp.OrbCaPem)

	plain := newM11Env(t, nil, false, nil)
	p, err := plain.svc.Create(context.Background(), CreateParams{})
	require.NoError(t, err)
	plain.waitState(p.ID, StateReady, 10*time.Second)
	assert.Nil(t, plain.lastSpawn(p.ID).OrbCaPem)
}
