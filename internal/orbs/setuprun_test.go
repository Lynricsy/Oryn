package orbs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func (o *fakeOrbd) ctlNow(t *testing.T) *link.Ctl {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	require.NotNil(t, o.ctl, "fake orbd 未连接")
	return o.ctl
}

func (h *harness) createProject(name string, setup, resume *string) db.Project {
	h.t.Helper()
	p := db.CreateProjectParams{ID: ids.New("prj"), Name: name, SetupScript: setup, ResumeScript: resume, CreatedAt: 1, UpdatedAt: 1}
	require.NoError(h.t, h.st.Q.CreateProject(context.Background(), p))
	prj, err := h.st.Q.GetProject(context.Background(), p.ID)
	require.NoError(h.t, err)
	return prj
}

func (h *harness) readyOrb(drv *fakeDriver, projectID string) (db.Orb, *link.Ctl) {
	h.t.Helper()
	orb, err := h.svc.Create(context.Background(), CreateParams{ProjectID: projectID})
	require.NoError(h.t, err)
	h.waitState(orb.ID, StateReady, 10*time.Second)
	return orb, drv.orbd(orb.ID).ctlNow(h.t)
}

func (h *harness) runs(orbID string, n int) []db.ListOrbSetupRunsRow {
	h.t.Helper()
	var rows []db.ListOrbSetupRunsRow
	require.Eventually(h.t, func() bool {
		var err error
		rows, err = h.st.Q.ListOrbSetupRuns(context.Background(), orbID)
		return err == nil && len(rows) == n
	}, 5*time.Second, 20*time.Millisecond, "期望 %d 条 setup 运行记录", n)
	return rows
}

func (h *harness) runLog(orbID, runID string) string {
	h.t.Helper()
	b, err := h.st.Q.GetOrbSetupRunLog(context.Background(), db.GetOrbSetupRunLogParams{ID: runID, OrbID: orbID})
	require.NoError(h.t, err)
	return string(b)
}

func sendLog(t *testing.T, c *link.Ctl, phase, data string) {
	t.Helper()
	require.NoError(t, c.Notify("setup.log", apigen.LinkCtlSetupLogParams{Phase: apigen.LinkCtlSetupLogParamsPhase(phase), Stream: "stdout", Data: data}))
}

// setup.log 按阶段缓冲、只保留尾部 256KiB；setup.result 把对应缓冲写入 orb_setup_runs，并推送 orb 更新。
func TestSetupResultRecordsRunsWithLogTail(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	orb, c := h.readyOrb(drv, "")

	// setup 阶段：clone 开头的输出 + 约 300KiB 带序号的脚本输出（超过 256KiB，开头被截掉）；中间夹着 resume 输出。
	sendLog(t, c, "clone", "CLONE-OUTPUT\n")
	line := strings.Repeat("x", 1000)
	for i := range 300 {
		sendLog(t, c, "setup", fmt.Sprintf("%04d %s\n", i, line))
		if i == 150 {
			sendLog(t, c, "resume", "RESUME-OUTPUT\n")
		}
	}
	sendLog(t, c, "setup", "LAST-SETUP-LINE\n")
	errMsg := "exit status 3"
	require.NoError(t, c.Notify("setup.result", apigen.LinkCtlSetupResultParams{
		Phase: apigen.SetupPhaseSetup, Trigger: apigen.SetupTriggerAuto, Ok: false, SetupRan: true, Error: &errMsg,
		DurationMs: 1234, StartedAt: 1000, HasRepoScript: true, HasProjectScript: false,
	}))
	rows := h.runs(orb.ID, 1)
	setup := rows[0]
	assert.Equal(t, "setup", setup.Phase)
	assert.Equal(t, "auto", setup.Trigger)
	assert.Zero(t, setup.Ok)
	assert.EqualValues(t, 1, setup.SetupRan)
	assert.EqualValues(t, 1, setup.HasRepoScript)
	assert.Zero(t, setup.HasProjectScript)
	assert.Equal(t, errMsg, *setup.Error)
	assert.EqualValues(t, 1000, setup.StartedAt)
	assert.GreaterOrEqual(t, setup.FinishedAt, int64(1000))

	logText := h.runLog(orb.ID, setup.ID)
	assert.LessOrEqual(t, len(logText), SetupLogTail)
	assert.Greater(t, len(logText), SetupLogTail-len(line)-8, "保留的是满额尾部")
	assert.True(t, strings.HasSuffix(logText, "0299 "+line+"\nLAST-SETUP-LINE\n"), "末尾是最后的输出")
	assert.NotContains(t, logText, "CLONE-OUTPUT", "超出 256KiB 的开头被截掉")
	assert.NotContains(t, logText, "RESUME-OUTPUT", "resume 输出不混入 setup 日志")
	// 保留下来的行按原顺序、无缺口
	var prev = -1
	for _, ln := range strings.Split(strings.TrimSuffix(logText, "\n"), "\n") {
		var n int
		if _, err := fmt.Sscanf(ln, "%04d ", &n); err != nil {
			continue
		}
		if prev >= 0 {
			require.Equal(t, prev+1, n, "日志行顺序")
		}
		prev = n
	}
	assert.Equal(t, 299, prev)

	// resume 阶段取自己的缓冲；未截断时原样保留
	require.NoError(t, c.Notify("setup.result", apigen.LinkCtlSetupResultParams{
		Phase: apigen.SetupPhaseResume, Trigger: apigen.SetupTriggerAuto, Ok: true, DurationMs: 5, StartedAt: 2000,
	}))
	rows = h.runs(orb.ID, 2)
	assert.Equal(t, "resume", rows[0].Phase)
	assert.Equal(t, "RESUME-OUTPUT\n", h.runLog(orb.ID, rows[0].ID))

	// 手动重跑：缓冲已在上次取出后清空，只含本次输出
	sendLog(t, c, "git", "git-config\n")
	sendLog(t, c, "setup", "rerun-ok\n")
	require.NoError(t, c.Notify("setup.result", apigen.LinkCtlSetupResultParams{
		Phase: apigen.SetupPhaseSetup, Trigger: apigen.SetupTriggerManual, Ok: true, SetupRan: true, DurationMs: 7, StartedAt: 3000,
	}))
	rows = h.runs(orb.ID, 3)
	assert.Equal(t, "manual", rows[0].Trigger)
	assert.Equal(t, "git-config\nrerun-ok\n", h.runLog(orb.ID, rows[0].ID))

	// View：lastSetup = 最近的 setup 运行（手动重跑成功），lastResume = resume 运行
	cur, err := h.svc.Get(ctx, orb.ID)
	require.NoError(t, err)
	v, err := View(ctx, h.st.Q, cur)
	require.NoError(t, err)
	require.NotNil(t, v.LastSetup)
	assert.Equal(t, rows[0].ID, v.LastSetup.ID)
	assert.True(t, v.LastSetup.Ok)
	assert.Equal(t, apigen.SetupTriggerManual, v.LastSetup.Trigger)
	require.NotNil(t, v.LastResume)
	assert.Equal(t, apigen.SetupPhaseResume, v.LastResume.Phase)
	assert.Nil(t, v.FailureReason)

	h.evMu.Lock()
	setupEvents := 0
	for _, ev := range h.events {
		if ev.OrbID == orb.ID && ev.Kind == EventSetup {
			setupEvents++
			assert.Equal(t, StateReady, ev.To)
		}
	}
	h.evMu.Unlock()
	assert.Equal(t, 3, setupEvents, "每条运行记录推送一次 orb 更新")

	// 他 orb 的运行记录查不到本 orb 的日志
	_, err = h.st.Q.GetOrbSetupRunLog(ctx, db.GetOrbSetupRunLogParams{ID: rows[0].ID, OrbID: "orb-other"})
	assert.Error(t, err)
}

func TestLogTailKeepsWholeRunes(t *testing.T) {
	// 截断点落在多字节字符中间时跳过残缺字节
	buf := []byte(strings.Repeat("a", 10) + strings.Repeat("中", SetupLogTail/3+1))
	out := logTail(buf)
	assert.LessOrEqual(t, len(out), SetupLogTail)
	assert.True(t, bytes.HasPrefix(out, []byte("中")))
}

// project.scripts.* 只能读写发起 link 的 orb 所属项目；无项目时 no_project。
func TestProjectScriptsScopedToOrbProject(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	str := func(s string) *string { return &s }
	a := h.createProject("a", str("echo a-setup"), str("echo a-resume"))
	b := h.createProject("b", str("echo b-setup"), nil)

	_, ca := h.readyOrb(drv, a.ID)

	var got apigen.LinkCtlProjectScripts
	require.NoError(t, ca.Call(ctx, "project.scripts.get", map[string]any{}, &got))
	assert.Equal(t, a.ID, got.ProjectID)
	assert.Equal(t, "a", got.ProjectName)
	assert.Equal(t, "echo a-setup", *got.Setup)
	assert.Equal(t, "echo a-resume", *got.Resume)

	// 只改 setup：resume 保持不变；即使参数里夹带别的项目 id 也只作用于本 orb 的项目
	got = apigen.LinkCtlProjectScripts{}
	require.NoError(t, ca.Call(ctx, "project.scripts.set", map[string]any{"setup": "echo new", "projectId": b.ID}, &got))
	assert.Equal(t, a.ID, got.ProjectID)
	assert.Equal(t, "echo new", *got.Setup)
	assert.Equal(t, "echo a-resume", *got.Resume)

	// 空串清除（存 NULL）
	got = apigen.LinkCtlProjectScripts{}
	require.NoError(t, ca.Call(ctx, "project.scripts.set", apigen.LinkCtlProjectScriptsSetParams{Resume: str("")}, &got))
	assert.Nil(t, got.Resume)
	pa, err := h.st.Q.GetProject(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "echo new", *pa.SetupScript)
	assert.Nil(t, pa.ResumeScript)

	pb, err := h.st.Q.GetProject(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, "echo b-setup", *pb.SetupScript, "其它项目不受影响")
	assert.Nil(t, pb.ResumeScript)
	assert.EqualValues(t, 1, pb.UpdatedAt)

	// 无项目的 orb
	_, cn := h.readyOrb(drv, "")
	for _, m := range []string{"project.scripts.get", "project.scripts.set"} {
		err := cn.Call(ctx, m, map[string]any{"setup": "echo x"}, nil)
		var ce *link.CtlError
		require.True(t, errors.As(err, &ce), "%s: %v", m, err)
		assert.Equal(t, CodeNoProject, ce.Code, m)
	}
	pa2, err := h.st.Q.GetProject(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, pa.SetupScript, pa2.SetupScript)
	pb2, err := h.st.Q.GetProject(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, pb.SetupScript, pb2.SetupScript)
}

// project.scripts.set 整行改写项目：图标、git 身份覆盖等与脚本无关的列必须原样保留。
func TestProjectScriptsSetKeepsOtherColumns(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	str := func(s string) *string { return &s }
	p := db.CreateProjectParams{ID: ids.New("prj"), Name: "keep", SetupScript: str("echo old"), Color: str("teal"), Icon: str("🧪"),
		GitAuthorName: str("Bot"), GitAuthorEmail: str("bot@example.com"), CreatedAt: 1, UpdatedAt: 1}
	require.NoError(t, h.st.Q.CreateProject(ctx, p))
	_, c := h.readyOrb(drv, p.ID)
	require.NoError(t, c.Call(ctx, "project.scripts.set", map[string]any{"setup": "echo new"}, nil))
	got, err := h.st.Q.GetProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, "echo new", *got.SetupScript)
	require.NotNil(t, got.Icon)
	assert.Equal(t, "🧪", *got.Icon)
	assert.Equal(t, "teal", *got.Color)
	require.NotNil(t, got.GitAuthorName)
	assert.Equal(t, "Bot", *got.GitAuthorName)
	assert.Equal(t, "bot@example.com", *got.GitAuthorEmail)
}

// failed 的 orb：View 带失败原因；Wake 把它移出 failed。
func TestFailedOrbReasonAndWake(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, func(c *Config) { c.ConnectTimeout = 300 * time.Millisecond })
	ctx := context.Background()
	drv.mu.Lock()
	drv.noConnect = true
	drv.mu.Unlock()
	orb, err := h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	failed := h.waitState(orb.ID, StateFailed, 10*time.Second)
	v, err := View(ctx, h.st.Q, failed)
	require.NoError(t, err)
	require.NotNil(t, v.FailureReason)
	assert.Equal(t, "no hello within connect timeout", *v.FailureReason)

	drv.mu.Lock()
	drv.noConnect = false
	drv.mu.Unlock()
	require.NoError(t, h.svc.Wake(ctx, orb.ID))
	ready := h.waitState(orb.ID, StateReady, 10*time.Second)
	v, err = View(ctx, h.st.Q, ready)
	require.NoError(t, err)
	assert.Nil(t, v.FailureReason, "离开 failed 后不再带失败原因")
	assert.Contains(t, h.eventSeq(orb.ID), "failed>waking")
}
