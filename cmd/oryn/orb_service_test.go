package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

const testPublicURL = "https://t-0123456789ab-p20001.orb.test"

func svcOK(name string) apigen.OrbService {
	return apigen.OrbService{
		Name: name, Source: apigen.OrbServiceSourceDynamic, Command: "python3 -m http.server", Status: apigen.OrbServiceStatusRunning,
		Port: new(20001), Pid: new(42), PublicURL: new(testPublicURL), Links: []apigen.OrbPortalLink{{Title: name, Path: "/"}},
	}
}

func TestOrbServiceStartDynamic(t *testing.T) {
	t.Setenv("ORYN_THREAD_ID", "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab")
	f := startFakeOrbd(t, func(_ context.Context, method string, _ json.RawMessage, out *orbsock.Responder) (any, error) {
		assert.Equal(t, orbsock.MethodServiceStart, method)
		out.Log(orbsock.StreamSystem, "[orbd] starting demo (port 20001)\n")
		return orbsock.ServiceResult{Service: svcOK("demo")}, nil
	})
	var out, errb bytes.Buffer
	code := serviceStart(context.Background(), serviceStartOpts{
		name: "demo", command: `python3 -m http.server "$PORT"`, hasCommand: true, portal: true, env: []string{"A=1", "B=x=y"},
	}, &out, &errb)
	assert.Equal(t, exitServiceOK, code)
	assert.Equal(t, testPublicURL+"/\n", out.String(), "stdout 只有带结尾 / 的 portal 地址")
	assert.Contains(t, errb.String(), "[orbd] starting demo (port 20001)")
	assert.Contains(t, errb.String(), "demo: running, port 20001\n")

	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.reqs, 1)
	assert.JSONEq(t, `{"name":"demo","command":"python3 -m http.server \"$PORT\"","portal":true,
		"env":{"A":"1","B":"x=y"},"threadId":"T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"}`, string(f.reqs[0].Params))
}

func TestOrbServiceStartOutcomes(t *testing.T) {
	var mu sync.Mutex
	var reply apigen.OrbService
	var callErr error
	set := func(o apigen.OrbService, err error) {
		mu.Lock()
		defer mu.Unlock()
		reply, callErr = o, err
	}
	f := startFakeOrbd(t, func(context.Context, string, json.RawMessage, *orbsock.Responder) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if callErr != nil {
			return nil, callErr
		}
		return orbsock.ServiceResult{Service: reply}, nil
	})

	// 已声明的服务（无 --command）：参数只有名字。
	set(svcOK("web"), nil)
	var out, errb bytes.Buffer
	assert.Equal(t, exitServiceOK, serviceStart(context.Background(), serviceStartOpts{name: "web"}, &out, &errb))
	f.mu.Lock()
	assert.JSONEq(t, `{"name":"web"}`, string(f.reqs[0].Params))
	f.mu.Unlock()

	// 不是 running → 1；failed 的错误写在状态行里，不打印 URL。
	set(apigen.OrbService{Name: "web", Status: apigen.OrbServiceStatusFailed, Port: new(3000), Error: new("port_conflict"), Links: []apigen.OrbPortalLink{}}, nil)
	out.Reset()
	errb.Reset()
	assert.Equal(t, exitServiceNotRunning, serviceStart(context.Background(), serviceStartOpts{name: "web"}, &out, &errb))
	assert.Empty(t, out.String())
	assert.Contains(t, errb.String(), "web: failed, port 3000 (port_conflict)")

	// portal 未启用：服务照常运行，stderr 提示。
	noURL := svcOK("web")
	noURL.PublicURL = nil
	set(noURL, nil)
	out.Reset()
	errb.Reset()
	assert.Equal(t, exitServiceOK, serviceStart(context.Background(), serviceStartOpts{name: "web"}, &out, &errb))
	assert.Empty(t, out.String())
	assert.Contains(t, errb.String(), "portal 未启用")

	// 调用错误 → 2。
	set(apigen.OrbService{}, orbsock.Errorf(orbsock.CodeServiceNotFound, "no service named \"web\""))
	errb.Reset()
	assert.Equal(t, exitServiceUsage, serviceStart(context.Background(), serviceStartOpts{name: "web"}, &out, &errb))
	assert.Contains(t, errb.String(), "service_not_found")

	// 本地就能发现的用法错误不调用 orbd。
	f.mu.Lock()
	n := len(f.reqs)
	f.mu.Unlock()
	errb.Reset()
	assert.Equal(t, exitServiceUsage, serviceStart(context.Background(), serviceStartOpts{command: "x", hasCommand: true}, &out, &errb))
	assert.Contains(t, errb.String(), "服务名")
	assert.Equal(t, exitServiceUsage, serviceStart(context.Background(), serviceStartOpts{name: "web", command: "x", hasCommand: true, env: []string{"NOEQ"}}, &out, &errb))
	f.mu.Lock()
	assert.Len(t, f.reqs, n)
	f.mu.Unlock()
}

func TestOrbServiceEnsure(t *testing.T) {
	var mu sync.Mutex
	var res orbsock.ServiceListResult
	f := startFakeOrbd(t, func(_ context.Context, method string, _ json.RawMessage, out *orbsock.Responder) (any, error) {
		assert.Equal(t, orbsock.MethodServiceEnsure, method)
		out.Log(orbsock.StreamSystem, "[orbd] starting web (port 20001)\n")
		mu.Lock()
		defer mu.Unlock()
		return res, nil
	})
	stopped := apigen.OrbService{Name: "old", Status: apigen.OrbServiceStatusStopped, Port: new(20002), Links: []apigen.OrbPortalLink{}}
	mu.Lock()
	res = orbsock.ServiceListResult{Services: []apigen.OrbService{svcOK("web"), stopped}, FileError: "bad yaml"}
	mu.Unlock()
	var out, errb bytes.Buffer
	assert.Equal(t, exitServiceOK, serviceStart(context.Background(), serviceStartOpts{}, &out, &errb), "用户停掉的服务不算失败")
	assert.Equal(t, testPublicURL+"/\n", out.String())
	assert.Contains(t, errb.String(), "web: running, port 20001")
	assert.Contains(t, errb.String(), "old: stopped, port 20002")
	assert.Contains(t, errb.String(), "services.yaml: bad yaml")
	f.mu.Lock()
	assert.Empty(t, f.reqs[0].Params, "ensure 无参数")
	f.mu.Unlock()

	mu.Lock()
	res.Services = append(res.Services, apigen.OrbService{Name: "bad", Status: apigen.OrbServiceStatusBackoff, Links: []apigen.OrbPortalLink{}})
	mu.Unlock()
	assert.Equal(t, exitServiceNotRunning, serviceStart(context.Background(), serviceStartOpts{}, &out, &errb))
}

func TestOrbServiceStopRestartList(t *testing.T) {
	f := startFakeOrbd(t, func(_ context.Context, method string, _ json.RawMessage, _ *orbsock.Responder) (any, error) {
		switch method {
		case orbsock.MethodServiceStop:
			o := svcOK("demo")
			o.Status, o.Pid = apigen.OrbServiceStatusStopped, nil
			return orbsock.ServiceResult{Service: o}, nil
		case orbsock.MethodServiceRestart:
			return orbsock.ServiceResult{Service: svcOK("demo")}, nil
		case orbsock.MethodServiceList:
			plain := apigen.OrbService{Name: "worker", Source: apigen.OrbServiceSourceFile, Status: apigen.OrbServiceStatusBackoff, Restarts: 2, Links: []apigen.OrbPortalLink{}}
			return orbsock.ServiceListResult{Services: []apigen.OrbService{svcOK("demo"), plain}}, nil
		}
		return nil, orbsock.Errorf(orbsock.CodeMethodNotFound, "%s", method)
	})
	var out, errb bytes.Buffer
	assert.Equal(t, exitServiceOK, serviceStop(context.Background(), "demo", true, &out, &errb))
	assert.Empty(t, out.String(), "停止的服务不打印 URL")
	assert.Contains(t, errb.String(), "demo: stopped, port 20001")
	assert.Contains(t, errb.String(), "已删除")

	out.Reset()
	errb.Reset()
	assert.Equal(t, exitServiceOK, serviceRestart(context.Background(), "demo", &out, &errb))
	assert.Equal(t, testPublicURL+"/\n", out.String())

	out.Reset()
	assert.Equal(t, exitServiceOK, serviceList(context.Background(), false, &out, &errb))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, []string{"NAME", "STATUS", "PORT", "RESTARTS", "SOURCE", "URL"}, strings.Fields(lines[0]))
	assert.Equal(t, []string{"demo", "running", "20001", "0", "dynamic", testPublicURL + "/"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"worker", "backoff", "-", "2", "file", "-"}, strings.Fields(lines[2]))

	out.Reset()
	assert.Equal(t, exitServiceOK, serviceList(context.Background(), true, &out, &errb))
	var lr orbsock.ServiceListResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &lr))
	assert.Equal(t, apigen.OrbServiceStatusRunning, lr.Services[0].Status)

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.JSONEq(t, `{"name":"demo","remove":true}`, string(f.reqs[0].Params))
	assert.JSONEq(t, `{"name":"demo"}`, string(f.reqs[1].Params))
}

func TestOrbServiceCallErrors(t *testing.T) {
	t.Setenv("ORYN_ORB", "")
	var out, errb bytes.Buffer
	assert.Equal(t, exitServiceUsage, serviceList(context.Background(), false, &out, &errb))
	assert.Contains(t, errb.String(), "只能在 Oryn orb 内使用")
	assert.Equal(t, exitServiceUsage, serviceStop(context.Background(), "x", false, &out, &errb))
	require.ErrorIs(t, serviceLogs(context.Background(), "x", 10, false, &out), errNotInOrb)

	t.Setenv("ORYN_ORB", "1")
	t.Setenv("HOME", t.TempDir())
	errb.Reset()
	assert.Equal(t, exitServiceUsage, serviceRestart(context.Background(), "x", &out, &errb))
	assert.Contains(t, errb.String(), "orbd.sock")
}

// syncBuffer 是并发安全的 bytes.Buffer（logs -f 的写方与断言方不在同一 goroutine）。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestOrbServiceLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORYN_ORB", "1")
	var out bytes.Buffer
	require.ErrorContains(t, serviceLogs(context.Background(), "web", 10, false, &out), "还没有服务 web 的日志")
	require.ErrorContains(t, serviceLogs(context.Background(), "../etc", 10, false, &out), "无效的服务名")

	path := filepath.Join(home, ".cache", "oryn", "logs", "services", "web.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("[orbd] starting web (port 1)\nline a\nline b\n"), 0o644))
	require.NoError(t, serviceLogs(context.Background(), "web", 2, false, &out))
	assert.Equal(t, "line a\nline b\n", out.String())

	// -f：先输出末尾，再输出新增内容；日志轮转后从新文件开头继续。
	old := serviceLogPoll
	serviceLogPoll = 5 * time.Millisecond
	t.Cleanup(func() { serviceLogPoll = old })
	ctx, cancel := context.WithCancel(context.Background())
	var fo syncBuffer
	done := make(chan error, 1)
	go func() { done <- serviceLogs(ctx, "web", 1, true, &fo) }()
	require.Eventually(t, func() bool { return fo.String() == "line b\n" }, 10*time.Second, 5*time.Millisecond)
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = fh.WriteString("line c\n")
	require.NoError(t, err)
	require.NoError(t, fh.Close())
	require.Eventually(t, func() bool { return fo.String() == "line b\nline c\n" }, 10*time.Second, 5*time.Millisecond)
	require.NoError(t, os.Rename(path, path+".1"))
	require.NoError(t, os.WriteFile(path, []byte("new\n"), 0o644))
	require.Eventually(t, func() bool { return fo.String() == "line b\nline c\nnew\n" }, 10*time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}

// logs -f 以文件身份识别轮转：新文件与旧文件等长或更长时也从新文件开头读，旧文件轮转前的尾部不丢。
// tick 通道无缓冲：下一次发送成功即上一轮读取已完成，断言不依赖调度时机。
func TestFollowLogRotationByIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web.log")
	require.NoError(t, os.WriteFile(path, []byte("aaaa\n"), 0o644))
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	_, err = io.ReadAll(f)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- followLog(ctx, path, f, tick, &out) }()
	poll := func() {
		tick <- time.Time{}
		tick <- time.Time{} // 第二次发送被接收 = 第一次的那一轮已读完
	}
	rotate := func(content string) {
		require.NoError(t, os.Rename(path, path+".1"))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	appendTo := func(p, s string) {
		fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, err = fh.WriteString(s)
		require.NoError(t, err)
		require.NoError(t, fh.Close())
	}

	rotate("bbbb\n") // 与旧文件等长
	poll()
	assert.Equal(t, "bbbb\n", out.String(), "等长的新文件从头读")

	rotate("cccccccccccccccc\n") // 比已读位置更长
	poll()
	assert.Equal(t, "bbbb\ncccccccccccccccc\n", out.String(), "更长的新文件从头读，不跳过开头")

	appendTo(path, "tail\n") // 轮转前写进旧文件、还没读到的尾部
	rotate("dd\n")
	poll()
	assert.Equal(t, "bbbb\ncccccccccccccccc\ntail\ndd\n", out.String())

	appendTo(path, "more\n")
	poll()
	assert.Equal(t, "bbbb\ncccccccccccccccc\ntail\ndd\nmore\n", out.String(), "同一文件追加照常读")

	cancel()
	require.NoError(t, <-done)
}
