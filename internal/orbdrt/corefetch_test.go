package orbdrt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/core"
)

// coreServer 是假的 Release 下载站：按 "/<tag>/omp-<平台>" 提供 assets 里的内容，记录每次请求的路径。
type coreServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	assets map[string][]byte
	hits   []string
}

func newCoreServer(t *testing.T) *coreServer {
	t.Helper()
	cs := &coreServer{assets: map[string][]byte{}}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.hits = append(cs.hits, r.URL.Path)
		body, ok := cs.assets[r.URL.Path]
		cs.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(cs.srv.Close)
	t.Setenv(coreBaseURLEnv, cs.srv.URL+"/") // 末尾的 / 被去掉
	return cs
}

// put 发布 tag 的当前平台产物并返回对应的 Release。
func (cs *coreServer) put(tag string, body []byte) core.Release {
	cs.mu.Lock()
	cs.assets["/"+tag+"/omp-"+corePlatform()] = body
	cs.mu.Unlock()
	return coreRelease(tag, body)
}

func (cs *coreServer) hitCount() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return len(cs.hits)
}

func coreRelease(tag string, body []byte) core.Release {
	sum := sha256.Sum256(body)
	return core.Release{Repo: "Lynricsy/oryn-core", Tag: tag, SHA256: map[string]string{corePlatform(): hex.EncodeToString(sum[:])}}
}

// 首次下载落到 ~/.oryn/core/<tag>/omp（0755）；已缓存且 sha 一致不再请求；换 tag 后下载新的并删掉旧 tag 目录；
// 缓存被改坏时重新下载。
func TestCoreFetchDownloadCacheAndPrune(t *testing.T) {
	cs := newCoreServer(t)
	home := t.TempDir()
	ctx := context.Background()

	relA := cs.put("oryn-v1", []byte("core-a"))
	bin, err := ensureCore(ctx, home, relA, nil)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".oryn", "core", "oryn-v1", "omp"), bin)
	assert.Equal(t, "core-a", readFile(t, bin))
	fi, err := os.Stat(bin)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
	assert.Equal(t, 1, cs.hitCount())

	again, err := ensureCore(ctx, home, relA, nil)
	require.NoError(t, err)
	assert.Equal(t, bin, again)
	assert.Equal(t, 1, cs.hitCount(), "已缓存不再请求")

	require.NoError(t, os.WriteFile(bin, []byte("tampered"), 0o755))
	_, err = ensureCore(ctx, home, relA, nil)
	require.NoError(t, err)
	assert.Equal(t, "core-a", readFile(t, bin), "sha 不符的缓存被重新下载")
	assert.Equal(t, 2, cs.hitCount())

	relB := cs.put("oryn-v2", []byte("core-b"))
	binB, err := ensureCore(ctx, home, relB, nil)
	require.NoError(t, err)
	assert.Equal(t, "core-b", readFile(t, binB))
	assert.NoDirExists(t, filepath.Join(home, ".oryn", "core", "oryn-v1"), "旧 tag 被清理")
	entries, err := os.ReadDir(filepath.Join(home, ".oryn", "core"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "oryn-v2", entries[0].Name())
}

// sha 不符：报错，不留下目标文件与临时文件，旧 tag 不被清理。
func TestCoreFetchShaMismatch(t *testing.T) {
	cs := newCoreServer(t)
	home := t.TempDir()
	_, err := ensureCore(context.Background(), home, cs.put("oryn-v1", []byte("core-a")), nil)
	require.NoError(t, err)

	cs.put("oryn-v2", []byte("evil"))
	rel := coreRelease("oryn-v2", []byte("expected"))
	_, err = ensureCore(context.Background(), home, rel, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sha256")
	assert.Contains(t, err.Error(), coreBaseURLEnv)
	dir := filepath.Join(home, ".oryn", "core", "oryn-v2")
	assert.NoFileExists(t, filepath.Join(dir, "omp"))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "临时文件被删掉")
	assert.FileExists(t, filepath.Join(home, ".oryn", "core", "oryn-v1", "omp"), "失败时不清理旧 tag")
}

// 未知平台：不发请求，直接报「此平台没有 Oryn Core 构建」。
func TestCoreFetchUnknownPlatform(t *testing.T) {
	cs := newCoreServer(t)
	rel := core.Release{Repo: "Lynricsy/oryn-core", Tag: "oryn-v1", SHA256: map[string]string{"plan9-mips": "00"}}
	_, err := ensureCore(context.Background(), t.TempDir(), rel, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "此平台没有 Oryn Core 构建")
	assert.Contains(t, err.Error(), corePlatform())
	assert.Zero(t, cs.hitCount())
}

// 下载失败（404）：错误里带 URL 与 ORYN_CORE_BASE_URL 提示。
func TestCoreFetchHTTPError(t *testing.T) {
	cs := newCoreServer(t)
	_, err := ensureCore(context.Background(), t.TempDir(), coreRelease("oryn-v9", []byte("x")), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), cs.srv.URL+"/oryn-v9/omp-"+corePlatform())
	assert.Contains(t, err.Error(), coreBaseURLEnv)
	assert.Contains(t, err.Error(), "404")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 下载地址：缺省是 GitHub Release；ORYN_CORE_BASE_URL 指向镜像时换成镜像。
func TestCoreFetchBaseURL(t *testing.T) {
	body := []byte("core")
	var got []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	rel := coreRelease("oryn-v1", body)

	t.Setenv(coreBaseURLEnv, "")
	_, err := ensureCore(context.Background(), t.TempDir(), rel, client)
	require.NoError(t, err)
	t.Setenv(coreBaseURLEnv, "https://mirror.example.com/core/")
	_, err = ensureCore(context.Background(), t.TempDir(), rel, client)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"https://github.com/Lynricsy/oryn-core/releases/download/oryn-v1/omp-" + corePlatform(),
		"https://mirror.example.com/core/oryn-v1/omp-" + corePlatform(),
	}, got)
}

func TestCoreFetchPlatformKey(t *testing.T) {
	assert.Regexp(t, `^[a-z0-9]+-[a-z0-9]+$`, corePlatform())
	assert.NotContains(t, corePlatform(), "amd64", "amd64 记作 x64")
}

// runner 没给 ORYN_AGENT_BIN 时启动即下载 Core；下载失败 orbd 启动失败（不回退 PATH 里的 omp），实例锁随之释放。
func TestCoreFetchRunnerStartupFails(t *testing.T) {
	f := newRunnerHome(t)
	newCoreServer(t) // 没有任何资产：一律 404
	cp := newFakeCP(t)
	cp.token.Store(testRunnerToken)
	cfg := Config{URL: cp.srv.URL, Token: testRunnerToken, Home: f.home, EgressAddr: "127.0.0.1:0", SSHAddr: "127.0.0.1:0", Runner: &RunnerConfig{Dirs: []string{f.work}}}
	_, err := New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Oryn Core")
	assert.NoFileExists(t, filepath.Join(f.home, ".oryn", "core", core.Source().Tag, "omp"))

	cfg.AgentBin = fakeompBin
	rt, err := New(cfg)
	require.NoError(t, err, "锁已释放")
	_ = rt.egressLn.Close()
	if rt.sshLn != nil {
		_ = rt.sshLn.Close()
	}
	rt.runner.unlock()
}
