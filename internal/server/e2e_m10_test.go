package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// usageStat 是 stats/usage 分组里本测试关心的字段。
type usageStat struct {
	Key       string  `json:"key"`
	Requests  int64   `json:"requests"`
	Turns     int64   `json:"turns"`
	CostUsd   float64 `json:"costUsd"`
	ToolCalls int64   `json:"toolCalls"`
}

type usageResp struct {
	Total  usageStat   `json:"total"`
	Groups []usageStat `json:"groups"`
}

// allTime 是覆盖整个测试的窗口：统计服务的时钟被固定在测试开始时刻，缺省的 to（= 服务时钟）会漏掉之后的事实。
func allTime() string { return fmt.Sprintf("from=0&to=%d", time.Now().Add(time.Hour).UnixMilli()) }

func (s *stack) usage(query string) usageResp {
	s.t.Helper()
	var u usageResp
	s.rest("GET", "/api/v1/stats/usage?"+allTime()+"&"+query, nil, &u, 200)
	return u
}

func findGroup(gs []usageStat, key string) (usageStat, bool) {
	for _, g := range gs {
		if g.Key == key {
			return g, true
		}
	}
	return usageStat{}, false
}

// 计划 M10 步骤 3「验证（切片 B）」最后一条 + 修订 R3 的预算 e2e：process 驱动 + 真 orbd + fakeomp（开启 model_usage 与 fork_from）。
func TestE2EM10Stats(t *testing.T) {
	t.Setenv("FAKEOMP_MODEL_USAGE", "1")
	t.Setenv("FAKEOMP_FORK", "1")
	start := time.Now()
	prevNow, prevInterval := statsNow, statsBudgetInterval
	statsNow, statsBudgetInterval = func() time.Time { return start }, time.Hour // 预算评估由测试直接调用
	t.Cleanup(func() { statsNow, statsBudgetInterval = prevNow, prevInterval })

	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	ctx := context.Background()
	var prj struct {
		ID string `json:"id"`
	}
	s.rest("POST", "/api/v1/projects", map[string]any{"name": "m10"}, &prj, 201)

	// 1. 一个回合：两条 assistant + 一条 title 用途的 model_usage = 3 个请求、1 个回合
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "hello m10", "projectId": prj.ID, "executor": map[string]any{"kind": "new_orb", "provider": "local", "type": "small"},
	}, &created, 201)
	th := s.waitThread(created.Thread.ID, 90*time.Second, func(v threadView) bool { return v.State == "idle" })
	tid, orbID := th.ID, th.OrbID
	require.NotEmpty(t, orbID)

	var byThread usageResp
	require.Eventually(t, func() bool {
		byThread = s.usage("groupBy=thread&thread=" + tid)
		return len(byThread.Groups) == 1 && byThread.Groups[0].Requests == 3
	}, 30*time.Second, 50*time.Millisecond, "三行用量入库：%+v", &byThread)
	assert.EqualValues(t, 1, byThread.Groups[0].Turns)
	threadCost := byThread.Groups[0].CostUsd
	assert.Positive(t, threadCost)

	byPurpose := s.usage("groupBy=purpose&thread=" + tid)
	_, hasTurn := findGroup(byPurpose.Groups, "")
	title, hasTitle := findGroup(byPurpose.Groups, "title")
	assert.True(t, hasTurn, "对话回合的用途为空串")
	assert.True(t, hasTitle, "title 用途")
	assert.EqualValues(t, 1, title.Requests)

	// 2. toolfail：工具错误计入
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "please toolfail", "executor": map[string]any{"kind": "orb", "orbId": orbID}}, &created, 201)
	tf := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	var tools struct {
		Total struct {
			Calls  int64 `json:"calls"`
			Errors int64 `json:"errors"`
		} `json:"total"`
	}
	require.Eventually(t, func() bool {
		s.rest("GET", "/api/v1/stats/tools?"+allTime()+"&thread="+tf.ID, nil, &tools, 200)
		return tools.Total.Calls > 0
	}, 30*time.Second, 50*time.Millisecond)
	assert.EqualValues(t, 1, tools.Total.Errors)

	// 3. fork（共用 orb）：拷贝来的历史不重复计费
	before := s.usage("groupBy=total").Total
	var forked struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads/"+tid+"/fork", map[string]any{"env": "share"}, &forked, 201)
	fid := forked.Thread.ID
	s.waitThread(fid, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	require.Eventually(t, func() bool {
		var page struct {
			Entries []json.RawMessage `json:"entries"`
		}
		s.rest("GET", "/api/v1/threads/"+fid+"/entries?limit=500", nil, &page, 200)
		return len(page.Entries) > 2
	}, 30*time.Second, 50*time.Millisecond, "fork 的会话文件已入库")
	after := s.usage("groupBy=total").Total
	assert.InDelta(t, before.CostUsd, after.CostUsd, 1e-9, "fork 不重复计费")
	assert.Equal(t, before.Requests, after.Requests)
	assert.EqualValues(t, 0, s.usage("groupBy=thread&thread="+fid).Total.Requests)

	// 4. 会话文件原样改写一次（cp 再 mv → entries.reset）：用量不变
	rev := func() int64 {
		row, err := s.srv.Store.Q.GetThread(ctx, tid)
		require.NoError(t, err)
		return row.TranscriptRev
	}
	rev0 := rev()
	files, err := filepath.Glob(filepath.Join(s.drv.HomeDir(orbID), ".oryn", "threads", tid, "*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	raw, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(files[0]+".tmp", raw, 0o600))
	require.NoError(t, os.Rename(files[0]+".tmp", files[0]))
	require.Eventually(t, func() bool { return rev() == rev0+1 }, 30*time.Second, 50*time.Millisecond, "transcript_rev 加一")
	again := s.usage("groupBy=thread&thread=" + tid)
	require.Len(t, again.Groups, 1)
	assert.EqualValues(t, 3, again.Groups[0].Requests, "重放幂等")
	assert.InDelta(t, threadCost, again.Groups[0].CostUsd, 1e-9)

	// 5. 删线程：成本仍归项目；按线程出现空键
	projCost := func() float64 {
		g, ok := findGroup(s.usage("groupBy=project").Groups, prj.ID)
		require.True(t, ok)
		return g.CostUsd
	}
	p0 := projCost()
	s.rest("DELETE", "/api/v1/threads/"+tid, nil, nil, 204)
	assert.InDelta(t, p0, projCost(), 1e-9, "删除时写入的归属快照")
	gone, ok := findGroup(s.usage("groupBy=thread").Groups, "")
	require.True(t, ok, "已删线程为空键")
	assert.InDelta(t, threadCost, gone.CostUsd, 1e-9)

	// 6. orb 时长
	var orbStats struct {
		Groups []struct {
			Key       string `json:"key"`
			RunningMs int64  `json:"runningMs"`
		} `json:"groups"`
	}
	s.rest("GET", "/api/v1/stats/orbs?groupBy=orb&"+allTime(), nil, &orbStats, 200)
	var running int64
	for _, g := range orbStats.Groups {
		if g.Key == orbID {
			running = g.RunningMs
		}
	}
	assert.Positive(t, running)

	// ---- 预算告警（R3）：一条没有线程的 budget 系统通知，按 id 已读，重复评估不重发 ----
	const secret = "m10-hook-secret"
	hooks := make(chan hookEvent, 64)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var e hookEvent
		_ = json.Unmarshal(b, &e)
		var keys map[string]json.RawMessage
		_ = json.Unmarshal(b, &keys)
		_, e.hasThread = keys["thread"]
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(b)
		e.sigOK = r.Header.Get("X-Oryn-Signature") == "sha256="+hex.EncodeToString(mac.Sum(nil)) && r.Header.Get("X-Oryn-Event") == e.Event
		hooks <- e
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	s.rest("POST", "/api/v1/notification-channels", map[string]any{
		"kind": "webhook", "name": "m10", "webhook": map[string]any{"url": hook.URL + "/in", "secret": secret},
	}, nil, 201)

	// 金额聚合由单测覆盖；这里直接写一行 1 美元的用量（fakeomp 每个请求只有 0.00081 美元）
	_, err = s.srv.Store.Q.InsertUsageRecord(ctx, db.InsertUsageRecordParams{EntrySeq: 1, Ts: statsNow().UnixMilli(), Provider: "p", Model: "m", CostUsd: 1.0})
	require.NoError(t, err)
	var bs struct {
		Day struct {
			Level string `json:"level"`
		} `json:"day"`
	}
	s.rest("PUT", "/api/v1/stats/budget", map[string]any{"dailyUsd": 0.5, "warnPct": 80, "timezone": "UTC", "enabled": true}, &bs, 200)
	assert.Equal(t, "over", bs.Day.Level)

	s.srv.Stats.EvaluateBudget(ctx)
	var e hookEvent
	for e = nextHook(t, hooks, 30*time.Second); e.Event != "budget.alert"; e = nextHook(t, hooks, 30*time.Second) {
	}
	assert.False(t, e.hasThread, "体里没有 thread")
	assert.Empty(t, e.Thread.ID)
	assert.Equal(t, "Daily budget exceeded", e.Title)
	assert.Equal(t, "http://localhost/stats/usage", e.URL)
	assert.True(t, e.sigOK, "签名与 X-Oryn-Event 正确")

	type notification struct {
		ID       string  `json:"id"`
		Kind     string  `json:"kind"`
		ThreadID *string `json:"threadId"`
		URL      string  `json:"url"`
		ReadAt   *int64  `json:"readAt"`
	}
	var list struct {
		Notifications []notification `json:"notifications"`
		Unread        int            `json:"unread"`
	}
	budgetRows := func() []notification {
		var out []notification
		for _, n := range list.Notifications {
			if n.Kind == "budget" {
				out = append(out, n)
			}
		}
		return out
	}
	s.rest("GET", "/api/v1/notifications?unread=true", nil, &list, 200)
	rows := budgetRows()
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].ThreadID)
	assert.Equal(t, "/stats/usage", rows[0].URL)
	nid, unread := rows[0].ID, list.Unread

	a := s.dial()
	s.rest("POST", "/api/v1/notifications/read", map[string]any{"id": nid}, nil, 204)
	a.until(10*time.Second, func(m wsMsg) bool { return m.Type == "notifications.read" && m.ID == nid })
	s.rest("GET", "/api/v1/notifications", nil, &list, 200)
	rows = budgetRows()
	require.Len(t, rows, 1)
	assert.NotNil(t, rows[0].ReadAt)
	assert.Equal(t, unread-1, list.Unread, "只标这一条")

	s.srv.Stats.EvaluateBudget(ctx)
	s.srv.Stats.EvaluateBudget(ctx)
	var n int
	require.NoError(t, s.srv.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE kind = 'budget'`).Scan(&n))
	assert.Equal(t, 1, n, "重复评估不重发")
	var levels []string
	r, err := s.srv.Store.DB.QueryContext(ctx, `SELECT level FROM budget_alerts ORDER BY level`)
	require.NoError(t, err)
	for r.Next() {
		var l string
		require.NoError(t, r.Scan(&l))
		levels = append(levels, l)
	}
	require.NoError(t, r.Close())
	assert.Equal(t, []string{"over", "warn"}, levels)

	s.rest("POST", "/api/v1/notifications/read", map[string]any{"id": nid, "threadId": fid}, nil, 400)
}
