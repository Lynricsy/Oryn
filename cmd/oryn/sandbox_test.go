package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// fakeSandboxAPI 记录调用；handlers 按 op 给结果，logs 在 exec 时依次回放。
type fakeSandboxAPI struct {
	orb      bool
	calls    []fakeSandboxCall
	handlers map[string]func(p map[string]any) (any, error)
	logs     [][2]string
}

type fakeSandboxCall struct {
	op string
	p  map[string]any
}

func (f *fakeSandboxAPI) inOrb() bool { return f.orb }

func (f *fakeSandboxAPI) call(_ context.Context, op string, params, out any, onLog func(stream, data string)) error {
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	f.calls = append(f.calls, fakeSandboxCall{op, p})
	if onLog != nil {
		for _, l := range f.logs {
			onLog(l[0], l[1])
		}
	}
	h := f.handlers[op]
	if h == nil {
		return &exitCodeError{code: 2, err: errors.New("sandbox_not_found: no handler")}
	}
	res, err := h(p)
	if err != nil || out == nil {
		return err
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func runSandbox(t *testing.T, f *fakeSandboxAPI, args ...string) (string, string, error) {
	t.Helper()
	cmd := newSandboxCmd(func() (sandboxAPI, error) { return f, nil })
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func exitCode(err error) int {
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return ec.code
	}
	return -1
}

func TestSandboxUpRoundsDurations(t *testing.T) {
	f := &fakeSandboxAPI{orb: true, handlers: map[string]func(map[string]any) (any, error){
		"create": func(map[string]any) (any, error) { return map[string]any{"sandbox": map[string]any{"id": "sbx_1"}}, nil },
	}}
	out, _, err := runSandbox(t, f, "up", "--network", "none", "--backend", "kvm", "--idle", "90s", "--ttl", "2h")
	require.NoError(t, err)
	assert.Equal(t, "sbx_1\n", out)
	assert.Equal(t, map[string]any{"network": "none", "backend": "kvm", "idleMinutes": float64(2), "ttlMinutes": float64(120)}, f.calls[0].p)
	assert.Equal(t, 1, ceilMinutes(time.Second))
	assert.Equal(t, 60, ceilMinutes(time.Hour))

	_, _, err = runSandbox(t, f, "up", "--network", "lan")
	assert.Equal(t, 2, exitCode(err))
}

func TestSandboxExec(t *testing.T) {
	f := &fakeSandboxAPI{orb: true, logs: [][2]string{{orbsock.StreamStdout, "out\n"}, {orbsock.StreamStderr, "err\n"}},
		handlers: map[string]func(map[string]any) (any, error){
			"exec": func(map[string]any) (any, error) { return map[string]any{"exit": 3, "durationMs": 5}, nil },
		}}
	out, errOut, err := runSandbox(t, f, "exec", "sbx_1", "--env", "A=1", "--", "echo", "hi", "&&", "false")
	assert.Equal(t, 3, exitCode(err), "远端退出码透传")
	assert.Equal(t, "out\n", out)
	assert.Equal(t, "err\n", errOut)
	assert.Equal(t, map[string]any{"sandbox": "sbx_1", "command": "echo hi && false", "env": map[string]any{"A": "1"}}, f.calls[0].p,
		"没给 --timeout 时不带 timeoutSec（控制面取缺省）")

	f.handlers["exec"] = func(map[string]any) (any, error) { return map[string]any{"jobId": "j1"}, nil }
	f.logs = nil
	out, _, err = runSandbox(t, f, "exec", "sbx_1", "--detach", "--timeout", "0", "--", "sleep", "60")
	require.NoError(t, err)
	assert.Equal(t, "j1\n", out)
	assert.Equal(t, true, f.calls[1].p["detach"])
	assert.Equal(t, float64(0), f.calls[1].p["timeoutSec"])

	_, _, err = runSandbox(t, f, "exec", "sbx_1", "--env", "NOEQ", "--", "true")
	assert.Equal(t, 2, exitCode(err))
}

func TestSandboxCpParse(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	id, dir, src, dst, err := parseSandboxCp("src", "sbx_01ABC:proj")
	require.NoError(t, err)
	assert.Equal(t, []string{"sbx_01ABC", "to_sandbox", filepath.Join(wd, "src"), "proj"}, []string{id, dir, src, dst})

	id, dir, src, dst, err = parseSandboxCp("sbx_01ABC:out.txt", "~/got.txt")
	require.NoError(t, err)
	assert.Equal(t, []string{"sbx_01ABC", "from_sandbox", "out.txt", "~/got.txt"}, []string{id, dir, src, dst})

	for _, c := range [][2]string{{"a", "b"}, {"sbx_1:a", "sbx_2:b"}, {"sbx_1:", "b"}} {
		_, _, _, _, err := parseSandboxCp(c[0], c[1])
		assert.Equal(t, 2, exitCode(err), "%v", c)
	}

	f := &fakeSandboxAPI{orb: true, handlers: map[string]func(map[string]any) (any, error){
		"cp": func(map[string]any) (any, error) { return map[string]any{"files": 3, "bytes": 10, "skipped": 1}, nil },
	}}
	out, errOut, err := runSandbox(t, f, "cp", "--gitignore", "--exclude", "*.log", "/home/user/src", "sbx_1:proj")
	require.NoError(t, err)
	assert.Equal(t, "copied 3 files, 10 bytes\n", out)
	assert.Contains(t, errOut, "skipped 1 entries")
	assert.Equal(t, map[string]any{"sandbox": "sbx_1", "direction": "to_sandbox", "source": "/home/user/src", "dest": "proj",
		"exclude": []any{"*.log"}, "gitignore": true}, f.calls[0].p)
}

func TestSandboxOutsideOrb(t *testing.T) {
	f := &fakeSandboxAPI{orb: false, handlers: map[string]func(map[string]any) (any, error){
		"list": func(map[string]any) (any, error) { return map[string]any{"sandboxes": []any{}}, nil },
	}}
	for _, args := range [][]string{
		{"up", "--network", "none"}, {"exec", "sbx_1", "--", "true"}, {"cp", "a", "sbx_1:b"}, {"net", "sbx_1", "none"},
		{"jobs", "sbx_1"}, {"extend", "sbx_1", "--ttl", "1h"},
	} {
		_, _, err := runSandbox(t, f, args...)
		assert.Equal(t, 2, exitCode(err), "%v", args)
		assert.ErrorContains(t, err, "只能在 orb 内使用", "%v", args)
	}
	assert.Empty(t, f.calls)
	out, _, err := runSandbox(t, f, "ls", "--thread", "T-1")
	require.NoError(t, err)
	assert.Contains(t, out, "ID")
	assert.Equal(t, "T-1", f.calls[0].p["thread"])
}
