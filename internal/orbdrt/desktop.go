package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 桌面（M8 计划 2.1–2.3、2.8）：常驻的显示栈（Xvnc + openbox）、按需启动且只此一个的 Chromium，
// 以及 desktop.state 上报。Xvnc 只开 unix socket（$HOME/.oryn/vnc.sock），不监听 TCP；
// Chromium 用固定的 user-data-dir（$HOME/.oryn/desktop/profile），DevTools 端口是临时端口，
// 对 agent 只经 CDP 前门（cdpfront.go）暴露。冻结（docker pause）原样保留浏览器；进程退出后不自动重启。

const (
	// DefaultDesktopCDPAddr 是 CDP 前门的默认监听地址。
	DefaultDesktopCDPAddr = "127.0.0.1:7822"

	desktopDisplay  = ":1"
	desktopGeometry = "1280x800"

	displayReadyTimeout  = 15 * time.Second
	displayProbeInterval = 50 * time.Millisecond
	// displayStableAfter：显示栈单次存活这么久之后，重启退避清零。
	displayStableAfter = 60 * time.Second

	// browserStartWait 是「显示就绪 + Profile 已定 + DevTools 就绪」的合计等待。
	browserStartWait = 30 * time.Second
	// browserReadyTimeout 是进程启动后等 DevTools 就绪的上限。
	browserReadyTimeout = 20 * time.Second
	// browserStopGrace 是 SIGTERM 之后到进程组 SIGKILL 的等待。
	browserStopGrace = 10 * time.Second
)

// errNotReady 是显示未就绪或 Profile 未定时 startBrowser 的错误（本机 API 映射为 409 not_ready）。
var errNotReady = errors.New("desktop: display or browser profile not ready")

// desktopBinaries 是启用桌面所需的三个可执行文件（缺任一则桌面整体禁用）。
var desktopBinaries = []string{"Xtigervnc", "openbox", "chromium"}

// defaultDesktopBackoff 是显示栈第 n 次（从 1 起）连续失败后的重启等待：1s、2s、4s、8s、16s、30s 封顶。
func defaultDesktopBackoff(n int) time.Duration {
	d := time.Second << min(n-1, 5)
	return min(d, 30*time.Second)
}

// browserProc 是一个已启动的 Chromium 主进程（同时是进程组组长）。
type browserProc struct {
	pid int
	// exited 在进程被回收、状态已改回 stopped 之后关闭。
	exited chan struct{}
}

type desktop struct {
	r       *Runtime
	log     *slog.Logger
	cdpLn   net.Listener
	tmpDir  string // X 锁与 X socket 所在目录（/tmp）
	backoff func(int) time.Duration
	onState func(apigen.LinkCtlDesktopStateParams)

	// opMu 串行化「启动浏览器进程」「装载」「清空」「导出」与 profile.mode（计划 2.6）。
	opMu sync.Mutex

	mu           sync.Mutex
	displayReady bool
	xvncPid      int
	browser      apigen.OrbBrowserStatus
	proc         *browserProc
	devPort      int
	dirty        bool
	marker       *profileMarker // nil = Profile 未定（还没收到控制面的 PUT / DELETE）
	changed      chan struct{}  // 任何状态变化时关闭并替换

	// emitMu 让 OnDesktopState 回调按状态修改的顺序发生；last 是上一次交出的快照（JSON），相同则不重复交出。
	emitMu sync.Mutex
	last   []byte
	report chan struct{} // 容量 1：唤醒上报 goroutine

	cdpSem chan struct{}

	logsMu sync.Mutex
	logs   map[string]*serviceLog
}

// newDesktop 按配置与可执行文件判定是否启用桌面；未启用返回 nil（hello 不带 desktop，其余行为不变）。
func newDesktop(r *Runtime) *desktop {
	if !r.cfg.Desktop {
		return nil
	}
	for _, b := range desktopBinaries {
		if _, err := exec.LookPath(b); err != nil {
			r.log.Warn("desktop disabled: executable not found", "bin", b)
			return nil
		}
	}
	ln, err := net.Listen("tcp", r.cfg.DesktopCDPAddr)
	if err != nil {
		r.log.Warn("desktop disabled: CDP front door listen failed", "addr", r.cfg.DesktopCDPAddr, "err", err)
		return nil
	}
	d := &desktop{
		r:       r,
		log:     r.log.With("part", "desktop"),
		cdpLn:   ln,
		tmpDir:  r.cfg.DesktopTmpDir,
		backoff: r.cfg.DesktopBackoff,
		onState: r.cfg.OnDesktopState,
		browser: apigen.OrbBrowserStatusStopped,
		changed: make(chan struct{}),
		report:  make(chan struct{}, 1),
		cdpSem:  make(chan struct{}, maxCDPConns),
		logs:    map[string]*serviceLog{},
	}
	if d.tmpDir == "" {
		d.tmpDir = "/tmp"
	}
	if d.backoff == nil {
		d.backoff = defaultDesktopBackoff
	}
	d.loadMarker()
	// 上次装载中断留下的半成品。
	_ = os.RemoveAll(d.profileNewDir())
	return d
}

// ---- 路径 ----

func (d *desktop) dir() string           { return filepath.Join(d.r.home, ".oryn", "desktop") }
func (d *desktop) sockPath() string      { return filepath.Join(d.r.home, ".oryn", "vnc.sock") }
func (d *desktop) profileDir() string    { return filepath.Join(d.dir(), "profile") }
func (d *desktop) profileNewDir() string { return filepath.Join(d.dir(), "profile.new") }
func (d *desktop) markerPath() string    { return filepath.Join(d.dir(), "profile.json") }
func (d *desktop) rcPath() string        { return filepath.Join(d.dir(), "openbox-rc.xml") }
func (d *desktop) cacheDir() string {
	return filepath.Join(d.r.home, ".cache", "oryn", "chromium-cache")
}
func (d *desktop) logPath(n string) string {
	return filepath.Join(d.r.home, ".cache", "oryn", "logs", "desktop", n+".log")
}

// desktopOverlayPath 是给 omp 的设置覆盖层（`--config`）。桌面未启用时它不存在。
func (r *Runtime) desktopOverlayPath() string {
	return filepath.Join(r.home, ".oryn", "core", "desktop.yml")
}

// desktopOverlay 返回覆盖层内容：browser 工具附着到 CDP 前门，启用 computer 工具。
func desktopOverlay(cdpAddr string) []byte {
	return fmt.Appendf(nil, `# 由 orbd 生成：让 omp 的 browser 工具附着到 Oryn 桌面上的 Chromium，并启用 computer 工具。
browser:
  cdpUrl: "http://%s"
computer:
  enabled: true
`, cdpAddr)
}

// cdpAddr 返回 CDP 前门的实际监听地址。
func (d *desktop) cdpAddr() string { return d.cdpLn.Addr().String() }

func (d *desktop) cdpPort() int {
	if a, ok := d.cdpLn.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// devtoolsPort 返回 Chromium 自己的 DevTools 端口；未运行为 0。
func (d *desktop) devtoolsPort() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.browser != apigen.OrbBrowserStatusRunning {
		return 0
	}
	return d.devPort
}

func (d *desktop) isDisplayReady() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.displayReady
}

// ---- 状态与上报 ----

func (d *desktop) bumpLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
}

// snapshot 返回当前的全量桌面状态（desktop.state 的参数）。
func (d *desktop) snapshot() apigen.LinkCtlDesktopStateParams {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

func (d *desktop) snapshotLocked() apigen.LinkCtlDesktopStateParams {
	st := apigen.LinkCtlDesktopStateParams{DisplayReady: d.displayReady, Browser: d.browser, Dirty: d.dirty}
	if m := d.marker; m != nil && m.ProfileID != "" {
		mode := m.access()
		st.ProfileID = new(m.ProfileID)
		st.ProfileMode = new(mode)
		if mode == apigen.BrowserProfileAccessReadonly {
			st.Dirty = false // 只读副本从不回写
			st.ReadonlyReason = new(m.readonlyReason())
			if m.ReadonlyBy != "" {
				st.ReadonlyBy = new(m.ReadonlyBy)
			}
		}
	}
	return st
}

// unlockAndEmit 释放 mu，并在快照与上一次交出的不同时同步回调 OnDesktopState、唤醒上报 goroutine。
// emitMu 在释放 mu 之前取得，因此回调顺序与状态修改顺序一致。调用方持有 mu。
func (d *desktop) unlockAndEmit() {
	d.emitMu.Lock()
	snap := d.snapshotLocked()
	d.mu.Unlock()
	defer d.emitMu.Unlock()
	b, _ := json.Marshal(snap)
	if string(b) == string(d.last) {
		return
	}
	d.last = b
	if d.onState != nil {
		d.onState(snap)
	}
	d.markDirty()
}

func (d *desktop) markDirty() {
	select {
	case d.report <- struct{}{}:
	default:
	}
}

// reportLoop 是 desktop.state 的单个发送 goroutine：每次被唤醒时发最新的全量快照（断连时丢弃，link 建立后补发）。
func (d *desktop) reportLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.report:
		}
		d.r.notify(string(apigen.MethodDesktopState), d.snapshot())
	}
}

// ---- 进程与日志 ----

func (d *desktop) logFor(name string) *serviceLog {
	d.logsMu.Lock()
	defer d.logsMu.Unlock()
	l := d.logs[name]
	if l == nil {
		l = &serviceLog{path: d.logPath(name)}
		d.logs[name] = l
	}
	return l
}

func (d *desktop) closeLogs() {
	d.logsMu.Lock()
	defer d.logsMu.Unlock()
	for _, l := range d.logs {
		l.close()
	}
}

// spawn 以独立进程组启动 bin，输出追加到 `<logName>.log`；返回的 done 在进程被回收后关闭。
func (d *desktop) spawn(logName, bin string, args []string, env map[string]string) (int, <-chan struct{}, error) {
	lg := d.logFor(logName)
	_, _ = fmt.Fprintf(lg, "[orbd] starting %s %s\n", bin, strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	cmd.Dir = d.r.home
	cmd.Env = envList(env)
	cmd.Stdout, cmd.Stderr = lg, lg
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(lg, "[orbd] start failed: %v\n", err)
		return 0, nil, err
	}
	done := make(chan struct{})
	go func() {
		err := cmd.Wait()
		_, _ = fmt.Fprintf(lg, "[orbd] %s exited: %v\n", bin, err)
		close(done)
	}()
	return cmd.Process.Pid, done, nil
}

// killWait 对进程组 SIGKILL 并等它的组长被回收。
func killWait(pid int, done <-chan struct{}) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-done
}

// ---- 显示栈 ----

// run 监督 Xvnc 与 openbox 直到 ctx 结束：任一退出即视为显示失效，杀掉另一个与浏览器，按退避重启。
// ctx 结束时三者的进程组都被 SIGKILL。
func (d *desktop) run(ctx context.Context) {
	defer d.closeLogs()
	defer d.killBrowser()
	failures := 0
	for ctx.Err() == nil {
		start := time.Now()
		d.runDisplayOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= displayStableAfter {
			failures = 0
		}
		failures++
		t := time.NewTimer(d.backoff(failures))
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}

// cleanStale 删除残留的 X 锁、X socket 与 RFB socket：容器停机再启动后 /tmp 仍在而 pid 会被复用，
// 不能靠 X 自己判断锁是否过期；orbd 是它们唯一的管理者。
func (d *desktop) cleanStale() {
	n := strings.TrimPrefix(desktopDisplay, ":")
	for _, p := range []string{
		filepath.Join(d.tmpDir, ".X"+n+"-lock"),
		filepath.Join(d.tmpDir, ".X11-unix", "X"+n),
		d.sockPath(),
	} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			d.log.Warn("remove stale X file", "path", p, "err", err)
		}
	}
}

func (d *desktop) xvncArgs() []string {
	return []string{
		desktopDisplay, "-geometry", desktopGeometry, "-depth", "24",
		"-rfbunixpath", d.sockPath(), "-rfbunixmode", "0600", "-rfbport", "-1",
		"-SecurityTypes", "None", "-AlwaysShared", "-AcceptSetDesktopSize=1",
		"-SendPrimary=0", "-SetPrimary=0", "-desktop", "Oryn", "-nolisten", "tcp",
	}
}

func (d *desktop) runDisplayOnce(ctx context.Context) {
	d.cleanStale()
	env := d.r.baseEnv()
	delete(env, "DISPLAY")
	xpid, xdone, err := d.spawn("xvnc", "Xtigervnc", d.xvncArgs(), env)
	if err != nil {
		d.log.Warn("start Xvnc failed", "err", err)
		return
	}
	defer killWait(xpid, xdone)
	d.mu.Lock()
	d.xvncPid = xpid
	d.mu.Unlock()
	if !d.waitSocket(ctx, xdone) {
		if ctx.Err() == nil {
			d.log.Warn("Xvnc did not become ready", "timeout", displayReadyTimeout)
		}
		return
	}
	env = d.r.baseEnv()
	env["DISPLAY"] = desktopDisplay
	opid, odone, err := d.spawn("openbox", "openbox", []string{"--config-file", d.rcPath()}, env)
	if err != nil {
		d.log.Warn("start openbox failed", "err", err)
		return
	}
	defer killWait(opid, odone)
	d.setDisplayReady(true)
	select {
	case <-xdone:
		d.log.Warn("Xvnc exited; restarting display")
	case <-odone:
		d.log.Warn("openbox exited; restarting display")
	case <-ctx.Done():
	}
	d.setDisplayReady(false)
	// 显示没了，浏览器跟着走（下次按需重新启动）。
	d.killBrowser()
}

// waitSocket 每 50ms 试连 vnc.sock，至多 15s；Xvnc 先退出或 ctx 结束时返回 false。
func (d *desktop) waitSocket(ctx context.Context, xdone <-chan struct{}) bool {
	deadline := time.NewTimer(displayReadyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(displayProbeInterval)
	defer tick.Stop()
	for {
		if c, err := net.DialTimeout("unix", d.sockPath(), time.Second); err == nil {
			_ = c.Close()
			return true
		}
		select {
		case <-tick.C:
		case <-xdone:
			return false
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (d *desktop) setDisplayReady(v bool) {
	d.mu.Lock()
	if d.displayReady != v {
		d.displayReady = v
		d.bumpLocked()
	}
	d.unlockAndEmit()
}

// ---- 浏览器 ----

// startBrowser 幂等地启动浏览器，返回时 DevTools 已就绪。显示未就绪或 Profile 未定时等待，
// 到 ctx 截止仍不满足返回 errNotReady。
func (d *desktop) startBrowser(ctx context.Context) error {
	for {
		d.mu.Lock()
		ch := d.changed
		switch {
		case d.browser == apigen.OrbBrowserStatusRunning:
			d.mu.Unlock()
			return nil
		case d.browser == apigen.OrbBrowserStatusStopped && d.displayReady && d.marker != nil:
			d.browser = apigen.OrbBrowserStatusStarting
			d.bumpLocked()
			d.unlockAndEmit()
			err := d.launchBrowser(ctx)
			if !errors.Is(err, errRetryStart) {
				return err
			}
			continue
		}
		d.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return errNotReady
		}
	}
}

// errRetryStart：取得 opMu 之后发现条件已不满足（期间显示掉了或 Profile 被清空），回到等待。
var errRetryStart = errors.New("desktop: retry browser start")

func (d *desktop) browserArgs() []string {
	return []string{
		"--user-data-dir=" + d.profileDir(), "--remote-debugging-port=0",
		"--no-first-run", "--no-default-browser-check", "--password-store=basic", "--no-sandbox", "--test-type",
		"--hide-crash-restore-bubble", "--start-maximized", "--disk-cache-dir=" + d.cacheDir(), "about:blank",
	}
}

// launchBrowser 在 opMu 下启动 Chromium 并等到 DevTools 就绪（20s）。调用方已把状态置为 starting。
func (d *desktop) launchBrowser(ctx context.Context) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	fail := func(err error) error {
		d.mu.Lock()
		if d.proc == nil {
			d.browser = apigen.OrbBrowserStatusStopped
			d.bumpLocked()
		}
		d.unlockAndEmit()
		return err
	}
	d.mu.Lock()
	ok := d.displayReady && d.marker != nil
	d.mu.Unlock()
	if !ok {
		return fail(errRetryStart)
	}
	dir := d.profileDir()
	for _, p := range []string{dir, d.cacheDir()} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fail(err)
		}
	}
	// 单例锁里编码了主机名与 pid（快照分叉、换机器、崩溃后都会残留）；DevToolsActivePort 必须是这一次写的。
	if des, err := os.ReadDir(dir); err == nil {
		for _, de := range des {
			if strings.HasPrefix(de.Name(), "Singleton") || de.Name() == "DevToolsActivePort" {
				_ = os.RemoveAll(filepath.Join(dir, de.Name()))
			}
		}
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fail(err)
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		return fail(err)
	}
	env := d.r.baseEnv()
	env["DISPLAY"] = desktopDisplay
	pid, done, err := d.spawn("chromium", "chromium", d.browserArgs(), env)
	if err != nil {
		return fail(err)
	}
	p := &browserProc{pid: pid, exited: make(chan struct{})}
	d.mu.Lock()
	d.proc = p
	d.dirty = true
	d.bumpLocked()
	d.unlockAndEmit()
	go func() {
		<-done
		d.onBrowserExit(p)
	}()

	rctx, cancel := context.WithTimeout(ctx, browserReadyTimeout)
	defer cancel()
	port, err := d.waitDevTools(rctx, w, p)
	if err != nil {
		d.log.Warn("browser did not become ready; killing", "err", err)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-p.exited
		return err
	}
	d.mu.Lock()
	if d.proc != p {
		d.mu.Unlock()
		return errors.New("desktop: browser exited during start")
	}
	d.devPort = port
	d.browser = apigen.OrbBrowserStatusRunning
	d.bumpLocked()
	d.unlockAndEmit()
	d.log.Info("browser running", "pid", pid, "devtoolsPort", port)
	return nil
}

// waitDevTools 等 `DevToolsActivePort` 出现且第一行是端口（fsnotify 监视目录），再等 `/json/version` 回 200。
func (d *desktop) waitDevTools(ctx context.Context, w *fsnotify.Watcher, p *browserProc) (int, error) {
	path := filepath.Join(d.profileDir(), "DevToolsActivePort")
	port := 0
	for port == 0 {
		port = readDevToolsPort(path)
		if port != 0 {
			break
		}
		select {
		case <-w.Events:
		case err := <-w.Errors:
			return 0, err
		case <-p.exited:
			return 0, errors.New("browser exited before DevTools was ready")
		case <-ctx.Done():
			return 0, fmt.Errorf("DevToolsActivePort not written: %w", ctx.Err())
		}
	}
	c := &http.Client{Timeout: 2 * time.Second}
	u := "http://127.0.0.1:" + strconv.Itoa(port) + "/json/version"
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if resp, err := c.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return port, nil
			}
		}
		t := time.NewTimer(100 * time.Millisecond)
		select {
		case <-t.C:
		case <-p.exited:
			t.Stop()
			return 0, errors.New("browser exited before DevTools was ready")
		case <-ctx.Done():
			t.Stop()
			return 0, fmt.Errorf("DevTools /json/version not ready: %w", ctx.Err())
		}
	}
}

// readDevToolsPort 读 DevToolsActivePort 的第一行；没有或不完整时返回 0。
func readDevToolsPort(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	line, _, ok := strings.Cut(string(b), "\n")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n <= 0 || n > 65535 {
		return 0
	}
	return n
}

// onBrowserExit 在主进程被回收后清理进程组残余并置 stopped（不自动重启）。
func (d *desktop) onBrowserExit(p *browserProc) {
	_ = syscall.Kill(-p.pid, syscall.SIGKILL)
	d.mu.Lock()
	if d.proc == p {
		d.proc = nil
		d.devPort = 0
		d.browser = apigen.OrbBrowserStatusStopped
		d.bumpLocked()
	}
	d.unlockAndEmit()
	close(p.exited)
}

// stopBrowser 停止浏览器：SIGTERM → grace → 进程组 SIGKILL；已停止则无操作。
func (d *desktop) stopBrowser(grace time.Duration) {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.stopBrowserLocked(grace)
}

// stopBrowserLocked 同 stopBrowser，调用方持有 opMu。
func (d *desktop) stopBrowserLocked(grace time.Duration) {
	d.mu.Lock()
	p := d.proc
	d.mu.Unlock()
	if p == nil {
		return
	}
	if grace > 0 {
		_ = syscall.Kill(-p.pid, syscall.SIGTERM)
		t := time.NewTimer(grace)
		select {
		case <-p.exited:
			t.Stop()
			return
		case <-t.C:
		}
	}
	_ = syscall.Kill(-p.pid, syscall.SIGKILL)
	<-p.exited
}

// killBrowser 立即 SIGKILL 浏览器进程组并等它被回收（显示失效、运行时结束时用；不取 opMu）。
func (d *desktop) killBrowser() {
	d.mu.Lock()
	p := d.proc
	d.mu.Unlock()
	if p != nil {
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		<-p.exited
	}
}

// browserPid 返回浏览器主进程 pid；未运行为 0。
func (d *desktop) browserPid() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.proc == nil {
		return 0
	}
	return d.proc.pid
}
