package providerrt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// mkExt4 用 `mke2fs -d src` 生成 ext4 镜像；没有 mke2fs 时跳过。
func mkExt4(t *testing.T, src, img string) {
	t.Helper()
	bin, err := exec.LookPath("mke2fs")
	if err != nil {
		t.Skip("没有 mke2fs")
	}
	f, err := os.Create(img)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(64<<20))
	require.NoError(t, f.Close())
	out, err := exec.Command(bin, "-q", "-t", "ext4", "-F", "-d", src, img).CombinedOutput()
	require.NoError(t, err, string(out))
}

// writeTree 在 root 下按 "路径 → 内容" 写文件（内容为 "->target" 时建符号链接，以 "/" 结尾的路径建目录）。
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			require.NoError(t, os.MkdirAll(full, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		if target, ok := strings.CutPrefix(body, "->"); ok {
			require.NoError(t, os.Symlink(target, full))
			continue
		}
		require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	}
}

// overlayImages 构造 lower / upper 两块镜像：upper 里有覆盖、新增、whiteout、opaque 目录与 .oryn。
// 需要 root（mknod 0:0 与 trusted.* 扩展属性），否则跳过。
func overlayImages(t *testing.T) (lowerImg, upperImg string) {
	t.Helper()
	dir := t.TempDir()
	lower, upper := filepath.Join(dir, "lower"), filepath.Join(dir, "upper")
	writeTree(t, lower, map[string]string{
		"etc/os-release":            "ID=test\n",
		"home/user/a.txt":           "lower a",
		"home/user/keep.txt":        "keep",
		"home/user/gone.txt":        "gone",
		"home/user/dir1/x":          "x",
		"home/user/opq/old":         "old",
		"home/user/link":            "->a.txt",
		"home/user/.bashrc":         "# rc",
		"home/user/workspace/repo/": "",
	})
	uh := filepath.Join(upper, "upper/home/user")
	writeTree(t, upper, map[string]string{
		"upper/home/user/a.txt":        "upper a!",
		"upper/home/user/new.txt":      "new",
		"upper/home/user/dir1/y":       "y",
		"upper/home/user/opq/new":      "new in opaque",
		"upper/home/user/.oryn/secret": "token",
		"upper/home/user/escape":       "->/etc/os-release",
		"work/":                        "",
	})
	if err := unix.Mknod(filepath.Join(uh, "gone.txt"), unix.S_IFCHR|0o600, 0); err != nil {
		t.Skipf("无法创建 whiteout（需要 root）: %v", err)
	}
	if err := unix.Setxattr(filepath.Join(uh, "opq"), "trusted.overlay.opaque", []byte("y"), 0); err != nil {
		t.Skipf("无法设置 trusted.* 扩展属性（需要 root）: %v", err)
	}
	lowerImg, upperImg = filepath.Join(dir, "lower.ext4"), filepath.Join(dir, "upper.ext4")
	mkExt4(t, lower, lowerImg)
	mkExt4(t, upper, upperImg)
	return lowerImg, upperImg
}

func TestOverlayIndexMerge(t *testing.T) {
	lowerImg, upperImg := overlayImages(t)
	ix, err := buildOverlayIndex(context.Background(), lowerImg, upperImg, nil)
	require.NoError(t, err)

	names := func(p string) []string {
		l, err := ix.list(p)
		require.NoError(t, err)
		var out []string
		for _, e := range l.Entries {
			out = append(out, e.Name)
		}
		slices.Sort(out)
		return out
	}
	assert.Equal(t, []string{".bashrc", "a.txt", "dir1", "escape", "keep.txt", "link", "new.txt", "opq", "workspace"}, names("~"),
		"whiteout 删掉 gone.txt，.oryn 不进索引")
	assert.Equal(t, []string{"x", "y"}, names("~/dir1"), "普通目录合并两层")
	assert.Equal(t, []string{"new"}, names("~/opq"), "opaque 目录遮住 lower 的内容")

	n, _ := ix.lookup("/home/user/a.txt")
	require.NotNil(t, n)
	assert.EqualValues(t, len("upper a!"), n.size, "upper 覆盖 lower")
	assert.Equal(t, upperImg, n.ref.(fcFileRef).img)
	n, _ = ix.lookup("/home/user/keep.txt")
	require.NotNil(t, n)
	assert.Equal(t, lowerImg, n.ref.(fcFileRef).img)

	_, _, err = ix.stat("~/.oryn/secret")
	assertFsErr(t, err, http.StatusForbidden, "protected_path")
	_, _, err = ix.readTarget("~/escape")
	assertFsErr(t, err, http.StatusForbidden, "outside_home")
	r := ix.roots([]string{"/home/user/workspace/repo"})
	assert.Equal(t, "/home/user/workspace/repo", r.Workdir)
}

// 从未启动过的 VM：可写盘里还没有 /upper，只有 lower 的内容。
func TestOverlayIndexWithoutUpper(t *testing.T) {
	dir := t.TempDir()
	lower, upper := filepath.Join(dir, "lower"), filepath.Join(dir, "upper")
	writeTree(t, lower, map[string]string{"home/user/a.txt": "a"})
	require.NoError(t, os.MkdirAll(upper, 0o755))
	lowerImg, upperImg := filepath.Join(dir, "l.ext4"), filepath.Join(dir, "u.ext4")
	mkExt4(t, lower, lowerImg)
	mkExt4(t, upper, upperImg)
	ix, err := buildOverlayIndex(context.Background(), lowerImg, upperImg, nil)
	require.NoError(t, err)
	l, err := ix.list("~")
	require.NoError(t, err)
	require.Len(t, l.Entries, 1)
	assert.Equal(t, "a.txt", l.Entries[0].Name)
}

// upper 的上级目录被 whiteout（rm -rf /home 后的 VM）：合并视图里没有 OrbHome。
func TestOverlayIndexHomeWhitedOut(t *testing.T) {
	dir := t.TempDir()
	lower, upper := filepath.Join(dir, "lower"), filepath.Join(dir, "upper")
	writeTree(t, lower, map[string]string{"home/user/a.txt": "a"})
	require.NoError(t, os.MkdirAll(filepath.Join(upper, "upper"), 0o755))
	if err := unix.Mknod(filepath.Join(upper, "upper/home"), unix.S_IFCHR|0o600, 0); err != nil {
		t.Skipf("无法创建 whiteout（需要 root）: %v", err)
	}
	lowerImg, upperImg := filepath.Join(dir, "l.ext4"), filepath.Join(dir, "u.ext4")
	mkExt4(t, lower, lowerImg)
	mkExt4(t, upper, upperImg)
	ix, err := buildOverlayIndex(context.Background(), lowerImg, upperImg, nil)
	require.NoError(t, err)
	l, err := ix.list("~")
	require.NoError(t, err)
	assert.Empty(t, l.Entries)
}

// fcOfflineFixture 建一台带 overlay 两层镜像的「已停机」VM。
func fcOfflineFixture(t *testing.T, orb string) *fcTest {
	t.Helper()
	lowerImg, upperImg := overlayImages(t)
	ft := newFCTest(t)
	d := ft.d
	vm := vmMeta{OrbID: orb, Base: "base1", Type: "small", CPUs: 2, MemMB: 4096}
	require.NoError(t, os.MkdirAll(d.paths.vmDir(orb), 0o700))
	require.NoError(t, writeJSONFile(d.paths.vmMeta(orb), vm))
	require.NoError(t, os.MkdirAll(d.paths.basesDir(), 0o755))
	require.NoError(t, os.Rename(lowerImg, d.paths.base("base1")))
	require.NoError(t, os.Rename(upperImg, d.paths.vmDisk(orb)))
	return ft
}

func fcFsReq(orb string, op apigen.ProviderFsRequestOp, p string) apigen.ProviderFsRequest {
	req := apigen.ProviderFsRequest{ID: "r1", OrbID: orb, Op: op}
	if p != "" {
		req.Path = &p
	}
	return req
}

// 预览过程中 orb 被唤醒：spawn 开始后，进行中的读取与旧索引一律作废，不会读到 VM 启动后被复用的 inode / 数据块。
func TestOfflineFsFirecrackerWakeDuringRead(t *testing.T) {
	const orb = "orb-fc1"
	ft := fcOfflineFixture(t, orb)
	d, ctx := ft.d, context.Background()

	rep := d.OfflineFS(ctx, ctx, fcFsReq(orb, apigen.Read, "~/keep.txt"))
	require.Equal(t, http.StatusOK, rep.Status)
	buf := make([]byte, 1)
	n, err := rep.Body.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "k", string(buf[:n]), "唤醒前正常读取")

	_, err = d.Exec(ctx, fcSpawn(orb), ft.out)
	require.NoError(t, err, ft.out.String())
	require.True(t, ft.f.Alive(orb))

	_, err = rep.Body.Read(buf)
	assert.ErrorIs(t, err, errOrbWoke, "spawn 之后在途的应答体不能再读盘")
	require.NoError(t, rep.Body.Close())

	st, _, body := readReply(t, d.OfflineFS(ctx, ctx, fcFsReq(orb, apigen.List, "~")))
	assert.Equal(t, http.StatusConflict, st)
	assert.Contains(t, body, `"orb_not_stopped"`)

	// 旧索引（上一代磁盘）里的条目即使被拿来读，也会被拒绝
	ix, err := buildOverlayIndex(ctx, d.paths.base("base1"), d.paths.vmDisk(orb), nil, &fcDiskSession{g: d.diskGate(orb), gen: 0})
	assert.ErrorIs(t, err, errOrbWoke, "旧代数的建索引整体作废")
	assert.Nil(t, ix)
}

// 生命周期操作进行中（VM 进程还没起来）也拒绝新的预览；结束且 VM 不在时按新一代磁盘重建索引。
func TestOfflineFsFirecrackerBusyDuringLifecycle(t *testing.T) {
	const orb = "orb-fc1"
	ft := fcOfflineFixture(t, orb)
	d, ctx := ft.d, context.Background()
	st, _, body := readReply(t, d.OfflineFS(ctx, ctx, fcFsReq(orb, apigen.List, "~")))
	require.Equal(t, http.StatusOK, st, body)
	old := d.offline.builds[orb]
	require.NotNil(t, old)

	end := d.beginLifecycle(orb)
	st, _, body = readReply(t, d.OfflineFS(ctx, ctx, fcFsReq(orb, apigen.Read, "~/keep.txt")))
	assert.Equal(t, http.StatusConflict, st, "spawn 期间 VM 进程未起来也不服务")
	assert.Contains(t, body, `"orb_not_stopped"`)
	assert.Nil(t, d.offline.builds[orb], "旧索引已丢弃")
	end()

	st, _, body = readReply(t, d.OfflineFS(ctx, ctx, fcFsReq(orb, apigen.Read, "~/keep.txt")))
	require.Equal(t, http.StatusOK, st, body)
	assert.Equal(t, "keep", body)
	assert.NotSame(t, old, d.offline.builds[orb], "按新一代磁盘重建")
}

// 预览读取与 spawn 并发：-race 下无数据竞争，spawn 返回后不再有读取成功。
func TestOfflineFsFirecrackerConcurrentSpawn(t *testing.T) {
	const orb = "orb-fc1"
	ft := fcOfflineFixture(t, orb)
	d, ctx := ft.d, context.Background()
	var spawned atomic.Bool
	var lateOK atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				after := spawned.Load()
				rep := d.OfflineFS(ctx, ctx, fcFsReq(orb, apigen.Read, "~/a.txt"))
				if rep.Body == nil {
					continue
				}
				b, err := io.ReadAll(rep.Body)
				_ = rep.Body.Close()
				if after && err == nil && rep.Status == http.StatusOK {
					lateOK.Add(1)
				}
				if err == nil && rep.Status == http.StatusOK {
					assert.Equal(t, "upper a!", string(b))
				}
			}
		})
	}
	_, err := d.Exec(ctx, fcSpawn(orb), ft.out)
	spawned.Store(true)
	wg.Wait()
	require.NoError(t, err, ft.out.String())
	assert.Zero(t, lateOK.Load(), "spawn 完成后的读取全部被拒")
}

func TestOfflineFsFirecracker(t *testing.T) {
	const orb = "orb-fc1"
	ft := fcOfflineFixture(t, orb)
	d := ft.d

	ctx := context.Background()
	do := func(orbID string, op apigen.ProviderFsRequestOp, p, rng string) (int, http.Header, string) {
		req := apigen.ProviderFsRequest{ID: "r1", OrbID: orbID, Op: op}
		if p != "" {
			req.Path = &p
		}
		if rng != "" {
			req.Range = &rng
		}
		return readReply(t, d.OfflineFS(ctx, ctx, req))
	}

	assert.True(t, *d.Info().Capabilities.OfflineFs)

	st, _, body := do("orb-none", apigen.Roots, "", "")
	assert.Equal(t, http.StatusNotFound, st, body)

	ft.f.mu.Lock()
	ft.f.alive[orb] = true
	ft.f.mu.Unlock()
	st, _, body = do(orb, apigen.List, "~", "")
	assert.Equal(t, http.StatusConflict, st)
	assert.Contains(t, body, `"orb_not_stopped"`)
	ft.f.mu.Lock()
	ft.f.alive[orb] = false
	ft.f.mu.Unlock()

	st, _, body = do(orb, apigen.List, "~", "")
	require.Equal(t, http.StatusOK, st, body)
	var listing apigen.FsListing
	require.NoError(t, json.Unmarshal([]byte(body), &listing))
	assert.Len(t, listing.Entries, 9)

	st, _, body = do(orb, apigen.Stat, "~/link", "")
	require.Equal(t, http.StatusOK, st, body)
	var e apigen.FsEntry
	require.NoError(t, json.Unmarshal([]byte(body), &e))
	assert.Equal(t, apigen.FsEntryTypeSymlink, e.Type)
	require.NotNil(t, e.Text)
	assert.True(t, *e.Text, "链接指向的 upper 文件按文本嗅探")

	st, h, body := do(orb, apigen.Read, "~/link", "")
	require.Equal(t, http.StatusOK, st, body)
	assert.Equal(t, "upper a!", body)
	assert.Equal(t, "bytes", h.Get("Accept-Ranges"))
	st, h, body = do(orb, apigen.Read, "~/keep.txt", "bytes=1-2")
	require.Equal(t, http.StatusPartialContent, st, body)
	assert.Equal(t, "ee", body)
	assert.Equal(t, "bytes 1-2/4", h.Get("Content-Range"))
	st, h, _ = do(orb, apigen.Read, "~/keep.txt", "bytes=9-")
	assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, st)
	assert.Equal(t, "bytes */4", h.Get("Content-Range"))
	st, _, body = do(orb, apigen.Read, "~/dir1", "")
	assert.Equal(t, http.StatusConflict, st)
	assert.Contains(t, body, `"is_a_directory"`)

	// 可写盘变了（VM 运行过）：重建索引
	require.NoError(t, os.Chtimes(d.paths.vmDisk(orb), time.Now(), time.Now().Add(time.Hour)))
	oldWait := offlineIndexWait
	offlineIndexWait = 5 * time.Second
	t.Cleanup(func() { offlineIndexWait = oldWait })
	st, _, body = do(orb, apigen.List, "~/opq", "")
	require.Equal(t, http.StatusOK, st, body)
}
