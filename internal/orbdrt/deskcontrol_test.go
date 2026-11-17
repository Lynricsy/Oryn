package orbdrt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/omprpc"
)

// syncBuf 是并发安全的写缓冲（假 omp 进程的 stdin）。
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// steers 返回写进 stdin 的 steer 命令文本。
func (b *syncBuf) steers(t *testing.T) []string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var f struct{ Type, ID, Message string }
		require.NoError(t, json.Unmarshal([]byte(line), &f))
		require.Equal(t, "steer", f.Type)
		require.True(t, strings.HasPrefix(f.ID, initIDPrefix), "steer 的应答须由 orbd 本地吞掉")
		out = append(out, f.Message)
	}
	return out
}

// fakeAgent 在 rt 上登记一个已就绪的线程进程，stdin 写进返回的缓冲（不启动真实进程）。
func fakeAgent(t *testing.T, rt *Runtime, id string) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	ready := make(chan struct{})
	close(ready)
	th := newThread(rt, id, t.TempDir())
	th.proc = &proc{w: omprpc.NewWriter(buf, 0), readyCh: ready}
	rt.threadsMu.Lock()
	rt.threads[id] = th
	rt.threadsMu.Unlock()
	return buf
}

func controlOf(s apigen.LinkCtlDesktopStateParams) apigen.OrbDesktopControl {
	if s.Control == nil {
		return apigen.OrbDesktopControl{}
	}
	return *s.Control
}

func controlIs(state apigen.OrbDesktopControlState, held bool) func(apigen.LinkCtlDesktopStateParams) bool {
	return func(s apigen.LinkCtlDesktopStateParams) bool {
		c := controlOf(s)
		return c.State == state && c.UserHeld == held
	}
}

func longTimings() *DeskControlTimings {
	return &DeskControlTimings{UserIdle: time.Hour, ViewerGrace: time.Hour, CDPHold: 300 * time.Millisecond}
}

func TestParseDesktopAsk(t *testing.T) {
	both := []string{"完成", "跳过"}
	for _, tc := range []struct {
		title   string
		options []string
		kind    apigen.OrbDesktopAskKind
		reason  string
		ok      bool
	}{
		{"需要你操作桌面（登录）：登录 GitHub", both, apigen.OrbDesktopAskKindSignIn, "登录 GitHub", true},
		{"需要你操作桌面（输入验证码）：短信验证码", both, apigen.OrbDesktopAskKindCode, "短信验证码", true},
		{"需要你操作桌面（人工验证）：过滑块\n然后点继续", both, apigen.OrbDesktopAskKindCheck, "过滑块\n然后点继续", true},
		{"需要你操作桌面：确认支付", both, apigen.OrbDesktopAskKindOther, "确认支付", true},
		{"需要你操作桌面（未知）：x", both, "", "", false},
		{"需要你操作桌面（登录）：登录", []string{"完成"}, "", "", false},
		{"别的标题", both, "", "", false},
	} {
		kind, reason, ok := parseDesktopAsk(tc.title, tc.options)
		assert.Equal(t, tc.ok, ok, tc.title)
		assert.Equal(t, tc.kind, kind, tc.title)
		assert.Equal(t, tc.reason, reason, tc.title)
	}
	_, long, ok := parseDesktopAsk("需要你操作桌面："+strings.Repeat("长", askReasonMax+10), both)
	require.True(t, ok)
	assert.Equal(t, askReasonMax, len([]rune(long)), "原因按字符截断")
}

func TestDesktopControlTransitions(t *testing.T) {
	e := newDeskEnv(t, func(c *Config) { c.DesktopControlTimings = longTimings() })
	d := e.rt.desk
	o := newOrbHTTP(t, e.cp, e.rt)
	const tid = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	stdin := fakeAgent(t, e.rt, tid)

	var st apigen.LinkCtlDesktopStateParams
	o.json(http.MethodGet, "/desktop/state", nil, nil, http.StatusOK, &st)
	assert.Equal(t, apigen.OrbDesktopControl{State: apigen.OrbDesktopControlStateIdle}, controlOf(st))

	// 回合开始：agent
	d.observeAgentFrame(tid, "agent_start", []byte(`{"type":"agent_start"}`))
	e.wait("agent", controlIs(apigen.OrbDesktopControlStateAgent, false))

	// 接管：user，并 steer 工作中的线程
	o.json(http.MethodPost, "/desktop/control/take", nil, nil, http.StatusOK, &st)
	c := controlOf(st)
	assert.Equal(t, apigen.OrbDesktopControlStateUser, c.State)
	require.NotNil(t, c.UserSince)
	e.wait("user", controlIs(apigen.OrbDesktopControlStateUser, true))
	assert.Equal(t, []string{steerUserTook}, stdin.steers(t))
	// 再次 take 只续期：不再 steer，接管时间不变
	o.json(http.MethodPost, "/desktop/control/take", nil, nil, http.StatusOK, &st)
	assert.Equal(t, *c.UserSince, *controlOf(st).UserSince)
	assert.Len(t, stdin.steers(t), 1)

	// 与格式不符的 select 不算 ask
	d.observeAgentFrame(tid, "extension_ui_request", []byte(`{"type":"extension_ui_request","id":"x1","method":"select","title":"选一个","options":["完成","跳过"]}`))
	// desktop_ask_user：ask 优先于 user
	d.observeAgentFrame(tid, "extension_ui_request", []byte(`{"type":"extension_ui_request","id":"q1","method":"select","title":"需要你操作桌面（登录）：登录 GitHub","options":["完成","跳过"]}`))
	s := e.wait("ask", controlIs(apigen.OrbDesktopControlStateAsk, true))
	assert.Equal(t, &apigen.OrbDesktopAsk{ThreadID: tid, RequestID: "q1", Kind: apigen.OrbDesktopAskKindSignIn, Reason: "登录 GitHub"}, controlOf(s).Ask)
	// 作答后回到 user
	d.observeAgentCommand([]byte(`{"type":"extension_ui_response","id":"q1","value":"完成"}`))
	e.wait("back to user", controlIs(apigen.OrbDesktopControlStateUser, true))

	// 交还：回到 agent，并 steer
	st = apigen.LinkCtlDesktopStateParams{}
	o.json(http.MethodPost, "/desktop/control/release", nil, nil, http.StatusOK, &st)
	assert.Equal(t, apigen.OrbDesktopControlStateAgent, controlOf(st).State)
	assert.Nil(t, controlOf(st).UserSince)
	assert.Equal(t, []string{steerUserTook, steerUserReleased}, stdin.steers(t))
	// 未持有时交还无操作
	o.json(http.MethodPost, "/desktop/control/release", nil, nil, http.StatusOK, &st)
	assert.Len(t, stdin.steers(t), 2)

	// 非终结的 agent_end 不结束回合
	d.observeAgentFrame(tid, "agent_end", []byte(`{"type":"agent_end","isTerminal":false}`))
	o.json(http.MethodGet, "/desktop/state", nil, nil, http.StatusOK, &st)
	assert.Equal(t, apigen.OrbDesktopControlStateAgent, controlOf(st).State)
	d.observeAgentFrame(tid, "agent_end", []byte(`{"type":"agent_end"}`))
	e.wait("idle", controlIs(apigen.OrbDesktopControlStateIdle, false))

	// 不在回合中接管不 steer
	o.json(http.MethodPost, "/desktop/control/take", nil, nil, http.StatusOK, nil)
	o.json(http.MethodPost, "/desktop/control/release", nil, nil, http.StatusOK, nil)
	assert.Len(t, stdin.steers(t), 2)

	// cancel 撤销 ask；进程退出清掉该线程的 ask 与工作状态
	d.observeAgentFrame(tid, "extension_ui_request", []byte(`{"type":"extension_ui_request","id":"q2","method":"select","title":"需要你操作桌面：确认支付","options":["完成","跳过"]}`))
	e.wait("ask 2", controlIs(apigen.OrbDesktopControlStateAsk, false))
	d.observeAgentFrame(tid, "extension_ui_request", []byte(`{"type":"extension_ui_request","method":"cancel","targetId":"q2"}`))
	e.wait("cancelled", controlIs(apigen.OrbDesktopControlStateIdle, false))
	d.observeAgentFrame(tid, "agent_start", []byte(`{"type":"agent_start"}`))
	d.observeAgentFrame(tid, "extension_ui_request", []byte(`{"type":"extension_ui_request","id":"q3","method":"select","title":"需要你操作桌面（输入验证码）：短信","options":["完成","跳过"]}`))
	e.wait("ask 3", controlIs(apigen.OrbDesktopControlStateAsk, false))
	d.agentExited(tid)
	e.wait("exited", controlIs(apigen.OrbDesktopControlStateIdle, false))
}

func TestDesktopControlAutoRelease(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		e := newDeskEnv(t, func(c *Config) {
			c.DesktopControlTimings = &DeskControlTimings{UserIdle: 400 * time.Millisecond, ViewerGrace: time.Hour, CDPHold: time.Second}
		})
		const tid = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ac"
		stdin := fakeAgent(t, e.rt, tid)
		e.rt.desk.observeAgentFrame(tid, "agent_start", []byte(`{"type":"agent_start"}`))
		start := time.Now()
		e.rt.desk.takeControl()
		e.wait("user", controlIs(apigen.OrbDesktopControlStateUser, true))
		time.Sleep(250 * time.Millisecond)
		e.rt.desk.takeControl() // 续期
		e.wait("released", controlIs(apigen.OrbDesktopControlStateAgent, false))
		assert.GreaterOrEqual(t, time.Since(start), 600*time.Millisecond, "续期把自动交还推迟到最后一次续期之后")
		// 状态先上报，steer 随后才写入；等通知本身送达，不能把状态上报当成写入屏障。
		assert.Eventually(t, func() bool {
			return slices.Equal([]string{steerUserTook, steerAutoReleased}, stdin.steers(t))
		}, time.Second, 10*time.Millisecond)
	})
	t.Run("viewers", func(t *testing.T) {
		e := newDeskEnv(t, func(c *Config) {
			c.DesktopControlTimings = &DeskControlTimings{UserIdle: time.Hour, ViewerGrace: 300 * time.Millisecond, CDPHold: time.Second}
		})
		e.wait("display", displayIs(true))
		st, rep := openDesk(t, e.cp)
		require.True(t, rep.Ok)
		e.rt.desk.takeControl()
		e.wait("user", controlIs(apigen.OrbDesktopControlStateUser, true))
		time.Sleep(500 * time.Millisecond)
		assert.True(t, e.rt.desk.userHeld(), "仍有观看者时不因宽限自动交还")
		closed := time.Now()
		require.NoError(t, st.Close())
		e.wait("released", controlIs(apigen.OrbDesktopControlStateIdle, false))
		assert.GreaterOrEqual(t, time.Since(closed), 250*time.Millisecond)
	})
}

// cdpConn 经 CDP 前门连上（假）Chromium 的浏览器级 WebSocket。
func cdpConn(t *testing.T, e *deskEnv) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+e.rt.desk.cdpAddr()+"/devtools/browser/fake", &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

type cdpReply struct {
	ID        int             `json:"id"`
	SessionID string          `json:"sessionId"`
	Result    json.RawMessage `json:"result"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func cdpRoundTrip(t *testing.T, c *websocket.Conn, cmd string) (cdpReply, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(cmd)))
	_, b, err := c.Read(ctx)
	require.NoError(t, err)
	var r cdpReply
	require.NoError(t, json.Unmarshal(b, &r), string(b))
	return r, time.Since(start)
}

func TestCDPFrontDoorHold(t *testing.T) {
	e := newDeskEnv(t, func(c *Config) { c.DesktopControlTimings = longTimings() })
	e.wait("display", displayIs(true))
	resp, b := newOrbHTTP(t, e.cp, e.rt).do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	c := cdpConn(t, e)

	r, _ := cdpRoundTrip(t, c, `{"id":1,"method":"Storage.getCookies"}`)
	require.Nil(t, r.Error, "未接管时照常转发（客户端提出的压缩被前门去掉）")

	// 接管：命令挂起至多 CDPHold，之后回 CDP 错误（带原 id 与 sessionId）
	e.rt.desk.takeControl()
	r, took := cdpRoundTrip(t, c, `{"id":2,"sessionId":"S1","method":"Storage.getCookies"}`)
	require.NotNil(t, r.Error)
	assert.Equal(t, 2, r.ID)
	assert.Equal(t, "S1", r.SessionID)
	assert.Equal(t, cdpHeldErrorCode, r.Error.Code)
	assert.Contains(t, r.Error.Message, "用户已接管桌面")
	assert.GreaterOrEqual(t, took, 250*time.Millisecond)
	// 紧接着的命令直接回错误
	r, took = cdpRoundTrip(t, c, `{"id":3,"method":"Storage.getCookies"}`)
	require.NotNil(t, r.Error)
	assert.Less(t, took, 200*time.Millisecond)

	// 挂起中交还：命令照常送达
	time.Sleep(cdpFailFastWindow)
	go func() {
		time.Sleep(100 * time.Millisecond)
		e.rt.desk.releaseControl(false)
	}()
	r, _ = cdpRoundTrip(t, c, `{"id":4,"method":"Storage.getCookies"}`)
	assert.Equal(t, 4, r.ID)
	assert.Nil(t, r.Error)

	// 普通 HTTP：持有期间挂起后回 503，交还后照常
	e.rt.desk.takeControl()
	resp, b = e.cdpGet("/json/version")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, string(b), "用户已接管桌面")
	e.rt.desk.releaseControl(false)
	resp, _ = e.cdpGet("/json/version")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// 真机验证（需要真实 omp）：oryn-orb.ts 在启用桌面时注册 desktop_ask_user；工具的 ctx.ui.select 经 orbd 往返，
// orbd 据此进入 ask 状态，作答后工具返回「完成」的结果、状态回到 idle。ORYN_REAL_OMP=<omp>（如 build/core/x64/omp）时运行。
// 没有模型可用，探针扩展在 session_start 里以真实 ctx 调用该工具的 execute（经代理截获 registerTool）。
func TestRealOmpDesktopAsk(t *testing.T) {
	bin := os.Getenv("ORYN_REAL_OMP")
	if bin == "" {
		t.Skip("set ORYN_REAL_OMP to a real omp binary")
	}
	e := newDeskEnv(t, func(c *Config) {
		c.AgentBin = bin
		c.InitTimeout = 90 * time.Second
		c.RestartDelays = []time.Duration{}
		c.DesktopControlTimings = longTimings()
	})
	e.cp.ctlNow(t)
	probe := filepath.Join(e.rt.home, ".omp", "agent", "extensions", "desktop-ask-probe.ts")
	require.NoError(t, os.MkdirAll(filepath.Dir(probe), 0o755))
	require.NoError(t, os.WriteFile(probe, []byte(`import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import orynOrb from `+strconv.Quote(e.rt.extensionPath())+`;
export default function (pi: ExtensionAPI) {
	let def: any;
	orynOrb(new Proxy(pi, {
		get(target, key) {
			if (key === "registerTool") return (d: any) => { def = d; };
			if (key === "on") return () => {};
			return Reflect.get(target, key);
		},
	}) as ExtensionAPI);
	pi.on("session_start", async (_event, ctx) => {
		const has = pi.getAllTools().some((t) => t.name === "desktop_ask_user");
		ctx.ui.notify("probe registered=" + has, "info");
		const r = await def.execute("probe", { kind: "sign_in", reason: "登录 GitHub" }, undefined, undefined, ctx);
		ctx.ui.notify("probe result=" + r.content[0].text, "info");
	});
}
`), 0o644))
	const thread = "T-realomp-desk"
	startAgent(t, e.cp, thread, nil)
	st := waitAgentStream(t, e.cp)
	fr := readFrames(st)
	frames := fr.frameWith(t, "ask select", func(f json.RawMessage) bool {
		return frameType(f) == "extension_ui_request" && strings.Contains(string(f), "需要你操作桌面（登录）：登录 GitHub")
	})
	var all strings.Builder
	for _, f := range frames {
		all.Write(f)
	}
	assert.Contains(t, all.String(), "probe registered=true", "desktop_ask_user 已注册")
	var req struct {
		ID      string   `json:"id"`
		Options []string `json:"options"`
	}
	require.NoError(t, json.Unmarshal(frames[len(frames)-1], &req))
	assert.Equal(t, []string{"完成", "跳过"}, req.Options)
	s := e.wait("ask", controlIs(apigen.OrbDesktopControlStateAsk, false))
	assert.Equal(t, req.ID, controlOf(s).Ask.RequestID)
	assert.Equal(t, apigen.OrbDesktopAskKindSignIn, controlOf(s).Ask.Kind)

	sendCmd(t, st, fmt.Sprintf(`{"type":"extension_ui_response","id":%q,"value":"完成"}`, req.ID))
	fr.frameWith(t, "tool result", func(f json.RawMessage) bool {
		return strings.Contains(string(f), "probe result=用户表示已在桌面上完成")
	})
	e.wait("idle", controlIs(apigen.OrbDesktopControlStateIdle, false))
}
