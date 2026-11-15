package providerrt

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/fcguest"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbtls"
)

// fcRealTLSPort 是测试 TLS 服务监听的端口（须在 oryn-fc-net.sh 的 ORYN_FC_LINK_PORTS 里，默认 7643）。
const fcRealTLSPort = "7643"

// fcRealUIDBase 是真机测试的槽位 uid 起点：远离常见的 subuid / subgid 分配（useradd 从 100000 起每段 65536），
// 预检的 uid 重叠检查才不会因宿主上已有的 subuid 失败。
const fcRealUIDBase = 1_500_000_000

// fcReal 是真机测试的公共环境：驱动（状态目录在产物旁的临时目录）、宿主侧 TLS 服务、令牌与 CA。
type fcReal struct {
	d      *FirecrackerDriver
	ctx    context.Context
	orbURL string
	caPEM  string
}

// newFCReal 准备真机测试（条件不满足则 Skip）：ORYN_TEST_FIRECRACKER=1、root、产物齐全（ORYN_TEST_FC_ARTIFACTS，
// 默认 <仓库>/build/fc，内含 firecracker、jailer、vmlinux、rootfs.ext4，见 make fc-artifacts）。宿主网络须已由
// deploy/firecracker/oryn-fc-net.sh up 建好（默认配置，放行 7643），否则预检失败、测试报错。
// 宿主上不能已有服务占用 172.31.255.1:7643（测试自己在那里起 TLS 服务，请求交给 handler）。
func newFCReal(t *testing.T, memory string, handler http.Handler) *fcReal {
	t.Helper()
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
	cfg.UIDBase = fcRealUIDBase
	cfg.Types = map[string]config.FirecrackerType{"small": {CPUs: 2, Memory: memory}}
	cfg.DefaultType = "small"
	cfg.Limits = config.FirecrackerLimits{MaxVMs: 1, MaxMemoryMB: 8192}
	hostAddr := cfg.Network.HostAddr
	orbURL := "https://" + net.JoinHostPort(hostAddr, fcRealTLSPort)
	d := NewFirecracker(cfg, orbURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	for _, c := range d.PreflightChecks(ctx) {
		if c.Name == "以 root 运行" || c.Err == nil {
			continue
		}
		t.Fatalf("预检 %s 失败: %v", c.Name, c.Err)
	}

	// 宿主侧 TLS 服务：证书由 orbtls 签发（SAN = host_addr）。
	tlsCfg, caPEM, err := orbtls.Load(t.TempDir(), []net.IP{net.ParseIP(hostAddr)}, nil)
	require.NoError(t, err)
	ln, err := tls.Listen("tcp", net.JoinHostPort(hostAddr, fcRealTLSPort), tlsCfg)
	require.NoError(t, err, "监听 %s:%s（宿主上是否已有 orynd 在用这个端口？）", hostAddr, fcRealTLSPort)
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &fcReal{d: d, ctx: ctx, orbURL: orbURL, caPEM: string(caPEM)}
}

func (r *fcReal) spawnJob(orbID, token string) Job {
	return Job{Op: "spawn", OrbID: orbID, Spawn: &apigen.JobSpawnParams{
		Type: "small", OrbCaPem: &r.caPEM,
		Env: apigen.JobSpawnEnv{OrynURL: r.orbURL, OrynOrbID: orbID, OrynOrbToken: token},
	}}
}

// run 执行一个任务并断言成功；令牌不进输出。
func (r *fcReal) run(t *testing.T, j Job, token string) *apigen.JobResult {
	t.Helper()
	var out bytes.Buffer
	start := time.Now()
	res, err := r.d.Exec(r.ctx, j, &out)
	require.NoError(t, err, out.String())
	t.Logf("%s %s ok（%s）\n%s", j.Op, j.PauseMode, time.Since(start).Round(time.Millisecond), out.String())
	assert.NotContains(t, out.String(), token)
	return res
}

func (r *fcReal) state(t *testing.T, orbID string) string {
	t.Helper()
	s, err := r.d.api.State(r.ctx, orbID)
	require.NoError(t, err)
	return s
}

// TestFirecrackerReal 在真机上跑一遍 Firecracker 驱动（计划 8 的验证）：
// spawn → guest orbd 经 TLS 拨到宿主 → ping → freeze → spawn（恢复）→ snapshot → stop → spawn → delete。
// 前提见 newFCReal。
func TestFirecrackerReal(t *testing.T) {
	type seen struct {
		path string
		tls  bool
	}
	reqs := make(chan seen, 64)
	// 只记录请求，对 link / whoami 回 401。
	r := newFCReal(t, "2g", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case reqs <- seen{r.URL.Path, r.TLS != nil}:
		default:
		}
		http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
	}))
	d := r.d
	orbID := ids.Orb()
	snap := ids.Snapshot()
	t.Cleanup(func() {
		var out bytes.Buffer
		_, _ = d.Exec(context.Background(), Job{Op: "delete", OrbID: orbID}, &out)
		_, _ = d.Exec(context.Background(), Job{Op: "delete_snapshot", SnapshotID: snap}, &out)
	})
	const token = "oot_fc_real_test_token"
	spawn := r.spawnJob(orbID, token)

	// spawn：guest 起来后 orbd 用下发的 CA 经 TLS 拨 /orb/v1/link（网络放行、CA 下发、guest 对时一并验证）。
	r.run(t, spawn, token)
	require.True(t, d.host.Alive(orbID))
	assert.Equal(t, fcStateRunning, r.state(t, orbID))
	deadline := time.After(2 * time.Minute)
	for linked := false; !linked; {
		select {
		case s := <-reqs:
			t.Logf("TLS 服务收到 %s（tls=%v）", s.path, s.tls)
			if s.path == "/orb/v1/link" {
				assert.True(t, s.tls)
				linked = true
			}
		case <-deadline:
			t.Fatal("2 分钟内没收到 guest orbd 的 /orb/v1/link 请求")
		}
	}
	require.NoError(t, d.guest.Do(r.ctx, orbID, fcguest.Request{Op: fcguest.OpPing}))

	// freeze → Paused；spawn 恢复并下发令牌。
	r.run(t, Job{Op: "pause", OrbID: orbID, PauseMode: "freeze"}, token)
	assert.Equal(t, fcStatePaused, r.state(t, orbID))
	r.run(t, spawn, token)
	assert.Equal(t, fcStateRunning, r.state(t, orbID))

	// snapshot：运行中 fsfreeze → 暂停 → 复制 → 恢复。
	res := r.run(t, Job{Op: "snapshot", OrbID: orbID, SnapshotID: snap}, token)
	require.NotNil(t, res)
	require.NotNil(t, res.SnapshotBytes)
	assert.Positive(t, *res.SnapshotBytes)
	assert.FileExists(t, d.paths.snapDisk(snap))
	assert.NoFileExists(t, d.paths.vmThawPending(orbID))
	assert.Equal(t, fcStateRunning, r.state(t, orbID))
	// 安全审查 S1：令牌只在 guest 的 tmpfs 里，快照盘（派生 orb 的 root 能读裸块设备）里不能有它。
	// 不直接查 "oot_"：ext4 上的任意文本（如 "root_"）都会误中；查本次令牌与真实令牌的格式。
	hit, err := sparseFind(d.paths.snapDisk(snap), regexp.MustCompile(`oot_fc_real_test_token|oot_[A-Za-z0-9_-]{43}`))
	require.NoError(t, err)
	assert.Empty(t, hit, "快照盘里有 orb 令牌")

	// stop：进程消失、磁盘在、tap 拆掉。
	vm, ok, err := d.paths.loadVM(orbID)
	require.NoError(t, err)
	require.True(t, ok)
	r.run(t, Job{Op: "pause", OrbID: orbID, PauseMode: "stop"}, token)
	assert.False(t, d.host.Alive(orbID))
	assert.FileExists(t, d.paths.vmDisk(orbID))
	_, err = net.InterfaceByName(vm.Tap)
	assert.Error(t, err, "停机后 tap 应已拆掉")

	// spawn（已停机）→ 重新启动，磁盘原样。
	r.run(t, spawn, token)
	assert.Equal(t, fcStateRunning, r.state(t, orbID))
	require.NoError(t, d.guest.Do(r.ctx, orbID, fcguest.Request{Op: fcguest.OpPing}))

	// delete：目录、tap、cgroup 都清掉。
	r.run(t, Job{Op: "delete", OrbID: orbID}, token)
	assert.NoDirExists(t, d.paths.vmDir(orbID))
	assert.NoDirExists(t, d.paths.jailDir(orbID))
	assert.NoDirExists(t, filepath.Join("/sys/fs/cgroup", fcCgroupParent, orbID))
	_, err = net.InterfaceByName(vm.Tap)
	assert.Error(t, err, "删除后 tap 应已拆掉")
}

// TestFirecrackerRealConsoleCap 验证串口日志的总量上限与 provider 进程无关（安全审查 S3）：
// 不运行 Housekeep，guest 里的 root 持续写 /dev/ttyS0 远超 tmpfs 容量 → console.log 实际占用不超过容量，
// firecracker 丢掉写不下的字节、VM 仍在运行并应答。为了几秒内写满，测试把串口限速放宽、tmpfs 缩到 1 MiB。
// guest 里的命令经一个最小的 link 服务端下发（setup.run 的 setup 脚本，sudo 写串口）。前提见 newFCReal。
func TestFirecrackerRealConsoleCap(t *testing.T) {
	const capBytes = 1 << 20
	saved := fcSerialRateLimit
	fcSerialRateLimit = map[string]any{"size": 64 << 20, "one_time_burst": 64 << 20, "refill_time": 1000}
	t.Cleanup(func() { fcSerialRateLimit = saved })

	ctls := make(chan *link.Ctl, 1)
	r := newFCReal(t, "1g", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/orb/v1/link" {
			http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		sess, err := link.Accept(w, req, func(*http.Request, string) error { return nil })
		if err != nil {
			return
		}
		defer sess.Close()
		hctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
		first, err := sess.AcceptStream(hctx)
		cancel()
		if err != nil || first.Header.T != link.StreamCtl {
			return
		}
		hello := make(chan struct{})
		var once sync.Once
		ctl := link.NewCtl(first, link.CtlHandlers{
			OnNotify: func(_ context.Context, method string, _ json.RawMessage) {
				if method == "hello" {
					once.Do(func() { close(hello) })
				}
			},
		})
		select {
		case <-hello:
		case <-ctl.Done():
			return
		}
		select {
		case ctls <- ctl:
		default:
		}
		<-ctl.Done()
	}))
	r.d.host.(*osHost).consoleFSBytes = capBytes
	d := r.d
	orbID := ids.Orb()
	t.Cleanup(func() {
		var out bytes.Buffer
		_, _ = d.Exec(context.Background(), Job{Op: "delete", OrbID: orbID}, &out)
	})
	const token = "oot_fc_real_console_token"
	r.run(t, r.spawnJob(orbID, token), token)

	// console 目录是 tmpfs。
	var sfs unix.Statfs_t
	require.NoError(t, unix.Statfs(filepath.Dir(d.consolePath(orbID)), &sfs))
	assert.Equal(t, int64(unix.TMPFS_MAGIC), int64(sfs.Type))

	var ctl *link.Ctl
	select {
	case ctl = <-ctls:
	case <-time.After(2 * time.Minute):
		t.Fatal("2 分钟内 guest orbd 没有建立 link 并发 hello")
	}
	// 4 倍容量的串口输出（逐字节 PIO，限速已放宽）。
	script := "sudo sh -c 'head -c 4194304 /dev/zero | tr \"\\000\" x > /dev/ttyS0'"
	var res apigen.LinkCtlSetupRunResult
	cctx, cancel := context.WithTimeout(r.ctx, 5*time.Minute)
	defer cancel()
	require.NoError(t, ctl.Call(cctx, "setup.run", apigen.LinkCtlSetupRunParams{
		Env: map[string]string{}, GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{},
		Scripts: apigen.LinkCtlSetupScripts{Setup: &script},
	}, &res))
	require.True(t, res.Ok, "setup 脚本失败")

	n, err := diskUsage(d.consolePath(orbID))
	require.NoError(t, err)
	st, err := os.Stat(d.consolePath(orbID))
	require.NoError(t, err)
	t.Logf("console.log 大小 %d，占用 %d", st.Size(), n)
	assert.LessOrEqual(t, n, int64(capBytes))
	assert.Greater(t, st.Size(), int64(capBytes)*9/10, "guest 应已把 tmpfs 写满")

	// 写满之后 VM 照常运行：进程在、API 报 Running、guest 应答 ping 与新的 spawn。
	assert.True(t, d.host.Alive(orbID))
	assert.Equal(t, fcStateRunning, r.state(t, orbID))
	require.NoError(t, d.guest.Do(r.ctx, orbID, fcguest.Request{Op: fcguest.OpPing}))
	r.run(t, r.spawnJob(orbID, token), token)

	// delete 卸载 tmpfs 并删掉 jail。
	r.run(t, Job{Op: "delete", OrbID: orbID}, token)
	assert.NoDirExists(t, d.paths.jailDir(orbID))
}

// sparseFind 在稀疏文件的数据段里找 re 的第一个匹配（跳过空洞；分块读，块间重叠 256 字节以免漏掉跨块的匹配）。
func sparseFind(path string, re *regexp.Regexp) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fd := int(f.Fd())
	const chunk, overlap = 8 << 20, 256
	buf := make([]byte, chunk+overlap)
	for off := int64(0); ; {
		data, err := unix.Seek(fd, off, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		hole, err := unix.Seek(fd, data, unix.SEEK_HOLE)
		if err != nil {
			return "", err
		}
		for pos := data; pos < hole; pos += chunk {
			n, err := f.ReadAt(buf[:min(int64(len(buf)), hole-pos)], pos)
			if err != nil && !errors.Is(err, io.EOF) {
				return "", err
			}
			if m := re.Find(buf[:n]); m != nil {
				return string(m), nil
			}
		}
		off = hole
	}
}
