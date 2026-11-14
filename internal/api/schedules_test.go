package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// schedThreads 是定时服务需要的最小线程能力：投递一律被接受但不应答。
type schedThreads struct{ st *store.Store }

func (f schedThreads) Get(ctx context.Context, id string) (apigen.Thread, error) {
	row, err := f.st.Q.GetThread(ctx, id)
	if err != nil {
		return apigen.Thread{}, threads.ErrNotFound
	}
	return apigen.Thread{ID: row.ID, State: apigen.ThreadState(row.State), ArchivedAt: row.ArchivedAt}, nil
}

func (schedThreads) Deliver(context.Context, string, threads.AutoMessage, func(error)) (func() bool, error) {
	return func() bool { return false }, nil
}
func (schedThreads) Publish(context.Context, string) {}
func (schedThreads) Create(context.Context, threads.CreateParams) (apigen.Thread, error) {
	return apigen.Thread{}, errors.New("not used")
}
func (schedThreads) Archive(context.Context, string) error { return nil }
func (schedThreads) RecoveredOutcomeMatch(context.Context, string, func(string) bool, int64) (threads.TurnEvent, bool, error) {
	return threads.TurnEvent{}, false, nil
}

type nopOrbs struct{}

func (nopOrbs) Delete(context.Context, string) error { return nil }

func newScheduleEnv(t *testing.T) (*notifyEnv, func() string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = h2PublicURL, dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	s.Schedules = &api.ScheduleDeps{Service: schedule.New(schedule.Config{}, st, schedThreads{st}, nopOrbs{}, nil)}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(srv.Close)
	dev, tok, err := auth.CreateDevice(ctx, st, "t", "cli", "")
	require.NoError(t, err)
	mkThread := func() string {
		id := ids.Thread()
		require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: id, Title: "t", TitleSource: "user",
			State: "idle", SessionDir: "~/.oryn/threads/" + id, CreatedAt: 1, UpdatedAt: 1}))
		return id
	}
	return &notifyEnv{t: t, srv: srv, st: st, token: tok, devID: dev.ID}, mkThread
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(b, &v), string(b))
	return v
}

func TestScheduleRoutes(t *testing.T) {
	e, mkThread := newScheduleEnv(t)
	th := mkThread()

	// 线程内定时
	code, b := e.do(http.MethodPut, "/api/v1/threads/"+th+"/schedule", map[string]any{"kind": "every", "every": "1h", "prompt": "p", "name": "hourly"})
	require.Equal(t, http.StatusOK, code, string(b))
	ts := decode[apigen.Schedule](t, b)
	assert.Equal(t, apigen.ScheduleModeThread, ts.Mode)
	assert.Equal(t, th, *ts.ThreadID)
	assert.Equal(t, "1h0m0s", *ts.Every)
	assert.Equal(t, apigen.ScheduleCreatedByUser, ts.CreatedBy)
	code, b = e.do(http.MethodGet, "/api/v1/threads/"+th+"/schedule", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, ts.ID, decode[apigen.Schedule](t, b).ID)
	code, b = e.do(http.MethodPost, "/api/v1/schedules", map[string]any{"mode": "thread", "threadId": th, "kind": "every", "every": "1h", "prompt": "p"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "conflict", errCode(b))

	// 项目定时
	code, b = e.do(http.MethodPost, "/api/v1/schedules", map[string]any{"kind": "every", "every": "1h", "prompt": "p", "name": "tick"})
	assert.Equal(t, http.StatusBadRequest, code, "POST 必须给 mode")
	assert.Equal(t, schedule.CodeInvalidSchedule, errCode(b))
	nt := map[string]any{"mode": "new_thread", "kind": "cron", "cron": "0 9 * * *", "timezone": "Asia/Shanghai", "prompt": "p", "name": "tick", "keepLast": 3}
	code, b = e.do(http.MethodPost, "/api/v1/schedules", nt)
	require.Equal(t, http.StatusCreated, code, string(b))
	ns := decode[apigen.Schedule](t, b)
	assert.Equal(t, 3, ns.KeepLast)
	assert.Equal(t, apigen.ScheduleOverlapSkip, ns.Overlap)
	code, b = e.do(http.MethodPost, "/api/v1/schedules", nt)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, schedule.CodeNameTaken, errCode(b))
	code, b = e.do(http.MethodPut, "/api/v1/schedules/"+ns.ID, map[string]any{"mode": "thread", "kind": "every", "every": "1h", "prompt": "p"})
	assert.Equal(t, http.StatusBadRequest, code, "目标不可改")
	assert.Equal(t, schedule.CodeInvalidSchedule, errCode(b))
	nt["keepLast"] = 7
	code, b = e.do(http.MethodPut, "/api/v1/schedules/"+ns.ID, nt)
	require.Equal(t, http.StatusOK, code, string(b))
	assert.Equal(t, 7, decode[apigen.Schedule](t, b).KeepLast)

	// 列表过滤
	list := func(q string) []string {
		code, b := e.do(http.MethodGet, "/api/v1/schedules"+q, nil)
		require.Equal(t, http.StatusOK, code, string(b))
		var out []string
		for _, s := range decode[apigen.ScheduleList](t, b).Schedules {
			out = append(out, s.ID)
		}
		return out
	}
	assert.ElementsMatch(t, []string{ts.ID, ns.ID}, list(""))
	assert.Equal(t, []string{ts.ID}, list("?mode=thread"))
	assert.Equal(t, []string{ns.ID}, list("?project=none"))
	assert.Equal(t, []string{ts.ID}, list("?thread="+th))
	code, _ = e.do(http.MethodGet, "/api/v1/schedules?mode=loop", nil)
	assert.Equal(t, http.StatusBadRequest, code)

	// 预览
	code, b = e.do(http.MethodPost, "/api/v1/schedules/preview", map[string]any{"kind": "cron", "cron": "61 * * * *", "prompt": ""})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, schedule.CodeInvalidSchedule, errCode(b))
	code, b = e.do(http.MethodPost, "/api/v1/schedules/preview", map[string]any{"kind": "cron", "cron": "0 9 * * 1-5", "timezone": "Asia/Shanghai", "prompt": ""})
	require.Equal(t, http.StatusOK, code)
	assert.Len(t, decode[apigen.SchedulePreview](t, b).Next, 5)

	// 暂停 / 恢复 / 手动运行 / 运行记录
	code, b = e.do(http.MethodPost, "/api/v1/schedules/"+ts.ID+"/pause", nil)
	require.Equal(t, http.StatusOK, code)
	paused := decode[apigen.Schedule](t, b)
	assert.False(t, paused.Enabled)
	assert.Nil(t, paused.NextRunAt)
	code, b = e.do(http.MethodPost, "/api/v1/schedules/"+ts.ID+"/resume", nil)
	require.Equal(t, http.StatusOK, code)
	assert.True(t, decode[apigen.Schedule](t, b).Enabled)
	code, b = e.do(http.MethodPost, "/api/v1/schedules/"+ts.ID+"/run", nil)
	require.Equal(t, http.StatusOK, code, string(b))
	run := decode[apigen.ScheduleRun](t, b)
	assert.Equal(t, apigen.ScheduleRunTriggerManual, run.Trigger)
	code, b = e.do(http.MethodPost, "/api/v1/schedules/"+ts.ID+"/run", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, schedule.CodeScheduleBusy, errCode(b))
	code, b = e.do(http.MethodGet, "/api/v1/schedules/"+ts.ID+"/runs?limit=5", nil)
	require.Equal(t, http.StatusOK, code)
	runs := decode[apigen.ScheduleRunList](t, b).Runs
	require.Len(t, runs, 1)
	assert.Equal(t, run.ID, runs[0].ID)
	code, _ = e.do(http.MethodGet, "/api/v1/schedules/"+ts.ID+"/runs?limit=0", nil)
	assert.Equal(t, http.StatusBadRequest, code)

	// 删除与 404
	code, _ = e.do(http.MethodDelete, "/api/v1/schedules/"+ns.ID, nil)
	assert.Equal(t, http.StatusNoContent, code)
	code, b = e.do(http.MethodGet, "/api/v1/schedules/"+ns.ID, nil)
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, schedule.CodeScheduleNotFound, errCode(b))
	code, _ = e.do(http.MethodGet, "/api/v1/schedules/sch_missing/runs", nil)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = e.do(http.MethodDelete, "/api/v1/threads/"+th+"/schedule", nil)
	assert.Equal(t, http.StatusNoContent, code)
	code, b = e.do(http.MethodDelete, "/api/v1/threads/"+th+"/schedule", nil)
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, schedule.CodeScheduleNotFound, errCode(b))
	code, b = e.do(http.MethodPut, "/api/v1/threads/T-missing/schedule", map[string]any{"kind": "every", "every": "1h", "prompt": "p"})
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, schedule.CodeNotFound, errCode(b))

	// 已归档线程
	arch := mkThread()
	_, err := e.st.DB.Exec(`UPDATE threads SET archived_at = 1 WHERE id = ?`, arch)
	require.NoError(t, err)
	code, b = e.do(http.MethodPut, "/api/v1/threads/"+arch+"/schedule", map[string]any{"kind": "every", "every": "1h", "prompt": "p"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, schedule.CodeThreadArchived, errCode(b))
}

func TestScheduleRoutesNotConfigured(t *testing.T) {
	e := newNotifyEnv(t)
	code, _ := e.do(http.MethodGet, "/api/v1/schedules", nil)
	assert.Equal(t, http.StatusNotImplemented, code)
}

// 版本条件（契约 C7.1）：过期 ifRev → 409 schedule_conflict 且库未变；线程内定时 0 = 只新建。
func TestScheduleIfRevRoutes(t *testing.T) {
	e, mkThread := newScheduleEnv(t)
	th := mkThread()
	body := func(prompt string, ifRev any) map[string]any {
		m := map[string]any{"kind": "every", "every": "1h", "prompt": prompt}
		if ifRev != nil {
			m["ifRev"] = ifRev
		}
		return m
	}
	code, b := e.do(http.MethodPut, "/api/v1/threads/"+th+"/schedule", body("p", 0))
	require.Equal(t, http.StatusOK, code, string(b))
	sc := decode[apigen.Schedule](t, b)
	assert.EqualValues(t, 1, sc.Rev)

	code, b = e.do(http.MethodPut, "/api/v1/threads/"+th+"/schedule", body("again", 0))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, schedule.CodeScheduleConflict, errCode(b))

	code, b = e.do(http.MethodPost, "/api/v1/schedules/"+sc.ID+"/pause", nil)
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 2, decode[apigen.Schedule](t, b).Rev)

	code, b = e.do(http.MethodPut, "/api/v1/schedules/"+sc.ID, body("stale", 1))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, schedule.CodeScheduleConflict, errCode(b))
	code, b = e.do(http.MethodGet, "/api/v1/schedules/"+sc.ID, nil)
	require.Equal(t, http.StatusOK, code)
	cur := decode[apigen.Schedule](t, b)
	assert.Equal(t, "p", cur.Prompt, "库未变")
	assert.False(t, cur.Enabled, "仍是暂停")
	assert.EqualValues(t, 2, cur.Rev)

	code, b = e.do(http.MethodPut, "/api/v1/schedules/"+sc.ID, body("fresh", 2))
	require.Equal(t, http.StatusOK, code, string(b))
	assert.EqualValues(t, 3, decode[apigen.Schedule](t, b).Rev)

	other := mkThread()
	code, b = e.do(http.MethodPut, "/api/v1/threads/"+other+"/schedule", body("p", 4))
	assert.Equal(t, http.StatusConflict, code, "线程没有定时却给了非 0 ifRev")
	assert.Equal(t, schedule.CodeScheduleConflict, errCode(b))

	// 新建入口忽略 ifRev
	nb := body("p", 7)
	nb["mode"], nb["threadId"] = "thread", other
	code, b = e.do(http.MethodPost, "/api/v1/schedules", nb)
	require.Equal(t, http.StatusCreated, code, string(b))
}
