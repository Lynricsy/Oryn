package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// fakeOrbd 在临时 $HOME 的 orbd.sock 上服务，记录收到的请求。
type fakeOrbd struct {
	mu   sync.Mutex
	reqs []orbsock.Request
}

func startFakeOrbd(t *testing.T, h orbsock.Handler) *fakeOrbd {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORYN_ORB", "1")
	f := &fakeOrbd{}
	ln, err := orbsock.Listen(orbsock.Path(home))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		orbsock.Serve(ctx, ln, func(ctx context.Context, method string, params json.RawMessage, out *orbsock.Responder) (any, error) {
			f.mu.Lock()
			f.reqs = append(f.reqs, orbsock.Request{Method: method, Params: params})
			f.mu.Unlock()
			return h(ctx, method, params, out)
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	return f
}

func TestOrbSetupRunExitCodes(t *testing.T) {
	startFakeOrbd(t, func(_ context.Context, method string, params json.RawMessage, out *orbsock.Responder) (any, error) {
		var p orbsock.RerunParams
		_ = json.Unmarshal(params, &p)
		if !p.Resume {
			out.Log(orbsock.StreamStdout, "installing deps\n")
			out.Log(orbsock.StreamStderr, "warn: slow\n")
			return apigen.LinkCtlSetupResultParams{Phase: apigen.SetupPhaseSetup, Trigger: apigen.SetupTriggerManual, Ok: true, SetupRan: true, HasRepoScript: true, DurationMs: 1500}, nil
		}
		return apigen.LinkCtlSetupResultParams{Phase: apigen.SetupPhaseResume, Trigger: apigen.SetupTriggerManual, Ok: false, Error: new(".oryn/resume: exit status 4")}, nil
	})
	var out, errb bytes.Buffer
	assert.Equal(t, 0, setupRun(context.Background(), false, &out, &errb))
	assert.Equal(t, "installing deps\n", out.String(), "stdout 只含脚本 stdout")
	assert.Contains(t, errb.String(), "warn: slow")
	assert.Contains(t, errb.String(), "setup 成功")

	out.Reset()
	errb.Reset()
	assert.Equal(t, 1, setupRun(context.Background(), true, &out, &errb), "脚本失败退出 1")
	assert.Contains(t, errb.String(), "exit status 4")
}

func TestOrbSetupRunCallErrors(t *testing.T) {
	startFakeOrbd(t, func(context.Context, string, json.RawMessage, *orbsock.Responder) (any, error) {
		return nil, orbsock.Errorf(orbsock.CodeBusy, "another setup or resume run is in progress")
	})
	var out, errb bytes.Buffer
	assert.Equal(t, 2, setupRun(context.Background(), false, &out, &errb), "busy 是调用错误")
	assert.Contains(t, errb.String(), "busy")

	// 不在 orb 内。
	t.Setenv("ORYN_ORB", "")
	errb.Reset()
	assert.Equal(t, 2, setupRun(context.Background(), false, &out, &errb))
	assert.Contains(t, errb.String(), "只能在 Oryn orb 内使用")

	// orb 内但 orbd 未运行。
	t.Setenv("ORYN_ORB", "1")
	t.Setenv("HOME", t.TempDir())
	errb.Reset()
	assert.Equal(t, 2, setupRun(context.Background(), false, &out, &errb))
	assert.Contains(t, errb.String(), "orbd.sock")
}

func TestOrbSetupProjectScriptSet(t *testing.T) {
	f := startFakeOrbd(t, func(_ context.Context, method string, params json.RawMessage, _ *orbsock.Responder) (any, error) {
		var p apigen.LinkCtlProjectScriptsSetParams
		_ = json.Unmarshal(params, &p)
		return apigen.LinkCtlProjectScripts{ProjectID: "prj_1", ProjectName: "demo", Resume: p.Resume}, nil
	})
	var out bytes.Buffer
	require.NoError(t, projectScriptSet(context.Background(), "resume", "-", strings.NewReader("echo hi\n"), &out))
	require.Error(t, projectScriptSet(context.Background(), "bogus", "-", strings.NewReader(""), &out))
	require.Error(t, projectScriptSet(context.Background(), "setup", "", strings.NewReader(""), &out), "--file 必填")

	file := filepath.Join(t.TempDir(), "empty.sh")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	require.NoError(t, projectScriptSet(context.Background(), "setup", file, nil, &out))
	assert.Contains(t, out.String(), "已清除")

	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.reqs, 2)
	assert.Equal(t, orbsock.MethodProjectScriptsSet, f.reqs[0].Method)
	assert.JSONEq(t, `{"resume":"echo hi\n"}`, string(f.reqs[0].Params), "只改 resume，setup 省略")
	assert.JSONEq(t, `{"setup":""}`, string(f.reqs[1].Params), "空文件 = 清除（空串而不是省略）")
}

func TestTailLines(t *testing.T) {
	assert.Equal(t, "c\n", tailLines("a\nb\nc\n", 1))
	assert.Equal(t, "b\nc\n", tailLines("a\nb\nc\n", 2))
	assert.Equal(t, "a\nb\nc\n", tailLines("a\nb\nc\n", 5))
	assert.Equal(t, "b\nc", tailLines("a\nb\nc", 2), "无结尾换行")
	assert.Equal(t, "a\nb\n", tailLines("a\nb\n", 0))
}
