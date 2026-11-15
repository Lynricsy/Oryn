package providerrt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/fcguest"
)

// fakeFC 同时实现 fcHost / fcAPI / fcGuest：进程、tap、VM 状态都在内存里，磁盘是临时目录里的真文件。
type fakeFC struct {
	mu    sync.Mutex
	calls []string
	alive map[string]bool
	state map[string]string // Running | Paused
	taps  map[string]bool
	reqs  []fcguest.Request

	apiDown     map[string]bool // State 返回错误
	startFails  int             // 前 n 次 start 返回传输错误
	startRemote bool            // start 返回 RemoteError
	startNever  bool            // start 永远返回传输错误
	syncErr     bool
	shutdownErr bool
	shutdownEOF bool  // shutdown 应答前连接被关（guest 已在重启）
	stuck       bool  // shutdown 后进程不退出
	copyErr     error // CopySparse 失败
	launchErr   error
}

func newFakeFC() *fakeFC {
	return &fakeFC{alive: map[string]bool{}, state: map[string]string{}, taps: map[string]bool{}, apiDown: map[string]bool{}}
}

func (f *fakeFC) log(format string, a ...any) { f.calls = append(f.calls, fmt.Sprintf(format, a...)) }

// take 返回并清空调用记录。
func (f *fakeFC) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func (f *fakeFC) Launch(_ context.Context, vm vmMeta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("Launch %s", vm.OrbID)
	if f.launchErr != nil {
		return f.launchErr
	}
	f.alive[vm.OrbID] = true
	f.state[vm.OrbID] = "Not started"
	return nil
}

func (f *fakeFC) Alive(orbID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[orbID]
}

func (f *fakeFC) Kill(orbID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alive[orbID] {
		f.log("Kill %s alive", orbID)
	} else {
		f.log("Kill %s", orbID)
	}
	delete(f.alive, orbID)
	delete(f.state, orbID)
	return nil
}

func (f *fakeFC) TapUp(vm vmMeta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("TapUp %s", vm.Tap)
	f.taps[vm.Tap] = true
	return nil
}

func (f *fakeFC) TapDown(vm vmMeta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("TapDown %s", vm.Tap)
	delete(f.taps, vm.Tap)
	return nil
}

func (f *fakeFC) MakeDisk(path string, gb int, uid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("MakeDisk %s %d %d", filepath.Base(path), gb, uid)
	return os.WriteFile(path, []byte("empty"), 0o600)
}

func (f *fakeFC) CopySparse(src, dst string, uid int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("CopySparse %s %s %d", filepath.Base(src), filepath.Base(dst), uid)
	if f.copyErr != nil {
		return 0, f.copyErr
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	return int64(len(b)) * 1000, os.WriteFile(dst, b, 0o600)
}

func (f *fakeFC) Boot(_ context.Context, vm vmMeta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("Boot %s", vm.OrbID)
	f.state[vm.OrbID] = "Running"
	return nil
}

func (f *fakeFC) State(_ context.Context, orbID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.apiDown[orbID] {
		return "", errors.New("api.sock: connection refused")
	}
	return f.state[orbID], nil
}

func (f *fakeFC) SetState(_ context.Context, orbID, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("SetState %s %s", orbID, state)
	if state == "Resumed" {
		state = "Running"
	}
	f.state[orbID] = state
	return nil
}

func (f *fakeFC) CtrlAltDel(_ context.Context, orbID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("CtrlAltDel %s", orbID)
	if !f.stuck {
		delete(f.alive, orbID)
	}
	return nil
}

func (f *fakeFC) Do(_ context.Context, orbID string, req fcguest.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	switch req.Op {
	case fcguest.OpShutdown:
		f.log("guest %s %s %d", req.Op, orbID, req.GraceSeconds)
	default:
		f.log("guest %s %s", req.Op, orbID)
	}
	switch req.Op {
	case fcguest.OpStart:
		switch {
		case f.startRemote:
			return &fcguest.RemoteError{Op: req.Op, Msg: "boom"}
		case f.startNever:
			return errors.New("vsock: connection reset")
		case f.startFails > 0:
			f.startFails--
			return errors.New("vsock: connection reset")
		}
	case fcguest.OpSync:
		if f.syncErr {
			return errors.New("sync timeout")
		}
	case fcguest.OpShutdown:
		if f.shutdownEOF {
			delete(f.alive, orbID)
			return fmt.Errorf("fcguest shutdown: %w", io.EOF)
		}
		if f.shutdownErr {
			return errors.New("vsock: connection reset")
		}
		if !f.stuck {
			delete(f.alive, orbID)
		}
	}
	return nil
}

// lastReq 返回最后一个 op 请求。
func (f *fakeFC) lastReq(op string) (fcguest.Request, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].Op == op {
			return f.reqs[i], true
		}
	}
	return fcguest.Request{}, false
}

var fcTestNow = time.UnixMilli(1_760_000_000_123)

type fcTest struct {
	t    *testing.T
	d    *FirecrackerDriver
	f    *fakeFC
	root string
	out  *bytes.Buffer
}

func newFCTest(t *testing.T, mut ...func(*config.Firecracker)) *fcTest {
	t.Helper()
	root := t.TempDir()
	art := filepath.Join(root, "artifacts")
	require.NoError(t, os.MkdirAll(art, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(art, "rootfs.ext4"), []byte("rootfs-v1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(art, "vmlinux"), []byte("kernel-v1"), 0o644))
	cfg := config.DefaultFirecracker()
	cfg.Kernel = filepath.Join(art, "vmlinux")
	cfg.Rootfs = filepath.Join(art, "rootfs.ext4")
	cfg.StateDir = filepath.Join(root, "state")
	cfg.Types = map[string]config.FirecrackerType{"small": {CPUs: 2, Memory: "4g"}, "medium": {CPUs: 4, Memory: "8g"}}
	cfg.DefaultType = "small"
	cfg.Limits = config.FirecrackerLimits{MaxVMs: 8, MaxMemoryMB: 32768}
	for _, m := range mut {
		m(&cfg)
	}
	f := newFakeFC()
	d := NewFirecracker(cfg, "http://172.31.255.1:7643")
	d.host, d.api, d.guest = f, f, f
	d.now = func() time.Time { return fcTestNow }
	d.pollEvery = time.Millisecond
	d.shutdownWait = time.Second
	return &fcTest{t: t, d: d, f: f, root: root, out: &bytes.Buffer{}}
}

func fcSpawn(orbID string, mut ...func(*apigen.JobSpawnParams)) Job {
	p := &apigen.JobSpawnParams{
		Type: "small",
		Env:  apigen.JobSpawnEnv{OrynURL: "https://172.31.255.1:7643", OrynOrbID: orbID, OrynOrbToken: "oot_secret_" + orbID},
	}
	for _, m := range mut {
		m(p)
	}
	return Job{Op: "spawn", OrbID: orbID, Spawn: p}
}

func (c *fcTest) exec(j Job) (*apigen.JobResult, error) {
	c.t.Helper()
	return c.d.Exec(context.Background(), j, c.out)
}

func (c *fcTest) must(j Job) *apigen.JobResult {
	c.t.Helper()
	res, err := c.exec(j)
	require.NoError(c.t, err, c.out.String())
	return res
}

func (c *fcTest) meta(orbID string) vmMeta {
	c.t.Helper()
	m, ok, err := c.d.paths.loadVM(orbID)
	require.NoError(c.t, err)
	require.True(c.t, ok, "VM %s 不存在", orbID)
	return m
}

func (c *fcTest) slots() slotTable {
	c.t.Helper()
	t := slotTable{}
	_, err := readJSONFile(c.d.paths.slots(), &t)
	require.NoError(c.t, err)
	return t
}

func (c *fcTest) baseID() string {
	b, err := os.ReadFile(c.d.Cfg.Rootfs)
	require.NoError(c.t, err)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:16]
}

const orbA = "orb-01jabcdefghjkmnpqrstvwxyz0"

// 8.0 表第一行：spawn（不存在）= 分配槽位 → 空盘 → tap → jailer → Boot → vsock start；第二次（运行中）只发 token。
func TestFCSpawnNew(t *testing.T) {
	c := newFCTest(t)
	ca := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	res := c.must(fcSpawn(orbA, func(p *apigen.JobSpawnParams) { p.OrbCaPem = &ca }))
	assert.Nil(t, res)
	assert.Equal(t, []string{
		"MakeDisk disk.ext4.tmp 40 200000",
		"TapUp fc-npqrstvwxyz0",
		"Launch " + orbA,
		"Boot " + orbA,
		"guest start " + orbA,
	}, c.f.take())

	m := c.meta(orbA)
	assert.Equal(t, vmMeta{
		OrbID: orbA, Base: c.baseID(), Type: "small", CPUs: 2, MemMB: 4096, Slot: 0, UID: 200000,
		Tap: "fc-npqrstvwxyz0", HostIP: "172.31.0.1", GuestIP: "172.31.0.2", MAC: "06:00:ac:1f:00:02",
	}, m)
	disk, err := os.ReadFile(c.d.paths.vmDisk(orbA))
	require.NoError(t, err)
	assert.Equal(t, "empty", string(disk))
	assert.Equal(t, slotTable{orbA: 0}, c.slots())
	// 基础盘是 rootfs 的硬链接。
	bst, err := os.Stat(c.d.paths.base(m.Base))
	require.NoError(t, err)
	rst, err := os.Stat(c.d.Cfg.Rootfs)
	require.NoError(t, err)
	assert.True(t, os.SameFile(bst, rst))

	start, ok := c.f.lastReq(fcguest.OpStart)
	require.True(t, ok)
	assert.Equal(t, fcguest.Request{
		Op: fcguest.OpStart, URL: "https://172.31.255.1:7643", OrbID: orbA, Token: "oot_secret_" + orbA,
		DNS: []string{"1.1.1.1", "1.0.0.1"}, NowMs: fcTestNow.UnixMilli(), CAPEM: ca,
	}, start)
	// start 的 caPem 原样等于 spawn 参数里的 orbCaPem。
	assert.Equal(t, ca, start.CAPEM)

	// 幂等：再 spawn 一次只轮换令牌。
	assert.Nil(t, c.must(fcSpawn(orbA)))
	assert.Equal(t, []string{"guest token " + orbA}, c.f.take())
	tok, _ := c.f.lastReq(fcguest.OpToken)
	assert.Equal(t, fcguest.Request{Op: fcguest.OpToken, Token: "oot_secret_" + orbA, NowMs: fcTestNow.UnixMilli()}, tok)
	assert.Equal(t, m, c.meta(orbA))

	// 令牌与 CA 不进任务输出。
	assert.NotContains(t, c.out.String(), "oot_secret")
	assert.NotContains(t, c.out.String(), "BEGIN CERTIFICATE")
}

// 未给 orbCaPem 时 start 不带 caPem；未知类型退回 default_type。
func TestFCSpawnNoCAUnknownType(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA, func(p *apigen.JobSpawnParams) { p.Type = "nope" }))
	start, _ := c.f.lastReq(fcguest.OpStart)
	assert.Empty(t, start.CAPEM)
	assert.Equal(t, "small", c.meta(orbA).Type)
}

// 8.0 表第二行：spawn（已暂停）= PATCH Resumed → vsock token。
func TestFCSpawnPaused(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "freeze"})
	c.f.take()
	c.must(fcSpawn(orbA))
	assert.Equal(t, []string{"SetState " + orbA + " Resumed", "guest token " + orbA}, c.f.take())
	// 幂等：已恢复后再来只发 token。
	c.must(fcSpawn(orbA))
	assert.Equal(t, []string{"guest token " + orbA}, c.f.take())
}

// 8.0 表第三行：spawn（已停机）= 重新走启动一段，磁盘原样。
func TestFCSpawnStopped(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	require.NoError(t, os.WriteFile(c.d.paths.vmDisk(orbA), []byte("user data"), 0o600))
	c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "stop"})
	c.f.take()
	c.must(fcSpawn(orbA))
	assert.Equal(t, []string{"TapUp " + tapName(orbA), "Launch " + orbA, "Boot " + orbA, "guest start " + orbA}, c.f.take())
	disk, err := os.ReadFile(c.d.paths.vmDisk(orbA))
	require.NoError(t, err)
	assert.Equal(t, "user data", string(disk))
	assert.Equal(t, slotTable{orbA: 0}, c.slots())
}

// 进程在但 API 不可达：杀掉后重新启动。
func TestFCSpawnAPIUnreachable(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	c.f.take()
	c.f.apiDown[orbA] = true
	c.must(fcSpawn(orbA))
	c.f.apiDown[orbA] = false
	assert.Equal(t, []string{"Kill " + orbA + " alive", "TapUp " + tapName(orbA), "Launch " + orbA, "Boot " + orbA, "guest start " + orbA}, c.f.take())
}

// guest 启动期间 vsock 连不上：重试直到成功。
func TestFCStartRetry(t *testing.T) {
	c := newFCTest(t)
	c.f.startFails = 3
	c.must(fcSpawn(orbA))
	calls := c.f.take()
	assert.Equal(t, []string{"guest start " + orbA, "guest start " + orbA, "guest start " + orbA, "guest start " + orbA}, calls[len(calls)-4:])
	assert.True(t, c.f.Alive(orbA))
}

// guest 明确拒绝 start 不重试；超时也失败。两种情况都杀掉进程、拆 tap，磁盘与槽位保留，下次 spawn 从启动一段重来。
func TestFCStartFailureCleansUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeFC)
		want int // start 次数
	}{
		{"remote", func(f *fakeFC) { f.startRemote = true }, 1},
		{"timeout", func(f *fakeFC) { f.startNever = true }, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newFCTest(t)
			c.d.startTimeout = 30 * time.Millisecond
			tc.set(c.f)
			_, err := c.exec(fcSpawn(orbA))
			require.Error(t, err)
			calls := c.f.take()
			starts := 0
			for _, s := range calls {
				if s == "guest start "+orbA {
					starts++
				}
			}
			if tc.want > 0 {
				assert.Equal(t, tc.want, starts)
			} else {
				assert.Greater(t, starts, 1)
			}
			assert.Equal(t, []string{"Kill " + orbA + " alive", "TapDown " + tapName(orbA)}, calls[len(calls)-2:])
			assert.False(t, c.f.Alive(orbA))
			assert.Empty(t, c.f.taps)
			assert.Equal(t, slotTable{orbA: 0}, c.slots())
			assert.NotContains(t, c.out.String(), "oot_secret")
			assert.NotContains(t, err.Error(), "oot_secret")

			c.f.startRemote, c.f.startNever = false, false
			c.must(fcSpawn(orbA))
			assert.Equal(t, []string{"TapUp " + tapName(orbA), "Launch " + orbA, "Boot " + orbA, "guest start " + orbA}, c.f.take())
		})
	}
}

// 8.0 表 pause{freeze}：运行中 = vsock sync → PATCH Paused；已暂停 / 已停机 = 无操作。
func TestFCFreeze(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	c.f.take()
	freeze := Job{Op: "pause", OrbID: orbA, PauseMode: "freeze"}
	c.must(freeze)
	assert.Equal(t, []string{"guest sync " + orbA, "SetState " + orbA + " Paused"}, c.f.take())
	c.must(freeze)
	assert.Empty(t, c.f.take())

	// sync 失败只是尽力，照样暂停。
	c.must(fcSpawn(orbA))
	c.f.take()
	c.f.syncErr = true
	c.must(freeze)
	assert.Equal(t, []string{"guest sync " + orbA, "SetState " + orbA + " Paused"}, c.f.take())

	c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "stop"})
	c.f.take()
	c.must(freeze)
	assert.Empty(t, c.f.take())

	_, err := c.exec(Job{Op: "pause", OrbID: "orb-missing", PauseMode: "freeze"})
	assert.Error(t, err)
}

// 8.0 表 pause{stop}：运行中 = shutdown{30} → 等退出 → 清理 cgroup / jail → 拆 tap；磁盘与槽位保留。
func TestFCStop(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	c.f.take()
	stop := Job{Op: "pause", OrbID: orbA, PauseMode: "stop"}
	c.must(stop)
	assert.Equal(t, []string{"guest shutdown " + orbA + " 30", "Kill " + orbA, "TapDown " + tapName(orbA)}, c.f.take())
	assert.FileExists(t, c.d.paths.vmDisk(orbA))
	assert.Equal(t, slotTable{orbA: 0}, c.slots())
	// 幂等：已停机只确保 tap 拆掉。
	c.must(stop)
	assert.Equal(t, []string{"TapDown " + tapName(orbA)}, c.f.take())
}

// pause{stop} 遇到暂停的 VM 先恢复；关不掉时 cgroup.kill；vsock 不通时改发 Ctrl+Alt+Del。
func TestFCStopBranches(t *testing.T) {
	t.Run("paused", func(t *testing.T) {
		c := newFCTest(t)
		c.must(fcSpawn(orbA))
		c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "freeze"})
		c.f.take()
		c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "stop"})
		assert.Equal(t, []string{"SetState " + orbA + " Resumed", "guest shutdown " + orbA + " 30", "Kill " + orbA, "TapDown " + tapName(orbA)}, c.f.take())
	})
	t.Run("stuck", func(t *testing.T) {
		c := newFCTest(t)
		c.d.shutdownWait = 5 * time.Millisecond
		c.must(fcSpawn(orbA))
		c.f.take()
		c.f.stuck = true
		c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "stop"})
		assert.Equal(t, []string{"guest shutdown " + orbA + " 30", "Kill " + orbA + " alive", "TapDown " + tapName(orbA)}, c.f.take())
		assert.False(t, c.f.Alive(orbA))
	})
	t.Run("vsock down", func(t *testing.T) {
		c := newFCTest(t)
		c.must(fcSpawn(orbA))
		c.f.take()
		c.f.shutdownErr = true
		c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "stop"})
		assert.Equal(t, []string{"guest shutdown " + orbA + " 30", "CtrlAltDel " + orbA, "Kill " + orbA, "TapDown " + tapName(orbA)}, c.f.take())
	})
	t.Run("reply lost on reboot", func(t *testing.T) {
		c := newFCTest(t)
		c.must(fcSpawn(orbA))
		c.f.take()
		c.f.shutdownEOF = true
		c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "stop"})
		assert.Equal(t, []string{"guest shutdown " + orbA + " 30", "Kill " + orbA, "TapDown " + tapName(orbA)}, c.f.take())
		assert.Empty(t, c.out.String())
	})
}

const snapA = "oryn-snap-01jaaaaaaaaaaaaaaaaaaaaaaa"

// 8.0 表 snapshot：运行中 = fsfreeze → Paused → 复制 → Resumed → fsthaw；已暂停 / 已停机 = 直接复制；已存在 = 直接返回（幂等）。
func TestFCSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prep  func(c *fcTest)
		calls []string
	}{
		{"running", func(*fcTest) {}, []string{
			"guest fsfreeze " + orbA, "SetState " + orbA + " Paused", "CopySparse disk.ext4 " + snapA + ".ext4.tmp 0",
			"SetState " + orbA + " Resumed", "guest fsthaw " + orbA,
		}},
		{"paused", func(c *fcTest) { c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "freeze"}) }, []string{
			"CopySparse disk.ext4 " + snapA + ".ext4.tmp 0",
		}},
		{"stopped", func(c *fcTest) { c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "stop"}) }, []string{
			"CopySparse disk.ext4 " + snapA + ".ext4.tmp 0",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newFCTest(t)
			c.must(fcSpawn(orbA))
			require.NoError(t, os.WriteFile(c.d.paths.vmDisk(orbA), []byte("disk-data"), 0o600))
			tc.prep(c)
			c.f.take()
			job := Job{Op: "snapshot", OrbID: orbA, SnapshotID: snapA}
			res := c.must(job)
			require.NotNil(t, res)
			require.NotNil(t, res.SnapshotBytes)
			assert.Equal(t, int64(9000), *res.SnapshotBytes)
			assert.Equal(t, tc.calls, c.f.take())
			var sm snapMeta
			ok, err := readJSONFile(c.d.paths.snapMeta(snapA), &sm)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, snapMeta{Base: c.baseID(), Bytes: 9000}, sm)
			b, err := os.ReadFile(c.d.paths.snapDisk(snapA))
			require.NoError(t, err)
			assert.Equal(t, "disk-data", string(b))

			res2 := c.must(job)
			assert.Equal(t, res, res2)
			assert.Empty(t, c.f.take())
		})
	}
}

// 运行中拍快照时复制失败：照样恢复并解冻，不留半成品。
func TestFCSnapshotCopyFailureThaws(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	c.f.take()
	c.f.copyErr = errors.New("disk full")
	_, err := c.exec(Job{Op: "snapshot", OrbID: orbA, SnapshotID: snapA})
	require.ErrorContains(t, err, "disk full")
	assert.Equal(t, []string{
		"guest fsfreeze " + orbA, "SetState " + orbA + " Paused", "CopySparse disk.ext4 " + snapA + ".ext4.tmp 0",
		"SetState " + orbA + " Resumed", "guest fsthaw " + orbA,
	}, c.f.take())
	assert.NoFileExists(t, c.d.paths.snapDisk(snapA))
	assert.NoFileExists(t, c.d.paths.snapMeta(snapA))
	_, err = c.exec(Job{Op: "snapshot", OrbID: "orb-missing", SnapshotID: snapA})
	assert.Error(t, err)
}

const orbB = "orb-01jbbbbbbbbbbbbbbbbbbbbbbb"

// spawn 带快照的三种分支：快照在 → 复制并 snapshotUsed=true；快照缺失且可选 → 空盘、false；缺失且必需 → 报错且不留痕迹。
func TestFCSpawnSnapshotBranches(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	require.NoError(t, os.WriteFile(c.d.paths.vmDisk(orbA), []byte("from-a"), 0o600))
	c.must(Job{Op: "snapshot", OrbID: orbA, SnapshotID: snapA})
	c.f.take()

	withSnap := func(id string, optional bool) func(*apigen.JobSpawnParams) {
		return func(p *apigen.JobSpawnParams) { p.SnapshotID, p.SnapshotOptional = &id, &optional }
	}

	// 快照在：复制，snapshotUsed = true；已有 VM 再 spawn 仍为 true。
	res := c.must(fcSpawn(orbB, withSnap(snapA, false)))
	require.NotNil(t, res)
	assert.Equal(t, new(true), res.SnapshotUsed)
	calls := c.f.take()
	assert.Equal(t, "CopySparse "+snapA+".ext4 disk.ext4.tmp 200001", calls[0])
	b, err := os.ReadFile(c.d.paths.vmDisk(orbB))
	require.NoError(t, err)
	assert.Equal(t, "from-a", string(b))
	assert.Equal(t, snapA, c.meta(orbB).Snapshot)
	assert.Equal(t, c.meta(orbA).Base, c.meta(orbB).Base)
	assert.Equal(t, new(true), c.must(fcSpawn(orbB, withSnap(snapA, false))).SnapshotUsed)
	// 已有 VM 请求另一个快照：false。
	assert.Equal(t, new(false), c.must(fcSpawn(orbB, withSnap("oryn-snap-other", true))).SnapshotUsed)

	// 快照缺失且可选：空盘，false，输出里有说明。
	const orbC = "orb-01jcccccccccccccccccccccccc"
	res = c.must(fcSpawn(orbC, withSnap("oryn-snap-gone", true)))
	assert.Equal(t, new(false), res.SnapshotUsed)
	assert.Contains(t, c.out.String(), "快照 oryn-snap-gone 不存在，改用空盘")
	assert.Empty(t, c.meta(orbC).Snapshot)

	// 快照缺失且必需：报错，不建盘、不占槽位。
	const orbD = "orb-01jdddddddddddddddddddddddd"
	c.f.take()
	_, err = c.exec(fcSpawn(orbD, withSnap("oryn-snap-gone", false)))
	require.ErrorContains(t, err, "快照 oryn-snap-gone 不存在")
	assert.Empty(t, c.f.take())
	assert.NoDirExists(t, c.d.paths.vmDir(orbD))
	_, ok := c.slots()[orbD]
	assert.False(t, ok)

	// 快照元数据在但其基础盘已不在：按快照不存在处理。
	require.NoError(t, writeJSONFile(c.d.paths.snapMeta("oryn-snap-orphan"), snapMeta{Base: "0123456789abcdef", Bytes: 1}))
	require.NoError(t, os.WriteFile(c.d.paths.snapDisk("oryn-snap-orphan"), []byte("x"), 0o600))
	_, err = c.exec(fcSpawn(orbD, withSnap("oryn-snap-orphan", false)))
	require.ErrorContains(t, err, "不存在")
	assert.Equal(t, new(false), c.must(fcSpawn(orbD, withSnap("oryn-snap-orphan", true))).SnapshotUsed)
}

// limits：运行中（含暂停）的 VM 数与内存总和；停机的不算；超限报 capacity 且不建盘、不占槽位。
func TestFCLimits(t *testing.T) {
	t.Run("max_vms", func(t *testing.T) {
		c := newFCTest(t, func(cfg *config.Firecracker) { cfg.Limits.MaxVMs = 1 })
		c.must(fcSpawn(orbA))
		c.f.take()
		_, err := c.exec(fcSpawn(orbB))
		require.ErrorContains(t, err, "capacity")
		assert.Empty(t, c.f.take())
		assert.NoDirExists(t, c.d.paths.vmDir(orbB))
		assert.Equal(t, slotTable{orbA: 0}, c.slots())
		// 暂停的也算。
		c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "freeze"})
		_, err = c.exec(fcSpawn(orbB))
		require.ErrorContains(t, err, "capacity")
		// 停机后腾出位置。
		c.must(Job{Op: "pause", OrbID: orbA, PauseMode: "stop"})
		c.must(fcSpawn(orbB))
		// 已有但停机的 VM 重启时同样受限。
		_, err = c.exec(fcSpawn(orbA))
		require.ErrorContains(t, err, "capacity")
		assert.False(t, c.f.Alive(orbA))
	})
	t.Run("max_memory_mb", func(t *testing.T) {
		c := newFCTest(t, func(cfg *config.Firecracker) { cfg.Limits.MaxMemoryMB = 12288 })
		c.must(fcSpawn(orbA))                                                                 // 4096
		_, err := c.exec(fcSpawn(orbB, func(p *apigen.JobSpawnParams) { p.Type = "medium" })) // +8192 = 12288 正好
		require.NoError(t, err)
		_, err = c.exec(fcSpawn("orb-01jcccccccccccccccccccccccc"))
		require.ErrorContains(t, err, "capacity")
		require.ErrorContains(t, err, "max_memory_mb")
	})
}

// 槽位：从 0 起取最小空闲；删除后释放并被复用；网络参数随槽位变化。
func TestFCSlots(t *testing.T) {
	c := newFCTest(t)
	orbs := []string{orbA, orbB, "orb-01jcccccccccccccccccccccccc"}
	for _, o := range orbs {
		c.must(fcSpawn(o))
	}
	assert.Equal(t, slotTable{orbs[0]: 0, orbs[1]: 1, orbs[2]: 2}, c.slots())
	m := c.meta(orbB)
	assert.Equal(t, []any{1, 200001, "172.31.0.5", "172.31.0.6", "06:00:ac:1f:00:06"}, []any{m.Slot, m.UID, m.HostIP, m.GuestIP, m.MAC})

	c.must(Job{Op: "delete", OrbID: orbB})
	assert.Equal(t, slotTable{orbs[0]: 0, orbs[2]: 2}, c.slots())
	const orbD = "orb-01jdddddddddddddddddddddddd"
	c.must(fcSpawn(orbD))
	assert.Equal(t, 1, c.meta(orbD).Slot)
	assert.Equal(t, "172.31.0.5", c.meta(orbD).HostIP)

	// 分配幂等。
	n, err := c.d.paths.allocSlot(orbD)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestFCSlotNet(t *testing.T) {
	p := netip.MustParsePrefix("10.20.0.0/16")
	h, g, mac := slotNet(p, 16319)
	assert.Equal(t, "10.20.254.253", h.String())
	assert.Equal(t, "10.20.254.254", g.String())
	assert.Equal(t, "06:00:0a:14:fe:fe", mac)
	assert.Equal(t, "fc-abc", tapName("abc"))
	assert.Equal(t, "fc-123456789012", tapName("orb-x123456789012"))
}

// 8.0 表 delete：kill → 拆 tap → 删 jail 与 VM 目录 → 释放槽位；幂等。
func TestFCDelete(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	c.f.take()
	del := Job{Op: "delete", OrbID: orbA}
	c.must(del)
	assert.Equal(t, []string{"Kill " + orbA + " alive", "TapDown " + tapName(orbA)}, c.f.take())
	assert.NoDirExists(t, c.d.paths.vmDir(orbA))
	assert.Equal(t, slotTable{}, c.slots())
	c.must(del)
	assert.Equal(t, []string{"Kill " + orbA, "TapDown " + tapName(orbA)}, c.f.take())
}

// 基础盘：rootfs 更新后新 VM 用新基础盘；旧基础盘在最后一个 VM / 快照引用消失后删除；当前 rootfs 的基础盘始终保留。
func TestFCBaseGC(t *testing.T) {
	c := newFCTest(t)
	c.must(fcSpawn(orbA))
	oldBase := c.meta(orbA).Base
	c.must(Job{Op: "snapshot", OrbID: orbA, SnapshotID: snapA})

	// 换 rootfs（新 inode，内容不同）。
	require.NoError(t, os.Remove(c.d.Cfg.Rootfs))
	require.NoError(t, os.WriteFile(c.d.Cfg.Rootfs, []byte("rootfs-v2-longer"), 0o644))
	c.must(fcSpawn(orbB))
	newBase := c.meta(orbB).Base
	require.NotEqual(t, oldBase, newBase)
	assert.Equal(t, c.baseID(), newBase)

	c.must(Job{Op: "delete", OrbID: orbA})
	assert.FileExists(t, c.d.paths.base(oldBase), "快照仍引用旧基础盘")
	c.must(Job{Op: "delete_snapshot", SnapshotID: snapA})
	assert.NoFileExists(t, c.d.paths.base(oldBase))
	assert.NoFileExists(t, c.d.paths.snapDisk(snapA))
	assert.NoFileExists(t, c.d.paths.snapMeta(snapA))
	// 幂等。
	c.must(Job{Op: "delete_snapshot", SnapshotID: snapA})

	c.must(Job{Op: "delete", OrbID: orbB})
	assert.FileExists(t, c.d.paths.base(newBase), "当前 rootfs 的基础盘保留")
}

func TestFCInfo(t *testing.T) {
	c := newFCTest(t)
	info := c.d.Info()
	assert.Equal(t, apigen.Firecracker, info.Driver)
	assert.Equal(t, apigen.ProviderCapabilities{Freeze: true, Snapshot: true}, info.Capabilities)
	assert.Equal(t, []apigen.OrbType{{Name: "medium", Cpus: 4, MemoryMB: 8192}, {Name: "small", Cpus: 2, MemoryMB: 4096}}, info.Types)
	assert.Equal(t, "small", info.DefaultType)
	k := sha256.Sum256([]byte("kernel-v1"))
	assert.Equal(t, "fc:"+c.baseID()+":"+hex.EncodeToString(k[:])[:16], info.BaseImageID)

	// 产物变化（大小或 mtime）后重算。
	require.NoError(t, os.WriteFile(c.d.Cfg.Kernel, []byte("kernel-v2!"), 0o644))
	k2 := sha256.Sum256([]byte("kernel-v2!"))
	assert.Equal(t, "fc:"+c.baseID()+":"+hex.EncodeToString(k2[:])[:16], c.d.Info().BaseImageID)

	require.NoError(t, os.Remove(c.d.Cfg.Rootfs))
	assert.Empty(t, c.d.Info().BaseImageID)
}

func TestFCNewFromConfig(t *testing.T) {
	cfg := config.Provider{Driver: "firecracker", Firecracker: config.DefaultFirecracker(), OrbURL: "https://172.31.255.1:7643"}
	drv, err := New(cfg)
	require.NoError(t, err)
	fc, ok := drv.(*FirecrackerDriver)
	require.True(t, ok)
	assert.Equal(t, "https://172.31.255.1:7643", fc.OrbURL)
	assert.Len(t, fc.Cfg.Types, 3)
	_, ok = drv.(PreflightReporter)
	assert.True(t, ok)
}

// fcRecorded 是 httptest 风格的 unix socket 服务收到的一个请求。
type fcRecorded struct {
	Method, Path, Body string
}

// serveFCAPI 在 path 上起一个 HTTP 服务，记录每个请求；fail 中的路径回 400。
func serveFCAPI(t *testing.T, path string, fail map[string]bool, state string) *[]fcRecorded {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	var mu sync.Mutex
	got := &[]fcRecorded{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*got = append(*got, fcRecorded{r.Method, r.URL.Path, string(b)})
		mu.Unlock()
		switch {
		case fail[r.URL.Path]:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"fault_message":"nope"}`)
		case r.Method == http.MethodGet:
			_, _ = fmt.Fprintf(w, `{"id":"x","state":%q}`, state)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return got
}

// Boot 的请求序列与 boot_args 逐字断言；entropy 失败被忽略。
func TestFCAPIBoot(t *testing.T) {
	root, err := os.MkdirTemp("", "fcapi")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p := fcPaths{root: root}
	const orb = "orb-1"
	got := serveFCAPI(t, p.apiSock(orb), map[string]bool{"/entropy": true}, "Paused")
	cfg := config.DefaultFirecracker()
	vm := newVMMeta(cfg, orb, 3)
	vm.CPUs, vm.MemMB = 4, 8192
	api := unixAPI{sock: p.apiSock}
	require.NoError(t, api.Boot(context.Background(), vm))
	assert.Equal(t, []fcRecorded{
		{"PUT", "/machine-config", `{"mem_size_mib":8192,"smt":false,"vcpu_count":4}`},
		{"PUT", "/boot-source", `{"boot_args":"console=ttyS0 reboot=k panic=1 pci=off nomodule i8042.noaux i8042.nomux i8042.dumbkbd quiet root=/dev/vda ro init=/usr/local/bin/orbd ip=172.31.0.14::172.31.0.13:255.255.255.252::eth0:off","kernel_image_path":"/vmlinux"}`},
		{"PUT", "/drives/rootfs", `{"drive_id":"rootfs","is_read_only":true,"is_root_device":false,"path_on_host":"/base.ext4"}`},
		{"PUT", "/drives/data", `{"drive_id":"data","is_read_only":false,"is_root_device":false,"path_on_host":"/disk.ext4"}`},
		{"PUT", "/network-interfaces/eth0", `{"guest_mac":"06:00:ac:1f:00:0e","host_dev_name":"fc-orb-1","iface_id":"eth0"}`},
		{"PUT", "/vsock", `{"guest_cid":3,"uds_path":"/v.sock"}`},
		{"PUT", "/serial", `{"rate_limiter":{"one_time_burst":1048576,"refill_time":1000,"size":16384},"serial_out_path":"/console.log"}`},
		{"PUT", "/entropy", `{}`},
		{"PUT", "/actions", `{"action_type":"InstanceStart"}`},
	}, *got)

	*got = nil
	st, err := api.State(context.Background(), orb)
	require.NoError(t, err)
	assert.Equal(t, "Paused", st)
	require.NoError(t, api.SetState(context.Background(), orb, "Resumed"))
	require.NoError(t, api.CtrlAltDel(context.Background(), orb))
	assert.Equal(t, []fcRecorded{
		{"GET", "/", ""},
		{"PATCH", "/vm", `{"state":"Resumed"}`},
		{"PUT", "/actions", `{"action_type":"SendCtrlAltDel"}`},
	}, *got)

	// 其它步骤失败时报错并停在那里。
	*got = nil
	serveFail := fcPaths{root: filepath.Join(root, "f")}
	got2 := serveFCAPI(t, serveFail.apiSock(orb), map[string]bool{"/vsock": true}, "Running")
	err = unixAPI{sock: serveFail.apiSock}.Boot(context.Background(), vm)
	var ae *apiError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, 400, ae.status)
	assert.Equal(t, "/vsock", (*got2)[len(*got2)-1].Path)

	// API 不可达。
	_, err = unixAPI{sock: func(string) string { return filepath.Join(root, "nope.sock") }}.State(context.Background(), orb)
	assert.Error(t, err)
}

// vsockGuest 经 jail 里的 v.sock 走 Firecracker 的 CONNECT 握手。
func TestFCVsockGuest(t *testing.T) {
	root, err := os.MkdirTemp("", "fcvs")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p := fcPaths{root: root}
	require.NoError(t, os.MkdirAll(p.jailRoot("orb-1"), 0o700))
	ln, err := net.Listen("unix", p.vsockPath("orb-1"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan fcguest.Request, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		line, _ := r.ReadString('\n')
		if line != fmt.Sprintf("CONNECT %d\n", fcguest.Port) {
			return
		}
		_, _ = io.WriteString(conn, "OK 1073741824\n")
		req, _ := fcguest.ReadRequest(r)
		got <- req
		_ = fcguest.WriteResponse(conn, fcguest.Response{OK: true})
	}()
	g := vsockGuest{sock: p.vsockPath}
	require.NoError(t, g.Do(context.Background(), "orb-1", fcguest.Request{Op: fcguest.OpPing}))
	assert.Equal(t, fcguest.Request{Op: fcguest.OpPing}, <-got)
}

func TestFCJailerArgs(t *testing.T) {
	cfg := config.DefaultFirecracker()
	vm := newVMMeta(cfg, "orb-1", 2)
	vm.CPUs, vm.MemMB = 4, 8192
	assert.Equal(t, []string{
		"--id", "orb-1", "--exec-file", "/usr/local/bin/firecracker", "--uid", "200002", "--gid", "200002",
		"--chroot-base-dir", "/var/lib/oryn-fc/jail", "--cgroup-version", "2", "--parent-cgroup", "oryn-fc",
		"--cgroup", "cpu.max=400000 100000", "--cgroup", "memory.max=9126805504",
		"--daemonize", "--new-pid-ns", "--", "--api-sock", "/api.sock",
	}, jailerArgs(cfg, vm))
	// jailer 的 chroot 在 <chroot-base>/<exec-file 的文件名>/<id>/root。
	assert.Equal(t, "/var/lib/oryn-fc/jail/firecracker/orb-1/root/api.sock", newFCPaths(cfg).apiSock("orb-1"))
	cfg.Bin = "/opt/fc/firecracker-v1.18.0"
	assert.Equal(t, "/var/lib/oryn-fc/jail/firecracker-v1.18.0/orb-1/root/v.sock", newFCPaths(cfg).vsockPath("orb-1"))
}

func TestFCPreflightHelpers(t *testing.T) {
	listing := `table inet oryn_fc {
	chain input {
		type filter hook input priority filter - 10; policy accept;
		iifname "fc-*" ct state established,related accept
		iifname "fc-*" ip daddr 172.31.255.1 tcp dport { 7600, 7643 } accept
		iifname "fc-*" drop
	}
}`
	ports, err := nftAcceptPorts(listing)
	require.NoError(t, err)
	assert.Equal(t, []portRange{{7600, 7600}, {7643, 7643}}, ports)
	ports, err = nftAcceptPorts(`iifname "fc-*" ip daddr 172.31.255.1 tcp dport 7643 accept` + "\n" + `tcp dport 8000-8010 accept`)
	require.NoError(t, err)
	assert.Equal(t, []portRange{{7643, 7643}, {8000, 8010}}, ports)
	_, err = nftAcceptPorts(`tcp dport { https } accept`)
	assert.Error(t, err)
	_, err = nftAcceptPorts(`iifname "fc-*" drop`)
	assert.Error(t, err)

	ok := checkOrbURLPort("https://172.31.255.1:7643", "172.31.255.1", listing)
	assert.NoError(t, ok.Err)
	assert.Empty(t, ok.Warn)
	bad := checkOrbURLPort("http://172.31.255.1:7601", "172.31.255.1", listing)
	assert.ErrorContains(t, bad.Err, "7601")
	assert.Error(t, checkOrbURLPort("https://172.31.255.1", "172.31.255.1", listing).Err, "https 缺省端口 443")
	w := checkOrbURLPort("http://172.31.255.1:7643", "172.31.255.1", `tcp dport { https } accept`)
	assert.NoError(t, w.Err)
	assert.Contains(t, w.Warn, "解析 nft 规则")
	w = checkOrbURLPort("://bad", "172.31.255.1", listing)
	assert.NoError(t, w.Err)
	assert.Contains(t, w.Warn, "orb_url")
	w = checkOrbURLPort("http://10.0.0.1:7643", "172.31.255.1", listing)
	assert.NoError(t, w.Err)
	assert.Contains(t, w.Warn, "host_addr")

	assert.Empty(t, kernelWarning("6.1.155-1-lts"))
	assert.Empty(t, kernelWarning("6.18.2-cachyos"))
	assert.Empty(t, kernelWarning("5.10.240"))
	assert.NotEmpty(t, kernelWarning("6.12.1"))
	assert.NotEmpty(t, kernelWarning("7.3.0-5-cachyos"))

	cidr := netip.MustParsePrefix("172.31.0.0/16")
	ipn := func(s string) net.Addr {
		_, n, _ := net.ParseCIDR(s)
		ip, _, _ := net.ParseCIDR(s)
		return &net.IPNet{IP: ip, Mask: n.Mask}
	}
	assert.NoError(t, cidrOverlap(cidr, "eth0", []net.Addr{ipn("192.168.1.5/24"), ipn("172.30.0.1/16")}))
	assert.ErrorContains(t, cidrOverlap(cidr, "br0", []net.Addr{ipn("172.31.5.1/24")}), "br0")
	assert.ErrorContains(t, cidrOverlap(cidr, "docker0", []net.Addr{ipn("172.16.0.1/12")}), "docker0")

	dir := t.TempDir()
	exe := filepath.Join(dir, "fc")
	require.NoError(t, os.WriteFile(exe, nil, 0o755))
	assert.NoError(t, checkArtifact(exe, true))
	noexec := filepath.Join(dir, "k")
	require.NoError(t, os.WriteFile(noexec, nil, 0o600))
	assert.Error(t, checkArtifact(noexec, true))
	assert.ErrorContains(t, checkArtifact(noexec, false), "其他用户可读")
	assert.Error(t, checkArtifact(dir, false))
	assert.Error(t, checkArtifact(filepath.Join(dir, "missing"), false))
}

// 预检的 state_dir 同一文件系统检查：能硬链接则通过，产物缺失则失败。
func TestFCPreflightSameFS(t *testing.T) {
	c := newFCTest(t)
	assert.NoError(t, c.d.checkSameFS())
	assert.NoFileExists(t, filepath.Join(c.d.Cfg.StateDir, ".preflight-link"))
	c.d.Cfg.Kernel = filepath.Join(c.root, "missing")
	assert.Error(t, c.d.checkSameFS())
}

// 元数据与槽位表是 JSON 文件，格式稳定（升级 provider 不丢 VM）。
func TestFCMetaJSON(t *testing.T) {
	b, err := json.Marshal(vmMeta{OrbID: "o", Base: "b", Snapshot: "s", Type: "small", CPUs: 2, MemMB: 4096, Slot: 1, UID: 2, Tap: "t", HostIP: "h", GuestIP: "g", MAC: "m"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"orbId":"o","base":"b","snapshot":"s","type":"small","cpus":2,"memMB":4096,"slot":1,"uid":2,"tap":"t","hostIP":"h","guestIP":"g","mac":"m"}`, string(b))
	assert.True(t, strings.HasPrefix(tapName(orbA), "fc-"))
}
