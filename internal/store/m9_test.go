package store

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestM9HostProviderAndCoordinatorOrb 覆盖内置 @host provider 的 upsert / 续租与协调 orb 查询。
func TestM9HostProviderAndCoordinatorOrb(t *testing.T) {
	ctx := context.Background()
	q := openTemp(t, t.TempDir()).Q

	require.NoError(t, q.UpsertHostProvider(ctx, db.UpsertHostProviderParams{
		ID: "prov_H1", TokenHash: "h1", Version: "v1", Capabilities: `{"freeze":true}`, OrbTypes: `[]`, CreatedAt: 1,
	}))
	_, err := q.DisableProvider(ctx, db.DisableProviderParams{ID: "prov_H1", DisabledAt: ptr(int64(5))})
	require.NoError(t, err)
	// 再次启动：新 id 与令牌被忽略，声明刷新，停用被撤销。
	require.NoError(t, q.UpsertHostProvider(ctx, db.UpsertHostProviderParams{
		ID: "prov_H2", TokenHash: "h2", Version: "v2", Capabilities: `{"freeze":true,"snapshot":false}`, OrbTypes: `[{"name":"host"}]`, CreatedAt: 9,
	}))
	p, err := q.GetProviderByName(ctx, "@host")
	require.NoError(t, err)
	assert.Equal(t, "prov_H1", p.ID)
	assert.Equal(t, "h1", p.TokenHash)
	assert.Equal(t, "host", p.Driver)
	assert.Equal(t, "host", p.DefaultType)
	assert.Equal(t, "v2", p.Version)
	assert.Equal(t, `[{"name":"host"}]`, p.OrbTypes)
	assert.Nil(t, p.DisabledAt)

	require.NoError(t, q.SetProviderLease(ctx, db.SetProviderLeaseParams{ID: p.ID, InstanceID: ptr("orynd"), LeaseUntil: ptr(int64(45))}))
	p, err = q.GetProvider(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "orynd", *p.InstanceID)
	assert.EqualValues(t, 45, *p.LeaseUntil)

	_, err = q.GetCoordinatorOrb(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)
	mk := func(id string, coord bool, at int64, state string) {
		require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{
			ID: id, ProviderID: p.ID, Type: "host", Image: "", State: state, StateChangedAt: at,
			TokenHash: "t-" + id, CreatedAt: at, Coordinator: coord,
		}))
	}
	mk("orb-normal", false, 1, "ready")
	mk("orb-old", true, 2, "deleted")
	mk("orb-cur", true, 3, "frozen")
	o, err := q.GetCoordinatorOrb(ctx)
	require.NoError(t, err)
	assert.Equal(t, "orb-cur", o.ID)
	assert.True(t, o.Coordinator)
}

// TestM9ThreadKindsAndSpaces 覆盖线程的 kind / space / shipped 列与 Space 查询。
func TestM9ThreadKindsAndSpaces(t *testing.T) {
	ctx := context.Background()
	q := openTemp(t, t.TempDir()).Q

	mk := func(id, kind string, space *string, parent *string, at int64) {
		require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{
			Kind: kind, ID: id, TitleSource: "auto", State: "idle", SessionDir: "/" + id, CreatedAt: at, UpdatedAt: at,
			SpaceID: space, ParentThreadID: parent,
		}))
	}
	assert.Error(t, q.CreateThread(ctx, db.CreateThreadParams{ID: "T-bad", TitleSource: "auto", State: "idle", SessionDir: "/b"}), "kind 必填")
	const spc = "spc_1"
	mk("T-chat", "space", ptr(spc), nil, 1)
	mk("T-asst", "assistant", nil, nil, 1)
	mk("T-w1", "work", ptr(spc), ptr("T-chat"), 2)
	mk("T-w2", "work", nil, ptr("T-chat"), 3)

	require.NoError(t, q.CreateSpace(ctx, db.CreateSpaceParams{ID: spc, Name: "m9", Goal: "g", ThreadID: "T-chat", CreatedAt: 1, UpdatedAt: 1}))
	assert.Error(t, q.CreateSpace(ctx, db.CreateSpaceParams{ID: "spc_2", Name: "m9", ThreadID: "T-asst", CreatedAt: 1, UpdatedAt: 1}), "名字唯一")
	sp, err := q.GetSpaceByThread(ctx, "T-chat")
	require.NoError(t, err)
	assert.Equal(t, spc, sp.ID)

	work := "work"
	list, err := q.ListThreads(ctx, db.ListThreadsParams{Kind: &work})
	require.NoError(t, err)
	assert.Len(t, list, 2)
	list, err = q.ListThreads(ctx, db.ListThreadsParams{SpaceID: ptr(spc)})
	require.NoError(t, err)
	assert.Len(t, list, 2, "Space 过滤含聊天线程本身（kind 由调用方另加）")
	all, err := q.ListThreads(ctx, db.ListThreadsParams{})
	require.NoError(t, err)
	assert.Len(t, all, 4)

	n, err := q.CountOpenChildren(ctx, "T-chat")
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	n, err = q.CountAgentThreadsSince(ctx, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	require.NoError(t, q.SetThreadShipped(ctx, db.SetThreadShippedParams{ID: "T-w1", ShippedAt: ptr(int64(7)), UpdatedAt: 7}))
	times, err := q.ListSpaceThreadTimes(ctx, spc)
	require.NoError(t, err)
	require.Len(t, times, 1)
	assert.EqualValues(t, 7, *times[0].ShippedAt)

	rows, err := q.SetThreadSpace(ctx, db.SetThreadSpaceParams{ID: "T-w2", SpaceID: ptr(spc), UpdatedAt: 8})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	require.NoError(t, q.ClearSpaceThreads(ctx, spc))
	w1, err := q.GetThread(ctx, "T-w1")
	require.NoError(t, err)
	assert.Nil(t, w1.SpaceID)
	chat, err := q.GetThread(ctx, "T-chat")
	require.NoError(t, err)
	assert.Equal(t, spc, *chat.SpaceID, "聊天线程不被清空")

	// 删除聊天线程级联删除 Space。
	_, err = q.DeleteThread(ctx, "T-chat")
	require.NoError(t, err)
	_, err = q.GetSpace(ctx, spc)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestM9ScheduleQueries 覆盖两种定时模式的约束、过滤、运行记录与回报、审计。
func TestM9ScheduleQueries(t *testing.T) {
	ctx := context.Background()
	q := openTemp(t, t.TempDir()).Q

	require.NoError(t, q.CreateProject(ctx, db.CreateProjectParams{ID: "prj_1", Name: "p1", CreatedAt: 1, UpdatedAt: 1}))
	for _, id := range []string{"T-1", "T-2"} {
		require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, TitleSource: "auto", State: "idle", SessionDir: "/" + id, CreatedAt: 1, UpdatedAt: 1}))
	}
	_, err := q.SetThreadSpace(ctx, db.SetThreadSpaceParams{ID: "T-2", SpaceID: ptr("spc_1"), UpdatedAt: 1})
	require.NoError(t, err)

	base := func(id, mode string) db.InsertScheduleParams {
		return db.InsertScheduleParams{
			ID: id, Mode: mode, Kind: "every", Spec: "1m0s", Timezone: "UTC", Prompt: "p", Enabled: true,
			NextRunAt: ptr(int64(100)), CreatedBy: "user", CreatedAt: 1, UpdatedAt: 1, Overlap: "skip", KeepLast: 5,
		}
	}
	// 线程模式：必须有 thread_id，不得带项目 / Space。
	bad := base("sch_bad1", "thread")
	assert.Error(t, q.InsertSchedule(ctx, bad), "thread 模式缺 thread_id")
	bad.ThreadID, bad.ProjectID = ptr("T-1"), ptr("prj_1")
	assert.Error(t, q.InsertSchedule(ctx, bad), "thread 模式带项目")
	bad = base("sch_bad2", "new_thread")
	bad.ThreadID = ptr("T-1")
	assert.Error(t, q.InsertSchedule(ctx, bad), "new_thread 带 thread_id")

	st1 := base("sch_t1", "thread")
	st1.ThreadID = ptr("T-1")
	require.NoError(t, q.InsertSchedule(ctx, st1))
	dup := base("sch_t1b", "thread")
	dup.ThreadID = ptr("T-1")
	assert.Error(t, q.InsertSchedule(ctx, dup), "每线程至多一个")
	st2 := base("sch_t2", "thread")
	st2.ThreadID, st2.NextRunAt = ptr("T-2"), ptr(int64(50))
	require.NoError(t, q.InsertSchedule(ctx, st2))
	np := base("sch_n1", "new_thread")
	np.Name, np.ProjectID, np.Overlap = "tick", ptr("prj_1"), "allow"
	require.NoError(t, q.InsertSchedule(ctx, np))
	nn := base("sch_n2", "new_thread")
	nn.Name, nn.SpaceID, nn.Enabled, nn.NextRunAt = "free", ptr("spc_1"), false, nil
	require.NoError(t, q.InsertSchedule(ctx, nn))

	got, err := q.GetScheduleByProjectName(ctx, db.GetScheduleByProjectNameParams{Name: "tick", ProjectID: ptr("prj_1")})
	require.NoError(t, err)
	assert.Equal(t, "sch_n1", got.ID)
	got, err = q.GetScheduleByProjectName(ctx, db.GetScheduleByProjectNameParams{Name: "free"})
	require.NoError(t, err)
	assert.Equal(t, "sch_n2", got.ID)
	_, err = q.GetScheduleByProjectName(ctx, db.GetScheduleByProjectNameParams{Name: "tick"})
	assert.ErrorIs(t, err, sql.ErrNoRows)

	ids := func(p db.ListSchedulesParams) []string {
		l, err := q.ListSchedules(ctx, p)
		require.NoError(t, err)
		var out []string
		for _, s := range l {
			out = append(out, s.ID)
		}
		return out
	}
	assert.Equal(t, []string{"sch_n1", "sch_n2", "sch_t1", "sch_t2"}, ids(db.ListSchedulesParams{}), "created_at 相同按 id")
	assert.Equal(t, []string{"sch_t1", "sch_t2"}, ids(db.ListSchedulesParams{Mode: ptr("thread")}))
	assert.Equal(t, []string{"sch_n1"}, ids(db.ListSchedulesParams{ProjectID: ptr("prj_1")}))
	assert.Equal(t, []string{"sch_n2"}, ids(db.ListSchedulesParams{NoProject: true}))
	assert.Equal(t, []string{"sch_n2", "sch_t2"}, ids(db.ListSchedulesParams{SpaceID: ptr("spc_1")}), "Space 的项目定时 + Space 线程上的线程内定时")
	assert.Equal(t, []string{"sch_t1"}, ids(db.ListSchedulesParams{ThreadID: ptr("T-1")}))

	n, err := q.CountEnabledSchedules(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
	due, err := q.ListDueSchedules(ctx, 60)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "sch_t2", due[0].ID)
	minNext, err := q.MinNextRun(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 50, minNext)

	sums, err := q.ListScheduleSummaries(ctx)
	require.NoError(t, err)
	assert.Len(t, sums, 2)
	sum, err := q.GetScheduleSummary(ctx, "T-1")
	require.NoError(t, err)
	assert.Equal(t, "sch_t1", sum.ID)

	// 运行：未结束计数、needs_input 开关、裁剪不碰未结束的运行。
	run := func(id string, at int64, status string) {
		require.NoError(t, q.InsertScheduleRun(ctx, db.InsertScheduleRunParams{
			ID: id, ScheduleID: "sch_n1", Trigger: "schedule", ScheduledFor: at, StartedAt: at, Status: status,
		}))
	}
	run("srn_1", 1, "completed")
	run("srn_2", 2, "failed")
	run("srn_3", 3, "needs_input")
	run("srn_4", 4, "pending")
	open, err := q.CountOpenScheduleRuns(ctx, db.CountOpenScheduleRunsParams{ScheduleID: "sch_n1"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, open)
	open, err = q.CountOpenScheduleRuns(ctx, db.CountOpenScheduleRunsParams{ScheduleID: "sch_n1", IncludeNeedsInput: true})
	require.NoError(t, err)
	assert.EqualValues(t, 2, open)

	require.NoError(t, q.SetScheduleRunThread(ctx, db.SetScheduleRunThreadParams{ID: "srn_4", ThreadID: ptr("T-2")}))
	require.NoError(t, q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{ID: "srn_4", Status: "running", DeliveredAt: ptr(int64(5))}))
	require.NoError(t, q.SetScheduleRunStatus(ctx, db.SetScheduleRunStatusParams{ID: "srn_4", Status: "completed", FinishedAt: ptr(int64(6))}))
	r4, err := q.GetScheduleRun(ctx, "srn_4")
	require.NoError(t, err)
	assert.Equal(t, "completed", r4.Status)
	assert.EqualValues(t, 5, *r4.DeliveredAt, "NULL 参数保留原值")
	assert.Equal(t, "T-2", *r4.ThreadID)

	require.NoError(t, q.TrimScheduleRuns(ctx, db.TrimScheduleRunsParams{ScheduleID: "sch_n1", Keep: 1}))
	runs, err := q.ListScheduleRuns(ctx, db.ListScheduleRunsParams{ScheduleID: "sch_n1", Limit: 100})
	require.NoError(t, err)
	var runIDs []string
	for _, r := range runs {
		runIDs = append(runIDs, r.ID)
	}
	assert.Equal(t, []string{"srn_4", "srn_3"}, runIDs)
	openRuns, err := q.ListOpenScheduleRuns(ctx)
	require.NoError(t, err)
	require.Len(t, openRuns, 1)
	assert.Equal(t, "srn_3", openRuns[0].ID)

	// 定义更新保留目标，清零失败数。
	require.NoError(t, q.SetScheduleFailures(ctx, db.SetScheduleFailuresParams{ID: "sch_n1", Failures: 2}))
	rows, err := q.UpdateScheduleDef(ctx, db.UpdateScheduleDefParams{
		ID: "sch_n1", Name: "tock", Kind: "cron", Spec: "0 9 * * *", Timezone: "Asia/Shanghai", Prompt: "q", Enabled: true,
		Overlap: "skip", KeepLast: 3, CreatedBy: "agent", UpdatedAt: 9,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)
	s1, err := q.GetSchedule(ctx, "sch_n1")
	require.NoError(t, err)
	assert.Equal(t, "prj_1", *s1.ProjectID)
	assert.EqualValues(t, 0, s1.Failures)
	assert.EqualValues(t, 3, s1.KeepLast)
	assert.Equal(t, "agent", s1.CreatedBy, "定义更新写入执行替换的一方")

	// 线程删除级联删除其线程内定时；项目删除级联删除项目定时。
	_, err = q.DeleteThread(ctx, "T-1")
	require.NoError(t, err)
	_, err = q.GetScheduleByThread(ctx, "T-1")
	assert.ErrorIs(t, err, sql.ErrNoRows)
	_, err = q.DeleteProject(ctx, "prj_1")
	require.NoError(t, err)
	_, err = q.GetSchedule(ctx, "sch_n1")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// 回报：upsert 改写目标。
	require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{Kind: "assistant", ID: "T-a", TitleSource: "auto", State: "idle", SessionDir: "/a", CreatedAt: 1, UpdatedAt: 1}))
	require.NoError(t, q.UpsertThreadReport(ctx, db.UpsertThreadReportParams{ThreadID: "T-2", ReportTo: "T-a", RequestedAt: 1}))
	require.NoError(t, q.UpsertThreadReport(ctx, db.UpsertThreadReportParams{ThreadID: "T-2", ReportTo: "T-a", RequestedAt: 2}))
	rep, err := q.GetThreadReport(ctx, "T-2")
	require.NoError(t, err)
	assert.EqualValues(t, 2, rep.RequestedAt)
	require.NoError(t, q.DeleteThreadReport(ctx, "T-2"))
}

// TestM9ScheduleThreadsAndCoordActions 覆盖保留策略的查询与审计记录。
func TestM9ScheduleThreadsAndCoordActions(t *testing.T) {
	ctx := context.Background()
	q := openTemp(t, t.TempDir()).Q

	require.NoError(t, q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "p", TokenHash: "h", CreatedAt: 1}))
	require.NoError(t, q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-1", ProviderID: "prov_1", Type: "small", State: "ready", TokenHash: "t1", CreatedAt: 1}))
	for i, id := range []string{"T-r1", "T-r2", "T-r3"} {
		at := int64(i + 1)
		require.NoError(t, q.CreateThread(ctx, db.CreateThreadParams{
			Kind: "work", ID: id, OrbID: ptr("orb-1"), TitleSource: "auto", State: "idle", SessionDir: "/" + id,
			CreatedAt: at, UpdatedAt: at, ScheduleID: ptr("sch_1"), ScheduleRun: ptr(at),
		}))
	}
	th, err := q.ListScheduleThreads(ctx, "sch_1")
	require.NoError(t, err)
	require.Len(t, th, 3)
	assert.Equal(t, "T-r3", th[0].ID)
	assert.Equal(t, "orb-1", *th[0].OrbID)
	n, err := q.CountUnarchivedThreadsByOrb(ctx, "orb-1")
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
	r1, err := q.GetThread(ctx, "T-r1")
	require.NoError(t, err)
	assert.Equal(t, "sch_1", *r1.ScheduleID)
	assert.EqualValues(t, 1, *r1.ScheduleRun)

	act := func(id string, ts int64, actor, target *string) {
		require.NoError(t, q.InsertCoordAction(ctx, db.InsertCoordActionParams{
			ID: id, Ts: ts, ActorKind: "assistant", ActorThreadID: actor, Origin: "owner", Action: "send_message",
			TargetThreadID: target, Summary: "hi", Ok: true,
		}))
	}
	act("cac_1", 1, ptr("T-a"), ptr("T-r1"))
	act("cac_2", 2, ptr("T-b"), ptr("T-r2"))
	act("cac_3", 3, ptr("T-r1"), nil)
	assert.Error(t, q.InsertCoordAction(ctx, db.InsertCoordActionParams{ID: "cac_x", ActorKind: "robot", Origin: "owner", Action: "a"}))

	list, err := q.ListCoordActions(ctx, db.ListCoordActionsParams{Limit: 50})
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, "cac_3", list[0].ID)
	list, err = q.ListCoordActions(ctx, db.ListCoordActionsParams{ThreadID: ptr("T-r1"), Limit: 50})
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, []string{"cac_3", "cac_1"}, []string{list[0].ID, list[1].ID})

	require.NoError(t, q.TrimCoordActions(ctx, 3))
	list, err = q.ListCoordActions(ctx, db.ListCoordActionsParams{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

// TestM9MigrationDownUp 回滚 0015 再重新应用：Down 段可执行，且回滚后 threads / orbs 恢复原列。
func TestM9MigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)

	r, err := p.Down(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 15, r.Source.Version)
	var n int
	require.NoError(t, s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('threads') WHERE name IN ('kind', 'space_id', 'shipped_at', 'schedule_id', 'schedule_run')`).Scan(&n))
	assert.Zero(t, n)
	require.NoError(t, s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('spaces', 'schedules', 'schedule_runs', 'thread_reports', 'coord_actions')`).Scan(&n))
	assert.Zero(t, n)

	_, err = p.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('orbs') WHERE name = 'coordinator'`).Scan(&n))
	assert.Equal(t, 1, n)
}
