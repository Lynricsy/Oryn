package providerrt

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/fcguest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbtls"
)

// fcRealTLSPort 是测试 TLS 服务监听的端口（须在 oryn-fc-net.sh 的 ORYN_FC_LINK_PORTS 里，默认 7643）。
const fcRealTLSPort = "7643"

// TestFirecrackerReal 在真机上跑一遍 Firecracker 驱动（计划 8 的验证）：
// spawn → guest orbd 经 TLS 拨到宿主 → ping → freeze → spawn（恢复）→ snapshot → stop → spawn → delete。
//
// 条件（不满足则 Skip）：ORYN_TEST_FIRECRACKER=1、root、产物齐全（ORYN_TEST_FC_ARTIFACTS，默认 <仓库>/build/fc，
// 内含 firecracker、jailer、vmlinux、rootfs.ext4，见 make fc-artifacts）。宿主网络须已由
// deploy/firecracker/oryn-fc-net.sh up 建好（默认配置，放行 7643），否则预检失败、测试报错。
// 宿主上不能已有服务占用 172.31.255.1:7643（测试自己在那里起一个记录请求的 TLS 服务）。
func TestFirecrackerReal(t *testing.T) {
	if os.Getenv("ORYN_TEST_FIRECRACKER") != "1" {
		t.Skip("未设置 ORYN_TEST_FIRECRACKER=1")
	}
	if os.Geteuid() != 0 {
		t.Skip("需要 root")
	}
	art := os.Getenv("ORYN_TEST_FC_ARTIFACTS")
	if art == "" {
		wd, err := os.Getwd()
		require.NoError(t, err)
		art = filepath.Join(wd, "..", "..", "build", "fc")
	}
	art, err := filepath.Abs(art)
	require.NoError(t, err)
	for _, f := range []string{"firecracker", "jailer", "vmlinux", "rootfs.ext4"} {
		if _, err := os.Stat(filepath.Join(art, f)); err != nil {
			t.Skipf("缺少产物 %s（先 make fc-artifacts）", filepath.Join(art, f))
		}
	}

	// 状态目录与产物在同一文件系统（驱动硬链接内核与根盘）。
	state, err := os.MkdirTemp(art, "real-test-state-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	cfg := config.DefaultFirecracker()
	cfg.Bin, cfg.Jailer = filepath.Join(art, "firecracker"), filepath.Join(art, "jailer")
	cfg.Kernel, cfg.Rootfs = filepath.Join(art, "vmlinux"), filepath.Join(art, "rootfs.ext4")
	cfg.StateDir = state
	cfg.DiskGB = 8
	cfg.Types = map[string]config.FirecrackerType{"small": {CPUs: 2, Memory: "2g"}}
	cfg.DefaultType = "small"
	cfg.Limits = config.FirecrackerLimits{MaxVMs: 2, MaxMemoryMB: 8192}
	hostAddr := cfg.Network.HostAddr
	orbURL := "https://" + net.JoinHostPort(hostAddr, fcRealTLSPort)
	d := NewFirecracker(cfg, orbURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, c := range d.PreflightChecks(ctx) {
		if c.Name == "以 root 运行" || c.Err == nil {
			continue
		}
		t.Fatalf("预检 %s 失败: %v", c.Name, c.Err)
	}

	// 宿主侧 TLS 服务：证书由 orbtls 签发（SAN = host_addr），只记录请求，对 link / whoami 回 401。
	tlsCfg, caPEM, err := orbtls.Load(t.TempDir(), []net.IP{net.ParseIP(hostAddr)}, nil)
	require.NoError(t, err)
	ln, err := tls.Listen("tcp", net.JoinHostPort(hostAddr, fcRealTLSPort), tlsCfg)
	require.NoError(t, err, "监听 %s:%s（宿主上是否已有 orynd 在用这个端口？）", hostAddr, fcRealTLSPort)
	type seen struct {
		path string
		tls  bool
	}
	reqs := make(chan seen, 64)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case reqs <- seen{r.URL.Path, r.TLS != nil}:
		default:
		}
		http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	orbID := ids.Orb()
	snap := ids.Snapshot()
	t.Cleanup(func() {
		var out bytes.Buffer
		_, _ = d.Exec(context.Background(), Job{Op: "delete", OrbID: orbID}, &out)
		_, _ = d.Exec(context.Background(), Job{Op: "delete_snapshot", SnapshotID: snap}, &out)
	})
	ca := string(caPEM)
	spawn := Job{Op: "spawn", OrbID: orbID, Spawn: &apigen.JobSpawnParams{
		Type: "small", OrbCaPem: &ca,
		Env: apigen.JobSpawnEnv{OrynURL: orbURL, OrynOrbID: orbID, OrynOrbToken: "oot_fc_real_test_token"},
	}}
	run := func(j Job) *apigen.JobResult {
		t.Helper()
		var out bytes.Buffer
		start := time.Now()
		res, err := d.Exec(ctx, j, &out)
		require.NoError(t, err, out.String())
		t.Logf("%s %s ok（%s）\n%s", j.Op, j.PauseMode, time.Since(start).Round(time.Millisecond), out.String())
		assert.NotContains(t, out.String(), "oot_fc_real_test_token")
		return res
	}
	state1 := func() string {
		s, err := d.api.State(ctx, orbID)
		require.NoError(t, err)
		return s
	}

	// spawn：guest 起来后 orbd 用下发的 CA 经 TLS 拨 /orb/v1/link（网络放行、CA 下发、guest 对时一并验证）。
	run(spawn)
	require.True(t, d.host.Alive(orbID))
	assert.Equal(t, fcStateRunning, state1())
	deadline := time.After(2 * time.Minute)
	for linked := false; !linked; {
		select {
		case r := <-reqs:
			t.Logf("TLS 服务收到 %s（tls=%v）", r.path, r.tls)
			if r.path == "/orb/v1/link" {
				assert.True(t, r.tls)
				linked = true
			}
		case <-deadline:
			t.Fatal("2 分钟内没收到 guest orbd 的 /orb/v1/link 请求")
		}
	}
	require.NoError(t, d.guest.Do(ctx, orbID, fcguest.Request{Op: fcguest.OpPing}))

	// freeze → Paused；spawn 恢复并下发令牌。
	run(Job{Op: "pause", OrbID: orbID, PauseMode: "freeze"})
	assert.Equal(t, fcStatePaused, state1())
	run(spawn)
	assert.Equal(t, fcStateRunning, state1())

	// snapshot：运行中 fsfreeze → 暂停 → 复制 → 恢复。
	res := run(Job{Op: "snapshot", OrbID: orbID, SnapshotID: snap})
	require.NotNil(t, res)
	require.NotNil(t, res.SnapshotBytes)
	assert.Positive(t, *res.SnapshotBytes)
	assert.FileExists(t, d.paths.snapDisk(snap))
	assert.Equal(t, fcStateRunning, state1())

	// stop：进程消失、磁盘在、tap 拆掉。
	vm, ok, err := d.paths.loadVM(orbID)
	require.NoError(t, err)
	require.True(t, ok)
	run(Job{Op: "pause", OrbID: orbID, PauseMode: "stop"})
	assert.False(t, d.host.Alive(orbID))
	assert.FileExists(t, d.paths.vmDisk(orbID))
	_, err = net.InterfaceByName(vm.Tap)
	assert.Error(t, err, "停机后 tap 应已拆掉")

	// spawn（已停机）→ 重新启动，磁盘原样。
	run(spawn)
	assert.Equal(t, fcStateRunning, state1())
	require.NoError(t, d.guest.Do(ctx, orbID, fcguest.Request{Op: fcguest.OpPing}))

	// delete：目录、tap、cgroup 都清掉。
	run(Job{Op: "delete", OrbID: orbID})
	assert.NoDirExists(t, d.paths.vmDir(orbID))
	assert.NoDirExists(t, d.paths.jailDir(orbID))
	assert.NoDirExists(t, filepath.Join("/sys/fs/cgroup", fcCgroupParent, orbID))
	_, err = net.InterfaceByName(vm.Tap)
	assert.Error(t, err, "删除后 tap 应已拆掉")
}
