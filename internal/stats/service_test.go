package stats

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeOrbs 是假 orb 服务：EnsureUsable 返回固定结果，Do 按路径回固定 JSON 并记下请求。
// stuckOrb 非空时，对该 orb 的 Do 先向 entered 发一次信号，再阻塞到 release 关闭（不理会 ctx，模拟卡住的 orbd）；
// served 非空时，每个未卡住的应答在返回前把「orb 路径」发进去。
type fakeOrbs struct {
	mu        sync.Mutex
	ensureErr error
	status    string
	commits   string
	ensures   []string
	reqs      []string
	// workdirs 记录每个请求的 X-Oryn-Workdir（M11b；没带为空串）。
	workdirs []string

	stuckOrb string
	entered  chan struct{}
	release  chan struct{}
	served   chan string
}

func (o *fakeOrbs) EnsureUsable(_ context.Context, orbID, feature string, wake bool, _ time.Duration) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ensures = append(o.ensures, orbID+" "+feature+" wake="+map[bool]string{true: "1", false: "0"}[wake])
	return o.ensureErr
}

func (o *fakeOrbs) Do(_ context.Context, orbID string, req *http.Request) (*http.Response, error) {
	if o.stuckOrb != "" && orbID == o.stuckOrb {
		o.entered <- struct{}{}
		<-o.release
		return nil, errors.New("released")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reqs = append(o.reqs, orbID+" "+req.URL.Path+"?"+req.URL.RawQuery)
	o.workdirs = append(o.workdirs, req.Header.Get(orbs.HeaderWorkdir))
	body := o.status
	if req.URL.Path == "/git/commits" {
		body = o.commits
	}
	if o.served != nil {
		o.served <- orbID + " " + req.URL.Path
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func (o *fakeOrbs) requests() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.reqs...)
}

// fakeTimers 是假 AfterFunc：只登记，由测试决定何时触发。
type fakeTimers struct {
	mu      sync.Mutex
	fns     []func()
	delays  []time.Duration
	stopped []bool
}

func (ft *fakeTimers) AfterFunc(d time.Duration, f func()) func() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	i := len(ft.fns)
	ft.fns, ft.delays, ft.stopped = append(ft.fns, f), append(ft.delays, d), append(ft.stopped, false)
	return func() {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		ft.stopped[i] = true
	}
}

func (ft *fakeTimers) fire(i int) {
	ft.mu.Lock()
	f := ft.fns[i]
	ft.mu.Unlock()
	f()
}

func commitRows(t *testing.T, f *fixture, thread string) []string {
	t.Helper()
	rows, err := f.st.DB.QueryContext(f.ctx, `SELECT sha FROM thread_commits WHERE thread_id = ? ORDER BY sha`, thread)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	return out
}

// 合法的提交 sha：SHA-1 与 SHA-256。
var (
	sha1A   = strings.Repeat("a1", 20)
	sha1B   = strings.Repeat("b2", 20)
	sha256C = strings.Repeat("c3", 32)
)

// commitTestNow 是提交采样测试的服务时钟。
var commitTestNow = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

const statusWithBase = `{"repo":true,"base":{"name":"main","ref":"refs/remotes/origin/main"},"files":[],"scope":"uncommitted","truncated":false}`

// commitList 是 /git/commits 的应答体。
func commitList(t *testing.T, cs ...apigen.GitCommit) string {
	t.Helper()
	b, err := json.Marshal(apigen.GitCommitList{Commits: cs})
	require.NoError(t, err)
	return string(b)
}

func commit(sha string, ts int64) apigen.GitCommit {
	return apigen.GitCommit{Sha: sha, ShortSha: sha[:min(len(sha), 7)], Subject: "s", AuthorName: "a", AuthorEmail: "e", TS: ts}
}

// commitsJSON 是两个合法的提交。
var commitsJSON = func() string {
	b, _ := json.Marshal(apigen.GitCommitList{Commits: []apigen.GitCommit{commit(sha1B, 2000), commit(sha1A, 1000)}})
	return string(b)
}()

func commitFixture(t *testing.T, o *fakeOrbs, timers *fakeTimers) (*fixture, *Service) {
	cfg := Config{}
	if timers != nil {
		cfg.AfterFunc = timers.AfterFunc
	}
	return commitFixtureCfg(t, o, cfg, nil)
}

func commitFixtureCfg(t *testing.T, o *fakeOrbs, cfg Config, n Notifier) (*fixture, *Service) {
	f := newFixture(t)
	f.exec(`UPDATE projects SET repo_url = 'https://git.example/r.git', base_branch = 'main' WHERE id = 'prj_a'`)
	f.orb("orb-1", "small", 1, "prj_a", "", false)
	f.thread("W", "prj_a", "", "work")
	f.thread("W-norepo", "prj_b", "", "work")
	f.exec(`UPDATE threads SET orb_id = 'orb-1' WHERE id IN ('W', 'W-norepo')`)
	require.NoError(t, f.st.Q.InsertThreadCommit(f.ctx, db.InsertThreadCommitParams{ThreadID: "W", Sha: "old", Ts: 1}))
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return commitTestNow }
	}
	return f, New(cfg, f.st, o, n)
}

func TestSampleCommitsWithBase(t *testing.T) {
	o := &fakeOrbs{status: statusWithBase, commits: commitsJSON}
	f, s := commitFixture(t, o, nil)
	require.NoError(t, s.SampleCommits(f.ctx, "W"))
	assert.Equal(t, []string{sha1A, sha1B}, commitRows(t, f, "W"), "整体替换")
	assert.Equal(t, []string{"orb-1 git wake=0"}, o.ensures, "不唤醒、不计交互")
	assert.Equal(t, []string{"orb-1 /git/status?base=main&scope=uncommitted", "orb-1 /git/commits?base=main&limit=200"}, o.requests())
}

// runner 线程（M11b）：采样提交的 git 请求带线程的工作目录；普通线程不带。
func TestSampleCommitsWorkdir(t *testing.T) {
	o := &fakeOrbs{status: statusWithBase, commits: commitsJSON}
	f, s := commitFixture(t, o, nil)
	require.NoError(t, s.SampleCommits(f.ctx, "W"))
	assert.Equal(t, []string{"", ""}, o.workdirs)
	f.exec(`UPDATE threads SET cwd = '/home/me/app' WHERE id = 'W'`)
	o.workdirs = nil
	require.NoError(t, s.SampleCommits(f.ctx, "W"))
	assert.Equal(t, []string{"/home/me/app", "/home/me/app"}, o.workdirs)
}

func TestSampleCommitsWithoutBase(t *testing.T) {
	for name, status := range map[string]string{
		"无基线":  `{"repo":true,"files":[],"scope":"uncommitted","truncated":false}`,
		"不是仓库": `{"repo":false,"files":[],"scope":"uncommitted","truncated":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			o := &fakeOrbs{status: status, commits: commitsJSON}
			f, s := commitFixture(t, o, nil)
			require.NoError(t, s.SampleCommits(f.ctx, "W"))
			assert.Empty(t, commitRows(t, f, "W"), "清空")
			for _, r := range o.requests() {
				assert.NotContains(t, r, "/git/commits", "没有基线时绝不取 /git/commits")
			}
			assert.Len(t, o.requests(), 1)
		})
	}
}

func TestSampleCommitsSkips(t *testing.T) {
	o := &fakeOrbs{status: statusWithBase, commits: commitsJSON, ensureErr: errors.New("orb is frozen")}
	f, s := commitFixture(t, o, nil)
	assert.Error(t, s.SampleCommits(f.ctx, "W"))
	assert.Empty(t, o.requests(), "orb 不可用：不请求")
	assert.Equal(t, []string{"old"}, commitRows(t, f, "W"), "不改库")

	o.ensureErr, o.ensures = nil, nil
	require.NoError(t, s.SampleCommits(f.ctx, "W-norepo"))
	assert.Empty(t, o.ensures, "项目没有仓库：什么都不做")
}

func TestStateChangedDebounce(t *testing.T) {
	o := &fakeOrbs{status: statusWithBase, commits: commitsJSON}
	timers := &fakeTimers{}
	f, s := commitFixture(t, o, timers)

	s.StateChanged(threads.StateEvent{ThreadID: "W", Prev: threads.StateIdle, State: threads.StateIdle})
	s.StateChanged(threads.StateEvent{ThreadID: "W", Prev: threads.StateError, State: threads.StateIdle})
	s.StateChanged(threads.StateEvent{ThreadID: "W", Prev: threads.StateIdle, State: threads.StateWorking})
	assert.Empty(t, timers.fns, "只有 working / awaiting_input → idle 才采样")

	s.StateChanged(threads.StateEvent{ThreadID: "W", Prev: threads.StateWorking, State: threads.StateIdle})
	s.StateChanged(threads.StateEvent{ThreadID: "W", Prev: threads.StateAwaitingInput, State: threads.StateIdle})
	require.Len(t, timers.fns, 2)
	assert.Equal(t, DefaultSampleDelay, timers.delays[0])
	assert.True(t, timers.stopped[0], "后到的一次取代先到的")
	timers.fire(0) // 已被取代的计时器即便触发也作废
	assert.Empty(t, s.wake)
	timers.fire(1)
	require.Len(t, s.wake, 1)
	<-s.wake
	s.drainSamples(f.ctx)
	assert.Len(t, o.requests(), 2, "恰好采样一次（status + commits）")
	assert.Equal(t, []string{sha1A, sha1B}, commitRows(t, f, "W"))
	s.drainSamples(f.ctx)
	assert.Len(t, o.requests(), 2, "队列已清空")
}

// orbd 的应答不可信：sha 不是 40 / 64 位小写十六进制、时间不在 [线程创建 − 1 年, 现在 + 1 天] 内的行都丢弃。
func TestSampleCommitsRejectsInvalid(t *testing.T) {
	o := &fakeOrbs{status: statusWithBase}
	f, s := commitFixture(t, o, nil)
	now := commitTestNow.UnixMilli()
	created := now - 10*dayMs
	f.exec(`UPDATE threads SET created_at = ? WHERE id = 'W'`, created)
	hex := func(n int) string { return strings.Repeat("0123456789abcdef", 8)[:n] }
	keep := []apigen.GitCommit{
		commit(sha1A, now-dayMs),
		commit(sha256C, now-dayMs),
		commit(sha1B, now+dayMs-1),                 // 未来不到一天（时钟偏差）
		commit(hex(40), created-365*dayMs),         // 下限本身
		commit(strings.Repeat("d", 40), now+dayMs), // 上限本身
	}
	drop := []apigen.GitCommit{
		commit("c1", now),
		commit(strings.ToUpper(sha1A), now),
		commit(hex(39), now),
		commit(hex(41), now),
		commit(hex(63), now),
		commit(hex(65), now),
		commit(hex(39)+"g", now),
		commit(sha1A+"\n", now),
		commit(" "+hex(40)[1:], now),
		commit(strings.Repeat("e", 4096), now),
		commit(strings.Repeat("f", 40), now+dayMs+1),         // 远期
		commit(strings.Repeat("9", 40), created-365*dayMs-1), // 早于线程创建一年以上
		commit(strings.Repeat("8", 40), -1),
	}
	o.commits = commitList(t, append(drop, keep...)...)
	require.NoError(t, s.SampleCommits(f.ctx, "W"))
	var want []string
	for _, c := range keep {
		want = append(want, c.Sha)
	}
	assert.ElementsMatch(t, want, commitRows(t, f, "W"))
}

// chanNotifier 把每条系统通知的标题发进通道。
type chanNotifier struct{ ch chan string }

func (n chanNotifier) PostSystem(_ context.Context, _, title, _, _ string) error {
	n.ch <- title
	return nil
}

// 一个卡住的 orb 只占住一个采样 worker：预算评估照常按周期执行，另一个 orb 上的线程照常采样。
func TestStuckOrbDoesNotDelayBudget(t *testing.T) {
	o := &fakeOrbs{status: statusWithBase, commits: commitsJSON, stuckOrb: "orb-1",
		entered: make(chan struct{}, 1), release: make(chan struct{}), served: make(chan string, 8)}
	timers := &fakeTimers{}
	n := chanNotifier{ch: make(chan string, 4)}
	f, s := commitFixtureCfg(t, o, Config{AfterFunc: timers.AfterFunc, BudgetInterval: time.Millisecond}, n)
	f.orb("orb-2", "small", 1, "prj_a", "", false)
	f.thread("W2", "prj_a", "", "work")
	f.exec(`UPDATE threads SET orb_id = 'orb-2' WHERE id = 'W2'`)

	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() {
		close(o.release)
		cancel()
		<-done
	}()
	wait := func(what string, ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatal(what)
		}
	}

	s.StateChanged(threads.StateEvent{ThreadID: "W", Prev: threads.StateWorking, State: threads.StateIdle})
	timers.fire(0)
	wait("W 的采样没有开始", o.entered)

	// W 的采样卡在 orb-1 上：另一个 worker 仍采样 orb-2 上的 W2
	s.StateChanged(threads.StateEvent{ThreadID: "W2", Prev: threads.StateWorking, State: threads.StateIdle})
	timers.fire(1)
	served := make(chan struct{})
	go func() {
		for p := range o.served {
			if p == "orb-2 /git/commits" {
				close(served)
				return
			}
		}
	}()
	wait("W2 的采样被卡住的 orb 挡住", served)

	// 采样卡住期间才设预算并越线：只有预算循环独立于采样，才会发出告警
	_, err := s.SetBudget(f.ctx, apigen.Budget{Enabled: true, DailyUsd: new(1.0), Timezone: "UTC"})
	require.NoError(t, err)
	f.usage("W2", commitTestNow.UnixMilli()-1, "m", 2)
	select {
	case title := <-n.ch:
		assert.Equal(t, "Daily budget exceeded", title)
	case <-time.After(10 * time.Second):
		t.Fatal("预算评估被卡住的采样推迟")
	}
}

func TestSweep(t *testing.T) {
	f := newFixture(t)
	now := ms("2027-06-01T00:00:00Z")
	day := dayMs
	f.orb("orb-live", "small", 1, "", "", false)
	f.orb("orb-dead", "small", 1, "", "", false)
	f.orb("orb-recent", "small", 1, "", "", false)
	require.NoError(t, f.st.Q.MarkOrbDeleted(f.ctx, db.MarkOrbDeletedParams{ID: "orb-dead", DeletedAt: now - 400*day}))
	require.NoError(t, f.st.Q.MarkOrbDeleted(f.ctx, db.MarkOrbDeletedParams{ID: "orb-recent", DeletedAt: now - 10*day}))
	for _, o := range []string{"orb-live", "orb-dead", "orb-recent"} {
		f.ev(o, now-500*day, "", "requested")
		f.setupRun(o, "setup", now-500*day, 1, true, true)
	}
	for _, ts := range []int64{now - 31*day, now - day} {
		require.NoError(t, f.st.Q.InsertOrbSample(f.ctx, db.InsertOrbSampleParams{OrbID: "orb-live", Ts: ts}))
	}
	for _, ts := range []int64{now - 400*day, now - 10*day} {
		_, err := f.st.Q.InsertBudgetAlert(f.ctx, db.InsertBudgetAlertParams{Period: "day", PeriodKey: time.UnixMilli(ts).UTC().Format("2006-01-02"), Level: "warn", CreatedAt: ts})
		require.NoError(t, err)
	}
	f.thread("T", "", "", "work")
	f.usage("T", now-100*day, "m", 1)
	f.usage("T", now-day, "m", 1)
	f.tool("T", now-100*day, "bash", false)
	f.turn("T", now-100*day)

	count := func(table string) int {
		var n int
		require.NoError(t, f.st.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM "+table).Scan(&n))
		return n
	}
	clock := func() time.Time { return time.UnixMilli(now) }
	// 全部为 0：什么都不删
	New(Config{Now: clock}, f.st, nil, nil).Sweep(f.ctx)
	assert.Equal(t, []int{2, 3, 3, 2, 2, 1, 1}, []int{count("orb_samples"), count("orb_events"), count("orb_setup_runs"), count("budget_alerts"), count("usage_records"), count("tool_calls"), count("turns")})

	ret := config.DefaultOrynd().Stats.Retention
	ret.Usage = config.Duration(90 * 24 * time.Hour)
	New(Config{Now: clock, Retention: ret}, f.st, nil, nil).Sweep(f.ctx)
	assert.Equal(t, 1, count("orb_samples"), "720h 前的采样被删")
	assert.Equal(t, 2, count("orb_events"), "只删一年前就已删除的 orb 的事件；活 orb 的事件不删")
	assert.Equal(t, 2, count("orb_setup_runs"))
	assert.Equal(t, 1, count("budget_alerts"))
	assert.Equal(t, 1, count("usage_records"))
	assert.Equal(t, 0, count("tool_calls"))
	assert.Equal(t, 0, count("turns"))
	var live int
	require.NoError(t, f.st.DB.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM orb_events WHERE orb_id = 'orb-live'`).Scan(&live))
	assert.Equal(t, 1, live)
}

func TestMeta(t *testing.T) {
	f := newFixture(t)
	ret := config.StatsRetention{Samples: config.Duration(720 * time.Hour), Events: config.Duration(24 * time.Hour)}
	s := New(Config{Now: func() time.Time { return time.UnixMilli(5000) }, Retention: ret}, f.st, nil, nil)
	m, err := s.Meta(f.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 5000, m.Now)
	assert.Equal(t, 0, m.FactsVersion)
	assert.Nil(t, m.Backfill)
	assert.Nil(t, m.EarliestTS, "没有用量时省略")
	assert.EqualValues(t, 720*3600*1000, m.Retention.SamplesMs)
	assert.EqualValues(t, 0, m.Retention.UsageMs)

	f.thread("T", "", "", "work")
	f.usage("T", 1234, "m", 1)
	for k, v := range map[string]string{"facts_version": "1", "backfill_total": "10", "backfill_done": "3"} {
		require.NoError(t, f.st.Q.SetStatsState(f.ctx, db.SetStatsStateParams{Key: k, Value: v}))
	}
	m, err = s.Meta(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, m.FactsVersion)
	require.NotNil(t, m.Backfill)
	assert.EqualValues(t, 3, m.Backfill.Done)
	assert.EqualValues(t, 10, m.Backfill.Total)
	assert.EqualValues(t, 1234, *m.EarliestTS)
}
