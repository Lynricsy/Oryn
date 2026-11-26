package orbdrt

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

type tarItem struct {
	typ  byte
	mode int64
	body string
	link string
}

func readTarItems(t *testing.T, b []byte) (map[string]tarItem, []string) {
	t.Helper()
	items := map[string]tarItem{}
	var order []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(tr)
		require.NoError(t, err)
		items[hdr.Name] = tarItem{typ: hdr.Typeflag, mode: hdr.Mode, body: string(body), link: hdr.Linkname}
		order = append(order, hdr.Name)
	}
	return items, order
}

func tarGet(o *orbHTTP, p string, q url.Values) (*http.Response, []byte) {
	o.t.Helper()
	if q == nil {
		q = url.Values{}
	}
	q.Set("path", p)
	return o.do(http.MethodGet, "/fs/tar", q, nil, nil)
}

func tarPut(o *orbHTTP, p, kind string, body []byte) (*http.Response, []byte) {
	o.t.Helper()
	return o.do(http.MethodPut, "/fs/tar", url.Values{"path": {p}, "kind": {kind}}, bytes.NewReader(body), nil)
}

func tarPutOK(o *orbHTTP, p, kind string, body []byte) apigen.OrbFsTarResult {
	o.t.Helper()
	resp, b := tarPut(o, p, kind, body)
	require.Equal(o.t, http.StatusOK, resp.StatusCode, string(b))
	var res apigen.OrbFsTarResult
	require.NoError(o.t, json.Unmarshal(b, &res))
	return res
}

func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	}))
	sort.Strings(out)
	return out
}

// 目录往返：嵌套、可执行位、内部符号链接保留，指向根外的符号链接被跳过；内容合并进已存在的目标目录。
func TestFsTarDirRoundTrip(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	src := filepath.Join(rt.home, "src")
	writeTree(t, src, map[string]string{"a.txt": "A", "bin/run.sh": "#!/bin/sh\n", "sub/deep/x.txt": "X"})
	require.NoError(t, os.Chmod(filepath.Join(src, "bin/run.sh"), 0o755))
	require.NoError(t, os.Symlink("a.txt", filepath.Join(src, "link-in")))
	require.NoError(t, os.Symlink("../../etc/passwd", filepath.Join(src, "link-out")))
	require.NoError(t, os.Symlink("/etc/hosts", filepath.Join(src, "link-abs")))

	resp, b := tarGet(o, "~/src", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, "dir", resp.Header.Get("X-Oryn-Tar-Kind"))
	assert.Equal(t, "application/x-tar", resp.Header.Get("Content-Type"))
	items, order := readTarItems(t, b)
	assert.Equal(t, tarItem{typ: tar.TypeReg, mode: 0o755, body: "#!/bin/sh\n"}, items["bin/run.sh"])
	assert.Equal(t, tarItem{typ: tar.TypeSymlink, mode: 0o777, link: "a.txt"}, items["link-in"])
	assert.Equal(t, byte(tar.TypeDir), items["sub/deep/"].typ)
	assert.NotContains(t, items, "./")
	assert.Less(t, indexOf(order, "sub/"), indexOf(order, "sub/deep/x.txt"), "父目录先于子项")

	dst := filepath.Join(rt.home, "dst")
	writeTree(t, dst, map[string]string{"keep.txt": "K", "a.txt": "old"})
	res := tarPutOK(o, "~/dst", "dir", b)
	assert.Equal(t, apigen.OrbFsTarResult{Files: 3, Bytes: 1 + 10 + 1, Skipped: 2}, res)
	assert.Equal(t, []string{"a.txt", "bin/run.sh", "keep.txt", "link-in", "sub/deep/x.txt"}, treeFiles(t, dst))
	data, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "A", string(data), "同名文件被覆盖")
	st, err := os.Stat(filepath.Join(dst, "bin/run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), st.Mode().Perm())
	target, err := os.Readlink(filepath.Join(dst, "link-in"))
	require.NoError(t, err)
	assert.Equal(t, "a.txt", target)
	assert.NoFileExists(t, filepath.Join(dst, "link-out"))

	// 新目标（父目录自动创建）
	res = tarPutOK(o, "~/new/deeper/copy", "dir", b)
	assert.Equal(t, 3, res.Files)
	assert.FileExists(t, filepath.Join(rt.home, "new/deeper/copy/sub/deep/x.txt"))

	// 目标是文件 → 409
	resp, b2 := tarPut(o, "~/dst/a.txt", "dir", b)
	assert.Equal(t, "not_a_directory", o.errCode(resp, b2, http.StatusConflict))

	// 非法条目名 → 400，不写出根之外
	var evil bytes.Buffer
	tw := tar.NewWriter(&evil)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}))
	_, _ = tw.Write([]byte("e"))
	require.NoError(t, tw.Close())
	resp, b2 = tarPut(o, "~/dst", "dir", evil.Bytes())
	assert.Equal(t, "invalid_request", o.errCode(resp, b2, http.StatusBadRequest))
	assert.NoFileExists(t, filepath.Join(rt.home, "evil.txt"))
}

func TestFsTarFileRoundTrip(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	writeTree(t, rt.home, map[string]string{"dir/f.sh": "echo hi\n"})
	require.NoError(t, os.Chmod(filepath.Join(rt.home, "dir/f.sh"), 0o700))

	resp, b := tarGet(o, "dir/f.sh", nil) // 相对路径以 $HOME 为根
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, "file", resp.Header.Get("X-Oryn-Tar-Kind"))
	items, _ := readTarItems(t, b)
	assert.Equal(t, map[string]tarItem{"f.sh": {typ: tar.TypeReg, mode: 0o700, body: "echo hi\n"}}, items)

	res := tarPutOK(o, "~/out/new/name.sh", "file", b)
	assert.Equal(t, apigen.OrbFsTarResult{Files: 1, Bytes: 8}, res)
	data, err := os.ReadFile(filepath.Join(rt.home, "out/new/name.sh"))
	require.NoError(t, err)
	assert.Equal(t, "echo hi\n", string(data))
	st, err := os.Stat(filepath.Join(rt.home, "out/new/name.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), st.Mode().Perm())
	assert.Empty(t, tmpResidue(t, rt))

	// 覆盖已存在的文件
	writeTree(t, rt.home, map[string]string{"out/old.txt": "old"})
	tarPutOK(o, "~/out/old.txt", "file", b)
	data, err = os.ReadFile(filepath.Join(rt.home, "out/old.txt"))
	require.NoError(t, err)
	assert.Equal(t, "echo hi\n", string(data))

	resp, b2 := tarPut(o, "~/out", "file", b)
	assert.Equal(t, "is_a_directory", o.errCode(resp, b2, http.StatusConflict))
	resp, b2 = tarGet(o, "~/dir/f.sh", url.Values{"gitignore": {"1"}})
	assert.Equal(t, "invalid_request", o.errCode(resp, b2, http.StatusBadRequest))
	resp, b2 = tarPut(o, "~/x", "zip", b)
	assert.Equal(t, "invalid_request", o.errCode(resp, b2, http.StatusBadRequest))
}

func TestFsTarExclude(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	writeTree(t, filepath.Join(rt.home, "p"), map[string]string{
		"keep.txt": "k", "a.log": "l", "sub/b.log": "l", "sub/keep.go": "g", "sub/inner/c.txt": "c",
		"node_modules/m.js": "m", "sub/node_modules/n.js": "n",
	})
	resp, b := tarGet(o, "~/p", url.Values{"exclude": {"*.log", "node_modules", "sub/inner"}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	items, _ := readTarItems(t, b)
	names := make([]string, 0, len(items))
	for n := range items {
		names = append(names, n)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"keep.txt", "sub/", "sub/keep.go"}, names)

	resp, b = tarGet(o, "~/p", url.Values{"exclude": {"[bad"}})
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))
}

func TestFsTarGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	repo := filepath.Join(rt.home, "repo")
	writeTree(t, repo, map[string]string{
		".gitignore": "ignored.txt\nbuild/\n", "tracked.txt": "t", "ignored.txt": "i", "build/out.bin": "b",
		"untracked.txt": "u", "dir/nested.txt": "n", "skip.log": "s",
	})
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "add", ".gitignore", "tracked.txt")

	resp, b := tarGet(o, "~/repo", url.Values{"gitignore": {"1"}, "exclude": {"*.log"}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	items, _ := readTarItems(t, b)
	names := make([]string, 0, len(items))
	for n := range items {
		names = append(names, n)
	}
	sort.Strings(names)
	assert.Equal(t, []string{".gitignore", "dir/", "dir/nested.txt", "tracked.txt", "untracked.txt"}, names)

	// 子目录同样只列它自己的文件
	resp, b = tarGet(o, "~/repo/dir", url.Values{"gitignore": {"1"}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	items, _ = readTarItems(t, b)
	assert.Equal(t, []string{"nested.txt"}, mapKeys(items))

	writeTree(t, rt.home, map[string]string{"plain/a.txt": "a"})
	resp, b = tarGet(o, "~/plain", url.Values{"gitignore": {"1"}})
	assert.Equal(t, "not_a_git_worktree", o.errCode(resp, b, http.StatusBadRequest))
}

func mapKeys(m map[string]tarItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestFsTarPathPolicy(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	var empty bytes.Buffer
	require.NoError(t, tar.NewWriter(&empty).Close())

	resp, b := tarGet(o, "~/.oryn/threads", nil)
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden))
	resp, b = tarPut(o, "~/.oryn/x", "dir", empty.Bytes())
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden))
	resp, b = tarGet(o, "~", nil)
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden))
	resp, b = tarPut(o, "~", "dir", empty.Bytes())
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden))
	resp, b = tarGet(o, "/etc", nil)
	assert.Equal(t, "outside_home", o.errCode(resp, b, http.StatusForbidden))
	resp, b = tarPut(o, "~/../escape", "dir", empty.Bytes())
	assert.Equal(t, "outside_home", o.errCode(resp, b, http.StatusForbidden))
	require.NoError(t, os.Symlink(filepath.Join(rt.home, ".oryn"), filepath.Join(rt.home, "sneaky")))
	resp, b = tarPut(o, "~/sneaky", "dir", empty.Bytes())
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden))
	resp, b = tarGet(o, "~/missing", nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))
}

func TestFsTarTooLarge(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	rt.maxTarBytes = 4 << 10
	o := newOrbHTTP(t, cp, rt)
	var big bytes.Buffer
	tw := tar.NewWriter(&big)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "big.bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: 16 << 10}))
	_, _ = tw.Write(make([]byte, 16<<10))
	require.NoError(t, tw.Close())
	for _, kind := range []string{"file", "dir"} {
		resp, b := tarPut(o, "~/big-"+kind, kind, big.Bytes())
		assert.Equal(t, "transfer_too_large", o.errCode(resp, b, http.StatusRequestEntityTooLarge), kind)
	}
	assert.Empty(t, tmpResidue(t, rt))
}

// runner 没有目录传输。
func TestFsTarNotOnRunner(t *testing.T) {
	rt := &Runtime{runner: &runnerState{}}
	req, _ := http.NewRequest(http.MethodGet, "/fs/tar?path=~/x", nil)
	rec := serveLocal(rt, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
