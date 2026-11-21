package repos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lfsCachePut(t *testing.T, c *lfsCache, pid, oid, content string) {
	t.Helper()
	p := c.path(pid, oid)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	c.reserve(int64(len(content)))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	c.add(pid, oid, int64(len(content)))
}

func TestLFSCacheEvictsLeastRecentlyUsed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".lfs-cache")
	c, err := openLFSCache(dir, 10)
	require.NoError(t, err)
	a, b, d := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("d", 64)

	lfsCachePut(t, c, "prj_A", a, "aaaa")
	lfsCachePut(t, c, "prj_B", b, "bbbb")
	_, ok := c.get("prj_A", a)
	require.True(t, ok)
	lfsCachePut(t, c, "prj_A", d, "dddd")

	_, ok = c.get("prj_B", b)
	assert.False(t, ok, "最久未用的 b 被淘汰")
	assert.NoFileExists(t, c.path("prj_B", b))
	for _, oid := range []string{a, d} {
		p, ok := c.get("prj_A", oid)
		assert.True(t, ok, oid)
		assert.FileExists(t, p)
	}
	assert.Equal(t, int64(8), c.total)

	// 残留：项目目录下的 tmp-*、*.trash-* 目录、名字不合法的文件
	tmp := filepath.Join(dir, "prj_A", "tmp-x")
	require.NoError(t, os.WriteFile(tmp, []byte("half"), 0o600))
	trash := filepath.Join(dir, "prj_C.trash-1")
	require.NoError(t, os.MkdirAll(filepath.Join(trash, "zz"), 0o700))
	stray := filepath.Join(dir, "prj_A", "notes.txt")
	require.NoError(t, os.WriteFile(stray, []byte("xx"), 0o600))

	re, err := openLFSCache(dir, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(8), re.total, "重启后按磁盘恢复")
	assert.Equal(t, 2, re.lru.Len())
	assert.NoFileExists(t, tmp)
	assert.NoDirExists(t, trash)
	assert.FileExists(t, stray, "不认识的文件不动也不计入")

	// 上限变小：重启时按 mtime 淘汰较旧的
	small, err := openLFSCache(dir, 4)
	require.NoError(t, err)
	assert.Equal(t, int64(4), small.total)
	assert.Equal(t, 1, small.lru.Len())
}

func TestLFSCacheDropAndAddAgain(t *testing.T) {
	c, err := openLFSCache(t.TempDir(), 100)
	require.NoError(t, err)
	a := strings.Repeat("a", 64)
	lfsCachePut(t, c, "prj_A", a, "aaaa")
	require.NoError(t, os.Remove(c.path("prj_A", a)))
	c.drop("prj_A", a)
	_, ok := c.get("prj_A", a)
	assert.False(t, ok)
	assert.Equal(t, int64(0), c.total)

	lfsCachePut(t, c, "prj_A", a, "aaaa")
	c.add("prj_A", a, 6) // 重复登记只更新大小
	assert.Equal(t, int64(6), c.total)
	assert.Equal(t, 1, c.lru.Len())
}

func TestLFSCacheRemoveProjectAndPrune(t *testing.T) {
	dir := t.TempDir()
	c, err := openLFSCache(dir, 100)
	require.NoError(t, err)
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	lfsCachePut(t, c, "prj_A", a, "aaaa")
	lfsCachePut(t, c, "prj_B", b, "bbbb")
	lfsCachePut(t, c, "prj_C", a, "cccc")

	c.removeProject("prj_A")
	assert.NoDirExists(t, filepath.Join(dir, "prj_A"))
	_, ok := c.get("prj_A", a)
	assert.False(t, ok)
	assert.Equal(t, int64(8), c.total)
	c.removeProject("prj_nope") // 没有目录也不出错

	c.prune(func(pid string) bool { return pid == "prj_B" })
	c.bg.Wait()
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"prj_B"}, names, "被删项目的目录与 trash 都已清掉")
	_, ok = c.get("prj_B", b)
	assert.True(t, ok)
	assert.Equal(t, int64(4), c.total)
}

// Service 接入：缺省上限、Remove 与启动清扫一并清掉 LFS 缓存。
func TestLFSCacheServiceWiring(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	require.NotNil(t, e.svc.lfsCache)
	assert.Equal(t, int64(2<<30), e.svc.cfg.MaxLFSCacheBytes)
	assert.Equal(t, filepath.Join(e.svc.cfg.Dir, ".lfs-cache"), e.svc.lfsCache.dir)

	keep, gone := e.project(t, ""), e.project(t, "")
	oid := strings.Repeat("a", 64)
	for _, pid := range []string{keep, gone, "prj_01JCZ00000000000000GHOST"} {
		lfsCachePut(t, e.svc.lfsCache, pid, oid, "aaaa")
	}

	e.svc.Remove(gone)
	assert.NoDirExists(t, filepath.Join(e.svc.lfsCache.dir, gone))

	e.svc.sweep(context.Background())
	e.svc.lfsCache.bg.Wait()
	assert.NoDirExists(t, filepath.Join(e.svc.lfsCache.dir, "prj_01JCZ00000000000000GHOST"), "项目已不存在的缓存被清掉")
	_, ok := e.svc.lfsCache.get(keep, oid)
	assert.True(t, ok)
	assert.FileExists(t, e.svc.lfsCache.path(keep, oid))
}
