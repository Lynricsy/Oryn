package providerrt

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
)

// TestDockerRealSmoke 在 ORYN_TEST_DOCKER=1 且本机有 oryn-orb:dev 镜像时，对真实 docker 跑一遍驱动。
func TestDockerRealSmoke(t *testing.T) {
	if os.Getenv("ORYN_TEST_DOCKER") != "1" {
		t.Skip("未设置 ORYN_TEST_DOCKER=1")
	}
	if err := exec.Command("docker", "image", "inspect", "oryn-orb:dev").Run(); err != nil {
		t.Skip("本机没有 oryn-orb:dev 镜像")
	}
	// 当前镜像里的 orbd 可能还是会立即退出的占位版本：派生一个以常驻脚本替换 orbd 的测试镜像，
	// 只验证驱动的 docker 命令（HUP 后脚本记录到 /tmp/hup）。
	const image = "oryn-orb:driver-smoke"
	build := exec.Command("docker", "build", "-q", "-t", image, "-")
	build.Stdin = strings.NewReader("FROM oryn-orb:dev\nUSER root\n" +
		"RUN printf '#!/bin/sh\\ntrap \"echo hup >> /tmp/hup\" HUP\\ntrap \"exit 0\" TERM\\nwhile :; do sleep 1 & wait $!; done\\n' > /usr/local/bin/orbd && chmod 755 /usr/local/bin/orbd\n" +
		"USER user\n")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建测试镜像失败: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", image).Run() })
	d := NewDocker(config.Docker{Image: image, DefaultType: "small", Types: map[string]config.DockerType{"small": {CPUs: 1, Memory: "1g"}}})
	orbID := ids.Orb()
	snap := ids.Snapshot()
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", orbID).Run()
		_ = exec.Command("docker", "rmi", snap).Run()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	status := func() string {
		out, err := exec.Command("docker", "container", "inspect", "--format", "{{.State.Status}}", orbID).Output()
		if err != nil {
			return "missing"
		}
		return strings.TrimSpace(string(out))
	}
	run := func(j Job) {
		t.Helper()
		out := newTailBuffer(1 << 16)
		require.NoError(t, d.Exec(ctx, j, out), out.String())
		t.Logf("%s %s → %s\n%s", j.Op, j.PauseMode, status(), out.String())
	}
	spawn := Job{Op: "spawn", OrbID: orbID, Spawn: &apigen.JobSpawnParams{Type: "small", Env: apigen.JobSpawnEnv{OrynURL: "http://127.0.0.1:9", OrynOrbID: orbID, OrynOrbToken: "oot_smoke"}}}

	run(spawn)
	assert.Equal(t, "running", status())
	out, err := exec.Command("docker", "container", "inspect", "--format", "{{.Config.Hostname}} {{index .Config.Labels \"oryn-orb\"}} {{.HostConfig.Init}}", orbID).Output()
	require.NoError(t, err)
	assert.Equal(t, orbID+" 1 true", strings.TrimSpace(string(out)))
	envOut, err := exec.Command("docker", "container", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", orbID).Output()
	require.NoError(t, err)
	assert.Contains(t, string(envOut), "ORYN_ORB_TOKEN=oot_smoke")

	run(Job{Op: "pause", OrbID: orbID, PauseMode: "freeze"})
	assert.Equal(t, "paused", status())
	run(spawn) // unpause + pkill -HUP
	assert.Equal(t, "running", status())
	require.Eventually(t, func() bool {
		out, err := exec.Command("docker", "exec", orbID, "cat", "/tmp/hup").Output()
		return err == nil && strings.Contains(string(out), "hup")
	}, 10*time.Second, 200*time.Millisecond, "orbd 收到 SIGHUP")
	tokenOf := func(id string) string {
		out, err := exec.Command("docker", "exec", id, "sh", "-c", `stat -c '%U %a' "$HOME/.oryn/orb-token" && cat "$HOME/.oryn/orb-token"`).Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	assert.Equal(t, "user 600\noot_smoke", tokenOf(orbID), "解冻时写入令牌文件")
	capOut, err := exec.Command("docker", "container", "inspect", "--format", "{{.HostConfig.CapDrop}}", orbID).Output()
	require.NoError(t, err)
	assert.Contains(t, string(capOut), "NET_RAW")
	assert.Contains(t, string(capOut), "NET_ADMIN")
	// seccomp 配置放行 user namespace（Chromium 沙箱所需）；默认配置下 unshare -U 会 EPERM。
	nsOut, err := exec.Command("docker", "exec", orbID, "unshare", "-U", "true").CombinedOutput()
	assert.NoError(t, err, "unshare -U inside the orb: %s", nsOut)
	run(Job{Op: "snapshot", OrbID: orbID, SnapshotID: snap})
	require.NoError(t, exec.Command("docker", "image", "inspect", snap).Run())

	// 从快照派生：镜像里带着源 orb 的令牌文件，必须在启动前换成克隆自己的令牌。
	cloneID := ids.Orb()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", cloneID).Run() })
	run(Job{Op: "spawn", OrbID: cloneID, Spawn: &apigen.JobSpawnParams{Type: "small", SnapshotID: &snap,
		Env: apigen.JobSpawnEnv{OrynURL: "http://127.0.0.1:9", OrynOrbID: cloneID, OrynOrbToken: "oot_clone"}}})
	assert.Equal(t, "user 600\noot_clone", tokenOf(cloneID))
	run(Job{Op: "delete", OrbID: cloneID})
	run(Job{Op: "pause", OrbID: orbID, PauseMode: "freeze"})
	run(Job{Op: "pause", OrbID: orbID, PauseMode: "stop"}) // paused → unpause → stop
	assert.Equal(t, "exited", status())
	run(spawn) // start
	assert.Equal(t, "running", status())
	run(Job{Op: "delete", OrbID: orbID})
	assert.Equal(t, "missing", status())
	run(Job{Op: "delete", OrbID: orbID}) // 幂等
	run(Job{Op: "delete_snapshot", SnapshotID: snap})
	assert.Error(t, exec.Command("docker", "image", "inspect", snap).Run())
	run(Job{Op: "delete_snapshot", SnapshotID: snap}) // 幂等
}
