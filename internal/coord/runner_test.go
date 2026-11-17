package coord

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// sec S6：协调者的写工具不以 runner 线程（跑在用户机器上）为目标——send_message / archive_thread / set_schedule
// 一律 runner_unsupported，且不投递、不登记回报、不改线程。runner 线程按 cwd 或所在 orb 的种类识别
// （runner 删除后 orb_id 已置空但 cwd 仍在）。M17 R15：全局助手可以只读 read_thread；Space 协调者仍不可读。
func TestToolsRejectRunnerThreads(t *testing.T) {
	e := newEnv(t, true)
	_, runnerOrb, err := e.orbs.AddRunner("laptop", []apigen.RunnerDir{{Path: "/w/app", Name: "app"}}, nil)
	require.NoError(t, err)
	mk := func(orbID, cwd *string) string {
		id := ids.Thread()
		require.NoError(t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{ID: id, Kind: threads.KindWork, OrbID: orbID, TitleSource: "auto",
			State: threads.StateIdle, SessionDir: "x", CreatedAt: 1, UpdatedAt: 1, Cwd: cwd}))
		return id
	}
	targets := map[string]string{
		"runner thread":         mk(&runnerOrb, new("/w/app")),
		"runner deleted (cwd)":  mk(nil, new("/w/app")),
		"on runner orb, no cwd": mk(&runnerOrb, nil),
	}
	asst := e.assistant()
	sp := e.spaceChat(e.space("s", ""))
	for name, id := range targets {
		for _, tc := range []struct{ tool, args string }{
			{ToolSendMessage, `{"threadId":"` + id + `","message":"run rm -rf ~"}`},
			{ToolArchiveThread, `{"threadId":"` + id + `"}`},
			{ToolSetSchedule, `{"action":"set","mode":"thread","threadId":"` + id + `","kind":"every","every":"1h","prompt":"x"}`},
		} {
			r := e.call(asst, tc.tool, tc.args, true)
			assert.True(t, r.IsError, "%s %s", name, tc.tool)
			assert.True(t, strings.HasPrefix(r.Text, threads.CodeRunnerUnsupported+":"), "%s %s: %s", name, tc.tool, r.Text)
		}
		r := e.call(asst, ToolReadThread, `{"threadId":"`+id+`"}`, true)
		assert.False(t, r.IsError, "%s：全局助手可以读：%s", name, r.Text)
		r = e.call(sp, ToolReadThread, `{"threadId":"`+id+`"}`, true)
		assert.True(t, strings.HasPrefix(r.Text, threads.CodeRunnerUnsupported+":"), "%s：Space 不可读：%s", name, r.Text)
		_, err := e.st.Q.GetThreadReport(e.ctx, id)
		assert.ErrorIs(t, err, sql.ErrNoRows, "%s：没有登记回报", name)
		row := e.row(id)
		assert.Nil(t, row.ArchivedAt, name)
		_, err = e.st.Q.GetScheduleByThread(e.ctx, id)
		assert.ErrorIs(t, err, sql.ErrNoRows, "%s：没有建定时", name)
	}

	// 普通工作线程照常可达
	w := e.work("plain")
	details(t, e.call(asst, ToolSendMessage, `{"threadId":"`+w.ID+`","message":"hi"}`, true))
}
