package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

func deskResult(browser apigen.OrbBrowserStatus, ready bool, profile string) orbsock.DesktopResult {
	return orbsock.DesktopResult{
		State:       apigen.LinkCtlDesktopStateParams{DisplayReady: ready, Browser: browser},
		Display:     ":1",
		CDPURL:      "http://127.0.0.1:7822",
		ProfileName: profile,
	}
}

func TestOrbDesktopStatus(t *testing.T) {
	// 测试与假 orbd 的 goroutine 只经 unix socket 交接（race 检测器看不到），共享变量一律加锁。
	var mu sync.Mutex
	var reply orbsock.DesktopResult
	set := func(r orbsock.DesktopResult) { mu.Lock(); reply = r; mu.Unlock() }
	f := startFakeOrbd(t, func(_ context.Context, method string, _ json.RawMessage, _ *orbsock.Responder) (any, error) {
		assert.Equal(t, orbsock.MethodDesktopStatus, method)
		mu.Lock()
		defer mu.Unlock()
		return reply, nil
	})

	set(deskResult(apigen.OrbBrowserStatusRunning, true, "工作账号"))
	var out, errb bytes.Buffer
	assert.Equal(t, exitDesktopOK, desktopStatus(context.Background(), false, &out, &errb))
	assert.Equal(t, "display :1 ready\nbrowser running\ncdp http://127.0.0.1:7822\nprofile 工作账号\n", out.String())
	assert.Empty(t, errb.String())

	stopped := deskResult(apigen.OrbBrowserStatusStopped, false, "")
	set(stopped)
	out.Reset()
	assert.Equal(t, exitDesktopOK, desktopStatus(context.Background(), false, &out, &errb))
	assert.Equal(t, "display :1 not ready\nbrowser stopped\ncdp http://127.0.0.1:7822\nprofile none\n", out.String())

	out.Reset()
	assert.Equal(t, exitDesktopOK, desktopStatus(context.Background(), true, &out, &errb))
	var got orbsock.DesktopResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, stopped, got)

	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.reqs, 3)
	assert.Empty(t, f.reqs[0].Params)
}

func TestOrbDesktopBrowser(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var startErr error
	setErr := func(err error) { mu.Lock(); startErr = err; mu.Unlock() }
	startFakeOrbd(t, func(_ context.Context, method string, _ json.RawMessage, _ *orbsock.Responder) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, method)
		switch method {
		case orbsock.MethodDesktopBrowserStart:
			if startErr != nil {
				return nil, startErr
			}
			return deskResult(apigen.OrbBrowserStatusRunning, true, ""), nil
		case orbsock.MethodDesktopBrowserStop:
			return deskResult(apigen.OrbBrowserStatusStopped, true, ""), nil
		}
		return nil, orbsock.Errorf(orbsock.CodeMethodNotFound, "unexpected %s", method)
	})

	var out, errb bytes.Buffer
	assert.Equal(t, exitDesktopOK, desktopBrowser(context.Background(), true, &out, &errb))
	assert.Equal(t, "browser running\n", out.String())
	out.Reset()
	assert.Equal(t, exitDesktopOK, desktopBrowser(context.Background(), false, &out, &errb))
	assert.Equal(t, "browser stopped\n", out.String())
	mu.Lock()
	assert.Equal(t, []string{orbsock.MethodDesktopBrowserStart, orbsock.MethodDesktopBrowserStop}, calls)
	mu.Unlock()

	// 未就绪 → 1，错误写 stderr。
	setErr(orbsock.Errorf(orbsock.CodeNotReady, "the desktop display or browser profile is not ready yet"))
	out.Reset()
	assert.Equal(t, exitDesktopFailed, desktopBrowser(context.Background(), true, &out, &errb))
	assert.Empty(t, out.String())
	assert.Contains(t, errb.String(), "not_ready")

	// 桌面未启用 → 1。
	setErr(orbsock.Errorf(orbsock.CodeNotSupported, "the desktop is not enabled on this orb"))
	errb.Reset()
	assert.Equal(t, exitDesktopFailed, desktopBrowser(context.Background(), true, &out, &errb))
	assert.Contains(t, errb.String(), "not_supported")
}

func TestOrbDesktopUsageErrors(t *testing.T) {
	// 不在 orb 内 → 2。
	t.Setenv("ORYN_ORB", "")
	var out, errb bytes.Buffer
	assert.Equal(t, exitDesktopUsage, desktopStatus(context.Background(), false, &out, &errb))
	assert.Contains(t, errb.String(), "ORYN_ORB")

	// orbd 不可达（控制口不存在）→ 2。
	t.Setenv("ORYN_ORB", "1")
	t.Setenv("HOME", t.TempDir())
	errb.Reset()
	assert.Equal(t, exitDesktopUsage, desktopBrowser(context.Background(), true, &out, &errb))
	assert.Contains(t, errb.String(), "orbd.sock")
	assert.Empty(t, out.String())
}
