package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// M19 端到端（步骤 5.1）：带真实协调宿主（沙箱里的 fakeomp）的栈上验证确认卡、主线（交给助手与沉默）、
// 助手操作 Runner 线程、tail 订阅、助手的电脑（fakeomp 兼作 orb 里的工具执行器）。

// useM19Notify 把通知去抖调短（须在组装栈之前调用）：done 通知有没有发出能在一秒内判定。
func useM19Notify(t *testing.T) {
	t.Helper()
	prevDone, prevInput := notifyDoneDelay, notifyInputDelay
	notifyDoneDelay, notifyInputDelay = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { notifyDoneDelay, notifyInputDelay = prevDone, prevInput })
}

// send 以主人身份向线程发一条消息，不等回合结束。
func (s *stack) send(threadID, text string) {
	s.t.Helper()
	frame, _ := json.Marshal(map[string]string{"type": "prompt", "message": text})
	require.NoError(s.t, s.srv.Threads.Command(context.Background(), threadID, frame, func(json.RawMessage) {}))
}

// status 发一个 REST 请求并只返回状态码。
func (s *stack) status(method, path string) int {
	s.t.Helper()
	req, _ := http.NewRequest(method, s.http.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(s.t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// notifications 返回最近的通知（新的在前）。
func (s *stack) notifications() []apigen.Notification {
	s.t.Helper()
	var list apigen.NotificationList
	s.rest("GET", "/api/v1/notifications?limit=100", nil, &list, 200)
	return list.Notifications
}

// waitNotification 等出现一条满足 cond 的通知。
func (s *stack) waitNotification(timeout time.Duration, cond func(apigen.Notification) bool) apigen.Notification {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, n := range s.notifications() {
			if cond(n) {
				return n
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("没有等到期望的通知：%+v", s.notifications())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// countNotifications 数线程 threadID 的 kind 类通知。
func (s *stack) countNotifications(threadID string, kind apigen.NotificationKind) int {
	s.t.Helper()
	n := 0
	for _, x := range s.notifications() {
		if x.Kind == kind && x.ThreadID != nil && *x.ThreadID == threadID {
			n++
		}
	}
	return n
}

func (s *stack) threadRow(id string) db.Thread {
	s.t.Helper()
	row, err := s.srv.Store.Q.GetThread(context.Background(), id)
	require.NoError(s.t, err)
	return row
}

// 确认卡：助手请求删除线程 → 对话 awaiting_input、出现可一触作答的 input 通知 → 主人经 REST 作答「是」→ 线程被删，
// 对话收到 `[Oryn confirmation · automatic] delete_thread … approved and done.`，审计 origin = owner。
func TestE2EM19Confirm(t *testing.T) {
	useM19Notify(t)
	s, _ := newM9Stack(t)
	var target apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "work", "executor": map[string]any{"kind": "new_orb"}}, &target, 201)
	s.idle(target.Thread.ID)
	var asst apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &asst, 201)
	conv := asst.Thread.ID

	s.send(conv, `hosttool delete_thread {"threadId":"`+target.Thread.ID+`"}`)
	s.waitThread(conv, 60*time.Second, func(v threadView) bool {
		reply, final := lastAssistantMsg(t, s, conv)
		return v.State == string(apigen.ThreadStateAwaitingInput) && final && strings.HasPrefix(reply, "host:delete_thread:ok:")
	})
	reply := lastAssistant(t, s, conv)
	assert.Contains(t, reply, `"confirmationId":"cfm_`, "工具结果只是等主人确认")
	// 作答之前线程还在
	assert.Equal(t, http.StatusOK, s.status("GET", "/api/v1/threads/"+target.Thread.ID))

	n := s.waitNotification(30*time.Second, func(n apigen.Notification) bool {
		return n.Kind == "input" && n.ThreadID != nil && *n.ThreadID == conv
	})
	require.NotNil(t, n.QuickAnswer, "确认卡可在通知上一触作答")
	require.Len(t, n.QuickAnswer.Options, 2)
	var ans apigen.NotificationAnswerResult
	s.rest("POST", "/api/v1/notifications/"+n.ID+"/answer", map[string]any{"option": 0}, &ans, 200)
	assert.Equal(t, conv, ans.ThreadID)
	// 同一张卡不能再答一次
	s.rest("POST", "/api/v1/notifications/"+n.ID+"/answer", map[string]any{"option": 0}, nil, 409)

	deadline := time.Now().Add(30 * time.Second)
	for s.status("GET", "/api/v1/threads/"+target.Thread.ID) != http.StatusNotFound {
		if time.Now().After(deadline) {
			t.Fatal("确认后线程没有被删除")
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.waitEntry(conv, "[Oryn confirmation · automatic] delete_thread "+target.Thread.ID+": approved and done.", 60*time.Second)
	s.settledReply(conv)
	assert.NotEqual(t, string(apigen.ThreadStateAwaitingInput), s.idle(conv).State)

	var acts apigen.AutomationActionList
	s.rest("GET", "/api/v1/automation/actions?thread="+target.Thread.ID, nil, &acts, 200)
	var del *apigen.AutomationAction
	for i, a := range acts.Actions {
		if string(a.Action) == "delete_thread" {
			del = &acts.Actions[i]
		}
	}
	require.NotNil(t, del, "删除写了审计：%+v", acts.Actions)
	assert.True(t, del.Ok)
	assert.Equal(t, apigen.AutomationActionOrigin("owner"), del.Origin)
	assert.Equal(t, apigen.AutomationActionActorKind("assistant"), del.ActorKind)
}

// 主线：开着 handToAssistant 的定时（new_thread）运行结束 → 15 秒合并窗口（假时钟）到点后主线收到
// `[Oryn automation results · automatic]`，该运行线程没有 done 通知；主线里自动回合调用 stay_silent 后不置未读、不推送。
func TestE2EM19Main(t *testing.T) {
	useM19Notify(t)
	clk := useSchedClock(t)
	s, _ := newM9Stack(t)
	ctx := context.Background()
	var main apigen.ThreadEnvelope
	s.rest("GET", "/api/v1/assistant/main", nil, &main, 200)
	mainID := main.Thread.ID
	require.NotNil(t, main.Thread.Main)
	assert.True(t, *main.Thread.Main)

	var sc apigen.Schedule
	s.rest("POST", "/api/v1/schedules", map[string]any{
		"mode": "new_thread", "name": "digest", "kind": "every", "every": "1h", "handToAssistant": true,
		"prompt": "only reply run {{run_number}}",
	}, &sc, 201)
	assert.True(t, sc.HandToAssistant)
	var run apigen.ScheduleRun
	s.rest("POST", "/api/v1/schedules/"+sc.ID+"/run", nil, &run, 200)
	done := s.waitRun(sc.ID, 1, "completed")
	require.NotNil(t, done.ThreadID)
	runThread := *done.ThreadID

	// 结算之后才进收件：等它落库再拨过合并窗口
	require.Eventually(t, func() bool {
		rows, err := s.srv.Store.Q.ListPendingAssistantInbox(ctx, 10)
		require.NoError(t, err)
		return len(rows) == 1 && rows[0].ThreadID == runThread
	}, 30*time.Second, 50*time.Millisecond, "运行结果进了主线收件")
	assert.False(t, entriesContain(t, s, mainID, "[Oryn automation results"), "合并窗口未到不投递")
	clk.Advance(15 * time.Second)

	s.waitEntry(mainID, "[Oryn automation results · automatic] 1 result(s).", 60*time.Second)
	assert.True(t, entriesContain(t, s, mainID, runThread), "自动消息带该运行的线程 id")
	s.settledReply(mainID)
	assert.True(t, s.threadRow(mainID).Unread, "说出口的自动回合照常置未读")
	// 主线回合的 done 通知（晚于运行线程结束）到了，说明运行线程的 done 定时器早已到期：它不该有通知
	s.waitNotification(30*time.Second, func(n apigen.Notification) bool {
		return n.Kind == "done" && n.ThreadID != nil && *n.ThreadID == mainID
	})
	assert.Zero(t, s.countNotifications(runThread, "done"), "交给助手的运行不推送完成通知")
	assert.True(t, s.threadRow(runThread).Unread, "运行线程照常标未读")

	// stay_silent：主线上的提醒（线程内定时，自动化身份）让助手沉默
	s.rest("PATCH", "/api/v1/threads/"+mainID, map[string]any{"unread": false}, nil, 200)
	require.False(t, s.threadRow(mainID).Unread)
	doneBefore := s.countNotifications(mainID, "done")
	var quiet apigen.Schedule
	s.rest("POST", "/api/v1/schedules", map[string]any{
		"mode": "thread", "threadId": mainID, "name": "quiet check", "kind": "every", "every": "1h",
		"prompt": `hosttool stay_silent {"reason":"nothing new"}`,
	}, &quiet, 201)
	s.rest("POST", "/api/v1/schedules/"+quiet.ID+"/run", nil, &run, 200)
	s.waitRun(quiet.ID, 1, "completed")
	reply := s.settledReply(mainID)
	require.True(t, strings.HasPrefix(reply, "host:stay_silent:ok:"), reply)
	assert.Contains(t, reply, `"ok":true`)
	time.Sleep(500 * time.Millisecond) // done 去抖 50ms：足够让本不该发的通知发出来
	assert.False(t, s.threadRow(mainID).Unread, "沉默的回合不置未读")
	assert.Equal(t, doneBefore, s.countNotifications(mainID, "done"), "沉默的回合不推送")
}

// startM19Runner 建 Runner 并以 `orbd run --runner` 起它（ORYN_AGENT_BIN = fakeomp），等它上线；返回 Runner 的
// orb id 与声明的工作目录。
func startM19Runner(t *testing.T, s *stack, name string) (orbID, work string) {
	t.Helper()
	var created struct {
		Runner struct {
			ID    string `json:"id"`
			OrbID string `json:"orbId"`
		} `json:"runner"`
		Token string `json:"token"`
	}
	s.rest("POST", "/api/v1/runners", map[string]string{"name": name}, &created, 201)
	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "orbd"), "run", "--runner", "--dir", work, "--allow-root")
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(), "ORYN_URL="+s.http.URL, "ORYN_RUNNER_TOKEN="+created.Token,
		"ORYN_AGENT_BIN="+filepath.Join(binDir, "fakeomp"), "FAKEOMP_DELAY_MS=15",
	)
	var logs strings.Builder
	cmd.Stdout, cmd.Stderr = &logs, &logs
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("runner orbd 日志：\n%s", logs.String())
		}
	})
	var list struct {
		Runners []struct {
			Online bool `json:"online"`
		} `json:"runners"`
	}
	require.Eventually(t, func() bool {
		s.rest("GET", "/api/v1/runners", nil, &list, 200)
		return len(list.Runners) == 1 && list.Runners[0].Online
	}, 30*time.Second, 50*time.Millisecond, "runner 上线")
	return created.Runner.OrbID, work
}

// 助手在 Runner 上建线程（create_thread{runner, dir}）并向它发消息（send_message），两者都写审计。
func TestE2EM19Runner(t *testing.T) {
	s, _ := newM9Stack(t)
	runnerOrb, work := startM19Runner(t, s, "laptop")
	var asst apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &asst, 201)
	conv := asst.Thread.ID

	s.prompt(conv, `hosttool create_thread {"prompt":"hello runner","runner":"laptop","dir":"`+work+`"}`)
	require.True(t, entriesContain(t, s, conv, "host:create_thread:ok:"), lastAssistant(t, s, conv))
	var list apigen.ThreadList
	s.rest("GET", "/api/v1/threads", nil, &list, 200)
	var child string
	for _, th := range list.Threads {
		if th.ParentThreadID != nil && *th.ParentThreadID == conv {
			child = th.ID
		}
	}
	require.NotEmpty(t, child, "助手建出的线程")
	row := s.threadRow(child)
	require.NotNil(t, row.OrbID)
	assert.Equal(t, runnerOrb, *row.OrbID, "线程跑在 Runner 上")
	require.NotNil(t, row.Cwd)
	assert.Equal(t, work, *row.Cwd)
	s.idle(child)
	s.waitEntry(child, "hello runner", 60*time.Second)
	s.waitEntry(conv, "[Oryn thread report · automatic] "+child+" is now idle.", 60*time.Second)
	s.settledReply(conv)

	s.prompt(conv, `hosttool send_message {"threadId":"`+child+`","message":"second from the assistant"}`)
	require.True(t, entriesContain(t, s, conv, "host:send_message:ok:"), lastAssistant(t, s, conv))
	s.waitEntry(child, `(`+conv+`) — sent by an agent, not typed by the owner]\nsecond from the assistant`, 60*time.Second)
	s.idle(child)

	var acts apigen.AutomationActionList
	s.rest("GET", "/api/v1/automation/actions?thread="+child, nil, &acts, 200)
	got := map[string]bool{}
	for _, a := range acts.Actions {
		assert.Equal(t, apigen.AutomationActionActorKind("assistant"), a.ActorKind)
		got[string(a.Action)] = a.Ok
	}
	assert.Equal(t, map[string]bool{"create_thread": true, "send_message": true}, got)
}

// tail 订阅：sub.tail 只补发最近 tail 条（夹到 50–2000），快照带 windowStart；更早的经 entries?before= 取回；
// 不带 tail 的订阅照旧补发全部。
func TestE2EM19Tail(t *testing.T) {
	s := newStack(t, m9Idle)
	var th apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "turn 0", "executor": map[string]any{"kind": "new_orb"}}, &th, 201)
	id := th.Thread.ID
	s.settledReply(id)
	for i := 1; i <= 15; i++ {
		s.prompt(id, "turn "+strings.Repeat("x", i))
	}
	rows, err := s.srv.Store.Q.ListEntriesAfter(context.Background(), db.ListEntriesAfterParams{ThreadID: id, AfterSeq: 0, Limit: 5000})
	require.NoError(t, err)
	require.Greater(t, len(rows), 60, "条目足够多才看得出窗口")
	wantStart := rows[len(rows)-50].Seq - 1

	collect := func(w *wsClient, sub map[string]any) (wsMsg, []int64) {
		w.send(sub)
		var snap wsMsg
		w.until(10*time.Second, func(m wsMsg) bool {
			if m.Type == "thread.snapshot" && m.ThreadID == id {
				snap = m
				return true
			}
			return false
		})
		var seqs []int64
		for snap.LastSeq > 0 && (len(seqs) == 0 || seqs[len(seqs)-1] < snap.LastSeq) {
			w.until(10*time.Second, func(m wsMsg) bool {
				if m.Type != "entries" || m.ThreadID != id {
					return false
				}
				for _, raw := range m.Entries {
					var e struct {
						Seq int64 `json:"seq"`
					}
					require.NoError(t, json.Unmarshal(raw, &e))
					seqs = append(seqs, e.Seq)
				}
				return true
			})
		}
		return snap, seqs
	}

	// tail 小于下限按 50 算
	snap, seqs := collect(s.dial(), map[string]any{"type": "sub", "threadId": id, "tail": 10})
	assert.Equal(t, rows[len(rows)-1].Seq, snap.LastSeq)
	assert.Equal(t, wantStart, snap.WindowStart)
	require.Len(t, seqs, 50)
	assert.Equal(t, wantStart+1, seqs[0])
	assert.Equal(t, snap.LastSeq, seqs[len(seqs)-1])

	// 不带 tail：全部补发，没有窗口
	snap, seqs = collect(s.dial(), map[string]any{"type": "sub", "threadId": id})
	assert.Zero(t, snap.WindowStart)
	assert.Len(t, seqs, len(rows))

	// 窗口之前的一页
	var page threads.EntryPage
	s.rest("GET", "/api/v1/threads/"+id+"/entries?before="+strconv.FormatInt(wantStart+1, 10)+"&limit=500", nil, &page, 200)
	assert.False(t, page.HasMore)
	require.Len(t, page.Entries, len(rows)-50)
	assert.Equal(t, rows[0].Seq, page.Entries[0].Seq)
	assert.Equal(t, wantStart, page.Entries[len(page.Entries)-1].Seq)
	s.rest("GET", "/api/v1/threads/"+id+"/entries?before=10&after=1", nil, nil, 400)
}

// 助手的电脑：computer_status 第一次用到时建出 computer = 1 的 orb（fakeomp 兼作工具执行器）并取回工具定义；
// computer_bash 的流式输出以 tool_execution_update 到达订阅者，结果原样回到助手。
func TestE2EM19Computer(t *testing.T) {
	s, _ := newM9Stack(t)
	var asst apigen.ThreadEnvelope
	s.rest("POST", "/api/v1/assistant/conversations", nil, &asst, 201)
	conv := asst.Thread.ID

	s.prompt(conv, `hosttool computer_status {}`)
	reply := lastAssistant(t, s, conv)
	require.True(t, strings.HasPrefix(reply, "host:computer_status:ok:"), reply)
	var comp apigen.AssistantComputer
	s.rest("GET", "/api/v1/assistant/computer", nil, &comp, 200)
	require.NotNil(t, comp.OrbID, "电脑已建出")
	assert.True(t, comp.ToolsReady)
	var orb apigen.Orb
	s.rest("GET", "/api/v1/orbs/"+*comp.OrbID, nil, &orb, 200)
	require.NotNil(t, orb.Computer)
	assert.True(t, *orb.Computer)
	assert.Equal(t, apigen.OrbState("ready"), orb.State)

	w := s.dial()
	w.send(map[string]any{"type": "sub", "threadId": conv})
	s.send(conv, `hosttool computer_bash {"command":"echo one; sleep 0.5; echo two"}`)
	var partials []string
	w.until(60*time.Second, func(m wsMsg) bool {
		if m.Type != "frame" || m.ThreadID != conv {
			return false
		}
		var f struct {
			Type          string `json:"type"`
			ToolName      string `json:"toolName"`
			PartialResult struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"partialResult"`
		}
		require.NoError(t, json.Unmarshal(m.Frame, &f))
		if f.Type == "tool_execution_update" && f.ToolName == "computer_bash" && len(f.PartialResult.Content) > 0 {
			partials = append(partials, f.PartialResult.Content[0].Text)
		}
		return f.Type == "agent_end"
	})
	require.NotEmpty(t, partials, "流式输出经 host_tool_update 到达")
	assert.Equal(t, "one\n", partials[0], "第一帧只有 sleep 之前的输出")
	reply = s.settledReply(conv)
	assert.True(t, strings.HasPrefix(reply, "host:computer_bash:ok:one\ntwo"), reply)
	assert.True(t, entriesContain(t, s, conv, `"toolName":"computer_bash"`))

	// 全库只有一台电脑：再次用到不新建
	s.prompt(conv, `hosttool computer_status {}`)
	var orbsList apigen.OrbList
	s.rest("GET", "/api/v1/orbs", nil, &orbsList, 200)
	n := 0
	for _, o := range orbsList.Orbs {
		if o.Computer != nil && *o.Computer {
			n++
		}
	}
	assert.Equal(t, 1, n)
}
