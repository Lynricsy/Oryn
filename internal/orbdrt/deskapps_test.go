package orbdrt

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func appIDs(apps []deskApp) []string {
	out := make([]string, len(apps))
	for i, a := range apps {
		out[i] = a.ID
	}
	return out
}

func TestDeskAppsManifest(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	dir := t.TempDir()

	t.Run("missing", func(t *testing.T) {
		apps := loadDeskApps(log, filepath.Join(dir, "none.json"))
		assert.Equal(t, builtinDeskApps(), apps)
		assert.Empty(t, apps[0].Icon)
	})
	t.Run("malformed", func(t *testing.T) {
		p := filepath.Join(dir, "bad.json")
		require.NoError(t, os.WriteFile(p, []byte(`{"id":`), 0o644))
		assert.Equal(t, builtinDeskApps(), loadDeskApps(log, p))
	})
	t.Run("image manifest", func(t *testing.T) {
		apps := loadDeskApps(log, filepath.Join("..", "..", "images", "orb", "desktop-apps.json"))
		assert.Equal(t, []string{"chromium", "terminal", "vscode", "files", "editor", "libreoffice"}, appIDs(apps))
		of := appMatcher(apps)
		for _, tc := range []struct {
			class []string
			want  string
		}{
			{[]string{"chromium", "Chromium"}, "chromium"},
			{[]string{"xfce4-terminal", "Xfce4-terminal"}, "terminal"},
			{[]string{"code", "Code"}, "vscode"},
			{[]string{"pcmanfm", "Pcmanfm"}, "files"},
			{[]string{"org.xfce.mousepad", "Org.xfce.mousepad"}, "editor"},
			{[]string{"libreoffice", "libreoffice-writer"}, "libreoffice"},
			{[]string{"soffice", "Soffice"}, "libreoffice"},
			{[]string{"xterm", "XTerm"}, ""},
			{nil, ""},
		} {
			assert.Equal(t, tc.want, of(tc.class), "WM_CLASS %q", tc.class)
		}
	})
	t.Run("invalid entries", func(t *testing.T) {
		p := filepath.Join(dir, "mixed.json")
		require.NoError(t, os.WriteFile(p, []byte(`[
			{"id":"ok","name":"OK","exec":["x"],"icon":"ok.svg"},
			{"id":"Bad","name":"Bad","exec":["x"]},
			{"id":"ok","name":"Dup","exec":["x"]},
			{"id":"noexec","name":"No exec","exec":[]},
			{"id":"up","name":"Up","exec":["x"],"icon":"../up.png"},
			{"id":"sub","name":"Sub","exec":["x"],"icon":"a/b.png"},
			{"id":"ext","name":"Ext","exec":["x"],"icon":"x.xpm"}
		]`), 0o644))
		apps := loadDeskApps(log, p)
		assert.Equal(t, []string{"ok", "up", "sub", "ext"}, appIDs(apps))
		assert.Equal(t, "ok.svg", apps[0].Icon)
		for _, a := range apps[1:] {
			assert.Empty(t, a.Icon, "icon of %s rejected", a.ID)
		}
	})
}

// fakeAppRun 是 fakeapp-gui 启动时报告的一行。
type fakeAppRun struct {
	Pid  int      `json:"pid"`
	Cwd  string   `json:"cwd"`
	Args []string `json:"args"`
}

// fakeAppRuns 读应用 id 的桌面日志里 fakeapp-gui 报告的启动。
func (e *deskEnv) fakeAppRuns(id string) []fakeAppRun {
	b, _ := os.ReadFile(e.home(".cache", "oryn", "logs", "desktop", "app-"+id+".log"))
	var out []fakeAppRun
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "fakeapp-gui "); ok {
			var r fakeAppRun
			if json.Unmarshal([]byte(v), &r) == nil {
				out = append(out, r)
			}
		}
	}
	return out
}

// waitFakeApp 等应用 id 的第 n 次（从 1 起）启动报告。
func (e *deskEnv) waitFakeApp(id string, n int) fakeAppRun {
	e.t.Helper()
	var runs []fakeAppRun
	require.Eventually(e.t, func() bool {
		runs = e.fakeAppRuns(id)
		return len(runs) >= n
	}, 10*time.Second, 10*time.Millisecond, "fakeapp-gui for %s started", id)
	return runs[n-1]
}

func deskAppState(s apigen.LinkCtlDesktopStateParams, id string) (apigen.OrbDesktopApp, bool) {
	i := slices.IndexFunc(s.Apps, func(a apigen.OrbDesktopApp) bool { return a.ID == id })
	if i < 0 {
		return apigen.OrbDesktopApp{}, false
	}
	return s.Apps[i], true
}

func appIs(id string, running, launching bool) func(apigen.LinkCtlDesktopStateParams) bool {
	return func(s apigen.LinkCtlDesktopStateParams) bool {
		a, ok := deskAppState(s, id)
		return ok && a.Running == running && a.Launching == launching
	}
}

// writeTestManifest 在临时目录里写一份清单（应用用 fakeapp-gui 扮演）与图标，并经 ORYN_DESKTOP_APPS 指过去。
func writeTestManifest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	icons := filepath.Join(dir, deskIconsDirName)
	require.NoError(t, os.MkdirAll(icons, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(icons, "chromium.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(icons, "vscode.png"), []byte("\x89PNG fake"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.png"), []byte("outside"), 0o644))
	manifest := `[
		{"id":"chromium","name":"Chromium","exec":[],"wmClass":["Chromium"],"icon":"chromium.svg"},
		{"id":"terminal","name":"Terminal","exec":["fakeapp-gui"],"wmClass":["Xfce4-terminal"]},
		{"id":"vscode","name":"VS Code","exec":["fakeapp-gui","--force-renderer-accessibility"],"wmClass":["code"],"icon":"vscode.png"},
		{"id":"escape","name":"Escape","exec":["fakeapp-gui"],"wmClass":["escape"],"icon":"../secret.png"}
	]`
	p := filepath.Join(dir, "desktop-apps.json")
	require.NoError(t, os.WriteFile(p, []byte(manifest), 0o644))
	t.Setenv("ORYN_DESKTOP_APPS", p)
}

func TestDeskAppsNoManifest(t *testing.T) {
	t.Setenv("ORYN_DESKTOP_APPS", filepath.Join(t.TempDir(), "missing.json"))
	e := newDeskEnv(t, nil)
	s := e.wait("display ready", displayIs(true))
	assert.Equal(t, []apigen.OrbDesktopApp{{ID: "chromium", Name: "Chromium"}}, s.Apps)

	_, raw := e.cp.waitNotif(t, 0, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	assert.Contains(t, h.Features, "desktop-apps")

	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := o.do(http.MethodGet, "/desktop/apps/chromium/icon", nil, nil, nil)
	assert.Equal(t, "app_not_found", o.errCode(resp, b, http.StatusNotFound), "built-in chromium has no icon")
}

func TestDeskAppsLaunch(t *testing.T) {
	writeTestManifest(t)
	fw := newFakeWM()
	e := newDeskEnv(t, func(c *Config) {
		c.DesktopWM = func(string) wmClient { return fw }
		c.DesktopUserNS = func() bool { return false }
	})
	// 启动的应用在独立进程组里，不随运行时结束：测试结束时按它们报告的 pid 杀掉。
	t.Cleanup(func() {
		for _, id := range []string{"terminal", "vscode"} {
			for _, r := range e.fakeAppRuns(id) {
				_ = syscall.Kill(-r.Pid, syscall.SIGKILL)
			}
		}
	})
	e.wait("display ready", displayIs(true))
	<-fw.watching
	gates, _ := fw.recorded()
	assert.Equal(t, 1, gates, "window manager gate before display ready")
	o := newOrbHTTP(t, e.cp, e.rt)
	d := e.rt.desk

	var st apigen.LinkCtlDesktopStateParams
	o.json(http.MethodGet, "/desktop/state", nil, nil, http.StatusOK, &st)
	require.Len(t, st.Apps, 4)
	assert.Equal(t, apigen.OrbDesktopApp{ID: "vscode", Name: "VS Code"}, st.Apps[2])

	// 工作区还没就绪：在 $HOME 下启动。
	o.json(http.MethodPost, "/desktop/apps/terminal/launch", nil, nil, http.StatusOK, &st)
	a, _ := deskAppState(st, "terminal")
	assert.True(t, a.Launching)
	assert.Equal(t, evalDir(t, e.rt.home), evalDir(t, e.waitFakeApp("terminal", 1).Cwd))

	// 工作区就绪后用 workspaceReady 记下的目录；没有命名空间沙箱时 vscode 加 --no-sandbox。
	ws := t.TempDir()
	d.workspaceReady(ws)
	o.json(http.MethodPost, "/desktop/apps/vscode/launch", nil, nil, http.StatusOK, &st)
	a, _ = deskAppState(st, "vscode")
	assert.Equal(t, apigen.OrbDesktopApp{ID: "vscode", Name: "VS Code", Launching: true}, a)
	run := e.waitFakeApp("vscode", 1)
	assert.Equal(t, evalDir(t, ws), evalDir(t, run.Cwd))
	assert.Equal(t, []string{"--force-renderer-accessibility", "--no-sandbox"}, run.Args)

	// 出现它的窗口：running，launching 结束。
	fw.push([]winInfo{{id: 41, class: []string{"code", "Code"}, title: "VS Code"}})
	e.wait("vscode running", appIs("vscode", true, false))

	// 已有窗口时再启动一个实例：launching 直到出现新窗口。
	o.json(http.MethodPost, "/desktop/apps/vscode/launch", nil, nil, http.StatusOK, &st)
	a, _ = deskAppState(st, "vscode")
	assert.True(t, a.Running)
	assert.True(t, a.Launching)
	e.waitFakeApp("vscode", 2)
	fw.push([]winInfo{
		{id: 41, class: []string{"code", "Code"}, title: "VS Code"},
		{id: 42, class: []string{"code", "Code"}, title: "VS Code 2"},
	})
	e.wait("second vscode window", appIs("vscode", true, false))

	// 窗口操作。
	o.json(http.MethodPost, "/desktop/windows/42/activate", nil, nil, http.StatusOK, nil)
	o.json(http.MethodPost, "/desktop/windows/41/minimize", nil, nil, http.StatusOK, nil)
	o.json(http.MethodPost, "/desktop/windows/41/close", nil, nil, http.StatusOK, nil)
	_, ops := fw.recorded()
	assert.Equal(t, []string{"activate:42", "minimize:41", "close:41"}, ops)
	resp, b := o.do(http.MethodPost, "/desktop/windows/99/activate", nil, nil, nil)
	assert.Equal(t, "window_not_found", o.errCode(resp, b, http.StatusNotFound))
	resp, b = o.do(http.MethodPost, "/desktop/windows/41/maximize", nil, nil, nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))

	// 未知应用。
	resp, b = o.do(http.MethodPost, "/desktop/apps/nope/launch", nil, nil, nil)
	assert.Equal(t, "app_not_found", o.errCode(resp, b, http.StatusNotFound))

	// 图标：按扩展名给 Content-Type，带 nosniff 与 CSP sandbox；清单里越出图标目录的 icon 不给。
	for _, tc := range []struct{ id, ctype, body string }{
		{"chromium", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg"/>`},
		{"vscode", "image/png", "\x89PNG fake"},
	} {
		resp, b := o.do(http.MethodGet, "/desktop/apps/"+tc.id+"/icon", nil, nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
		assert.Equal(t, tc.ctype, resp.Header.Get("Content-Type"), tc.id)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
		assert.Equal(t, "private, max-age=86400", resp.Header.Get("Cache-Control"))
		assert.Equal(t, tc.body, string(b))
	}
	for _, id := range []string{"terminal", "escape", "nope"} {
		resp, b := o.do(http.MethodGet, "/desktop/apps/"+id+"/icon", nil, nil, nil)
		assert.Equal(t, "app_not_found", o.errCode(resp, b, http.StatusNotFound), id)
	}

	// agent 正在操作桌面：启动与窗口操作都回 409 control_agent。
	const tid = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	fakeAgent(t, e.rt, tid)
	d.observeAgentFrame(tid, "agent_start", []byte(`{"type":"agent_start"}`))
	e.wait("agent", controlIs(apigen.OrbDesktopControlStateAgent, false))
	resp, b = o.do(http.MethodPost, "/desktop/apps/terminal/launch", nil, nil, nil)
	assert.Equal(t, "control_agent", o.errCode(resp, b, http.StatusConflict))
	resp, b = o.do(http.MethodPost, "/desktop/windows/42/activate", nil, nil, nil)
	assert.Equal(t, "control_agent", o.errCode(resp, b, http.StatusConflict))
	_, ops = fw.recorded()
	assert.Len(t, ops, 3, "no window operation while the agent works")
	assert.Len(t, e.fakeAppRuns("terminal"), 1, "no launch while the agent works")
}

// chromium 的启动走浏览器启动（Profile 未定时的 not_ready 与 /desktop/browser/start 同一条路径，等满 30s，不在这里测）：
// 定为不用 Profile 之后浏览器跑起来，chromium 为 running。
func TestDeskAppsLaunchChromium(t *testing.T) {
	writeTestManifest(t)
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)

	resp, b := o.do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	var st apigen.LinkCtlDesktopStateParams
	o.json(http.MethodPost, "/desktop/apps/chromium/launch", nil, nil, http.StatusOK, &st)
	assert.Equal(t, apigen.OrbBrowserStatusRunning, st.Browser)
	a, _ := deskAppState(st, "chromium")
	assert.True(t, a.Running)
	assert.True(t, a.Launching, "no chromium window yet")
}
