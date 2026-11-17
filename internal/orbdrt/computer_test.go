package orbdrt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 工具执行器（M19 步骤 4.2）的测试：fakeomp 实现 oryn_tool_defs / oryn_tool_call / oryn_tool_cancel 与 oryn_tool_update。

func computerCall(t *testing.T, cp *fakeCP, callID, tool string, args map[string]any, timeoutSec int) (apigen.LinkCtlComputerCallResult, error) {
	t.Helper()
	var res apigen.LinkCtlComputerCallResult
	err := cp.call(t, string(apigen.MethodComputerCall), apigen.LinkCtlComputerCallParams{
		CallID: callID, Tool: tool, Args: args, TimeoutSec: timeoutSec,
	}, &res)
	return res, err
}

// resultText 取工具结果第一个内容块的文本。
func resultText(t *testing.T, r map[string]any) string {
	t.Helper()
	content, ok := r["content"].([]any)
	require.True(t, ok, "result has content: %v", r)
	require.NotEmpty(t, content)
	block, ok := content[0].(map[string]any)
	require.True(t, ok)
	text, _ := block["text"].(string)
	return text
}

// computer.start 惰性拉起执行器并返回所列工具的定义（未知名字跳过）与 omp 版本；
// 执行器以 `--mode rpc --no-session …--cwd <home>` 启动，环境是 orb 的基础环境（不带线程变量）。
func TestComputerStartReturnsDefs(t *testing.T) {
	dump := t.TempDir()
	t.Setenv("FAKEOMP_DUMP_DIR", dump)
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	assert.False(t, rt.comp.running(), "the executor starts lazily")

	var res apigen.LinkCtlComputerStartResult
	require.NoError(t, cp.call(t, string(apigen.MethodComputerStart), apigen.LinkCtlComputerStartParams{Names: []string{"bash", "nope", "read"}}, &res))
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool["name"].(string))
		assert.NotNil(t, tool["parameters"])
	}
	assert.Equal(t, []string{"bash", "read"}, names)
	assert.Equal(t, "omp/0.0.0-fakeomp", res.AgentVersion)
	assert.True(t, rt.comp.running())

	entries, err := os.ReadDir(dump)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	b, err := os.ReadFile(filepath.Join(dump, entries[0].Name()))
	require.NoError(t, err)
	var d agentDump
	require.NoError(t, json.Unmarshal(b, &d))
	assert.Equal(t, []string{"--mode", "rpc", "--no-session", "--no-skills", "--no-extensions", "--no-rules", "--no-context-files", "--cwd", rt.home, "--config", rt.computerOverlayPath()}, d.Args)
	overlay, err := os.ReadFile(rt.computerOverlayPath())
	require.NoError(t, err)
	assert.Equal(t, computerOverlay, string(overlay))
	assert.Equal(t, rt.home, d.Env["HOME"])
	assert.Equal(t, "off", d.Env["PI_NOTIFICATIONS"])
	assert.Equal(t, "orb-test", d.Env["ORYN_ORB_ID"])
	assert.NotContains(t, d.Env, "ORYN_THREAD_ID")
	assert.NotContains(t, d.Env, "ORYN_ORB_TOKEN")

	// 再次 start 复用同一个进程
	require.NoError(t, cp.call(t, string(apigen.MethodComputerStart), apigen.LinkCtlComputerStartParams{Names: []string{"bash"}}, &res))
	entries, err = os.ReadDir(dump)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

// computer.call：结果原样返回（含图片块），运行中的输出以 computer.update 通知到达；执行器拒绝的命令与非法时限报错。
func TestComputerCallStreams(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	from := cp.notifCount()

	res, err := computerCall(t, cp, "c1", "bash", map[string]any{"command": "for i in 1 2 3; do echo line$i; sleep 0.2; done"}, 0)
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2\nline3\n", resultText(t, res.Result))
	assert.Nil(t, res.Result["isError"])
	_, raw := cp.waitNotif(t, from, string(apigen.MethodComputerUpdate), nil)
	var u apigen.LinkCtlComputerUpdateParams
	require.NoError(t, json.Unmarshal(raw, &u))
	assert.Equal(t, "c1", u.CallID)
	assert.Contains(t, resultText(t, u.PartialResult), "line1")

	// write 后 read
	path := filepath.Join(rt.home, "note.txt")
	_, err = computerCall(t, cp, "c2", "write", map[string]any{"path": path, "content": "hello"}, 10)
	require.NoError(t, err)
	res, err = computerCall(t, cp, "c3", "read", map[string]any{"path": path}, 10)
	require.NoError(t, err)
	assert.Equal(t, "hello", resultText(t, res.Result))

	// 图片块原样到达
	res, err = computerCall(t, cp, "c4", "screenshot", nil, 10)
	require.NoError(t, err)
	content := res.Result["content"].([]any)
	require.Len(t, content, 2)
	img := content[1].(map[string]any)
	assert.Equal(t, "image", img["type"])
	assert.Equal(t, "image/png", img["mimeType"])
	assert.NotEmpty(t, img["data"])

	// 工具里的错误是 isError 结果，不是 ctl 错误
	res, err = computerCall(t, cp, "c5", "bash", map[string]any{"command": "exit 3"}, 10)
	require.NoError(t, err)
	assert.Equal(t, true, res.Result["isError"])

	_, err = computerCall(t, cp, "c6", "nope", nil, 10)
	assert.Equal(t, CodeToolRejected, ctlCode(err), "%v", err)
	_, err = computerCall(t, cp, "c7", "bash", map[string]any{}, 10)
	assert.Equal(t, CodeToolRejected, ctlCode(err), "%v", err)
	_, err = computerCall(t, cp, "c8", "bash", map[string]any{"command": "true"}, computerMaxTimeoutSec+1)
	assert.Equal(t, "invalid_params", ctlCode(err))
	_, err = computerCall(t, cp, "", "bash", map[string]any{"command": "true"}, 10)
	assert.Equal(t, "invalid_params", ctlCode(err))
}

// 超时：回 timeout，并向执行器发出取消（同一 callId 随后可以再用）。
func TestComputerCallTimeoutCancels(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	start := time.Now()
	_, err := computerCall(t, cp, "slow", "bash", map[string]any{"command": "sleep 30"}, 1)
	assert.Equal(t, CodeTimeout, ctlCode(err), "%v", err)
	assert.Less(t, time.Since(start), 10*time.Second)

	require.Eventually(t, func() bool {
		res, err := computerCall(t, cp, "slow", "bash", map[string]any{"command": "echo again"}, 10)
		return err == nil && resultText(t, res.Result) == "again\n"
	}, 10*time.Second, 100*time.Millisecond, "the timed-out call was not cancelled in the executor")
}

// computer.cancel：在途调用以工具中止时的结果返回。
func TestComputerCancel(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	from := cp.notifCount()

	var (
		wg  sync.WaitGroup
		res apigen.LinkCtlComputerCallResult
		err error
	)
	wg.Go(func() {
		res, err = computerCall(t, cp, "k", "bash", map[string]any{"command": "echo started; sleep 30"}, 60)
	})
	cp.waitNotif(t, from, string(apigen.MethodComputerUpdate), nil)
	var ok apigen.LinkCtlOkResult
	require.NoError(t, cp.call(t, string(apigen.MethodComputerCancel), apigen.LinkCtlComputerCancelParams{CallID: "k"}, &ok))
	assert.True(t, ok.Ok)
	wg.Wait()
	require.NoError(t, err)
	assert.Equal(t, true, res.Result["isError"])
	assert.Equal(t, "Cancelled by host", resultText(t, res.Result))

	// 没有这个调用时同样成功
	require.NoError(t, cp.call(t, string(apigen.MethodComputerCancel), apigen.LinkCtlComputerCancelParams{CallID: "gone"}, &ok))
}

// 执行器崩溃：在途调用以 agent_exited 失败，下一条调用重新拉起。
func TestComputerCrashRecovery(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)

	_, err := computerCall(t, cp, "boom", "bash", map[string]any{"command": "kill -9 $PPID; sleep 5"}, 30)
	assert.Equal(t, CodeAgentExited, ctlCode(err), "%v", err)
	require.Eventually(t, func() bool { return !rt.comp.running() }, 5*time.Second, 20*time.Millisecond)

	res, err := computerCall(t, cp, "after", "bash", map[string]any{"command": "echo back"}, 30)
	require.NoError(t, err)
	assert.Equal(t, "back\n", resultText(t, res.Result))
}

// 空闲退出：没有调用满 ComputerIdleExit 后停掉进程；在途调用期间不计空闲；之后的调用重新拉起。
// computer.stop 立即停掉。
func TestComputerIdleExitAndStop(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) { c.ComputerIdleExit = 300 * time.Millisecond })
	cp.waitNotif(t, 0, "hello", nil)

	res, err := computerCall(t, cp, "long", "bash", map[string]any{"command": "sleep 0.8; echo done"}, 30)
	require.NoError(t, err, "a call longer than the idle period is not interrupted")
	assert.Equal(t, "done\n", resultText(t, res.Result))
	assert.True(t, rt.comp.running())
	require.Eventually(t, func() bool { return !rt.comp.running() }, 5*time.Second, 20*time.Millisecond)

	res, err = computerCall(t, cp, "again", "bash", map[string]any{"command": "echo up"}, 30)
	require.NoError(t, err)
	assert.Equal(t, "up\n", resultText(t, res.Result))

	// 之后的 stop 不受空闲计时影响
	rt.comp.mu.Lock()
	rt.comp.idle = time.Hour
	rt.comp.mu.Unlock()
	_, err = computerCall(t, cp, "once", "bash", map[string]any{"command": "true"}, 30)
	require.NoError(t, err)
	var ok apigen.LinkCtlOkResult
	require.NoError(t, cp.call(t, string(apigen.MethodComputerStop), map[string]any{}, &ok))
	assert.True(t, ok.Ok)
	assert.False(t, rt.comp.running())
}

// runner 与协调宿主没有工具执行器：computer.* 一律 not_supported，hello 也不报 computer。
func TestComputerNotOnRunnerOrCoordinator(t *testing.T) {
	methods := []string{
		string(apigen.MethodComputerStart), string(apigen.MethodComputerCall),
		string(apigen.MethodComputerCancel), string(apigen.MethodComputerStop),
	}

	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	assert.NotContains(t, helloOf(t, f.cp).Features, "computer")
	for _, m := range methods {
		err := f.cp.call(t, m, map[string]any{}, nil)
		assert.Equal(t, CodeNotSupported, ctlCode(err), "runner %s: %v", m, err)
	}

	_, cp, _, _ := startCoordinator(t)
	assert.NotContains(t, helloOf(t, cp).Features, "computer")
	for _, m := range methods {
		err := cp.call(t, m, map[string]any{}, nil)
		assert.Equal(t, CodeNotSupported, ctlCode(err), "coordinator %s: %v", m, err)
	}
}
