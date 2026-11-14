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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M8 真机验收 B9 / B13 / B18 / B21 的回归：Profile 保存要拿到浏览器里最新的 Cookie。
// fakedesk 的 chromium 与真 Chromium 一样，内存里的 Cookie 只在正常关闭（Browser.close）时落盘，SIGTERM 会丢。

// archiveFiles 把 tar.gz 解成 名字 → 内容。
func archiveFiles(t *testing.T, b []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		data, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[h.Name] = string(data)
	}
}

// browserCDP 对运行中的浏览器发一条浏览器级 CDP 命令。
func (e *deskEnv) browserCDP(method string, params, result any) {
	e.t.Helper()
	port := e.rt.desk.devtoolsPort()
	require.Positive(e.t, port, "browser running")
	require.NoError(e.t, cdpCall(context.Background(), port, method, params, result))
}

// cookieNames 返回浏览器当前的 Cookie（name=value）。
func (e *deskEnv) cookieNames() []string {
	e.t.Helper()
	var res struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	}
	e.browserCDP("Storage.getCookies", nil, &res)
	var out []string
	for _, c := range res.Cookies {
		out = append(out, c.Name+"="+c.Value)
	}
	return out
}

func (e *deskEnv) startBrowserHTTP(o *orbHTTP) {
	e.t.Helper()
	var snap apigen.LinkCtlDesktopStateParams
	o.json(http.MethodPost, "/desktop/browser/start", nil, nil, http.StatusOK, &snap)
	require.Equal(e.t, apigen.OrbBrowserStatusRunning, snap.Browser)
}

func persistentCookie(name, value string) map[string]any {
	return map[string]any{"name": name, "value": value, "domain": "example.test", "path": "/", "expires": float64(time.Now().Add(24 * time.Hour).Unix())}
}

// 停机 / 删除前的关闭式保存：浏览器经 Browser.close 落盘后再归档（B9 / B13 / B18 / B21）。
func TestDesktopClosedExportFlushesCookies(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := putProfile(o, testProfileA, "Work", nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	e.startBrowserHTTP(o)
	e.browserCDP("Storage.setCookies", map[string]any{"cookies": []any{persistentCookie("sid", "fresh")}}, nil)

	resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"closed"}}, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	files := archiveFiles(t, b)
	assert.Contains(t, files["Default/fake-cookies.json"], `"value":"fresh"`, "the cookie the browser had in memory reached the archive")
	assert.NotContains(t, files, cookieSnapshotRel, "closed export relies on the browser's own flush")
	_, err := os.Stat(e.profile("Default", "exit-term"))
	assert.ErrorIs(t, err, os.ErrNotExist, "no SIGTERM (it loses unflushed cookies)")
}

// 冻结前 / 接管前的活体保存：浏览器继续运行，Cookie 经 CDP 快照进归档；装载后首次启动时回灌。
func TestDesktopLiveExportCookieSnapshot(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := putProfile(o, testProfileA, "Work", nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	e.startBrowserHTTP(o)
	pid := e.rt.desk.browserPid()
	e.browserCDP("Storage.setCookies", map[string]any{"cookies": []any{
		persistentCookie("sid", "live"),
		persistentCookie("a", "first"),
		map[string]any{"name": "tmp", "value": "session-only", "domain": "example.test", "path": "/"},
	}}, nil)

	resp, live := o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"live"}}, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(live))
	assert.Equal(t, pid, e.rt.desk.browserPid(), "the browser keeps running")
	files := archiveFiles(t, live)
	assert.NotContains(t, files, "Default/fake-cookies.json", "nothing was flushed to disk yet")
	snap := files[cookieSnapshotRel]
	require.NotEmpty(t, snap, "cookie snapshot in the live archive")
	var cookies []map[string]any
	require.NoError(t, json.Unmarshal([]byte(snap), &cookies))
	var names []string
	for _, c := range cookies {
		names = append(names, c["name"].(string)+"="+c["value"].(string))
	}
	assert.Equal(t, []string{"a=first", "sid=live"}, names, "persistent cookies only, sorted")
	// 快照的字节只随 Cookie 变化（控制面按归档摘要去重）。
	again, err := liveCookies(context.Background(), e.rt.desk.devtoolsPort())
	require.NoError(t, err)
	assert.Equal(t, snap, string(again))

	// 另一份 Profile 目录（相当于另一台 orb）装载这份活体归档，浏览器第一次启动即带上这些 Cookie。
	resp, b = putProfile(o, testProfileB, "Copy", nil, live)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	_, err = os.Stat(e.profile("Default", "oryn-cookies.json"))
	require.NoError(t, err, "snapshot waits on disk until the browser starts")
	e.startBrowserHTTP(o)
	assert.ElementsMatch(t, []string{"a=first", "sid=live"}, e.cookieNames())
	_, err = os.Stat(e.profile("Default", "oryn-cookies.json"))
	assert.ErrorIs(t, err, os.ErrNotExist, "snapshot consumed")

	// 回灌后关闭式保存：Cookie 已由浏览器自己落盘，归档里不再有快照。
	resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"closed"}}, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	files = archiveFiles(t, b)
	assert.Contains(t, files["Default/fake-cookies.json"], `"value":"live"`)
	assert.NotContains(t, files, cookieSnapshotRel)
}

// 装载后浏览器还没启动过：待回灌的快照原样进归档（活体与关闭式都一样）。
func TestDesktopExportKeepsPendingCookieSnapshot(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	pending := `[{"name":"sid","value":"pending"}]`
	resp, b := putProfile(o, testProfileA, "Work", nil, archiveOf(t, map[string]string{cookieSnapshotRel: pending, "Default/Cookies": "x"}))
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	for _, mode := range []string{"live", "closed"} {
		resp, b = o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {mode}}, nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
		assert.Equal(t, pending, archiveFiles(t, b)[cookieSnapshotRel], mode)
	}
}

// Go 审查 high #3：导出用与还原相同的条目数与解包字节上限，超限在输出之前 413，不交出无法装载的归档。
func TestDesktopExportEnforcesRestoreLimits(t *testing.T) {
	e := newDeskEnv(t, nil)
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := putProfile(o, testProfileA, "Work", nil, archiveOf(t, map[string]string{
		"Default/a": "12345", "Default/b": "67890", "Default/c": "abcde", "Local State": "{}",
	}))
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))

	export := func() (*http.Response, []byte) {
		return o.do(http.MethodGet, "/desktop/profile/export", url.Values{"mode": {"closed"}}, nil, nil)
	}
	oldEntries, oldBytes := maxProfileEntries, maxProfileUnpackedBytes
	t.Cleanup(func() { maxProfileEntries, maxProfileUnpackedBytes = oldEntries, oldBytes })

	maxProfileEntries = 3
	resp, b = export()
	assert.Equal(t, "too_large", o.errCode(resp, b, http.StatusRequestEntityTooLarge), "4 files > 3")
	maxProfileEntries = oldEntries

	maxProfileUnpackedBytes = 16
	resp, b = export()
	assert.Equal(t, "too_large", o.errCode(resp, b, http.StatusRequestEntityTooLarge), "17 bytes > 16")

	// 写归档的过程本身也守着上限（预估之后文件又变大时中途失败，而不是交出超限的归档）。
	err := writeProfileArchive(io.Discard, e.profile(), nil)
	var ae *apiError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "too_large", ae.code)
	err = writeProfileArchive(io.Discard, t.TempDir(), []byte(strings.Repeat("c", 17)))
	require.ErrorAs(t, err, &ae, "the cookie snapshot counts too")

	maxProfileUnpackedBytes = 17
	resp, b = export()
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	dst := t.TempDir() + "/profile"
	require.NoError(t, extractProfileArchive(bytes.NewReader(b), dst), "an exported archive always restores")
}

// 安全审查 F1：user namespace 不可用时退回 --no-sandbox，但不再加 --test-type（保留 Chromium 的警告条）。
func TestDesktopBrowserArgsWithoutUserNS(t *testing.T) {
	e := newDeskEnv(t, func(c *Config) { c.DesktopUserNS = func() bool { return false } })
	e.wait("display ready", displayIs(true))
	o := newOrbHTTP(t, e.cp, e.rt)
	resp, b := o.do(http.MethodDelete, "/desktop/profile", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	e.startBrowserHTTP(o)
	assert.Equal(t, []string{
		"--user-data-dir=" + e.profile(), "--remote-debugging-port=0", "--no-first-run", "--no-default-browser-check",
		"--password-store=basic", "--no-sandbox", "--hide-crash-restore-bubble", "--start-maximized", "about:blank",
	}, procArgs(t, e.rt.desk.browserPid())[1:])
}
