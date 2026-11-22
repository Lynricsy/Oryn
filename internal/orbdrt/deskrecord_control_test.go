package orbdrt

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// 人工输入前结束录制（M25 计划 3.5）：fake ffmpeg 读到 q 后先睡 ffmpegQuitDelay 再退出，
// 用来确认新控制状态交给 onState 的时刻晚于 ffmpeg 退出。

const ffmpegQuitDelay = 300 * time.Millisecond

const recCtlThread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ad"

// ctlRecEnv 是记录「控制状态第一次变成 watch 时交给 onState 的时刻，以及当时 ffmpeg 是否已退出」的桌面环境。
type ctlRecEnv struct {
	*deskEnv
	rr  *deskRecorder
	cur atomic.Pointer[recording] // 被观察的录制（start 之后设）

	mu       sync.Mutex
	seenAt   time.Time
	seenDone bool // 当时 ffmpeg 已退出且录制已收尾
}

func newCtlRecEnv(t *testing.T, watch apigen.OrbDesktopControlState) *ctlRecEnv {
	t.Setenv("FAKEDESK_FFMPEG_QUIT_DELAY", ffmpegQuitDelay.String())
	ce := &ctlRecEnv{}
	ce.deskEnv = newDeskEnv(t, func(c *Config) {
		c.DesktopControlTimings = longTimings()
		prev := c.OnDesktopState
		c.OnDesktopState = func(s apigen.LinkCtlDesktopStateParams) {
			// 回调在 emitMu 下：只看 channel，不碰录制器的锁。
			if rec := ce.cur.Load(); rec != nil && controlOf(s).State == watch {
				ce.mu.Lock()
				if ce.seenAt.IsZero() {
					ce.seenAt, ce.seenDone = time.Now(), isClosed(rec.exited) && isClosed(rec.finished)
				}
				ce.mu.Unlock()
			}
			prev(s)
		}
	})
	ce.wait("display ready", displayIs(true))
	ce.rr = ce.rt.desk.recorder()
	return ce
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// startWatched 开始录制并登记为被观察的录制。
func (ce *ctlRecEnv) startWatched(t *testing.T) orbsock.DesktopRecordStartResult {
	t.Helper()
	started, err := ce.rr.start(t.Context())
	require.NoError(t, err)
	ce.rr.mu.Lock()
	ce.cur.Store(ce.rr.cur)
	ce.rr.mu.Unlock()
	return started
}

// assertSeenAfterExit 断言被观察的状态在 ffmpeg 退出之后才交给 onState，且晚于 since 至少 ffmpegQuitDelay。
func (ce *ctlRecEnv) assertSeenAfterExit(t *testing.T, since time.Time) {
	t.Helper()
	ce.mu.Lock()
	defer ce.mu.Unlock()
	require.False(t, ce.seenAt.IsZero(), "the new control state was not handed to onState")
	assert.True(t, ce.seenDone, "the new control state was handed out before ffmpeg exited")
	assert.GreaterOrEqual(t, ce.seenAt.Sub(since), ffmpegQuitDelay)
}

// assertStoppedByControl 断言录制已结束，下一次 stop 取走 StoppedBy=control 的结果，再下一次回 not_recording。
func (ce *ctlRecEnv) assertStoppedByControl(t *testing.T, started orbsock.DesktopRecordStartResult) {
	t.Helper()
	assert.False(t, ce.rr.active())
	rec, err := ce.rr.stop(t.Context())
	require.NoError(t, err)
	assert.Equal(t, started.Path, rec.Path)
	assert.Equal(t, orbsock.RecordStoppedByControl, rec.StoppedBy)
	assertRecordingFile(t, rec)
	_, err = ce.rr.stop(t.Context())
	assert.ErrorIs(t, err, errNotRecording)
}

// TestDeskRecordControlTake：agent → user（takeControl）先结束录制再上报 user；等 ffmpeg 收尾时不持 d.mu；
// user 状态下 start 回 409 control_user（本机路由与 sock 同码）；续期不受影响。
func TestDeskRecordControlTake(t *testing.T) {
	ce := newCtlRecEnv(t, apigen.OrbDesktopControlStateUser)
	d := ce.rt.desk
	o := newOrbHTTP(t, ce.cp, ce.rt)
	d.observeAgentFrame(recCtlThread, "agent_start", []byte(`{"type":"agent_start"}`))
	ce.wait("agent", controlIs(apigen.OrbDesktopControlStateAgent, false))
	started := ce.startWatched(t)

	t0 := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.takeControl()
	}()
	// 等 ffmpeg 收尾期间 d.mu 不被占着：快照立即返回；录制还在时状态仍是 agent。
	polls := 0
	for ce.rr.active() {
		s0 := time.Now()
		st := d.snapshot()
		assert.Less(t, time.Since(s0), 100*time.Millisecond, "snapshot blocked on d.mu while the recorder was finishing")
		if ce.rr.active() {
			assert.Equal(t, apigen.OrbDesktopControlStateAgent, controlOf(st).State)
			polls++
		}
		time.Sleep(10 * time.Millisecond)
	}
	<-done
	assert.GreaterOrEqual(t, time.Since(t0), ffmpegQuitDelay)
	assert.Positive(t, polls)
	ce.wait("user", controlIs(apigen.OrbDesktopControlStateUser, true))
	ce.assertSeenAfterExit(t, t0)
	ce.assertStoppedByControl(t, started)

	resp, b := o.do(http.MethodPost, "/desktop/record/start", nil, nil, nil)
	assert.Equal(t, orbsock.CodeControlUser, o.errCode(resp, b, http.StatusConflict))
	_, err := sockCall(t, ce.rt, orbsock.MethodDesktopRecordStart, nil, nil)
	assert.Equal(t, orbsock.CodeControlUser, sockErrCode(err))

	// 已接管时 take 只续期，立即返回
	t1 := time.Now()
	d.takeControl()
	assert.Less(t, time.Since(t1), ffmpegQuitDelay)
}

// TestDeskRecordControlAsk：agent → ask（desktop_ask_user 的 select 帧）先结束录制再上报 ask；ask 状态下 start 回
// control_user；作答后回到 agent 不自动重新录制。
func TestDeskRecordControlAsk(t *testing.T) {
	ce := newCtlRecEnv(t, apigen.OrbDesktopControlStateAsk)
	d := ce.rt.desk
	d.observeAgentFrame(recCtlThread, "agent_start", []byte(`{"type":"agent_start"}`))
	ce.wait("agent", controlIs(apigen.OrbDesktopControlStateAgent, false))
	started := ce.startWatched(t)

	t0 := time.Now()
	d.observeAgentFrame(recCtlThread, "extension_ui_request", []byte(`{"type":"extension_ui_request","id":"q1","method":"select","title":"需要你操作桌面（登录）：登录 GitHub","options":["完成","跳过"]}`))
	ce.wait("ask", controlIs(apigen.OrbDesktopControlStateAsk, false))
	ce.assertSeenAfterExit(t, t0)

	_, err := sockCall(t, ce.rt, orbsock.MethodDesktopRecordStart, nil, nil)
	assert.Equal(t, orbsock.CodeControlUser, sockErrCode(err))
	o := newOrbHTTP(t, ce.cp, ce.rt)
	resp, b := o.do(http.MethodPost, "/desktop/record/start", nil, nil, nil)
	assert.Equal(t, orbsock.CodeControlUser, o.errCode(resp, b, http.StatusConflict))

	d.observeAgentCommand([]byte(`{"type":"extension_ui_response","id":"q1","value":"完成"}`))
	ce.wait("back to agent", controlIs(apigen.OrbDesktopControlStateAgent, false))
	ce.assertStoppedByControl(t, started)
}

// TestDeskRecordControlTurnEnd：agent → idle（终结的 agent_end）结束录制；非终结的 agent_end 不动录制；
// idle 状态下 start 成功，之后 agent stop 照常。
func TestDeskRecordControlTurnEnd(t *testing.T) {
	ce := newCtlRecEnv(t, apigen.OrbDesktopControlStateIdle)
	d := ce.rt.desk
	o := newOrbHTTP(t, ce.cp, ce.rt)
	d.observeAgentFrame(recCtlThread, "agent_start", []byte(`{"type":"agent_start"}`))
	ce.wait("agent", controlIs(apigen.OrbDesktopControlStateAgent, false))
	started := ce.startWatched(t)

	d.observeAgentFrame(recCtlThread, "agent_end", []byte(`{"type":"agent_end","isTerminal":false}`))
	assert.True(t, ce.rr.active(), "a non-terminal agent_end keeps the turn and the recording")

	t0 := time.Now()
	d.observeAgentFrame(recCtlThread, "agent_end", []byte(`{"type":"agent_end"}`))
	ce.wait("idle", controlIs(apigen.OrbDesktopControlStateIdle, false))
	ce.assertSeenAfterExit(t, t0)
	ce.assertStoppedByControl(t, started)

	// idle（助手的电脑通常一直是 idle）里可以开始录制
	var again orbsock.DesktopRecordStartResult
	o.json(http.MethodPost, "/desktop/record/start", nil, nil, http.StatusOK, &again)
	var rec orbsock.DesktopRecording
	o.json(http.MethodPost, "/desktop/record/stop", nil, nil, http.StatusOK, &rec)
	assert.Equal(t, again.Path, rec.Path)
	assert.Equal(t, orbsock.RecordStoppedByAgent, rec.StoppedBy)
}

// TestDeskRecordControlAgentExited：回合中线程的 omp 进程退出（agent → idle）同样先结束录制。
func TestDeskRecordControlAgentExited(t *testing.T) {
	ce := newCtlRecEnv(t, apigen.OrbDesktopControlStateIdle)
	d := ce.rt.desk
	d.observeAgentFrame(recCtlThread, "agent_start", []byte(`{"type":"agent_start"}`))
	ce.wait("agent", controlIs(apigen.OrbDesktopControlStateAgent, false))
	started := ce.startWatched(t)

	t0 := time.Now()
	d.agentExited(recCtlThread)
	ce.wait("idle", controlIs(apigen.OrbDesktopControlStateIdle, false))
	ce.assertSeenAfterExit(t, t0)
	ce.assertStoppedByControl(t, started)
}
