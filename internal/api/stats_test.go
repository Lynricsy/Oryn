package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/stats"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// statsD 是统计接口测试的基准日（UTC 零点）。
var statsD = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).UnixMilli()

const dayMillis = int64(24 * time.Hour / time.Millisecond)

type statsEnv struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	srv *httptest.Server
	dev string
	seq int64
}

func newStatsEnv(t *testing.T, withService bool) *statsEnv {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	cfg := config.DefaultOrynd()
	cfg.PublicURL, cfg.DataDir = "https://oryn.test", dir
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st, AuthMiddleware: auth.Middleware(st)}}
	if withService {
		now := time.UnixMilli(statsD + 3*dayMillis)
		s.Stats = &api.StatsDeps{Service: stats.New(stats.Config{Now: func() time.Time { return now }, Retention: cfg.Stats.Retention}, st, nil, nil)}
	}
	e := &statsEnv{t: t, ctx: ctx, st: st, srv: httptest.NewServer(api.NewRouter(s))}
	t.Cleanup(func() { e.srv.Close(); _ = st.Close() })
	_, e.dev, err = auth.CreateDevice(ctx, st, "t", "cli", "")
	require.NoError(t, err)
	require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: "prj_a", Name: "alpha", CreatedAt: 1, UpdatedAt: 1}))
	for _, id := range []string{"T-1", "T-2"} {
		p := db.CreateThreadParams{ID: id, Title: "title " + id, TitleSource: "auto", State: "idle", StateChangedAt: 1, SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: "work"}
		if id == "T-1" {
			p.ProjectID = new("prj_a")
		}
		require.NoError(t, st.Q.CreateThread(ctx, p))
	}
	return e
}

func (e *statsEnv) usage(thread string, ts int64, model string, cost float64) {
	e.t.Helper()
	e.seq++
	_, err := e.st.Q.InsertUsageRecord(e.ctx, db.InsertUsageRecordParams{ThreadID: new(thread), EntrySeq: e.seq, Ts: ts, Provider: "p", Model: model, InputTokens: 10, OutputTokens: 5, CostUsd: cost})
	require.NoError(e.t, err)
}

// get 发 GET 并返回状态码、响应头与响应体。
func (e *statsEnv) get(path string) (int, http.Header, []byte) {
	e.t.Helper()
	return e.req("GET", path, nil)
}

func (e *statsEnv) req(method, path string, body any) (int, http.Header, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+e.dev)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

func (e *statsEnv) json(path string, out any) {
	e.t.Helper()
	code, _, raw := e.get(path)
	require.Equal(e.t, 200, code, string(raw))
	require.NoError(e.t, json.Unmarshal(raw, out), string(raw))
}

func statsErrCode(t *testing.T, raw []byte) string {
	t.Helper()
	var b struct{ Code string }
	require.NoError(t, json.Unmarshal(raw, &b), string(raw))
	return b.Code
}

func csvRows(t *testing.T, raw []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	require.NoError(t, err, string(raw))
	return rows
}

func win(extra string) string {
	return fmt.Sprintf("from=%d&to=%d%s", statsD, statsD+3*dayMillis, extra)
}

func TestStatsUsageAPI(t *testing.T) {
	e := newStatsEnv(t, true)
	e.usage("T-1", statsD+time.Hour.Milliseconds(), "m1", 1.0)
	e.usage("T-1", statsD+dayMillis+1, "m2", 0.5)
	e.usage("T-2", statsD+2*dayMillis+1, "m1", 2.0)

	var u apigen.UsageStats
	e.json("/api/v1/stats/usage?groupBy=model&"+win(""), &u)
	assert.Equal(t, apigen.UsageGroupByModel, u.GroupBy)
	require.Len(t, u.Groups, 2)
	assert.Equal(t, "p/m1", u.Groups[0].Key)
	assert.InDelta(t, 3.5, u.Total.CostUsd, 1e-9)
	assert.Equal(t, "UTC", u.Tz)

	e.json("/api/v1/stats/usage?groupBy=day&tz=Asia/Shanghai&"+win(""), &u)
	assert.Equal(t, "Asia/Shanghai", u.Tz)
	assert.Len(t, u.Groups, 4, "上海时区下窗口跨 4 个本地日")

	e.json("/api/v1/stats/usage?groupBy=project&project=none&"+win(""), &u)
	assert.InDelta(t, 2.0, u.Total.CostUsd, 1e-9)

	// 缺省窗口 = 近 7 天（只读聚合用真实时钟）
	before := time.Now().UnixMilli()
	e.json("/api/v1/stats/usage?groupBy=total", &u)
	assert.GreaterOrEqual(t, u.To, before+1)
	assert.EqualValues(t, stats.DefaultWindow.Milliseconds(), u.To-u.From)

	// 错误码
	for q, want := range map[string]string{
		"groupBy=nope&" + win(""):                                              "invalid_request",
		"groupBy=day&series=day&" + win(""):                                    "invalid_request",
		"groupBy=model&limit=501&" + win(""):                                   "invalid_request",
		fmt.Sprintf("groupBy=model&from=%d&to=%d", statsD, statsD):             "invalid_request",
		"groupBy=model&tz=Mars/Phobos&" + win(""):                              "invalid_timezone",
		fmt.Sprintf("groupBy=hour&from=%d&to=%d", statsD, statsD+60*dayMillis): "range_too_large",
		"groupBy=model&format=xml&" + win(""):                                  "invalid_request",
	} {
		code, _, raw := e.get("/api/v1/stats/usage?" + q)
		assert.Equal(t, 400, code, q)
		assert.Equal(t, want, statsErrCode(t, raw), q)
	}

	// CSV：头与列
	code, h, raw := e.get("/api/v1/stats/usage?groupBy=model&format=csv&" + win(""))
	require.Equal(t, 200, code)
	assert.Equal(t, "text/csv; charset=utf-8", h.Get("Content-Type"))
	assert.Equal(t, `attachment; filename="oryn-usage-model-20261110-20261113.csv"`, h.Get("Content-Disposition"))
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "no-store", h.Get("Cache-Control"))
	assert.False(t, bytes.HasPrefix(raw, []byte("\xef\xbb\xbf")), "不写 BOM")
	rows := csvRows(t, raw)
	assert.Equal(t, []string{"bucket", "key", "label", "requests", "request_errors", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "subagent_cost_usd", "avg_ttft_ms", "tokens_per_second", "tool_calls", "tool_errors", "turns"}, rows[0])
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"", "p/m1", "", "2", "0", "20", "10", "0", "0", "3", "0", "", "", "0", "0", "0"}, rows[1])

	// 长表：一桶一组一行，只含非零的「桶 × 组」
	_, _, raw = e.get("/api/v1/stats/usage?groupBy=model&series=day&format=csv&" + win(""))
	rows = csvRows(t, raw)
	require.Len(t, rows, 4)
	assert.Equal(t, []string{"2026-11-10", "p/m1"}, rows[1][:2])
	assert.Equal(t, []string{"2026-11-11", "p/m2"}, rows[2][:2])

	// download=true 的 JSON 带同名 .json 附件头
	code, h, _ = e.get("/api/v1/stats/usage?groupBy=thread&download=true&" + win(""))
	require.Equal(t, 200, code)
	assert.Equal(t, `attachment; filename="oryn-usage-thread-20261110-20261113.json"`, h.Get("Content-Disposition"))
	assert.Equal(t, "application/json", h.Get("Content-Type"))
}

func TestStatsRecordsExportPages(t *testing.T) {
	e := newStatsEnv(t, false)
	tx, err := e.st.DB.BeginTx(e.ctx, nil)
	require.NoError(t, err)
	const n = 5001
	for i := range n {
		// 相同时间戳的行跨页：键集分页按 (ts, id)
		_, err := tx.ExecContext(e.ctx, `INSERT INTO usage_records (thread_id, entry_seq, ts, provider, model, input_tokens, output_tokens,
			cache_read_tokens, cache_write_tokens, cost_usd, is_subagent) VALUES ('T-1', ?, ?, 'p', 'm', 1, 2, 0, 0, 0.5, ?)`, i, statsD+int64(i/3), i%2 == 0)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	code, h, raw := e.get("/api/v1/stats/usage/records?" + win(""))
	require.Equal(t, 200, code, string(raw))
	assert.Equal(t, `attachment; filename="oryn-usage-records-20261110-20261113.csv"`, h.Get("Content-Disposition"))
	rows := csvRows(t, raw)
	assert.Equal(t, []string{"ts", "time", "thread_id", "project_id", "space_id", "provider", "model", "purpose", "subagent", "stop_reason", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "duration_ms", "ttft_ms"}, rows[0])
	require.Len(t, rows, n+1, "跨页无缺无重")
	assert.Equal(t, []string{fmt.Sprint(statsD), "2026-11-10T00:00:00Z", "T-1", "prj_a", "", "p", "m", "", "1", "", "1", "2", "0", "0", "0.5", "", ""}, rows[1])
	// 跨页：每个时间戳恰好 3 行（最后一个 2 行），且 ts 不减
	perTs := map[string]int{}
	prev := ""
	for _, r := range rows[1:] {
		perTs[r[0]]++
		assert.GreaterOrEqual(t, r[0], prev)
		prev = r[0]
	}
	assert.Len(t, perTs, (n+2)/3)
	assert.Equal(t, 3, perTs[fmt.Sprint(statsD+1666)], "第 5000 / 5001 行所在的时间戳跨页")

	_, _, raw = e.get("/api/v1/stats/usage/records?thread=T-2&" + win(""))
	assert.Len(t, csvRows(t, raw), 1, "只有表头")
}

func TestStatsToolsOrbsOutputAPI(t *testing.T) {
	e := newStatsEnv(t, true)
	_, err := e.st.Q.InsertToolCall(e.ctx, db.InsertToolCallParams{ThreadID: new("T-1"), EntrySeq: 1, Ts: statsD + 1, Tool: "bash", IsError: true})
	require.NoError(t, err)
	var ts apigen.ToolStats
	e.json("/api/v1/stats/tools?series=day&"+win(""), &ts)
	assert.Equal(t, []apigen.ToolStat{{Tool: "bash", Calls: 1, Errors: 1}}, ts.Tools)
	assert.Len(t, ts.Series, 3)
	_, h, raw := e.get("/api/v1/stats/tools?format=csv&" + win(""))
	assert.Equal(t, `attachment; filename="oryn-tools-tool-20261110-20261113.csv"`, h.Get("Content-Disposition"))
	assert.Equal(t, [][]string{{"bucket", "tool", "calls", "errors"}, {"", "bash", "1", "1"}}, csvRows(t, raw))

	require.NoError(t, e.st.Q.CreateProvider(e.ctx, db.CreateProviderParams{ID: "prov_1", Name: "p", TokenHash: "x", CreatedAt: 1}))
	require.NoError(t, e.st.Q.CreateOrb(e.ctx, db.CreateOrbParams{ID: "orb-a", ProviderID: "prov_1", Type: "small", Cpus: 2, MemoryMb: 4096, State: "ready", StateChangedAt: 1, TokenHash: "h", CreatedAt: statsD}))
	require.NoError(t, e.st.Q.InsertOrbEvent(e.ctx, db.InsertOrbEventParams{OrbID: "orb-a", Ts: statsD, FromState: "", ToState: "ready", Detail: "{}"}))
	var os apigen.OrbStats
	e.json("/api/v1/stats/orbs?"+win(""), &os)
	assert.Equal(t, apigen.OrbGroupByType, os.GroupBy)
	require.Len(t, os.Groups, 1)
	assert.EqualValues(t, 3*dayMillis, os.Groups[0].RunningMs)
	_, h, raw = e.get("/api/v1/stats/orbs?groupBy=orb&format=csv&" + win(""))
	assert.Equal(t, `attachment; filename="oryn-orbs-orb-20261110-20261113.csv"`, h.Get("Content-Disposition"))
	rows := csvRows(t, raw)
	assert.Equal(t, []string{"key", "label", "type", "cpus", "memory_mb", "orbs", "running_ms", "frozen_ms", "stopped_ms", "setup_ms", "spawns", "snapshot_spawns", "freezes", "stops", "wakes", "failures"}, rows[0])
	assert.Equal(t, []string{"orb-a", "", "small", "2", "4096", "1", fmt.Sprint(3 * dayMillis), "0", "0", "0", "0", "0", "0", "0", "0", "0"}, rows[1])
	code, _, raw := e.get("/api/v1/stats/orbs?groupBy=model&" + win(""))
	assert.Equal(t, 400, code)
	assert.Equal(t, "invalid_request", statsErrCode(t, raw))

	var tl apigen.OrbTimeline
	e.json("/api/v1/orbs/orb-a/timeline?"+win(""), &tl)
	require.Len(t, tl.Segments, 1)
	assert.Equal(t, apigen.OrbStateReady, tl.Segments[0].State)
	code, _, raw = e.get("/api/v1/orbs/orb-nope/timeline")
	assert.Equal(t, 404, code)
	assert.Equal(t, "not_found", statsErrCode(t, raw))

	var out apigen.OutputStats
	e.json("/api/v1/stats/output?"+win(""), &out)
	assert.Equal(t, apigen.OutputGroupByDay, out.GroupBy)
	assert.Len(t, out.Groups, 3)
	_, _, raw = e.get("/api/v1/stats/output?groupBy=project&format=csv&" + win(""))
	assert.Equal(t, []string{"key", "label", "threads_created", "threads_shipped", "threads_archived", "shipped_cost_usd", "commits", "turns"}, csvRows(t, raw)[0])
}

func TestStatsMetaAndBudgetAPI(t *testing.T) {
	e := newStatsEnv(t, true)
	var m apigen.StatsMeta
	e.json("/api/v1/stats/meta", &m)
	assert.EqualValues(t, statsD+3*dayMillis, m.Now)
	assert.EqualValues(t, 720*3600*1000, m.Retention.SamplesMs)

	var b apigen.BudgetStatus
	e.json("/api/v1/stats/budget", &b)
	assert.Equal(t, apigen.Budget{WarnPct: 80, Timezone: "UTC"}, b.Budget)
	assert.Equal(t, apigen.BudgetLevelNone, b.Day.Level)

	e.usage("T-1", statsD+3*dayMillis, "m", 0.9) // 服务时钟恰在 11-13 零点：落在当日
	code, _, raw := e.req("PUT", "/api/v1/stats/budget", map[string]any{"dailyUsd": 1.0, "warnPct": 80, "timezone": "UTC", "enabled": true})
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, json.Unmarshal(raw, &b))
	assert.Equal(t, apigen.BudgetLevelWarn, b.Day.Level)
	assert.InDelta(t, 0.9, b.Day.SpentUsd, 1e-9)
	assert.Equal(t, "2026-11-13", b.Day.Key)

	for _, body := range []map[string]any{
		{"dailyUsd": 0.001, "warnPct": 80, "timezone": "UTC", "enabled": true},
		{"warnPct": 100, "timezone": "UTC", "enabled": true},
		{"warnPct": 80, "timezone": "Mars/Phobos", "enabled": true},
	} {
		code, _, raw := e.req("PUT", "/api/v1/stats/budget", body)
		assert.Equal(t, 400, code)
		assert.Equal(t, "invalid_budget", statsErrCode(t, raw))
	}

	// 没有统计服务：meta 与预算 501，只读聚合照常
	bare := newStatsEnv(t, false)
	code, _, _ = bare.get("/api/v1/stats/meta")
	assert.Equal(t, 501, code)
	code, _, _ = bare.get("/api/v1/stats/budget")
	assert.Equal(t, 501, code)
	code, _, _ = bare.get("/api/v1/stats/usage?groupBy=total")
	assert.Equal(t, 200, code)
}
