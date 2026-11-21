package repos

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// smartServer 是托管仓库的 smart-HTTP 测试服务：/owner/r.git/<端点> 是主人入口，/orb/r.git/<端点> 是 orb-1 的入口。
type smartServer struct {
	*httptest.Server
	env     *testEnv
	dir     string
	started chan struct{} // 每个请求进入处理函数时一个信号
	done    chan struct{} // 每个请求处理完时一个信号
}

func newSmartServer(t *testing.T, mut func(*Config)) *smartServer {
	t.Helper()
	e := newTestEnv(t, mut, nil, nil)
	id := e.project(t, "")
	_, err := e.svc.InitHosted(context.Background(), id, "main", true, testIdentity)
	require.NoError(t, err)
	s := &smartServer{env: e, dir: e.svc.dir(id), started: make(chan struct{}, 64), done: make(chan struct{}, 64)}
	// receive-pack 推送后（即使解包失败）会跑 `git maintenance run --auto`，缺省分离到后台：分离的进程拿着
	// objects/maintenance.lock，可能比处理函数活得久，让「没有残留锁」的断言偶发失败。测试里让它同步跑完。
	gitCmd(t, s.dir, "config", "maintenance.autoDetach", "false")
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.started <- struct{}{}
		defer func() { s.done <- struct{}{} }()
		who, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/r.git/")
		orb := ""
		if who == "orb" {
			orb = "orb-1"
		}
		e.svc.serveSmartHTTP(w, r, s.dir, Endpoint(rest), orb)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *smartServer) owner() string { return s.URL + "/owner/r.git" }
func (s *smartServer) orb() string   { return s.URL + "/orb/r.git" }

// clientGit 以客户端身份执行宿主 git：不读系统与用户配置、不走代理；返回合并输出与错误。
func clientGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !strings.HasSuffix(strings.ToLower(k), "_proxy") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(), "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mustClientGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := clientGit(t, dir, args...)
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return out
}

func commitFile(t *testing.T, work, name, body, msg string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(work, name), []byte(body), 0o644))
	mustClientGit(t, work, "add", name)
	mustClientGit(t, work, "commit", "-q", "-m", msg)
	return mustClientGit(t, work, "rev-parse", "HEAD")
}

func TestSmartHTTPCloneAndPush(t *testing.T) {
	s := newSmartServer(t, nil)
	tmp := t.TempDir()

	// 缺省协议（v2）与 v0 都能克隆
	mustClientGit(t, tmp, "clone", "-q", s.owner(), "w")
	mustClientGit(t, tmp, "-c", "protocol.version=0", "clone", "-q", s.owner(), "w0")
	work := filepath.Join(tmp, "w")
	assert.Equal(t, "Initial commit", mustClientGit(t, work, "log", "--format=%s"))
	assert.Equal(t, "Initial commit", mustClientGit(t, filepath.Join(tmp, "w0"), "log", "--format=%s"))

	// 普通推送
	head := commitFile(t, work, "a.txt", "a\n", "add a")
	mustClientGit(t, work, "push", "-q", "origin", "main")
	assert.Equal(t, head, gitCmd(t, s.dir, "rev-parse", "refs/heads/main"))

	// 强推（基线不设保护）
	mustClientGit(t, work, "commit", "-q", "--amend", "-m", "add a (amended)")
	amended := mustClientGit(t, work, "rev-parse", "HEAD")
	mustClientGit(t, work, "push", "-q", "--force", "origin", "main")
	assert.Equal(t, amended, gitCmd(t, s.dir, "rev-parse", "refs/heads/main"))

	// 新建分支再删除
	mustClientGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/b")
	assert.Equal(t, amended, gitCmd(t, s.dir, "rev-parse", "refs/heads/b"))
	mustClientGit(t, work, "push", "-q", "origin", ":b")
	assert.Empty(t, gitCmd(t, s.dir, "for-each-ref", "refs/heads/b"))

	// 新克隆看到推上去的历史
	mustClientGit(t, tmp, "clone", "-q", s.owner(), "w2")
	assert.Equal(t, "add a (amended)\nInitial commit", mustClientGit(t, filepath.Join(tmp, "w2"), "log", "--format=%s"))
}

func smartDo(t *testing.T, method, url string, hdr map[string]string, body []byte) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(b)
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(b)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestSmartHTTPRequests(t *testing.T) {
	s := newSmartServer(t, nil)
	refs := s.owner() + "/info/refs"
	upload := s.owner() + "/git-upload-pack"
	uploadReq := map[string]string{"Content-Type": "application/x-git-upload-pack-request"}

	resp, _ := smartDo(t, http.MethodDelete, refs+"?service=git-upload-pack", nil, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, http.MethodGet, resp.Header.Get("Allow"))

	resp, body := smartDo(t, http.MethodGet, refs, nil, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, body, "oryn: 只支持 smart HTTP")
	assert.Equal(t, "no-cache, max-age=0, must-revalidate", resp.Header.Get("Cache-Control"))

	resp, _ = smartDo(t, http.MethodGet, upload, nil, nil)
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, http.MethodPost, resp.Header.Get("Allow"))

	resp, _ = smartDo(t, http.MethodPost, upload, map[string]string{"Content-Type": "text/plain"}, []byte("0000"))
	assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)

	resp, _ = smartDo(t, http.MethodPost, upload,
		map[string]string{"Content-Type": "application/x-git-upload-pack-request", "Content-Encoding": "br"}, []byte("0000"))
	assert.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode)

	resp, _ = smartDo(t, http.MethodPost, upload,
		map[string]string{"Content-Type": "application/x-git-upload-pack-request", "Content-Encoding": "gzip"}, []byte("not gzip"))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// gzip 压缩的 flush 包：upload-pack 没有 want，正常结束
	resp, _ = smartDo(t, http.MethodPost, upload,
		map[string]string{"Content-Type": "application/x-git-upload-pack-request", "Content-Encoding": "gzip"}, gzipped(t, []byte("0000")))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-git-upload-pack-result", resp.Header.Get("Content-Type"))

	resp, _ = smartDo(t, http.MethodGet, refs+"?service=git-upload-pack", map[string]string{"Git-Protocol": "version=2;rm -rf"}, nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, _ = smartDo(t, http.MethodPost, upload, map[string]string{"Content-Type": uploadReq["Content-Type"], "Git-Protocol": "version=2 x"}, []byte("0000"))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, body = smartDo(t, http.MethodGet, refs+"?service=git-upload-pack", map[string]string{"Git-Protocol": "version=0"}, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/x-git-upload-pack-advertisement", resp.Header.Get("Content-Type"))
	assert.True(t, strings.HasPrefix(body, "001e# service=git-upload-pack\n0000"), body)
	assert.Contains(t, body, "refs/heads/main")

	// v2：不写 service 行，直接是能力通告
	resp, body = smartDo(t, http.MethodGet, refs+"?service=git-upload-pack", map[string]string{"Git-Protocol": "version=2"}, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(body, "000eversion 2\n"), body)

	// receive-pack 忽略 Git-Protocol，总是写 service 行
	resp, body = smartDo(t, http.MethodGet, refs+"?service=git-receive-pack", map[string]string{"Git-Protocol": "version=2;bad"}, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(body, "001f# service=git-receive-pack\n0000"), body)

	rec := httptest.NewRecorder()
	s.env.svc.serveSmartHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil), s.dir, "objects/info/packs", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestIsPush(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/r.git/info/refs?service=git-receive-pack", nil)
	assert.True(t, IsPush(r, EndpointInfoRefs))
	r = httptest.NewRequest(http.MethodGet, "/r.git/info/refs?service=git-upload-pack", nil)
	assert.False(t, IsPush(r, EndpointInfoRefs))
	assert.True(t, IsPush(httptest.NewRequest(http.MethodPost, "/r.git/git-receive-pack", nil), EndpointReceivePack))
	assert.True(t, IsPush(httptest.NewRequest(http.MethodGet, "/r.git/git-receive-pack", nil), EndpointReceivePack))
	assert.False(t, IsPush(httptest.NewRequest(http.MethodPost, "/r.git/git-upload-pack", nil), EndpointUploadPack))
}

func TestSmartHTTPOrbFsck(t *testing.T) {
	s := newSmartServer(t, nil)
	tmp := t.TempDir()
	mustClientGit(t, tmp, "clone", "-q", s.owner(), "w")
	work := filepath.Join(tmp, "w")

	// 树里有名为 .git 的路径项：检出时会写进仓库目录
	blob := mustClientGit(t, work, "hash-object", "-w", "--stdin")
	cmd := exec.Command("git", "mktree")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	cmd.Stdin = strings.NewReader("100644 blob " + blob + "\t.git\n")
	tree, err := cmd.Output()
	require.NoError(t, err)
	bad := mustClientGit(t, work, "commit-tree", strings.TrimSpace(string(tree)), "-p", "HEAD", "-m", "bad")

	out, err := clientGit(t, work, "push", s.orb(), bad+":refs/heads/bad")
	require.Error(t, err, out)
	assert.Empty(t, gitCmd(t, s.dir, "for-each-ref", "refs/heads/bad"))

	mustClientGit(t, work, "push", "-q", s.owner(), bad+":refs/heads/bad")
	assert.Equal(t, bad, gitCmd(t, s.dir, "rev-parse", "refs/heads/bad"))
}

func TestSmartHTTPHiddenRefs(t *testing.T) {
	s := newSmartServer(t, nil)
	main := gitCmd(t, s.dir, "rev-parse", "refs/heads/main")
	gitCmd(t, s.dir, "update-ref", "refs/oryn/saved/1/heads/x", main)

	tmp := t.TempDir()
	out := mustClientGit(t, tmp, "ls-remote", s.owner())
	assert.Contains(t, out, "refs/heads/main")
	assert.NotContains(t, out, "refs/oryn")

	mustClientGit(t, tmp, "clone", "-q", s.owner(), "w")
	work := filepath.Join(tmp, "w")
	out, err := clientGit(t, work, "push", "origin", ":refs/oryn/saved/1/heads/x")
	require.Error(t, err, out)
	out, err = clientGit(t, work, "push", "origin", "HEAD:refs/oryn/x")
	require.Error(t, err, out)
	assert.Equal(t, main, gitCmd(t, s.dir, "rev-parse", "refs/oryn/saved/1/heads/x"))
	assert.Empty(t, gitCmd(t, s.dir, "for-each-ref", "refs/oryn/x"))
}

func TestSmartHTTPReceivePackClientGone(t *testing.T) {
	s := newSmartServer(t, nil)
	old := gitCmd(t, s.dir, "rev-parse", "refs/heads/main")
	body := string(pktLine(old+" "+strings.Repeat("1", 40)+" refs/heads/main\x00report-status\n")) +
		"0000" + "PACK\x00\x00\x00\x02\x00\x00\x00\x05partial"

	conn, err := net.Dial("tcp", s.Listener.Addr().String())
	require.NoError(t, err)
	_, err = fmt.Fprintf(conn, "POST /owner/r.git/git-receive-pack HTTP/1.1\r\nHost: x\r\n"+
		"Content-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\n\r\n%s", len(body)+1<<20, body)
	require.NoError(t, err)
	select {
	case <-s.started:
	case <-time.After(10 * time.Second):
		t.Fatal("请求没有进入处理函数")
	}
	time.Sleep(200 * time.Millisecond) // receive-pack 已在读包
	require.NoError(t, conn.Close())
	select {
	case <-s.done:
	case <-time.After(30 * time.Second):
		t.Fatal("客户端断开后 receive-pack 没有结束")
	}

	var locks []string
	require.NoError(t, filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".lock") {
			locks = append(locks, p)
		}
		return err
	}))
	assert.Empty(t, locks)
	assert.Equal(t, old, gitCmd(t, s.dir, "rev-parse", "refs/heads/main"))

	tmp := t.TempDir()
	mustClientGit(t, tmp, "clone", "-q", s.owner(), "w")
	work := filepath.Join(tmp, "w")
	head := commitFile(t, work, "b.txt", "b\n", "after disconnect")
	mustClientGit(t, work, "push", "-q", "origin", "main")
	assert.Equal(t, head, gitCmd(t, s.dir, "rev-parse", "refs/heads/main"))
}

func TestSmartHTTPNoSpace(t *testing.T) {
	s := newSmartServer(t, func(c *Config) { c.MinFreeBytes = 1 << 62 })
	resp, body := smartDo(t, http.MethodPost, s.owner()+"/git-receive-pack",
		map[string]string{"Content-Type": "application/x-git-receive-pack-request"}, []byte("0000"))
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, body, "磁盘空间不足")
	resp, body = smartDo(t, http.MethodGet, s.owner()+"/info/refs?service=git-receive-pack", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, body, "磁盘空间不足")

	resp, _ = smartDo(t, http.MethodGet, s.owner()+"/info/refs?service=git-upload-pack", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	mustClientGit(t, t.TempDir(), "clone", "-q", s.owner(), "w")
}

func TestSmartSlots(t *testing.T) {
	var sl smartSlots
	r1, ok := sl.acquire("orb-a")
	require.True(t, ok)
	r2, ok := sl.acquire("orb-a")
	require.True(t, ok)
	_, ok = sl.acquire("orb-a")
	assert.False(t, ok, "每台 orb 至多 2 个")

	var rel []func()
	for range smartGlobalSlots - 2 {
		r, ok := sl.acquire("")
		require.True(t, ok)
		rel = append(rel, r)
	}
	_, ok = sl.acquire("")
	assert.False(t, ok, "全局至多 8 个")
	_, ok = sl.acquire("orb-b")
	assert.False(t, ok)

	r1()
	r1() // 重复释放无害
	r3, ok := sl.acquire("orb-b")
	require.True(t, ok)
	r2()
	r3()
	for _, r := range rel {
		r()
	}
	assert.Zero(t, sl.total)
	assert.Empty(t, sl.byOrb)
}

func TestSmartHTTPBusy(t *testing.T) {
	s := newSmartServer(t, nil)
	var rel []func()
	for range smartOrbSlots {
		r, ok := s.env.svc.smart.acquire("orb-1")
		require.True(t, ok)
		rel = append(rel, r)
	}
	resp, body := smartDo(t, http.MethodGet, s.orb()+"/info/refs?service=git-upload-pack", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "5", resp.Header.Get("Retry-After"))
	assert.Contains(t, body, "oryn: 仓库正忙，请稍后重试")

	// 主人入口不受 orb 名额影响
	resp, _ = smartDo(t, http.MethodGet, s.owner()+"/info/refs?service=git-upload-pack", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	for _, r := range rel {
		r()
	}
	resp, _ = smartDo(t, http.MethodGet, s.orb()+"/info/refs?service=git-upload-pack", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestPktLine(t *testing.T) {
	assert.Equal(t, "001e# service=git-upload-pack\n", string(pktLine("# service=git-upload-pack\n")))
	assert.Equal(t, "0004", string(pktLine("")))
}
