package orbdrt

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// memoryEnvKeys 是 memory 参数可能写入或删除的环境变量。
var memoryEnvKeys = []string{"HINDSIGHT_API_URL", "HINDSIGHT_BANK_ID", "HINDSIGHT_SCOPING", "HINDSIGHT_AUTO_RETAIN", "HINDSIGHT_API_TOKEN", "ORYN_HINDSIGHT_PROJECT"}

// startAgentMem 以 memory 参数执行 agent.start（orb 形态：会话目录在 ~/.oryn/threads 下）。
func startAgentMem(t *testing.T, cp *fakeCP, thread, cwd string, env map[string]string, m *apigen.LinkCtlAgentMemory) int {
	t.Helper()
	var res apigen.LinkCtlAgentStartResult
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: thread, Cwd: cwd, SessionDir: "~/.oryn/threads/" + thread, Env: env, InitFrames: initFrames(thread), Memory: m,
	}, &res))
	require.Positive(t, res.Pid)
	return res.Pid
}

func configArgs(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func assertNoMemory(t *testing.T, rt *Runtime, rec agentDump) {
	t.Helper()
	for k := range rec.Env {
		assert.False(t, strings.HasPrefix(k, "HINDSIGHT_"), "unexpected %s", k)
	}
	assert.NotContains(t, rec.Env, "ORYN_HINDSIGHT_PROJECT")
	assert.NotContains(t, rec.Args, rt.memoryOverlayPath())
}

// memory 缺省：不写覆盖层、不加 --config、环境里没有任何 HINDSIGHT_*。
func TestMemoryAgentStartNil(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	dump := filepath.Join(rt.home, "dump")
	startAgent(t, cp, thread1, map[string]string{"FAKEOMP_DUMP_DIR": dump})
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	rec := setupDump(t, dump, thread1)
	assertNoMemory(t, rt, rec)
	assert.NotContains(t, rec.Args, "--config")
	assert.NoFileExists(t, rt.memoryOverlayPath())
}

// memory 启用（orb）：覆盖层内容固定；env 在 p.Env 之后设置，env 密钥改不了 HINDSIGHT_*；NO_PROXY 合并两份已有值
// 并追加本机地址；继承的 HINDSIGHT_API_TOKEN 被删除；项目与 autoRetain 两种组合。
func TestMemoryAgentStartEnv(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	dump := filepath.Join(rt.home, "dump")
	evil := map[string]string{
		"FAKEOMP_DUMP_DIR":       dump,
		"HINDSIGHT_API_URL":      "http://evil.example",
		"HINDSIGHT_BANK_ID":      "other",
		"HINDSIGHT_SCOPING":      "global",
		"HINDSIGHT_AUTO_RETAIN":  "true",
		"HINDSIGHT_API_TOKEN":    "hsk_real",
		"ORYN_HINDSIGHT_PROJECT": "prj_evil",
		"NO_PROXY":               "corp.example, localhost",
		"no_proxy":               "internal.example,corp.example",
	}
	proj := "prj_01hzyaaaa"
	startAgentMem(t, cp, thread1, "", evil, &apigen.LinkCtlAgentMemory{Project: &proj, AutoRetain: true})
	cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	rec := setupDump(t, dump, thread1)
	assert.Equal(t, "http://"+rt.EgressAddr()+"/memory/t/"+thread1, rec.Env["HINDSIGHT_API_URL"])
	assert.Equal(t, "oryn", rec.Env["HINDSIGHT_BANK_ID"])
	assert.Equal(t, "per-project-tagged", rec.Env["HINDSIGHT_SCOPING"])
	assert.Equal(t, proj, rec.Env["ORYN_HINDSIGHT_PROJECT"])
	assert.Equal(t, "true", rec.Env["HINDSIGHT_AUTO_RETAIN"], "autoRetain 为真时不设置（env 原值保留）")
	assert.NotContains(t, rec.Env, "HINDSIGHT_API_TOKEN")
	assert.Equal(t, "corp.example,localhost,internal.example,127.0.0.1", rec.Env["NO_PROXY"])
	assert.Equal(t, rec.Env["NO_PROXY"], rec.Env["no_proxy"])
	assert.Equal(t, rt.memoryOverlayPath(), filepath.Join(rt.home, ".oryn", "memory-overlay.yml"))
	assert.Equal(t, []string{rt.memoryOverlayPath()}, configArgs(rec.Args))
	assert.Equal(t, "# 由 orbd 生成（M16）：启用 Oryn 内置的 Hindsight 记忆。\nmemory:\n  backend: hindsight\nhindsight:\n  globalScope: true\n", readFile(t, rt.memoryOverlayPath()))

	// 无项目线程：不带 ORYN_HINDSIGHT_PROJECT（env 里的也去掉），关闭自动写入；没有 NO_PROXY 时只有本机地址。
	delete(evil, "NO_PROXY")
	delete(evil, "no_proxy")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	startAgentMem(t, cp, projThread2, "", evil, &apigen.LinkCtlAgentMemory{AutoRetain: false})
	cp.waitNotif(t, 0, "agent.status", statusIs(projThread2, "ready"))
	rec = setupDump(t, dump, projThread2)
	assert.Equal(t, "http://"+rt.EgressAddr()+"/memory/t/"+projThread2, rec.Env["HINDSIGHT_API_URL"])
	assert.NotContains(t, rec.Env, "ORYN_HINDSIGHT_PROJECT")
	assert.Equal(t, "false", rec.Env["HINDSIGHT_AUTO_RETAIN"])
	assert.NotContains(t, rec.Env, "HINDSIGHT_API_TOKEN")
	assert.Equal(t, "127.0.0.1,localhost", rec.Env["NO_PROXY"])
	assert.Equal(t, "127.0.0.1,localhost", rec.Env["no_proxy"])
}

// 桌面 orb：记忆覆盖层在项目层 --config 之后、桌面覆盖层之前。
func TestMemoryOverlayOrder(t *testing.T) {
	e := newDeskEnv(t, nil)
	writeTree(t, filepath.Join(e.rt.projectConfigRoot(), ".omp"), map[string]string{"config.yml": "theme: dark\n"})
	pid := startAgentMem(t, e.cp, thread1, "", nil, &apigen.LinkCtlAgentMemory{AutoRetain: true})
	e.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	assert.Equal(t, []string{
		filepath.Join(e.rt.projectConfigRoot(), ".omp", "config.yml"),
		e.rt.memoryOverlayPath(),
		e.rt.desktopOverlayPath(),
	}, configArgs(procArgs(t, pid)))
}

// 协调宿主收到 memory → invalid_params。
func TestMemoryRejectedOnCoordinator(t *testing.T) {
	_, cp, _, _ := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	coord := apigen.AgentProfileCoordinator
	prompt := "a\nb"
	err := cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: thread1, Profile: &coord, SystemPrompt: &prompt, Memory: &apigen.LinkCtlAgentMemory{},
	}, nil)
	assert.Equal(t, link.CodeInvalidParams, ctlCode(err), "%v", err)
}

// hello：普通 orbd 与 runner 上报 memory，协调宿主不上报。
func TestMemoryHelloFeatures(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	assert.Contains(t, helloOf(t, cp).Features, "memory")

	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	assert.Contains(t, helloOf(t, f.cp).Features, "memory")

	_, ccp, _, _ := startCoordinator(t)
	assert.NotContains(t, helloOf(t, ccp).Features, "memory")
}

// freeAddr 占一个空闲端口再放掉，返回其地址。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// runner：不带 memory 的 agent.start 不监听；第一个带 memory 的才在 EgressAddr 上开记忆通道，请求原样经 egress 流
// 转给控制面；之后的线程共用同一个地址。
func TestRunnerMemoryEgress(t *testing.T) {
	f := newRunnerHome(t)
	egress := freeAddr(t)
	startRunnerRT(t, f, func(c *Config) { c.EgressAddr = egress })
	helloOf(t, f.cp)
	dump := filepath.Join(f.home, "dump")
	env := map[string]string{"FAKEOMP_DUMP_DIR": dump}

	startAgentMem(t, f.cp, thread1, f.work, env, nil)
	f.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	assertNoMemory(t, f.rt, setupDump(t, dump, thread1))
	assert.Empty(t, f.rt.EgressAddr())
	probe, err := net.Listen("tcp", egress)
	require.NoError(t, err, "no listener without memory")
	require.NoError(t, probe.Close())

	proj := "prj_r"
	startAgentMem(t, f.cp, projThread2, f.work, env, &apigen.LinkCtlAgentMemory{Project: &proj, AutoRetain: true})
	f.cp.waitNotif(t, 0, "agent.status", statusIs(projThread2, "ready"))
	rec := setupDump(t, dump, projThread2)
	assert.Equal(t, egress, f.rt.EgressAddr())
	assert.Equal(t, "http://"+egress+"/memory/t/"+projThread2, rec.Env["HINDSIGHT_API_URL"])
	assert.Equal(t, proj, rec.Env["ORYN_HINDSIGHT_PROJECT"])
	assert.Equal(t, []string{f.rt.memoryOverlayPath()}, configArgs(rec.Args))

	req, err := http.NewRequest(http.MethodGet, "http://"+egress+"/memory/t/"+projThread2+"/v1/default/banks/x", nil)
	require.NoError(t, err)
	req.Header.Set("X-Test", "runner")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "egress GET /memory/t/"+projThread2+"/v1/default/banks/x runner", string(body))

	addr, err := f.rt.memoryEgressAddr()
	require.NoError(t, err)
	assert.Equal(t, egress, addr, "所有线程共用一个监听")
}

// runner 的记忆通道绑不上（端口被占）：agent 照常启动，不带记忆。
func TestRunnerMemoryEgressBindFailure(t *testing.T) {
	f := newRunnerHome(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	startRunnerRT(t, f, func(c *Config) { c.EgressAddr = busy.Addr().String() })
	helloOf(t, f.cp)
	dump := filepath.Join(f.home, "dump")
	startAgentMem(t, f.cp, thread1, f.work, map[string]string{"FAKEOMP_DUMP_DIR": dump}, &apigen.LinkCtlAgentMemory{AutoRetain: true})
	f.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	assertNoMemory(t, f.rt, setupDump(t, dump, thread1))
	assert.Empty(t, f.rt.EgressAddr())
	_, err = os.Stat(f.rt.memoryOverlayPath())
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestMemoryMergeNoProxy(t *testing.T) {
	assert.Equal(t, "127.0.0.1,localhost", mergeNoProxy("", ""))
	assert.Equal(t, "a,127.0.0.1,localhost", mergeNoProxy("a", " a ,"))
	assert.Equal(t, "localhost,b,127.0.0.1", mergeNoProxy("localhost", "b"))
}
