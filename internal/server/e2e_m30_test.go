package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M30 §4.2 / §5.1：Agent 沙箱全栈——process 驱动（声明 sandbox 能力，沙箱 orbd 以精简模式经 unix 中继连 link）
// + 真实 orbd + 真实 `oryn sandbox …`（orb 内经 orbd.sock 的 sandbox.call）。沙箱对账周期调到 200ms，
// 时钟是「真实时间 + 可调偏移」，用来让 TTL 到期而不必真等。

// sbxCaller 以某线程在 orb 里的身份（ORYN_ORB=1、ORYN_THREAD_ID、orb 的 HOME）跑真实的 oryn。
type sbxCaller struct {
	thread string
	orb    string
	home   string
}

// run 在 HOME 里跑 `oryn sandbox args…`，返回退出码与分开的 stdout / stderr。
func (c *sbxCaller) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "oryn"), append([]string{"sandbox"}, args...)...)
	cmd.Dir = c.home
	cmd.Env = []string{"HOME=" + c.home, "PATH=" + os.Getenv("PATH"), "ORYN_ORB=1", "ORYN_THREAD_ID=" + c.thread}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), stdout.String(), stderr.String()
	}
	require.NoError(t, err, "oryn sandbox %v: %s%s", args, stdout.String(), stderr.String())
	return 0, "", ""
}

// ok 跑命令并要求退出码 0，返回 stdout。
func (c *sbxCaller) ok(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := c.run(t, args...)
	require.Equal(t, 0, code, "oryn sandbox %v\nstdout: %s\nstderr: %s", args, out, errOut)
	return out
}

// fail 跑命令并要求退出码 2，返回 stderr（`<code>: <message>`）。
func (c *sbxCaller) fail(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := c.run(t, args...)
	require.Equal(t, 2, code, "oryn sandbox %v\nstdout: %s\nstderr: %s", args, out, errOut)
	return errOut
}

// up 开一台沙箱并返回它（--json）。
func (c *sbxCaller) up(t *testing.T, args ...string) apigen.Sandbox {
	t.Helper()
	out := c.ok(t, append([]string{"up", "--json"}, args...)...)
	var sb apigen.Sandbox
	require.NoError(t, json.Unmarshal([]byte(out), &sb), out)
	return sb
}

// ---------------------------------------------------------------- TestE2EM30

type m30Env struct {
	*stack
	offset   *atomic.Int64 // sandboxesNow 的偏移（毫秒）
	a, b     *sbxCaller    // 两个线程，各自的 orb
	id, id2  string        // S1 的断网沙箱、S8 的联网沙箱
	ttlID    string        // S11 带 TTL 的沙箱
	ws       *wsClient
	bSandbox string // S13 线程 b 的沙箱
}

func TestE2EM30(t *testing.T) {
	// process 驱动的 orbd 继承本进程环境：宿主 shell 里的 GH_* / HINDSIGHT*（例如 GH_PAGER）会经它进入沙箱，
	// 与「沙箱零凭据」的断言无关（docker / Firecracker 的沙箱环境只有驱动给的变量），先摘掉。
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GH_") || strings.HasPrefix(k, "HINDSIGHT") {
			require.NoError(t, os.Unsetenv(k))
			t.Cleanup(func() { _ = os.Setenv(k, v) })
		}
	}
	offset := &atomic.Int64{}
	prevTick, prevNow := sandboxesTick, sandboxesNow
	sandboxesTick = 200 * time.Millisecond
	sandboxesNow = func() time.Time { return time.Now().Add(time.Duration(offset.Load()) * time.Millisecond) }
	t.Cleanup(func() { sandboxesTick, sandboxesNow = prevTick, prevNow })

	s := newStack(t, m23Idle)
	e := &m30Env{stack: s, offset: offset}
	for _, c := range []**sbxCaller{&e.a, &e.b} {
		th := s.newThread(map[string]any{"prompt": "hi"})
		*c = &sbxCaller{thread: th.ID, orb: th.OrbID, home: s.drv.HomeDir(th.OrbID)}
	}
	require.NotEqual(t, e.a.orb, e.b.orb)

	steps := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"S1创建", e.stepUp},
		{"S2零凭据", e.stepClean},
		{"S3退出码与两路输出", e.stepExec},
		{"S4超时", e.stepTimeout},
		{"S5目录上传", e.stepUpload},
		{"S6文件下载", e.stepDownload},
		{"S7后台作业", e.stepJobs},
		{"S8网络", e.stepNetwork},
		{"S9配额", e.stepQuota},
		{"S10越权", e.stepForeign},
		{"S11到期", e.stepTTL},
		{"S12级联", e.stepCascade},
		{"S13REST与推送", e.stepREST},
	}
	for _, st := range steps {
		ok := t.Run(st.name, func(sub *testing.T) {
			e.stack.t = sub
			defer func() { e.stack.t = t }()
			st.fn(sub)
		})
		if !ok {
			t.Fatalf("%s 失败，后续步骤依赖它，停止", st.name)
		}
	}
}

func (e *m30Env) row(t *testing.T, id string) db.Sandbox {
	t.Helper()
	sb, err := e.srv.Store.Q.GetSandbox(context.Background(), id)
	require.NoError(t, err)
	return sb
}

// waitDestroyed 等沙箱行进入 destroyed，要求原因为 reason，并等它的 orb 回收完。
func (e *m30Env) waitDestroyed(t *testing.T, id, reason string) {
	t.Helper()
	var sb db.Sandbox
	require.Eventually(t, func() bool {
		sb = e.row(t, id)
		return sb.State == "destroyed"
	}, 60*time.Second, 100*time.Millisecond, "沙箱 %s 进入 destroyed", id)
	require.NotNil(t, sb.DestroyReason)
	assert.Equal(t, reason, *sb.DestroyReason)
	e.waitOrbState(sb.OrbID, "deleted", 60*time.Second)
}

// S1：断网沙箱就绪；process 驱动算 docker 后端；orb 行记下创建时的网络。
func (e *m30Env) stepUp(t *testing.T) {
	sb := e.a.up(t, "--network", "none")
	e.id = sb.ID
	assert.Equal(t, apigen.SandboxStateReady, sb.State)
	assert.Equal(t, apigen.SandboxBackend("docker"), sb.Backend)
	assert.Equal(t, apigen.SandboxNetworkNone, sb.Network)
	assert.Equal(t, e.a.thread, string(sb.OwnerThreadID))
	o, err := e.srv.Store.Q.GetOrb(context.Background(), e.row(t, e.id).OrbID)
	require.NoError(t, err)
	require.NotNil(t, o.SandboxNetwork)
	assert.Equal(t, "none", *o.SandboxNetwork)
}

// S2：沙箱里的命令环境没有任何凭据，精简模式没有 core 与 orbd.sock。
func (e *m30Env) stepClean(t *testing.T) {
	env := e.a.ok(t, "exec", e.id, "--", "env")
	assert.Contains(t, env, "HOME=")
	for _, bad := range []string{"ORYN_ORB_TOKEN", "HINDSIGHT", "GH_"} {
		assert.NotContains(t, env, bad)
	}
	out := e.a.ok(t, "exec", e.id, "--", "test ! -e ~/.oryn/core && test ! -S ~/.oryn/orbd.sock && echo clean")
	assert.Equal(t, "clean", strings.TrimSpace(out))
}

// S3：两路输出分开转发，退出码透传。
func (e *m30Env) stepExec(t *testing.T) {
	code, out, errOut := e.a.run(t, "exec", e.id, "--", "echo out; echo err >&2; exit 3")
	assert.Equal(t, 3, code)
	assert.Equal(t, "out\n", out)
	assert.Contains(t, errOut, "err\n")
}

// S4：同步 exec 超时 → 124，及时返回。
func (e *m30Env) stepTimeout(t *testing.T) {
	start := time.Now()
	code, out, errOut := e.a.run(t, "exec", e.id, "--timeout", "1", "--", "sleep", "30")
	assert.Equal(t, 124, code, "stdout: %s\nstderr: %s", out, errOut)
	assert.Less(t, time.Since(start), 5*time.Second)
}

// S5：目录上传：--gitignore 只带 git 会跟踪的文件，--exclude 再排掉 *.log；.git 不在其中。
func (e *m30Env) stepUpload(t *testing.T) {
	src := filepath.Join(e.a.home, "src")
	writeTree(t, src, map[string]string{
		".gitignore":     "ignored.txt\n",
		"tracked.txt":    "tracked",
		"sub/nested.txt": "nested",
		"ignored.txt":    "ignored",
	})
	gitE2E(t, src, "init", "-q", "-b", "main")
	commitAll(t, src, "init")
	writeTree(t, src, map[string]string{"skip.log": "log"}) // 未跟踪、未忽略：只被 --exclude 挡掉
	out := e.a.ok(t, "cp", "--gitignore", "--exclude", "*.log", "~/src", e.id+":proj")
	assert.Contains(t, out, "copied 3 files")
	list := e.a.ok(t, "exec", e.id, "--", "cd ~/proj && find . -type f | sort")
	assert.Equal(t, "./.gitignore\n./sub/nested.txt\n./tracked.txt\n", list)
}

// S6：文件下载到 orb。
func (e *m30Env) stepDownload(t *testing.T) {
	e.a.ok(t, "exec", e.id, "--", "echo result > ~/out.txt")
	e.a.ok(t, "cp", e.id+":out.txt", "~/got.txt")
	b, err := os.ReadFile(filepath.Join(e.a.home, "got.txt"))
	require.NoError(t, err)
	assert.Equal(t, "result\n", string(b))
}

// S7：后台作业：列表、日志、终止。
func (e *m30Env) stepJobs(t *testing.T) {
	job := strings.TrimSpace(e.a.ok(t, "exec", e.id, "--detach", "--", "echo a; sleep 60"))
	require.NotEmpty(t, job)
	assert.Regexp(t, job+`\s+running`, e.a.ok(t, "jobs", e.id))
	require.Eventually(t, func() bool {
		return strings.Contains(e.a.ok(t, "jobs", e.id, "logs", job), "a")
	}, 10*time.Second, 100*time.Millisecond, "作业日志出现 a")
	e.a.ok(t, "jobs", e.id, "kill", job)
	require.Eventually(t, func() bool {
		return strings.Contains(e.a.ok(t, "jobs", e.id), "exited")
	}, 10*time.Second, 100*time.Millisecond, "作业变为 exited")
}

// S8：创建时断网的不能开网；联网的可在 egress ↔ none 间来回切（process 驱动把当前模式记在 network 文件里）。
func (e *m30Env) stepNetwork(t *testing.T) {
	assert.Contains(t, e.a.fail(t, "net", e.id, "egress"), "network_unavailable")
	sb := e.a.up(t, "--network", "egress")
	e.id2 = sb.ID
	assert.Equal(t, apigen.SandboxNetworkEgress, sb.Network)
	netFile := filepath.Join(e.drv.Root, "orbs", e.row(t, e.id2).OrbID, "network")
	read := func() string {
		b, err := os.ReadFile(netFile)
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, "egress", read())
	assert.Contains(t, e.a.ok(t, "net", e.id2, "none"), "network: none")
	assert.Equal(t, "none", read())
	assert.Equal(t, "none", e.row(t, e.id2).Network)
	assert.Contains(t, e.a.ok(t, "net", e.id2, "egress"), "network: egress")
	assert.Equal(t, "egress", read())
}

// S9：每 owner orb 至多 2 台。
func (e *m30Env) stepQuota(t *testing.T) {
	assert.Contains(t, e.a.fail(t, "up", "--network", "none"), "quota_per_orb")
}

// S10：另一台 orb 的线程看不到别人的沙箱。
func (e *m30Env) stepForeign(t *testing.T) {
	assert.Contains(t, e.b.fail(t, "exec", e.id, "--", "true"), "sandbox_not_found")
}

// S11：TTL 到点即销毁（拨快沙箱时钟）。
func (e *m30Env) stepTTL(t *testing.T) {
	e.a.ok(t, "rm", e.id2)
	e.waitDestroyed(t, e.id2, "agent")
	sb := e.a.up(t, "--network", "none", "--ttl", "1m")
	e.ttlID = sb.ID
	require.NotNil(t, sb.TTLDeadline)
	e.offset.Store((2 * time.Minute).Milliseconds())
	e.waitDestroyed(t, e.ttlID, "ttl")
	e.offset.Store(0) // 后面要再建沙箱：偏移下 provider 的租约看起来已过期
	assert.Equal(t, "ready", e.row(t, e.id).State, "没到期的沙箱不受影响")
}

// S12：归档 owner 线程 → 它名下剩下的沙箱销毁。
func (e *m30Env) stepCascade(t *testing.T) {
	e.rest("PATCH", "/api/v1/threads/"+e.a.thread, map[string]any{"archived": true}, nil, 200)
	e.waitDestroyed(t, e.id, "thread_archived")
}

// S13：设备令牌的 REST 列表与销毁，WS 推送 sandboxes.changed。
func (e *m30Env) stepREST(t *testing.T) {
	sb := e.b.up(t, "--network", "none")
	e.bSandbox = sb.ID
	var list apigen.SandboxList
	e.rest("GET", "/api/v1/sandboxes?thread="+e.b.thread, nil, &list, 200)
	require.Len(t, list.Sandboxes, 1)
	assert.Equal(t, e.bSandbox, list.Sandboxes[0].ID)
	assert.Equal(t, apigen.SandboxStateReady, list.Sandboxes[0].State)

	var all apigen.SandboxList
	e.rest("GET", "/api/v1/sandboxes", nil, &all, 200)
	ids := make([]string, 0, len(all.Sandboxes))
	for _, x := range all.Sandboxes {
		ids = append(ids, x.ID)
	}
	assert.Equal(t, []string{e.bSandbox}, ids, "省略 thread = 全部在用的")

	e.ws = e.dial()
	e.rest("DELETE", "/api/v1/sandboxes/"+e.bSandbox, nil, nil, http.StatusNoContent)
	e.ws.until(10*time.Second, func(m wsMsg) bool { return m.Type == "sandboxes.changed" && m.ThreadID == e.b.thread })
	e.waitDestroyed(t, e.bSandbox, "owner")
	e.rest("DELETE", "/api/v1/sandboxes/"+e.bSandbox, nil, nil, http.StatusNoContent) // 已结束：幂等
	assert.Equal(t, "sandbox_not_found", e.restErr("DELETE", "/api/v1/sandboxes/sbx_missing", nil, http.StatusNotFound))

	e.rest("GET", "/api/v1/sandboxes?thread="+e.b.thread, nil, &list, 200)
	require.Len(t, list.Sandboxes, 1, "最近结束的仍在线程的列表里")
	require.NotNil(t, list.Sandboxes[0].DestroyReason)
	assert.Equal(t, "owner", *list.Sandboxes[0].DestroyReason)
}
