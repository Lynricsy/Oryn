package repos

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/lfs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const lfsTok = "tok-lfs-1234567890"

// lfsEnv 起带 acme/widgets 的假 GitHub 与 Service（GitHub 令牌 lfsTok），返回一个指向它的项目。
func lfsEnv(t *testing.T, mut func(*Config)) (*testEnv, db.Project) {
	t.Helper()
	e := newTestEnv(t, mut, nil, fakeGitHub{tok: lfsTok})
	id := e.project(t, "https://github.com/acme/widgets")
	prj, err := e.st.Q.GetProject(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return e, prj
}

// putObj 在假上游存 content，返回指向它的指针。
func putObj(e *testEnv, content string) lfs.Pointer {
	sum := sha256.Sum256([]byte(content))
	p := lfs.Pointer{OID: hex.EncodeToString(sum[:]), Size: int64(len(content))}
	e.fake.PutLFS(p.OID, []byte(content))
	return p
}

func readObj(t *testing.T, e *testEnv, prj db.Project, p lfs.Pointer) (string, error) {
	t.Helper()
	f, err := e.svc.lfsObject(context.Background(), prj, p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), nil
}

func errCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func (e *testEnv) lfsBatches() int {
	return len(e.fake.CallsTo("POST", "/acme/widgets.git/info/lfs/objects/batch"))
}

// cacheFiles 列出项目缓存目录下的全部文件名。
func cacheFiles(t *testing.T, e *testEnv, projectID string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(filepath.Join(e.svc.cfg.Dir, ".lfs-cache", projectID), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, d.Name())
		}
		return nil
	})
	return out
}

func TestLFSObjectFetchAndCache(t *testing.T) {
	e, prj := lfsEnv(t, nil)
	p := putObj(e, "hello lfs\n")
	got, err := readObj(t, e, prj, p)
	if err != nil || got != "hello lfs\n" {
		t.Fatalf("lfsObject = %q, %v", got, err)
	}
	batches := e.fake.CallsTo("POST", "/acme/widgets.git/info/lfs/objects/batch")
	if len(batches) != 1 {
		t.Fatalf("batch 调用 %d 次", len(batches))
	}
	cred, ok := strings.CutPrefix(batches[0].Auth, "Basic ")
	dec, _ := base64.StdEncoding.DecodeString(cred)
	if !ok || string(dec) != "x-access-token:"+lfsTok {
		t.Fatalf("batch 的 Authorization = %q", batches[0].Auth)
	}
	objs := e.fake.CallsTo("GET", "/_lfs/objects/"+p.OID)
	if len(objs) != 1 || objs[0].Auth != "" {
		t.Fatalf("对象请求 = %+v（不应带 Authorization）", objs)
	}
	path := filepath.Join(e.svc.cfg.Dir, ".lfs-cache", prj.ID, p.OID[0:2], p.OID[2:4], p.OID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("缓存文件: %v", err)
	}
	// 二次读取命中缓存
	if got, err := readObj(t, e, prj, p); err != nil || got != "hello lfs\n" || e.lfsBatches() != 1 {
		t.Fatalf("二次读取 = %q, %v, batch %d 次", got, err, e.lfsBatches())
	}
}

func TestLFSSingleFlight(t *testing.T) {
	e, prj := lfsEnv(t, nil)
	p := putObj(e, "concurrent object\n")
	var wg sync.WaitGroup
	var bad atomic.Int32
	for range 5 {
		wg.Go(func() {
			if got, err := readObj(t, e, prj, p); err != nil || got != "concurrent object\n" {
				bad.Add(1)
			}
		})
	}
	wg.Wait()
	if bad.Load() != 0 || e.lfsBatches() != 1 {
		t.Fatalf("失败 %d 个，batch %d 次", bad.Load(), e.lfsBatches())
	}
}

func TestLFSCorruptAndMissing(t *testing.T) {
	e, prj := lfsEnv(t, nil)
	sum := sha256.Sum256([]byte("expected"))
	p := lfs.Pointer{OID: hex.EncodeToString(sum[:]), Size: 8}
	e.fake.PutLFS(p.OID, []byte("tampered"))
	if _, err := readObj(t, e, prj, p); errCode(err) != "lfs_corrupt" {
		t.Fatalf("损坏对象 = %v", err)
	}
	if files := cacheFiles(t, e, prj.ID); len(files) != 0 {
		t.Fatalf("损坏对象留下了文件 %v", files)
	}
	long := lfs.Pointer{OID: p.OID, Size: 4}
	if _, err := readObj(t, e, prj, long); errCode(err) != "lfs_corrupt" {
		t.Fatalf("超长对象 = %v", err)
	}
	missing := lfs.Pointer{OID: strings.Repeat("ab", 32), Size: 3}
	if _, err := readObj(t, e, prj, missing); errCode(err) != "lfs_missing" {
		t.Fatalf("未上传的对象 = %v", err)
	}
}

func TestLFSEmptyObject(t *testing.T) {
	e, prj := lfsEnv(t, nil)
	if got, err := readObj(t, e, prj, lfs.Pointer{OID: lfsEmptyOID}); err != nil || got != "" {
		t.Fatalf("空对象 = %q, %v", got, err)
	}
	if _, err := readObj(t, e, prj, lfs.Pointer{OID: strings.Repeat("0", 64)}); errCode(err) != "lfs_corrupt" {
		t.Fatalf("oid 不符的空对象 = %v", err)
	}
	if e.lfsBatches() != 0 {
		t.Fatal("空对象不应访问上游")
	}
}

func TestLFSOtherHostThroughGuard(t *testing.T) {
	e, prj := lfsEnv(t, nil)
	var hits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	t.Cleanup(other.Close)
	e.fake.Lock()
	e.fake.LFSHrefOrigin = other.URL
	e.fake.Unlock()
	p := putObj(e, "elsewhere\n")
	if _, err := readObj(t, e, prj, p); errCode(err) != "lfs_unreachable" {
		t.Fatalf("内网对象存储 = %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("守卫应拦下请求，对象存储收到 %d 个", hits.Load())
	}
}

func TestLFSEchoAuthSameOrigin(t *testing.T) {
	e, prj := lfsEnv(t, nil)
	e.fake.Lock()
	e.fake.LFSEchoAuth = true
	e.fake.Unlock()
	p := putObj(e, "echoed\n")
	if got, err := readObj(t, e, prj, p); err != nil || got != "echoed\n" {
		t.Fatalf("lfsObject = %q, %v", got, err)
	}
	objs := e.fake.CallsTo("GET", "/_lfs/objects/"+p.OID)
	if len(objs) != 1 || !strings.HasPrefix(objs[0].Auth, "Basic ") {
		t.Fatalf("同源对象请求应带回显的头：%+v", objs)
	}
}

func TestLFSActionHeaderFilter(t *testing.T) {
	ep, _ := url.Parse("https://git.example/a/b.git/info/lfs")
	other, _ := url.Parse("https://objects.example/x?sig=1")
	same, _ := url.Parse("https://GIT.example:443/media/x")
	h := map[string]string{
		"Authorization": "Basic " + basicAuth(lfsTok),
		"X-Raw":         "token " + lfsTok,
		"X-Basic":       "basic " + base64.StdEncoding.EncodeToString([]byte("user:"+lfsTok)),
		"X-Amz-Date":    "20260101T000000Z",
	}
	got := actionHeader(other, ep, h, lfsTok)
	if len(got) != 1 || got.Get("X-Amz-Date") == "" {
		t.Fatalf("别的主机只应留下 X-Amz-Date：%v", got)
	}
	if got := actionHeader(same, ep, h, lfsTok); len(got) != 4 {
		t.Fatalf("同主机应原样保留：%v", got)
	}
}

func TestLFSCacheEviction(t *testing.T) {
	e, prj := lfsEnv(t, func(c *Config) { c.MaxLFSCacheBytes = 10 })
	a := putObj(e, "0123456789")
	b := putObj(e, "abcdefghij")
	for _, p := range []lfs.Pointer{a, b} {
		if _, err := readObj(t, e, prj, p); err != nil {
			t.Fatal(err)
		}
	}
	if files := cacheFiles(t, e, prj.ID); len(files) != 1 || files[0] != b.OID {
		t.Fatalf("缓存应只剩第二个对象：%v", files)
	}
}

func TestLFSRemoveDropsCache(t *testing.T) {
	e, prj := lfsEnv(t, nil)
	if _, err := readObj(t, e, prj, putObj(e, "bye\n")); err != nil {
		t.Fatal(err)
	}
	e.svc.Remove(prj.ID)
	if _, err := os.Stat(filepath.Join(e.svc.cfg.Dir, ".lfs-cache", prj.ID)); !os.IsNotExist(err) {
		t.Fatalf("缓存目录仍在：%v", err)
	}
}

func TestLFSFlightTimeout(t *testing.T) {
	e, prj := lfsEnv(t, func(c *Config) { c.LFSFlightTimeout = 200 * time.Millisecond })
	e.fake.Lock()
	e.fake.LFSStall = true
	e.fake.Unlock()
	p := putObj(e, "never arrives\n")
	if _, err := readObj(t, e, prj, p); errCode(err) != "lfs_unreachable" {
		t.Fatalf("挂起的上游 = %v", err)
	}
	if files := cacheFiles(t, e, prj.ID); len(files) != 0 {
		t.Fatalf("留下了临时文件 %v", files)
	}
}

func TestLFSPerProjectSlots(t *testing.T) {
	e, prj := lfsEnv(t, nil)
	e.fake.Lock()
	e.fake.LFSStall = true
	e.fake.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	for i := range 4 {
		p := putObj(e, "object "+string(rune('a'+i)))
		wg.Go(func() { _, _ = e.svc.lfsObject(ctx, prj, p) })
	}
	waitFor(t, func() bool { return e.fake.LFSActive() == 2 })
	time.Sleep(200 * time.Millisecond)
	if e.fake.LFSPeak() != 2 {
		t.Fatalf("同一项目同时在途 %d 个对象请求，应为 %d", e.fake.LFSPeak(), lfsPerProject)
	}
	// 另一个项目不被挂起的项目卡住
	id := e.project(t, "https://github.com/acme/widgets")
	other, err := e.st.Q.GetProject(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	p := putObj(e, "other project")
	wg.Go(func() { _, _ = e.svc.lfsObject(ctx, other, p) })
	waitFor(t, func() bool { return e.fake.LFSActive() == 3 })
}

func TestLFSNoSpace(t *testing.T) {
	e, prj := lfsEnv(t, func(c *Config) { c.MinFreeBytes = 1 << 62 })
	if _, err := readObj(t, e, prj, putObj(e, "full disk\n")); errCode(err) != "lfs_no_space" {
		t.Fatalf("磁盘不足 = %v", err)
	}
	if e.lfsBatches() != 0 {
		t.Fatal("磁盘不足时不应访问上游")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("等待超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
