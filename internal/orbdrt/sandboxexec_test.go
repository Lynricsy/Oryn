package orbdrt

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func sbxExec(t *testing.T, o *orbHTTP, req apigen.OrbSandboxExecRequest) execResult {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	resp, b := o.do(http.MethodPost, "/sandbox/exec", nil, bytes.NewReader(body), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, "application/x-ndjson", resp.Header.Get("Content-Type"))
	var res execResult
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var line struct {
			S, D string
			Exit *int
		}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line), sc.Text())
		switch {
		case line.Exit != nil:
			res.exit = line.Exit
		case line.S == "o":
			res.stdout += line.D
		case line.S == "e":
			res.stderr += line.D
		}
	}
	return res
}

func sbxExecErr(t *testing.T, o *orbHTTP, req any) string {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	resp, b := o.do(http.MethodPost, "/sandbox/exec", nil, bytes.NewReader(body), nil)
	return o.errCode(resp, b, http.StatusBadRequest)
}

func TestSandboxExec(t *testing.T) {
	cp, rt := startSandbox(t)
	o := newOrbHTTP(t, cp, rt)

	res := sbxExec(t, o, apigen.OrbSandboxExecRequest{
		Command: "echo out; echo err >&2; echo $FOO; exit 3", Env: map[string]string{"FOO": "bar"},
	})
	require.NotNil(t, res.exit)
	assert.Equal(t, 3, *res.exit)
	assert.Equal(t, "out\nbar\n", res.stdout)
	assert.Equal(t, "err\n", res.stderr)

	start := time.Now()
	res = sbxExec(t, o, apigen.OrbSandboxExecRequest{Command: "echo start; exec sleep 30", TimeoutSec: 1})
	require.NotNil(t, res.exit)
	assert.Equal(t, boxExecTimeoutExit, *res.exit)
	assert.Equal(t, "start\n", res.stdout)
	assert.Less(t, time.Since(start), 10*time.Second)

	for name, req := range map[string]apigen.OrbSandboxExecRequest{
		"empty":         {Command: " "},
		"long":          {Command: strings.Repeat("x", sandboxExecMaxCommand+1)},
		"bad env":       {Command: "true", Env: map[string]string{"1BAD": "x"}},
		"timeout":       {Command: "true", TimeoutSec: sandboxExecMaxTimeout + 1},
		"job timeout":   {Command: "true", TimeoutSec: sandboxJobMaxTimeout + 1, Detach: new(true)},
		"negative":      {Command: "true", TimeoutSec: -1},
		"too many envs": {Command: "true", Env: manyEnv(sandboxExecMaxEnv + 1)},
	} {
		assert.Equal(t, "invalid_request", sbxExecErr(t, o, req), name)
	}
	assert.Equal(t, "invalid_path", sbxExecErr(t, o, apigen.OrbSandboxExecRequest{Command: "true", Cwd: "/etc"}))

	// 普通 orb 上没有 /sandbox/*。
	cp2 := newFakeCP(t)
	rt2 := startRuntime(t, cp2, nil)
	o2 := newOrbHTTP(t, cp2, rt2)
	resp, b := o2.do(http.MethodPost, "/sandbox/exec", nil, strings.NewReader(`{"command":"true"}`), nil)
	assert.Equal(t, "not_found", o2.errCode(resp, b, http.StatusNotFound))
}

func manyEnv(n int) map[string]string {
	m := map[string]string{}
	for i := range n {
		m[fmt.Sprintf("K%d", i)] = "v"
	}
	return m
}

func startJob(t *testing.T, o *orbHTTP, req apigen.OrbSandboxExecRequest) (string, int, []byte) {
	t.Helper()
	req.Detach = new(true)
	body, err := json.Marshal(req)
	require.NoError(t, err)
	resp, b := o.do(http.MethodPost, "/sandbox/exec", nil, bytes.NewReader(body), nil)
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, b
	}
	var js apigen.OrbSandboxJobStart
	require.NoError(t, json.Unmarshal(b, &js))
	return js.JobID, resp.StatusCode, b
}

func jobList(t *testing.T, o *orbHTTP) map[string]apigen.OrbSandboxJob {
	t.Helper()
	var l apigen.OrbSandboxJobList
	o.json(http.MethodGet, "/sandbox/jobs", nil, nil, http.StatusOK, &l)
	m := map[string]apigen.OrbSandboxJob{}
	for _, j := range l.Jobs {
		m[j.ID] = j
	}
	return m
}

func jobLog(t *testing.T, o *orbHTTP, id string, q url.Values) apigen.OrbSandboxJobLog {
	t.Helper()
	var l apigen.OrbSandboxJobLog
	o.json(http.MethodGet, "/sandbox/jobs/"+id+"/log", q, nil, http.StatusOK, &l)
	return l
}

func TestSandboxJobs(t *testing.T) {
	cp, rt := startSandbox(t)
	o := newOrbHTTP(t, cp, rt)

	id, code, b := startJob(t, o, apigen.OrbSandboxExecRequest{Command: "echo a; echo b >&2; sleep 60", Env: map[string]string{"X": "1"}})
	require.Equal(t, http.StatusOK, code, string(b))
	assert.Equal(t, "j1", id)
	assert.Equal(t, apigen.OrbSandboxJobRunning, jobList(t, o)[id].State)
	require.Eventually(t, func() bool {
		return strings.Contains(jobLog(t, o, id, nil).Text, "a\nb\n")
	}, 10*time.Second, 50*time.Millisecond)
	l := jobLog(t, o, id, url.Values{"offset": {"2"}})
	assert.Equal(t, "b\n", l.Text)
	assert.Equal(t, int64(2), l.Offset)
	assert.Equal(t, int64(4), l.NextOffset)
	assert.False(t, l.EOF)

	o.json(http.MethodPost, "/sandbox/jobs/"+id+"/kill", nil, nil, http.StatusOK, nil)
	j := jobList(t, o)[id]
	assert.Equal(t, apigen.OrbSandboxJobExited, j.State)
	require.NotNil(t, j.Exit)
	assert.Equal(t, 128+9, *j.Exit)
	assert.NotNil(t, j.EndedAt)
	assert.Equal(t, int64(4), j.LogBytes)
	l = jobLog(t, o, id, nil)
	assert.True(t, l.EOF)
	assert.Equal(t, apigen.OrbSandboxJobExited, l.State)

	resp, b := o.do(http.MethodPost, "/sandbox/jobs/j99/kill", nil, nil, nil)
	assert.Equal(t, "job_not_found", o.errCode(resp, b, http.StatusNotFound))
	resp, b = o.do(http.MethodGet, "/sandbox/jobs/j1/log", url.Values{"offset": {"999"}}, nil, nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))

	// 同时至多 16 个在运行。
	for range sandboxMaxRunningJobs {
		_, code, b := startJob(t, o, apigen.OrbSandboxExecRequest{Command: "sleep 60"})
		require.Equal(t, http.StatusOK, code, string(b))
	}
	_, code, b = startJob(t, o, apigen.OrbSandboxExecRequest{Command: "sleep 60"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "too_many_jobs", o.errCode(&http.Response{StatusCode: code}, b, http.StatusConflict))

	rt.sbxJobs.killAll()
	require.Eventually(t, func() bool {
		for _, j := range jobList(t, o) {
			if j.State != apigen.OrbSandboxJobExited {
				return false
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond)

	// 到时限杀进程组，退出码 124。
	id, code, b = startJob(t, o, apigen.OrbSandboxExecRequest{Command: "exec sleep 30", TimeoutSec: 1})
	require.Equal(t, http.StatusOK, code, string(b))
	require.Eventually(t, func() bool { return jobList(t, o)[id].State == apigen.OrbSandboxJobExited }, 10*time.Second, 50*time.Millisecond)
	assert.Equal(t, boxExecTimeoutExit, *jobList(t, o)[id].Exit)
}

func TestSandboxJobLogTail(t *testing.T) {
	cp, rt := startSandbox(t)
	o := newOrbHTTP(t, cp, rt)
	// 15 万个「é」（2 字节）加 5 字节结尾：末尾 256 KiB 的起点落在字符中间，向后对齐到字符边界。
	id, code, b := startJob(t, o, apigen.OrbSandboxExecRequest{Command: `s=$(printf 'é%.0s' {1..150000}); printf '%s' "${s}done!"`})
	require.Equal(t, http.StatusOK, code, string(b))
	require.Eventually(t, func() bool { return jobList(t, o)[id].State == apigen.OrbSandboxJobExited }, 10*time.Second, 50*time.Millisecond)
	l := jobLog(t, o, id, nil)
	assert.True(t, l.EOF)
	assert.True(t, strings.HasSuffix(l.Text, "done!"))
	assert.True(t, strings.HasPrefix(l.Text, "é"), "starts on a rune boundary")
	assert.Equal(t, int64(300005-sandboxJobLogPage+1), l.Offset)
	assert.Equal(t, jobList(t, o)[id].LogBytes, l.NextOffset)
}
