package providerrt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

func TestParseDockerPSSizes(t *testing.T) {
	raw := "" +
		"orb-a\t0B (virtual 2.14GB)\n" +
		"orb-b\t128MB (virtual 2.27GB)\n" +
		"orb-c\t1.5GB (virtual 3.64GB)\n" +
		"orb-d\t34.5kB\n" +
		"orb-e\t2GiB (virtual 4GiB)\n" +
		"broken-line-without-tab\n"

	total, byOrb := parseDockerPSSizes(raw)
	assert.Equal(t, int64(0), byOrb["orb-a"])
	assert.Equal(t, int64(128_000_000), byOrb["orb-b"])
	assert.Equal(t, int64(1_500_000_000), byOrb["orb-c"])
	assert.Equal(t, int64(34_500), byOrb["orb-d"])
	assert.Equal(t, int64(2*1024*1024*1024), byOrb["orb-e"])
	assert.Equal(t, int64(128_000_000+1_500_000_000+34_500+2*1024*1024*1024), total)
}

func TestFirecrackerOrbDiskUsageSparse(t *testing.T) {
	dir := t.TempDir()
	d := NewFirecracker(config.Firecracker{StateDir: dir}, "http://127.0.0.1:8080")

	// 创建一个稀疏文件（逻辑 1 GiB，但仅写入 1 MiB 物理块），确认统计结果远小于逻辑大小
	require.NoError(t, os.MkdirAll(d.paths.vmDir("orb-fc-1"), 0o700))
	f, err := os.Create(d.paths.vmDisk("orb-fc-1"))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(1<<30))
	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = 0x5a
	}
	_, err = f.WriteAt(buf, 0)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	total, byOrb, ok := d.OrbDiskUsage(context.Background())
	require.True(t, ok)
	assert.Contains(t, byOrb, "orb-fc-1")
	assert.GreaterOrEqual(t, byOrb["orb-fc-1"], int64(1<<20))
	assert.Less(t, byOrb["orb-fc-1"], int64(64<<20), "稀疏盘不应按 1GiB 逻辑大小虚报")
	assert.Equal(t, byOrb["orb-fc-1"], total)
}

func TestProcessOrbDiskUsage(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess(dir, "orbd", config.Docker{})

	h1 := p.HomeDir("orb-p1")
	require.NoError(t, os.MkdirAll(h1, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(h1, "data.bin"), make([]byte, 64*1024), 0o600))

	total, byOrb, ok := p.OrbDiskUsage(context.Background())
	require.True(t, ok)
	assert.GreaterOrEqual(t, byOrb["orb-p1"], int64(64*1024))
	assert.Equal(t, byOrb["orb-p1"], total)
}

func TestProcessOrbDiskUsageRespectsContextCancel(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess(dir, "orbd", config.Docker{})
	h1 := p.HomeDir("orb-p1")
	require.NoError(t, os.MkdirAll(h1, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(h1, "data.bin"), make([]byte, 4096), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	total, byOrb, ok := p.OrbDiskUsage(ctx)
	assert.False(t, ok, "ctx 已取消时不应返回截断结果")
	assert.Zero(t, total)
	assert.Nil(t, byOrb)
}

type blockingOrbDiskDriver struct {
	entered   chan struct{}
	unblock   chan struct{}
	inFlight  atomic.Int32
	maxFlight atomic.Int32
	enterOnce sync.Once
}

func (d *blockingOrbDiskDriver) Info() Info {
	return Info{Driver: apigen.Process}
}

func (d *blockingOrbDiskDriver) Exec(context.Context, Job, io.Writer) (*apigen.JobResult, error) {
	return nil, nil
}

func (d *blockingOrbDiskDriver) OrbDiskUsage(ctx context.Context) (int64, map[string]int64, bool) {
	cur := d.inFlight.Add(1)
	defer d.inFlight.Add(-1)
	for {
		prev := d.maxFlight.Load()
		if cur <= prev || d.maxFlight.CompareAndSwap(prev, cur) {
			break
		}
	}
	d.enterOnce.Do(func() { close(d.entered) })
	select {
	case <-d.unblock:
		return 123456, map[string]int64{"orb-slow-1": 123456}, true
	case <-ctx.Done():
		return 0, nil, false
	}
}

// TestRunnerHelloNotBlockedBySlowOrbDiskSizer 证明慢速/阻塞的 OrbDiskSizer 不会延迟首次 hello、
// worker 认领启动或心跳续租，且采样单并发并在完成后随下一次心跳上报快照。
func TestRunnerHelloNotBlockedBySlowOrbDiskSizer(t *testing.T) {
	var (
		mu        sync.Mutex
		hellos    []apigen.ProviderHelloRequest
		claimedCh = make(chan struct{}, 1)
		helloCh   = make(chan struct{}, 16)
		stopSrv   = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/provider/v1/hello":
			var req apigen.ProviderHelloRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			hellos = append(hellos, req)
			mu.Unlock()
			select {
			case helloCh <- struct{}{}:
			default:
			}
			_ = json.NewEncoder(w).Encode(apigen.ProviderHelloResponse{})
		case "/provider/v1/jobs/claim":
			select {
			case claimedCh <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
			case <-stopSrv:
			}
		case "/provider/v1/bye":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer func() {
		close(stopSrv)
		srv.Close()
	}()

	drv := &blockingOrbDiskDriver{
		entered: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	runner := &Runner{
		URL:               srv.URL,
		Token:             "opv_test",
		Driver:            drv,
		Concurrency:       1,
		HeartbeatInterval: 40 * time.Millisecond,
		OrbDiskInterval:   20 * time.Millisecond,
		ShutdownGrace:     time.Second,
		HTTP:              srv.Client(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	// 1. 即使 OrbDiskUsage 一直阻塞，首次 hello 与 worker 认领也必须立刻完成
	select {
	case <-helloCh:
	case <-time.After(2 * time.Second):
		t.Fatal("首次 hello 被阻塞的 OrbDiskSizer 卡住")
	}
	select {
	case <-claimedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 认领被阻塞的 OrbDiskSizer 卡住")
	}
	// 后台采样器已启动并正阻塞在 OrbDiskUsage 内
	select {
	case <-drv.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("后台 orb 磁盘采样器未启动")
	}

	// 2. 采样阻塞期间，续租心跳照常发出（不被 helloMu 或磁盘采样阻塞）
	select {
	case <-helloCh:
	case <-time.After(2 * time.Second):
		t.Fatal("心跳续租在磁盘采样阻塞期间未能发出")
	}
	mu.Lock()
	require.GreaterOrEqual(t, len(hellos), 2)
	require.NotNil(t, hellos[0].HostMetrics)
	assert.Nil(t, hellos[0].HostMetrics.OrbDiskBytes, "首次 hello 不应同步等待磁盘采样")
	mu.Unlock()

	// 3. 放行采样后，后续心跳携带缓存快照，且并发度始终 <= 1
	close(drv.unblock)
	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		last := hellos[len(hellos)-1]
		return last.HostMetrics != nil &&
			last.HostMetrics.OrbDiskBytes != nil &&
			*last.HostMetrics.OrbDiskBytes == 123456 &&
			last.HostMetrics.OrbDiskByOrb["orb-slow-1"] == 123456
	}, "采样完成后心跳应上报缓存的 orb 磁盘快照")
	assert.Equal(t, int32(1), drv.maxFlight.Load(), "后台磁盘采样必须是单并发")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Runner 未在 ctx 取消后及时退出")
	}
}
