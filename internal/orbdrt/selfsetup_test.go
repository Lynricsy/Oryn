package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// waitResult 等待下标 ≥ from 的 phase 阶段 setup.result，返回其下标与内容。
func waitResult(t *testing.T, cp *fakeCP, from int, phase apigen.SetupPhase) (int, apigen.LinkCtlSetupResultParams) {
	t.Helper()
	idx, raw := cp.waitNotif(t, from, "setup.result", func(raw json.RawMessage) bool {
		var p apigen.LinkCtlSetupResultParams
		_ = json.Unmarshal(raw, &p)
		return p.Phase == phase
	})
	var p apigen.LinkCtlSetupResultParams
	require.NoError(t, json.Unmarshal(raw, &p))
	return idx, p
}

func (cp *fakeCP) results() []apigen.LinkCtlSetupResultParams {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	var out []apigen.LinkCtlSetupResultParams
	for _, n := range cp.notifs {
		if n.method == "setup.result" {
			var p apigen.LinkCtlSetupResultParams
			_ = json.Unmarshal(n.params, &p)
			out = append(out, p)
		}
	}
	return out
}

func readStatusFile(t *testing.T, home string) orbsock.Status {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".oryn", "setup-status.json"))
	require.NoError(t, err)
	var st orbsock.Status
	require.NoError(t, json.Unmarshal(b, &st))
	return st
}

type sockLog struct {
	mu    sync.Mutex
	lines map[string]*strings.Builder
}

func (l *sockLog) add(stream, data string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lines == nil {
		l.lines = map[string]*strings.Builder{}
	}
	if l.lines[stream] == nil {
		l.lines[stream] = &strings.Builder{}
	}
	l.lines[stream].WriteString(data)
}

func (l *sockLog) get(stream string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.lines[stream]; b != nil {
		return b.String()
	}
	return ""
}

func sockCall(t *testing.T, rt *Runtime, method string, params any, log *sockLog) (json.RawMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var onLog func(string, string)
	if log != nil {
		onLog = log.add
	}
	var raw json.RawMessage
	var err error
	// 控制口在 Run 启动后才监听。
	require.Eventually(t, func() bool {
		_, serr := os.Stat(orbsock.Path(rt.home))
		return serr == nil
	}, 10*time.Second, 10*time.Millisecond)
	raw, err = orbsock.Call(ctx, orbsock.Path(rt.home), method, params, onLog)
	return raw, err
}

func sockErrCode(err error) string {
	var e *orbsock.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// 自动 setup：无脚本、已完成跳过、仓库脚本与项目脚本标志、失败内容，以及 setup 先于 resume 上报。
func TestSetupResultAuto(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)

	// 1. 没有任何脚本：照样发两条 ok 结果，控制面据此判断「缺少配置」。
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{}, &res))
	iS, s := waitResult(t, cp, 0, apigen.SetupPhaseSetup)
	iR, r := waitResult(t, cp, 0, apigen.SetupPhaseResume)
	assert.Less(t, iS, iR, "setup 结果先于 resume")
	assert.True(t, s.Ok)
	assert.True(t, s.SetupRan)
	assert.False(t, s.HasRepoScript)
	assert.False(t, s.HasProjectScript)
	assert.Equal(t, apigen.SetupTriggerAuto, s.Trigger)
	assert.Positive(t, s.StartedAt)
	assert.True(t, r.Ok)
	assert.Equal(t, apigen.SetupTriggerAuto, r.Trigger)

	// 2. 标记已存在：setupRan=false、ok=true；仓库脚本（.agents 回退）与项目脚本被识别。
	work := filepath.Join(rt.home, "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(work, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".agents", "setup"), []byte("#!/bin/sh\necho repo\n"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(work, ".oryn"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, ".oryn", "resume"), []byte("#!/bin/sh\nexit 0\n"), 0o644)) // 无可执行位：不算
	from := cp.notifCount()
	params := apigen.LinkCtlSetupRunParams{Workdir: "~/proj", Scripts: apigen.LinkCtlSetupScripts{Setup: new("echo p"), Resume: new("  \n")}}
	require.NoError(t, cp.call(t, "setup.run", params, &res))
	_, s = waitResult(t, cp, from, apigen.SetupPhaseSetup)
	_, r = waitResult(t, cp, from, apigen.SetupPhaseResume)
	assert.True(t, s.Ok)
	assert.False(t, s.SetupRan)
	assert.True(t, s.HasRepoScript)
	assert.True(t, s.HasProjectScript)
	assert.False(t, r.HasRepoScript, "不可执行的 .oryn/resume 不算仓库脚本")
	assert.False(t, r.HasProjectScript, "只含空白的项目脚本不算")

	// 3. resume.run 只发 resume 结果。
	from = cp.notifCount()
	require.NoError(t, cp.call(t, "resume.run", params, &res))
	waitResult(t, cp, from, apigen.SetupPhaseResume)
	time.Sleep(200 * time.Millisecond)
	for _, p := range cp.results()[len(cp.results())-1:] {
		assert.Equal(t, apigen.SetupPhaseResume, p.Phase)
	}
	n := 0
	for _, p := range cp.results() {
		if p.Phase == apigen.SetupPhaseSetup {
			n++
		}
	}
	assert.Equal(t, 2, n, "resume.run 不发 setup 结果")

	// 4. setup 失败（删掉标记让它重跑）：ok=false，错误带脚本名与退出码，现状文件同步。
	require.NoError(t, os.Remove(filepath.Join(rt.home, ".oryn", "setup.done")))
	from = cp.notifCount()
	params.Scripts.Setup = new("echo boom; exit 3")
	require.NoError(t, cp.call(t, "setup.run", params, &res))
	assert.False(t, res.Ok)
	_, s = waitResult(t, cp, from, apigen.SetupPhaseSetup)
	assert.False(t, s.Ok)
	assert.True(t, s.SetupRan)
	require.NotNil(t, s.Error)
	assert.Contains(t, *s.Error, "project setup script")
	assert.Contains(t, *s.Error, "exit status 3")
	waitResult(t, cp, from, apigen.SetupPhaseResume)
	st := readStatusFile(t, rt.home)
	require.NotNil(t, st.Setup)
	assert.False(t, st.Setup.Ok)
	require.NotNil(t, st.Resume)
	assert.True(t, st.Resume.Ok)
}

// resume 超过等待时间转入后台：结果在脚本真正结束时发送，之后的非零退出也记 ok=false。
func TestResumeResultAfterBackground(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) { c.ResumeWait = 300 * time.Millisecond })

	from := cp.notifCount()
	start := time.Now()
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Resume: new("sleep 1.5; exit 4")}}, &res))
	assert.Less(t, time.Since(start), 1200*time.Millisecond, "请求不等后台 resume")
	for _, p := range cp.results() {
		assert.NotEqual(t, apigen.SetupPhaseResume, p.Phase, "resume 尚未结束时不应有结果")
	}
	_, r := waitResult(t, cp, from, apigen.SetupPhaseResume)
	assert.False(t, r.Ok)
	require.NotNil(t, r.Error)
	assert.Contains(t, *r.Error, "exit status 4")
	assert.GreaterOrEqual(t, r.DurationMs, int64(1400))
	assert.True(t, r.HasProjectScript)

	// 等待时间内非零退出同样记失败。
	from = cp.notifCount()
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Resume: new("exit 5")}}, &res))
	_, r = waitResult(t, cp, from, apigen.SetupPhaseResume)
	assert.False(t, r.Ok)
	assert.Contains(t, *r.Error, "exit status 5")
	assert.False(t, readStatusFile(t, rt.home).Resume.Ok)
}

// 断连期间结束的后台 resume：结果在重连后补发。
func TestSetupResultQueuedWhileOffline(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, func(c *Config) { c.ResumeWait = 100 * time.Millisecond })
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Resume: new("sleep 1")}}, &res))
	cp.blocked.Store(true)
	cp.killLink()
	time.Sleep(1500 * time.Millisecond) // resume 在断连期间结束
	from := cp.notifCount()
	cp.blocked.Store(false)
	_, r := waitResult(t, cp, from, apigen.SetupPhaseResume)
	assert.True(t, r.Ok)
}

// 手动重跑：流式输出、忽略标记、trigger=manual、日志进文件与 setup.log 通知；后台 resume 运行时 busy。
func TestSocketRerun(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) { c.ResumeWait = 100 * time.Millisecond })

	_, err := sockCall(t, rt, orbsock.MethodSetupRerun, nil, nil)
	assert.Equal(t, orbsock.CodeNotReady, sockErrCode(err), "还没收到 setup 参数")

	gate := filepath.Join(rt.home, "gate")
	setup := "echo hello-out\necho to-err >&2\necho run >> \"$HOME/count.txt\"\n"
	resume := "while [ ! -f \"$HOME/gate\" ]; do sleep 0.05; done\necho resumed-out\n"
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{
		Env:     map[string]string{"FOO": "bar"},
		Scripts: apigen.LinkCtlSetupScripts{Setup: &setup, Resume: &resume},
	}, &res))
	require.True(t, res.Ok)

	// 自动 resume 仍在后台运行：手动重跑被拒。
	_, err = sockCall(t, rt, orbsock.MethodSetupRerun, orbsock.RerunParams{}, nil)
	assert.Equal(t, orbsock.CodeBusy, sockErrCode(err))
	require.NoError(t, os.WriteFile(gate, nil, 0o644))
	waitResult(t, cp, 0, apigen.SetupPhaseResume)

	// setup 重跑：忽略 setup.done，再执行一次。
	from := cp.notifCount()
	log := &sockLog{}
	raw, err := sockCall(t, rt, orbsock.MethodSetupRerun, orbsock.RerunParams{}, log)
	require.NoError(t, err)
	var got apigen.LinkCtlSetupResultParams
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.True(t, got.Ok)
	assert.True(t, got.SetupRan)
	assert.Equal(t, apigen.SetupTriggerManual, got.Trigger)
	assert.True(t, got.HasProjectScript)
	assert.Contains(t, log.get(orbsock.StreamStdout), "hello-out")
	assert.Contains(t, log.get(orbsock.StreamStderr), "to-err")
	assert.Contains(t, log.get(orbsock.StreamSystem), "[orbd] setup completed")
	b, _ := os.ReadFile(filepath.Join(rt.home, "count.txt"))
	assert.Equal(t, "run\nrun\n", string(b))
	b, _ = os.ReadFile(filepath.Join(rt.home, ".cache", "oryn", "logs", "setup.log"))
	assert.Equal(t, 2, strings.Count(string(b), "hello-out"))

	_, s := waitResult(t, cp, from, apigen.SetupPhaseSetup)
	assert.Equal(t, apigen.SetupTriggerManual, s.Trigger)
	assert.True(t, s.Ok)
	cp.waitNotif(t, from, "setup.log", func(raw json.RawMessage) bool {
		var p apigen.LinkCtlSetupLogParams
		_ = json.Unmarshal(raw, &p)
		return p.Phase == apigen.Setup && strings.Contains(p.Data, "hello-out")
	})
	st := readStatusFile(t, rt.home)
	require.NotNil(t, st.Setup)
	assert.Equal(t, apigen.SetupTriggerManual, st.Setup.Trigger)

	// resume 重跑：前台等到结束（不受 ResumeWait 影响），输出流式回传。
	from = cp.notifCount()
	log = &sockLog{}
	raw, err = sockCall(t, rt, orbsock.MethodSetupRerun, orbsock.RerunParams{Resume: true}, log)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, apigen.SetupPhaseResume, got.Phase)
	assert.True(t, got.Ok)
	assert.Contains(t, log.get(orbsock.StreamStdout), "resumed-out")
	_, r := waitResult(t, cp, from, apigen.SetupPhaseResume)
	assert.Equal(t, apigen.SetupTriggerManual, r.Trigger)

	// 失败的手动重跑：结果 ok=false（不是调用错误），现状反映失败。
	require.NoError(t, os.Remove(filepath.Join(rt.home, "count.txt")))
	setup = "exit 7"
	require.NoError(t, cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Setup: &setup}}, &res))
	raw, err = sockCall(t, rt, orbsock.MethodSetupRerun, nil, nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.False(t, got.Ok)
	assert.Contains(t, *got.Error, "exit status 7")

	raw, err = sockCall(t, rt, orbsock.MethodSetupStatus, nil, nil)
	require.NoError(t, err)
	var status orbsock.Status
	require.NoError(t, json.Unmarshal(raw, &status))
	require.NotNil(t, status.Setup)
	assert.False(t, status.Setup.Ok)
	require.NotNil(t, status.Resume)
	assert.Equal(t, apigen.SetupTriggerAuto, status.Resume.Trigger, "resume.run 之后的 resume 是自动的")
}

// 项目脚本经 ctl 转发，只作用于本 orb 的项目；写入后手动重跑用上新脚本。
func TestSocketProjectScripts(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)

	_, err := sockCall(t, rt, orbsock.MethodProjectScriptsGet, nil, nil)
	assert.Equal(t, orbsock.CodeNoProject, sockErrCode(err), "控制面的错误码原样透传")

	cp.mu.Lock()
	cp.project = &apigen.LinkCtlProjectScripts{ProjectID: "prj_1", ProjectName: "demo", Setup: new("echo old >> \"$HOME/which.txt\"")}
	cp.mu.Unlock()
	var res apigen.LinkCtlSetupRunResult
	require.NoError(t, cp.call(t, "setup.run", apigen.LinkCtlSetupRunParams{Scripts: apigen.LinkCtlSetupScripts{Setup: new("echo old >> \"$HOME/which.txt\"")}}, &res))

	raw, err := sockCall(t, rt, orbsock.MethodProjectScriptsGet, nil, nil)
	require.NoError(t, err)
	var ps apigen.LinkCtlProjectScripts
	require.NoError(t, json.Unmarshal(raw, &ps))
	assert.Equal(t, "demo", ps.ProjectName)

	_, err = sockCall(t, rt, orbsock.MethodProjectScriptsSet, apigen.LinkCtlProjectScriptsSetParams{}, nil)
	assert.Equal(t, orbsock.CodeInvalidParams, sockErrCode(err))

	raw, err = sockCall(t, rt, orbsock.MethodProjectScriptsSet, apigen.LinkCtlProjectScriptsSetParams{Setup: new("echo new >> \"$HOME/which.txt\"")}, nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &ps))
	require.NotNil(t, ps.Setup)
	assert.Contains(t, *ps.Setup, "echo new")
	cp.mu.Lock()
	assert.Contains(t, *cp.project.Setup, "echo new")
	cp.mu.Unlock()

	_, err = sockCall(t, rt, orbsock.MethodSetupRerun, nil, nil)
	require.NoError(t, err)
	b, _ := os.ReadFile(filepath.Join(rt.home, "which.txt"))
	assert.Equal(t, "old\nnew\n", string(b), "重跑使用刚保存的项目脚本")

	// 未连上控制面：明确报 offline。
	cp.blocked.Store(true)
	cp.killLink()
	require.Eventually(t, func() bool { return rt.current() == nil }, 10*time.Second, 10*time.Millisecond)
	_, err = sockCall(t, rt, orbsock.MethodProjectScriptsGet, nil, nil)
	assert.Equal(t, orbsock.CodeOffline, sockErrCode(err))
}

// orbd 启动写出扩展与技能，omp 启动参数带 `-e <扩展>`。
func TestAgentAssetsAndExtensionArg(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	ext := filepath.Join(rt.home, ".oryn", "core", "extensions", "oryn-orb.ts")
	b, err := os.ReadFile(ext)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"before_agent_start"`)
	assert.Contains(t, string(b), "setup-status.json")
	b, err = os.ReadFile(filepath.Join(rt.home, ".agents", "skills", "oryn-setup", "SKILL.md"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(b), "---\nname: oryn-setup\n"), "技能 frontmatter")

	pid := startAgent(t, cp, thread1, nil)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	require.NoError(t, err)
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	i := indexOf(args, "-e")
	require.GreaterOrEqual(t, i, 0, "argv %v", args)
	require.Less(t, i+1, len(args))
	assert.Equal(t, ext, args[i+1])
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
