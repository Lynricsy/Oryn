package research

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeHost 是测试用的 research.Host。
type fakeHost struct {
	mu         sync.Mutex
	warm       map[string]bool // 角色 → agent 还在（ResearcherCold 为假）
	delivered  []delivery
	stops      []string
	findings   []Finding
	threads    []string
	excerpts   map[string]Excerpt
	refreshes  int
	models     []string
	deliverErr error
	// prepared 记下每次 PrepareMCP 的服务器名单；prepareErr 非 nil 时 PrepareMCP 失败。
	prepared   [][]string
	prepareErr error
}

type delivery struct{ role, runID, prompt string }

func (h *fakeHost) ResearcherCold(_ context.Context, role string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.warm[role]
}

func (h *fakeHost) DeliverResearch(_ context.Context, role, runID, prompt string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.deliverErr != nil {
		return h.deliverErr
	}
	h.delivered = append(h.delivered, delivery{role, runID, prompt})
	return nil
}

func (h *fakeHost) PrepareMCP(_ context.Context, _ string, servers []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prepared = append(h.prepared, servers)
	return h.prepareErr
}

func (h *fakeHost) StopResearcher(_ context.Context, role string) {
	h.mu.Lock()
	h.stops = append(h.stops, role)
	h.mu.Unlock()
}

func (h *fakeHost) LastReply(context.Context, string) (string, error) { return "", nil }

func (h *fakeHost) ResearcherThreads(context.Context) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.threads...), nil
}

func (h *fakeHost) Excerpt(_ context.Context, threadID string, _ int64) (Excerpt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.excerpts[threadID], nil
}

func (h *fakeHost) PostFinding(_ context.Context, f Finding) {
	h.mu.Lock()
	h.findings = append(h.findings, f)
	h.mu.Unlock()
}

func (h *fakeHost) RefreshPolicy(context.Context) error {
	h.mu.Lock()
	h.refreshes++
	h.mu.Unlock()
	return nil
}

func (h *fakeHost) SetResearcherModel(_ context.Context, model string) error {
	h.mu.Lock()
	h.models = append(h.models, model)
	h.mu.Unlock()
	return nil
}

func (h *fakeHost) lastDelivery(t *testing.T) delivery {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	require.NotEmpty(t, h.delivered)
	return h.delivered[len(h.delivered)-1]
}

func (h *fakeHost) deliveries() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.delivered)
}

type fakeNotifier struct {
	mu    sync.Mutex
	posts []string
}

func (n *fakeNotifier) PostSystem(_ context.Context, kind, title, _, _ string) error {
	n.mu.Lock()
	n.posts = append(n.posts, kind+": "+title)
	n.mu.Unlock()
	return nil
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	s     *Service
	host  *fakeHost
	note  *fakeNotifier
	clk   *testClock
	webOn bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{t: t, ctx: ctx, st: st, host: &fakeHost{warm: map[string]bool{}, excerpts: map[string]Excerpt{}}, note: &fakeNotifier{},
		clk: &testClock{t: time.Date(2026, 11, 22, 10, 0, 0, 0, time.Local)}, webOn: true}
	f.s = New(Config{
		Now: f.clk.Now, AfterFunc: func(time.Duration, func()) func() { return func() {} },
		Store: st, Host: f.host, Notifier: f.note, WebAccess: func(context.Context) bool { return f.webOn },
	})
	return f
}

// enable 打开总开关（其余取缺省）。
func (f *fixture) enable(mut ...func(*apigen.ResearchSettings)) {
	f.t.Helper()
	in := apigen.ResearchSettings{Enabled: true, Web: true, Mine: true, Internal: true, DailyUsd: 20, McpTools: map[string][]string{}}
	for _, m := range mut {
		m(&in)
	}
	_, err := f.s.PutSettings(f.ctx, in)
	require.NoError(f.t, err)
}

func (f *fixture) topic(title, question string) apigen.ResearchTopic {
	f.t.Helper()
	tp, err := f.s.CreateTopic(f.ctx, TopicInput{Title: title, Question: question, Baseline: "BASELINE-SECRET"})
	require.NoError(f.t, err)
	return tp
}

// due 让话题立即到期。
func (f *fixture) due(id string) {
	f.t.Helper()
	_, err := f.s.RunTopicNow(f.ctx, id)
	require.NoError(f.t, err)
}

// reply 模拟研究员回合完成：回复以给定 json 块结尾。
func (f *fixture) reply(runID, jsonBody string) {
	f.s.TurnEnded(threads.TurnEvent{Tag: runID, Status: threads.TurnCompleted, HasText: true,
		Text: "Here is what I found.\n```json\n" + jsonBody + "\n```"})
	f.s.step(f.ctx)
}

func (f *fixture) run(id string) (status, outcome, errMsg string) {
	f.t.Helper()
	r, err := f.st.Q.GetResearchRun(f.ctx, id)
	require.NoError(f.t, err)
	return r.Status, r.Outcome, deref(r.Error)
}

func TestSettingsDefaults(t *testing.T) {
	f := newFixture(t)
	env, err := f.s.Settings(f.ctx)
	require.NoError(t, err)
	assert.False(t, env.Settings.Enabled)
	assert.True(t, env.Settings.Web)
	assert.False(t, env.Settings.Mcp)
	assert.InDelta(t, 20, env.Settings.DailyUsd, 0.001)
	assert.Equal(t, map[string][]string{}, env.Settings.McpTools)
	assert.Zero(t, env.SpentTodayUsd)

	_, err = f.s.PutSettings(f.ctx, apigen.ResearchSettings{DailyUsd: 0.05, McpTools: map[string][]string{}})
	assert.ErrorAs(t, err, new(*Error))
	_, err = f.s.PutSettings(f.ctx, apigen.ResearchSettings{DailyUsd: 1, Preferences: strings.Repeat("字", prefsMax+1)})
	assert.ErrorAs(t, err, new(*Error))

	f.enable(func(in *apigen.ResearchSettings) {
		in.Model = &apigen.ModelRef{Provider: "p", ID: "m"}
		in.McpTools = map[string][]string{"mail": {"list", "list", "read"}, "empty": {}}
	})
	env, err = f.s.Settings(f.ctx)
	require.NoError(t, err)
	assert.True(t, env.Settings.Enabled)
	assert.Equal(t, map[string][]string{"mail": {"list", "read"}}, env.Settings.McpTools, "去重、去空")
	require.NotNil(t, env.Settings.Model)
	assert.Equal(t, []string{"p/m"}, f.host.models)
	assert.Equal(t, 1, f.host.refreshes)
	row, err := f.st.Q.GetResearchSettings(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, row.MineSince, "打开总开关时记下挖掘起点")
}

func TestCreateTopicValidation(t *testing.T) {
	f := newFixture(t)
	tp := f.topic("Node 24 LTS date", "When does Node 24 become LTS?")
	assert.Equal(t, apigen.ResearchTopicActive, tp.State)
	require.NotNil(t, tp.NextRunAt)
	assert.Equal(t, f.clk.Now().Add(webFirstDelay).UnixMilli(), *tp.NextRunAt)
	require.NotNil(t, tp.ExpiresAt, "问题跟进会结束")

	_, err := f.s.CreateTopic(f.ctx, TopicInput{Title: "  node 24   LTS DATE ", Question: "q"})
	assert.ErrorContains(t, err, "already exists", "规范化后重名")
	require.NoError(t, f.s.DismissTopic(f.ctx, tp.ID))
	_, err = f.s.CreateTopic(f.ctx, TopicInput{Title: "Node 24 LTS date", Question: "q"})
	assert.ErrorContains(t, err, "already exists", "已删除的标题也不再登记")

	_, err = f.s.CreateTopic(f.ctx, TopicInput{Title: strings.Repeat("长", titleMax+1), Question: "q"})
	assert.ErrorAs(t, err, new(*Error))
	_, err = f.s.CreateTopic(f.ctx, TopicInput{Title: "t", Question: strings.Repeat("q", questionMax+1)})
	assert.ErrorAs(t, err, new(*Error))
	for _, q := range []string{"see https://x.test/a", "www.example.com news", "ask bob@example.com"} {
		_, err = f.s.CreateTopic(f.ctx, TopicInput{Title: "mined " + q, Question: q, Origin: originMined})
		assert.ErrorContains(t, err, "links", q)
	}
	_, err = f.s.CreateTopic(f.ctx, TopicInput{Title: "manual link", Question: "see https://x.test/a"})
	assert.NoError(t, err, "手动建的可以带网址")

	interest, err := f.s.CreateTopic(f.ctx, TopicInput{Kind: kindInterest, Title: "Rust releases", Question: "q"})
	require.NoError(t, err)
	assert.Nil(t, interest.ExpiresAt, "长期关注不结束")

	for i := 0; ; i++ {
		_, err = f.s.CreateTopic(f.ctx, TopicInput{Title: "bulk " + string(rune('a'+i%26)) + string(rune('a'+i/26)), Question: "q"})
		if err != nil {
			break
		}
	}
	assert.ErrorContains(t, err, "active topics")
	n, err := f.st.Q.CountActiveResearchTopics(f.ctx)
	require.NoError(t, err)
	assert.EqualValues(t, maxActiveTopics, n)
}

func TestEngineDisabledDoesNothing(t *testing.T) {
	f := newFixture(t)
	tp := f.topic("t", "q")
	f.due(tp.ID)
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries())
}

func TestEngineBudgetCap(t *testing.T) {
	f := newFixture(t)
	_, err := f.st.DB.ExecContext(f.ctx, `INSERT INTO threads (id, title_source, state, state_changed_at, session_dir, created_at, updated_at, kind, hidden, research)
		VALUES ('T-r', 'auto', 'idle', 0, 'd', 1, 1, 'assistant', 1, 'web')`)
	require.NoError(t, err)
	_, err = f.st.DB.ExecContext(f.ctx, `INSERT INTO usage_records (thread_id, entry_seq, ts, cost_usd) VALUES ('T-r', 1, ?, 1.0)`, f.clk.Now().UnixMilli())
	require.NoError(t, err)
	f.host.threads = []string{"T-r"}
	f.enable(func(in *apigen.ResearchSettings) { in.DailyUsd = 0.5 })
	tp := f.topic("t", "q")
	f.due(tp.ID)
	f.s.step(f.ctx)
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries())
	assert.Equal(t, []string{"budget: Background research paused for today"}, f.note.posts, "每天至多一条")
	env, err := f.s.Settings(f.ctx)
	require.NoError(t, err)
	assert.True(t, env.Capped)
	assert.InDelta(t, 1, env.SpentTodayUsd, 0.001)

	// 第二天花费归零
	f.clk.Add(24 * time.Hour)
	f.due(tp.ID)
	f.s.step(f.ctx)
	assert.Equal(t, 1, f.host.deliveries())
}

func TestEngineOneRunPerRole(t *testing.T) {
	f := newFixture(t)
	f.enable()
	a, b := f.topic("a", "qa"), f.topic("b", "qb")
	f.due(a.ID)
	f.due(b.ID)
	f.s.step(f.ctx)
	f.s.step(f.ctx)
	assert.Equal(t, 1, f.host.deliveries(), "同一角色同时至多一条运行")
	assert.Equal(t, PipelineWeb, f.s.CurrentPipeline(RoleWeb))
	first := f.host.lastDelivery(t)
	f.reply(first.runID, `{"status":"nothing","summary":""}`)
	assert.Equal(t, 2, f.host.deliveries(), "上一条结束后的同一轮发起下一条")
	assert.NotEqual(t, first.runID, f.host.lastDelivery(t).runID)
}

func TestEngineWaitsForColdResearcher(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	f.due(tp.ID)
	f.host.warm[RoleWeb] = true
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries())
	assert.Contains(t, f.host.stops, RoleWeb, "先让上一代停下")
	f.host.mu.Lock()
	f.host.warm[RoleWeb] = false
	f.host.mu.Unlock()
	f.s.step(f.ctx)
	assert.Equal(t, 1, f.host.deliveries())
}

func TestEngineIgnoresStaleTurn(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	f.due(tp.ID)
	f.s.step(f.ctx)
	d := f.host.lastDelivery(t)
	f.reply("rsr_stale", `{"status":"update","summary":"S"}`)
	assert.Equal(t, PipelineWeb, f.s.CurrentPipeline(RoleWeb), "迟到的旧回合不结束当前运行")
	status, _, _ := f.run(d.runID)
	assert.Equal(t, "running", status)
	assert.Empty(t, f.host.findings)
}

func TestEngineTimeout(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	f.due(tp.ID)
	f.s.step(f.ctx)
	d := f.host.lastDelivery(t)
	f.clk.Add(runTimeout + time.Minute)
	f.s.step(f.ctx)
	status, _, errMsg := f.run(d.runID)
	assert.Equal(t, "failed", status)
	assert.Equal(t, "timeout", errMsg)
	assert.Contains(t, f.host.stops, RoleWeb)
	got, err := f.s.GetTopic(f.ctx, tp.ID)
	require.NoError(t, err)
	require.NotNil(t, got.NextRunAt)
	assert.Equal(t, f.clk.Now().Add(webBaseInterval).UnixMilli(), *got.NextRunAt, "失败按原间隔再排")
}

func TestEngineUnparseableReport(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	f.due(tp.ID)
	f.s.step(f.ctx)
	d := f.host.lastDelivery(t)
	f.s.TurnEnded(threads.TurnEvent{Tag: d.runID, Status: threads.TurnCompleted, HasText: true, Text: "no block here"})
	f.s.step(f.ctx)
	status, _, errMsg := f.run(d.runID)
	assert.Equal(t, "failed", status)
	assert.Equal(t, "unparseable report", errMsg)
}

func TestRecoverMarksInterrupted(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	f.due(tp.ID)
	f.s.step(f.ctx)
	d := f.host.lastDelivery(t)
	s2 := New(f.s.cfg)
	s2.recover(f.ctx)
	status, _, errMsg := f.run(d.runID)
	assert.Equal(t, "failed", status)
	assert.Equal(t, "interrupted", errMsg)
}

func TestLastJSONBlock(t *testing.T) {
	raw, ok := lastJSONBlock("a\n```json\n{\"a\":1}\n```\nmid\n```json\n{\"b\":2}\n```\n")
	require.True(t, ok)
	assert.JSONEq(t, `{"b":2}`, string(raw))
	_, ok = lastJSONBlock("```json\n[1,2]\n```")
	assert.False(t, ok, "必须是对象")
	_, ok = lastJSONBlock("```json\n{broken\n```")
	assert.False(t, ok)
	_, ok = lastJSONBlock("plain")
	assert.False(t, ok)
}
