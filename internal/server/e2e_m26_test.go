package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/research"
)

// M26 端到端（步骤 5.1）：带真实协调宿主（沙箱里的 fakeomp）的栈上验证后台研究的公网跟进与受限的一段、自动挖掘、
// 工作线程里的 `oryn follow add`、每日花费上限与研究员对话的隐藏。
//
// fakeomp 的回复以 `You said: <提示词原文>` 结尾，所以把想要的报告 ```json 块放进话题的 question 或对话消息里，它就是
// 回复里最后一个围栏块。研究任务的提示词头行 `[Oryn research task · …]` 含 "ask"（task），fakeomp 会先发一个 select
// 并等作答：answerResearcherAsks 替主人答掉。协调宿主里 agent 的环境从零构造、带不进 FAKEOMP_DUMP_DIR：转储写在
// 协调 orb 的 HOME 下的 .fakeomp-dump/<线程 id>.json（每次启动覆盖，留下的是最近一次）。

// m26MainModeRetry 与 coord 的 mainModeRetry 相同：等主线换成目标变体时的重查间隔。
const m26MainModeRetry = 5 * time.Second

// m26Dump 是 fakeomp 转储里本文件用到的字段。
type m26Dump struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

// argValue 返回 argv 里 flag 之后的那个值；没有该 flag 时 ok 为假。
func (d m26Dump) argValue(flag string) (string, bool) {
	i := slices.Index(d.Args, flag)
	if i < 0 || i+1 >= len(d.Args) {
		return "", false
	}
	return d.Args[i+1], true
}

// overlay 读 --config 指向的覆盖层文件。
func (d m26Dump) overlay(t *testing.T) string {
	t.Helper()
	p, ok := d.argValue("--config")
	require.True(t, ok, "argv 没有 --config：%v", d.Args)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

// m26Coord 是一套 M26 栈：主线 id 与协调 orb 的转储目录。
type m26Coord struct {
	s      *stack
	mainID string
	dumps  string
}

// newM26Stack 起带真实协调宿主的栈，取主线并在协调 orb 的 HOME 下建转储目录，替研究员答掉 fakeomp 的 select。
func newM26Stack(t *testing.T) *m26Coord {
	t.Helper()
	s, coordDir := newM9Stack(t)
	var main apigen.ThreadEnvelope
	s.rest("GET", "/api/v1/assistant/main", nil, &main, 200)
	row := s.threadRow(main.Thread.ID)
	require.NotNil(t, row.OrbID)
	home := filepath.Join(coordDir, "homes", *row.OrbID)
	// HOME 由协调宿主建（属主是降权账号）：等它出现再在里面建转储目录，免得抢先建出 root 的父目录
	require.Eventually(t, func() bool {
		fi, err := os.Stat(home)
		return err == nil && fi.IsDir()
	}, 60*time.Second, 50*time.Millisecond, "协调 orb 的 HOME 出现")
	dumps := filepath.Join(home, ".fakeomp-dump")
	require.NoError(t, os.Mkdir(dumps, 0o777))
	require.NoError(t, os.Chmod(dumps, 0o777))
	s.answerResearcherAsks()
	return &m26Coord{s: s, mainID: main.Thread.ID, dumps: dumps}
}

// answerResearcherAsks 在测试期间不断替研究员作答 fakeomp 的 select（每代 agent 的请求 id 从 ui-1 起）。没有待答请求时
// UIResponse 直接回 ui_resolved，不改变任何状态。
func (s *stack) answerResearcherAsks() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
			for _, role := range []string{research.RoleWeb, research.RolePrivate} {
				id, ok, err := s.srv.Threads.ResearchThread(ctx, role)
				if err != nil || !ok {
					continue
				}
				for _, ui := range []string{"ui-1", "ui-2", "ui-3"} {
					frame, _ := json.Marshal(map[string]string{"type": "extension_ui_response", "id": ui, "value": "alpha"})
					_ = s.srv.Threads.UIResponse(ctx, id, frame)
				}
			}
		}
	}()
	s.t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

// dump 等线程 threadID 的转储出现并读出（写入不是原子的：解析失败时重读）。
func (c *m26Coord) dump(threadID string) m26Dump {
	c.s.t.Helper()
	var d m26Dump
	require.Eventually(c.s.t, func() bool {
		b, err := os.ReadFile(filepath.Join(c.dumps, threadID+".json"))
		return err == nil && json.Unmarshal(b, &d) == nil && len(d.Args) > 0
	}, 60*time.Second, 50*time.Millisecond, "线程 %s 的 fakeomp 转储", threadID)
	return d
}

// dropDump 删掉线程的转储：之后出现的就是下一次启动的。
func (c *m26Coord) dropDump(threadID string) {
	c.s.t.Helper()
	err := os.Remove(filepath.Join(c.dumps, threadID+".json"))
	if !errors.Is(err, os.ErrNotExist) {
		require.NoError(c.s.t, err)
	}
}

// putResearch 在现有研究设置上改几项后整体 PUT。
func (s *stack) putResearch(mut func(*apigen.ResearchSettings)) apigen.ResearchSettingsEnvelope {
	s.t.Helper()
	var env apigen.ResearchSettingsEnvelope
	s.rest("GET", "/api/v1/assistant/research/settings", nil, &env, 200)
	set := env.Settings
	if set.McpTools == nil {
		set.McpTools = map[string][]string{}
	}
	mut(&set)
	s.rest("PUT", "/api/v1/assistant/research/settings", set, &env, 200)
	return env
}

// researchRuns 列出研究运行（新的在前）。
func (s *stack) researchRuns() []apigen.ResearchRun {
	s.t.Helper()
	var list apigen.ResearchRunList
	s.rest("GET", "/api/v1/assistant/research/runs?limit=200", nil, &list, 200)
	return list.Runs
}

// waitResearchRun 等出现一条 pipeline 管线的运行结束；失败时带上错误直接判负。
func (s *stack) waitResearchRun(pipeline apigen.ResearchRunPipeline, timeout time.Duration) apigen.ResearchRun {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, r := range s.researchRuns() {
			if r.Pipeline != pipeline {
				continue
			}
			switch r.Status {
			case apigen.ResearchRunCompleted:
				return r
			case apigen.ResearchRunFailed:
				s.t.Fatalf("研究运行失败：%+v（error=%v）", r, deref(r.Error))
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("没有等到 %s 运行结束：%+v", pipeline, s.researchRuns())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// researchTopics 列出全部状态的话题。
func (s *stack) researchTopics() []apigen.ResearchTopic {
	s.t.Helper()
	var list apigen.ResearchTopicList
	s.rest("GET", "/api/v1/assistant/research/topics?state=all", nil, &list, 200)
	return list.Topics
}

// researcherID 返回角色 role 的研究员对话 id（必须已存在）。
func (s *stack) researcherID(role string) string {
	s.t.Helper()
	id, ok, err := s.srv.Threads.ResearchThread(context.Background(), role)
	require.NoError(s.t, err)
	require.True(s.t, ok, "研究员对话 %s 不存在", role)
	return id
}

// 公网跟进：立即检查一个话题 → 公网研究员以 `--tools web_search,read`、只读公网的覆盖层、有 PI_PROXY 没有 MCP 凭据
// 启动；报告为 update → 一条受限收件 → 主线换成 main_restricted（`--tools read`、没有 PI_PROXY）后收到研究发现；
// 那一回合结束后主人再发一条消息，主线恢复完整工具表。
func TestE2EM26Web(t *testing.T) {
	clk := useSchedClock(t)
	c := newM26Stack(t)
	s, ctx := c.s, context.Background()

	s.putResearch(func(set *apigen.ResearchSettings) {
		set.Enabled, set.Web, set.Mine, set.Internal, set.Mcp = true, true, false, false, false
	})
	question := "When does Widget 2 ship?\n```json\n" +
		`{"status":"update","summary":"S1","sources":[{"url":"https://example.com/a","title":"A"}]}` + "\n```"
	var topic apigen.ResearchTopic
	s.rest("POST", "/api/v1/assistant/research/topics", map[string]any{"kind": "question", "title": "Widget 2 release", "question": question}, &topic, 201)
	c.dropDump(c.mainID)
	s.rest("POST", "/api/v1/assistant/research/topics/"+topic.ID+"/run", nil, &topic, 200)

	run := s.waitResearchRun(apigen.ResearchPipelineWeb, 90*time.Second)
	assert.Equal(t, "findings:1", run.Outcome)
	require.NotNil(t, run.TopicID)
	assert.Equal(t, topic.ID, *run.TopicID)
	s.rest("GET", "/api/v1/assistant/research/topics/"+topic.ID, nil, &topic, 200)
	assert.Equal(t, "S1", topic.LastReport)
	assert.Equal(t, 1, topic.RunCount)

	// 公网研究员的启动参数与环境
	web := c.dump(s.researcherID(research.RoleWeb))
	tools, ok := web.argValue("--tools")
	assert.True(t, ok)
	assert.Equal(t, "web_search,read", tools)
	ov := web.overlay(t)
	assert.Contains(t, ov, "remoteOnly: true")
	assert.Contains(t, ov, "fileMentions: false")
	assert.Contains(t, web.Env, "PI_PROXY")
	assert.NotContains(t, web.Env, "ORYN_MCP_TOKEN")
	assert.NotContains(t, web.Env, "ORYN_MCP_BASE")

	// 受限收件
	var kind string
	var restricted int64
	require.NoError(t, s.srv.Store.DB.QueryRowContext(ctx,
		`SELECT kind, restricted FROM assistant_inbox WHERE source_id = ?`, run.ID).Scan(&kind, &restricted))
	assert.Equal(t, "research", kind)
	assert.Equal(t, int64(1), restricted)

	// 合并窗口到点后主线先被换成受限变体，换好之后的重查才投递
	header := "[Oryn research findings · automatic] 1 finding(s)."
	clk.Advance(15 * time.Second)
	deadline := time.Now().Add(90 * time.Second)
	for !entriesContain(t, s, c.mainID, header) {
		if time.Now().After(deadline) {
			t.Fatalf("主线没有收到研究发现")
		}
		clk.Advance(m26MainModeRetry)
		time.Sleep(200 * time.Millisecond)
	}
	assert.True(t, entriesContain(t, s, c.mainID, `\"summary\":\"S1\"`), "研究发现带摘要")
	restrictedDump := c.dump(c.mainID)
	tools, ok = restrictedDump.argValue("--tools")
	assert.True(t, ok)
	assert.Equal(t, "read", tools, "受限的一段只有 read")
	assert.NotContains(t, restrictedDump.Env, "PI_PROXY")
	assert.NotContains(t, restrictedDump.Env, "ORYN_MCP_TOKEN")
	s.settledReply(c.mainID)

	// 回合结束后受限变体的 agent 静止后停掉；主人的下一条消息启动完整工具表的主线
	require.Eventually(t, func() bool {
		v, known := s.srv.Threads.AgentVariant(ctx, c.mainID)
		return !known || v != research.VariantMainRestricted
	}, 60*time.Second, 50*time.Millisecond, "受限的主线 agent 停掉")
	c.dropDump(c.mainID)
	s.prompt(c.mainID, "hello again")
	full := c.dump(c.mainID)
	tools, _ = full.argValue("--tools")
	assert.Contains(t, strings.Split(tools, ","), "web_search", "主线恢复完整工具表：%v", full.Args)
	assert.Contains(t, full.Env, "PI_PROXY")
}

// 自动挖掘：助手对话静止 30 分钟后私有研究员（不带 --tools、没有 PI_PROXY）从摘录里挖出话题，origin = mined。
func TestE2EM26Mine(t *testing.T) {
	clk := useSchedClock(t)
	c := newM26Stack(t)
	s := c.s

	s.putResearch(func(set *apigen.ResearchSettings) {
		set.Enabled, set.Web, set.Mine, set.Internal, set.Mcp = true, false, true, false, false
	})
	var conv apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &conv, 201)
	s.prompt(conv.Thread.ID, "Keep an eye on this for me.\n```json\n"+
		`{"topics":[{"kind":"question","title":"T-mined","question":"Q","baseline":"B"}]}`+"\n```")

	clk.Advance(40 * time.Minute)
	var mined *apigen.ResearchTopic
	deadline := time.Now().Add(90 * time.Second)
	for mined == nil {
		for _, tp := range s.researchTopics() {
			if tp.Title == "T-mined" {
				mined = &tp
			}
		}
		if mined != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有挖出话题；运行：%+v", s.researchRuns())
		}
		time.Sleep(100 * time.Millisecond)
	}
	assert.Equal(t, apigen.ResearchOriginMined, mined.Origin)
	require.NotNil(t, mined.SourceThreadID)
	assert.Equal(t, conv.Thread.ID, *mined.SourceThreadID)
	assert.Equal(t, "Q", mined.Question)
	assert.Equal(t, "B", mined.Baseline)
	run := s.waitResearchRun(apigen.ResearchPipelineMine, 30*time.Second)
	assert.Equal(t, conv.Thread.ID, run.Subject)

	priv := c.dump(s.researcherID(research.RolePrivate))
	_, hasTools := priv.argValue("--tools")
	assert.False(t, hasTools, "私有研究员不带 --tools：%v", priv.Args)
	assert.Contains(t, priv.Args, "--no-tools")
	assert.Contains(t, priv.overlay(t), "fileMentions: false")
	assert.NotContains(t, priv.Env, "PI_PROXY")
	assert.NotContains(t, priv.Env, "ORYN_MCP_TOKEN", "挖掘运行没有 MCP 凭据")
}

// followAddCLI 以工作线程在 orb 里的身份跑真实的 `oryn follow add`，返回退出码、标准输出与标准错误。
func followAddCLI(t *testing.T, home, threadID string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "oryn"), append([]string{"follow", "add"}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "ORYN_ORB=1", "ORYN_THREAD_ID=" + threadID}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), stdout.String(), stderr.String()
	}
	require.NoError(t, err)
	return 0, "", ""
}

// 工作线程里的 `oryn follow add`：--quote 逐字出现在主人消息里时登记 origin = thread 的话题，否则 forbidden。
func TestE2EM26Follow(t *testing.T) {
	s := newStack(t, m9Idle)
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "请跟进这个上游修复", "executor": map[string]any{"kind": "new_orb", "provider": "local"}}, &created, 201)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })
	s.waitEntry(th.ID, "请跟进这个上游修复", 30*time.Second)
	home := s.drv.HomeDir(th.OrbID)

	code, out, errOut := followAddCLI(t, home, th.ID, "--title", "T", "--question", "Q", "--quote", "跟进这个上游修复")
	require.Equal(t, 0, code, "stdout=%s stderr=%s", out, errOut)
	id := strings.TrimSpace(out)
	require.NotEmpty(t, id)
	var topic apigen.ResearchTopic
	s.rest("GET", "/api/v1/assistant/research/topics/"+url.PathEscape(id), nil, &topic, 200)
	assert.Equal(t, apigen.ResearchTopicOrigin("thread"), topic.Origin)
	require.NotNil(t, topic.SourceThreadID)
	assert.Equal(t, th.ID, *topic.SourceThreadID)
	assert.Equal(t, "T", topic.Title)
	assert.Equal(t, "Q", topic.Question)

	code, out, errOut = followAddCLI(t, home, th.ID, "--title", "T2", "--question", "Q2", "--quote", "主人从没说过这句话")
	assert.NotZero(t, code, "stdout=%s", out)
	assert.Contains(t, errOut, "forbidden")
	for _, tp := range s.researchTopics() {
		assert.NotEqual(t, "T2", tp.Title, "原话不在线程里时不登记")
	}
}

// 每日上限：研究员对话当天已花 1 美元、上限 0.1 美元 → 不发起运行，发一条 budget 通知（当天只发一次）。
func TestE2EM26Budget(t *testing.T) {
	clk := useSchedClock(t)
	s, _ := newM9Stack(t)
	ctx := context.Background()

	s.putResearch(func(set *apigen.ResearchSettings) {
		set.Enabled, set.Web, set.Mine, set.Internal, set.Mcp = true, true, false, false, false
		set.DailyUsd = 0.1
	})
	webID, err := s.srv.Coord.EnsureResearcher(ctx, research.RoleWeb)
	require.NoError(t, err)
	_, err = s.srv.Store.DB.ExecContext(ctx,
		`INSERT INTO usage_records (thread_id, entry_seq, ts, cost_usd) VALUES (?, 999999, ?, 1)`, webID, clk.Now().UnixMilli())
	require.NoError(t, err)

	var topic apigen.ResearchTopic
	s.rest("POST", "/api/v1/assistant/research/topics", map[string]any{"kind": "question", "title": "Budget probe", "question": "Q"}, &topic, 201)
	s.rest("POST", "/api/v1/assistant/research/topics/"+topic.ID+"/run", nil, &topic, 200)
	for range 2 {
		clk.Advance(time.Minute)
		time.Sleep(300 * time.Millisecond)
	}
	s.waitNotification(30*time.Second, func(n apigen.Notification) bool { return n.Kind == apigen.NotificationKindBudget })
	clk.Advance(time.Minute)
	time.Sleep(300 * time.Millisecond)

	budget := 0
	for _, n := range s.notifications() {
		if n.Kind == apigen.NotificationKindBudget {
			budget++
		}
	}
	assert.Equal(t, 1, budget, "每天至多一条预算通知")
	assert.Empty(t, s.researchRuns(), "到了上限不发起运行")
	var env apigen.ResearchSettingsEnvelope
	s.rest("GET", "/api/v1/assistant/research/settings", nil, &env, 200)
	assert.True(t, env.Capped)
	assert.GreaterOrEqual(t, env.SpentTodayUsd, float32(1))
}

// 研究员对话是隐藏的：不在线程列表与全文搜索里，回合结束不产生通知，不会被新建的助手对话认领。
func TestE2EM26Hidden(t *testing.T) {
	useM19Notify(t)
	s, _ := newM9Stack(t)
	ctx := context.Background()

	webID, err := s.srv.Coord.EnsureResearcher(ctx, research.RoleWeb)
	require.NoError(t, err)
	privID, err := s.srv.Coord.EnsureResearcher(ctx, research.RolePrivate)
	require.NoError(t, err)
	hidden := []string{webID, privID}

	// 研究员的一个回合（标签不是进行中的运行：研究服务直接丢弃它的回合终态）
	require.NoError(t, s.srv.Coord.DeliverResearch(ctx, research.RoleWeb, research.RunTagPrefix+"e2ehidden", "zqhiddenmark hello"))
	require.Eventually(t, func() bool {
		reply, final := lastAssistantMsg(t, s, webID)
		return final && strings.Contains(reply, "You said: zqhiddenmark") && s.threadRow(webID).State == "idle"
	}, 60*time.Second, 50*time.Millisecond, "研究员回合结束")

	// 对照：普通助手对话的消息搜得到
	var conv apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &conv, 201)
	assert.NotContains(t, hidden, conv.Thread.ID, "新建的助手对话不认领研究员对话")
	s.prompt(conv.Thread.ID, "zqvisiblemark hello")
	var res apigen.SearchResults
	require.Eventually(t, func() bool {
		s.rest("GET", "/api/v1/search?q=zqvisiblemark", nil, &res, 200)
		return len(res.Hits) > 0
	}, 30*time.Second, 100*time.Millisecond, "普通对话可搜索")
	s.rest("GET", "/api/v1/search?q=zqhiddenmark", nil, &res, 200)
	assert.Empty(t, res.Hits, "研究员对话的内容搜不到")

	var list apigen.ThreadList
	s.rest("GET", "/api/v1/threads?kind=all", nil, &list, 200)
	listed := make([]string, 0, len(list.Threads))
	for _, th := range list.Threads {
		listed = append(listed, th.ID)
	}
	assert.Contains(t, listed, conv.Thread.ID)
	for _, id := range hidden {
		assert.NotContains(t, listed, id, "研究员对话不在线程列表里")
	}

	time.Sleep(500 * time.Millisecond) // done 去抖 50ms：足够让本不该发的通知发出来
	for _, n := range s.notifications() {
		if n.ThreadID != nil {
			assert.NotContains(t, hidden, *n.ThreadID, "研究员对话不产生通知：%+v", n)
		}
	}

	var another apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &another, 201)
	assert.NotContains(t, hidden, another.Thread.ID)
	for _, id := range hidden {
		row := s.threadRow(id)
		assert.Equal(t, int64(1), row.Hidden)
		require.NotNil(t, row.Research)
	}
}
