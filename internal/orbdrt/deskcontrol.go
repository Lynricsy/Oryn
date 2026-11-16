package orbdrt

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 桌面控制状态（docs/protocol.md「桌面控制状态」）：orbd 是唯一权威，状态随 desktop.state 上报，
// 所有观看者（任意设备）与 `oryn orb desktop status` 看到的是同一份。
//
//	ask   有待答的 desktop_ask_user 请求（oryn-orb.ts 注册的工具，经 ctx.ui.select 阻塞）；
//	user  用户经 POST /desktop/control/take 接管；
//	agent 本 orb 上有线程处于回合中（见 agent_start / agent_end），用户未接管；
//	idle  其余。
//
// 用户持有控制（userHeld）期间 CDP 前门挂起 agent 的浏览器命令（cdpfront.go）；接管与交还时若有线程在工作，
// 经 steer 通知 agent。computer 工具直接作用于 X 显示、无法拦截，steer 是它唯一的约束手段。

// DeskControlTimings 是桌面控制的时限。
type DeskControlTimings struct {
	// UserIdle：接管后这么久没有续期即自动交还。Web 在用户有输入时至多每 30s 续期一次，因此它衡量的是
	// 「用户离开了多久」。10 分钟足够完成登录、二次验证、支付确认这类人工步骤（通常 1–3 分钟），
	// 又不至于让忘记交还的接管把 agent 长时间挡在门外。
	UserIdle time.Duration
	// ViewerGrace：所有桌面观看者断开满这么久即自动交还。60s 覆盖页面刷新、网络抖动与移动端短暂切到后台。
	ViewerGrace time.Duration
	// CDPHold：用户持有控制时 CDP 前门挂起单条命令的上限。45s 低于常见 CDP 客户端的协议超时（Puppeteer 180s），
	// agent 因此拿到的是一条说明原因的错误，而不是含糊的超时。
	CDPHold time.Duration
}

var defaultDeskControlTimings = DeskControlTimings{UserIdle: 10 * time.Minute, ViewerGrace: 60 * time.Second, CDPHold: 45 * time.Second}

// desktop_ask_user 的请求格式（oryn-orb.ts、web/src/lib/desktop-ask.ts 与此处三处一致，见 docs/protocol.md）：
// 标题「需要你操作桌面（<事项>）：<原因>」（other 不带括号），选项恰为「完成」「跳过」。
const (
	askOptionDone = "完成"
	askOptionSkip = "跳过"
	// askReasonMax 是上报原因的上限（按字符）。
	askReasonMax = 500
)

var askTitleRe = regexp.MustCompile(`^需要你操作桌面(?:（(登录|输入验证码|人工验证)）)?：([\s\S]+)$`)

var askKindByLabel = map[string]apigen.OrbDesktopAskKind{
	"":      apigen.OrbDesktopAskKindOther,
	"登录":    apigen.OrbDesktopAskKindSignIn,
	"输入验证码": apigen.OrbDesktopAskKindCode,
	"人工验证":  apigen.OrbDesktopAskKindCheck,
}

// parseDesktopAsk 识别 desktop_ask_user 发出的 select 请求。
func parseDesktopAsk(title string, options []string) (apigen.OrbDesktopAskKind, string, bool) {
	if !slices.Equal(options, []string{askOptionDone, askOptionSkip}) {
		return "", "", false
	}
	m := askTitleRe.FindStringSubmatch(title)
	if m == nil {
		return "", "", false
	}
	reason := m[2]
	if utf8.RuneCountInString(reason) > askReasonMax {
		reason = string([]rune(reason)[:askReasonMax])
	}
	return askKindByLabel[m[1]], reason, true
}

// 发给 agent 的 steer 文本。
const (
	steerUserTook = "[Oryn] 用户接管了桌面（Desktop 面板）。在用户交还之前不要操作桌面：browser 工具的命令会被挂起，" +
		"最多等 45 秒后报错；computer 工具与其它 GUI 操作不会被拦截，也不要使用。可以先做与桌面无关的工作，或等用户交还后再继续。"
	steerUserReleased = "[Oryn] 用户交还了桌面控制，可以继续操作桌面。用户可能改动过页面，继续之前先确认当前状态。"
	steerAutoReleased = "[Oryn] 用户的桌面接管因长时间无操作或观看者全部断开而自动结束，可以继续操作桌面。用户可能改动过页面，继续之前先确认当前状态。"
)

type deskAsk struct {
	seq int
	v   apigen.OrbDesktopAsk
}

// deskControl 是控制状态的原始数据，受 desktop.mu 保护。
type deskControl struct {
	userHeld      bool
	since         time.Time // 接管时间
	renewed       time.Time // 最近一次接管或续期
	viewers       int
	noViewerSince time.Time // viewers 降到 0 的时间
	working       map[string]bool
	asks          map[string]deskAsk // requestId →
	seq           int
	timer         *time.Timer
	gen           int // 自动交还计时器的代数：旧计时器触发时直接返回
}

// controlLocked 返回控制状态的对外形态。调用方持有 mu。
func (d *desktop) controlLocked() apigen.OrbDesktopControl {
	c := &d.ctl
	out := apigen.OrbDesktopControl{State: apigen.OrbDesktopControlStateIdle, UserHeld: c.userHeld}
	if c.userHeld {
		out.UserSince = new(c.since.UnixMilli())
	}
	switch {
	case len(c.asks) > 0:
		out.State = apigen.OrbDesktopControlStateAsk
		var first *deskAsk
		for _, a := range c.asks {
			if first == nil || a.seq < first.seq {
				first = &a
			}
		}
		out.Ask = new(first.v)
	case c.userHeld:
		out.State = apigen.OrbDesktopControlStateUser
	case len(c.working) > 0:
		out.State = apigen.OrbDesktopControlStateAgent
	}
	return out
}

// workingLocked 返回正在工作的线程（按 id 排序，steer 顺序稳定）。调用方持有 mu。
func (d *desktop) workingLocked() []string {
	return slices.Sorted(maps.Keys(d.ctl.working))
}

// takeControl 由用户接管桌面；已接管时只续期。新接管时 steer 正在工作的线程。
func (d *desktop) takeControl() {
	d.mu.Lock()
	now := time.Now()
	c := &d.ctl
	var steer []string
	if !c.userHeld {
		c.userHeld, c.since = true, now
		steer = d.workingLocked()
		d.bumpLocked()
	}
	c.renewed = now
	d.armControlTimerLocked(now)
	d.unlockAndEmit()
	d.steer(steer, steerUserTook)
}

// releaseControl 交还控制（auto 为自动交还）；未持有时无操作。交还时 steer 仍在工作的线程。
func (d *desktop) releaseControl(auto bool) {
	d.mu.Lock()
	if !d.ctl.userHeld {
		d.mu.Unlock()
		return
	}
	steer := d.releaseLocked()
	d.unlockAndEmit()
	msg := steerUserReleased
	if auto {
		msg = steerAutoReleased
	}
	d.steer(steer, msg)
}

// releaseLocked 清除用户控制并停掉计时器，返回要 steer 的线程。调用方持有 mu。
func (d *desktop) releaseLocked() []string {
	c := &d.ctl
	c.userHeld, c.since = false, time.Time{}
	c.gen++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	d.bumpLocked()
	return d.workingLocked()
}

// controlDeadlineLocked 返回自动交还的时间：最近续期 + UserIdle；没有观看者时另取
// max(最后一个观看者断开, 接管时间) + ViewerGrace 中较早者。调用方持有 mu。
func (d *desktop) controlDeadlineLocked() time.Time {
	c := &d.ctl
	dl := c.renewed.Add(d.ctlT.UserIdle)
	if c.viewers == 0 {
		base := c.noViewerSince
		if c.since.After(base) {
			base = c.since
		}
		if g := base.Add(d.ctlT.ViewerGrace); g.Before(dl) {
			dl = g
		}
	}
	return dl
}

// armControlTimerLocked 按当前条件重设自动交还计时器。调用方持有 mu。
func (d *desktop) armControlTimerLocked(now time.Time) {
	c := &d.ctl
	c.gen++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if !c.userHeld {
		return
	}
	gen := c.gen
	c.timer = time.AfterFunc(d.controlDeadlineLocked().Sub(now), func() { d.controlTimerFired(gen) })
}

func (d *desktop) controlTimerFired(gen int) {
	d.mu.Lock()
	c := &d.ctl
	if gen != c.gen || !c.userHeld {
		d.mu.Unlock()
		return
	}
	if now := time.Now(); now.Before(d.controlDeadlineLocked()) {
		d.armControlTimerLocked(now)
		d.mu.Unlock()
		return
	}
	d.log.Info("desktop control released automatically", "viewers", c.viewers)
	steer := d.releaseLocked()
	d.unlockAndEmit()
	d.steer(steer, steerAutoReleased)
}

// viewerJoined / viewerLeft 由 desk 流在对接 Xvnc 前后调用。
func (d *desktop) viewerJoined() {
	d.mu.Lock()
	d.ctl.viewers++
	d.armControlTimerLocked(time.Now())
	d.mu.Unlock()
}

func (d *desktop) viewerLeft() {
	d.mu.Lock()
	now := time.Now()
	d.ctl.viewers--
	if d.ctl.viewers == 0 {
		d.ctl.noViewerSince = now
	}
	d.armControlTimerLocked(now)
	d.mu.Unlock()
}

// waitUserRelease 在用户持有控制时等待交还，至多 max；返回是否已不再持有。
func (d *desktop) waitUserRelease(ctx context.Context, max time.Duration) bool {
	tm := time.NewTimer(max)
	defer tm.Stop()
	for {
		d.mu.Lock()
		held, ch := d.ctl.userHeld, d.changed
		d.mu.Unlock()
		if !held {
			return true
		}
		select {
		case <-ch:
		case <-tm.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (d *desktop) userHeld() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ctl.userHeld
}

// ---- agent 帧观察 ----

// agentFrameHead 是控制状态关心的 omp 帧字段。
type agentFrameHead struct {
	ID         string   `json:"id"`
	Method     string   `json:"method"`
	Title      string   `json:"title"`
	Options    []string `json:"options"`
	TargetID   string   `json:"targetId"`
	IsTerminal *bool    `json:"isTerminal"`
}

// observeAgentFrame 处理 omp → 控制面的帧：agent_start / agent_end 维护「工作中」，
// desktop_ask_user 的 select 请求登记为待答，cancel 撤销。其余类型不解析。
func (d *desktop) observeAgentFrame(threadID, typ string, frame []byte) {
	switch typ {
	case "agent_start", "agent_end", "extension_ui_request":
	default:
		return
	}
	var h agentFrameHead
	if json.Unmarshal(frame, &h) != nil {
		return
	}
	d.mu.Lock()
	c := &d.ctl
	before := d.controlLocked()
	switch typ {
	case "agent_start":
		if c.working == nil {
			c.working = map[string]bool{}
		}
		c.working[threadID] = true
	case "agent_end":
		// 与控制面 threads/live.go 一致：isTerminal=false 的 agent_end 不结束回合
		if h.IsTerminal == nil || *h.IsTerminal {
			delete(c.working, threadID)
		}
	case "extension_ui_request":
		switch {
		case h.Method == "cancel" && h.TargetID != "":
			delete(c.asks, h.TargetID)
		case h.Method == "select" && h.ID != "":
			kind, reason, ok := parseDesktopAsk(h.Title, h.Options)
			if !ok {
				break
			}
			if c.asks == nil {
				c.asks = map[string]deskAsk{}
			}
			c.seq++
			c.asks[h.ID] = deskAsk{seq: c.seq, v: apigen.OrbDesktopAsk{ThreadID: threadID, RequestID: h.ID, Kind: kind, Reason: reason}}
		}
	}
	d.finishObserveLocked(before)
}

// observeAgentCommand 处理控制面 → omp 的 extension_ui_response：对应的待答请求已作答。
func (d *desktop) observeAgentCommand(line []byte) {
	var h struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(line, &h) != nil || h.ID == "" {
		return
	}
	d.mu.Lock()
	before := d.controlLocked()
	delete(d.ctl.asks, h.ID)
	d.finishObserveLocked(before)
}

// agentExited 在线程的 omp 进程退出后清除它的工作状态与待答请求。
func (d *desktop) agentExited(threadID string) {
	d.mu.Lock()
	before := d.controlLocked()
	delete(d.ctl.working, threadID)
	for id, a := range d.ctl.asks {
		if a.v.ThreadID == threadID {
			delete(d.ctl.asks, id)
		}
	}
	d.finishObserveLocked(before)
}

// finishObserveLocked 在控制状态有变化时唤醒等待者并上报，否则只释放 mu。调用方持有 mu。
func (d *desktop) finishObserveLocked(before apigen.OrbDesktopControl) {
	after := d.controlLocked()
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if string(b1) == string(b2) {
		d.mu.Unlock()
		return
	}
	d.bumpLocked()
	d.unlockAndEmit()
}

// ---- steer ----

var steerSeq atomic.Uint64

// steer 向各线程当前的 omp 进程写一条 steer 命令（id 带 orbd 前缀，应答由 readStdout 在本地吞掉）。
func (d *desktop) steer(threadIDs []string, msg string) {
	for _, id := range threadIDs {
		d.r.threadsMu.Lock()
		t := d.r.threads[id]
		d.r.threadsMu.Unlock()
		if t == nil {
			continue
		}
		t.mu.Lock()
		p := t.proc
		t.mu.Unlock()
		if p == nil || !p.isReady() {
			continue
		}
		frame, _ := json.Marshal(map[string]string{
			"type": "steer", "id": fmt.Sprintf("%sdesk-%d", initIDPrefix, steerSeq.Add(1)), "message": msg,
		})
		if err := p.w.WriteRaw(frame); err != nil {
			d.log.Debug("steer agent failed", "thread", id, "err", err)
		}
	}
}
