package orbdrt

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// resumeEnv 是带假显示栈（fakedesk）的 orbd；测试直接调用 workspaceReady，不走 setup。
// resume 脚本把「标签 工作目录」追加到 marker，把自己的 pid 追加到 pids（测试结束时杀掉整个进程组）。
type resumeEnv struct {
	*deskEnv
	marker string
	pids   string
	// gate 非 nil 时显示栈失效后的重启要等测试放行（每次放行一次）。
	gate chan struct{}
}

func newResumeEnv(t *testing.T, gated bool) *resumeEnv {
	t.Helper()
	dir := t.TempDir()
	e := &resumeEnv{marker: filepath.Join(dir, "marker"), pids: filepath.Join(dir, "pids")}
	if gated {
		e.gate = make(chan struct{})
	}
	e.deskEnv = newDeskEnv(t, func(c *Config) {
		if gate := e.gate; gate != nil {
			c.DesktopBackoff = func(int) time.Duration {
				<-gate
				return time.Millisecond
			}
		}
	})
	// 先于运行时停止执行（Cleanup 后进先出）：放开被挡住的重启，杀掉仍在运行的脚本。
	t.Cleanup(func() {
		if e.gate != nil {
			close(e.gate)
		}
		b, _ := os.ReadFile(e.pids)
		for _, s := range strings.Fields(string(b)) {
			if pid, err := strconv.Atoi(s); err == nil {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})
	return e
}

// script 在 path 写一个 resume 脚本；extra 是追加在末尾的命令。
func (e *resumeEnv) script(path, tag string, mode os.FileMode, extra string) {
	e.t.Helper()
	require.NoError(e.t, os.MkdirAll(filepath.Dir(path), 0o755))
	body := "#!/bin/sh\n" +
		"echo $$ >> '" + e.pids + "'\n" +
		"echo \"" + tag + " $(pwd -P)\" >> '" + e.marker + "'\n" +
		extra + "\n"
	require.NoError(e.t, os.WriteFile(path, []byte(body), mode))
}

// runs 等 marker 恰好有 n 行并返回它们。
func (e *resumeEnv) runs(n int) []string {
	e.t.Helper()
	var lines []string
	require.Eventually(e.t, func() bool {
		b, _ := os.ReadFile(e.marker)
		lines = strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(b) == 0 {
			lines = nil
		}
		return len(lines) >= n
	}, 20*time.Second, 10*time.Millisecond, "resume script runs")
	require.Len(e.t, lines, n)
	return lines
}

// started 返回 orbd 已启动 resume 脚本的次数（启动是同步的：workspaceReady 返回时已写进 resume.log）。
func (e *resumeEnv) started() int {
	b, _ := os.ReadFile(e.home(".cache", "oryn", "logs", "desktop", "resume.log"))
	return strings.Count(string(b), "[orbd] display generation ")
}

func (e *resumeEnv) restartDisplay() {
	e.t.Helper()
	require.NoError(e.t, syscall.Kill(e.xvncPid(), syscall.SIGKILL))
	e.wait("display lost", displayIs(false))
}

func evalDir(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}

// 显示先就绪，workspaceReady 后到：在 workspaceReady 时执行。
func TestDeskResumeDisplayFirst(t *testing.T) {
	e := newResumeEnv(t, false)
	workdir := e.home("workspace", "repo")
	e.script(filepath.Join(workdir, ".oryn", "desktop", "resume"), "workdir", 0o755, "")
	e.wait("display ready", displayIs(true))
	assert.Zero(t, e.started(), "workspace not ready yet")

	e.rt.desk.workspaceReady(workdir)
	assert.Equal(t, []string{"workdir " + evalDir(t, workdir)}, e.runs(1))
}

// workspaceReady 先到，显示后就绪：在显示就绪时执行。
func TestDeskResumeWorkspaceFirst(t *testing.T) {
	e := newResumeEnv(t, true)
	workdir := e.home("workspace", "repo")
	e.script(filepath.Join(workdir, ".oryn", "desktop", "resume"), "workdir", 0o755, "")
	e.wait("display ready", displayIs(true))
	e.restartDisplay() // 重启被 gate 挡住

	e.rt.desk.workspaceReady(workdir)
	assert.Zero(t, e.started(), "display not ready")

	e.gate <- struct{}{}
	e.wait("display back", displayIs(true))
	assert.Equal(t, []string{"workdir " + evalDir(t, workdir)}, e.runs(1))
}

// 只执行 workdir 下的脚本，$HOME/workspace 下的同名脚本不执行。
func TestDeskResumeWorkdir(t *testing.T) {
	for name, rel := range map[string][]string{
		"repo":   {"workspace", "repo"},
		"custom": {"projects", "custom"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newResumeEnv(t, false)
			workdir := e.home(rel...)
			e.script(e.home("workspace", ".oryn", "desktop", "resume"), "workspace", 0o755, "")
			e.script(filepath.Join(workdir, ".oryn", "desktop", "resume"), "workdir", 0o755, "")
			e.wait("display ready", displayIs(true))

			e.rt.desk.workspaceReady(workdir)
			assert.Equal(t, []string{"workdir " + evalDir(t, workdir)}, e.runs(1))
			assert.Equal(t, 1, e.started())
		})
	}
}

// 只有 home 脚本时执行它，工作目录是 $HOME。
func TestDeskResumeHomeScript(t *testing.T) {
	e := newResumeEnv(t, false)
	workdir := e.home("workspace", "repo")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	e.script(e.home(".oryn", "desktop", "resume"), "home", 0o755, "")
	e.wait("display ready", displayIs(true))

	e.rt.desk.workspaceReady(workdir)
	assert.Equal(t, []string{"home " + evalDir(t, e.home())}, e.runs(1))
}

// 不可执行的脚本跳过：没有别的脚本时什么都不执行，有 home 脚本时执行 home 脚本。
func TestDeskResumeNotExecutable(t *testing.T) {
	t.Run("skipped", func(t *testing.T) {
		e := newResumeEnv(t, false)
		workdir := e.home("workspace", "repo")
		e.script(filepath.Join(workdir, ".oryn", "desktop", "resume"), "workdir", 0o644, "")
		e.wait("display ready", displayIs(true))

		e.rt.desk.workspaceReady(workdir)
		assert.Zero(t, e.started())
		_, err := os.Stat(e.marker)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("falls back to home", func(t *testing.T) {
		e := newResumeEnv(t, false)
		workdir := e.home("workspace", "repo")
		e.script(filepath.Join(workdir, ".oryn", "desktop", "resume"), "workdir", 0o644, "")
		e.script(e.home(".oryn", "desktop", "resume"), "home", 0o755, "")
		e.wait("display ready", displayIs(true))

		e.rt.desk.workspaceReady(workdir)
		assert.Equal(t, []string{"home " + evalDir(t, e.home())}, e.runs(1))
	})
}

// 同一代数里第二次 workspaceReady 不重复执行；显示重启（代数加 1）后再执行一次。
func TestDeskResumeOncePerGeneration(t *testing.T) {
	e := newResumeEnv(t, false)
	workdir := e.home("workspace", "repo")
	e.script(filepath.Join(workdir, ".oryn", "desktop", "resume"), "workdir", 0o755, "")
	e.wait("display ready", displayIs(true))

	e.rt.desk.workspaceReady(workdir)
	e.runs(1)
	e.rt.desk.workspaceReady(workdir)
	assert.Equal(t, 1, e.started(), "same display generation")

	e.restartDisplay()
	e.wait("display back", displayIs(true))
	e.runs(2)
	assert.Equal(t, 2, e.started())
}

// 不退出的脚本（sleep 120）不阻塞 workspaceReady，也不阻塞显示栈的就绪与监督。
func TestDeskResumeLongScript(t *testing.T) {
	e := newResumeEnv(t, false)
	workdir := e.home("workspace", "repo")
	e.script(filepath.Join(workdir, ".oryn", "desktop", "resume"), "workdir", 0o755, "sleep 120")
	e.wait("display ready", displayIs(true))

	start := time.Now()
	e.rt.desk.workspaceReady(workdir)
	assert.Less(t, time.Since(start), 5*time.Second, "workspaceReady does not wait for the script")
	e.runs(1)

	e.restartDisplay()
	e.wait("display back", displayIs(true))
	e.runs(2)
	// 显示栈仍在监督：再杀一次照常失效、恢复。
	e.restartDisplay()
	e.wait("display back again", displayIs(true))
	assert.True(t, e.rt.desk.isDisplayReady())
}

// deferResume 的 setup.run 不触发桌面脚本（控制面在它之后、resume.run 之前拍项目快照）；随后的 resume.run 才触发。
func TestDeskResumeDeferredSetup(t *testing.T) {
	e := newResumeEnv(t, false)
	workdir := e.home("workspace")
	e.script(filepath.Join(workdir, ".oryn", "desktop", "resume"), "workdir", 0o755, "")
	e.wait("display ready", displayIs(true))

	res, n := setupCall(t, e.cp, apigen.LinkCtlSetupRunParams{DeferResume: new(true)})
	require.True(t, res.Ok, "%v", deref(res.SetupError))
	require.True(t, n.Ok)
	assert.Zero(t, e.started(), "deferred setup.run must not start the desktop script before the snapshot")

	from := e.cp.notifCount()
	require.NoError(t, e.cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{}, &res))
	waitResult(t, e.cp, from, apigen.SetupPhaseResume)
	assert.Equal(t, []string{"workdir " + evalDir(t, workdir)}, e.runs(1))
	assert.Equal(t, 1, e.started())
}
