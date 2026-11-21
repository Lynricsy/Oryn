package repos

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/lfs"
)

// lfsServerEnv 是一个带初始提交的托管仓库；PublicURL = https://oryn.test。
type lfsServerEnv struct {
	*testEnv
	id, dir string
}

func newLFSServerEnv(t *testing.T) *lfsServerEnv {
	t.Helper()
	e := newTestEnv(t, func(c *Config) { c.PublicURL = "https://oryn.test" }, nil, nil)
	id := e.hostedProject(t, "main")
	_, err := e.svc.InitHosted(context.Background(), id, "main", true, testIdentity)
	require.NoError(t, err)
	return &lfsServerEnv{testEnv: e, id: id, dir: e.svc.dir(id)}
}

// do 经 ServeHostedGit 发一个 LFS 请求。
func (e *lfsServerEnv) do(method, endpoint string, body io.Reader, set func(*http.Request), pusher egress.HostedPusher) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/r.git/"+endpoint, body)
	if set != nil {
		set(r)
	}
	w := httptest.NewRecorder()
	e.svc.ServeHostedGit(w, r, e.id, endpoint, pusher)
	return w
}

// batch 发一个批量请求，返回响应（200 时解出 out）。
func (e *lfsServerEnv) batch(t *testing.T, op string, objs []lfs.ObjectSpec, hashAlgo string, set func(*http.Request), pusher egress.HostedPusher) (*httptest.ResponseRecorder, lfs.BatchResponse) {
	t.Helper()
	b, err := json.Marshal(lfs.BatchRequest{Operation: op, Transfers: []string{"basic"}, Objects: objs, HashAlgo: hashAlgo})
	require.NoError(t, err)
	w := e.do(http.MethodPost, lfsBatchPath, strings.NewReader(string(b)), func(r *http.Request) {
		r.Header.Set("Content-Type", lfs.MediaType+"; charset=utf-8")
		if set != nil {
			set(r)
		}
	}, pusher)
	var out lfs.BatchResponse
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		assert.Equal(t, lfs.MediaType, w.Header().Get("Content-Type"))
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
	return w, out
}

func (e *lfsServerEnv) put(oid, body string) *httptest.ResponseRecorder {
	return e.do(http.MethodPut, lfsObjectsPrefix+oid, strings.NewReader(body), nil, egress.HostedPusher{})
}

func (e *lfsServerEnv) objPath(oid string) string {
	return lfs.ObjectPath(filepath.Join(e.dir, "lfs", "objects"), oid)
}

func (e *lfsServerEnv) tmpFiles(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(e.dir, "lfs", "tmp"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, en := range ents {
		names = append(names, en.Name())
	}
	return names
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// 上传：batch 给 upload（authenticated、回显凭据）；PUT 校验摘要；已存在且大小相符时不给 actions；写坏的对象能被重新上传修复。
func TestLFSServerUpload(t *testing.T) {
	e := newLFSServerEnv(t)
	const obj = "hello hosted lfs\n"
	p := lfsPtr(obj)
	spec := []lfs.ObjectSpec{{OID: p.OID, Size: p.Size}}
	auth := func(r *http.Request) { r.Header.Set("Authorization", basic(strings.Repeat("a", 2000), "tok")) }

	w, out := e.batch(t, "upload", spec, "", auth, egress.HostedPusher{})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "basic", out.Transfer)
	require.Len(t, out.Objects, 1)
	got := out.Objects[0]
	assert.True(t, got.Authenticated)
	require.Contains(t, got.Actions, "upload")
	up := got.Actions["upload"]
	assert.Equal(t, "https://oryn.test/git/"+e.id+".git/info/lfs/objects/"+p.OID, up.Href)
	assert.Equal(t, basic("oryn", "tok"), up.Header["Authorization"], "回显重新拼成的凭据，不照抄超长用户名")
	assert.Equal(t, int64(lfsHrefTTL), up.ExpiresIn)
	assert.NotContains(t, w.Body.String(), strings.Repeat("a", 100))

	// 内容与 oid 不符 → 422，没有对象也没有临时文件
	w = e.put(p.OID, strings.Repeat("x", len(obj)))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "sha256")
	assert.NoFileExists(t, e.objPath(p.OID))
	assert.Empty(t, e.tmpFiles(t))

	w = e.put(p.OID, obj)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	b, err := os.ReadFile(e.objPath(p.OID))
	require.NoError(t, err)
	assert.Equal(t, obj, string(b))
	assert.Empty(t, e.tmpFiles(t))

	// 已有且大小相符 → 没有 actions
	_, out = e.batch(t, "upload", spec, "", nil, egress.HostedPusher{})
	require.Len(t, out.Objects, 1)
	assert.Nil(t, out.Objects[0].Actions)
	assert.Nil(t, out.Objects[0].Error)

	// 写坏（截断成 1 字节）→ 又给 upload；重新上传后恢复
	require.NoError(t, os.WriteFile(e.objPath(p.OID), []byte("h"), 0o600))
	_, out = e.batch(t, "upload", spec, "", nil, egress.HostedPusher{})
	require.Contains(t, out.Objects[0].Actions, "upload")
	assert.Empty(t, out.Objects[0].Actions["upload"].Header, "请求没带凭据时不回显")
	require.Equal(t, http.StatusOK, e.put(p.OID, obj).Code)
	b, err = os.ReadFile(e.objPath(p.OID))
	require.NoError(t, err)
	assert.Equal(t, obj, string(b))

	// 来自 orb 的 batch：传输地址换成 http（经出站代理的托管通道）
	_, out = e.batch(t, "download", spec, "", nil, egress.HostedPusher{OrbID: "orb_1", ThreadID: "T"})
	require.Contains(t, out.Objects[0].Actions, "download")
	assert.Equal(t, "http://oryn.test/git/"+e.id+".git/info/lfs/objects/"+p.OID, out.Objects[0].Actions["download"].Href)
}

// 下载：download batch、GET 的头与 Range、不存在的对象。
func TestLFSServerDownload(t *testing.T) {
	e := newLFSServerEnv(t)
	const obj = "0123456789"
	p := lfsPtr(obj)
	require.Equal(t, http.StatusOK, e.put(p.OID, obj).Code)
	missing := lfsPtr("nope")

	_, out := e.batch(t, "download", []lfs.ObjectSpec{{OID: p.OID, Size: p.Size}, {OID: missing.OID, Size: 4}, {OID: p.OID, Size: 3}}, "", nil, egress.HostedPusher{})
	require.Len(t, out.Objects, 3)
	require.Contains(t, out.Objects[0].Actions, "download")
	assert.Equal(t, "https://oryn.test/git/"+e.id+".git/info/lfs/objects/"+p.OID, out.Objects[0].Actions["download"].Href)
	require.NotNil(t, out.Objects[1].Error)
	assert.Equal(t, http.StatusNotFound, out.Objects[1].Error.Code)
	assert.Nil(t, out.Objects[1].Actions)
	require.NotNil(t, out.Objects[2].Error)
	assert.Equal(t, http.StatusUnprocessableEntity, out.Objects[2].Error.Code)

	w := e.do(http.MethodGet, lfsObjectsPrefix+p.OID, nil, nil, egress.HostedPusher{})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, obj, w.Body.String())
	assert.Equal(t, "application/octet-stream", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "attachment", w.Header().Get("Content-Disposition"))
	assert.Equal(t, `"`+p.OID+`"`, w.Header().Get("ETag"))

	w = e.do(http.MethodGet, lfsObjectsPrefix+p.OID, nil, func(r *http.Request) { r.Header.Set("Range", "bytes=0-3") }, egress.HostedPusher{})
	assert.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, "0123", w.Body.String())

	w = e.do(http.MethodGet, lfsObjectsPrefix+missing.OID, nil, nil, egress.HostedPusher{})
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, lfs.MediaType, w.Header().Get("Content-Type"))

	// 不合法的 oid（含 ..）不碰文件系统：直接调 serveLFS 也是 404
	prj, err := e.svc.project(context.Background(), e.id)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	e.svc.serveLFS(rec, httptest.NewRequest(http.MethodGet, "/x", nil), prj, e.dir, "info/lfs/objects/../../config", egress.HostedPusher{})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Body.String(), "bare")
}

// 请求级与对象级的拒绝：锁接口 404、方法、类型、对象数、hash_algo、归档只读、Content-Length、磁盘保护。
func TestLFSServerRejects(t *testing.T) {
	e := newLFSServerEnv(t)
	p := lfsPtr("abc")
	spec := []lfs.ObjectSpec{{OID: p.OID, Size: p.Size}}

	for _, ep := range []string{lfsLocksPath + "/verify", lfsLocksPath, lfsLocksPath + "/l1/unlock"} {
		w := e.do(http.MethodPost, ep, strings.NewReader("{}"), nil, egress.HostedPusher{})
		assert.Equal(t, http.StatusNotFound, w.Code, ep)
		assert.Equal(t, lfs.MediaType, w.Header().Get("Content-Type"))
		assert.JSONEq(t, `{"message":"oryn: 不支持 Git LFS 文件锁"}`, w.Body.String())
	}

	w := e.do(http.MethodGet, lfsBatchPath, nil, nil, egress.HostedPusher{})
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, http.MethodPost, w.Header().Get("Allow"))
	w = e.do(http.MethodPost, lfsBatchPath, strings.NewReader("{}"), func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, egress.HostedPusher{})
	assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
	w = e.do(http.MethodPost, lfsBatchPath, strings.NewReader("{"), func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, egress.HostedPusher{})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	w, _ = e.batch(t, "verify", spec, "", nil, egress.HostedPusher{})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	w, _ = e.batch(t, "download", nil, "", nil, egress.HostedPusher{})
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	many := make([]lfs.ObjectSpec, lfs.BatchMaxObjects+1)
	for i := range many {
		many[i] = lfs.ObjectSpec{OID: p.OID, Size: 1}
	}
	w, _ = e.batch(t, "download", many, "", nil, egress.HostedPusher{})
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)

	_, out := e.batch(t, "upload", spec, "sha512", nil, egress.HostedPusher{})
	require.NotNil(t, out.Objects[0].Error)
	assert.Equal(t, http.StatusConflict, out.Objects[0].Error.Code)
	_, out = e.batch(t, "upload", []lfs.ObjectSpec{{OID: "../x", Size: 1}, {OID: p.OID, Size: -1}}, "", nil, egress.HostedPusher{})
	assert.Equal(t, http.StatusUnprocessableEntity, out.Objects[0].Error.Code)
	assert.Equal(t, http.StatusUnprocessableEntity, out.Objects[1].Error.Code)

	// 分块上传（没有 Content-Length）→ 411，不留临时文件
	w = e.do(http.MethodPut, lfsObjectsPrefix+p.OID, strings.NewReader("abc"), func(r *http.Request) { r.ContentLength = -1 }, egress.HostedPusher{})
	assert.Equal(t, http.StatusLengthRequired, w.Code)
	assert.Empty(t, e.tmpFiles(t))
	w = e.do(http.MethodPut, lfsObjectsPrefix+p.OID, strings.NewReader("abc"), func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, egress.HostedPusher{})
	assert.Equal(t, http.StatusUnsupportedMediaType, w.Code)
	w = e.do(http.MethodDelete, lfsObjectsPrefix+p.OID, nil, nil, egress.HostedPusher{})
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)

	// 磁盘保护
	e.svc.cfg.MinFreeBytes = 1 << 62
	w = e.put(p.OID, "abc")
	assert.Equal(t, http.StatusInsufficientStorage, w.Code)
	assert.NoFileExists(t, e.objPath(p.OID))
	e.svc.cfg.MinFreeBytes = 0

	// 归档后只读：upload batch 与 PUT 403，下载照常
	require.Equal(t, http.StatusOK, e.put(p.OID, "abc").Code)
	e.exec(t, "UPDATE projects SET archived_at = 1 WHERE id = ?", e.id)
	w, _ = e.batch(t, "upload", spec, "", nil, egress.HostedPusher{})
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "项目已归档，仓库只读")
	assert.Equal(t, http.StatusForbidden, e.put(p.OID, "abc").Code)
	w, out = e.batch(t, "download", spec, "", nil, egress.HostedPusher{})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, out.Objects[0].Actions, "download")

	// 非托管项目 → 404 JSON
	plain := e.project(t, "")
	rec := httptest.NewRecorder()
	e.svc.ServeHostedGit(rec, httptest.NewRequest(http.MethodPost, "/x", nil), plain, lfsBatchPath, egress.HostedPusher{})
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, lfs.MediaType, rec.Header().Get("Content-Type"))
}

// 请求体比 Content-Length 短：客户端关闭写端 → 400；客户端直接断开 → 处理结束后不留临时文件。
func TestLFSServerShortBody(t *testing.T) {
	e := newLFSServerEnv(t)
	p := lfsPtr("0123456789")
	done := make(chan int, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		e.svc.ServeHostedGit(rec, r, e.id, strings.TrimPrefix(r.URL.Path, "/r.git/"), egress.HostedPusher{})
		done <- rec.status
	}))
	defer srv.Close()

	send := func(closeWrite bool) (net.Conn, error) {
		c, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			return nil, err
		}
		_, err = fmt.Fprintf(c, "PUT /r.git/%s%s HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n01234", lfsObjectsPrefix, p.OID)
		if err != nil {
			return nil, err
		}
		if closeWrite {
			err = c.(*net.TCPConn).CloseWrite()
		} else {
			err = c.Close()
		}
		return c, err
	}

	c, err := send(true)
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	_ = c.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("处理没有结束")
	}
	assert.Empty(t, e.tmpFiles(t))

	_, err = send(false)
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("客户端断开后处理没有结束")
	}
	assert.Empty(t, e.tmpFiles(t))
	assert.NoFileExists(t, e.objPath(p.OID))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// 对象传输名额：每台 orb lfsServeOrbSlots 个，没有名额时排队；名额释放后排队的拿到，等满上限报忙，ctx 取消即返回。
func TestLFSServerSlots(t *testing.T) {
	var l lfsSlots
	l.wait = 100 * time.Millisecond
	ctx := context.Background()
	var rel []func()
	for range lfsServeOrbSlots {
		r, err := l.acquire(ctx, "orb-a")
		require.NoError(t, err)
		rel = append(rel, r)
	}
	_, err := l.acquire(ctx, "orb-a")
	assert.ErrorIs(t, err, errLFSSlotBusy)
	// 别的 orb 与主人本机不受影响
	r, err := l.acquire(ctx, "orb-b")
	require.NoError(t, err)
	r()
	r, err = l.acquire(ctx, "")
	require.NoError(t, err)
	r()

	l.wait = 5 * time.Second
	got := make(chan error, 1)
	go func() {
		r, err := l.acquire(ctx, "orb-a")
		if err == nil {
			r()
		}
		got <- err
	}()
	time.Sleep(50 * time.Millisecond)
	rel[0]()
	rel[0]() // 重复释放无害
	select {
	case err := <-got:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("名额释放后排队的没有拿到")
	}

	cctx, cancel := context.WithCancel(ctx)
	rel = rel[1:]
	r, err = l.acquire(ctx, "orb-a")
	require.NoError(t, err)
	rel = append(rel, r)
	go func() {
		_, err := l.acquire(cctx, "orb-a")
		got <- err
	}()
	cancel()
	select {
	case err := <-got:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后排队的没有返回")
	}
	for _, r := range rel {
		r()
	}
	assert.Zero(t, l.total)
	assert.Empty(t, l.byOrb)
}

// Run 启动时清掉托管仓库的 lfs/tmp。
func TestLFSServerSweepTmp(t *testing.T) {
	e := newLFSServerEnv(t)
	tmp := filepath.Join(e.dir, "lfs", "tmp")
	require.NoError(t, os.MkdirAll(tmp, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "x-1"), []byte("half"), 0o600))
	e.svc.sweep(context.Background())
	assert.NoDirExists(t, tmp)
	assert.DirExists(t, e.dir)
}
