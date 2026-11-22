package orbdrt

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// fakeWM 是测试用的 wmClient：Watch 记下回调并阻塞到 ctx 结束；push 模拟窗口管理器报告新列表。
type fakeWM struct {
	mu       sync.Mutex
	onChange func([]winInfo)
	watching chan struct{} // 每次 Watch 开始时发一个信号
}

func newFakeWM() *fakeWM { return &fakeWM{watching: make(chan struct{}, 4)} }

func (f *fakeWM) Watch(ctx context.Context, onChange func([]winInfo)) error {
	f.mu.Lock()
	f.onChange = onChange
	f.mu.Unlock()
	f.watching <- struct{}{}
	<-ctx.Done()
	f.mu.Lock()
	f.onChange = nil
	f.mu.Unlock()
	return ctx.Err()
}

func (f *fakeWM) push(ws []winInfo) {
	f.mu.Lock()
	cb := f.onChange
	f.mu.Unlock()
	if cb != nil {
		cb(ws)
	}
}

func (f *fakeWM) Activate(uint32) error { return nil }
func (f *fakeWM) Minimize(uint32) error { return nil }
func (f *fakeWM) Close(uint32) error    { return nil }

func windowsLen(n int) func(apigen.LinkCtlDesktopStateParams) bool {
	return func(s apigen.LinkCtlDesktopStateParams) bool { return len(s.Windows) == n }
}

func TestDeskWinFake(t *testing.T) {
	fw := newFakeWM()
	e := newDeskEnv(t, func(c *Config) {
		c.DesktopWM = func(string) wmClient { return fw }
	})
	e.wait("display ready", displayIs(true))
	<-fw.watching
	e.rt.desk.mu.Lock()
	e.rt.desk.appOf = func(class []string) string {
		if slices.Contains(class, "Mousepad") {
			return "mousepad"
		}
		return ""
	}
	e.rt.desk.mu.Unlock()

	long := strings.Repeat("标", 250)
	fw.push([]winInfo{
		{id: 30, class: []string{"mousepad", "Mousepad"}, title: "笔记", active: true},
		{id: 10, class: []string{"xterm", "XTerm"}, title: long, minimized: true},
		{id: 20, class: []string{"mousepad", "Mousepad"}, title: "草稿"},
	})
	s := e.wait("three windows", windowsLen(3))
	assert.Equal(t, []string{"30", "10", "20"}, []string{s.Windows[0].ID, s.Windows[1].ID, s.Windows[2].ID}, "client list order")
	require.NotNil(t, s.Windows[0].AppID)
	assert.Equal(t, "mousepad", *s.Windows[0].AppID)
	assert.Nil(t, s.Windows[1].AppID, "unmapped class has no appId")
	require.NotNil(t, s.Windows[2].AppID)
	assert.Equal(t, "mousepad", *s.Windows[2].AppID)
	assert.Equal(t, winTitleMax, utf8.RuneCountInString(s.Windows[1].Title))
	assert.True(t, strings.HasPrefix(long, s.Windows[1].Title))
	assert.True(t, s.Windows[0].Active)
	assert.True(t, s.Windows[1].Minimized)

	// 超过 winListMax 个只取前面的。
	many := make([]winInfo, 40)
	for i := range many {
		many[i] = winInfo{id: uint32(100 + i), title: fmt.Sprint(i)}
	}
	fw.push(many)
	s = e.wait("capped windows", windowsLen(winListMax))
	assert.Equal(t, "100", s.Windows[0].ID)
	assert.Equal(t, fmt.Sprint(100+winListMax-1), s.Windows[winListMax-1].ID)

	// 1 秒内 10 次窗口变化，至多 3 次上报；最后一次变化最终会报出去。
	time.Sleep(winReportGap) // 让上一次窗口上报过了间隔
	e.mu.Lock()
	before := len(e.states)
	e.mu.Unlock()
	start := time.Now()
	for i := range 10 {
		fw.push([]winInfo{{id: 1, title: fmt.Sprint("t", i)}})
		time.Sleep(90 * time.Millisecond)
	}
	for time.Since(start) < time.Second {
		time.Sleep(10 * time.Millisecond)
	}
	e.mu.Lock()
	n := len(e.states) - before
	e.mu.Unlock()
	assert.LessOrEqual(t, n, 3, "window-only reports within 1s")
	assert.GreaterOrEqual(t, n, 1)
	e.wait("last change reported", func(s apigen.LinkCtlDesktopStateParams) bool {
		return len(s.Windows) == 1 && s.Windows[0].Title == "t9"
	})

	// 显示失效：窗口清空；新的显示代重新 Watch。
	require.NoError(t, syscall.Kill(e.xvncPid(), syscall.SIGKILL))
	s = e.wait("display lost", displayIs(false))
	assert.Empty(t, s.Windows)
	e.wait("display back", displayIs(true))
	select {
	case <-fw.watching:
	case <-time.After(10 * time.Second):
		t.Fatal("watch not restarted after display restart")
	}
	fw.push([]winInfo{{id: 7, title: "again"}})
	e.wait("window after restart", windowsLen(1))
}

// ---- 真实 X（Xvfb + openbox） ----

// realPath 是去掉 fakedesk 目录（它扮演 openbox 等）之后的 PATH。
func realPath() string {
	var dirs []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d != fakedeskDir {
			dirs = append(dirs, d)
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// realLookPath 在 realPath 里找可执行文件。
func realLookPath(name string) (string, error) {
	for _, d := range filepath.SplitList(realPath()) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

// startXvfb 起 Xvfb :<随机> 与 openbox（等到 openbox 接管根窗口），返回 X 套接字路径与 Xvfb 的 pid。
func startXvfb(t *testing.T) (string, int) {
	t.Helper()
	xvfb, err := realLookPath("Xvfb")
	if err != nil {
		t.Skip("Xvfb not installed")
	}
	openbox, err := realLookPath("openbox")
	if err != nil {
		t.Skip("openbox not installed")
	}
	var n int
	var sock string
	for {
		n = 200 + rand.IntN(700)
		sock = fmt.Sprintf("/tmp/.X11-unix/X%d", n)
		if _, err := os.Stat(sock); os.IsNotExist(err) {
			if _, err := os.Stat(fmt.Sprintf("/tmp/.X%d-lock", n)); os.IsNotExist(err) {
				break
			}
		}
	}
	display := fmt.Sprint(":", n)
	start := func(bin string, args []string, env []string) int {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		require.NoError(t, cmd.Start())
		t.Cleanup(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		})
		return cmd.Process.Pid
	}
	env := []string{"DISPLAY=" + display, "PATH=" + realPath()}
	xpid := start(xvfb, []string{display, "-screen", "0", "1280x800x24", "-nolisten", "tcp"}, env)
	require.Eventually(t, func() bool {
		c, err := net.Dial("unix", sock)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	}, 10*time.Second, 20*time.Millisecond, "Xvfb socket")
	home := t.TempDir()
	start(openbox, nil, append(env, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config")))
	nc, err := net.Dial("unix", sock)
	require.NoError(t, err)
	c, err := xgb.NewConnNet(nc)
	require.NoError(t, err)
	defer c.Close()
	const check = "_NET_SUPPORTING_WM_CHECK"
	atom, err := xproto.InternAtom(c, false, uint16(len(check)), check).Reply()
	require.NoError(t, err)
	root := xproto.Setup(c).DefaultScreen(c).Root
	require.Eventually(t, func() bool {
		r, err := xproto.GetProperty(c, false, root, atom.Atom, xproto.AtomWindow, 0, 1).Reply()
		return err == nil && r.ValueLen > 0
	}, 10*time.Second, 20*time.Millisecond, "openbox manages the root window")
	return sock, xpid
}

// mapTestWindow 在独立的 X 连接上建一个带 WM_CLASS 的顶层窗口并 map（关闭时窗口管理器断开的是这条连接），
// 等到 openbox 接管它（设了 WM_STATE）。openbox 刚起来的几百毫秒里会漏掉 MapRequest（实测），
// 这时窗口既没映射也没被接管：每 300ms 重新 unmap + map 一次。
func mapTestWindow(t *testing.T, sock, title string) uint32 {
	t.Helper()
	nc, err := net.Dial("unix", sock)
	require.NoError(t, err)
	c, err := xgb.NewConnNet(nc)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	scr := xproto.Setup(c).DefaultScreen(c)
	wid, err := xproto.NewWindowId(c)
	require.NoError(t, err)
	require.NoError(t, xproto.CreateWindowChecked(c, scr.RootDepth, wid, scr.Root, 10, 10, 200, 100, 0,
		xproto.WindowClassInputOutput, scr.RootVisual, xproto.CwBackPixel, []uint32{scr.WhitePixel}).Check())
	class := "xmessage\x00Xmessage\x00"
	require.NoError(t, xproto.ChangePropertyChecked(c, xproto.PropModeReplace, wid, xproto.AtomWmClass, xproto.AtomString,
		8, uint32(len(class)), []byte(class)).Check())
	require.NoError(t, xproto.ChangePropertyChecked(c, xproto.PropModeReplace, wid, xproto.AtomWmName, xproto.AtomString,
		8, uint32(len(title)), []byte(title)).Check())
	const wmState = "WM_STATE"
	st, err := xproto.InternAtom(c, false, uint16(len(wmState)), wmState).Reply()
	require.NoError(t, err)
	deadline := time.Now().Add(10 * time.Second)
	for {
		require.NoError(t, xproto.MapWindowChecked(c, wid).Check())
		for range 30 {
			r, err := xproto.GetProperty(c, false, wid, st.Atom, xproto.GetPropertyTypeAny, 0, 2).Reply()
			require.NoError(t, err)
			if r.Format != 0 {
				return uint32(wid)
			}
			time.Sleep(10 * time.Millisecond)
		}
		require.True(t, time.Now().Before(deadline), "openbox did not manage window %q", title)
		require.NoError(t, xproto.UnmapWindowChecked(c, wid).Check())
	}
}

func TestDeskWinRealX(t *testing.T) {
	sock, _ := startXvfb(t)
	wm := newXWM(sock)

	var mu sync.Mutex
	var latest []winInfo
	sig := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	watchErr := make(chan error, 1)
	go func() {
		watchErr <- wm.Watch(ctx, func(ws []winInfo) {
			mu.Lock()
			latest = ws
			mu.Unlock()
			select {
			case sig <- struct{}{}:
			default:
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-watchErr:
			assert.ErrorIs(t, err, context.Canceled)
		case <-time.After(5 * time.Second):
			t.Error("Watch did not return after cancel")
		}
	})
	waitWins := func(what string, pred func([]winInfo) bool) []winInfo {
		t.Helper()
		deadline := time.After(15 * time.Second)
		for {
			mu.Lock()
			ws := latest
			mu.Unlock()
			if pred(ws) {
				return ws
			}
			select {
			case <-sig:
			case err := <-watchErr:
				t.Fatalf("Watch returned while waiting for %s: %v", what, err)
			case <-deadline:
				t.Fatalf("windows %s not reached; last %+v", what, ws)
			}
		}
	}
	find := func(ws []winInfo, id uint32) (winInfo, bool) {
		i := slices.IndexFunc(ws, func(w winInfo) bool { return w.id == id })
		if i < 0 {
			return winInfo{}, false
		}
		return ws[i], true
	}

	w1 := mapTestWindow(t, sock, "first")
	w2 := mapTestWindow(t, sock, "second")
	ws := waitWins("two Xmessage windows", func(ws []winInfo) bool {
		n := 0
		for _, w := range ws {
			if slices.Equal(w.class, []string{"xmessage", "Xmessage"}) {
				n++
			}
		}
		return n == 2
	})
	assert.Equal(t, []uint32{w1, w2}, []uint32{ws[0].id, ws[1].id}, "client list order")
	assert.Equal(t, "first", ws[0].title)

	// Activate 等操作用的是 Watch 的连接：上面已经收到回调，连接已建立。
	require.NoError(t, wm.Minimize(w1))
	waitWins("first minimized", func(ws []winInfo) bool { w, ok := find(ws, w1); return ok && w.minimized })

	// 假设 A2：openbox 处理 _NET_ACTIVE_WINDOW 时把最小化的窗口还原到前台。
	require.NoError(t, wm.Activate(w1))
	waitWins("first active and restored", func(ws []winInfo) bool {
		w, ok := find(ws, w1)
		return ok && w.active && !w.minimized
	})

	require.NoError(t, wm.Close(w1))
	ws = waitWins("one window left", func(ws []winInfo) bool { return len(ws) == 1 })
	assert.Equal(t, w2, ws[0].id)
}

// X 连接断开（Xvfb 退出）时 Watch 返回错误，而不是阻塞或忙等。
func TestDeskWinRealXDisconnect(t *testing.T) {
	sock, xpid := startXvfb(t)
	wm := newXWM(sock)
	got := make(chan struct{}, 1)
	watchErr := make(chan error, 1)
	go func() {
		watchErr <- wm.Watch(context.Background(), func([]winInfo) {
			select {
			case got <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-got:
	case err := <-watchErr:
		t.Fatalf("Watch failed early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no initial window list")
	}
	require.NoError(t, syscall.Kill(xpid, syscall.SIGKILL))
	select {
	case err := <-watchErr:
		require.Error(t, err)
		assert.NotErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not return after X went away")
	}
	assert.ErrorIs(t, wm.Activate(1), errWMNotConnected)
}
