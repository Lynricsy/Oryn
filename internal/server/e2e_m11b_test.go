package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

// M11b runner 端到端：进程内 orynd + 以 `orbd run --runner --dir <临时目录>` 起的真实 orbd（ORYN_AGENT_BIN=fakeomp）。
// 建 runner → orbd 经 whoami 得知 orb、拨 link 上线 → 建线程 → idle，fakeomp 以该目录为工作目录启动。
func TestFullStackRunner(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	var created struct {
		Runner struct {
			ID    string `json:"id"`
			OrbID string `json:"orbId"`
		} `json:"runner"`
		Token string `json:"token"`
	}
	s.rest("POST", "/api/v1/runners", map[string]string{"name": "laptop"}, &created, 201)

	// whoami 与 link 挂在同一路由组
	req, _ := http.NewRequest("GET", s.http.URL+"/orb/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	var who struct {
		OrbID string `json:"orbId"`
		Kind  string `json:"kind"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&who))
	_ = res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, created.Runner.OrbID, who.OrbID)
	assert.Equal(t, "runner", who.Kind)

	work, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home, dump := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "orbd"), "run", "--runner", "--dir", work, "--allow-root")
	cmd.Env = append(os.Environ(),
		"HOME="+home, "ORYN_URL="+s.http.URL, "ORYN_RUNNER_TOKEN="+created.Token,
		"ORYN_AGENT_BIN="+filepath.Join(binDir, "fakeomp"), "FAKEOMP_DUMP_DIR="+dump, "FAKEOMP_DELAY_MS=15",
	)
	var logs strings.Builder
	cmd.Stdout, cmd.Stderr = &logs, &logs
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("orbd 日志：\n%s", logs.String())
		}
	})

	var list struct {
		Runners []struct {
			Online bool `json:"online"`
			Dirs   []struct {
				Path string `json:"path"`
			} `json:"dirs"`
		} `json:"runners"`
	}
	require.Eventually(t, func() bool {
		s.rest("GET", "/api/v1/runners", nil, &list, 200)
		return len(list.Runners) == 1 && list.Runners[0].Online
	}, 30*time.Second, 50*time.Millisecond, "runner 上线")
	require.Len(t, list.Runners[0].Dirs, 1)
	assert.Equal(t, work, list.Runners[0].Dirs[0].Path)

	var th struct {
		Thread struct {
			ID  string  `json:"id"`
			Cwd *string `json:"cwd"`
		} `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt":   "hello from a runner",
		"executor": map[string]any{"kind": "runner", "runnerId": created.Runner.ID, "dir": work},
	}, &th, 201)
	require.NotNil(t, th.Thread.Cwd)
	assert.Equal(t, work, *th.Thread.Cwd)
	s.waitThread(th.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" })

	// fakeomp 的转储（FAKEOMP_DUMP_DIR/<线程 id>.json）：工作目录是该目录
	b, err := os.ReadFile(filepath.Join(dump, th.Thread.ID+".json"))
	require.NoError(t, err)
	var rec struct {
		Cwd string            `json:"cwd"`
		Env map[string]string `json:"env"`
	}
	require.NoError(t, json.Unmarshal(b, &rec))
	assert.Equal(t, work, rec.Cwd)
	assert.Equal(t, "1", rec.Env["ORYN_RUNNER"])
	assert.Empty(t, rec.Env["ORYN_ORB"])
}
