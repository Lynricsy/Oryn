package orbdrt

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// orbHTTP 经真实 link 打开 fs 流访问 orbd 本机 API（与控制面 orbs.Service.Transport 同样的方式）。
type orbHTTP struct {
	t  *testing.T
	c  *http.Client
	rt *Runtime
}

func newOrbHTTP(t *testing.T, cp *fakeCP, rt *Runtime) *orbHTTP {
	t.Helper()
	cp.ctlNow(t)
	tr := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			cp.mu.Lock()
			sess := cp.sess
			cp.mu.Unlock()
			return sess.Open(ctx, link.StreamHeader{T: link.StreamFS})
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &orbHTTP{t: t, c: &http.Client{Transport: tr, Timeout: 60 * time.Second}, rt: rt}
}

// do 发出请求并读完响应体。
func (o *orbHTTP) do(method, path string, q url.Values, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	o.t.Helper()
	u := "http://orb" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(method, u, body)
	require.NoError(o.t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := o.c.Do(req)
	require.NoError(o.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(o.t, err)
	return resp, b
}

func (o *orbHTTP) json(method, path string, q url.Values, body any, want int, out any) {
	o.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(o.t, err)
		rd = bytes.NewReader(b)
	}
	resp, b := o.do(method, path, q, rd, nil)
	require.Equal(o.t, want, resp.StatusCode, "%s %s: %s", method, path, b)
	if out != nil {
		require.NoError(o.t, json.Unmarshal(b, out), string(b))
	}
}

// errCode 断言状态码并返回错误码。
func (o *orbHTTP) errCode(resp *http.Response, b []byte, status int) string {
	o.t.Helper()
	require.Equal(o.t, status, resp.StatusCode, string(b))
	var e apigen.Error
	require.NoError(o.t, json.Unmarshal(b, &e), string(b))
	return e.Code
}

func (o *orbHTTP) put(path string, body []byte, q url.Values, hdr map[string]string) (*http.Response, []byte) {
	o.t.Helper()
	if q == nil {
		q = url.Values{}
	}
	q.Set("path", path)
	return o.do(http.MethodPut, "/fs/file", q, bytes.NewReader(body), hdr)
}

func pathQ(p string) url.Values { return url.Values{"path": {p}} }

func setWorkdir(rt *Runtime, dir string) {
	rt.envMu.Lock()
	rt.workdir = dir
	rt.envMu.Unlock()
}

func tmpResidue(t *testing.T, rt *Runtime) []string {
	t.Helper()
	des, err := os.ReadDir(rt.tmpDir())
	require.NoError(t, err)
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

func TestHelloAdvertisesFeatures(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	_, raw := cp.waitNotif(t, 0, "hello", nil)
	var h apigen.LinkCtlHelloParams
	require.NoError(t, json.Unmarshal(raw, &h))
	assert.Equal(t, []string{"fs", "git", "attachments", "services", "portal"}, h.Features)
}

func TestLocalFSRoundTrip(t *testing.T) {
	cp := newFakeCP(t)
	var seen []http.Header
	var mu sync.Mutex
	rt := startRuntime(t, cp, func(c *Config) {
		c.OnLocalRequest = func(r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Header.Clone())
			mu.Unlock()
		}
	})
	o := newOrbHTTP(t, cp, rt)
	ws := filepath.Join(rt.home, "workspace")
	require.NoError(t, os.MkdirAll(ws, 0o755))

	var roots apigen.FsRoots
	o.json(http.MethodGet, "/fs/roots", nil, nil, http.StatusOK, &roots)
	assert.Equal(t, apigen.FsRoots{Home: rt.home, Workdir: ws}, roots)

	var dir apigen.FsEntry
	o.json(http.MethodPost, "/fs/mkdir", nil, apigen.FsPathRequest{Path: "~/workspace/a/b"}, http.StatusOK, &dir)
	assert.Equal(t, filepath.Join(ws, "a", "b"), dir.Path)
	assert.Equal(t, apigen.FsEntryTypeDir, dir.Type)
	assert.Nil(t, dir.Etag, "目录没有 etag")

	content := []byte("hello\nworld\n")
	resp, b := o.put("~/workspace/a/file.txt", content, nil, map[string]string{"Cookie": "__Host-oryn_session=x"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	var written apigen.FsEntry
	require.NoError(t, json.Unmarshal(b, &written))
	assert.Equal(t, filepath.Join(ws, "a", "file.txt"), written.Path)
	assert.Equal(t, apigen.FsEntryTypeFile, written.Type)
	assert.EqualValues(t, len(content), written.Size)
	assert.Equal(t, 0o644, written.Mode, "新文件 0644")
	require.NotNil(t, written.Etag)
	assert.Empty(t, tmpResidue(t, rt))

	// 读回：字节一致、ETag 与写入结果相同；绝对路径同样可用。
	resp, b = o.do(http.MethodGet, "/fs/file", pathQ(filepath.Join(ws, "a", "file.txt")), nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, content, b)
	assert.Equal(t, *written.Etag, resp.Header.Get("ETag"))

	// Range。
	resp, b = o.do(http.MethodGet, "/fs/file", pathQ("~/workspace/a/file.txt"), nil, map[string]string{"Range": "bytes=6-10"})
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, "world", string(b))
	assert.Equal(t, "bytes 6-10/12", resp.Header.Get("Content-Range"))

	// 目录不能当文件读。
	resp, b = o.do(http.MethodGet, "/fs/file", pathQ("~/workspace/a"), nil, nil)
	assert.Equal(t, "is_a_directory", o.errCode(resp, b, http.StatusConflict))

	var st apigen.FsEntry
	o.json(http.MethodGet, "/fs/stat", pathQ("~/workspace/a/file.txt"), nil, http.StatusOK, &st)
	require.NotNil(t, st.Mime)
	assert.True(t, strings.HasPrefix(*st.Mime, "text/plain"), *st.Mime)
	require.NotNil(t, st.Text)
	assert.True(t, *st.Text)

	// 列表：目录在前、按名不分大小写。
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a", "Zeta.txt"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a", "alpha.txt"), nil, 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(ws, "a", "Z-dir"), 0o755))
	var ls apigen.FsListing
	o.json(http.MethodGet, "/fs/list", pathQ("~/workspace/a"), nil, http.StatusOK, &ls)
	var names []string
	for _, e := range ls.Entries {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"b", "Z-dir", "alpha.txt", "file.txt", "Zeta.txt"}, names)
	assert.False(t, ls.Truncated)
	assert.Equal(t, filepath.Join(ws, "a"), ls.Path)

	// $HOME 本身可列出，但文件不能当目录列。
	o.json(http.MethodGet, "/fs/list", pathQ("~"), nil, http.StatusOK, &ls)
	resp, b = o.do(http.MethodGet, "/fs/list", pathQ("~/workspace/a/file.txt"), nil, nil)
	assert.Equal(t, "not_a_directory", o.errCode(resp, b, http.StatusConflict))

	// rename：目标存在且未 overwrite → 409；overwrite 后成功。
	resp, b = o.do(http.MethodPost, "/fs/rename", nil, strings.NewReader(`{"from":"~/workspace/a/file.txt","to":"~/workspace/a/alpha.txt"}`), nil)
	assert.Equal(t, "exists", o.errCode(resp, b, http.StatusConflict))
	var moved apigen.FsEntry
	o.json(http.MethodPost, "/fs/rename", nil, map[string]any{"from": "~/workspace/a/file.txt", "to": "~/workspace/a/b/renamed.txt"}, http.StatusOK, &moved)
	assert.Equal(t, filepath.Join(ws, "a", "b", "renamed.txt"), moved.Path)
	got, err := os.ReadFile(filepath.Join(ws, "a", "b", "renamed.txt"))
	require.NoError(t, err)
	assert.Equal(t, content, got)
	o.json(http.MethodPost, "/fs/rename", nil, map[string]any{"from": "~/workspace/a/b/renamed.txt", "to": "~/workspace/a/alpha.txt", "overwrite": true}, http.StatusOK, nil)
	got, err = os.ReadFile(filepath.Join(ws, "a", "alpha.txt"))
	require.NoError(t, err)
	assert.Equal(t, content, got)
	// 不能移进自身，也不能用 overwrite 覆盖自己的上级。
	resp, b = o.do(http.MethodPost, "/fs/rename", nil, strings.NewReader(`{"from":"~/workspace/a","to":"~/workspace/a/b/a"}`), nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))
	resp, b = o.do(http.MethodPost, "/fs/rename", nil, strings.NewReader(`{"from":"~/workspace/a/b","to":"~/workspace/a","overwrite":true}`), nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))
	assert.DirExists(t, filepath.Join(ws, "a", "b"))

	// delete；再删 → 404。
	o.json(http.MethodPost, "/fs/delete", nil, apigen.FsPathRequest{Path: "~/workspace/a/b"}, http.StatusNoContent, nil)
	assert.NoDirExists(t, filepath.Join(ws, "a", "b"))
	resp, b = o.do(http.MethodPost, "/fs/delete", nil, strings.NewReader(`{"path":"~/workspace/a/b"}`), nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))
	resp, b = o.do(http.MethodGet, "/fs/stat", pathQ("~/workspace/nope"), nil, nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))

	// 路径形式：相对路径与空路径被拒。
	resp, b = o.do(http.MethodGet, "/fs/list", pathQ("workspace"), nil, nil)
	assert.Equal(t, "invalid_path", o.errCode(resp, b, http.StatusBadRequest))
	resp, b = o.do(http.MethodGet, "/fs/list", nil, nil, nil)
	assert.Equal(t, "invalid_path", o.errCode(resp, b, http.StatusBadRequest))

	// 钩子看到了原始请求头（e2e 据此断言控制面已删去 Cookie / Authorization）。
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, h := range seen {
		if h.Get("Cookie") == "__Host-oryn_session=x" {
			found = true
		}
	}
	assert.True(t, found, "OnLocalRequest 收到请求头")
}

func TestLocalFSWriteConditions(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	ws := filepath.Join(rt.home, "workspace")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	p := filepath.Join(ws, "doc.txt")
	require.NoError(t, os.WriteFile(p, []byte("v1"), 0o600))

	var st apigen.FsEntry
	o.json(http.MethodGet, "/fs/stat", pathQ(p), nil, http.StatusOK, &st)
	require.NotNil(t, st.Etag)

	// overwrite=false：已存在 → 409；新文件可写。
	resp, b := o.put(p, []byte("x"), url.Values{"overwrite": {"false"}}, nil)
	assert.Equal(t, "exists", o.errCode(resp, b, http.StatusConflict))
	resp, b = o.put(filepath.Join(ws, "new.txt"), []byte("x"), url.Values{"overwrite": {"0"}}, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))

	// If-Match 相符：写入并沿用原权限；随后用旧 etag 再写 → 412，内容不变。
	resp, b = o.put(p, []byte("v2"), nil, map[string]string{"If-Match": *st.Etag})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	var e2 apigen.FsEntry
	require.NoError(t, json.Unmarshal(b, &e2))
	assert.Equal(t, 0o600, e2.Mode, "沿用原文件权限")
	assert.NotEqual(t, *st.Etag, *e2.Etag)
	resp, b = o.put(p, []byte("v3"), nil, map[string]string{"If-Match": *st.Etag})
	assert.Equal(t, "modified", o.errCode(resp, b, http.StatusPreconditionFailed))
	got, _ := os.ReadFile(p)
	assert.Equal(t, "v2", string(got))
	// 文件不存在时任何 If-Match 都不符。
	resp, b = o.put(filepath.Join(ws, "ghost.txt"), []byte("x"), nil, map[string]string{"If-Match": *e2.Etag})
	assert.Equal(t, "modified", o.errCode(resp, b, http.StatusPreconditionFailed))
	assert.NoFileExists(t, filepath.Join(ws, "ghost.txt"))

	// 父目录不存在 → 404；目标是目录 → 409。
	resp, b = o.put(filepath.Join(ws, "missing", "x.txt"), []byte("x"), nil, nil)
	assert.Equal(t, "not_found", o.errCode(resp, b, http.StatusNotFound))
	resp, b = o.put(ws, []byte("x"), nil, nil)
	assert.Equal(t, "is_a_directory", o.errCode(resp, b, http.StatusConflict))

	// 目标是（$HOME 内的）符号链接：写入其指向的文件，链接保留。
	require.NoError(t, os.Symlink("doc.txt", filepath.Join(ws, "link.txt")))
	resp, b = o.put(filepath.Join(ws, "link.txt"), []byte("via-link"), nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	fi, err := os.Lstat(filepath.Join(ws, "link.txt"))
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink, "链接仍是链接")
	got, _ = os.ReadFile(p)
	assert.Equal(t, "via-link", string(got))
	var ls apigen.FsListing
	o.json(http.MethodGet, "/fs/list", pathQ(ws), nil, http.StatusOK, &ls)
	for _, e := range ls.Entries {
		if e.Name == "link.txt" {
			assert.Equal(t, apigen.FsEntryTypeSymlink, e.Type)
			require.NotNil(t, e.Target)
			assert.Equal(t, "doc.txt", *e.Target)
			require.NotNil(t, e.TargetType)
			assert.Equal(t, apigen.FsEntryTargetTypeFile, *e.TargetType)
		}
	}
	assert.Empty(t, tmpResidue(t, rt))
}

func TestLocalFSPathPolicy(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	ws := filepath.Join(rt.home, "workspace")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ws, "escape")))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "escdir")))

	// $HOME 之外（直接或经符号链接）一律 403 outside_home。
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/fs/list?path=/etc"},
		{http.MethodGet, "/fs/file?path=" + url.QueryEscape(filepath.Join(outside, "secret"))},
		{http.MethodGet, "/fs/file?path=" + url.QueryEscape("~/workspace/escape")},
		{http.MethodGet, "/fs/stat?path=" + url.QueryEscape("~/workspace/escape")},
		{http.MethodGet, "/fs/list?path=" + url.QueryEscape("~/workspace/escdir")},
		{http.MethodGet, "/fs/list?path=" + url.QueryEscape("~/..")},
	} {
		resp, b := o.do(c.method, c.path, nil, nil, nil)
		assert.Equal(t, "outside_home", o.errCode(resp, b, http.StatusForbidden), c.path)
	}
	resp, b := o.put("~/workspace/escape", []byte("pwn"), nil, nil)
	assert.Equal(t, "outside_home", o.errCode(resp, b, http.StatusForbidden))
	resp, b = o.put("~/workspace/escdir/new", []byte("pwn"), nil, nil)
	assert.Equal(t, "outside_home", o.errCode(resp, b, http.StatusForbidden))
	got, _ := os.ReadFile(filepath.Join(outside, "secret"))
	assert.Equal(t, "s", string(got))
	assert.NoFileExists(t, filepath.Join(outside, "new"))

	// 列出含越界链接的目录本身没问题：链接没有 targetType。
	var ls apigen.FsListing
	o.json(http.MethodGet, "/fs/list", pathQ(ws), nil, http.StatusOK, &ls)
	for _, e := range ls.Entries {
		if e.Name == "escape" {
			assert.Nil(t, e.TargetType)
		}
	}

	// ~/.oryn 只读：可列出，任何写入被拒。
	require.NoError(t, os.WriteFile(filepath.Join(ws, "movable"), nil, 0o644))
	o.json(http.MethodGet, "/fs/list", pathQ("~/.oryn"), nil, http.StatusOK, &ls)
	resp, b = o.put("~/.oryn/orb-token", []byte("x"), nil, nil)
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden))
	for _, c := range []struct{ path, body string }{
		{"/fs/mkdir", `{"path":"~/.oryn/x"}`},
		{"/fs/delete", `{"path":"~/.oryn/threads"}`},
		{"/fs/delete", `{"path":"~/.oryn"}`},
		{"/fs/rename", `{"from":"~/.oryn/threads","to":"~/stolen"}`},
		{"/fs/rename", `{"from":"~/workspace/movable","to":"~/.oryn/x"}`},
		// $HOME 本身与工作目录（及其上级）不能删除 / 移走。
		{"/fs/delete", `{"path":"~"}`},
		{"/fs/delete", `{"path":"~/workspace"}`},
		{"/fs/rename", `{"from":"~/workspace","to":"~/ws2"}`},
	} {
		resp, b := o.do(http.MethodPost, c.path, nil, strings.NewReader(c.body), nil)
		assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden), c.body)
	}
	assert.DirExists(t, filepath.Join(rt.home, ".oryn", "threads"))
	assert.DirExists(t, ws)

	// 设置了仓库工作目录：其上级 ~/workspace 同样受保护，兄弟目录可删。
	repo := filepath.Join(ws, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "other"), 0o755))
	setWorkdir(rt, repo)
	resp, b = o.do(http.MethodPost, "/fs/delete", nil, strings.NewReader(`{"path":"~/workspace/repo"}`), nil)
	assert.Equal(t, "protected_path", o.errCode(resp, b, http.StatusForbidden))
	o.json(http.MethodPost, "/fs/delete", nil, apigen.FsPathRequest{Path: "~/workspace/other"}, http.StatusNoContent, nil)
}

// 超过上限：带 Content-Length 的请求直接 413；分块上传在读到上限时 413；都不留临时文件、不创建目标。
func TestLocalFSWriteTooLarge(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	rt.maxFileBytes = 64 << 10
	o := newOrbHTTP(t, cp, rt)
	ws := filepath.Join(rt.home, "workspace")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	big := bytes.Repeat([]byte("x"), 64<<10+1)

	resp, b := o.put("~/workspace/big.bin", big, nil, nil)
	assert.Equal(t, "too_large", o.errCode(resp, b, http.StatusRequestEntityTooLarge))

	q := url.Values{"path": {"~/workspace/big.bin"}}
	resp, b = o.do(http.MethodPut, "/fs/file", q, io.MultiReader(bytes.NewReader(big)), nil) // 无 Content-Length → 分块
	assert.Equal(t, "too_large", o.errCode(resp, b, http.StatusRequestEntityTooLarge))
	assert.NoFileExists(t, filepath.Join(ws, "big.bin"))
	assert.Empty(t, tmpResidue(t, rt))

	// 恰好等于上限可以写入。
	resp, b = o.put("~/workspace/ok.bin", big[:64<<10], nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
}

// 启动时清空上传临时目录。
func TestLocalTmpClearedOnStart(t *testing.T) {
	cp := newFakeCP(t)
	home := t.TempDir()
	stale := filepath.Join(home, ".oryn", "tmp", "up-stale")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o700))
	require.NoError(t, os.WriteFile(stale, []byte("partial"), 0o600))
	startRuntime(t, cp, func(c *Config) { c.Home = home })
	assert.NoFileExists(t, stale)
	assert.DirExists(t, filepath.Join(home, ".oryn", "tmp"))
}

func TestIsText(t *testing.T) {
	assert.True(t, isText([]byte("plain"), false))
	assert.True(t, isText(nil, false))
	assert.False(t, isText([]byte("a\x00b"), false))
	cut := []byte("ab\xe4\xb8") // 「中」被截断
	assert.False(t, isText(cut, false), "未读满时截断的码点是非法 UTF-8")
	assert.True(t, isText(cut, true), "读满 8KiB 时允许末尾截断的码点")
	assert.False(t, isText([]byte("a\xffb"), true))
}

// ORYN_ORBD_REQUEST_LOG：子进程 orbd 把每个本机 API 请求写成一行 JSON。
func TestRequestLogger(t *testing.T) {
	p := filepath.Join(t.TempDir(), "req.log")
	log := requestLogger(p)
	req, err := http.NewRequest(http.MethodGet, "http://orb/fs/list?path=~", nil)
	require.NoError(t, err)
	req.Header.Set("X-Test", "1")
	log(req)
	log(req)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	require.Len(t, lines, 2)
	var rec struct {
		Method, Path, RawQuery string
		Header                 http.Header
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &rec))
	assert.Equal(t, "GET", rec.Method)
	assert.Equal(t, "/fs/list", rec.Path)
	assert.Equal(t, "path=~", rec.RawQuery)
	assert.Equal(t, "1", rec.Header.Get("X-Test"))

	t.Setenv("ORYN_URL", "http://x")
	t.Setenv("ORYN_ORB_ID", "orb-x")
	t.Setenv("ORYN_ORB_TOKEN", "oot_x")
	t.Setenv("ORYN_ORBD_REQUEST_LOG", p)
	cfg, err := ConfigFromEnv("v")
	require.NoError(t, err)
	assert.NotNil(t, cfg.OnLocalRequest)
}

// 控制面打开的未知类型流（fs / portal 之外）被关闭，fs 流之外不受影响。
func TestNonFSStreamClosed(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, nil)
	cp.ctlNow(t)
	cp.mu.Lock()
	sess := cp.sess
	cp.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamDesk})
	require.NoError(t, err)
	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err = st.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}

// mutatingBody 在第一次被读取时（handleFsWrite 已做完预检、开始接收上传）执行 f，模拟上传期间的并发改动。
type mutatingBody struct {
	f    func()
	r    io.Reader
	once sync.Once
}

func (b *mutatingBody) Read(p []byte) (int, error) { b.once.Do(b.f); return b.r.Read(p) }
func (*mutatingBody) Close() error                 { return nil }

func serveLocal(rt *Runtime, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rt.localHandler().ServeHTTP(rec, req)
	return rec
}

// 上传期间目标被别的编辑者改动：收完上传后复核 If-Match，412 且不覆盖并发的改动。
func TestFsWriteRechecksIfMatchAfterUpload(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	p := filepath.Join(rt.home, "doc.txt")
	require.NoError(t, os.WriteFile(p, []byte("original"), 0o600))
	fi, err := os.Stat(p)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/fs/file?path="+url.QueryEscape(p), nil)
	req.Header.Set("If-Match", etagFor(fi))
	req.Body = &mutatingBody{f: func() { require.NoError(t, os.WriteFile(p, []byte("concurrent edit"), 0o600)) }, r: strings.NewReader("stale upload")}
	rec := serveLocal(rt, req)
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "concurrent edit", string(data))
	left, _ := os.ReadDir(rt.tmpDir())
	assert.Empty(t, left, "被拒的上传不留临时文件")
}

// overwrite=false 的上传期间目标被创建：复核后 409，不覆盖。
func TestFsWriteCreateOnlyRechecksAfterUpload(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	p := filepath.Join(rt.home, "new.txt")
	req := httptest.NewRequest(http.MethodPut, "/fs/file?overwrite=false&path="+url.QueryEscape(p), nil)
	req.Body = &mutatingBody{f: func() { require.NoError(t, os.WriteFile(p, []byte("concurrent new file"), 0o600)) }, r: strings.NewReader("upload")}
	rec := serveLocal(rt, req)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "concurrent new file", string(data))
}

// 复核之后、提交之前（锁外的 agent）创建了目标：RENAME_NOREPLACE 提交失败为 409，不覆盖。
func TestFsWriteCreateOnlyCommitDoesNotReplace(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	p := filepath.Join(rt.home, "race.txt")
	fsCommitHook = func(dst string) { require.NoError(t, os.WriteFile(dst, []byte("agent wrote this"), 0o600)) }
	t.Cleanup(func() { fsCommitHook = nil })
	rec := serveLocal(rt, httptest.NewRequest(http.MethodPut, "/fs/file?overwrite=false&path="+url.QueryEscape(p), strings.NewReader("upload")))
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"exists"`)
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "agent wrote this", string(data))
	left, _ := os.ReadDir(rt.tmpDir())
	assert.Empty(t, left)

	// 没有竞争时只创建照常成功
	fsCommitHook = nil
	q := filepath.Join(rt.home, "fresh.txt")
	rec = serveLocal(rt, httptest.NewRequest(http.MethodPut, "/fs/file?overwrite=false&path="+url.QueryEscape(q), strings.NewReader("hello")))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data, err = os.ReadFile(q)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

// RENAME_NOREPLACE 不可用时的 link + unlink 退路同样不覆盖已存在的目标。
func TestLinkNoReplace(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0o600))
	err := linkNoReplace(src, dst)
	require.ErrorIs(t, err, os.ErrExist)
	data, _ := os.ReadFile(dst)
	assert.Equal(t, "old", string(data))
	require.FileExists(t, src)
	require.NoError(t, os.Remove(dst))
	require.NoError(t, linkNoReplace(src, dst))
	assert.NoFileExists(t, src)
	data, _ = os.ReadFile(dst)
	assert.Equal(t, "new", string(data))
}

// 经符号链接父目录把文件改名到它自己：是同一目录项，不得先删除再移动。
func TestFsRenameThroughAliasedParent(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	parent := filepath.Join(rt.home, "parent")
	src := filepath.Join(parent, "doc.txt")
	alias := filepath.Join(rt.home, "alias")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	require.NoError(t, os.WriteFile(src, []byte("irreplaceable"), 0o600))
	require.NoError(t, os.Symlink(parent, alias))
	body, _ := json.Marshal(map[string]any{"from": src, "to": filepath.Join(alias, "doc.txt"), "overwrite": true})
	rec := serveLocal(rt, httptest.NewRequest(http.MethodPost, "/fs/rename", bytes.NewReader(body)))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Equal(t, "irreplaceable", string(data))

	// 经别名移进自身子目录同样被拒
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "sub"), 0o755))
	body, _ = json.Marshal(map[string]any{"from": parent, "to": filepath.Join(alias, "sub", "moved")})
	rec = serveLocal(rt, httptest.NewRequest(http.MethodPost, "/fs/rename", bytes.NewReader(body)))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.DirExists(t, parent)
}

// ~/.oryn 下指向别处的链接：按目标判断不在 .oryn 下，但 delete / rename 作用于链接本身，必须拒绝。
func TestFsMutateRejectsOrynEntryLink(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	work := filepath.Join(rt.home, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	link := filepath.Join(rt.home, ".oryn", "foo")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(work, link))
	for _, c := range []struct{ path, body string }{
		{"/fs/delete", `{"path":"~/.oryn/foo"}`},
		{"/fs/rename", `{"from":"~/.oryn/foo","to":"~/moved"}`},
	} {
		rec := serveLocal(rt, httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body)))
		assert.Equal(t, http.StatusForbidden, rec.Code, c.path)
		assert.Contains(t, rec.Body.String(), "protected_path", c.path)
	}
	_, err := os.Lstat(link)
	require.NoError(t, err, "链接仍在")
}
