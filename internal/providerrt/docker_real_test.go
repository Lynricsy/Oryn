package providerrt

import (
	"context"
	"os"
	"os/exec"
	"slices"
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
		require.NoError(t, execErr(d, ctx, j, out), out.String())
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
	// S-P2-3：spawn 经 -e 下发的变量在快照镜像里全部置空（令牌不随镜像外流）。
	assert.Equal(t, map[string]string{"ORYN_URL": "", "ORYN_ORB_ID": "", "ORYN_ORB_TOKEN": "", "ORYN_ORB_CA_PEM": "", "ORYN_NESTED_DOCKER": ""}, imageOrynEnv(t, snap))

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

// imageOrynEnv 返回镜像配置里 snapshotClearEnv 各变量的值（缺失的不出现在结果里）。
func imageOrynEnv(t *testing.T, image string) map[string]string {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", image).Output()
	require.NoError(t, err)
	got := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, _ := strings.Cut(kv, "=")
		if slices.Contains(snapshotClearEnv, k) {
			got[k] = v
		}
	}
	return got
}

// TestDockerRealSysboxSnapshot（X22）在 ORYN_TEST_DOCKER=1、本机有 oryn-orb:dev 且登记了 sysbox-runc 时运行：
// sysbox 容器运行中 commit 拍不到上层（Docker 处在 naive diff 模式），驱动须停机后再 commit 并重新启动。
// 断言快照里有 uid 1000 写进 /home/user 的内容、敏感变量已置空、源容器重新运行、从快照起的克隆能读到内容。
// 只用临时容器与临时镜像，结束时全部删除。
func TestDockerRealSysboxSnapshot(t *testing.T) {
	if os.Getenv("ORYN_TEST_DOCKER") != "1" {
		t.Skip("未设置 ORYN_TEST_DOCKER=1")
	}
	if err := exec.Command("docker", "image", "inspect", "oryn-orb:dev").Run(); err != nil {
		t.Skip("本机没有 oryn-orb:dev 镜像")
	}
	rts, err := exec.Command("docker", "info", "--format", "{{json .Runtimes}}").Output()
	if err != nil || !strings.Contains(string(rts), `"`+config.SysboxRuntime+`"`) {
		t.Skip("本机 Docker 没有登记 sysbox-runc")
	}
	// 以常驻脚本替换 orbd（不连控制面），记录每次启动，便于确认快照后容器被重新启动。
	image := "oryn-orb:driver-sysbox-smoke-" + strings.ToLower(ids.Snapshot()[len("oryn-snap-"):])
	build := exec.Command("docker", "build", "-q", "-t", image, "-")
	build.Stdin = strings.NewReader("FROM oryn-orb:dev\nUSER root\n" +
		"RUN printf '#!/bin/sh\\necho start >> /tmp/starts\\ntrap \"exit 0\" TERM\\nwhile :; do sleep 1 & wait $!; done\\n' > /usr/local/bin/orbd && chmod 755 /usr/local/bin/orbd\n" +
		"USER user\n")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建测试镜像失败: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", image).Run() })
	d := NewDocker(config.Docker{Image: image, DefaultType: "dind", PidsLimit: 512, Types: map[string]config.DockerType{
		"dind": {CPUs: 1, Memory: "1g", Runtime: config.SysboxRuntime, NestedDocker: true},
	}})
	orbID, cloneID := ids.Orb(), ids.Orb()
	snap, control := ids.Snapshot(), ids.Snapshot()
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", orbID, cloneID).Run()
		_ = exec.Command("docker", "rmi", snap, control).Run()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	inspect := func(id, format string) string {
		out, err := exec.Command("docker", "container", "inspect", "--format", format, id).Output()
		if err != nil {
			return "missing"
		}
		return strings.TrimSpace(string(out))
	}
	run := func(j Job) *apigen.JobResult {
		t.Helper()
		out := newTailBuffer(1 << 16)
		res, err := d.Exec(ctx, j, out)
		require.NoError(t, err, out.String())
		t.Logf("%s → %s\n%s", j.Op, inspect(j.OrbID, "{{.State.Status}}"), out.String())
		return res
	}
	pem := "-----BEGIN CERTIFICATE-----\nMIIBfake\n-----END CERTIFICATE-----"
	spawn := func(id string, snapID *string) Job {
		return Job{Op: "spawn", OrbID: id, Spawn: &apigen.JobSpawnParams{Type: "dind", SnapshotID: snapID, OrbCaPem: &pem,
			Env: apigen.JobSpawnEnv{OrynURL: "https://127.0.0.1:9", OrynOrbID: id, OrynOrbToken: "oot_sysbox"}}}
	}
	execUser := func(id string, script string) (string, error) {
		out, err := exec.Command("docker", "exec", "-u", "1000", id, "sh", "-c", script).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	run(spawn(orbID, nil))
	require.Equal(t, "running", inspect(orbID, "{{.State.Status}}"))
	assert.Equal(t, config.SysboxRuntime+" 512", inspect(orbID, "{{.HostConfig.Runtime}} {{.HostConfig.PidsLimit}}"))
	assert.Contains(t, inspect(orbID, "{{range .Config.Env}}{{println .}}{{end}}"), "ORYN_ORB_CA_PEM="+pem)
	out, err := execUser(orbID, `echo x22-content > /home/user/x22-marker && mkdir -p /home/user/.cache/x22 && head -c 65536 /dev/urandom > /home/user/.cache/x22/blob && sync && id -u`)
	require.NoError(t, err, out)
	require.Equal(t, "1000", out)

	// 对照：直接 commit 运行中的 sysbox 容器（修复前的做法）。只记录结果，不作断言（取决于 sysbox / Docker 版本）。
	if b, err := exec.Command("docker", "commit", orbID, control).CombinedOutput(); err == nil {
		got, _ := exec.Command("docker", "run", "--rm", "--entrypoint", "cat", control, "/home/user/x22-marker").CombinedOutput()
		t.Logf("对照：运行中直接 commit 的快照里 /home/user/x22-marker = %q", strings.TrimSpace(string(got)))
	} else {
		t.Logf("对照 commit 失败: %v %s", err, b)
	}

	run(Job{Op: "snapshot", OrbID: orbID, SnapshotID: snap})
	got, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", snap, "-c",
		"cat /home/user/x22-marker && stat -c '%u %s' /home/user/.cache/x22/blob").CombinedOutput()
	require.NoError(t, err, string(got))
	assert.Equal(t, "x22-content\n1000 65536", strings.TrimSpace(string(got)), "停机快照拍到 uid 1000 写进 /home/user 的内容")
	assert.Equal(t, map[string]string{"ORYN_URL": "", "ORYN_ORB_ID": "", "ORYN_ORB_TOKEN": "", "ORYN_ORB_CA_PEM": "", "ORYN_NESTED_DOCKER": ""}, imageOrynEnv(t, snap),
		"令牌、CA 等 spawn 变量在快照镜像里置空")

	// 源容器被重新启动（orbd 第二次启动），内容还在。
	assert.Equal(t, "running", inspect(orbID, "{{.State.Status}}"))
	require.Eventually(t, func() bool {
		out, err := execUser(orbID, "cat /tmp/starts")
		return err == nil && out == "start\nstart"
	}, 30*time.Second, 200*time.Millisecond, "快照后 orbd 重新启动")
	out, err = execUser(orbID, "cat /home/user/x22-marker")
	require.NoError(t, err, out)
	assert.Equal(t, "x22-content", out)

	// 从快照起克隆：snapshotUsed=true，内容在，环境变量是克隆自己的。
	res := run(spawn(cloneID, &snap))
	require.NotNil(t, res)
	require.NotNil(t, res.SnapshotUsed)
	assert.True(t, *res.SnapshotUsed)
	out, err = execUser(cloneID, `cat /home/user/x22-marker && printf '%s|%s|%s' "$ORYN_ORB_ID" "$ORYN_ORB_TOKEN" "$ORYN_NESTED_DOCKER"`)
	require.NoError(t, err, out)
	assert.Equal(t, "x22-content\n"+cloneID+"|oot_sysbox|1", out)

	// 暂停中的 sysbox 容器：拍完保持停机，内容完整。
	run(Job{Op: "pause", OrbID: cloneID, PauseMode: "freeze"})
	require.Equal(t, "paused", inspect(cloneID, "{{.State.Status}}"))
	snap2 := ids.Snapshot()
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", snap2).Run() })
	run(Job{Op: "snapshot", OrbID: cloneID, SnapshotID: snap2})
	assert.Equal(t, "exited", inspect(cloneID, "{{.State.Status}}"))
	got, err = exec.Command("docker", "run", "--rm", "--entrypoint", "cat", snap2, "/home/user/x22-marker").CombinedOutput()
	require.NoError(t, err, string(got))
	assert.Equal(t, "x22-content", strings.TrimSpace(string(got)))

	run(Job{Op: "delete", OrbID: cloneID})
	run(Job{Op: "delete", OrbID: orbID})
	run(Job{Op: "delete_snapshot", SnapshotID: snap2})
	run(Job{Op: "delete_snapshot", SnapshotID: snap})
}
