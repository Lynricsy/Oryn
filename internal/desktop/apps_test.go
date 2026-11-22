package desktop

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// appsOrbd 让 orbd 按 /desktop/apps|windows 路由回 resp（nil = 回带一个应用的状态快照）。
func appsOrbd(o *fakeOrbd, resp func(req *http.Request) *http.Response) {
	o.extra = func(req *http.Request) *http.Response {
		if !strings.HasPrefix(req.URL.Path, "/desktop/apps/") && !strings.HasPrefix(req.URL.Path, "/desktop/windows/") {
			return nil
		}
		if resp != nil {
			return resp(req)
		}
		return jsonResp(200, apigen.LinkCtlDesktopStateParams{
			DisplayReady: true, Browser: apigen.OrbBrowserStatusStopped,
			Apps: []apigen.OrbDesktopApp{{ID: "vscode", Name: "VS Code", Launching: true}},
		})
	}
}

func iconResp(ct string, body []byte, length int64) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, ContentLength: length, Body: io.NopCloser(bytes.NewReader(body))}
}

// 启动与窗口操作：要 desktop-apps 并唤醒；回包快照交给 ApplyDesktopReply；路径按参数拼出。
func TestLaunchAppAndWindowAction(t *testing.T) {
	e := newEnv(t)
	o := e.orb("")
	od := e.fo.orbd(o)
	appsOrbd(od, nil)

	require.NoError(t, e.svc.LaunchApp(e.ctx, o, "vscode"))
	assert.Equal(t, "POST /desktop/apps/vscode/launch?", last(od.requests()))
	assert.Equal(t, o+" desktop-apps true", e.fo.ensured[len(e.fo.ensured)-1])
	require.Len(t, e.fo.applied[o].Apps, 1)
	assert.True(t, e.fo.applied[o].Apps[0].Launching)

	require.NoError(t, e.svc.WindowAction(e.ctx, o, "4194307", "minimize"))
	assert.Equal(t, "POST /desktop/windows/4194307/minimize?", last(od.requests()))
	assert.Equal(t, o+" desktop-apps true", e.fo.ensured[len(e.fo.ensured)-1])

	// 参数不合法：400，不发给 orbd
	n := len(od.requests())
	for _, err := range []error{
		e.svc.LaunchApp(e.ctx, o, "../profile"),
		e.svc.WindowAction(e.ctx, o, "abc", "close"),
		e.svc.WindowAction(e.ctx, o, "1", "maximize"),
	} {
		var de *Error
		require.ErrorAs(t, err, &de)
		assert.Equal(t, http.StatusBadRequest, de.Status)
		assert.Equal(t, "invalid_request", de.Code)
	}
	assert.Len(t, od.requests(), n)
}

// 旧 orbd（hello 没有 desktop-apps）：三个方法都回 ErrOrbdOutdated，不发请求。
func TestDesktopAppsOutdated(t *testing.T) {
	e := newEnv(t)
	o := e.orb("")
	e.fo.lacks = map[string]string{o: orbs.FeatureDesktopApps}
	require.ErrorIs(t, e.svc.LaunchApp(e.ctx, o, "vscode"), orbs.ErrOrbdOutdated)
	require.ErrorIs(t, e.svc.WindowAction(e.ctx, o, "1", "close"), orbs.ErrOrbdOutdated)
	_, _, err := e.svc.AppIcon(e.ctx, o, "vscode")
	require.ErrorIs(t, err, orbs.ErrOrbdOutdated)
	assert.Empty(t, e.fo.orbd(o).requests())
}

// orbd 的业务错误映射成同码的 *Error；not_ready 同 SetBrowser 回 503 orb_unavailable；其它原样返回。
func TestDesktopAppsErrorMapping(t *testing.T) {
	e := newEnv(t)
	o := e.orb("")
	od := e.fo.orbd(o)
	for _, c := range []struct {
		status int
		code   string
		want   int
		wcode  string
	}{
		{409, "control_agent", 409, "control_agent"},
		{404, "app_not_found", 404, "app_not_found"},
		{404, "window_not_found", 404, "window_not_found"},
		{400, "invalid_request", 400, "invalid_request"},
		{409, "not_ready", 503, "orb_unavailable"},
	} {
		appsOrbd(od, func(*http.Request) *http.Response { return errResp(c.status, c.code) })
		for _, err := range []error{e.svc.LaunchApp(e.ctx, o, "vscode"), e.svc.WindowAction(e.ctx, o, "7", "activate")} {
			var de *Error
			require.ErrorAs(t, err, &de, c.code)
			assert.Equal(t, c.want, de.Status, c.code)
			assert.Equal(t, c.wcode, de.Code, c.code)
		}
	}
	appsOrbd(od, func(*http.Request) *http.Response { return errResp(500, "failed") })
	err := e.svc.LaunchApp(e.ctx, o, "vscode")
	var de *Error
	assert.False(t, errors.As(err, &de), "orbd 的 failed 不映射成业务错误")
	assert.Equal(t, "failed", orbdCode(err))
	// orbd 没有这些路由（404 not_found）：视同旧 orbd
	appsOrbd(od, func(*http.Request) *http.Response { return errResp(404, "not_found") })
	require.ErrorIs(t, e.svc.LaunchApp(e.ctx, o, "vscode"), orbs.ErrOrbdOutdated)
}

// 图标：不唤醒；只转发 image/svg+xml 与 image/png（去掉参数），没声明长度或超过上限 → 502；orb 不可用 → 409 orb_unavailable。
func TestAppIcon(t *testing.T) {
	e := newEnv(t)
	o := e.orb("")
	od := e.fo.orbd(o)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	png := []byte("\x89PNG\r\n\x1a\nxx")
	var next *http.Response
	od.extra = func(req *http.Request) *http.Response {
		if req.URL.Path == "/desktop/apps/vscode/icon" && req.Method == http.MethodGet {
			return next
		}
		return errResp(404, "app_not_found")
	}

	next = iconResp("image/svg+xml; charset=utf-8", svg, int64(len(svg)))
	ct, body, err := e.svc.AppIcon(e.ctx, o, "vscode")
	require.NoError(t, err)
	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	assert.Equal(t, "image/svg+xml", ct)
	assert.Equal(t, svg, got)
	assert.Equal(t, o+" desktop-apps false", e.fo.ensured[len(e.fo.ensured)-1])

	next = iconResp("image/png", png, int64(len(png)))
	ct, body, err = e.svc.AppIcon(e.ctx, o, "vscode")
	require.NoError(t, err)
	got, _ = io.ReadAll(body)
	require.NoError(t, body.Close())
	assert.Equal(t, "image/png", ct)
	assert.Equal(t, png, got)

	old := maxIconBytes
	maxIconBytes = 8
	t.Cleanup(func() { maxIconBytes = old })
	for name, r := range map[string]*http.Response{
		"html":      iconResp("text/html", svg[:4], 4),
		"无类型":       iconResp("", svg[:4], 4),
		"svg 加别的类型": iconResp("image/svg+xml, text/html", svg[:4], 4),
		"未声明长度":     iconResp("image/png", png[:4], -1),
		"超过上限":      iconResp("image/png", png, int64(len(png))),
	} {
		next = r
		_, _, err := e.svc.AppIcon(e.ctx, o, "vscode")
		var de *Error
		require.ErrorAs(t, err, &de, name)
		assert.Equal(t, http.StatusBadGateway, de.Status, name)
		assert.Equal(t, "orbd_bad_response", de.Code, name)
	}

	_, _, err = e.svc.AppIcon(e.ctx, o, "nope")
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, http.StatusNotFound, de.Status)
	assert.Equal(t, "app_not_found", de.Code)

	_, _, err = e.svc.AppIcon(e.ctx, o, "Bad/..")
	require.ErrorAs(t, err, &de)
	assert.Equal(t, http.StatusBadRequest, de.Status)

	e.fo.mu.Lock()
	e.fo.usable[o] = orbs.ErrOrbNotReady
	e.fo.mu.Unlock()
	n := len(od.requests())
	_, _, err = e.svc.AppIcon(e.ctx, o, "vscode")
	require.ErrorAs(t, err, &de)
	assert.Equal(t, http.StatusConflict, de.Status)
	assert.Equal(t, "orb_unavailable", de.Code)
	assert.Len(t, od.requests(), n, "不可用时不发请求")
}
