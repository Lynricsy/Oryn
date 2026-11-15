package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func sampleRunners() apigen.RunnerList {
	host, os, arch, ver := "dev\x1b[31mbox", "linux", "amd64", "0.11.0"
	seen := int64(1_000_000 - 90_000)
	return apigen.RunnerList{Runners: []apigen.Runner{
		{
			ID: "prov_R1", Name: "laptop", Online: true, OrbID: "orb-r1", Hostname: &host, Os: &os, Arch: &arch, OrbdVersion: &ver,
			Dirs:       []apigen.RunnerDir{{Path: "/home/u/code/oryn", Name: "oryn", Git: true}, {Path: "/home/u/notes", Name: "notes"}},
			Features:   []string{"fs", "git", "attachments", "metrics"},
			LastSeenAt: &seen,
		},
		{ID: "prov_R2", Name: "nas", OrbID: "orb-r2", Dirs: []apigen.RunnerDir{}, Features: []string{}},
	}}
}

func TestRunnerCreate(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/runners", 201, apigen.RunnerCreated{Runner: sampleRunners().Runners[1], Token: "ort_abc"})
	var out, errb bytes.Buffer
	require.NoError(t, runnerCreate(context.Background(), f.client(), "nas", &out, &errb))
	assert.Equal(t, "ort_abc\n", out.String(), "stdout 只有令牌")
	assert.Contains(t, errb.String(), "ORYN_URL="+f.srv.URL+" ORYN_RUNNER_TOKEN=ort_abc orbd run --runner --dir <目录>")
	reqs := f.requests()
	require.Len(t, reqs, 1)
	assert.JSONEq(t, `{"name":"nas"}`, string(reqs[0].Body))

	f.json("POST /api/v1/runners", 409, apigen.Error{Code: "conflict", Message: "名称已被占用"})
	out.Reset()
	err := runnerCreate(context.Background(), f.client(), "nas", &out, &errb)
	assert.ErrorContains(t, err, "名称已被占用")
	assert.Empty(t, out.String())
}

func TestRunnerLs(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/runners", 200, sampleRunners())
	var out bytes.Buffer
	require.NoError(t, runnerLs(context.Background(), f.client(), false, time.UnixMilli(1_000_000), &out))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	assert.Equal(t, []string{"NAME", "ID", "STATUS", "HOST", "DIRS", "FEATURES", "VERSION", "LAST", "SEEN"}, strings.Fields(lines[0]))
	assert.NotContains(t, out.String(), "\x1b", "服务端文本经 termText，控制字符不进终端")
	assert.Equal(t, []string{"laptop", "prov_R1", "online", "dev", "[31mbox", "linux/amd64", "/home/u/code/oryn(git),/home/u/notes",
		"fs,git,attachments,metrics", "0.11.0", "1m30s", "ago"}, strings.Fields(lines[1]))
	assert.Equal(t, []string{"nas", "prov_R2", "offline", "-", "-", "-", "-", "-"}, strings.Fields(lines[2]))

	out.Reset()
	require.NoError(t, runnerLs(context.Background(), f.client(), true, time.Now(), &out))
	assert.Contains(t, out.String(), `"runners"`)
	assert.Contains(t, out.String(), `"orbId": "orb-r1"`)
}

func TestRunnerRm(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/runners", 200, sampleRunners())
	f.mu.Lock()
	f.routes["DELETE /api/v1/runners/prov_R2"] = func(w http.ResponseWriter, _ []byte) { w.WriteHeader(http.StatusNoContent) }
	f.mu.Unlock()
	var errb bytes.Buffer
	require.NoError(t, runnerRm(context.Background(), f.client(), "nas", &errb))
	assert.Equal(t, "已删除 nas\n", errb.String())
	require.NoError(t, runnerRm(context.Background(), f.client(), "prov_R2", &errb), "也接受 id")
	assert.ErrorContains(t, runnerRm(context.Background(), f.client(), "nope", &errb), "不存在")
	var deletes int
	for _, r := range f.requests() {
		if r.Method == http.MethodDelete {
			deletes++
			assert.Equal(t, "/api/v1/runners/prov_R2", r.Path)
		}
	}
	assert.Equal(t, 2, deletes)
}

func TestThreadNewRunner(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/threads", 201, apigen.ThreadEnvelope{Thread: sampleThread("T-1")})
	f.json("GET /api/v1/runners", 200, sampleRunners())
	ctx := context.Background()
	lastBody := func() string {
		reqs := f.requests()
		return string(reqs[len(reqs)-1].Body)
	}

	_, err := threadNew(ctx, f.client(), "hi", threadNewOptions{runner: "laptop", dir: "/home/u/code/oryn"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"runner","runnerId":"prov_R1","dir":"/home/u/code/oryn"}}`, lastBody())
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{runner: "prov_R1", dir: "/home/u/code/oryn", worktree: "fix-login"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"runner","runnerId":"prov_R1","dir":"/home/u/code/oryn","worktree":{"name":"fix-login"}}}`, lastBody())
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{runner: "laptop", dir: "/home/u/notes/a", shareDir: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"runner","runnerId":"prov_R1","dir":"/home/u/notes/a","shareDir":true}}`, lastBody())

	n := len(f.requests())
	for _, tc := range []struct {
		o    threadNewOptions
		want string
	}{
		{threadNewOptions{dir: "/x"}, "需要与 --runner 一起使用"},
		{threadNewOptions{shareDir: true}, "需要与 --runner 一起使用"},
		{threadNewOptions{runner: "laptop"}, "需要 --dir"},
		{threadNewOptions{runner: "laptop", dir: "code/oryn"}, "绝对路径"},
		{threadNewOptions{runner: "laptop", dir: "/x", orb: "orb-x"}, "不能同时使用"},
		{threadNewOptions{runner: "laptop", dir: "/x", provider: "local"}, "不能同时使用"},
		{threadNewOptions{runner: "laptop", dir: "/x", browserProfile: "工作"}, "浏览器 Profile"},
		{threadNewOptions{runner: "laptop", dir: "/x", worktree: "a", shareDir: true}, "不能同时使用"},
	} {
		_, err := threadNew(ctx, f.client(), "hi", tc.o)
		assert.ErrorContains(t, err, tc.want, "%+v", tc.o)
	}
	assert.Len(t, f.requests(), n, "参数不合法时不发请求")

	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{runner: "nope", dir: "/x"})
	assert.ErrorContains(t, err, `runner "nope" 不存在`)

	// 409 dir_in_use：列出占用的线程并提示 --worktree / --share-dir
	f.json("POST /api/v1/threads", 409, apigen.DirInUseConflict{Code: apigen.DirInUseConflictCodeDirInUse, Message: "dir in use", ThreadIds: []string{"T-a", "T-\x07b"}})
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{runner: "laptop", dir: "/home/u/code/oryn"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "（T-a, T- b）")
	assert.Contains(t, err.Error(), "--worktree <名字>")
	assert.Contains(t, err.Error(), "--share-dir")
}
