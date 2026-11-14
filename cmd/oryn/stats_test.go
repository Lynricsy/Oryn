package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// statsNow 是测试里的「现在」：2026-11-15 12:00:00 UTC。
var statsNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// raw 让 route 以给定的 Content-Type 原样返回 body。
func (f *fakeAPI) raw(route, contentType, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = func(w http.ResponseWriter, _ []byte) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}
}

// statsHarness 用假服务端、固定时钟与可替换的 /etc/localtime 执行 `oryn stats …`。
type statsHarness struct {
	f         *fakeAPI
	localtime string // 空 = 读取失败
	tty       bool   // stdout 是否当作终端
}

func newStatsHarness(t *testing.T) *statsHarness {
	t.Helper()
	t.Setenv("ORYN_ORB", "")
	t.Setenv("TZ", "UTC")
	return &statsHarness{f: newFakeAPI(t)}
}

func (h *statsHarness) run(args ...string) (string, string, error) {
	env := statsEnv{
		client: func() (*apiClient, error) { return h.f.client(), nil },
		now:    func() time.Time { return statsNow },
		localtime: func() (string, error) {
			if h.localtime == "" {
				return "", os.ErrNotExist
			}
			return h.localtime, nil
		},
		isTTY: func(io.Writer) bool { return h.tty },
	}
	cmd := newStatsCmd(env)
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

// lastQuery 解析最后一个请求的查询串。
func (h *statsHarness) lastQuery(t *testing.T) url.Values {
	t.Helper()
	reqs := h.f.requests()
	require.NotEmpty(t, reqs)
	q, err := url.ParseQuery(reqs[len(reqs)-1].RawQuery)
	require.NoError(t, err)
	return q
}

func ms(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func requireUsageErr(t *testing.T, err error) {
	t.Helper()
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
}

func TestStatsUsageJSONPassthrough(t *testing.T) {
	h := newStatsHarness(t)
	t.Setenv("TZ", "Asia/Shanghai")
	// 故意用非规范的键序与空白：--json 必须逐字节转发
	body := "{\"tz\":\"Asia/Shanghai\",  \"groupBy\":\"model\",\"from\":1,\"to\":2,\"total\":{\"key\":\"total\",\"costUsd\":1.50},\"groups\":[]}\n"
	h.f.raw("GET /api/v1/stats/usage", "application/json", body)

	out, _, err := h.run("--by", "model", "--since", "7d", "--json")
	require.NoError(t, err)
	assert.Equal(t, body, out)
	q := h.lastQuery(t)
	assert.Equal(t, url.Values{
		"groupBy": {"model"},
		"from":    {ms(statsNow.Add(-7 * 24 * time.Hour))},
		"to":      {ms(statsNow)},
		"tz":      {"Asia/Shanghai"},
	}, q)

	// `stats usage` 与 `stats` 等价，缺省按模型
	_, _, err = h.run("usage", "--json")
	require.NoError(t, err)
	assert.Equal(t, "model", h.lastQuery(t).Get("groupBy"))
}

func TestStatsSince(t *testing.T) {
	h := newStatsHarness(t)
	h.f.raw("GET /api/v1/stats/usage", "application/json", "{}")
	for _, c := range []struct {
		since string
		d     time.Duration
	}{{"7d", 7 * 24 * time.Hour}, {"2w", 14 * 24 * time.Hour}, {"36h", 36 * time.Hour}} {
		_, _, err := h.run("--since", c.since, "--json")
		require.NoError(t, err, c.since)
		q := h.lastQuery(t)
		assert.Equal(t, ms(statsNow.Add(-c.d)), q.Get("from"), c.since)
		assert.Equal(t, ms(statsNow), q.Get("to"), c.since)
	}
	n := len(h.f.requests())
	for _, bad := range []string{"0d", "-1h", "abc", "2x", "w"} {
		_, err := parseSince(bad)
		requireUsageErr(t, err)
		_, _, err = h.run("--since", bad)
		requireUsageErr(t, err)
	}
	assert.Len(t, h.f.requests(), n, "参数错误时不发请求")
}

func TestStatsTimezoneFallback(t *testing.T) {
	h := newStatsHarness(t)
	h.f.raw("GET /api/v1/stats/usage", "application/json", "{}")

	// 1. --tz 优先于 TZ
	t.Setenv("TZ", "Europe/Berlin")
	_, errOut, err := h.run("--tz", "America/New_York", "--json")
	require.NoError(t, err)
	assert.Equal(t, "America/New_York", h.lastQuery(t).Get("tz"))
	assert.Empty(t, errOut)

	// 2. 环境变量 TZ
	_, _, err = h.run("--json")
	require.NoError(t, err)
	assert.Equal(t, "Europe/Berlin", h.lastQuery(t).Get("tz"))

	// 3. /etc/localtime 链接目标（相对路径同样认）
	t.Setenv("TZ", "")
	h.localtime = "../usr/share/zoneinfo/Asia/Kolkata"
	_, errOut, err = h.run("--json")
	require.NoError(t, err)
	assert.Equal(t, "Asia/Kolkata", h.lastQuery(t).Get("tz"))
	assert.Empty(t, errOut)

	// 4. 都没有：UTC 并在 stderr 提示
	h.localtime = ""
	_, errOut, err = h.run("--json")
	require.NoError(t, err)
	assert.Equal(t, "UTC", h.lastQuery(t).Get("tz"))
	assert.Contains(t, errOut, "UTC")

	// 非法 --tz
	_, _, err = h.run("--tz", "Mars/Olympus", "--json")
	requireUsageErr(t, err)
}

func TestStatsFromTo(t *testing.T) {
	h := newStatsHarness(t)
	h.f.raw("GET /api/v1/stats/usage", "application/json", "{}")
	_, _, err := h.run("--from", "2026-11-01", "--to", "2026-11-02", "--tz", "Asia/Shanghai", "--json")
	require.NoError(t, err)
	q := h.lastQuery(t)
	// 本地零点（+08:00）换算；--to 含当日 → 次日零点
	assert.Equal(t, ms(time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)), q.Get("from"))
	assert.Equal(t, ms(time.Date(2026, 11, 2, 16, 0, 0, 0, time.UTC)), q.Get("to"))
	assert.Equal(t, "Asia/Shanghai", q.Get("tz"))

	// 只给 --from：到现在
	_, _, err = h.run("--from", "2026-11-10", "--json")
	require.NoError(t, err)
	q = h.lastQuery(t)
	assert.Equal(t, ms(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)), q.Get("from"))
	assert.Equal(t, ms(statsNow), q.Get("to"))
}

func TestStatsArgumentErrors(t *testing.T) {
	h := newStatsHarness(t)
	h.f.raw("GET /api/v1/stats/usage", "application/json", "{}")
	for _, args := range [][]string{
		{"--json", "--csv"},
		{"tools", "--json", "--csv"},
		{"orbs", "--json", "--csv"},
		{"output", "--json", "--csv"},
		{"--since", "7d", "--from", "2026-11-01"},
		{"--since", "7d", "--to", "2026-11-01"},
		{"--to", "2026-11-01"},
		{"--from", "2026-11-03", "--to", "2026-11-01"},
		{"--from", "11/01/2026"},
		{"--by", "bogus"},
		{"--series", "minute"},
		{"--by", "day", "--series", "hour"},
		{"orbs", "--by", "model"},
		{"output", "--by", "model"},
	} {
		_, _, err := h.run(args...)
		requireUsageErr(t, err)
	}
	assert.Empty(t, h.f.requests(), "参数错误时不发请求")
}

func TestStatsInOrbRefused(t *testing.T) {
	h := newStatsHarness(t)
	t.Setenv("ORYN_ORB", "1")
	t.Setenv("ORYN_API_TOKEN", "")
	for _, args := range [][]string{{}, {"tools"}, {"thread", "T-1"}, {"budget", "set", "--daily", "1"}} {
		_, _, err := h.run(args...)
		requireUsageErr(t, err)
		assert.ErrorContains(t, err, "orb 内不可用")
	}
	assert.Empty(t, h.f.requests())

	// 有显式 API 凭据时可用
	t.Setenv("ORYN_API_TOKEN", "odt_x")
	h.f.raw("GET /api/v1/stats/usage", "application/json", "{}")
	_, _, err := h.run("--json")
	require.NoError(t, err)
}

func TestStatsFilters(t *testing.T) {
	h := newStatsHarness(t)
	h.f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "demo"}}})
	h.f.json("GET /api/v1/spaces", 200, apigen.SpaceList{Spaces: []apigen.Space{{ID: "spc_S", Name: "m10"}}})
	h.f.raw("GET /api/v1/stats/usage", "application/json", "{}")
	h.f.raw("GET /api/v1/stats/tools", "application/json", "{}")
	h.f.raw("GET /api/v1/stats/orbs", "application/json", "{}")
	h.f.raw("GET /api/v1/stats/output", "application/json", "{}")

	_, _, err := h.run("--project", "demo", "--space", "m10", "--thread", "T-1", "--model", "anthropic/claude-x", "--limit", "5", "--series", "day", "--json")
	require.NoError(t, err)
	q := h.lastQuery(t)
	assert.Equal(t, "prj_A", q.Get("project"))
	assert.Equal(t, "spc_S", q.Get("space"))
	assert.Equal(t, "T-1", q.Get("thread"))
	assert.Equal(t, "anthropic/claude-x", q.Get("model"))
	assert.Equal(t, "5", q.Get("limit"))
	assert.Equal(t, "day", q.Get("series"))
	reqs := h.f.requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, "/api/v1/projects", reqs[0].Path)
	assert.Equal(t, "/api/v1/spaces", reqs[1].Path)
	assert.Equal(t, "archived=true", reqs[1].RawQuery, "名称解析包含已归档的 Space")

	// none 与 id 不经列表接口
	_, _, err = h.run("tools", "--project", "none", "--space", "spc_X", "--thread", "T-2", "--json")
	require.NoError(t, err)
	q = h.lastQuery(t)
	assert.Equal(t, "none", q.Get("project"))
	assert.Equal(t, "spc_X", q.Get("space"))
	assert.Equal(t, "T-2", q.Get("thread"))
	assert.Len(t, h.f.requests(), 4)
	assert.Equal(t, "/api/v1/stats/tools", h.f.requests()[3].Path)

	_, _, err = h.run("orbs", "--by", "orb", "--orb", "orb-x", "--project", "prj_B", "--json")
	require.NoError(t, err)
	q = h.lastQuery(t)
	assert.Equal(t, "orb", q.Get("groupBy"))
	assert.Equal(t, "orb-x", q.Get("orb"))
	assert.Equal(t, "prj_B", q.Get("project"))

	_, _, err = h.run("output", "--space", "none", "--json")
	require.NoError(t, err)
	q = h.lastQuery(t)
	assert.Equal(t, "day", q.Get("groupBy"), "output 缺省按日")
	assert.Equal(t, "none", q.Get("space"))

	_, _, err = h.run("orbs", "--json")
	require.NoError(t, err)
	assert.Equal(t, "type", h.lastQuery(t).Get("groupBy"), "orbs 缺省按类型")

	// 不存在的项目名：报错且不发统计请求
	n := len(h.f.requests())
	_, _, err = h.run("--project", "nope")
	require.ErrorContains(t, err, "nope")
	assert.Len(t, h.f.requests(), n+1)
}

func TestStatsCSV(t *testing.T) {
	h := newStatsHarness(t)
	csv := "key,start,requests,costUsd\r\n2026-11-15,1,2,0.5\r\n"
	h.f.raw("GET /api/v1/stats/usage", "text/csv; charset=utf-8", csv)
	out, _, err := h.run("--by", "day", "--csv", "--tz", "Asia/Shanghai")
	require.NoError(t, err)
	assert.Equal(t, csv, out)
	q := h.lastQuery(t)
	assert.Equal(t, "csv", q.Get("format"))
	assert.Equal(t, "day", q.Get("groupBy"))
	assert.Equal(t, "Asia/Shanghai", q.Get("tz"))

	h.f.raw("GET /api/v1/stats/orbs", "text/csv", "key,orbs\n")
	out, _, err = h.run("orbs", "--csv")
	require.NoError(t, err)
	assert.Equal(t, "key,orbs\n", out)
	assert.Equal(t, "csv", h.lastQuery(t).Get("format"))
}

func TestStatsServerError(t *testing.T) {
	h := newStatsHarness(t)
	h.f.json("GET /api/v1/stats/usage", 400, map[string]string{"code": "range_too_large", "message": "too many buckets"})
	for _, args := range [][]string{{"--json"}, {"--csv"}, {}} {
		_, _, err := h.run(args...)
		var ae *apiError
		require.ErrorAs(t, err, &ae, args)
		assert.Equal(t, 400, ae.Status)
		assert.Equal(t, "range_too_large", ae.Code)
		assert.Equal(t, "too many buckets", ae.Message)
	}
	// 非 JSON 错误体：原文作为消息
	h.f.mu.Lock()
	h.f.routes["GET /api/v1/stats/tools"] = func(w http.ResponseWriter, _ []byte) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway\n"))
	}
	h.f.mu.Unlock()
	_, err := h.f.client().getRaw(context.Background(), "/api/v1/stats/tools")
	var ae *apiError
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, 502, ae.Status)
	assert.Equal(t, "bad gateway", ae.Message)
}

func TestStatsUsageTable(t *testing.T) {
	h := newStatsHarness(t)
	h.f.json("GET /api/v1/stats/usage", 200, apigen.UsageStats{
		GroupBy: "model",
		Groups: []apigen.UsageStat{
			{Key: "anthropic/claude-x", Requests: 4, RequestErrors: 1, InputTokens: 1234, OutputTokens: 3_400_000, CacheReadTokens: 999,
				CostUsd: 12.34, AvgTtftMs: new(850.0), TokensPerSecond: new(42.25), ToolCalls: 7, Turns: 2},
			{Key: "__other__", Requests: 1, CostUsd: 0.0042},
		},
		Total:  apigen.UsageStat{Key: "total", Requests: 5, RequestErrors: 1, InputTokens: 1234, OutputTokens: 3_400_000, CostUsd: 12.3442, AvgTtftMs: new(1500.0), ToolCalls: 7, Turns: 2},
		Series: []apigen.UsageSeriesPoint{{Key: "2026-11-14", Total: apigen.UsageStat{Requests: 5, CostUsd: 2}}},
	})
	out, _, err := h.run("--series", "day")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Equal(t, []string{"KEY", "REQ", "ERR%", "IN", "OUT", "CACHE-R", "CACHE-W", "COST", "TTFT", "TOK/S", "TOOLS", "TURNS"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"anthropic/claude-x", "4", "25%", "1.2k", "3.4M", "999", "0", "$12.34", "850ms", "42.2", "7", "2"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"(other)", "1", "0%", "0", "0", "0", "0", "$0.0042", "-", "-", "0", "0"}, strings.Fields(lines[2]))
	assert.Equal(t, []string{"TOTAL", "5", "20%", "1.2k", "3.4M", "0", "0", "$12.34", "1.5s", "-", "7", "2"}, strings.Fields(lines[3]))
	assert.Equal(t, "", lines[4])
	assert.Equal(t, "BUCKET", strings.Fields(lines[5])[0])
	assert.Equal(t, []string{"2026-11-14", "5", "0%", "0", "0", "0", "0", "$2.00", "-", "-", "0", "0"}, strings.Fields(lines[6]))
}

func TestStatsToolsOrbsOutputTables(t *testing.T) {
	h := newStatsHarness(t)
	h.f.json("GET /api/v1/stats/tools", 200, apigen.ToolStats{
		Tools: []apigen.ToolStat{{Tool: "bash", Calls: 8, Errors: 1}},
		Total: apigen.ToolStat{Calls: 8, Errors: 1},
	})
	out, _, err := h.run("tools")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Equal(t, []string{"TOOL", "CALLS", "ERRORS", "ERR%"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"bash", "8", "1", "13%"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"TOTAL", "8", "1", "13%"}, strings.Fields(lines[2]))

	h.f.json("GET /api/v1/stats/orbs", 200, apigen.OrbStats{
		GroupBy: "type",
		Groups: []apigen.OrbStat{{Key: "medium", Orbs: 2, RunningMs: 83 * 60_000, FrozenMs: 83 * 60_000, StoppedMs: 42_000,
			Spawns: 2, SnapshotSpawns: 1, Wakes: 3, Failures: 1}},
		Total: apigen.OrbStat{Key: "total", Orbs: 2, RunningMs: 83 * 60_000, FrozenMs: 83 * 60_000, StoppedMs: 42_000,
			Spawns: 2, SnapshotSpawns: 1, Wakes: 3, Failures: 1},
		Setup: apigen.OrbSetupStats{Runs: 3, Failures: 1, SlowRuns: 1, P50Ms: new(int64(62_000)), ResumeRuns: 4},
	})
	out, _, err = h.run("orbs")
	require.NoError(t, err)
	lines = strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Equal(t, []string{"KEY", "ORBS", "RUNNING", "FROZEN", "FROZEN%", "STOPPED", "SPAWNS", "WAKES", "FAILS"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"medium", "2", "1h23m", "1h23m", "50%", "42s", "2", "3", "1"}, strings.Fields(lines[1]))
	assert.Equal(t, "TOTAL", strings.Fields(lines[2])[0])
	assert.Contains(t, out, "Setup   runs 3  failures 1  slow(≥2m) 1  p50 1m2s  p95 -  max -")
	assert.Contains(t, out, "Resume  runs 4  failures 0")
	assert.Contains(t, out, "Snapshot spawns  1/2 (50%)")

	h.f.json("GET /api/v1/stats/output", 200, apigen.OutputStats{
		Groups: []apigen.OutputStat{{Key: "prj_A", Label: new("demo"), ThreadsCreated: 3, ThreadsShipped: 2, ThreadsArchived: 1, ShippedCostUsd: 3, Commits: 9, Turns: 5}, {Key: ""}},
		Total:  apigen.OutputStat{Key: "total", ThreadsCreated: 3, ThreadsShipped: 2, ThreadsArchived: 1, ShippedCostUsd: 3, Commits: 9, Turns: 5},
	})
	out, _, err = h.run("output", "--by", "project")
	require.NoError(t, err)
	lines = strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Equal(t, []string{"KEY", "CREATED", "SHIPPED", "ARCHIVED", "COMMITS", "TURNS", "COST/SHIPPED"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"demo", "3", "2", "1", "9", "5", "$1.50"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"(no", "project)", "0", "0", "0", "0", "0", "-"}, strings.Fields(lines[2]))
	assert.Equal(t, "TOTAL", strings.Fields(lines[3])[0])
	assert.Equal(t, "project", h.lastQuery(t).Get("groupBy"))
}

func TestStatsThread(t *testing.T) {
	h := newStatsHarness(t)
	created := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	thread := apigen.Thread{ID: "T-1", Title: "修复登录", State: apigen.ThreadStateIdle, Labels: []string{}, TitleSource: "auto",
		CreatedAt: created.UnixMilli(), OrbID: new("orb-x")}
	h.f.json("GET /api/v1/threads/T-1", 200, thread)
	h.f.mu.Lock()
	h.f.routes["GET /api/v1/stats/usage"] = func(w http.ResponseWriter, _ []byte) {
		reqs := h.f.requests()
		q, _ := url.ParseQuery(reqs[len(reqs)-1].RawQuery)
		st := apigen.UsageStats{Total: apigen.UsageStat{Key: "total", Requests: 3, CostUsd: 0.75, Turns: 1}}
		switch q.Get("groupBy") {
		case "model":
			st.Groups = []apigen.UsageStat{{Key: "anthropic/claude-x", Requests: 3, CostUsd: 0.75}}
		case "purpose":
			st.Groups = []apigen.UsageStat{{Key: "", Requests: 2, CostUsd: 0.7}, {Key: "title", Requests: 1, CostUsd: 0.05}}
		}
		_ = json.NewEncoder(w).Encode(st)
	}
	h.f.mu.Unlock()
	h.f.json("GET /api/v1/stats/tools", 200, apigen.ToolStats{Tools: []apigen.ToolStat{{Tool: "read", Calls: 2}}, Total: apigen.ToolStat{Calls: 2}})
	h.f.json("GET /api/v1/stats/orbs", 200, apigen.OrbStats{
		Groups: []apigen.OrbStat{{Key: "orb-x", Type: new("small"), RunningMs: 3_600_000, FrozenMs: 125_000, SetupMs: 30_000}},
		Total:  apigen.OrbStat{Key: "total"},
	})

	out, _, err := h.run("thread", "T-1", "--tz", "Asia/Shanghai")
	require.NoError(t, err)
	reqs := h.f.requests()
	require.Len(t, reqs, 6)
	want := []struct{ path, groupBy, orb string }{
		{"/api/v1/threads/T-1", "", ""},
		{"/api/v1/stats/usage", "thread", ""},
		{"/api/v1/stats/usage", "model", ""},
		{"/api/v1/stats/usage", "purpose", ""},
		{"/api/v1/stats/tools", "", ""},
		{"/api/v1/stats/orbs", "orb", "orb-x"},
	}
	for i, w := range want {
		assert.Equal(t, w.path, reqs[i].Path)
		if i == 0 {
			continue
		}
		q, _ := url.ParseQuery(reqs[i].RawQuery)
		assert.Equal(t, w.groupBy, q.Get("groupBy"), w.path)
		assert.Equal(t, ms(created), q.Get("from"), "窗口从线程创建开始")
		assert.Equal(t, ms(statsNow), q.Get("to"))
		assert.Equal(t, "Asia/Shanghai", q.Get("tz"))
		if w.path == "/api/v1/stats/orbs" {
			assert.Equal(t, "orb-x", q.Get("orb"))
			assert.Empty(t, q.Get("thread"))
		} else {
			assert.Equal(t, "T-1", q.Get("thread"))
		}
	}
	assert.Contains(t, out, "Window  2026-11-10 16:00:00 → now (Asia/Shanghai)")
	lines := strings.Split(out, "\n")
	idx := func(s string) int {
		for i, l := range lines {
			if strings.HasPrefix(l, s) {
				return i
			}
		}
		t.Fatalf("输出缺少 %q：\n%s", s, out)
		return -1
	}
	assert.Equal(t, "$0.7500", strings.Fields(lines[idx("TOTAL ")])[7], "总计行成本")
	assert.Less(t, idx("Total:"), idx("By model:"))
	assert.Less(t, idx("By model:"), idx("By purpose:"))
	assert.Less(t, idx("By purpose:"), idx("Tools:"))
	assert.Less(t, idx("Tools:"), idx("Orb:"))
	assert.Equal(t, "anthropic/claude-x", strings.Fields(lines[idx("By model:")+2])[0])
	assert.Equal(t, "(turns)", strings.Fields(lines[idx("By purpose:")+2])[0])
	assert.Equal(t, "title", strings.Fields(lines[idx("By purpose:")+3])[0])
	orb := idx("Orb:")
	assert.Equal(t, []string{"ORB", "TYPE", "RUNNING", "FROZEN", "STOPPED", "SETUP"}, strings.Fields(lines[orb+1]))
	assert.Equal(t, []string{"orb-x", "small", "1h0m", "2m5s", "0s", "30s"}, strings.Fields(lines[orb+2]))

	// --json：各响应原样嵌入
	out, _, err = h.run("thread", "T-1", "--json")
	require.NoError(t, err)
	var res map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.ElementsMatch(t, []string{"thread", "total", "models", "purposes", "tools", "orb"}, keys(res))

	// 没有 orb：不请求 orb 统计、不打印 Orb 段
	thread.OrbID = nil
	h.f.json("GET /api/v1/threads/T-1", 200, thread)
	n := len(h.f.requests())
	out, _, err = h.run("thread", "T-1")
	require.NoError(t, err)
	assert.Len(t, h.f.requests(), n+5)
	assert.NotContains(t, out, "Orb:")
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestStatsBudget(t *testing.T) {
	h := newStatsHarness(t)
	cur := apigen.BudgetStatus{
		Budget: apigen.Budget{DailyUsd: new(1.0), MonthlyUsd: new(100.0), WarnPct: 70, Timezone: "UTC", Enabled: false},
		Day:    apigen.BudgetPeriod{Key: "2026-11-15", SpentUsd: 0.5, LimitUsd: new(1.0), Level: apigen.BudgetLevelNone},
		Month:  apigen.BudgetPeriod{Key: "2026-11", SpentUsd: 12.5, Level: apigen.BudgetLevelWarn},
	}
	h.f.json("GET /api/v1/stats/budget", 200, cur)
	h.f.json("PUT /api/v1/stats/budget", 200, cur)

	out, _, err := h.run("budget")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Equal(t, []string{"Budget", "disabled", "tz", "UTC", "warn", "70%"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"PERIOD", "KEY", "SPENT", "LIMIT", "USED", "LEVEL"}, strings.Fields(lines[2]))
	assert.Equal(t, []string{"day", "2026-11-15", "$0.5000", "$1.00", "50%", "none"}, strings.Fields(lines[3]))
	assert.Equal(t, []string{"month", "2026-11", "$12.50", "-", "-", "warn"}, strings.Fields(lines[4]))
	_, _, err = h.run("budget", "show")
	require.NoError(t, err)

	lastPut := func() string {
		reqs := h.f.requests()
		r := reqs[len(reqs)-1]
		require.Equal(t, "PUT", r.Method)
		require.Equal(t, "GET", reqs[len(reqs)-2].Method, "先读后写")
		return string(r.Body)
	}
	// 只改给出的项，其余保留现值；set 会启用
	_, _, err = h.run("budget", "set", "--daily", "5", "--tz", "Asia/Shanghai")
	require.NoError(t, err)
	assert.JSONEq(t, `{"dailyUsd":5,"monthlyUsd":100,"warnPct":70,"timezone":"Asia/Shanghai","enabled":true}`, lastPut())

	_, _, err = h.run("budget", "set", "--warn", "80")
	require.NoError(t, err)
	assert.JSONEq(t, `{"dailyUsd":1,"monthlyUsd":100,"warnPct":80,"timezone":"UTC","enabled":true}`, lastPut())

	// --monthly 0：取消月上限
	_, _, err = h.run("budget", "set", "--monthly", "0")
	require.NoError(t, err)
	assert.JSONEq(t, `{"dailyUsd":1,"warnPct":70,"timezone":"UTC","enabled":true}`, lastPut())

	_, _, err = h.run("budget", "off")
	require.NoError(t, err)
	assert.JSONEq(t, `{"dailyUsd":1,"monthlyUsd":100,"warnPct":70,"timezone":"UTC","enabled":false}`, lastPut())

	// --json 原样打印 PUT 的响应
	h.f.raw("PUT /api/v1/stats/budget", "application/json", `{"budget":{"warnPct":0,"timezone":"UTC","enabled":false},"day":{"key":"d","from":0,"to":1,"spentUsd":0,"level":"none"},"month":{"key":"m","from":0,"to":1,"spentUsd":0,"level":"none"}}`)
	out, _, err = h.run("budget", "off", "--json")
	require.NoError(t, err)
	assert.Equal(t, `{"budget":{"warnPct":0,"timezone":"UTC","enabled":false},"day":{"key":"d","from":0,"to":1,"spentUsd":0,"level":"none"},"month":{"key":"m","from":0,"to":1,"spentUsd":0,"level":"none"}}`+"\n", out)

	n := len(h.f.requests())
	_, _, err = h.run("budget", "set", "--daily", "-1")
	requireUsageErr(t, err)
	assert.Len(t, h.f.requests(), n)
}

func TestStatsFormatters(t *testing.T) {
	assert.Equal(t, "999", fmtTokens(999))
	assert.Equal(t, "1.2k", fmtTokens(1234))
	assert.Equal(t, "1.0M", fmtTokens(999_999))
	assert.Equal(t, "3.4M", fmtTokens(3_400_000))
	assert.Equal(t, "2.5B", fmtTokens(2_500_000_000))
	assert.Equal(t, "$0.0042", fmtUSD(0.0042))
	assert.Equal(t, "$12.34", fmtUSD(12.34))
	assert.Equal(t, "$0.00", fmtUSD(0))
	assert.Equal(t, "1h23m", fmtDur(83*60_000+5_000))
	assert.Equal(t, "5m12s", fmtDur(312_000))
	assert.Equal(t, "42s", fmtDur(42_400))
	assert.Equal(t, "33%", fmtPct(1, 3))
	assert.Equal(t, "-", fmtPct(1, 0))
}

// 服务端文本（模型 / 用途 / 工具名 / 标题 / 预算键）里的控制字符不进终端：表格总是替换，
// --csv 只在写终端时替换（重定向到文件时原样），--json 原样。
func TestStatsTerminalControlChars(t *testing.T) {
	const evil = "a\x1b[31mb\x1b]52;c;ZXZpbA==\x07\u009b2J\u2028c\rd\te"
	want := []string{"a", "[31mb", "]52;c;ZXZpbA==", "2J", "c", "d", "e"}
	assertClean := func(t *testing.T, out string) {
		t.Helper()
		for _, bad := range []string{"\x1b", "\x07", "\u009b", "\u2028", "\r", "\t"} {
			assert.NotContains(t, out, bad)
		}
	}
	firstLine := func(t *testing.T, out string, n int) []string {
		t.Helper()
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		require.Greater(t, len(lines), n)
		return strings.Fields(lines[n])
	}
	h := newStatsHarness(t)
	h.tty = true

	h.f.json("GET /api/v1/stats/usage", 200, apigen.UsageStats{
		Groups: []apigen.UsageStat{{Key: evil, Requests: 1}},
		Series: []apigen.UsageSeriesPoint{{Key: evil, Total: apigen.UsageStat{Requests: 1}}},
	})
	out, _, err := h.run("--by", "purpose", "--series", "day")
	require.NoError(t, err)
	assertClean(t, out)
	assert.Equal(t, want, firstLine(t, out, 1)[:len(want)], "用途键")
	assert.Equal(t, want, firstLine(t, out, 5)[:len(want)], "桶键")

	h.f.json("GET /api/v1/stats/usage", 200, apigen.UsageStats{Groups: []apigen.UsageStat{{Key: "T-1", Label: new(evil), Requests: 1}}})
	out, _, err = h.run("--by", "thread")
	require.NoError(t, err)
	assertClean(t, out)
	assert.Equal(t, want, firstLine(t, out, 1)[:len(want)], "线程标题")

	h.f.json("GET /api/v1/stats/tools", 200, apigen.ToolStats{Tools: []apigen.ToolStat{{Tool: evil, Calls: 1}}})
	out, _, err = h.run("tools")
	require.NoError(t, err)
	assertClean(t, out)
	assert.Equal(t, want, firstLine(t, out, 1)[:len(want)], "工具名")

	h.f.json("GET /api/v1/stats/orbs", 200, apigen.OrbStats{Groups: []apigen.OrbStat{{Key: "prj_a", Label: new(evil)}}})
	out, _, err = h.run("orbs", "--by", "project")
	require.NoError(t, err)
	assertClean(t, out)
	assert.Equal(t, want, firstLine(t, out, 1)[:len(want)], "orb 分组标签")

	h.f.json("GET /api/v1/stats/output", 200, apigen.OutputStats{Groups: []apigen.OutputStat{{Key: "prj_a", Label: new(evil)}}})
	out, _, err = h.run("output", "--by", "project")
	require.NoError(t, err)
	assertClean(t, out)
	assert.Equal(t, want, firstLine(t, out, 1)[:len(want)], "产出分组标签")

	h.f.json("GET /api/v1/stats/budget", 200, apigen.BudgetStatus{
		Budget: apigen.Budget{Timezone: evil, Enabled: true},
		Day:    apigen.BudgetPeriod{Key: evil, Level: apigen.BudgetLevel(evil)},
	})
	out, _, err = h.run("budget")
	require.NoError(t, err)
	assertClean(t, out)

	h.f.json("GET /api/v1/threads/T-1", 200, apigen.Thread{ID: "T-1", Title: evil, State: apigen.ThreadStateIdle, Labels: []string{}, TitleSource: "auto",
		CreatedAt: statsNow.Add(-time.Hour).UnixMilli()})
	out, _, err = h.run("thread", "T-1")
	require.NoError(t, err)
	assertClean(t, out)
	assert.Equal(t, append([]string{"Thread", "T-1"}, want...), firstLine(t, out, 0))

	// --csv：写终端时替换控制字符（换行保留，非法 UTF-8 成 U+FFFD），写文件时原样
	const csvBody = "key,n\r\n\"a\x1b[2Jb\u009b\u2028中\xff\",1\n"
	h.f.raw("GET /api/v1/stats/usage", "text/csv", csvBody)
	out, _, err = h.run("--csv")
	require.NoError(t, err)
	assert.Equal(t, "key,n \n\"a [2Jb  中\uFFFD\",1\n", out)
	h.tty = false
	out, _, err = h.run("--csv")
	require.NoError(t, err)
	assert.Equal(t, csvBody, out, "重定向到文件：原样")

	// --json 不动（终端也不动：JSON 字符串里的控制字符本就以 \u 转义）
	const jsonBody = `{"groups":[{"key":"a\u001b[31mb"}]}`
	h.f.raw("GET /api/v1/stats/usage", "application/json", jsonBody)
	h.tty = true
	out, _, err = h.run("--json")
	require.NoError(t, err)
	assert.Equal(t, jsonBody, out)
}
