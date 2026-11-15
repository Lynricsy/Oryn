package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

// runnerThreadsEnv 是 runner 线程的测试环境：threadstest 的 orb 替身（含 runner）+ 线程服务 + API 路由。
type runnerThreadsEnv struct {
	*threadsEnv
	orbs *threadstest.Orbs
	// app / lib 是 runner 声明的两个目录（真实存在：fakeomp 以它们为 --cwd 启动）。
	app, lib    string
	runner, orb string
}

func newRunnerThreadsEnv(t *testing.T, features ...string) *runnerThreadsEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "data"))
	require.NoError(t, err)
	o, err := threadstest.NewOrbs(st, fakeomp(t), filepath.Join(dir, "orbs"), "FAKEOMP_DELAY_MS=5", "FAKEOMP_FORK=1")
	require.NoError(t, err)
	svc := threads.New(threads.Config{DataDir: filepath.Join(dir, "data")}, st, o)
	cfg := config.DefaultOrynd()
	cfg.DataDir = filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Threads = &api.ThreadsDeps{Service: svc}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		o.Close()
		svc.Close()
		_ = st.Close()
	})
	e := &runnerThreadsEnv{threadsEnv: &threadsEnv{t: t, st: st, svc: svc, srv: srv, dir: dir}, orbs: o}
	e.app, e.lib = filepath.Join(dir, "work", "app"), filepath.Join(dir, "work", "lib")
	for _, d := range []string{e.app, e.lib, e.app + "-feat", filepath.Join(e.app, "sub")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	e.runner, e.orb, err = o.AddRunner("laptop", []apigen.RunnerDir{{Path: e.app, Name: "app", Git: true}, {Path: e.lib, Name: "lib"}}, features)
	require.NoError(t, err)
	return e
}

func (e *runnerThreadsEnv) create(body map[string]any) (int, []byte) {
	e.t.Helper()
	exec := map[string]any{"kind": "runner", "runnerId": e.runner}
	for k, v := range body {
		if k != "prompt" && k != "browserProfileId" {
			exec[k] = v
		}
	}
	req := map[string]any{"prompt": "hello", "executor": exec}
	if v, ok := body["browserProfileId"]; ok {
		req["browserProfileId"] = v
	}
	if v, ok := body["prompt"]; ok {
		req["prompt"] = v
	}
	return e.do("POST", "/api/v1/threads", req)
}

func (e *runnerThreadsEnv) thread(id string) apigen.Thread {
	e.t.Helper()
	code, body := e.do("GET", "/api/v1/threads/"+id, nil)
	require.Equal(e.t, http.StatusOK, code, string(body))
	var th apigen.Thread
	require.NoError(e.t, json.Unmarshal(body, &th))
	return th
}

func (e *runnerThreadsEnv) waitIdle(id string) apigen.Thread {
	e.t.Helper()
	var th apigen.Thread
	require.Eventually(e.t, func() bool {
		th = e.thread(id)
		return th.State == "idle" && th.LeafEntryID != nil
	}, 20*time.Second, 20*time.Millisecond, "thread %s did not become idle (now %s)", id, th.State)
	return th
}

// created 要求 201 并取出新线程（参数是 do / create 的两个返回值）。
func (e *runnerThreadsEnv) created(code int, body []byte) apigen.Thread {
	e.t.Helper()
	require.Equal(e.t, http.StatusCreated, code, string(body))
	var env apigen.ThreadEnvelope
	require.NoError(e.t, json.Unmarshal(body, &env))
	return env.Thread
}

func apiErr(t *testing.T, body []byte) apigen.Error {
	t.Helper()
	var e apigen.Error
	require.NoError(t, json.Unmarshal(body, &e), string(body))
	return e
}

func frameTypes(fs []apigen.OmpFrame) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f["type"].(string))
	}
	return out
}

func TestRunnerThreadCreate(t *testing.T) {
	e := newRunnerThreadsEnv(t, "fs", "git", "metrics")

	// 离线 → 409 runner_offline；不存在的 runner → 404
	code, body := e.create(map[string]any{"dir": e.app})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "runner_offline", apiErr(t, body).Code)
	code, _ = e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": map[string]any{"kind": "runner", "runnerId": "prov_missing", "dir": e.app}})
	assert.Equal(t, http.StatusNotFound, code)

	e.orbs.ConnectRunner(e.orb)

	// 目录不在声明之内（含相对路径、`..` 逃出、前缀相同的兄弟目录）→ 400 dir_not_served
	for _, d := range []string{"/etc", "work/app", e.app + "/../other", e.app + "x"} {
		code, body = e.create(map[string]any{"dir": d})
		assert.Equal(t, http.StatusBadRequest, code, d)
		assert.Equal(t, "dir_not_served", apiErr(t, body).Code, d)
	}
	code, body = e.create(map[string]any{"dir": e.app, "browserProfileId": "bpf_x"})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "invalid", apiErr(t, body).Code)

	// 成功：cwd = 目录（规范化），agent.start 带 cwd，无 terminal 特性时不发 terminal_configure
	th := e.created(e.create(map[string]any{"dir": e.app + "/"}))
	require.NotNil(t, th.Cwd)
	assert.Equal(t, e.app, *th.Cwd)
	assert.Nil(t, th.Worktree)
	require.NotNil(t, th.OrbID)
	assert.Equal(t, e.orb, *th.OrbID)
	e.waitIdle(th.ID)
	starts := e.orbs.Starts(th.ID)
	require.NotEmpty(t, starts)
	assert.Equal(t, e.app, starts[0].Cwd)
	assert.NotContains(t, frameTypes(starts[0].InitFrames), "terminal_configure")
	assert.Contains(t, frameTypes(starts[0].InitFrames), "set_subagent_subscription")

	// 同一目录 → 409 dir_in_use（带线程 id）；子目录不算同一目录；shareDir 放行
	code, body = e.create(map[string]any{"dir": e.app})
	require.Equal(t, http.StatusConflict, code)
	var conflict apigen.DirInUseConflict
	require.NoError(t, json.Unmarshal(body, &conflict))
	assert.Equal(t, apigen.DirInUseConflictCodeDirInUse, conflict.Code)
	assert.Equal(t, []string{th.ID}, conflict.ThreadIds)
	sub := e.created(e.create(map[string]any{"dir": filepath.Join(e.app, "sub")}))
	assert.Equal(t, filepath.Join(e.app, "sub"), *sub.Cwd)
	shared := e.created(e.create(map[string]any{"dir": e.app, "shareDir": true}))
	assert.Equal(t, e.app, *shared.Cwd)

	// worktree：cwd 取 worktree.add 返回的目录，worktree 记下 {repo, dir, branch}
	wt := e.created(e.create(map[string]any{"dir": e.app, "worktree": map[string]any{"name": "feat"}}))
	require.NotNil(t, wt.Cwd)
	assert.Equal(t, e.app+"-feat", *wt.Cwd)
	require.NotNil(t, wt.Worktree)
	assert.Equal(t, apigen.ThreadWorktree{Repo: e.app, Dir: e.app + "-feat", Branch: "feat"}, *wt.Worktree)
	assert.Equal(t, []apigen.LinkCtlWorktreeAddParams{{Repo: e.app, Name: "feat"}}, e.orbs.Worktrees())
	e.waitIdle(wt.ID)
	assert.Equal(t, e.app+"-feat", e.orbs.Starts(wt.ID)[0].Cwd)

	// worktree.add 失败 → 400 worktree_failed（git 的报错原样），不留线程
	before, err := e.svc.List(context.Background(), threads.ListFilter{})
	require.NoError(t, err)
	e.orbs.FailWorktree("worktree.add", &link.CtlError{Code: "failed", Message: "fatal: a branch named 'x' already exists"})
	code, body = e.create(map[string]any{"dir": e.app, "worktree": map[string]any{"name": "x"}})
	assert.Equal(t, http.StatusBadRequest, code)
	ae := apiErr(t, body)
	assert.Equal(t, "worktree_failed", ae.Code)
	assert.Contains(t, ae.Message, "already exists")
	after, err := e.svc.List(context.Background(), threads.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, after, len(before))
}

func TestRunnerThreadTerminalFeature(t *testing.T) {
	e := newRunnerThreadsEnv(t, "fs", "git", "terminal")
	e.orbs.ConnectRunner(e.orb)
	th := e.created(e.create(map[string]any{"dir": e.lib}))
	e.waitIdle(th.ID)
	assert.Contains(t, frameTypes(e.orbs.Starts(th.ID)[0].InitFrames), "terminal_configure", "上报了 terminal 才开终端")
}

func TestRunnerThreadOfflineQueue(t *testing.T) {
	e := newRunnerThreadsEnv(t, "fs")
	e.orbs.ConnectRunner(e.orb)
	th := e.created(e.create(map[string]any{"dir": e.app}))
	first := e.waitIdle(th.ID)

	// runner 离线：命令受理并排队，wake → 409 runner_offline
	e.orbs.DisconnectRunner(e.orb)
	replies := make(chan json.RawMessage, 1)
	require.NoError(t, e.svc.Command(context.Background(), th.ID, json.RawMessage(`{"type":"prompt","message":"while offline"}`), func(r json.RawMessage) { replies <- r }))
	code, body := e.do("POST", "/api/v1/threads/"+th.ID+"/wake", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "runner_offline", apiErr(t, body).Code)
	select {
	case r := <-replies:
		t.Fatalf("runner 离线时命令不应被下发：%s", r)
	default:
	}

	// runner 回来：ready 事件触发启动 agent 并按序下发
	e.orbs.ConnectRunner(e.orb)
	select {
	case r := <-replies:
		assert.Contains(t, string(r), `"success":true`)
	case <-time.After(20 * time.Second):
		t.Fatal("runner 重连后排队的命令没有下发")
	}
	require.Eventually(t, func() bool {
		cur := e.thread(th.ID)
		return cur.State == "idle" && cur.LeafEntryID != nil && *cur.LeafEntryID != *first.LeafEntryID
	}, 20*time.Second, 20*time.Millisecond)
}

func TestRunnerThreadForkAndWorktreeRemove(t *testing.T) {
	e := newRunnerThreadsEnv(t, "fs")
	e.orbs.ConnectRunner(e.orb)
	plain := e.created(e.create(map[string]any{"dir": e.app}))
	wt := e.created(e.create(map[string]any{"dir": e.app, "worktree": map[string]any{"name": "feat"}}))
	e.waitIdle(plain.ID)
	e.waitIdle(wt.ID)

	// fork：copy → 409 runner_unsupported；share 继承 cwd、不继承 worktree
	code, body := e.do("POST", "/api/v1/threads/"+wt.ID+"/fork", map[string]any{"env": "copy"})
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "runner_unsupported", apiErr(t, body).Code)
	child := e.created(e.do("POST", "/api/v1/threads/"+wt.ID+"/fork", map[string]any{"env": "share"}))
	require.NotNil(t, child.Cwd)
	assert.Equal(t, e.app+"-feat", *child.Cwd)
	assert.Nil(t, child.Worktree)
	e.waitIdle(child.ID)
	assert.Equal(t, e.app+"-feat", e.orbs.Starts(child.ID)[0].Cwd)

	// 移除 worktree：没有 worktree → 409 conflict；有未提交改动 → 409 worktree_dirty（git 报错原样）；成功 → 204 并清空
	code, body = e.do("POST", "/api/v1/threads/"+plain.ID+"/worktree/remove", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "conflict", apiErr(t, body).Code)
	e.orbs.FailWorktree("worktree.remove", &link.CtlError{Code: "worktree_dirty", Message: "fatal: contains modified or untracked files"})
	code, body = e.do("POST", "/api/v1/threads/"+wt.ID+"/worktree/remove", nil)
	assert.Equal(t, http.StatusConflict, code)
	ae := apiErr(t, body)
	assert.Equal(t, "worktree_dirty", ae.Code)
	assert.Contains(t, ae.Message, "modified")
	require.NotNil(t, e.thread(wt.ID).Worktree)
	e.orbs.FailWorktree("worktree.remove", nil)
	code, body = e.do("POST", "/api/v1/threads/"+wt.ID+"/worktree/remove", nil)
	require.Equal(t, http.StatusNoContent, code, string(body))
	got := e.thread(wt.ID)
	assert.Nil(t, got.Worktree)
	assert.Equal(t, e.app+"-feat", *got.Cwd, "cwd 不变")

	// runner 离线时移除 → 409 runner_offline
	require.NoError(t, os.MkdirAll(e.lib+"-two", 0o755))
	wt2 := e.created(e.create(map[string]any{"dir": e.lib, "worktree": map[string]any{"name": "two"}}))
	e.orbs.DisconnectRunner(e.orb)
	code, body = e.do("POST", "/api/v1/threads/"+wt2.ID+"/worktree/remove", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "runner_offline", apiErr(t, body).Code)
}
