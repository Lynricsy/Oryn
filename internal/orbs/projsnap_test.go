package orbs

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- M11a 夹具：带仓库的项目 setup、模拟 orbd 完成标记与 repo.update 的假 setup 应答 ----

// projSetup 给项目 orb 下发仓库（repo.update 只在有仓库时出现）。
type projSetup struct{}

func (projSetup) Plan(_ context.Context, orb db.Orb) (SetupPlan, error) {
	plan := SetupPlan{Params: apigen.LinkCtlSetupRunParams{Env: map[string]string{}, GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}}}
	if orb.ProjectID != nil {
		plan.Params.Repo = &apigen.LinkCtlSetupRepo{URL: "https://example.com/o/r"}
	}
	return plan, nil
}

// 假 orbd 的 setup 行为（m11Env.mode）。
const (
	modeHit         = "hit"          // 快照里的检出就是基线：跳过 setup
	modeRefresh     = "refresh"      // 基线前进：增量重跑 setup 成功
	modeRefreshFail = "refresh_fail" // 基线前进：增量重跑失败
	modeFail        = "fail"         // 全新 setup 失败
)

type m11Env struct {
	*harness
	drv *fakeDriver

	mode   atomic.Value // string
	commit atomic.Value // string
	// block 非 nil 时 setup.run 阻塞到它关闭（模拟 setup 进行中）。
	block atomic.Value // chan struct{}

	mu      sync.Mutex
	markers map[string]bool // orb → setup.done 标记已在
	setups  map[string][]apigen.LinkCtlSetupRunParams
}

func defaultSnapCfg() config.Snapshots {
	return config.Snapshots{Enabled: true, MaxAge: config.Duration(72 * time.Hour), MaxDepth: 16, MinSetup: config.Duration(time.Second), MaxTotalGB: 40}
}

func newM11Env(t *testing.T, features []string, run bool, mod func(*Config)) *m11Env {
	t.Helper()
	drv := newFakeDriver()
	drv.baseImage = "sha256:base-1"
	drv.features = features
	drv.snapBytes = new(int64(1000))
	e := &m11Env{drv: drv, markers: map[string]bool{}, setups: map[string][]apigen.LinkCtlSetupRunParams{}}
	e.mode.Store(modeHit)
	e.commit.Store("c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1")
	drv.onSetup = e.onSetup
	e.harness = newHarnessRun(t, drv, projSetup{}, func(c *Config) {
		c.Snapshots = defaultSnapCfg()
		if mod != nil {
			mod(c)
		}
	}, run)
	require.Eventually(t, func() bool {
		p, err := e.st.Q.GetProvider(context.Background(), e.provID)
		return err == nil && p.BaseImageID != ""
	}, 5*time.Second, 20*time.Millisecond)
	return e
}

// spawnedFrom 返回 orb 最近一次 spawn 的快照与该快照是否「不存在」。
func (e *m11Env) spawnedFrom(orbID string) (string, bool) {
	e.drv.mu.Lock()
	defer e.drv.mu.Unlock()
	for i := len(e.drv.spawns) - 1; i >= 0; i-- {
		sp := e.drv.spawns[i]
		if sp.Env.OrynOrbID != orbID {
			continue
		}
		if sp.SnapshotID == nil {
			return "", false
		}
		return *sp.SnapshotID, e.drv.missing[*sp.SnapshotID]
	}
	return "", false
}

// onSetup 模拟 orbd 的 setup / resume：完成标记来自快照（镜像存在时）或本 orb 上一次成功的 setup。
func (e *m11Env) onSetup(orbID, method string, p apigen.LinkCtlSetupRunParams) apigen.LinkCtlSetupRunResult {
	if ch, _ := e.block.Load().(chan struct{}); ch != nil && method == "setup.run" {
		<-ch
	}
	snap, missing := e.spawnedFrom(orbID)
	e.mu.Lock()
	e.setups[orbID] = append(e.setups[orbID], p)
	prepared := e.markers[orbID] || (snap != "" && !missing)
	e.mu.Unlock()
	mode := e.mode.Load().(string)
	commit := e.commit.Load().(string)
	res := apigen.LinkCtlSetupRunResult{Ok: true, DurationMs: 5000, Prepared: new(prepared), BaseCommit: new(commit)}
	switch {
	case method == "resume.run":
		res.DurationMs = 10
	case !prepared:
		res.SetupRan = true
		res.Ok = mode != modeFail
	case p.Repo != nil && p.Repo.Update != nil && *p.Repo.Update && (mode == modeRefresh || mode == modeRefreshFail):
		res.SetupRan, res.RepoUpdated = true, new(true)
		res.Ok = mode == modeRefresh
	}
	if res.Ok && method == "setup.run" {
		e.mu.Lock()
		e.markers[orbID] = true
		e.mu.Unlock()
	}
	phase := apigen.SetupPhaseSetup
	if method == "resume.run" {
		phase = apigen.SetupPhaseResume
	}
	if o := e.drv.orbd(orbID); o != nil {
		o.mu.Lock()
		ctl := o.ctl
		o.mu.Unlock()
		if ctl != nil {
			_ = ctl.Notify("setup.result", apigen.LinkCtlSetupResultParams{
				Phase: phase, Trigger: apigen.SetupTriggerAuto, Ok: res.Ok, SetupRan: res.SetupRan, DurationMs: res.DurationMs,
				StartedAt: time.Now().UnixMilli(), Prepared: res.Prepared, BaseCommit: res.BaseCommit, RepoUpdated: res.RepoUpdated,
			})
		}
	}
	return res
}

// deletedSnaps 返回 delete_snapshot 任务删过的快照。
func (e *m11Env) deletedSnaps() []string {
	e.drv.mu.Lock()
	defer e.drv.mu.Unlock()
	return append([]string(nil), e.drv.deletedSnaps...)
}

func (e *m11Env) setupCalls(orbID string) []apigen.LinkCtlSetupRunParams {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]apigen.LinkCtlSetupRunParams(nil), e.setups[orbID]...)
}

// project 建一个带仓库与 setup 脚本的项目。
func (e *m11Env) project(name string, mod func(*db.CreateProjectParams)) string {
	e.t.Helper()
	id := ids.New("prj")
	now := time.Now().UnixMilli()
	p := db.CreateProjectParams{ID: id, Name: name, RepoUrl: new("https://example.com/o/r"), SetupScript: new("make deps"), CreatedAt: now, UpdatedAt: now}
	if mod != nil {
		mod(&p)
	}
	require.NoError(e.t, e.st.Q.CreateProject(context.Background(), p))
	return id
}

func (e *m11Env) setSetupScript(prjID, script string) {
	e.t.Helper()
	_, err := e.st.DB.Exec(`UPDATE projects SET setup_script = ? WHERE id = ?`, script, prjID)
	require.NoError(e.t, err)
}

func (e *m11Env) snapshots(prjID string) []db.ProjectSnapshot {
	e.t.Helper()
	rows, err := e.st.Q.ListProjectSnapshotsByProject(context.Background(), prjID)
	require.NoError(e.t, err)
	out := make([]db.ProjectSnapshot, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ProjectSnapshot)
	}
	// 按创建顺序（最早在前）
	slices.Reverse(out)
	return out
}

func (e *m11Env) snapshot(id string) db.ProjectSnapshot {
	e.t.Helper()
	r, err := e.st.Q.GetProjectSnapshot(context.Background(), id)
	require.NoError(e.t, err)
	return r
}

func (e *m11Env) createReady(p CreateParams) db.Orb {
	e.t.Helper()
	orb, err := e.svc.Create(context.Background(), p)
	require.NoError(e.t, err)
	return e.waitState(orb.ID, StateReady, 10*time.Second)
}

func (e *m11Env) lastSpawn(orbID string) apigen.JobSpawnParams {
	e.t.Helper()
	e.drv.mu.Lock()
	defer e.drv.mu.Unlock()
	for i := len(e.drv.spawns) - 1; i >= 0; i-- {
		if e.drv.spawns[i].Env.OrynOrbID == orbID {
			return e.drv.spawns[i]
		}
	}
	e.t.Fatalf("orb %s 没有 spawn", orbID)
	return apigen.JobSpawnParams{}
}

func (e *m11Env) latestSetupRun(orbID string) db.LatestOrbSetupRunRow {
	e.t.Helper()
	var row db.LatestOrbSetupRunRow
	require.Eventually(e.t, func() bool {
		var err error
		row, err = e.st.Q.LatestOrbSetupRun(context.Background(), db.LatestOrbSetupRunParams{OrbID: orbID, Phase: "setup"})
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	return row
}

func retireReason(r db.ProjectSnapshot) string { return derefStr(r.RetireReason) }

// ---- 测试 ----

// 计划 a1 2.x「验证」orbs 部分：发布、命中、刷新、刷新失败、配置变化、过期、回收；fork 快照的回收路径不变。
func TestProjectSnapshotLifecycle(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	e.quiet.Store(false) // 拨快时钟时不让空闲评估冻结 orb
	ctx := context.Background()
	prj := e.project("app", nil)

	// 首个项目 orb：全新 setup 后发布 depth=0 的快照
	a := e.createReady(CreateParams{ProjectID: prj})
	assert.Nil(t, e.lastSpawn(a.ID).SnapshotID)
	assert.NotEmpty(t, derefStr(a.ConfigHash))
	require.Nil(t, e.setupCalls(a.ID)[0].Repo.Update, "从基础镜像起的新 orb 不置 repo.update")
	rows := e.snapshots(prj)
	require.Len(t, rows, 1)
	s1 := rows[0]
	assert.Equal(t, SnapshotReady, s1.State)
	assert.EqualValues(t, 0, s1.Depth)
	assert.EqualValues(t, 1000, deref64(s1.SizeBytes))
	assert.Equal(t, "c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1", s1.BaseCommit)
	assert.Equal(t, a.ID, derefStr(s1.SourceOrbID))
	assert.Equal(t, derefStr(a.ConfigHash), s1.ConfigHash)
	assert.Equal(t, 1, e.drv.countOps("snapshot"))

	// 第二个 orb：命中快照（snapshotOptional）、repo.update、prepared 且没跑 setup，不再拍快照
	b := e.createReady(CreateParams{ProjectID: prj})
	sp := e.lastSpawn(b.ID)
	require.NotNil(t, sp.SnapshotID)
	assert.Equal(t, s1.ID, *sp.SnapshotID)
	require.NotNil(t, sp.SnapshotOptional)
	assert.True(t, *sp.SnapshotOptional)
	assert.Equal(t, s1.ID, derefStr(b.SnapshotOf))
	assert.Equal(t, s1.ID, b.Image)
	calls := e.setupCalls(b.ID)
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].Repo.Update)
	assert.True(t, *calls[0].Repo.Update)
	run := e.latestSetupRun(b.ID)
	assert.True(t, run.Prepared)
	assert.EqualValues(t, 0, run.SetupRan)
	assert.EqualValues(t, 1, e.snapshot(s1.ID).UseCount)
	assert.Equal(t, 1, e.drv.countOps("snapshot"))
	v, err := View(ctx, e.st.Q, b)
	require.NoError(t, err)
	require.NotNil(t, v.SnapshotKind)
	assert.Equal(t, apigen.OrbSnapshotKindProject, *v.SnapshotKind)
	require.NotNil(t, v.LastSetup)
	assert.True(t, *v.LastSetup.Prepared)

	// 基线前进：增量重跑成功 → depth=1、血统起点不变，旧快照 superseded
	e.mode.Store(modeRefresh)
	e.commit.Store("c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2")
	c := e.createReady(CreateParams{ProjectID: prj})
	assert.Equal(t, s1.ID, derefStr(c.SnapshotOf))
	rows = e.snapshots(prj)
	require.Len(t, rows, 2)
	assert.Equal(t, SnapshotRetired, rows[0].State)
	assert.Equal(t, RetireSuperseded, retireReason(rows[0]))
	s2 := rows[1]
	assert.Equal(t, SnapshotReady, s2.State)
	assert.EqualValues(t, 1, s2.Depth)
	assert.Equal(t, s1.LineageAt, s2.LineageAt)
	assert.Equal(t, "c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2", s2.BaseCommit)

	// 刷新失败 → 旧快照 refresh_failed，没有新行
	e.mode.Store(modeRefreshFail)
	d := e.createReady(CreateParams{ProjectID: prj})
	assert.Equal(t, s2.ID, derefStr(d.SnapshotOf))
	rows = e.snapshots(prj)
	require.Len(t, rows, 2)
	assert.Equal(t, RetireRefreshFailed, retireReason(e.snapshot(s2.ID)))
	assert.Equal(t, 2, e.drv.countOps("snapshot"))

	// 没有 ready 快照 → 全新 setup，新血统
	e.mode.Store(modeHit)
	ev := e.createReady(CreateParams{ProjectID: prj})
	assert.Nil(t, ev.SnapshotOf)
	rows = e.snapshots(prj)
	require.Len(t, rows, 3)
	s3 := rows[2]
	assert.Equal(t, SnapshotReady, s3.State)
	assert.EqualValues(t, 0, s3.Depth)
	// 让一台 orb 用上 s3（被引用的退役快照不会被回收器立刻删掉，便于检查退役原因）
	pin := e.createReady(CreateParams{ProjectID: prj})
	require.Equal(t, s3.ID, derefStr(pin.SnapshotOf))

	// 改项目 setup 脚本 → 下一次 Create 不带快照，旧行 config_changed
	e.setSetupScript(prj, "make deps && make more")
	f := e.createReady(CreateParams{ProjectID: prj})
	assert.Nil(t, e.lastSpawn(f.ID).SnapshotID)
	assert.Equal(t, RetireConfigChanged, retireReason(e.snapshot(s3.ID)))
	assert.NotEqual(t, derefStr(ev.ConfigHash), derefStr(f.ConfigHash))
	rows = e.snapshots(prj)
	s4 := rows[len(rows)-1]
	require.Equal(t, SnapshotReady, s4.State)

	// 退役快照在最后一个引用它的 orb 删除后入队 delete_snapshot，任务成功后行消失
	require.NoError(t, e.svc.Delete(ctx, pin.ID))
	e.waitState(pin.ID, StateDeleted, 10*time.Second)
	require.Eventually(t, func() bool {
		_, err := e.st.Q.GetProjectSnapshot(ctx, s3.ID)
		return err != nil
	}, 5*time.Second, 20*time.Millisecond)
	assert.Contains(t, e.deletedSnaps(), s3.ID)

	// 时钟拨过 72 小时 → expired（先让一台 orb 用上 s4，退役后不至于立刻被回收）
	h := e.createReady(CreateParams{ProjectID: prj})
	require.Equal(t, s4.ID, derefStr(h.SnapshotOf))
	e.skew.Store(int64(73 * time.Hour))
	require.Eventually(t, func() bool { return retireReason(e.snapshot(s4.ID)) == RetireExpired }, 5*time.Second, 20*time.Millisecond)

	// 仍被 orb 引用的退役快照不回收；最后一个引用它的 orb 删除后才入队 delete_snapshot
	_, err = e.st.Q.GetProjectSnapshot(ctx, s1.ID)
	require.NoError(t, err, "s1 仍被 b、c 引用")
	for _, o := range []db.Orb{b, c} {
		require.NoError(t, e.svc.Delete(ctx, o.ID))
		e.waitState(o.ID, StateDeleted, 10*time.Second)
	}
	require.Eventually(t, func() bool {
		_, err := e.st.Q.GetProjectSnapshot(ctx, s1.ID)
		return err != nil
	}, 5*time.Second, 20*time.Millisecond)

	// fork 快照：spawn 不带 snapshotOptional，删除最后一个使用者后由 releaseSnapshot 立即回收
	fork, err := e.svc.Snapshot(ctx, a.ID)
	require.NoError(t, err)
	g := e.createReady(CreateParams{SnapshotOf: a.ID, SnapshotID: fork})
	assert.Nil(t, e.lastSpawn(g.ID).SnapshotOptional)
	v, err = View(ctx, e.st.Q, g)
	require.NoError(t, err)
	assert.Equal(t, apigen.OrbSnapshotKindFork, *v.SnapshotKind)
	require.NoError(t, e.svc.Delete(ctx, g.ID))
	e.waitState(g.ID, StateDeleted, 10*time.Second)
	require.Eventually(t, func() bool {
		e.drv.mu.Lock()
		defer e.drv.mu.Unlock()
		return slices.Contains(e.drv.deletedSnaps, fork)
	}, 5*time.Second, 20*time.Millisecond)
}

// provider 上快照镜像不存在：spawn 退回基础镜像（snapshotOptional），prepared=false → 退役 image_missing 并重建新血统。
func TestProjectSnapshotImageMissing(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	prj := e.project("app", nil)
	e.createReady(CreateParams{ProjectID: prj})
	s1 := e.snapshots(prj)[0]
	e.drv.mu.Lock()
	e.drv.missing[s1.ID] = true
	e.drv.mu.Unlock()
	b := e.createReady(CreateParams{ProjectID: prj})
	assert.Equal(t, s1.ID, derefStr(b.SnapshotOf))
	assert.Equal(t, RetireImageMissing, retireReason(e.snapshot(s1.ID)))
	rows := e.snapshots(prj)
	require.Len(t, rows, 2)
	assert.Equal(t, SnapshotReady, rows[1].State)
	assert.EqualValues(t, 0, rows[1].Depth)
}

// 旧 orbd（hello 不含 repo-update）：来自项目快照的 orb 不收到 repo.update（X12）。
func TestProjectSnapshotOldOrbdNoRepoUpdate(t *testing.T) {
	e := newM11Env(t, nil, true, nil)
	prj := e.project("app", nil)
	e.createReady(CreateParams{ProjectID: prj})
	b := e.createReady(CreateParams{ProjectID: prj})
	require.NotNil(t, b.SnapshotOf)
	calls := e.setupCalls(b.ID)
	require.Len(t, calls, 1)
	assert.Nil(t, calls[0].Repo.Update)
}

// 两个 orb 并发 setup：同键至多一次构建，另一台直接进 ready。
func TestProjectSnapshotConcurrentDedupe(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	prj := e.project("app", nil)
	gate := make(chan struct{})
	e.drv.mu.Lock()
	e.drv.snapGate = gate
	e.drv.mu.Unlock()
	ctx := context.Background()
	a, err := e.svc.Create(ctx, CreateParams{ProjectID: prj})
	require.NoError(t, err)
	b, err := e.svc.Create(ctx, CreateParams{ProjectID: prj})
	require.NoError(t, err)
	// 构建者卡在 snapshot 任务里（setting_up），另一台不等它
	var builder, other string
	require.Eventually(t, func() bool {
		oa, _ := e.st.Q.GetOrb(ctx, a.ID)
		ob, _ := e.st.Q.GetOrb(ctx, b.ID)
		switch {
		case oa.State == StateReady && ob.State == StateSettingUp:
			builder, other = b.ID, a.ID
		case ob.State == StateReady && oa.State == StateSettingUp:
			builder, other = a.ID, b.ID
		default:
			return false
		}
		return e.drv.countOps("snapshot") == 1
	}, 10*time.Second, 20*time.Millisecond)
	rows := e.snapshots(prj)
	require.Len(t, rows, 1)
	assert.Equal(t, SnapshotBuilding, rows[0].State)
	assert.Equal(t, builder, derefStr(rows[0].SourceOrbID))
	close(gate)
	e.waitState(builder, StateReady, 10*time.Second)
	assert.Equal(t, 1, e.drv.countOps("snapshot"))
	assert.Equal(t, SnapshotReady, e.snapshot(rows[0].ID).State)
	_ = other
}

// snapshot 任务失败：building 行删除、入队一次清理，orb 照常进入 ready。
func TestProjectSnapshotCommitFailure(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	e.drv.mu.Lock()
	e.drv.snapFail = true
	e.drv.mu.Unlock()
	prj := e.project("app", nil)
	a := e.createReady(CreateParams{ProjectID: prj})
	assert.Empty(t, e.snapshots(prj))
	require.Eventually(t, func() bool {
		e.drv.mu.Lock()
		defer e.drv.mu.Unlock()
		return len(e.drv.deletedSnaps) == 1
	}, 5*time.Second, 20*time.Millisecond)
	evs, err := e.st.Q.ListOrbEvents(context.Background(), a.ID)
	require.NoError(t, err)
	var failed bool
	for _, ev := range evs {
		var d map[string]any
		_ = json.Unmarshal([]byte(ev.Detail), &d)
		failed = failed || d["snapshot"] == "failed"
	}
	assert.True(t, failed)
}

// 快的 setup（< min_setup）、项目关了快照、provider 没有基础镜像身份：都不建快照。
func TestProjectSnapshotSkipped(t *testing.T) {
	e := newM11Env(t, []string{FeatureRepoUpdate}, true, func(c *Config) { c.Snapshots.MinSetup = config.Duration(time.Hour) })
	prj := e.project("fast", nil)
	e.createReady(CreateParams{ProjectID: prj})
	assert.Empty(t, e.snapshots(prj), "setup 短于 min_setup")

	e2 := newM11Env(t, []string{FeatureRepoUpdate}, true, nil)
	off := e2.project("off", func(p *db.CreateProjectParams) { p.Snapshots = new(false) })
	e2.createReady(CreateParams{ProjectID: off})
	assert.Empty(t, e2.snapshots(off), "项目关闭了快照")
	none := e2.createReady(CreateParams{})
	assert.Nil(t, none.ConfigHash, "无项目的 orb 没有配置哈希")
}

// 配额：超过 max_total_gb 时按 LRU 退役其它键的 ready 快照；剩下的都被 orb 引用着时拒绝建立。
func TestReserveSnapshotQuota(t *testing.T) {
	e := newM11Env(t, nil, false, func(c *Config) { c.Snapshots.MaxTotalGB = 1 })
	ctx := context.Background()
	prjA, prjB := e.project("a", nil), e.project("b", nil)
	const gb = int64(1) << 30
	mk := func(prj, typ string, size int64, usedAt int64) string {
		id := ids.Snapshot()
		_, err := e.st.Q.InsertBuildingProjectSnapshot(ctx, db.InsertBuildingProjectSnapshotParams{ID: id, ProjectID: prj, ProviderID: e.provID, Type: typ, ConfigHash: "h", LineageAt: 1, CreatedAt: 1})
		require.NoError(t, err)
		_, err = e.st.Q.PublishProjectSnapshot(ctx, db.PublishProjectSnapshotParams{ReadyAt: usedAt, SizeBytes: new(size), ID: id})
		require.NoError(t, err)
		return id
	}
	old := mk(prjA, "small", gb/2, 10)
	newer := mk(prjA, "large", gb/2, 20)
	mine := mk(prjB, "small", gb/2, 5) // 最老，但与要建立的键相同：不动
	assert.True(t, e.svc.reserveSnapshotQuota(ctx, e.provID, prjB, "small"))
	assert.Equal(t, RetireQuota, retireReason(e.snapshot(old)))
	assert.Equal(t, SnapshotReady, e.snapshot(newer).State)
	assert.Equal(t, SnapshotReady, e.snapshot(mine).State)

	// 被 orb 引用着的退役快照仍占配额：再也腾不出来 → false
	for _, id := range []string{old, newer} {
		orbID := ids.Orb()
		require.NoError(t, e.st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: orbID, ProviderID: e.provID, Type: "small", Image: id, State: StateReady,
			StateChangedAt: 1, TokenHash: ids.Hash(orbID), SnapshotOf: new(id), CreatedAt: 1}))
	}
	big := mk(prjA, "medium", gb, 30)
	assert.False(t, e.svc.reserveSnapshotQuota(ctx, e.provID, prjB, "small"))
	assert.Equal(t, RetireQuota, retireReason(e.snapshot(big)))
}

// 回收器：building 残留超过 15 分钟 → 删行并入队清理；退役快照的回收任务失败后 10 分钟内不重复入队。
func TestSweepSnapshots(t *testing.T) {
	e := newM11Env(t, nil, false, nil)
	ctx := context.Background()
	prj := e.project("a", nil)
	stale := ids.Snapshot()
	_, err := e.st.Q.InsertBuildingProjectSnapshot(ctx, db.InsertBuildingProjectSnapshotParams{ID: stale, ProjectID: prj, ProviderID: e.provID, Type: "small", ConfigHash: "h", LineageAt: 1, CreatedAt: time.Now().UnixMilli()})
	require.NoError(t, err)
	e.svc.sweepSnapshots(ctx)
	_, err = e.st.Q.GetProjectSnapshot(ctx, stale)
	require.NoError(t, err, "15 分钟内的 building 不动")
	e.skew.Store(int64(16 * time.Minute))
	e.svc.sweepSnapshots(ctx)
	_, err = e.st.Q.GetProjectSnapshot(ctx, stale)
	require.Error(t, err)
	require.Eventually(t, func() bool {
		e.drv.mu.Lock()
		defer e.drv.mu.Unlock()
		return slices.Contains(e.drv.deletedSnaps, stale)
	}, 5*time.Second, 20*time.Millisecond)

	// 退役快照：入队一次后，任务在途时不重复入队
	e.skew.Store(0)
	ret := ids.Snapshot()
	_, err = e.st.Q.InsertBuildingProjectSnapshot(ctx, db.InsertBuildingProjectSnapshotParams{ID: ret, ProjectID: prj, ProviderID: e.provID, Type: "large", ConfigHash: "h", LineageAt: 1, CreatedAt: 1})
	require.NoError(t, err)
	_, err = e.st.Q.PublishProjectSnapshot(ctx, db.PublishProjectSnapshotParams{ReadyAt: 1, ID: ret})
	require.NoError(t, err)
	require.True(t, e.svc.retireSnapshot(ctx, ret, RetireManual))
	e.svc.sweepSnapshots(ctx)
	row := e.snapshot(ret)
	require.NotNil(t, row.GcJobID)
	first := *row.GcJobID
	e.svc.sweepSnapshots(ctx)
	if r, err := e.st.Q.GetProjectSnapshot(ctx, ret); err == nil {
		assert.Equal(t, first, derefStr(r.GcJobID), "任务在途 / 刚入队时不重复入队")
	}
	// 任务成功后行消失
	done, err := e.q.Wait(ctx, first)
	require.NoError(t, err)
	assert.Equal(t, jobs.StateSucceeded, done.State)
	require.Eventually(t, func() bool {
		_, err := e.st.Q.GetProjectSnapshot(ctx, ret)
		return err != nil
	}, 5*time.Second, 20*time.Millisecond)
}
