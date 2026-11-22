package schedule

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/sshgw"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// ---- 运行前闸门 ----

type gateCall struct {
	projectID  string
	automation bool
	host, cmd  string
	timeout    time.Duration
}

// fakeGate 记录调用；hosts 是可用的主机名（nil = 全部可用），result 是每次执行的结果。
type fakeGate struct {
	mu     sync.Mutex
	hosts  map[string]bool
	result sshgw.GateResult
	calls  []gateCall
	// queued 非 nil 时 Go 不立即执行，而是把闸门排进这里（模拟仍在执行中）。
	queued *[]func()
}

func (g *fakeGate) CheckGateHost(_ context.Context, _ string, _ bool, host string) error {
	if g.hosts != nil && !g.hosts[host] {
		return errors.New("host not visible")
	}
	return nil
}

func (g *fakeGate) RunGate(_ context.Context, projectID string, automation bool, host, command, _ string, timeout time.Duration) sshgw.GateResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, gateCall{projectID: projectID, automation: automation, host: host, cmd: command, timeout: timeout})
	return g.result
}

func (g *fakeGate) Calls() []gateCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gateCall(nil), g.calls...)
}

// withGate 给调度器装上假闸门；闸门默认同步执行（结果入队，下一次处理事件时生效）。
func (f *fixture) withGate() *fakeGate {
	g := &fakeGate{}
	f.s.cfg.Gate = g
	f.s.cfg.Go = func(fn func()) {
		if g.queued != nil {
			*g.queued = append(*g.queued, fn)
			return
		}
		fn()
	}
	return g
}

func gateInput(host, cmd string) *apigen.ScheduleGate {
	return &apigen.ScheduleGate{Host: host, Command: cmd}
}

// 闸门输出 {"wakeAgent": false}：本次运行记 skipped(gate)，不建线程；其它输出照常建线程，输出附在头行之后、提示词之前。
func TestGateSkipsOrWakes(t *testing.T) {
	f := newFixture(t)
	g := f.withGate()
	prj := f.project("ops")
	s := f.putNew(Input{Name: "watch", Kind: KindEvery, Every: "1h", Prompt: "handle it", ProjectID: prj, Gate: gateInput("ultra", "s2a gate")})
	require.NotNil(t, s.Gate)
	assert.Equal(t, 60, *s.Gate.TimeoutSeconds, "缺省 60 秒")

	g.result = sshgw.GateResult{Stdout: "checked 12 instances\n{\"wakeAgent\": false}\n"}
	f.advanceTick(time.Hour)
	runs := f.runs(s.ID)
	require.Len(t, runs, 1)
	assert.Equal(t, runSkipped, runs[0].Status)
	assert.Equal(t, reasonGate, deref(runs[0].Error))
	assert.Empty(t, f.th.Creates(), "闸门判定不必运行：不建线程")
	require.Len(t, g.Calls(), 1)
	assert.Equal(t, gateCall{projectID: prj, automation: true, host: "ultra", cmd: "s2a gate", timeout: time.Minute}, g.Calls()[0],
		"项目定时按项目范围、全自动解析主机")

	g.result = sshgw.GateResult{Stdout: "{\"new\": [\"proxy:jp\"]}\n"}
	f.advanceTick(time.Hour)
	creates := f.th.Creates()
	require.Len(t, creates, 1)
	prompt := creates[0].Prompt
	first, rest, _ := strings.Cut(prompt, "\n")
	assert.True(t, runAnchor(f.runs(s.ID)[0].ScheduledFor)(prompt), "头行仍是第一行，恢复时认得出：%q", first)
	assert.True(t, strings.HasPrefix(rest, "[Pre-run check on ultra exited with code 0]"), rest)
	assert.Contains(t, rest, "{\"new\": [\"proxy:jp\"]}")
	assert.True(t, strings.HasSuffix(rest, "\n\nhandle it"), rest)
	assert.Equal(t, runRunning, f.runs(s.ID)[0].Status)
}

// 闸门没能正常结束（连不上、超时）或非 0 退出：一律照常运行，失败原因交给 agent；非 0 退出时即使输出了 wakeAgent:false 也运行。
func TestGateFailureStillRuns(t *testing.T) {
	f := newFixture(t)
	g := f.withGate()
	th := f.thread()
	s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "check", Gate: gateInput("ultra", "gate")})

	g.result = sshgw.GateResult{Err: errors.New("dial tcp: connection refused")}
	f.advanceTick(time.Hour)
	ds := f.th.Delivers()
	require.Len(t, ds, 1)
	assert.Contains(t, ds[0].text, "[Pre-run check on ultra failed: dial tcp: connection refused]")
	assert.Equal(t, gateCall{host: "ultra", cmd: "gate", timeout: time.Minute}, g.Calls()[0], "线程内定时：线程的项目（无），不算全自动")
	f.ack(0, nil)
	f.done(0)

	g.result = sshgw.GateResult{Stdout: "{\"wakeAgent\": false}", Stderr: "boom", ExitCode: 2}
	f.advanceTick(time.Hour)
	ds = f.th.Delivers()
	require.Len(t, ds, 2)
	assert.Contains(t, ds[1].text, "exited with code 2")
	assert.Contains(t, ds[1].text, "Stderr:\n```\nboom\n```")
	f.ack(1, nil)
	assert.Equal(t, runRunning, f.runs(s.ID)[0].Status, "线程内定时：应答后 running")
}

// 闸门执行期间运行保持 pending：下一次到点按重叠跳过；定时在此期间被删除时丢弃闸门结果。
func TestGateInFlight(t *testing.T) {
	f := newFixture(t)
	g := f.withGate()
	var queued []func()
	g.queued = &queued
	g.result = sshgw.GateResult{Stdout: "wake"}
	s := f.putNew(Input{Name: "slow", Kind: KindEvery, Every: "1m", Prompt: "p", Gate: gateInput("h", "c")})

	f.advanceTick(time.Minute)
	require.Len(t, queued, 1)
	assert.Equal(t, runPending, f.runs(s.ID)[0].Status)
	f.advanceTick(time.Minute)
	runs := f.runs(s.ID)
	require.Len(t, runs, 2)
	assert.Equal(t, runSkipped, runs[0].Status)
	assert.Equal(t, "overlap", deref(runs[0].Error))

	queued[0]()
	f.process()
	require.Len(t, f.th.Creates(), 1, "闸门结束后照常建线程")

	// 删除定时后到达的闸门结果被丢弃
	s2 := f.putNew(Input{Name: "gone", Kind: KindEvery, Every: "1m", Prompt: "p", Gate: gateInput("h", "c")})
	f.advanceTick(time.Minute)
	require.Len(t, queued, 2)
	require.NoError(t, f.s.Delete(f.ctx, s2.ID))
	queued[1]()
	f.process()
	assert.Len(t, f.th.Creates(), 1)
}

// 手动触发同样先过闸门。
func TestGateRunNow(t *testing.T) {
	f := newFixture(t)
	g := f.withGate()
	g.result = sshgw.GateResult{Stdout: `{"wakeAgent":false}`}
	s := f.putNew(Input{Name: "m", Kind: KindEvery, Every: "1h", Prompt: "p", Enabled: new(false), Gate: gateInput("h", "c")})
	r, err := f.s.RunNow(f.ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, apigen.ScheduleRunStatusSkipped, r.Status)
	assert.Equal(t, reasonGate, deref(r.Error))
	assert.Empty(t, f.th.Creates())
}

// 保存：主机须可用；agent 不能设置闸门，改写时沿用现值；主人整份替换时省略即去掉。
func TestGateDefinition(t *testing.T) {
	f := newFixture(t)
	g := f.withGate()
	g.hosts = map[string]bool{"ultra": true}

	_, err := f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, Name: "x", Kind: KindEvery, Every: "1h", Prompt: "p", Gate: gateInput("nope", "c")}, "user")
	requireCode(t, err, CodeInvalidSchedule)
	_, err = f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, Name: "x", Kind: KindEvery, Every: "1h", Prompt: "p", Gate: &apigen.ScheduleGate{Host: "ultra", Command: "c", TimeoutSeconds: new(601)}}, "user")
	requireCode(t, err, CodeInvalidSchedule)
	_, err = f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, Name: "x", Kind: KindEvery, Every: "1h", Prompt: "p", Gate: gateInput("ultra", "  ")}, "user")
	requireCode(t, err, CodeInvalidSchedule)

	th := f.thread()
	_, err = f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "1h", Prompt: "p", Gate: gateInput("ultra", "c")}, "agent")
	requireCode(t, err, CodeInvalidSchedule)

	s := f.putThread(th, Input{Kind: KindEvery, Every: "1h", Prompt: "p", Gate: &apigen.ScheduleGate{Host: " ultra ", Command: "c", TimeoutSeconds: new(30)}})
	assert.Equal(t, apigen.ScheduleGate{Host: "ultra", Command: "c", TimeoutSeconds: new(30)}, *s.Gate)
	got, err := f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "2h", Prompt: "p2"}, "agent")
	require.NoError(t, err)
	require.NotNil(t, got.Gate, "agent 改写沿用闸门")
	assert.Equal(t, "ultra", got.Gate.Host)
	got, err = f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "2h", Prompt: "p2"}, "user")
	require.NoError(t, err)
	assert.Nil(t, got.Gate, "主人整份替换省略即去掉")

	// 没有 SSH 网关时不能保存带闸门的定义
	f.s.cfg.Gate = nil
	_, err = f.s.Put(f.ctx, "", Input{Mode: ModeNewThread, Name: "y", Kind: KindEvery, Every: "1h", Prompt: "p", Gate: gateInput("ultra", "c")}, "user")
	requireCode(t, err, CodeInvalidSchedule)
}

// 安静结束的回合不交给助手。
func TestQuietTurnNotHandedOff(t *testing.T) {
	f := newFixture(t)
	got := f.handOffs()
	s := f.putNew(Input{Name: "digest", Kind: KindEvery, Every: "1h", Prompt: "p", HandToAssistant: new(true)})
	f.advanceTick(time.Hour)
	c := f.th.Creates()[0]
	tid := *f.runs(s.ID)[0].ThreadID
	f.state(tid, apigen.ThreadStateWorking)
	f.s.TurnEnded(threads.TurnEvent{ThreadID: tid, Tag: c.Tag, Status: threads.TurnCompleted, Text: "[SILENT]", HasText: true, Quiet: true})
	f.process()
	assert.Empty(t, got())
	assert.Equal(t, runCompleted, f.runs(s.ID)[0].Status)
}

func TestWakeAgent(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      bool
	}{
		{"empty", "", true},
		{"false", `{"wakeAgent": false}`, false},
		{"false after logs", "line 1\n{\"wakeAgent\":false}\n\n  ", false},
		{"true", `{"wakeAgent": true}`, true},
		{"missing key", `{"new": []}`, true},
		{"string false", `{"wakeAgent": "false"}`, true},
		{"not last", "{\"wakeAgent\": false}\nmore", true},
		{"array", `[false]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, wakeAgent(tc.out), "wakeAgent(%q)", tc.out)
		})
	}
}

func TestFencedOutlastsBackticks(t *testing.T) {
	assert.Equal(t, "```\nplain\n```", fenced("plain\n"))
	assert.Equal(t, "````\na ``` b\n````", fenced("a ``` b"))
}
