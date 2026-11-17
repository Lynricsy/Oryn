package coord

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// M19 步骤 3.1：并发两次 EnsureMain 只建一行主线；它是带固定标题的助手对话，挂在协调 orb 上。
func TestEnsureMainSingleton(t *testing.T) {
	e := newEnv(t, true)

	var wg sync.WaitGroup
	got := make([]apigen.Thread, 2)
	errs := make([]error, 2)
	for i := range got {
		wg.Go(func() { got[i], errs[i] = e.c.EnsureMain(e.ctx) })
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, got[0].ID, got[1].ID)
	require.NotNil(t, got[0].Main)
	assert.True(t, *got[0].Main)
	require.NotNil(t, got[0].Kind)
	assert.Equal(t, apigen.ThreadKind("assistant"), *got[0].Kind)
	assert.Equal(t, "Main", got[0].Title)

	row := e.row(got[0].ID)
	assert.Equal(t, int64(1), row.Main)
	assert.Equal(t, "user", row.TitleSource)
	require.NotNil(t, row.OrbID)
	assert.Equal(t, e.coordOrb, *row.OrbID)

	var n int
	require.NoError(t, e.st.DB.QueryRowContext(e.ctx, `SELECT count(*) FROM threads WHERE main = 1`).Scan(&n))
	assert.Equal(t, 1, n)

	// 普通助手对话不是主线
	a := e.assistant()
	assert.Nil(t, a.Main)
}

// M19 步骤 3.4：助手在主线上按名字建多条提醒；改、停、恢复、删都要 scheduleId。
func TestMainScheduleTool(t *testing.T) {
	e := newEnv(t, true)
	main, err := e.c.EnsureMain(e.ctx)
	require.NoError(t, err)
	failsWith := func(r threads.HostToolResult, code string) {
		t.Helper()
		assert.True(t, r.IsError && strings.HasPrefix(r.Text, code+":"), r.Text)
	}

	a := details(t, e.call(main, ToolSetSchedule, `{"action":"set","name":"a","kind":"every","every":"1h","prompt":"p"}`, false))["schedule"].(map[string]any)
	b := details(t, e.call(main, ToolSetSchedule, `{"action":"set","name":"b","kind":"every","every":"1h","prompt":"p"}`, false))["schedule"].(map[string]any)
	assert.Equal(t, main.ID, a["threadId"])
	assert.Equal(t, main.ID, b["threadId"])
	assert.NotEqual(t, a["id"], b["id"])

	failsWith(e.call(main, ToolSetSchedule, `{"action":"set","kind":"every","every":"1h","prompt":"p"}`, false), CodeInvalidSchedule)
	failsWith(e.call(main, ToolSetSchedule, `{"action":"set","name":"a","kind":"every","every":"1h","prompt":"p"}`, false), schedule.CodeNameTaken)

	upd := details(t, e.call(main, ToolSetSchedule, `{"action":"set","scheduleId":"`+a["id"].(string)+`","name":"a","kind":"every","every":"2h","prompt":"q"}`, false))["schedule"].(map[string]any)
	assert.Equal(t, a["id"], upd["id"])
	assert.Equal(t, "q", upd["prompt"])

	failsWith(e.call(main, ToolSetSchedule, `{"action":"pause"}`, false), CodeInvalid)
	paused := details(t, e.call(main, ToolSetSchedule, `{"action":"pause","scheduleId":"`+b["id"].(string)+`"}`, false))["schedule"].(map[string]any)
	assert.Equal(t, false, paused["enabled"])

	list, err := e.c.sched.List(e.ctx, schedule.Filter{ThreadID: main.ID})
	require.NoError(t, err)
	assert.Len(t, list, 2)
}
