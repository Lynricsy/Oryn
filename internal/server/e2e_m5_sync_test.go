package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/syncclient"
)

// M5 §4：`oryn sync` 全栈——process 驱动 + 真实 orbd；syncclient 经真实 REST（控制面 → orb link → orbd /sync/*）。

// gitEnvE2E 只给测试自己的 git 命令用（不进进程环境：orbd 会继承进程环境并执行 `git config --global`，
// 指向 /dev/null 的 GIT_CONFIG_GLOBAL 会让以 root 运行的 git 把 /dev/null 替换成普通文件）。
var gitEnvE2E = []string{
	"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
	"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
}

func gitE2E(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnvE2E...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func TestE2EM5SyncApply(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "hi", "executor": map[string]any{"kind": "new_orb", "provider": "local"}}, &created, 201)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })

	// origin（裸）→ orb 的 $HOME/workspace/repo：一个未推送的提交 + 未提交改动 + 未跟踪文件
	tmp := t.TempDir()
	origin, seed := filepath.Join(tmp, "origin.git"), filepath.Join(tmp, "seed")
	gitE2E(t, tmp, "init", "-q", "--bare", "-b", "main", origin)
	gitE2E(t, tmp, "init", "-q", "-b", "main", seed)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("base\n"), 0o644))
	gitE2E(t, seed, "add", "-A")
	gitE2E(t, seed, "commit", "-q", "-m", "base")
	gitE2E(t, seed, "push", "-q", origin, "main")
	repo := filepath.Join(s.drv.HomeDir(th.OrbID), "workspace", "repo")
	gitE2E(t, tmp, "clone", "-q", origin, repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("committed in orb\n"), 0o644))
	gitE2E(t, repo, "add", "feature.txt")
	gitE2E(t, repo, "commit", "-q", "-m", "orb commit")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\nedited in orb\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644))

	local := filepath.Join(tmp, "local")
	gitE2E(t, tmp, "clone", "-q", origin, local)
	remote := &syncclient.HTTPRemote{BaseURL: s.http.URL, Token: s.token, OrbID: th.OrbID}
	opts := syncclient.Options{Dir: local, ThreadID: th.ID, Root: "~/workspace/repo"}
	require.NoError(t, syncclient.Apply(context.Background(), remote, opts))
	assert.Equal(t, gitE2E(t, repo, "rev-parse", "HEAD"), gitE2E(t, local, "rev-parse", "HEAD"))
	assert.Equal(t, "main", gitE2E(t, local, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, gitE2E(t, repo, "status", "--porcelain"), gitE2E(t, local, "status", "--porcelain"))
	for _, n := range []string{"README.md", "feature.txt", "untracked.txt"} {
		want, err := os.ReadFile(filepath.Join(repo, n))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(local, n))
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), n)
	}
	assert.Empty(t, gitE2E(t, local, "for-each-ref", "refs/oryn"))

	// 本地已脏：前置检查拒绝，不碰 orb 之外的任何东西
	require.ErrorIs(t, syncclient.Apply(context.Background(), remote, opts), syncclient.ErrDirty)

	// 冻结的 orb：bg=1 → 409 orb_not_ready，且不唤醒
	base := "/api/v1/orbs/" + th.OrbID
	orbState := func() string {
		var o struct {
			State string `json:"state"`
		}
		s.rest("GET", base, nil, &o, 200)
		return o.State
	}
	s.rest("POST", base+"/pause", map[string]string{"mode": "freeze"}, nil, 202)
	require.Eventually(t, func() bool { return orbState() == "frozen" }, 30*time.Second, 50*time.Millisecond)
	root := "~/workspace/repo"
	_, err := remote.Prepare(context.Background(), apigen.OrbSyncPrepareRequest{Root: &root}, true)
	require.Error(t, err)
	assert.Equal(t, "orb_not_ready", syncclient.Code(err))
	var ae *syncclient.APIError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, 409, ae.Status)
	assert.Equal(t, "frozen", orbState())
}
