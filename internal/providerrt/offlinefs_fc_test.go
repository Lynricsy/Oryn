package providerrt

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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

func TestOfflineFsFirecracker(t *testing.T) {
	lowerImg, upperImg := overlayImages(t)
	ft := newFCTest(t)
	d := ft.d
	const orb = "orb-fc1"
	vm := vmMeta{OrbID: orb, Base: "base1"}
	require.NoError(t, os.MkdirAll(d.paths.vmDir(orb), 0o700))
	require.NoError(t, writeJSONFile(d.paths.vmMeta(orb), vm))
	require.NoError(t, os.MkdirAll(d.paths.basesDir(), 0o755))
	require.NoError(t, os.Rename(lowerImg, d.paths.base("base1")))
	require.NoError(t, os.Rename(upperImg, d.paths.vmDisk(orb)))

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
