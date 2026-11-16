package providerrt

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// tarEntry 描述测试 tar 流里的一个条目。
type tarEntry struct {
	name     string
	typ      byte
	body     string
	linkname string
	mode     int64
}

var offlineTestMtime = time.Date(2026, 11, 1, 8, 0, 0, 123456789, time.UTC)

func makeTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
			if e.typ == tar.TypeDir {
				mode = 0o755
			}
		}
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Linkname: e.linkname, Mode: mode, ModTime: offlineTestMtime, Format: tar.FormatPAX}
		if e.typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if e.typ == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// homeTar 是 `docker cp orb:/home/user -` 的样例（条目以 `user/` 开头）。
func homeTar(t *testing.T) []byte {
	return makeTar(t,
		tarEntry{name: "user/", typ: tar.TypeDir, mode: 0o750},
		tarEntry{name: "user/.oryn/", typ: tar.TypeDir},
		tarEntry{name: "user/.oryn/orb-token", typ: tar.TypeReg, body: "secret", mode: 0o600},
		tarEntry{name: "user/b.txt", typ: tar.TypeReg, body: "hello world"},
		tarEntry{name: "user/A.txt", typ: tar.TypeReg, body: "A"},
		tarEntry{name: "user/zdir/", typ: tar.TypeDir},
		tarEntry{name: "user/adir/", typ: tar.TypeDir},
		tarEntry{name: "user/zdir/inner.go", typ: tar.TypeReg, body: "package x\n"},
		tarEntry{name: "user/link-dir", typ: tar.TypeSymlink, linkname: "zdir"},
		tarEntry{name: "user/abs-link", typ: tar.TypeSymlink, linkname: "/home/user/b.txt"},
		tarEntry{name: "user/escape", typ: tar.TypeSymlink, linkname: "/etc/passwd"},
		tarEntry{name: "user/dangling", typ: tar.TypeSymlink, linkname: "nope"},
		tarEntry{name: "user/to-oryn", typ: tar.TypeSymlink, linkname: ".oryn/orb-token"},
		tarEntry{name: "user/hard", typ: tar.TypeLink, linkname: "user/b.txt"},
		tarEntry{name: "user/fifo", typ: tar.TypeFifo},
		tarEntry{name: "user/deep/x/y.txt", typ: tar.TypeReg, body: "y"},
		tarEntry{name: "user/up", typ: tar.TypeSymlink, linkname: "../user/zdir"},
		tarEntry{name: "user/loop", typ: tar.TypeSymlink, linkname: "loop"},
	)
}

func testIndex(t *testing.T) *fsIndex {
	t.Helper()
	var last int
	ix, err := buildIndex(bytes.NewReader(homeTar(t)), func(n int) { last = n })
	require.NoError(t, err)
	assert.Equal(t, ix.count, last, "进度回调给出已扫描条目数")
	assert.False(t, ix.truncated)
	return ix
}

func assertFsErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var fe *fsErr
	require.True(t, errors.As(err, &fe), "want fsErr %s, got %v", code, err)
	assert.Equal(t, status, fe.status, fe.msg)
	assert.Equal(t, code, fe.code, fe.msg)
}

func TestOfflineIndexListOrder(t *testing.T) {
	ix := testIndex(t)
	res, err := ix.list("~")
	require.NoError(t, err)
	assert.Equal(t, OrbHome, res.Path)
	assert.False(t, res.Truncated)
	var names []string
	for _, e := range res.Entries {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{
		"adir", "deep", "link-dir", "up", "zdir", // 目录与指向目录的链接在前
		"A.txt", "abs-link", "b.txt", "dangling", "escape", "fifo", "hard", "loop", "to-oryn",
	}, names, ".oryn 不出现")

	byName := map[string]apigen.FsEntry{}
	for _, e := range res.Entries {
		byName[e.Name] = e
	}
	b := byName["b.txt"]
	assert.Equal(t, apigen.FsEntryTypeFile, b.Type)
	assert.Equal(t, "/home/user/b.txt", b.Path)
	assert.EqualValues(t, 11, b.Size)
	assert.Equal(t, 0o644, b.Mode)
	assert.Equal(t, offlineTestMtime.UnixMilli(), b.Mtime)
	require.NotNil(t, b.Etag)
	assert.Equal(t, `"1793520000123456789-11"`, *b.Etag)

	assert.Equal(t, apigen.FsEntryTypeFile, byName["hard"].Type, "硬链接按普通文件")
	assert.EqualValues(t, 11, byName["hard"].Size)
	assert.Equal(t, apigen.FsEntryTypeOther, byName["fifo"].Type)
	assert.Equal(t, apigen.FsEntryTypeDir, byName["deep"].Type, "缺失的上级目录隐式建立")

	for name, want := range map[string]apigen.FsEntryTargetType{"link-dir": "dir", "up": "dir", "abs-link": "file"} {
		e := byName[name]
		assert.Equal(t, apigen.FsEntryTypeSymlink, e.Type, name)
		require.NotNil(t, e.TargetType, name)
		assert.Equal(t, want, *e.TargetType, name)
	}
	for _, name := range []string{"escape", "dangling", "to-oryn", "loop"} {
		e := byName[name]
		assert.Nil(t, e.TargetType, "%s：悬空 / 越界 / .oryn 不给 targetType", name)
		require.NotNil(t, e.Target)
	}
	assert.Equal(t, "/etc/passwd", *byName["escape"].Target)
	assert.Nil(t, byName["escape"].Etag)

	// 根条目的属性来自 tar 的 `user/` 条目。
	e, _, err := ix.stat("~")
	require.NoError(t, err)
	assert.Equal(t, apigen.FsEntryTypeDir, e.Type)
	assert.Equal(t, 0o750, e.Mode)
}

func TestOfflineIndexSymlinkPaths(t *testing.T) {
	ix := testIndex(t)

	// 路径中段的符号链接：列表路径保持词法路径。
	res, err := ix.list("/home/user/link-dir")
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)
	assert.Equal(t, "/home/user/link-dir/inner.go", res.Entries[0].Path)

	e, sniff, err := ix.stat("~/link-dir/inner.go")
	require.NoError(t, err)
	assert.Equal(t, apigen.FsEntryTypeFile, e.Type)
	assert.Equal(t, "/home/user/link-dir/inner.go", e.Path)
	assert.Equal(t, "/home/user/zdir/inner.go", sniff)

	_, real, err := ix.readTarget("~/up/inner.go")
	require.NoError(t, err)
	assert.Equal(t, "/home/user/zdir/inner.go", real)

	// 链接本身：Lstat 语义；指向文件时 sniff 用目标。
	e, sniff, err = ix.stat("~/abs-link")
	require.NoError(t, err)
	assert.Equal(t, apigen.FsEntryTypeSymlink, e.Type)
	assert.Equal(t, "/home/user/b.txt", sniff)

	// 悬空链接：stat 照常给链接本身，不嗅探；读取 404。
	e, sniff, err = ix.stat("~/dangling")
	require.NoError(t, err)
	assert.Equal(t, apigen.FsEntryTypeSymlink, e.Type)
	assert.Nil(t, e.TargetType)
	assert.Empty(t, sniff)
	_, _, err = ix.readTarget("~/dangling")
	assertFsErr(t, err, http.StatusNotFound, "not_found")
	_, _, err = ix.stat("~/missing")
	assertFsErr(t, err, http.StatusNotFound, "not_found")

	// 循环链接。
	e, _, err = ix.stat("~/loop")
	require.NoError(t, err)
	assert.Equal(t, apigen.FsEntryTypeSymlink, e.Type)
	_, err = ix.list("~/loop")
	assertFsErr(t, err, http.StatusBadRequest, "invalid_path")
}

func TestOfflineIndexPathPolicy(t *testing.T) {
	ix := testIndex(t)

	// 越出 home。
	_, err := ix.list("~/escape")
	assertFsErr(t, err, http.StatusForbidden, "outside_home")
	_, _, err = ix.stat("~/escape")
	assertFsErr(t, err, http.StatusForbidden, "outside_home")
	_, _, err = ix.readTarget("~/escape")
	assertFsErr(t, err, http.StatusForbidden, "outside_home")
	_, err = ix.list("/etc")
	assertFsErr(t, err, http.StatusForbidden, "outside_home")
	_, _, err = ix.stat("/home")
	assertFsErr(t, err, http.StatusForbidden, "outside_home")
	_, err = ix.list("/")
	assertFsErr(t, err, http.StatusForbidden, "outside_home")
	res, err := ix.list("/home/user/zdir/../../user/adir")
	require.NoError(t, err, "词法上经过上级目录再回到 home")
	assert.Equal(t, "/home/user/adir", res.Path)

	// .oryn：直接访问与经链接访问都是 protected_path。
	_, err = ix.list("~/.oryn")
	assertFsErr(t, err, http.StatusForbidden, "protected_path")
	_, _, err = ix.stat("/home/user/.oryn/orb-token")
	assertFsErr(t, err, http.StatusForbidden, "protected_path")
	_, _, err = ix.stat("~/to-oryn")
	assertFsErr(t, err, http.StatusForbidden, "protected_path")
	_, _, err = ix.readTarget("~/to-oryn")
	assertFsErr(t, err, http.StatusForbidden, "protected_path")

	// 类型不符。
	_, err = ix.list("~/b.txt")
	assertFsErr(t, err, http.StatusConflict, "not_a_directory")
	_, err = ix.list("~/b.txt/x")
	assertFsErr(t, err, http.StatusConflict, "not_a_directory")
	_, _, err = ix.stat("~/b.txt/x")
	assertFsErr(t, err, http.StatusConflict, "not_a_directory")
	_, _, err = ix.readTarget("~/zdir")
	assertFsErr(t, err, http.StatusConflict, "is_a_directory")
	_, _, err = ix.readTarget("~/link-dir")
	assertFsErr(t, err, http.StatusConflict, "is_a_directory")
	_, _, err = ix.readTarget("~/fifo")
	assertFsErr(t, err, http.StatusBadRequest, "invalid_path")

	// 非法路径。
	for _, p := range []string{"", "  ", "rel/x", "~user", "/home/user/\x00"} {
		_, err = ix.list(p)
		assertFsErr(t, err, http.StatusBadRequest, "invalid_path")
	}
}

func TestOfflineIndexRoots(t *testing.T) {
	ix := testIndex(t)
	assert.Equal(t, apigen.FsRoots{Home: OrbHome, Workdir: OrbHome}, ix.roots(nil))
	assert.Equal(t, apigen.FsRoots{Home: OrbHome, Workdir: OrbHome}, ix.roots([]string{"/etc", "~/missing", "~/b.txt", "~/.oryn"}))
	assert.Equal(t, "/home/user/link-dir", ix.roots([]string{"/home/user/missing", "~/b.txt", "/home/user/link-dir", "/home/user/zdir"}).Workdir)
	assert.Equal(t, "/home/user/zdir", ix.roots([]string{"", "/home/user/zdir/"}).Workdir)
}

func TestOfflineIndexTruncated(t *testing.T) {
	old := offlineIndexMaxEntries
	offlineIndexMaxEntries = 3
	t.Cleanup(func() { offlineIndexMaxEntries = old })
	ix, err := buildIndex(bytes.NewReader(homeTar(t)), nil)
	require.NoError(t, err)
	assert.True(t, ix.truncated)
	assert.Equal(t, 3, ix.count)
	res, err := ix.list("~")
	require.NoError(t, err)
	assert.True(t, res.Truncated, "索引被截断时所有列表都标记 truncated")
	assert.Len(t, res.Entries, 3)
	_, err = ix.list("~/adir")
	assertFsErr(t, err, http.StatusNotFound, "not_found")
}

func TestOfflineIndexTarNames(t *testing.T) {
	// 不同写法的条目名（./ 前缀、缺少目录条目、`..` 逃逸）。
	ix, err := buildIndex(bytes.NewReader(makeTar(t,
		tarEntry{name: "./user/a/b.txt", typ: tar.TypeReg, body: "x"},
		tarEntry{name: "user/../../etc/passwd", typ: tar.TypeReg, body: "x"},
		tarEntry{name: "user/a/", typ: tar.TypeDir, mode: 0o700},
	)), nil)
	require.NoError(t, err)
	res, err := ix.list("~")
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)
	assert.Equal(t, 0o700, res.Entries[0].Mode, "目录条目晚于内容出现时更新属性并保留子项")
	res, err = ix.list("~/a")
	require.NoError(t, err)
	require.Len(t, res.Entries, 1)
	assert.Equal(t, "b.txt", res.Entries[0].Name)

	_, err = buildIndex(strings.NewReader(strings.Repeat("x", 1024)), nil)
	assert.Error(t, err, "损坏的 tar 流")
}

func TestOfflineParseRange(t *testing.T) {
	for _, c := range []struct {
		h                   string
		size, start, length int64
		partial, ok         bool
	}{
		{"", 10, 0, 10, false, true},
		{"bytes=0-4", 10, 0, 5, true, true},
		{"bytes=2-", 10, 2, 8, true, true},
		{"bytes=-3", 10, 7, 3, true, true},
		{"bytes=-30", 10, 0, 10, true, true},
		{"bytes=5-100", 10, 5, 5, true, true},
		{"bytes=9-9", 10, 9, 1, true, true},
		{"bytes=10-", 10, 0, 0, false, false},
		{"bytes=4-2", 10, 0, 0, false, false},
		{"bytes=-0", 10, 0, 0, false, false},
		{"bytes=0-1,3-4", 10, 0, 0, false, false},
		{"items=0-1", 10, 0, 0, false, false},
		{"bytes=a-b", 10, 0, 0, false, false},
		{"bytes=5", 10, 0, 0, false, false},
		{"bytes=0-", 0, 0, 0, false, false},
		{"bytes=-1", 0, 0, 0, false, false},
	} {
		start, length, partial, ok := parseRange(c.h, c.size)
		assert.Equal(t, c.ok, ok, c.h)
		if c.ok {
			assert.Equal(t, []any{c.start, c.length, c.partial}, []any{start, length, partial}, c.h)
		}
	}
}

func TestOfflineSniffContent(t *testing.T) {
	m, text := sniffContent("/home/user/a.json", []byte(`{"a":1}`), false)
	assert.Equal(t, "application/json", m)
	assert.True(t, text)
	m, text = sniffContent("/home/user/noext", []byte("\x00\x01\x02"), false)
	assert.Equal(t, "application/octet-stream", m)
	assert.False(t, text)
	// 读满前缀时允许末尾截断的码点。
	b := append(bytes.Repeat([]byte("a"), offlineSniffBytes-1), "中"[0])
	assert.True(t, isTextPrefix(b, true))
	assert.False(t, isTextPrefix(b, false))
}

func TestOfflineIndexCache(t *testing.T) {
	oldWait, oldTTL, oldSize := offlineIndexWait, offlineIndexErrTTL, offlineIndexCacheSize
	offlineIndexWait = 50 * time.Millisecond
	t.Cleanup(func() { offlineIndexWait, offlineIndexErrTTL, offlineIndexCacheSize = oldWait, oldTTL, oldSize })
	ctx := context.Background()
	var c indexCache

	// 单飞：并发请求只建一次；建好之前回 503 offline_indexing（带进度）。
	release := make(chan struct{})
	var calls atomic.Int32
	slow := func(_ context.Context, scanned *atomic.Int64) (*fsIndex, error) {
		calls.Add(1)
		scanned.Store(7)
		<-release
		return &fsIndex{root: newDirNode()}, nil
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			_, err := c.get(ctx, ctx, "orb-a", "k1", slow)
			assertFsErr(t, err, http.StatusServiceUnavailable, "offline_indexing")
			assert.Contains(t, err.Error(), "indexing: 7 entries scanned")
		})
	}
	wg.Wait()
	close(release)
	require.Eventually(t, func() bool { _, err := c.get(ctx, ctx, "orb-a", "k1", slow); return err == nil }, 5*time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, calls.Load())

	// 键变化（容器重启后又停机）→ 重建。
	fast := func(_ context.Context, _ *atomic.Int64) (*fsIndex, error) {
		calls.Add(1)
		return &fsIndex{root: newDirNode()}, nil
	}
	_, err := c.get(ctx, ctx, "orb-a", "k2", fast)
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load())

	// 失败缓存 offlineIndexErrTTL：期间不重建。
	failing := func(_ context.Context, _ *atomic.Int64) (*fsIndex, error) {
		calls.Add(1)
		return nil, errors.New("boom")
	}
	_, err = c.get(ctx, ctx, "orb-b", "k", failing)
	require.ErrorContains(t, err, "boom")
	_, err = c.get(ctx, ctx, "orb-b", "k", failing)
	require.ErrorContains(t, err, "boom")
	assert.EqualValues(t, 3, calls.Load())
	offlineIndexErrTTL = 0
	_, err = c.get(ctx, ctx, "orb-b", "k", failing)
	require.ErrorContains(t, err, "boom")
	assert.EqualValues(t, 4, calls.Load(), "失败过期后重建")

	// LRU：超过容量时淘汰最久未用的。
	offlineIndexCacheSize = 2
	_, err = c.get(ctx, ctx, "orb-a", "k2", fast) // orb-a 变为最近使用
	require.NoError(t, err)
	_, err = c.get(ctx, ctx, "orb-c", "k", fast)
	require.NoError(t, err)
	c.mu.Lock()
	_, hasB := c.builds["orb-b"]
	_, hasA := c.builds["orb-a"]
	n := len(c.builds)
	c.mu.Unlock()
	assert.False(t, hasB)
	assert.True(t, hasA)
	assert.Equal(t, 2, n)

	// 后台建立受 life 约束：life 结束 → 失败。
	life, cancel := context.WithCancel(ctx)
	blocked := func(bctx context.Context, _ *atomic.Int64) (*fsIndex, error) {
		<-bctx.Done()
		return nil, bctx.Err()
	}
	_, err = c.get(ctx, life, "orb-d", "k", blocked)
	assertFsErr(t, err, http.StatusServiceUnavailable, "offline_indexing")
	cancel()
	offlineIndexErrTTL = time.Hour
	require.Eventually(t, func() bool {
		_, err := c.get(ctx, ctx, "orb-d", "k", blocked)
		return errors.Is(err, context.Canceled)
	}, 5*time.Second, 10*time.Millisecond)
}

// fakeOfflineDocker 写一个假 docker：inspect 读 <dir>/inspect.<orb>，cp 输出 <dir>/cp/<路径，/ 换成 _>。
func fakeOfflineDocker(t *testing.T) (*Docker, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cp"), 0o755))
	script := `#!/bin/sh
D='` + dir + `'
echo "$*" >> "$D/log"
case "$1" in
container)
  for orb; do :; done
  if [ -f "$D/inspect.$orb" ]; then cat "$D/inspect.$orb"; exit 0; fi
  echo "Error: No such container: $orb" >&2; exit 1;;
cp)
  p="${2#*:}"
  if [ -f "$D/slow" ] && [ "$p" = /home/user ]; then while [ -f "$D/slow" ]; do sleep 0.05; done; fi
  f="$D/cp/$(printf %s "$p" | tr / _)"
  if [ -f "$f" ]; then cat "$f"; exit 0; fi
  echo "Error response from daemon: Could not find the file $p in container x" >&2; exit 1;;
esac
exit 1
`
	bin := filepath.Join(dir, "docker")
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	d := NewDocker(config.Docker{})
	d.Bin = bin
	return d, dir
}

func readReply(t *testing.T, rep FsReply) (int, http.Header, string) {
	t.Helper()
	require.NotNil(t, rep.Body)
	b, err := io.ReadAll(rep.Body)
	require.NoError(t, err)
	require.NoError(t, rep.Body.Close())
	if rep.Length >= 0 {
		assert.EqualValues(t, rep.Length, len(b), "Length 与应答体一致")
	}
	return rep.Status, rep.Header, string(b)
}

func TestOfflineFsDocker(t *testing.T) {
	d, dir := fakeOfflineDocker(t)
	write := func(name string, b []byte) { require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o644)) }
	write("inspect.orb-x", []byte("cid1 exited 2026-11-16T00:00:00Z\n"))
	write("inspect.orb-run", []byte("cid2 running 0001-01-01T00:00:00Z\n"))
	write("cp/_home_user", homeTar(t))
	write("cp/_home_user_b.txt", makeTar(t, tarEntry{name: "b.txt", typ: tar.TypeReg, body: "hello world"}))
	ctx := context.Background()
	do := func(orb string, op apigen.ProviderFsRequestOp, p, rng string, workdirs ...string) (int, http.Header, string) {
		req := apigen.ProviderFsRequest{ID: "r1", OrbID: orb, Op: op, Workdirs: workdirs}
		if p != "" {
			req.Path = &p
		}
		if rng != "" {
			req.Range = &rng
		}
		return readReply(t, d.OfflineFS(ctx, ctx, req))
	}
	cpHomeCount := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "log"))
		return strings.Count(string(b), "cp orb-x:/home/user -")
	}

	assert.True(t, *d.Info().Capabilities.OfflineFs)

	st, _, body := do("orb-run", apigen.Roots, "", "")
	assert.Equal(t, http.StatusConflict, st)
	assert.Contains(t, body, `"orb_not_stopped"`)
	st, _, body = do("orb-gone", apigen.Roots, "", "")
	assert.Equal(t, http.StatusNotFound, st)
	assert.Contains(t, body, `"not_found"`)

	st, h, body := do("orb-x", apigen.Roots, "", "", "/home/user/workspace/repo", "/home/user/zdir")
	require.Equal(t, http.StatusOK, st, body)
	assert.Equal(t, "application/json", h.Get("Content-Type"))
	var roots apigen.FsRoots
	require.NoError(t, json.Unmarshal([]byte(body), &roots))
	assert.Equal(t, apigen.FsRoots{Home: "/home/user", Workdir: "/home/user/zdir"}, roots)

	st, _, body = do("orb-x", apigen.List, "~", "")
	require.Equal(t, http.StatusOK, st, body)
	var listing apigen.FsListing
	require.NoError(t, json.Unmarshal([]byte(body), &listing))
	assert.Len(t, listing.Entries, 14)
	assert.Equal(t, 1, cpHomeCount(), "索引已缓存")

	st, _, body = do("orb-x", apigen.Stat, "~/abs-link", "")
	require.Equal(t, http.StatusOK, st, body)
	var e apigen.FsEntry
	require.NoError(t, json.Unmarshal([]byte(body), &e))
	assert.Equal(t, apigen.FsEntryTypeSymlink, e.Type)
	require.NotNil(t, e.Mime)
	assert.Equal(t, "text/plain; charset=utf-8", *e.Mime)
	require.NotNil(t, e.Text)
	assert.True(t, *e.Text)

	st, _, body = do("orb-x", apigen.Stat, "~/.oryn/orb-token", "")
	assert.Equal(t, http.StatusForbidden, st)
	assert.Contains(t, body, `"protected_path"`)

	st, h, body = do("orb-x", apigen.Read, "~/abs-link", "")
	require.Equal(t, http.StatusOK, st, body)
	assert.Equal(t, "hello world", body)
	assert.Equal(t, "11", h.Get("Content-Length"))
	assert.Equal(t, "bytes", h.Get("Accept-Ranges"))
	assert.Equal(t, `"1793520000123456789-11"`, h.Get("ETag"))
	assert.Empty(t, h.Get("Content-Range"))

	st, h, body = do("orb-x", apigen.Read, "~/b.txt", "bytes=6-")
	require.Equal(t, http.StatusPartialContent, st, body)
	assert.Equal(t, "world", body)
	assert.Equal(t, "bytes 6-10/11", h.Get("Content-Range"))
	assert.Equal(t, "5", h.Get("Content-Length"))

	st, _, body = do("orb-x", apigen.Read, "~/b.txt", "bytes=-3")
	require.Equal(t, http.StatusPartialContent, st)
	assert.Equal(t, "rld", body)

	st, h, body = do("orb-x", apigen.Read, "~/b.txt", "bytes=20-")
	assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, st)
	assert.Equal(t, "bytes */11", h.Get("Content-Range"))
	assert.Contains(t, body, `"range_not_satisfiable"`)

	st, _, body = do("orb-x", apigen.Read, "~/zdir", "")
	assert.Equal(t, http.StatusConflict, st)
	assert.Contains(t, body, `"is_a_directory"`)

	// 索引里有、docker cp 却找不到 → 404。
	st, _, body = do("orb-x", apigen.Read, "~/A.txt", "")
	assert.Equal(t, http.StatusNotFound, st, body)
	assert.Equal(t, 1, cpHomeCount())

	// 容器重新停机（FinishedAt 变化）→ 重建索引；建立期间 503 offline_indexing + Retry-After。
	oldWait := offlineIndexWait
	offlineIndexWait = 50 * time.Millisecond
	t.Cleanup(func() { offlineIndexWait = oldWait })
	write("slow", nil)
	write("inspect.orb-x", []byte("cid1 exited 2026-11-16T01:00:00Z\n"))
	st, h, body = do("orb-x", apigen.List, "~", "")
	assert.Equal(t, http.StatusServiceUnavailable, st)
	assert.Equal(t, "2", h.Get("Retry-After"))
	assert.Contains(t, body, `"offline_indexing"`)
	require.NoError(t, os.Remove(filepath.Join(dir, "slow")))
	require.Eventually(t, func() bool { st, _, _ := do("orb-x", apigen.List, "~", ""); return st == http.StatusOK }, 10*time.Second, 20*time.Millisecond)
	assert.Equal(t, 2, cpHomeCount())
}

// fakeFsDriver 只用于 runner 的只读文件 worker 测试。
type fakeFsDriver struct {
	recDriver
	closed atomic.Int32
}

type closeCounter struct {
	io.Reader
	n *atomic.Int32
}

func (c closeCounter) Close() error { c.n.Add(1); return nil }

func (d *fakeFsDriver) OfflineFS(_ context.Context, _ context.Context, req apigen.ProviderFsRequest) FsReply {
	if req.Op == apigen.Read {
		h := http.Header{}
		h.Set("Content-Type", "application/octet-stream")
		h.Set("ETag", `"1-9"`)
		// 长度未知 → chunked。
		return FsReply{Status: http.StatusPartialContent, Header: h, Body: closeCounter{strings.NewReader("streaming"), &d.closed}, Length: -1}
	}
	return jsonReply(http.StatusOK, apigen.FsRoots{Home: OrbHome, Workdir: OrbHome})
}

func TestRunnerOfflineFsWorker(t *testing.T) {
	type reply struct {
		path, status, auth, etag string
		length                   int64
		chunked                  bool
		body                     string
	}
	var (
		mu     sync.Mutex
		served int
	)
	replies := make(chan reply, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/provider/v1/hello":
			_ = json.NewEncoder(w).Encode(apigen.ProviderHelloResponse{ProviderID: "prov_x", LeaseSeconds: 45})
		case req.URL.Path == "/provider/v1/fs/claim":
			var c apigen.ProviderFsClaimRequest
			assert.NoError(t, json.NewDecoder(req.Body).Decode(&c))
			assert.NotEmpty(t, c.InstanceID)
			mu.Lock()
			n := served
			served++
			mu.Unlock()
			switch n {
			case 0:
				_ = json.NewEncoder(w).Encode(apigen.ProviderFsClaimResponse{Request: apigen.ProviderFsRequest{ID: "fs_1", OrbID: "orb-a", Op: apigen.Roots}})
			case 1:
				_ = json.NewEncoder(w).Encode(apigen.ProviderFsClaimResponse{Request: apigen.ProviderFsRequest{ID: "fs_2", OrbID: "orb-a", Op: apigen.Read}})
			default:
				<-req.Context().Done()
			}
		case strings.HasPrefix(req.URL.Path, "/provider/v1/fs/"):
			b, _ := io.ReadAll(req.Body)
			replies <- reply{
				path: req.URL.Path, status: req.Header.Get("X-Oryn-Status"), auth: req.Header.Get("Authorization"), etag: req.Header.Get("ETag"),
				length: req.ContentLength, chunked: len(req.TransferEncoding) > 0 && req.TransferEncoding[0] == "chunked", body: string(b),
			}
			w.WriteHeader(http.StatusNoContent)
		case req.URL.Path == "/provider/v1/jobs/claim":
			// 读完请求体，服务端才会感知客户端断开并取消请求上下文。
			_, _ = io.Copy(io.Discard, req.Body)
			<-req.Context().Done()
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)

	drv := &fakeFsDriver{}
	r := &Runner{URL: srv.URL, Token: "opv_x", Driver: drv, Version: "t", Concurrency: 1, ClaimWait: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	got := map[string]reply{}
	for range 2 {
		select {
		case rp := <-replies:
			got[rp.path] = rp
		case <-time.After(10 * time.Second):
			t.Fatal("no fs reply")
		}
	}
	cancel()
	require.NoError(t, <-done)

	roots := got["/provider/v1/fs/fs_1/reply"]
	assert.Equal(t, "200", roots.status)
	assert.Equal(t, "Bearer opv_x", roots.auth)
	assert.False(t, roots.chunked)
	assert.EqualValues(t, len(roots.body), roots.length, "长度已知时带 Content-Length")
	assert.JSONEq(t, `{"home":"/home/user","workdir":"/home/user"}`, roots.body)

	read := got["/provider/v1/fs/fs_2/reply"]
	assert.Equal(t, "206", read.status)
	assert.Equal(t, `"1-9"`, read.etag, "应答头原样转交")
	assert.True(t, read.chunked, "长度未知时 chunked")
	assert.Equal(t, "streaming", read.body)
	assert.Positive(t, drv.closed.Load(), "应答体回传后关闭")
}

func TestRunnerNoFsWorkersWithoutOfflineFS(t *testing.T) {
	var fsClaims atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/provider/v1/hello":
			_ = json.NewEncoder(w).Encode(apigen.ProviderHelloResponse{ProviderID: "prov_x", LeaseSeconds: 45})
		case "/provider/v1/fs/claim":
			fsClaims.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "/provider/v1/jobs/claim":
			_, _ = io.Copy(io.Discard, req.Body)
			<-req.Context().Done()
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	r := &Runner{URL: srv.URL, Token: "opv_x", Driver: &recDriver{}, Version: "t", Concurrency: 1, ClaimWait: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, r.Run(ctx))
	assert.Zero(t, fsClaims.Load())
}
