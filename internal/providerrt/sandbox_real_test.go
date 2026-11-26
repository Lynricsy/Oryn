package providerrt

import (
	"context"
	"io"
	"net/http/httptest"
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
	"github.com/Lynricsy/Oryn/internal/ids"
)

// TestRealDockerSandbox 在 ORYN_TEST_DOCKER=1 且本机有含精简模式 orbd 的 oryn-orb:dev（make image）时运行：
// none 沙箱经 unix 中继连上测试里的 link 服务端、hello 带 sandbox、/proc/net/dev 只有 lo；
// egress 沙箱 set_network 到 none 再回 egress，前后各查一次接口列表。测试自建并删除 docker 网络。
func TestRealDockerSandbox(t *testing.T) {
	if os.Getenv("ORYN_TEST_DOCKER") != "1" {
		t.Skip("未设置 ORYN_TEST_DOCKER=1")
	}
	const image = "oryn-orb:dev"
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skip("本机没有 oryn-orb:dev 镜像")
	}
	network := "oryn-sbx-test-" + strings.ToLower(ids.New("n")[2:])
	if out, err := exec.Command("docker", "network", "create", "-o", "com.docker.network.bridge.enable_icc=false", network).CombinedOutput(); err != nil {
		t.Fatalf("建测试网络: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })

	handler, links := serveSandboxLinks(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	sockDir := filepath.Join(t.TempDir(), "sbx")
	d := NewDocker(config.Docker{
		Image: image, DefaultType: "small", Types: map[string]config.DockerType{"small": {CPUs: 1, Memory: "1g"}},
		Sandbox: config.DockerSandbox{Enabled: true, Network: network, SocketDir: sockDir},
	})
	t.Cleanup(d.relays.closeAll)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, d.Preflight(ctx))

	run := func(j Job) {
		t.Helper()
		out := newTailBuffer(1 << 16)
		require.NoError(t, execErr(d, ctx, j, out), out.String())
	}
	up := func(network apigen.SandboxNetwork) (string, sandboxLink) {
		t.Helper()
		orbID := ids.Orb()
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", orbID).Run() })
		run(Job{Op: "spawn", OrbID: orbID, Spawn: &apigen.JobSpawnParams{
			Type: "small", Env: apigen.JobSpawnEnv{OrynURL: srv.URL, OrynOrbID: orbID, OrynOrbToken: "oot_" + orbID},
			Sandbox: &apigen.JobSpawnSandbox{Network: network},
		}})
		l := waitSandboxLink(t, links, 2*time.Minute)
		assert.Equal(t, "oot_"+orbID, l.token)
		return orbID, l
	}
	ifaces := func(l sandboxLink) []string {
		t.Helper()
		out, code := sandboxExec(t, ctx, l.sess, "cat /proc/net/dev")
		require.Equal(t, 0, code, out)
		return netInterfaces(t, out)
	}

	none, l := up(apigen.SandboxNetworkNone)
	assert.Equal(t, []string{"lo"}, ifaces(l))
	run(Job{Op: "delete", OrbID: none})
	assert.NoDirExists(t, filepath.Join(sockDir, none))

	egress, l2 := up(apigen.SandboxNetworkEgress)
	assert.Contains(t, ifaces(l2), "eth0")
	run(Job{Op: "set_network", OrbID: egress, Network: "none"})
	assert.Equal(t, []string{"lo"}, ifaces(l2))
	run(Job{Op: "set_network", OrbID: egress, Network: "egress"})
	assert.Contains(t, ifaces(l2), "eth0")
	run(Job{Op: "delete", OrbID: egress})
}

// TestRealFirecrackerSandbox 在真机上验证 vsock 中继（计划 2.7 的闸门：vsock 能承载 TLS + WebSocket + yamux）：
// none 沙箱经 vsock 中继连上 link、hello 带 sandbox、/proc/net/dev 只有 lo；egress 沙箱里 curl 得 200，
// set_network none 后失败，set_network egress 后又得 200。前提见 newFCReal（另需含精简模式 orbd 的根盘）。
func TestRealFirecrackerSandbox(t *testing.T) {
	handler, links := serveSandboxLinks(t)
	r := newFCReal(t, "1g", handler)
	d := r.d
	d.Cfg.Sandbox.Enabled = true
	t.Cleanup(d.relays.closeAll)

	up := func(network apigen.SandboxNetwork) (string, string, sandboxLink) {
		t.Helper()
		orbID := ids.Orb()
		t.Cleanup(func() { _, _ = d.Exec(context.Background(), Job{Op: "delete", OrbID: orbID}, io.Discard) })
		token := "oot_fc_sbx_" + orbID
		j := r.spawnJob(orbID, token)
		j.Spawn.Sandbox = &apigen.JobSpawnSandbox{Network: network}
		r.run(t, j, token)
		l := waitSandboxLink(t, links, 2*time.Minute)
		assert.Equal(t, token, l.token)
		return orbID, token, l
	}

	none, token, l := up(apigen.SandboxNetworkNone)
	out, code := sandboxExec(t, r.ctx, l.sess, "cat /proc/net/dev")
	require.Equal(t, 0, code, out)
	assert.Equal(t, []string{"lo"}, netInterfaces(t, out))
	r.run(t, Job{Op: "delete", OrbID: none}, token)

	egress, token, l2 := up(apigen.SandboxNetworkEgress)
	const curl = "curl -sS -m 10 -o /dev/null -w '%{http_code}' https://example.com"
	out, code = sandboxExec(t, r.ctx, l2.sess, curl)
	require.Equal(t, 0, code, out)
	assert.Equal(t, "200", strings.TrimSpace(out))
	r.run(t, Job{Op: "set_network", OrbID: egress, Network: "none"}, token)
	out, code = sandboxExec(t, r.ctx, l2.sess, curl)
	assert.NotEqual(t, 0, code, out)
	r.run(t, Job{Op: "set_network", OrbID: egress, Network: "egress"}, token)
	out, code = sandboxExec(t, r.ctx, l2.sess, curl)
	require.Equal(t, 0, code, out)
	assert.Equal(t, "200", strings.TrimSpace(out))
	r.run(t, Job{Op: "delete", OrbID: egress}, token)
}
