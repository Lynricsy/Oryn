package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

// M14 步骤 1.6：orb 里的 agent 经 orbd.sock 的 artifact.call 建 artifact（扩展工具与 `oryn artifact call` 走同一条路），
// 控制面经 library.fetch 取回 source 的字节。

var (
	orynBinOnce sync.Once
	orynBinPath string
	orynBinErr  error
)

// orynBin 构建一次 cmd/oryn（只有本文件的用例需要，不放进 TestMain）。
func orynBin(t *testing.T) string {
	t.Helper()
	orynBinOnce.Do(func() {
		orynBinPath = filepath.Join(binDir, "oryn")
		if out, err := exec.Command("go", "build", "-o", orynBinPath, "../../cmd/oryn").CombinedOutput(); err != nil {
			orynBinErr = fmt.Errorf("构建 oryn 失败: %v\n%s", err, out)
		}
	})
	require.NoError(t, orynBinErr)
	return orynBinPath
}

type artifactCallOut struct {
	Text    string         `json:"text"`
	Details map[string]any `json:"details"`
}

// artifactCall 以 orb 里线程 threadID 的身份运行 `oryn artifact call <op> --params <params> --json`。
func artifactCall(t *testing.T, home, threadID, op string, params any) artifactCallOut {
	t.Helper()
	p, err := json.Marshal(params)
	require.NoError(t, err)
	cmd := exec.Command(orynBin(t), "artifact", "call", op, "--params", string(p), "--json")
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "ORYN_ORB=1", "ORYN_THREAD_ID=" + threadID}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "oryn artifact call %s: %s", op, stderr.String())
	var res artifactCallOut
	require.NoError(t, json.Unmarshal(out, &res), "%s", out)
	return res
}

func TestE2EArtifactsAgent(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "hi", "executor": map[string]any{"kind": "new_orb", "provider": "local", "type": "small"}}, &created, 201)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })
	home := s.drv.HomeDir(th.OrbID)

	res := artifactCall(t, home, th.ID, "create_artifact", map[string]any{"title": "报告", "html": "<h1>hi</h1>"})
	id, _ := res.Details["id"].(string)
	require.NotEmpty(t, id, "details: %v", res.Details)
	assert.Equal(t, "create_artifact", res.Details["op"])

	type artifactRow struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Version  int    `json:"version"`
		ThreadID string `json:"threadId"`
	}
	var list struct {
		Artifacts []artifactRow `json:"artifacts"`
	}
	s.rest("GET", "/api/v1/artifacts?threadId="+th.ID, nil, &list, 200)
	require.Len(t, list.Artifacts, 1)
	assert.Equal(t, artifactRow{ID: id, Kind: "web", Version: 1, ThreadID: th.ID}, list.Artifacts[0])

	// source 指向 orb 里的文件：控制面在这次请求里经 library.fetch 取回字节。
	data := make([]byte, 300<<10)
	for i := range data {
		data[i] = byte(i*7 + i>>8)
	}
	src := filepath.Join(home, "workspace", "chart.bin")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o755))
	require.NoError(t, os.WriteFile(src, data, 0o644))
	res = artifactCall(t, home, th.ID, "create_artifact", map[string]any{
		"title": "图表",
		"files": []map[string]any{
			{"path": "index.html", "content": `<img src="data/chart.bin">`},
			{"path": "data/chart.bin", "source": src},
		},
	})
	id2, _ := res.Details["id"].(string)
	require.NotEmpty(t, id2)
	assert.NotEqual(t, id, id2)

	req, err := http.NewRequest("GET", s.http.URL+"/api/v1/artifacts/"+id2+"/file?path=data/chart.bin", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(data, got), "source 的字节原样存下（%d / %d 字节）", len(got), len(data))

	list.Artifacts = nil
	s.rest("GET", "/api/v1/artifacts?threadId="+th.ID, nil, &list, 200)
	assert.Len(t, list.Artifacts, 2)
}
