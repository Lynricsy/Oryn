package sshgw

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
)

func TestSFTPWriteRead(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")

	e1, err := e.svc.FsWrite(ctx, e.host.ID, p, true, "", strings.NewReader("hello world"))
	require.NoError(t, err)
	require.Equal(t, p, e1.Path)
	require.Equal(t, int64(11), e1.Size)
	require.NotNil(t, e1.Etag)
	require.Equal(t, 0o644, e1.Mode)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "hello world", string(b))

	// 读回，并按 Range 的方式 Seek 读一段
	f, err := e.svc.FsOpen(ctx, e.host.ID, p)
	require.NoError(t, err)
	require.Equal(t, *e1.Etag, f.Etag())
	_, err = f.Seek(6, io.SeekStart)
	require.NoError(t, err)
	part := make([]byte, 5)
	_, err = io.ReadFull(f, part)
	require.NoError(t, err)
	require.Equal(t, "world", string(part))
	require.NoError(t, f.Close())

	// If-Match 不符 → modified；相符则覆盖并沿用权限
	require.NoError(t, os.Chmod(p, 0o600))
	_, err = e.svc.FsWrite(ctx, e.host.ID, p, true, `"1-1"`, strings.NewReader("x"))
	requireCode(t, err, "modified", "")
	st, err := e.svc.FsStat(ctx, e.host.ID, p)
	require.NoError(t, err)
	require.True(t, *st.Text)
	require.Equal(t, "text/plain; charset=utf-8", *st.Mime)
	e2, err := e.svc.FsWrite(ctx, e.host.ID, p, true, *st.Etag, strings.NewReader("second"))
	require.NoError(t, err)
	require.Equal(t, 0o600, e2.Mode)
	b, _ = os.ReadFile(p)
	require.Equal(t, "second", string(b))

	// overwrite=false 且已存在 → exists；新文件可以
	_, err = e.svc.FsWrite(ctx, e.host.ID, p, false, "", strings.NewReader("x"))
	requireCode(t, err, "exists", "")
	_, err = e.svc.FsWrite(ctx, e.host.ID, filepath.Join(dir, "b.bin"), false, "", bytes.NewReader([]byte{0, 1, 2}))
	require.NoError(t, err)
	// 目录 / 父目录不存在
	_, err = e.svc.FsWrite(ctx, e.host.ID, dir, true, "", strings.NewReader("x"))
	requireCode(t, err, "is_a_directory", "")
	_, err = e.svc.FsWrite(ctx, e.host.ID, filepath.Join(dir, "nope", "x"), true, "", strings.NewReader("x"))
	requireCode(t, err, "not_found", "")
	_, err = e.svc.FsOpen(ctx, e.host.ID, dir)
	requireCode(t, err, "is_a_directory", "")
	_, err = e.svc.FsList(ctx, e.host.ID, "relative/path")
	requireCode(t, err, "invalid_request", "")

	// 没有残留临时文件
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, de := range ents {
		require.NotContains(t, de.Name(), fsTmpInfix)
	}

	rows := e.auditRows(t)
	require.Equal(t, "sftp", string(rows[0].Kind))
	require.Contains(t, rows[0].Detail, "get "+dir)
}

func TestSFTPListMkdirRenameDelete(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "A.txt"), []byte("a"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "zdir"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(dir, "zdir"), filepath.Join(dir, "link")))

	m, err := e.svc.FsMkdir(ctx, e.host.ID, filepath.Join(dir, "x", "y"))
	require.NoError(t, err)
	require.Equal(t, "dir", string(m.Type))

	l, err := e.svc.FsList(ctx, e.host.ID, dir)
	require.NoError(t, err)
	var names []string
	for _, en := range l.Entries {
		names = append(names, en.Name)
	}
	require.Equal(t, []string{"link", "x", "zdir", "A.txt", "b.txt"}, names, "目录（含指向目录的链接）在前、不分大小写排序")
	require.Equal(t, "symlink", string(l.Entries[0].Type))
	require.Equal(t, filepath.Join(dir, "zdir"), *l.Entries[0].Target)
	_, err = e.svc.FsList(ctx, e.host.ID, filepath.Join(dir, "b.txt"))
	requireCode(t, err, "not_a_directory", "")

	_, err = e.svc.FsRename(ctx, e.host.ID, filepath.Join(dir, "A.txt"), filepath.Join(dir, "b.txt"), false)
	requireCode(t, err, "exists", "")
	r, err := e.svc.FsRename(ctx, e.host.ID, filepath.Join(dir, "A.txt"), filepath.Join(dir, "b.txt"), true)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "b.txt"), r.Path)
	b, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	require.Equal(t, "a", string(b))

	// 删除指向目录的链接只删链接本身
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zdir", "keep"), []byte("k"), 0o644))
	require.NoError(t, e.svc.FsDelete(ctx, e.host.ID, filepath.Join(dir, "link")))
	_, err = os.Stat(filepath.Join(dir, "zdir", "keep"))
	require.NoError(t, err)
	require.NoError(t, e.svc.FsDelete(ctx, e.host.ID, filepath.Join(dir, "x")))
	_, err = os.Stat(filepath.Join(dir, "x"))
	require.True(t, os.IsNotExist(err))
	requireCode(t, e.svc.FsDelete(ctx, e.host.ID, filepath.Join(dir, "x")), "not_found", "")

	// / 与主目录受保护
	roots, err := e.svc.FsRoots(ctx, e.host.ID)
	require.NoError(t, err)
	require.NotEmpty(t, roots.Home)
	requireCode(t, e.svc.FsDelete(ctx, e.host.ID, "/"), "protected_path", "")
	requireCode(t, e.svc.FsDelete(ctx, e.host.ID, roots.Home+"/"), "protected_path", "")
}

func TestSFTPConnCacheInvalidateAndReap(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	ctx := context.Background()

	_, err := e.svc.FsRoots(ctx, e.host.ID)
	require.NoError(t, err)
	e.svc.fsMu.Lock()
	first := e.svc.fsConns[e.host.ID]
	e.svc.fsMu.Unlock()
	require.NotNil(t, first)
	_, err = e.svc.FsRoots(ctx, e.host.ID)
	require.NoError(t, err)
	e.svc.fsMu.Lock()
	require.Same(t, first, e.svc.fsConns[e.host.ID], "复用缓存的连接")
	e.svc.fsMu.Unlock()

	// 主机被改：缓存的连接被关掉，下次重新拨号
	_, err = e.svc.ResetHostKey(ctx, e.host.ID)
	require.NoError(t, err)
	e.svc.fsMu.Lock()
	require.Nil(t, e.svc.fsConns[e.host.ID])
	e.svc.fsMu.Unlock()
	_, err = e.svc.FsRoots(ctx, e.host.ID)
	require.NoError(t, err)

	// 闲置超时被回收
	e.svc.fsMu.Lock()
	fc := e.svc.fsConns[e.host.ID]
	require.NotNil(t, fc)
	require.NotSame(t, first, fc)
	fc.last = fc.last.Add(-fsIdleTTL - 1)
	e.svc.fsMu.Unlock()
	e.svc.reapFs()
	e.svc.fsMu.Lock()
	require.Nil(t, e.svc.fsConns[e.host.ID])
	e.svc.fsMu.Unlock()

	// 连不上 → ssh_unreachable
	e.target.Close()
	_, err = e.svc.FsRoots(ctx, e.host.ID)
	requireCode(t, err, "ssh_unreachable", "连不上")
}
