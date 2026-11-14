package orbdrt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

const (
	testProfileA = "bpf_01JAAAAAAAAAAAAAAAAAAAAAAA"
	testProfileB = "bpf_01JBBBBBBBBBBBBBBBBBBBBBBB"
)

// deskEnv 是启用了桌面（fakedesk 扮演 Xvnc / openbox / chromium）的 orbd 与假控制面；
// 状态经 OnDesktopState 记录，测试按顺序等待，不 sleep。
type deskEnv struct {
	t   *testing.T
	cp  *fakeCP
	rt  *Runtime
	x11 string

	mu     sync.Mutex
	states []apigen.LinkCtlDesktopStateParams
	cursor int
	sig    chan struct{}
}

func newDeskEnv(t *testing.T, mutate func(*Config)) *deskEnv {
	t.Helper()
	e := &deskEnv{t: t, cp: newFakeCP(t), x11: t.TempDir(), sig: make(chan struct{}, 1)}
	e.rt = startRuntime(t, e.cp, func(c *Config) {
		c.Desktop = true
		c.DesktopCDPAddr = "127.0.0.1:0"
		c.DesktopTmpDir = e.x11
		c.DesktopBackoff = func(int) time.Duration { return 5 * time.Millisecond }
		c.DesktopUserNS = func() bool { return true }
		c.OnDesktopState = func(s apigen.LinkCtlDesktopStateParams) {
			e.mu.Lock()
			e.states = append(e.states, s)
			e.mu.Unlock()
			select {
			case e.sig <- struct{}{}:
			default:
			}
		}
		if mutate != nil {
			mutate(c)
		}
	})
	require.NotNil(t, e.rt.desk, "desktop should be enabled")
	return e
}

// wait 消费记录的状态，直到出现满足 pred 的一条。
func (e *deskEnv) wait(what string, pred func(apigen.LinkCtlDesktopStateParams) bool) apigen.LinkCtlDesktopStateParams {
	e.t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		e.mu.Lock()
		for e.cursor < len(e.states) {
			s := e.states[e.cursor]
			e.cursor++
			if pred(s) {
				e.mu.Unlock()
				return s
			}
		}
		e.mu.Unlock()
		select {
		case <-e.sig:
		case <-deadline.C:
			e.t.Fatalf("desktop state %q not reached", what)
		}
	}
}

func displayIs(v bool) func(apigen.LinkCtlDesktopStateParams) bool {
	return func(s apigen.LinkCtlDesktopStateParams) bool { return s.DisplayReady == v }
}

func browserIs(b apigen.OrbBrowserStatus) func(apigen.LinkCtlDesktopStateParams) bool {
	return func(s apigen.LinkCtlDesktopStateParams) bool { return s.Browser == b }
}

func (e *deskEnv) home(p ...string) string {
	return filepath.Join(append([]string{e.rt.home}, p...)...)
}

func (e *deskEnv) profile(p ...string) string {
	return filepath.Join(append([]string{e.rt.home, ".oryn", "desktop", "profile"}, p...)...)
}

func (e *deskEnv) marker() profileMarker {
	e.t.Helper()
	b, err := os.ReadFile(e.home(".oryn", "desktop", "profile.json"))
	require.NoError(e.t, err)
	var m profileMarker
	require.NoError(e.t, json.Unmarshal(b, &m))
	return m
}

func (e *deskEnv) xvncPid() int {
	e.rt.desk.mu.Lock()
	defer e.rt.desk.mu.Unlock()
	return e.rt.desk.xvncPid
}

// cdpGet 经 CDP 前门发一个 GET。
func (e *deskEnv) cdpGet(path string) (*http.Response, []byte) {
	e.t.Helper()
	c := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get("http://" + e.rt.desk.cdpAddr() + path)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(e.t, err)
	return resp, b
}

func procArgs(t *testing.T, pid int) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

func procEnv(t *testing.T, pid int) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

func openDesk(t *testing.T, cp *fakeCP) (*link.Stream, apigen.LinkDeskReply) {
	t.Helper()
	cp.ctlNow(t)
	cp.mu.Lock()
	sess := cp.sess
	cp.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamDesk})
	require.NoError(t, err)
	line, err := st.ReadLine(4096)
	require.NoError(t, err)
	var rep apigen.LinkDeskReply
	require.NoError(t, json.Unmarshal(line, &rep))
	return st, rep
}

// ---- 启用判定 ----

func assertDesktopAbsent(t *testing.T, cp *fakeCP, rt *Runtime) {
	t.Helper()
	assert.Nil(t, rt.desk)
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	assert.NotContains(t, h.Features, "desktop")

	o := newOrbHTTP(t, cp, rt)
	resp, b := o.do(http.MethodGet, "/desktop/state", nil, nil, nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))
	resp, b = o.do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))

	// desk 流被直接关闭。
	cp.mu.Lock()
	sess := cp.sess
	cp.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamDesk})
	require.NoError(t, err)
	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err = st.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)

	_, err = sockCall(t, rt, orbsock.MethodDesktopStatus, nil, nil)
	assert.Equal(t, orbsock.CodeNotSupported, sockErrCode(err))
	err = cp.call(t, string(apigen.MethodProfileMode), apigen.LinkCtlProfileModeParams{ProfileID: testProfileA, Mode: apigen.BrowserProfileAccessReadonly, Reason: apigen.BrowserProfileReadonlyReasonTakenOver}, nil)
	require.Error(t, err)

	_, err = os.Stat(rt.desktopOverlayPath())
	assert.ErrorIs(t, err, os.ErrNotExist, "desktop.yml only exists when the desktop is enabled")
}

func TestDesktopDisabledByConfig(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) { c.Desktop = false })
	assertDesktopAbsent(t, cp, rt)
}

func TestDesktopDisabledWhenExecutableMissing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, linkFakedesk(dir, "Xvnc", "openbox")) // 缺 chromium
	t.Setenv("PATH", dir)
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) { c.Desktop = true; c.DesktopCDPAddr = "127.0.0.1:0" })
	assertDesktopAbsent(t, cp, rt)
}

// ---- 显示栈 ----

func TestDesktopDisplaySupervision(t *testing.T) {
	var x11 string
	e := newDeskEnv(t, func(c *Config) {
		x11 = c.DesktopTmpDir
		// 上次运行残留的 X 锁与 X socket。
		require.NoError(t, os.WriteFile(filepath.Join(x11, ".X1-lock"), []byte("  4242\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(x11, ".X11-unix"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(x11, ".X11-unix", "X1"), nil, 0o644))
	})
	e.wait("display ready", displayIs(true))
	for _, p := range []string{filepath.Join(x11, ".X1-lock"), filepath.Join(x11, ".X11-unix", "X1")} {
		_, err := os.Stat(p)
		assert.ErrorIs(t, err, os.ErrNotExist, "stale %s removed", p)
	}
	fi, err := os.Stat(e.home(".oryn", "vnc.sock"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	// hello 带 desktop；link 建立后补发全量状态。
	_, raw := e.cp.waitNotif(t, 0, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	assert.Contains(t, h.Features, "desktop")
	e.cp.waitNotif(t, 0, string(apigen.MethodDesktopState), func(p json.RawMessage) bool {
		var s apigen.LinkCtlDesktopStateParams
		return json.Unmarshal(p, &s) == nil && s.DisplayReady && s.Browser == apigen.OrbBrowserStatusStopped
	})

	// 启动参数。
	pid := e.xvncPid()
	args := procArgs(t, pid)
	assert.Equal(t, []string{
		":1", "-geometry", "1280x800", "-depth", "24", "-rfbunixpath", e.home(".oryn", "vnc.sock"), "-rfbunixmode", "0600",
		"-rfbport", "-1", "-SecurityTypes", "None", "-AlwaysShared", "-AcceptSetDesktopSize=1", "-SendPrimary=0",
		"-SetPrimary=0", "-desktop", "Oryn", "-nolisten", "tcp",
	}, args[1:])
	assert.NotContains(t, procEnv(t, pid), "DISPLAY=:1", "Xvnc itself runs without DISPLAY")

	// 杀掉 Xvnc：显示失效，退避后恢复。
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	e.wait("display lost", displayIs(false))
	e.wait("display back", displayIs(true))
	assert.NotEqual(t, pid, e.xvncPid())
}

// ---- 浏览器与 CDP 前门 ----

func TestDesktopBrowserLifecycle(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)

	// Profile 未定：等到截止返回 errNotReady。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	err := e.rt.desk.startBrowser(ctx)
	cancel()
	require.ErrorIs(t, err, errNotReady)
	assert.Equal(t, apigen.OrbBrowserStatusStopped, e.rt.desk.snapshot().Browser)

	// 定为不用 Profile；经前门的第一个连接触发启动。
	resp, b := o.do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.Equal(t, profileMarker{OrbID: "orb-test"}, e.marker())
	require.NoError(t, os.MkdirAll(e.profile(), 0o700))
	require.NoError(t, os.Symlink("orb-elsewhere-1", e.profile("SingletonLock")))

	resp, b = e.cdpGet("/json/version")
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	var ver map[string]string
	require.NoError(t, json.Unmarshal(b, &ver))
	assert.Equal(t, "ws://"+e.rt.desk.cdpAddr()+"/devtools/browser/fake", ver["webSocketDebuggerUrl"], "Host 头原样到达 Chromium")
	_, b = e.cdpGet("/json/list")
	assert.Equal(t, "echo GET /json/list ", string(b))

	st := e.wait("browser running", browserIs(apigen.OrbBrowserStatusRunning))
	assert.True(t, st.Dirty)
	_, err = os.Lstat(e.profile("SingletonLock"))
	assert.ErrorIs(t, err, os.ErrNotExist, "stale singleton lock removed before start")

	pid := e.rt.desk.browserPid()
	require.Positive(t, pid)
	// user namespace 可用：不带 --no-sandbox；从不带 --test-type 与 --disk-cache-dir（缓存留在 Profile 目录里）。
	assert.Equal(t, []string{
		"--user-data-dir=" + e.profile(), "--remote-debugging-port=0", "--no-first-run", "--no-default-browser-check",
		"--password-store=basic", "--hide-crash-restore-bubble", "--start-maximized", "about:blank",
	}, procArgs(t, pid)[1:])
	assert.Contains(t, procEnv(t, pid), "DISPLAY=:1")
	assert.Positive(t, e.rt.desk.devtoolsPort())

	// 幂等：已在运行时直接返回。
	require.NoError(t, e.rt.desk.startBrowser(context.Background()))
	assert.Equal(t, pid, e.rt.desk.browserPid())

	// 停止：经 CDP Browser.close 走完整的关闭流程，不发 SIGTERM。
	var snap apigen.LinkCtlDesktopStateParams
	o.json(http.MethodPost, "/desktop/browser/stop", nil, nil, http.StatusOK, &snap)
	assert.Equal(t, apigen.OrbBrowserStatusStopped, snap.Browser)
	_, err = os.Stat(e.profile("Default", "exit-clean"))
	require.NoError(t, err, "browser closed itself via Browser.close")
	_, err = os.Stat(e.profile("Default", "exit-term"))
	assert.ErrorIs(t, err, os.ErrNotExist, "no SIGTERM")
	assert.Zero(t, e.rt.desk.devtoolsPort())

	// 本机 API 的启动。
	o.json(http.MethodPost, "/desktop/browser/start", nil, nil, http.StatusOK, &snap)
	assert.Equal(t, apigen.OrbBrowserStatusRunning, snap.Browser)
	assert.NotEqual(t, pid, e.rt.desk.browserPid())
	o.json(http.MethodGet, "/desktop/state", nil, nil, http.StatusOK, &snap)
	assert.Equal(t, apigen.OrbBrowserStatusRunning, snap.Browser)

	// 浏览器自己退出：回到 stopped，不自动重启。
	e.wait("browser running again", browserIs(apigen.OrbBrowserStatusRunning))
	require.NoError(t, syscall.Kill(e.rt.desk.browserPid(), syscall.SIGKILL))
	e.wait("browser stopped", browserIs(apigen.OrbBrowserStatusStopped))
	assert.Zero(t, e.rt.desk.browserPid())
}

// ---- desk 流 ----

func TestDeskStream(t *testing.T) {
	e := newDeskEnv(t, func(c *Config) {
		c.DesktopBackoff = func(int) time.Duration { return time.Hour } // 显示掉了之后不恢复
	})
	e.wait("display ready", displayIs(true))

	st, rep := openDesk(t, e.cp)
	require.True(t, rep.Ok, "%+v", rep)
	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte(i * 13)
	}
	go func() {
		_, _ = st.Write(payload)
		_ = st.Close() // 半关闭：假 Xvnc 读到 EOF 后关闭，orbd 把 EOF 传回来
	}()
	got, err := io.ReadAll(st)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "1MiB round trip through the RFB socket")

	require.NoError(t, syscall.Kill(e.xvncPid(), syscall.SIGKILL))
	e.wait("display lost", displayIs(false))
	_, rep = openDesk(t, e.cp)
	assert.False(t, rep.Ok)
	require.NotNil(t, rep.Code)
	assert.Equal(t, "display_unavailable", *rep.Code)
}

// ---- 归档 ----

func TestProfileIncluded(t *testing.T) {
	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"Local State", false, true},
		{"Default", true, true},
		{"Default/Cookies", false, true},
		{"Default/Network/Cookies", false, true},
		{"Default/Local Storage/leveldb/x", false, true},
		{"Default/IndexedDB", true, true},
		{"Default/Preferences", false, true},
		{"Default/Cache/x", false, false},
		{"Default/Cache", true, false},
		{"Default/Code Cache/x", false, false},
		{"Default/GPUCache/x", false, false},
		{"Default/Service Worker/CacheStorage/x", false, false},
		{"Default/blob_storage/x", false, false},
		{"Default/Session Storage/x", false, false},
		{"Default/Sessions/x", false, false},
		{"Default/Cookies.tmp", false, false},
		{"SingletonLock", false, false},
		{"DevToolsActivePort", false, false},
		{"Crashpad/x", false, false},
		{"Profile 1/x", false, false},
		{"Local State", true, false},
		{"Default", false, false},
		{"Default/Extensions", true, false},
		{"Default/Extensions/abc/1.0/manifest.json", false, false},
		{"Default/Local Extension Settings/abc/000003.log", false, false},
		{"Default/Extension State", true, false},
		{"Default/Extension Rules/x", false, false},
		{"Default/Extension Scripts/x", false, false},
		{"Default/Managed Extension Settings/x", false, false},
		{"Default/Sync Extension Settings/x", false, false},
		{"Default/Extension Cookies", false, false},
		{"Default/Extension Cookies-journal", false, false},
		{"Default/Preferences", false, true},
		{"Default/Secure Preferences", false, true},
		{"Default/oryn-cookies.json", false, true},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, profileIncluded(c.rel, c.isDir), "%q dir=%v", c.rel, c.isDir)
	}
}

// writeTree 在 dir 下按 相对路径 → 内容 建文件。
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(data), 0o600))
	}
}

func archiveNames(t *testing.T, b []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		require.NoError(t, err)
		assert.Equal(t, int64(0o600), h.Mode)
		assert.Zero(t, h.Uid)
		assert.Empty(t, h.Uname)
		names = append(names, h.Name)
	}
}

func TestProfileArchiveRoundTrip(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"Local State":                            "{}",
		"Default/Cookies":                        "c1",
		"Default/Network/Cookies":                "c2",
		"Default/Local Storage/leveldb/x":        "ls",
		"Default/Cache/x":                        "cache",
		"Default/Code Cache/js/x":                "code",
		"Default/Sessions/Session_1":             "s",
		"Default/Preferences.tmp":                "tmp",
		"SingletonCookie":                        "x",
		"Crashpad/settings.dat":                  "x",
		"Profile 1/Cookies":                      "x",
		"Default/Extensions/abc/manifest.json":   "{}",
		"Default/Local Extension Settings/abc/x": "x",
		"Default/Extension State/LOG":            "x",
		"Default/Extension Cookies":              "x",
	})
	require.NoError(t, os.Symlink("Cookies", filepath.Join(src, "Default", "link")))

	var a, b bytes.Buffer
	require.NoError(t, writeProfileArchive(&a, src, nil))
	require.NoError(t, writeProfileArchive(&b, src, nil))
	assert.Equal(t, a.Bytes(), b.Bytes(), "same directory → identical bytes")
	assert.Equal(t, []string{"Default/Cookies", "Default/Local Storage/leveldb/x", "Default/Network/Cookies", "Local State"}, archiveNames(t, a.Bytes()))

	dst := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, extractProfileArchive(bytes.NewReader(a.Bytes()), dst))
	got, err := os.ReadFile(filepath.Join(dst, "Default", "Network", "Cookies"))
	require.NoError(t, err)
	assert.Equal(t, "c2", string(got))
	var c bytes.Buffer
	require.NoError(t, writeProfileArchive(&c, dst, nil))
	assert.Equal(t, a.Bytes(), c.Bytes(), "restored directory archives to the same bytes")

	// 不存在的目录 = 空 Profile。
	var empty bytes.Buffer
	require.NoError(t, writeProfileArchive(&empty, filepath.Join(t.TempDir(), "missing"), nil))
	assert.Empty(t, archiveNames(t, empty.Bytes()))
}

type tarEntry struct {
	hdr  tar.Header
	data string
}

func mkTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, en := range entries {
		h := en.hdr
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(en.data))
		}
		if h.Mode == 0 {
			h.Mode = 0o600
		}
		require.NoError(t, tw.WriteHeader(&h))
		if en.data != "" {
			_, err := tw.Write([]byte(en.data))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func reg(name, data string) tarEntry {
	return tarEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: name}, data: data}
}

func TestExtractProfileArchiveRejects(t *testing.T) {
	codeOf := func(err error) string {
		var ae *apiError
		if assert.ErrorAs(t, err, &ae) {
			return ae.code
		}
		return ""
	}
	extract := func(b []byte) error {
		return extractProfileArchive(bytes.NewReader(b), filepath.Join(t.TempDir(), "profile"))
	}
	cases := map[string][]byte{
		"dotdot":     mkTarGz(t, reg("../x", "x")),
		"inner":      mkTarGz(t, reg("Default/../../x", "x")),
		"absolute":   mkTarGz(t, reg("/etc/passwd", "x")),
		"symlink":    mkTarGz(t, tarEntry{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "Default/Cookies", Linkname: "/etc/passwd"}}),
		"hardlink":   mkTarGz(t, tarEntry{hdr: tar.Header{Typeflag: tar.TypeLink, Name: "Default/Cookies", Linkname: "Local State"}}),
		"device":     mkTarGz(t, tarEntry{hdr: tar.Header{Typeflag: tar.TypeChar, Name: "Default/tty"}}),
		"outside":    mkTarGz(t, reg("Crashpad/x", "x")),
		"cache":      mkTarGz(t, reg("Default/Cache/x", "x")),
		"notgzip":    []byte("plain text"),
		"truncated":  mkTarGz(t, reg("Default/Cookies", strings.Repeat("c", 4096)))[:60],
		"dot-prefix": mkTarGz(t, reg("./Default/Cookies", "x")),
	}
	for name, b := range cases {
		assert.Equal(t, "invalid_archive", codeOf(extract(b)), name)
	}

	old := maxProfileEntries
	maxProfileEntries = 2
	err := extract(mkTarGz(t, reg("Default/a", "1"), reg("Default/b", "2"), reg("Default/c", "3")))
	maxProfileEntries = old
	assert.Equal(t, "too_large", codeOf(err), "entry limit")

	oldBytes := maxProfileUnpackedBytes
	maxProfileUnpackedBytes = 8
	err = extract(mkTarGz(t, reg("Default/a", "12345"), reg("Default/b", "67890")))
	maxProfileUnpackedBytes = oldBytes
	assert.Equal(t, "too_large", codeOf(err), "unpacked size limit")

	require.NoError(t, extract(mkTarGz(t, tarEntry{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "Default/"}}, reg("Default/Cookies", "ok"))))

	// 旧版 orbd 存进去的扩展条目：跳过不解，归档仍可装载；路径检查照旧先做。
	dst := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, extractProfileArchive(bytes.NewReader(mkTarGz(t,
		tarEntry{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "Default/Extensions/"}},
		reg("Default/Extensions/abc/1.0/manifest.json", "{}"),
		reg("Default/Local Extension Settings/abc/000003.log", "x"),
		reg("Default/Extension Cookies", "x"),
		reg("Default/Cookies", "ok"),
	)), dst))
	got, err := os.ReadFile(filepath.Join(dst, "Default", "Cookies"))
	require.NoError(t, err)
	assert.Equal(t, "ok", string(got))
	for _, p := range []string{"Extensions", "Local Extension Settings", "Extension Cookies"} {
		_, err := os.Stat(filepath.Join(dst, "Default", p))
		assert.ErrorIs(t, err, os.ErrNotExist, p)
	}
	assert.Equal(t, "invalid_archive", codeOf(extract(mkTarGz(t, reg("Default/Extensions/../../x", "x")))))
}

// ---- 本机 API ----

func archiveOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, files)
	var buf bytes.Buffer
	require.NoError(t, writeProfileArchive(&buf, dir, nil))
	return buf.Bytes()
}

func putProfile(o *orbHTTP, id, name string, extra url.Values, body []byte) (*http.Response, []byte) {
	q := url.Values{"id": {id}, "name": {name}}
	for k, v := range extra {
		q[k] = v
	}
	return o.do(http.MethodPut, "/desktop/profile", q, bytes.NewReader(body), nil)
}

func TestDesktopProfileAPI(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)

	// 带体装载。
	resp, b := putProfile(o, testProfileA, "Work", nil, archiveOf(t, map[string]string{"Default/Cookies": "sid=1", "Local State": "{}"}))
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.Equal(t, profileMarker{OrbID: "orb-test", ProfileID: testProfileA, Name: "Work", Mode: apigen.BrowserProfileAccessExclusive}, e.marker())
	got, err := os.ReadFile(e.profile("Default", "Cookies"))
	require.NoError(t, err)
	assert.Equal(t, "sid=1", string(got))
	st := e.wait("profile A", func(s apigen.LinkCtlDesktopStateParams) bool {
		return s.ProfileID != nil && *s.ProfileID == testProfileA
	})
	assert.False(t, st.Dirty)
	require.NotNil(t, st.ProfileMode)
	assert.Equal(t, apigen.BrowserProfileAccessExclusive, *st.ProfileMode)

	// 坏归档：原目录与标记不动。
	resp, b = putProfile(o, testProfileB, "Other", nil, mkTarGz(t, reg("../evil", "x")))
	assert.Equal(t, "invalid_archive", o.errCode(resp, b, http.StatusBadRequest))
	assert.Equal(t, testProfileA, e.marker().ProfileID)
	_, err = os.Stat(e.profile("Default", "Cookies"))
	require.NoError(t, err)
	_, err = os.Stat(e.home(".oryn", "desktop", "profile.new"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	resp, b = putProfile(o, "bpf_bad", "x", nil, nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))

	// keep=1：命中只改写标记，不命中 profile_mismatch。
	resp, b = putProfile(o, testProfileA, "Work 2", url.Values{"keep": {"1"}}, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.Equal(t, "Work 2", e.marker().Name)
	resp, b = putProfile(o, testProfileB, "Other", url.Values{"keep": {"1"}}, nil)
	assert.Equal(t, "profile_mismatch", o.errCode(resp, b, http.StatusConflict))

	// keep=1&mode=readonly：改为只读，导出被拒；再改回独占。
	resp, b = putProfile(o, testProfileA, "Work 2", url.Values{"keep": {"1"}, "mode": {"readonly"}}, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	m := e.marker()
	assert.Equal(t, apigen.BrowserProfileAccessReadonly, m.Mode)
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonRequested, m.ReadonlyReason)
	st = e.wait("readonly", func(s apigen.LinkCtlDesktopStateParams) bool {
		return s.ProfileMode != nil && *s.ProfileMode == apigen.BrowserProfileAccessReadonly
	})
	require.NotNil(t, st.ReadonlyReason)
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonRequested, *st.ReadonlyReason)
	resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"live"}}, nil, nil)
	assert.Equal(t, "profile_readonly", o.errCode(resp, b, http.StatusConflict))
	resp, b = putProfile(o, testProfileA, "Work 2", url.Values{"keep": {"1"}, "mode": {"exclusive"}}, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.Equal(t, apigen.BrowserProfileAccessExclusive, e.marker().Mode)
	assert.Empty(t, e.marker().ReadonlyReason)

	// 导出（closed）：先停浏览器，归档成功后 dirty=false。
	var snap apigen.LinkCtlDesktopStateParams
	o.json(http.MethodPost, "/desktop/browser/start", nil, nil, http.StatusOK, &snap)
	require.Equal(t, apigen.OrbBrowserStatusRunning, snap.Browser)
	assert.True(t, snap.Dirty)
	resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"closed"}}, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, "application/gzip", resp.Header.Get("Content-Type"))
	assert.Equal(t, testProfileA, resp.Header.Get("X-Oryn-Profile-Id"))
	assert.Contains(t, archiveNames(t, b), "Default/Cookies")
	assert.NotContains(t, archiveNames(t, b), "DevToolsActivePort")
	snap = e.rt.desk.snapshot()
	assert.Equal(t, apigen.OrbBrowserStatusStopped, snap.Browser)
	assert.False(t, snap.Dirty)
	_, err = os.Stat(e.profile("Default", "exit-clean"))
	require.NoError(t, err)

	// 导出（live）：SIGSTOP / SIGCONT，浏览器仍在运行，dirty 不变。
	o.json(http.MethodPost, "/desktop/browser/start", nil, nil, http.StatusOK, &snap)
	pid := e.rt.desk.browserPid()
	resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"live"}}, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Contains(t, archiveNames(t, b), "Default/Cookies")
	snap = e.rt.desk.snapshot()
	assert.Equal(t, apigen.OrbBrowserStatusRunning, snap.Browser)
	assert.True(t, snap.Dirty)
	assert.Equal(t, pid, e.rt.desk.browserPid())
	require.Eventually(t, func() bool {
		_, err := os.Stat(e.profile("Default", "sigcont"))
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "browser received SIGCONT after the live export")
	resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"bogus"}}, nil, nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))

	// 空体装载 = 空 Profile（停掉正在运行的浏览器）。
	resp, b = o.do(http.MethodPut, "/desktop/profile", url.Values{"id": {testProfileB}, "name": {"Empty"}, "mode": {"readonly"}}, http.NoBody, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	des, err := os.ReadDir(e.profile())
	require.NoError(t, err)
	assert.Empty(t, des)
	assert.Equal(t, profileMarker{OrbID: "orb-test", ProfileID: testProfileB, Name: "Empty", Mode: apigen.BrowserProfileAccessReadonly, ReadonlyReason: apigen.BrowserProfileReadonlyReasonRequested}, e.marker())
	assert.Zero(t, e.rt.desk.browserPid())

	// DELETE：标记是某个 Profile → 停浏览器并清空。
	o.json(http.MethodPost, "/desktop/browser/start", nil, nil, http.StatusOK, &snap)
	require.NoError(t, os.WriteFile(e.profile("Default", "marker-file"), []byte("x"), 0o600))
	resp, b = o.do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.Zero(t, e.rt.desk.browserPid())
	_, err = os.Stat(e.profile())
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, profileMarker{OrbID: "orb-test"}, e.marker())
	resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"closed"}}, nil, nil)
	assert.Equal(t, "no_profile", o.errCode(resp, b, http.StatusConflict))

	// DELETE：标记已是「不用」→ 不停浏览器、不删目录。
	o.json(http.MethodPost, "/desktop/browser/start", nil, nil, http.StatusOK, &snap)
	pid = e.rt.desk.browserPid()
	require.NoError(t, os.WriteFile(e.profile("Default", "keep-me"), []byte("x"), 0o600))
	resp, b = o.do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.Equal(t, pid, e.rt.desk.browserPid())
	_, err = os.Stat(e.profile("Default", "keep-me"))
	require.NoError(t, err)
}

func TestDesktopProfileModeCtl(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := putProfile(o, testProfileA, "Work", nil, archiveOf(t, map[string]string{"Default/Cookies": "x"}))
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	var snap apigen.LinkCtlDesktopStateParams
	o.json(http.MethodPost, "/desktop/browser/start", nil, nil, http.StatusOK, &snap)
	pid := e.rt.desk.browserPid()

	ctlCode := func(err error) string {
		var ce *link.CtlError
		if assert.ErrorAs(t, err, &ce) {
			return ce.Code
		}
		return ""
	}
	// 旧版控制面会带上接管方的线程标题与 id：orbd 不采用、不落盘、不上报（安全审查 F5）。
	params := map[string]any{"profileId": testProfileB, "mode": "readonly", "reason": "taken_over", "by": "Thread B", "byThreadId": "T-0193a5d2-0000-7000-8000-000000000001"}
	assert.Equal(t, "profile_mismatch", ctlCode(e.cp.call(t, string(apigen.MethodProfileMode), params, nil)))
	params["profileId"] = testProfileA
	params["mode"] = "exclusive"
	assert.Equal(t, link.CodeInvalidParams, ctlCode(e.cp.call(t, string(apigen.MethodProfileMode), params, nil)))

	params["mode"] = "readonly"
	n := e.cp.notifCount()
	var ok apigen.LinkCtlOkResult
	require.NoError(t, e.cp.call(t, string(apigen.MethodProfileMode), params, &ok))
	assert.True(t, ok.Ok)
	m := e.marker()
	assert.Equal(t, apigen.BrowserProfileAccessReadonly, m.Mode)
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonTakenOver, m.ReadonlyReason)
	raw, err := os.ReadFile(e.home(".oryn", "desktop", "profile.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "Thread B")
	assert.NotContains(t, string(raw), "T-0193a5d2")
	// 浏览器照常运行；desktop.state 重发，带只读原因，不带接管方。
	assert.Equal(t, pid, e.rt.desk.browserPid())
	_, raw = e.cp.waitNotif(t, n, string(apigen.MethodDesktopState), nil)
	assert.NotContains(t, string(raw), "Thread B")
	var st apigen.LinkCtlDesktopStateParams
	require.NoError(t, json.Unmarshal(raw, &st))
	assert.Equal(t, apigen.OrbBrowserStatusRunning, st.Browser)
	assert.False(t, st.Dirty)
	require.NotNil(t, st.ProfileMode)
	assert.Equal(t, apigen.BrowserProfileAccessReadonly, *st.ProfileMode)
	require.NotNil(t, st.ReadonlyReason)
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonTakenOver, *st.ReadonlyReason)
	assert.Nil(t, st.ReadonlyBy)
	sr, err := sockCall(t, e.rt, orbsock.MethodDesktopStatus, nil, nil)
	require.NoError(t, err)
	assert.NotContains(t, string(sr), "Thread B", "oryn orb desktop status --json shows nothing about the taker")

	resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"closed"}}, nil, nil)
	assert.Equal(t, "profile_readonly", o.errCode(resp, b, http.StatusConflict))
	assert.Equal(t, pid, e.rt.desk.browserPid(), "a refused export does not stop the browser")

	// keep=1&mode=readonly 保留接管原因。
	resp, b = putProfile(o, testProfileA, "Work", url.Values{"keep": {"1"}, "mode": {"readonly"}}, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonTakenOver, e.marker().ReadonlyReason)
}

func TestDesktopForeignMarkerCleared(t *testing.T) {
	home := t.TempDir()
	writeTree(t, home, map[string]string{
		".oryn/desktop/profile.json":            `{"orbId":"orb-source","profileId":"` + testProfileA + `","name":"Work"}`,
		".oryn/desktop/profile/Default/Cookies": "secret",
		".oryn/desktop/profile.new/Default/x":   "half",
	})
	e := newDeskEnv(t, func(c *Config) { c.Home = home })
	for _, p := range []string{"profile.json", "profile", "profile.new"} {
		_, err := os.Stat(filepath.Join(home, ".oryn", "desktop", p))
		assert.ErrorIs(t, err, os.ErrNotExist, p)
	}
	assert.Nil(t, e.rt.desk.snapshot().ProfileID)

	// 自己的标记保留。
	// 旧版 orbd 写的标记带着接管方的标题：读入后按现在的字段重写；旧版放在 Profile 外的磁盘缓存被删掉。
	home2 := t.TempDir()
	writeTree(t, home2, map[string]string{
		".oryn/desktop/profile.json":                     `{"orbId":"orb-test","profileId":"` + testProfileA + `","name":"Work","mode":"readonly","readonlyReason":"taken_over","readonlyBy":"Thread X"}`,
		".oryn/desktop/profile/Default/Cookies":          "mine",
		".cache/oryn/chromium-cache/Cache_Data/f_000001": "cached response of the previous profile",
	})
	e2 := newDeskEnv(t, func(c *Config) { c.Home = home2 })
	st := e2.rt.desk.snapshot()
	require.NotNil(t, st.ProfileID)
	assert.Equal(t, testProfileA, *st.ProfileID)
	require.NotNil(t, st.ProfileMode)
	assert.Equal(t, apigen.BrowserProfileAccessReadonly, *st.ProfileMode)
	_, err := os.Stat(filepath.Join(home2, ".oryn", "desktop", "profile", "Default", "Cookies"))
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(home2, ".oryn", "desktop", "profile.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "Thread X")
	assert.Contains(t, string(raw), `"readonlyReason":"taken_over"`)
	_, err = os.Stat(filepath.Join(home2, ".cache", "oryn", "chromium-cache"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// ---- 与 omp 的对接 ----

func TestDesktopAgentConfig(t *testing.T) {
	e := newDeskEnv(t, nil)
	overlay := e.home(".oryn", "core", "desktop.yml")
	b, err := os.ReadFile(overlay)
	require.NoError(t, err)
	assert.Contains(t, string(b), "cdpUrl: \"http://"+e.rt.desk.cdpAddr()+"\"")
	assert.Contains(t, string(b), "computer:\n  enabled: true")
	rc, err := os.ReadFile(e.home(".oryn", "desktop", "openbox-rc.xml"))
	require.NoError(t, err)
	assert.Contains(t, string(rc), `<application class="Chromium*">`)

	pid := startAgent(t, e.cp, thread1, nil)
	e.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	args := procArgs(t, pid)
	i := indexOf(args, "--config")
	require.GreaterOrEqual(t, i, 0, "argv %v", args)
	require.Less(t, i+1, len(args))
	assert.Equal(t, overlay, args[i+1])
	assert.Contains(t, procEnv(t, pid), "DISPLAY=:1")
}

// ---- 保留端口 ----

func TestDesktopReservedPorts(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, e.cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{}, &res))

	cdp := e.rt.desk.cdpPort()
	_, rep := openPortal(t, e.cp, cdp)
	require.NotNil(t, rep.Code)
	assert.Equal(t, "forbidden_port", *rep.Code)

	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := o.do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	require.NoError(t, e.rt.desk.startBrowser(context.Background()))
	dev := e.rt.desk.devtoolsPort()
	require.Positive(t, dev)
	_, rep = openPortal(t, e.cp, dev)
	require.NotNil(t, rep.Code)
	assert.Equal(t, "forbidden_port", *rep.Code)

	for name, port := range map[string]int{"oncdp": cdp, "ondev": dev} {
		sv, err := e.rt.svc.Start(orbsock.ServiceStartParams{Name: name, Command: new(sleepCmd), Port: port, Portal: true})
		require.NoError(t, err)
		assert.Equal(t, apigen.OrbServiceStatusFailed, sv.Status, name)
		require.NotNil(t, sv.Error, name)
		assert.Equal(t, "forbidden_port", *sv.Error, name)
		assert.False(t, e.rt.svc.Exposed(port))
	}
}

// ---- orbd.sock ----

func TestDesktopSock(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := putProfile(o, testProfileA, "Work", nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))

	call := func(method string) orbsock.DesktopResult {
		t.Helper()
		raw, err := sockCall(t, e.rt, method, nil, nil)
		require.NoError(t, err)
		var r orbsock.DesktopResult
		require.NoError(t, json.Unmarshal(raw, &r))
		return r
	}
	r := call(orbsock.MethodDesktopStatus)
	assert.Equal(t, ":1", r.Display)
	assert.Equal(t, "http://"+e.rt.desk.cdpAddr(), r.CDPURL)
	assert.Equal(t, "Work", r.ProfileName)
	assert.True(t, r.State.DisplayReady)
	assert.Equal(t, apigen.OrbBrowserStatusStopped, r.State.Browser)

	r = call(orbsock.MethodDesktopBrowserStart)
	assert.Equal(t, apigen.OrbBrowserStatusRunning, r.State.Browser)
	r = call(orbsock.MethodDesktopBrowserStop)
	assert.Equal(t, apigen.OrbBrowserStatusStopped, r.State.Browser)
}

// 并发的前门连接共用同一个浏览器进程。
func TestDesktopConcurrentCDPStartsOnce(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := o.do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			c := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
			resp, err := c.Get("http://" + e.rt.desk.cdpAddr() + "/json/version")
			if assert.NoError(t, err) {
				_ = resp.Body.Close()
				assert.Equal(t, http.StatusOK, resp.StatusCode)
			}
		})
	}
	wg.Wait()
	e.mu.Lock()
	starts := 0
	prev := apigen.OrbBrowserStatusStopped
	for _, s := range e.states {
		if s.Browser == apigen.OrbBrowserStatusStarting && prev != apigen.OrbBrowserStatusStarting {
			starts++
		}
		prev = s.Browser
	}
	e.mu.Unlock()
	assert.Equal(t, 1, starts, "exactly one browser launch")
}
