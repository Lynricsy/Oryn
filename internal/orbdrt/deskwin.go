package orbdrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 窗口监视（M25 步骤 1.4）：显示就绪后经 X11（纯 Go 的 xgb）监视窗口管理器的 EWMH 属性，
// 把顶层窗口列表放进 desktop.state 快照的 windows；只有窗口变化的上报合并，至少间隔 winReportGap。

const (
	// winTitleMax 是快照里窗口标题的最大字符数（按 rune 计）。
	winTitleMax = 200
	// winListMax 是快照里最多的窗口数（按 _NET_CLIENT_LIST 顺序取前这么多个）。
	winListMax = 32
	// winReportGap 是只有窗口变化时两次上报的最小间隔。
	winReportGap = 500 * time.Millisecond
	// wmRetryDelay 是 Watch 出错（连不上 X、连接断开）后重连前的等待。
	wmRetryDelay = 2 * time.Second
)

// winInfo 是窗口管理器报告的一个顶层窗口。
type winInfo struct {
	id        uint32
	class     []string // WM_CLASS：instance、class
	title     string
	active    bool
	minimized bool
	pid       int // _NET_WM_PID；没有为 0
}

// wmClient 是窗口管理器的客户端：Watch 阻塞到 ctx 结束或 X 连接断开，每次窗口列表变化时回调 onChange
// （首次连上也回调一次）；三个操作经 EWMH ClientMessage 请窗口管理器执行。
// WaitManaged 是显示就绪前的闸门：阻塞到窗口管理器确实接管了一个探针窗口，ctx 结束时返回错误。
type wmClient interface {
	Watch(ctx context.Context, onChange func([]winInfo)) error
	WaitManaged(ctx context.Context) error
	Activate(id uint32) error
	Minimize(id uint32) error
	Close(id uint32) error
}

// ---- desktop 侧 ----

// xSocket 是显示 desktopDisplay 的 X unix 套接字（与 cleanStale 清理的是同一个）。
func (d *desktop) xSocket() string {
	return filepath.Join(d.tmpDir, ".X11-unix", "X"+strings.TrimPrefix(desktopDisplay, ":"))
}

// watchWindows 在 ctx 结束前持续监视窗口；Watch 出错时记一行日志，等 wmRetryDelay 后重连。
func (d *desktop) watchWindows(ctx context.Context, wm wmClient) {
	for {
		err := wm.Watch(ctx, d.setWins)
		if ctx.Err() != nil {
			return
		}
		d.log.Warn("window watch failed; retrying", "err", err, "delay", wmRetryDelay)
		t := time.NewTimer(wmRetryDelay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}

// setWins 记下最新的窗口列表并上报；距上一次窗口上报不足 winReportGap 时推迟到期满再报（期间的变化合并）。
func (d *desktop) setWins(ws []winInfo) {
	d.mu.Lock()
	d.wins = ws
	if d.winsTimer != nil { // 已排定，到期时带上最新列表
		d.mu.Unlock()
		return
	}
	if wait := winReportGap - time.Since(d.winsAt); wait > 0 {
		d.winsTimer = time.AfterFunc(wait, d.flushWins)
		d.mu.Unlock()
		return
	}
	d.winsAt = time.Now()
	d.unlockAndEmit()
}

// flushWins 是推迟的窗口上报。
func (d *desktop) flushWins() {
	d.mu.Lock()
	d.winsTimer = nil
	d.winsAt = time.Now()
	d.unlockAndEmit()
}

// windowsLocked 把 wins 转成快照字段：按 _NET_CLIENT_LIST 顺序，至多 winListMax 个，标题截断到 winTitleMax。
func (d *desktop) windowsLocked() []apigen.OrbDesktopWindow {
	if len(d.wins) == 0 {
		return nil
	}
	ws := d.wins[:min(len(d.wins), winListMax)]
	out := make([]apigen.OrbDesktopWindow, len(ws))
	for i, w := range ws {
		out[i] = apigen.OrbDesktopWindow{
			ID:        strconv.FormatUint(uint64(w.id), 10),
			Title:     truncRunes(w.title, winTitleMax),
			Active:    w.active,
			Minimized: w.minimized,
		}
		if d.appOf != nil {
			if app := d.appOf(w.class); app != "" {
				out[i].AppID = new(app)
			}
		}
	}
	return out
}

// truncRunes 把 s 截到至多 n 个字符。
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// ---- xgb 实现 ----

func init() {
	// xgb 的包级日志默认写 stderr（每次连接都抱怨找不到 Xauthority）；错误都经返回值处理，这里丢弃。
	xgb.Logger = log.New(io.Discard, "", 0)
}

// ICCCM 的 WithdrawnState 与 IconicState（WM_STATE 的值、WM_CHANGE_STATE 的参数）。
const (
	withdrawnState = 0
	iconicState    = 3
)

// wmGateRemap 是闸门里探针窗口每次映射后等 WM_STATE 的时长；到期没出现就取消映射再重新映射。
const wmGateRemap = 200 * time.Millisecond

// EWMH ClientMessage 的 source indication：2 = 来自分页器 / 任务栏（用户的直接操作）。
const ewmhSourcePager = 2

// xAtomNames 是要用到的原子（WM_NAME、WM_CLASS 是预定义原子，不在这里）。
var xAtomNames = []string{
	"_NET_CLIENT_LIST", "_NET_ACTIVE_WINDOW", "_NET_WM_NAME", "_NET_WM_STATE",
	"_NET_WM_STATE_HIDDEN", "_NET_WM_PID", "_NET_CLOSE_WINDOW", "WM_CHANGE_STATE", "UTF8_STRING",
}

type xAtoms struct {
	clientList, activeWindow, wmName, wmState, stateHidden, wmPid, closeWindow, changeState, utf8 xproto.Atom
}

// xwm 是 wmClient 的 xgb 实现。连接随 Watch 建立和关闭；操作复用 Watch 的连接，没在监视时返回错误。
type xwm struct {
	sock string

	mu    sync.Mutex
	conn  *xgb.Conn
	root  xproto.Window
	atoms xAtoms
}

var errWMNotConnected = errors.New("window manager: not connected")

// newXWM 返回连接 X unix 套接字 sock（如 /tmp/.X11-unix/X1）的 wmClient。
func newXWM(sock string) wmClient { return &xwm{sock: sock} }

func (w *xwm) dial() (*xgb.Conn, error) {
	nc, err := net.DialTimeout("unix", w.sock, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect X %s: %w", w.sock, err)
	}
	c, err := xgb.NewConnNet(nc)
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("X handshake %s: %w", w.sock, err)
	}
	return c, nil
}

func internAtoms(c *xgb.Conn) (xAtoms, error) {
	cookies := make([]xproto.InternAtomCookie, len(xAtomNames))
	for i, n := range xAtomNames {
		cookies[i] = xproto.InternAtom(c, false, uint16(len(n)), n)
	}
	ids := make([]xproto.Atom, len(xAtomNames))
	for i, ck := range cookies {
		r, err := ck.Reply()
		if err != nil {
			return xAtoms{}, fmt.Errorf("intern atom %s: %w", xAtomNames[i], err)
		}
		ids[i] = r.Atom
	}
	return xAtoms{
		clientList: ids[0], activeWindow: ids[1], wmName: ids[2], wmState: ids[3],
		stateHidden: ids[4], wmPid: ids[5], closeWindow: ids[6], changeState: ids[7], utf8: ids[8],
	}, nil
}

// Watch 订阅根窗口与各客户窗口的 PropertyChange，相关属性一变就重读整个列表，与上次不同才回调。
// ctx 结束时关闭连接并返回 ctx.Err()；X 连接断开时返回错误。
func (w *xwm) Watch(ctx context.Context, onChange func([]winInfo)) error {
	c, err := w.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, c.Close)
	defer stop()

	atoms, err := internAtoms(c)
	if err != nil {
		return err
	}
	root := xproto.Setup(c).DefaultScreen(c).Root
	if err := xproto.ChangeWindowAttributesChecked(c, root, xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		return fmt.Errorf("select root PropertyChange: %w", err)
	}
	w.mu.Lock()
	w.conn, w.root, w.atoms = c, root, atoms
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		if w.conn == c {
			w.conn = nil
		}
		w.mu.Unlock()
	}()

	relevant := map[xproto.Atom]bool{
		atoms.clientList: true, atoms.activeWindow: true, atoms.wmName: true, xproto.AtomWmName: true,
		atoms.wmState: true, xproto.AtomWmClass: true, atoms.wmPid: true,
	}
	// 事件在单独的 goroutine 里读：主循环发属性请求、等应答时，事件不会把 xgb 的事件队列塞满而卡住应答。
	dirty := make(chan struct{}, 1)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			ev, xerr := c.WaitForEvent()
			if ev == nil && xerr == nil {
				return // 连接已关闭
			}
			// xerr 是异步 X 错误：给已销毁的客户窗口订阅事件时的 BadWindow，忽略。
			if e, ok := ev.(xproto.PropertyNotifyEvent); ok && relevant[e.Atom] {
				select {
				case dirty <- struct{}{}:
				default:
				}
			}
		}
	}()

	watched := map[xproto.Window]bool{}
	var last []winInfo
	for first := true; ; first = false {
		ws, err := readWindows(c, root, atoms, watched)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if first || !winsEqual(last, ws) {
			last = ws
			onChange(slices.Clone(ws))
		}
		select {
		case <-dirty:
		case <-closed:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("window manager: X connection closed")
		}
	}
}

// readWindows 读 _NET_CLIENT_LIST 与 _NET_ACTIVE_WINDOW，再批量读每个客户窗口的属性。
// 读属性时 BadWindow（窗口刚销毁）的窗口跳过：列表随后的变化会把它去掉。新出现的窗口订阅 PropertyChange。
func readWindows(c *xgb.Conn, root xproto.Window, a xAtoms, watched map[xproto.Window]bool) ([]winInfo, error) {
	listCk := xproto.GetProperty(c, false, root, a.clientList, xproto.AtomWindow, 0, 4096)
	activeCk := xproto.GetProperty(c, false, root, a.activeWindow, xproto.AtomWindow, 0, 1)
	lr, err := listCk.Reply()
	if err != nil {
		return nil, fmt.Errorf("read _NET_CLIENT_LIST: %w", err)
	}
	ar, err := activeCk.Reply()
	if err != nil {
		return nil, fmt.Errorf("read _NET_ACTIVE_WINDOW: %w", err)
	}
	ids := prop32(lr)
	var active uint32
	if v := prop32(ar); len(v) > 0 {
		active = v[0]
	}

	type cookies struct{ netName, name, state, class, pid xproto.GetPropertyCookie }
	cks := make([]cookies, len(ids))
	seen := make(map[xproto.Window]bool, len(ids))
	for i, id := range ids {
		win := xproto.Window(id)
		seen[win] = true
		if !watched[win] {
			watched[win] = true
			xproto.ChangeWindowAttributes(c, win, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
		}
		cks[i] = cookies{
			netName: xproto.GetProperty(c, false, win, a.wmName, a.utf8, 0, 1024),
			name:    xproto.GetProperty(c, false, win, xproto.AtomWmName, xproto.GetPropertyTypeAny, 0, 1024),
			state:   xproto.GetProperty(c, false, win, a.wmState, xproto.AtomAtom, 0, 64),
			class:   xproto.GetProperty(c, false, win, xproto.AtomWmClass, xproto.AtomString, 0, 256),
			pid:     xproto.GetProperty(c, false, win, a.wmPid, xproto.AtomCardinal, 0, 1),
		}
	}
	for win := range watched {
		if !seen[win] {
			delete(watched, win)
		}
	}

	out := make([]winInfo, 0, len(ids))
	for i, id := range ids {
		ck := cks[i]
		netName, err1 := ck.netName.Reply()
		name, err2 := ck.name.Reply()
		state, err3 := ck.state.Reply()
		class, err4 := ck.class.Reply()
		pid, err5 := ck.pid.Reply()
		if err := errors.Join(err1, err2, err3, err4, err5); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("read window properties: %w", err)
			}
			continue // BadWindow 等：窗口已销毁
		}
		wi := winInfo{id: id, active: id == active}
		if t := propBytes(netName); len(t) > 0 {
			wi.title = strings.ToValidUTF8(string(t), "\uFFFD")
		} else {
			wi.title = latin1(propBytes(name))
		}
		wi.minimized = slices.Contains(prop32(state), uint32(a.stateHidden))
		for s := range strings.SplitSeq(string(propBytes(class)), "\x00") {
			if s != "" {
				wi.class = append(wi.class, s)
			}
		}
		if v := prop32(pid); len(v) > 0 {
			wi.pid = int(v[0])
		}
		out = append(out, wi)
	}
	return out, nil
}

// propBytes 返回 8 位属性的值；属性不存在时为 nil。
func propBytes(r *xproto.GetPropertyReply) []byte {
	if r == nil || r.Format != 8 {
		return nil
	}
	return r.Value[:min(int(r.ValueLen), len(r.Value))]
}

// prop32 返回 32 位属性的值（WINDOW、ATOM、CARDINAL 列表）；属性不存在时为 nil。
func prop32(r *xproto.GetPropertyReply) []uint32 {
	if r == nil || r.Format != 32 {
		return nil
	}
	n := min(int(r.ValueLen), len(r.Value)/4)
	out := make([]uint32, n)
	for i := range out {
		out[i] = xgb.Get32(r.Value[i*4:])
	}
	return out
}

// latin1 把 WM_NAME（STRING 类型，ISO 8859-1）转成 UTF-8；已经是合法 UTF-8 的原样返回。
func latin1(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func winsEqual(a, b []winInfo) bool {
	return slices.EqualFunc(a, b, func(x, y winInfo) bool {
		return x.id == y.id && x.title == y.title && x.active == y.active && x.minimized == y.minimized &&
			x.pid == y.pid && slices.Equal(x.class, y.class)
	})
}

// clientMessage 向根窗口发一条关于窗口 id 的 EWMH / ICCCM ClientMessage。
func (w *xwm) clientMessage(id uint32, typ func(xAtoms) xproto.Atom, data ...uint32) error {
	w.mu.Lock()
	c, root, a := w.conn, w.root, w.atoms
	w.mu.Unlock()
	if c == nil {
		return errWMNotConnected
	}
	var d [5]uint32
	copy(d[:], data)
	ev := xproto.ClientMessageEvent{
		Format: 32,
		Window: xproto.Window(id),
		Type:   typ(a),
		Data:   xproto.ClientMessageDataUnionData32New(d[:]),
	}
	mask := uint32(xproto.EventMaskSubstructureNotify | xproto.EventMaskSubstructureRedirect)
	if err := xproto.SendEventChecked(c, false, root, mask, string(ev.Bytes())).Check(); err != nil {
		return fmt.Errorf("send client message to window %d: %w", id, err)
	}
	return nil
}

// WaitManaged 在独立连接上建一个 1×1 的探针窗口并映射，等窗口管理器给它设上 WM_STATE（说明它已接管）：
// openbox 设好 _NET_SUPPORTING_WM_CHECK 之后的几百毫秒里会丢 MapRequest，被丢的窗口永远不会被管理，
// 所以每 wmGateRemap 没等到就取消映射再重新映射。返回前销毁探针（关闭连接也会销毁它）。
func (w *xwm) WaitManaged(ctx context.Context) error {
	c, err := w.dial()
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, c.Close)
	defer stop()
	fail := func(what string, err error) error {
		if ctx.Err() != nil {
			return fmt.Errorf("window manager did not manage the probe window: %w", ctx.Err())
		}
		return fmt.Errorf("window manager probe: %s: %w", what, err)
	}

	const wmStateName = "WM_STATE"
	st, err := xproto.InternAtom(c, false, uint16(len(wmStateName)), wmStateName).Reply()
	if err != nil {
		return fail("intern WM_STATE", err)
	}
	scr := xproto.Setup(c).DefaultScreen(c)
	wid, err := xproto.NewWindowId(c)
	if err != nil {
		return fail("allocate window id", err)
	}
	if err := xproto.CreateWindowChecked(c, scr.RootDepth, wid, scr.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, scr.RootVisual, 0, nil).Check(); err != nil {
		return fail("create window", err)
	}
	defer func() { _ = xproto.DestroyWindowChecked(c, wid).Check() }()
	class := "oryn-probe\x00OrynProbe\x00"
	if err := xproto.ChangePropertyChecked(c, xproto.PropModeReplace, wid, xproto.AtomWmClass, xproto.AtomString,
		8, uint32(len(class)), []byte(class)).Check(); err != nil {
		return fail("set WM_CLASS", err)
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := xproto.MapWindowChecked(c, wid).Check(); err != nil {
			return fail("map window", err)
		}
		remap := time.Now().Add(wmGateRemap)
		for time.Now().Before(remap) {
			r, err := xproto.GetProperty(c, false, wid, st.Atom, xproto.GetPropertyTypeAny, 0, 2).Reply()
			if err != nil {
				return fail("read WM_STATE", err)
			}
			if v := prop32(r); len(v) > 0 && v[0] != withdrawnState {
				return nil
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				return fail("wait", ctx.Err())
			}
		}
		if err := xproto.UnmapWindowChecked(c, wid).Check(); err != nil {
			return fail("unmap window", err)
		}
	}
}

// Activate 发 _NET_ACTIVE_WINDOW（source = 分页器）：窗口管理器把窗口切到前台，最小化的先还原。
func (w *xwm) Activate(id uint32) error {
	return w.clientMessage(id, func(a xAtoms) xproto.Atom { return a.activeWindow }, ewmhSourcePager, xproto.TimeCurrentTime)
}

// Minimize 发 WM_CHANGE_STATE（IconicState）。
func (w *xwm) Minimize(id uint32) error {
	return w.clientMessage(id, func(a xAtoms) xproto.Atom { return a.changeState }, iconicState)
}

// Close 发 _NET_CLOSE_WINDOW：窗口管理器请窗口关闭（不支持 WM_DELETE_WINDOW 的客户会被断开连接）。
func (w *xwm) Close(id uint32) error {
	return w.clientMessage(id, func(a xAtoms) xproto.Atom { return a.closeWindow }, xproto.TimeCurrentTime, ewmhSourcePager)
}
