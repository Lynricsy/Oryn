package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 桌面应用（M25 步骤 1.5）：镜像里的应用清单（任务栏顺序）、应用在快照里的 running / launching、
// 启动应用与窗口操作、取图标。本机路由见 docs/protocol.md「M25：桌面应用与窗口」。

const (
	// defaultDeskAppsPath 是镜像里的应用清单；环境变量 ORYN_DESKTOP_APPS 可以覆盖（测试用）。
	// 图标在清单旁边的 desktop-icons 目录里，清单的 icon 字段是其中的文件名。
	defaultDeskAppsPath = "/usr/local/share/oryn/desktop-apps.json"
	deskIconsDirName    = "desktop-icons"

	// deskAppsMax、deskAppNameMax 是契约里 apps 的条数与 name 的长度上限（name 按字符计）。
	deskAppsMax    = 16
	deskAppNameMax = 64
	// appLaunchWait：launching 至多持续这么久（之后即使还没出现窗口也置回 false）。
	appLaunchWait = 20 * time.Second
	// wmGateTimeout 是显示就绪前等窗口管理器接管探针窗口的上限；超时只记警告。
	wmGateTimeout = 5 * time.Second

	builtinBrowserApp = "chromium"
	// vscodeApp：没有命名空间沙箱时与 Chromium 一样要 --no-sandbox（Electron，默认 seccomp 下不加会崩）。
	vscodeApp = "vscode"
)

var deskAppIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// deskApp 是清单里的一个应用。Exec 为空的只有 chromium（走浏览器启动）；Icon 是图标目录里的文件名，空 = 没有图标。
type deskApp struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Exec    []string `json:"exec"`
	WMClass []string `json:"wmClass"`
	Icon    string   `json:"icon"`
}

// builtinDeskApps 是没有清单（或清单不可用）时的应用：只有内置的 Chromium，没有图标。
func builtinDeskApps() []deskApp {
	return []deskApp{{ID: builtinBrowserApp, Name: "Chromium", WMClass: []string{"Chromium"}}}
}

// deskAppsPath 返回应用清单路径：ORYN_DESKTOP_APPS 优先，缺省 defaultDeskAppsPath。
func deskAppsPath() string {
	if p := os.Getenv("ORYN_DESKTOP_APPS"); p != "" {
		return p
	}
	return defaultDeskAppsPath
}

// loadDeskApps 读取应用清单。文件不存在或解析失败时只返回内置的 chromium（记一行日志）；不合法的条目跳过，
// 不合法的 icon（含路径分隔符、`..`，或不是 .svg / .png）置空。
func loadDeskApps(log *slog.Logger, path string) []deskApp {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Info("no desktop app manifest; only the built-in chromium", "path", path)
		} else {
			log.Warn("read desktop app manifest failed; only the built-in chromium", "path", path, "err", err)
		}
		return builtinDeskApps()
	}
	var raw []deskApp
	if err := json.Unmarshal(b, &raw); err != nil {
		log.Warn("parse desktop app manifest failed; only the built-in chromium", "path", path, "err", err)
		return builtinDeskApps()
	}
	apps := make([]deskApp, 0, min(len(raw), deskAppsMax))
	for _, a := range raw {
		switch {
		case len(apps) == deskAppsMax:
			log.Warn("desktop app manifest has too many apps; extra ignored", "path", path, "max", deskAppsMax)
			return apps
		case !deskAppIDRe.MatchString(a.ID):
			log.Warn("desktop app skipped: invalid id", "id", a.ID)
			continue
		case slices.ContainsFunc(apps, func(x deskApp) bool { return x.ID == a.ID }):
			log.Warn("desktop app skipped: duplicate id", "id", a.ID)
			continue
		case a.Name == "" || len([]rune(a.Name)) > deskAppNameMax:
			log.Warn("desktop app skipped: invalid name", "id", a.ID)
			continue
		case a.ID != builtinBrowserApp && (len(a.Exec) == 0 || a.Exec[0] == ""):
			log.Warn("desktop app skipped: empty exec", "id", a.ID)
			continue
		}
		if a.Icon != "" && iconContentType(a.Icon) == "" {
			log.Warn("desktop app icon ignored: not a plain .svg / .png file name", "id", a.ID, "icon", a.Icon)
			a.Icon = ""
		}
		apps = append(apps, a)
	}
	return apps
}

// iconContentType 返回图标文件名对应的 Content-Type；名字含路径分隔符、是 `.` / `..`，或扩展名不是 .svg / .png
// 时返回空串。
func iconContentType(name string) string {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return ""
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	}
	return ""
}

// appMatcher 返回 WM_CLASS → 应用 id 的映射函数：先看 class（第二项），再看 instance；清单里靠前的应用优先。
func appMatcher(apps []deskApp) func(class []string) string {
	byClass := map[string]string{}
	for _, a := range apps {
		for _, c := range a.WMClass {
			if _, ok := byClass[c]; !ok {
				byClass[c] = a.ID
			}
		}
	}
	return func(class []string) string {
		for i := len(class) - 1; i >= 0; i-- {
			if id, ok := byClass[class[i]]; ok {
				return id
			}
		}
		return ""
	}
}

// appLaunch 是一次已发起、还没出现窗口的启动：known 是发起时这个应用已有的窗口，出现不在其中的窗口即算启动完成。
type appLaunch struct {
	deadline time.Time
	known    []uint32
}

// app 按 id 找清单里的应用。
func (d *desktop) app(id string) (deskApp, bool) {
	i := slices.IndexFunc(d.apps, func(a deskApp) bool { return a.ID == id })
	if i < 0 {
		return deskApp{}, false
	}
	return d.apps[i], true
}

// appWindowsLocked 返回归属于应用 id 的窗口。调用方持有 mu。
func (d *desktop) appWindowsLocked(id string) []uint32 {
	if d.appOf == nil {
		return nil
	}
	var out []uint32
	for _, w := range d.wins {
		if d.appOf(w.class) == id {
			out = append(out, w.id)
		}
	}
	return out
}

// appsLocked 把清单转成快照字段：running = 有归属于它的窗口（chromium 另外看 browser == running）；
// launching = 有未完成的启动（还没出现新窗口，且没过 appLaunchWait）。调用方持有 mu。
func (d *desktop) appsLocked() []apigen.OrbDesktopApp {
	if len(d.apps) == 0 {
		return nil
	}
	now := time.Now()
	out := make([]apigen.OrbDesktopApp, len(d.apps))
	for i, a := range d.apps {
		wins := d.appWindowsLocked(a.ID)
		out[i] = apigen.OrbDesktopApp{
			ID:      a.ID,
			Name:    a.Name,
			Running: len(wins) > 0 || (a.ID == builtinBrowserApp && d.browser == apigen.OrbBrowserStatusRunning),
		}
		if l, ok := d.launches[a.ID]; ok && now.Before(l.deadline) {
			out[i].Launching = !slices.ContainsFunc(wins, func(id uint32) bool { return !slices.Contains(l.known, id) })
		}
	}
	return out
}

// markLaunching 记下应用 id 的一次启动并上报；appLaunchWait 后自动撤销。返回撤销函数（启动失败时调用）。
func (d *desktop) markLaunching(id string) (cancel func()) {
	d.mu.Lock()
	l := &appLaunch{deadline: time.Now().Add(appLaunchWait), known: d.appWindowsLocked(id)}
	if d.launches == nil {
		d.launches = map[string]*appLaunch{}
	}
	d.launches[id] = l
	d.unlockAndEmit()
	drop := func() {
		d.mu.Lock()
		if d.launches[id] == l {
			delete(d.launches, id)
		}
		d.unlockAndEmit()
	}
	t := time.AfterFunc(appLaunchWait, drop)
	return func() {
		t.Stop()
		drop()
	}
}

// agentControlsLocked 报告 agent 是否正在操作桌面（control.state == agent）。调用方持有 mu。
func (d *desktop) agentControlsLocked() bool {
	return d.controlLocked().State == apigen.OrbDesktopControlStateAgent
}

var errControlAgent = errf(http.StatusConflict, "control_agent", "the agent is operating the desktop; take over first")

// launchApp 启动应用 id：chromium 走 startBrowser，其它按清单的 exec 在独立进程组里起一个新实例。
func (d *desktop) launchApp(ctx context.Context, id string) error {
	d.mu.Lock()
	a, ok := d.app(id)
	agent, ready, wsDir := d.agentControlsLocked(), d.displayReady, d.wsDir
	d.mu.Unlock()
	switch {
	case agent:
		return errControlAgent
	case !ok:
		return errf(http.StatusNotFound, "app_not_found", "no desktop app %q", id)
	}
	cancel := d.markLaunching(id)
	if id == builtinBrowserApp {
		ctx, done := context.WithTimeout(ctx, browserStartWait)
		defer done()
		if err := d.startBrowser(ctx); err != nil {
			cancel()
			if errors.Is(err, errNotReady) {
				return errf(http.StatusConflict, "not_ready", "%v", err)
			}
			return errf(http.StatusInternalServerError, "failed", "start browser: %v", err)
		}
		return nil
	}
	if !ready {
		cancel()
		return errf(http.StatusConflict, "not_ready", "the desktop display is not ready")
	}
	dir := wsDir
	if dir == "" {
		dir = d.r.home
	} else if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		d.log.Warn("workspace dir unavailable; launching app in $HOME", "app", id, "dir", dir)
		dir = d.r.home
	}
	args := slices.Clone(a.Exec[1:])
	if id == vscodeApp && !d.sandbox {
		args = append(args, "--no-sandbox")
	}
	if _, _, err := d.spawnIn(dir, "app-"+id, a.Exec[0], args, d.r.baseEnv()); err != nil {
		cancel()
		return errf(http.StatusInternalServerError, "failed", "start %s: %v", id, err)
	}
	return nil
}

// windowAction 对窗口 wid 执行 activate / minimize / close。
func (d *desktop) windowAction(wid, action string) error {
	var op func(wmClient, uint32) error
	switch action {
	case "activate":
		op = wmClient.Activate
	case "minimize":
		op = wmClient.Minimize
	case "close":
		op = wmClient.Close
	default:
		return badRequest("unknown window action %q", action)
	}
	n, err := strconv.ParseUint(wid, 10, 32)
	if err != nil {
		return errf(http.StatusNotFound, "window_not_found", "no window %q", wid)
	}
	id := uint32(n)
	d.mu.Lock()
	agent, wm := d.agentControlsLocked(), d.wm
	known := slices.ContainsFunc(d.wins, func(w winInfo) bool { return w.id == id })
	d.mu.Unlock()
	switch {
	case agent:
		return errControlAgent
	case !known || wm == nil:
		return errf(http.StatusNotFound, "window_not_found", "no window %d", id)
	}
	if err := op(wm, id); err != nil {
		if errors.Is(err, errWMNotConnected) {
			return errf(http.StatusConflict, "not_ready", "%v", err)
		}
		return errf(http.StatusInternalServerError, "failed", "%s window %d: %v", action, id, err)
	}
	return nil
}

// handleAppIcon 回应用的图标：只打开图标目录里清单写明的文件名（os.Root 保证不出目录）。
func (d *desktop) handleAppIcon(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	a, ok := d.app(id)
	ctype := iconContentType(a.Icon)
	if !ok || ctype == "" {
		writeErr(w, errf(http.StatusNotFound, "app_not_found", "no icon for desktop app %q", id))
		return
	}
	f, err := openIcon(d.iconDir, a.Icon)
	if err != nil {
		d.log.Warn("open desktop app icon failed", "app", id, "err", err)
		writeErr(w, errf(http.StatusNotFound, "app_not_found", "no icon for desktop app %q", id))
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		writeErr(w, errf(http.StatusNotFound, "app_not_found", "no icon for desktop app %q", id))
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	w.WriteHeader(http.StatusOK)
	if req.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
}

// openIcon 在图标目录 dir 里打开文件 name。
func openIcon(dir, name string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open icon dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	return root.Open(name)
}
