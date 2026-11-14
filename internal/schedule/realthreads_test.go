package schedule

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

// 走真实 threads reducer、真实 RecoveredOutcomeMatch 与 EntriesAppended 的集成测试（补充契约 C1.6 / C1.7）。

// errorTurnScript 是 fakeomp 的回合：写用户条目 → agent_start → 等一次作答（重启调度器的时机）→
// agent_end → prompt_result(error) → 隔一会儿才写 stopReason=error 的 assistant 条目（条目晚于状态事件入库，
// 之后不再有状态变化，只能靠 EntriesAppended 结算）→ session_settled。
const errorTurnScript = `{"entry":{"role":"user","content":[{"type":"text","text":"{{prompt}}"}]}}
{"emit":{"type":"agent_start"}}
{"ask":{"method":"select","title":"continue?","options":["yes","no"]}}
{"emit":{"type":"agent_end","isTerminal":true,"yielded":true,"messages":[]}}
{"emit":{"type":"prompt_result","agentInvoked":true,"status":"error","error":{"message":"provider exploded"},"sessionSettled":true}}
{"sleep":200}
{"entry":{"role":"assistant","content":[{"type":"text","text":"partial"}],"stopReason":"error","errorMessage":"provider exploded"}}
{"emit":{"type":"session_settled"}}
`

var (
	realFakeompOnce sync.Once
	realFakeompPath string
	realFakeompErr  error
)

func realFakeomp(t *testing.T) string {
	t.Helper()
	realFakeompOnce.Do(func() {
		dir, err := os.MkdirTemp("", "oryn-schedule-fakeomp-*")
		if err != nil {
			realFakeompErr = err
			return
		}
		realFakeompPath, realFakeompErr = threadstest.BuildFakeOmp(dir)
	})
	require.NoError(t, realFakeompErr)
	return realFakeompPath
}

// untagged 包一层真实线程服务：投递时去掉标签（模拟重启后命令 id 已丢、threads 不会再为它发 TurnEnded），
// 并记录 RecoveredOutcomeMatch 的结果。
type untagged struct {
	*threads.Service
	mu       sync.Mutex
	outcomes []bool
}

func (u *untagged) Deliver(ctx context.Context, threadID string, m threads.AutoMessage, ack func(error)) (func() bool, error) {
	m.Tag = ""
	return u.Service.Deliver(ctx, threadID, m, ack)
}

func (u *untagged) RecoveredOutcomeMatch(ctx context.Context, threadID string, match func(string) bool, since int64) (threads.TurnEvent, bool, error) {
	ev, ok, err := u.Service.RecoveredOutcomeMatch(ctx, threadID, match, since)
	u.mu.Lock()
	u.outcomes = append(u.outcomes, ok)
	u.mu.Unlock()
	return ev, ok, err
}

// poker 是测试用 Observer：任何线程事件都戳一下 poke，待答请求另送进 asks。
type poker struct {
	poke chan struct{}
	asks chan string
}

func (p *poker) signal() {
	select {
	case p.poke <- struct{}{}:
	default:
	}
}
func (p *poker) StateChanged(threads.StateEvent) { p.signal() }
func (p *poker) InputRequested(ev threads.InputEvent) {
	p.asks <- ev.RequestID
	p.signal()
}
func (p *poker) InputResolved(string, string) { p.signal() }
func (p *poker) Seen(string)                  {}
func (p *poker) TurnEnded(threads.TurnEvent)  { p.signal() }
func (p *poker) EntriesAppended(string)       { p.signal() }

// waitUntil 每收到一次事件就检查 cond，直到成立（上限 20 秒，只作防挂死）。
func (p *poker) waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !cond() {
		select {
		case <-p.poke:
		case <-deadline:
			t.Fatalf("等待超时：%s", what)
		}
	}
}

func (p *poker) nextAsk(t *testing.T) string {
	t.Helper()
	select {
	case id := <-p.asks:
		return id
	case <-time.After(20 * time.Second):
		t.Fatal("等待待答请求超时")
		return ""
	}
}

// pump 只处理事件（Run 去掉 recover 与到点触发），返回停止函数。
func pump(s *Service) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				s.loopMu.Lock()
				s.processEvents(ctx)
				s.loopMu.Unlock()
			}
		}
	}()
	return func() { cancel(); <-done }
}

// 重启后按 agent_end(stopReason=error) → prompt_result(error) 的顺序连续 3 次（第 1 次投递后改名并改时区：替换定义
// 会清零失败计数，所以放在第一次失败之前；旧消息的头行仍是旧名字、旧偏移），手动触发的 ScheduledFor 取真实时钟、
// 带毫秒；每次条目都晚于状态事件入库：3 个运行都 failed、定时自动暂停。
func TestRealThreadsRecoveredPromptErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	script := filepath.Join(dir, "turn.jsonl")
	require.NoError(t, os.WriteFile(script, []byte(errorTurnScript), 0o644))
	o, err := threadstest.NewOrbs(st, realFakeomp(t), filepath.Join(dir, "orbs"), "FAKEOMP_DELAY_MS=1", "FAKEOMP_SCRIPT="+script)
	require.NoError(t, err)
	t.Cleanup(o.Close)
	ts := threads.New(threads.Config{DataDir: filepath.Join(dir, "data")}, st, o)
	t.Cleanup(ts.Close)
	p := &poker{poke: make(chan struct{}, 1), asks: make(chan string, 8)}
	ts.AddObserver(p)
	th := &untagged{Service: ts}
	answer := func(threadID, id string) {
		require.NoError(t, ts.UIResponse(ctx, threadID, json.RawMessage(`{"type":"extension_ui_response","id":"`+id+`","value":"yes"}`)))
	}

	// 线程的首条提示词也走同一脚本：作答后以 prompt 错误结束
	thr, err := ts.Create(ctx, threads.CreateParams{Prompt: "setup"})
	require.NoError(t, err)
	answer(thr.ID, p.nextAsk(t))
	p.waitUntil(t, "首个回合结束", func() bool {
		g, err := ts.Get(ctx, thr.ID)
		return err == nil && g.State == threads.StateError
	})

	newSched := func() *Service {
		s := New(Config{Changed: func(string) { p.signal() }}, st, th, nopOrbsT{}, nil)
		ts.AddObserver(s)
		return s
	}
	runStatus := func(id string) string {
		r, err := st.Q.GetScheduleRun(ctx, id)
		require.NoError(t, err)
		return r.Status
	}
	cur := newSched()
	stop := pump(cur)
	sc, err := cur.PutForThread(ctx, thr.ID, Input{Name: "nightly", Kind: KindEvery, Every: "1h", Prompt: "check"}, "user")
	require.NoError(t, err)
	for i := range 3 {
		run, err := cur.RunNow(ctx, sc.ID)
		require.NoError(t, err)
		askID := p.nextAsk(t)
		p.waitUntil(t, "投递已应答", func() bool { s := runStatus(run.ID); return s == runRunning || s == runNeedsInput })
		if i == 0 {
			_, err := cur.Put(ctx, sc.ID, Input{Name: "renamed", Kind: KindEvery, Every: "1h", Prompt: "check", Timezone: "Asia/Tokyo"}, "user")
			require.NoError(t, err)
		}
		// 控制面重启：新调度器从库里恢复运行（没有标签）
		stop()
		cur = newSched()
		cur.recover(ctx)
		cur.mu.Lock()
		_, recovered := cur.recovered[run.ID]
		cur.mu.Unlock()
		require.True(t, recovered, "第 %d 次：重新登记为恢复运行", i+1)
		stop = pump(cur)

		answer(thr.ID, askID)
		p.waitUntil(t, "运行结算", func() bool { return runStatus(run.ID) == runFailed })
		r, err := st.Q.GetScheduleRun(ctx, run.ID)
		require.NoError(t, err)
		assert.Equal(t, "provider exploded", deref(r.Error), "第 %d 次", i+1)
	}
	stop()
	row, err := st.Q.GetSchedule(ctx, sc.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 3, row.Failures)
	assert.False(t, row.Enabled)
	assert.Equal(t, pausedFailures, deref(row.PausedReason))
	th.mu.Lock()
	t.Logf("RecoveredOutcomeMatch 结果序列：%v（false = 条目尚未入库，之后由 EntriesAppended 重查）", th.outcomes)
	th.mu.Unlock()
}

type nopOrbsT struct{}

func (nopOrbsT) Delete(context.Context, string) error { return nil }
