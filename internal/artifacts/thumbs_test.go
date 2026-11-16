package artifacts

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

// TestMain 让测试二进制自己充当 `orynd render-exec`（Chromium 集成用例的渲染器 Exe 指向测试二进制）。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == render.ExecCommand {
		os.Exit(render.ExecMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

// fakeRenderer 回一张按窗口与倍率算出尺寸的纯色 PNG；block 非空时每次渲染先发 started 再等 block。
type fakeRenderer struct {
	mu      sync.Mutex
	jobs    []render.Job
	runs    atomic.Int32
	down    string
	started chan struct{}
	block   chan struct{}
}

func (f *fakeRenderer) Status() (bool, string) {
	if f.down != "" {
		return false, f.down
	}
	return true, ""
}

func (f *fakeRenderer) Run(ctx context.Context, j render.Job) ([]byte, error) {
	if f.down != "" {
		return nil, fmt.Errorf("%w：%s", render.ErrUnavailable, f.down)
	}
	f.runs.Add(1)
	f.mu.Lock()
	f.jobs = append(f.jobs, j)
	f.mu.Unlock()
	if f.block != nil {
		f.started <- struct{}{}
		<-f.block
	}
	scale := j.Scale
	if scale == 0 {
		scale = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, int(float64(j.Width)*scale), int(float64(j.Height)*scale)))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (f *fakeRenderer) lastJob() render.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs[len(f.jobs)-1]
}

func withRenderer(e *env, r Renderer) {
	e.s.thumbs.mu.Lock()
	e.s.thumbs.r = r
	e.s.thumbs.mu.Unlock()
}

// fakeAfter 记下延迟调用，由测试手动触发。
type fakeAfter struct {
	mu    sync.Mutex
	calls []*thumbAfterCall
}

type thumbAfterCall struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (a *fakeAfter) after(d time.Duration, f func()) func() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := &thumbAfterCall{d: d, f: f}
	a.calls = append(a.calls, t)
	return func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

func (e *env) thumbFiles(t *testing.T) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(e.dir, "artifacts", "thumbs", "*.jpg"))
	require.NoError(t, err)
	return m
}

func TestThumbRenderJPEG(t *testing.T) {
	e := newEnv(t)
	r := &fakeRenderer{}
	withRenderer(e, r)
	ctx := context.Background()
	a := mkPage(t, e)

	before := e.hub.changed
	require.NoError(t, e.s.ensureThumb(ctx, a.ID, 1))
	j := r.lastJob()
	assert.Equal(t, render.Job{ArtifactID: a.ID, Version: 1, Width: 1280, Height: 800, Scale: 0.5625}, j)
	p, served, err := e.s.Thumbnail(ctx, a.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, served)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	require.NoError(t, err)
	assert.Equal(t, 720, cfg.Width)
	assert.Equal(t, 450, cfg.Height)
	assert.Equal(t, before+1, e.hub.changed, "渲染完成后发一次 artifacts.changed")

	// 已有就不再渲染
	require.NoError(t, e.s.ensureThumb(ctx, a.ID, 1))
	assert.EqualValues(t, 1, r.runs.Load())

	// 非网页类型带 view=thumbnail
	d, err := e.s.Create(ctx, CreateParams{Title: "画布", Files: []FileChange{
		put("canvas.json", `{"pages":[{"id":"a","name":"A","file":"a.html"}]}`), put("a.html", "<p>a</p>"),
	}})
	require.NoError(t, err)
	require.NoError(t, e.s.ensureThumb(ctx, d.ID, 0))
	assert.Equal(t, "view=thumbnail", r.lastJob().Query)
}

// 同一 files_key 的两次并发请求只渲染一次。
func TestThumbConcurrentOnce(t *testing.T) {
	e := newEnv(t)
	r := &fakeRenderer{started: make(chan struct{}), block: make(chan struct{})}
	withRenderer(e, r)
	joined := make(chan struct{})
	e.s.thumbs.joined = func() { close(joined) }
	a := mkPage(t, e)
	ctx := context.Background()

	errs := make(chan error, 2)
	go func() { errs <- e.s.ensureThumb(ctx, a.ID, 1) }()
	<-r.started
	go func() { errs <- e.s.ensureThumb(ctx, a.ID, 1) }()
	<-joined
	close(r.block)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	assert.EqualValues(t, 1, r.runs.Load())
	assert.Len(t, e.thumbFiles(t), 1)
}

// 第 MaxThumbs+1 张把最旧的一张挤掉。
func TestThumbEvictsOldest(t *testing.T) {
	e := newEnv(t)
	withRenderer(e, &fakeRenderer{})
	dir := filepath.Join(e.dir, "artifacts", "thumbs")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	old := time.Now().Add(-48 * time.Hour)
	for i := range MaxThumbs {
		p := filepath.Join(dir, fmt.Sprintf("k%04d.jpg", i))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		mt := old.Add(time.Duration(i) * time.Second)
		require.NoError(t, os.Chtimes(p, mt, mt))
	}
	a := mkPage(t, e)
	require.NoError(t, e.s.ensureThumb(context.Background(), a.ID, 1))
	files := e.thumbFiles(t)
	assert.Len(t, files, MaxThumbs)
	assert.NoFileExists(t, filepath.Join(dir, "k0000.jpg"))
	assert.FileExists(t, filepath.Join(dir, "k0001.jpg"))
	_, _, err := e.s.Thumbnail(context.Background(), a.ID, 1)
	assert.NoError(t, err)
}

// 请求的版本没有图时回最近一个有图的更早版本，并把请求的版本补入队；都没有回 no_thumbnail。
func TestThumbFallback(t *testing.T) {
	e := newEnv(t)
	withRenderer(e, &fakeRenderer{})
	ctx := context.Background()
	a := mkPage(t, e)

	_, _, err := e.s.Thumbnail(ctx, a.ID, 0)
	assert.Equal(t, CodeNoThumbnail, code(err))
	oe, _ := AsOpError(err)
	assert.Equal(t, 404, oe.Status())
	id, n, _, ok := e.s.nextThumb()
	require.True(t, ok, "未命中时补入队")
	assert.Equal(t, a.ID, id)
	assert.Equal(t, 1, n)

	for i := 2; i <= 3; i++ {
		_, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: i - 1, Files: []FileChange{put("index.html", fmt.Sprintf("<h1>v%d</h1>", i))}})
		require.NoError(t, err)
	}
	require.NoError(t, e.s.ensureThumb(ctx, a.ID, 2))
	_, served, err := e.s.Thumbnail(ctx, a.ID, 3)
	require.NoError(t, err)
	assert.Equal(t, 2, served)
	_, served, err = e.s.Thumbnail(ctx, a.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, served)
	_, _, err = e.s.Thumbnail(ctx, a.ID, 1)
	assert.Equal(t, CodeNoThumbnail, code(err))
	_, _, err = e.s.Thumbnail(ctx, a.ID, 9)
	assert.Equal(t, CodeNotFound, code(err))
	id, n, _, ok = e.s.nextThumb()
	require.True(t, ok)
	assert.Equal(t, a.ID, id)
	assert.Equal(t, 3, n, "同一 artifact 在队列里只保留最新的版本")
	_, _, _, ok = e.s.nextThumb()
	assert.False(t, ok)

	// 渲染不可用时不补入队
	withRenderer(e, &fakeRenderer{down: "自检没通过"})
	_, _, _ = e.s.Thumbnail(ctx, a.ID, 3)
	_, _, _, ok = e.s.nextThumb()
	assert.False(t, ok)
}

// 新版本后延迟入队；同一 artifact 连续写入只保留最后一次。
func TestThumbScheduledAfterVersion(t *testing.T) {
	e := newEnv(t)
	withRenderer(e, &fakeRenderer{})
	fa := &fakeAfter{}
	e.s.thumbs.after = fa.after
	e.s.OnVersion(e.s.scheduleThumb)
	ctx := context.Background()

	a := mkPage(t, e)
	_, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("index.html", "<h1>v2</h1>")}})
	require.NoError(t, err)
	require.Len(t, fa.calls, 2)
	assert.Equal(t, thumbDelay, fa.calls[0].d)
	assert.True(t, fa.calls[0].stopped, "第二个版本取消第一次的延迟入队")
	_, _, _, ok := e.s.nextThumb()
	assert.False(t, ok, "延迟到点之前不入队")

	fa.calls[1].f()
	id, n, _, ok := e.s.nextThumb()
	require.True(t, ok)
	assert.Equal(t, a.ID, id)
	assert.Equal(t, 2, n)
	require.NoError(t, e.s.ensureThumb(ctx, id, n))
	_, served, err := e.s.Thumbnail(ctx, a.ID, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, served)
}

// worker 处理队列：入队之后渲染出缩略图；ctx 结束时退出。
func TestThumbWorker(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r := &fakeRenderer{started: make(chan struct{}), block: make(chan struct{})}
	withRenderer(e, r)
	go func() { e.s.thumbWorker(ctx); close(done) }()
	a := mkPage(t, e)
	e.s.enqueueThumb(a.ID, 1)
	<-r.started
	close(r.block)
	cancel()
	<-done
	assert.EqualValues(t, 1, r.runs.Load())
}

func TestScreenshot(t *testing.T) {
	e := newEnv(t)
	r := &fakeRenderer{}
	withRenderer(e, r)
	mkThread(t, e.st, "thr_a", "")
	mkThread(t, e.st, "thr_b", "")
	ca, cb := threadCaller("thr_a"), threadCaller("thr_b")
	created := textObj(t, mustCall(t, e.s, ca, OpCreate, `{"title":"页","html":"<h1>hi</h1>"}`))
	id := created["id"].(string)

	res := mustCall(t, e.s, ca, OpScreenshot, `{"id":"`+id+`","width":400,"height":300}`)
	assert.Equal(t, id+" v1 的 index.html，400×300", res.Text)
	require.NotNil(t, res.Image)
	assert.Equal(t, "image/png", res.Image.MimeType)
	raw, err := base64.StdEncoding.DecodeString(res.Image.Data)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 400, 300), img.Bounds())
	assert.Equal(t, render.Job{ArtifactID: id, Version: 1, Width: 400, Height: 300, Scale: 1}, r.lastJob())

	// 缺省 1280×800；path 必须在最新版本里
	res = mustCall(t, e.s, ca, OpScreenshot, `{"id":"`+id+`","path":"index.html"}`)
	assert.Equal(t, id+" v1 的 index.html，1280×800", res.Text)
	_, err = agentCall(t, e.s, ca, OpScreenshot, `{"id":"`+id+`","path":"nope.html"}`)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = agentCall(t, e.s, ca, OpScreenshot, `{"id":"`+id+`","width":1601}`)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = agentCall(t, e.s, ca, OpScreenshot, `{"id":"`+id+`","height":4001}`)
	assert.Equal(t, CodeInvalid, code(err))

	// 范围之外 not_found
	_, err = agentCall(t, e.s, cb, OpScreenshot, `{"id":"`+id+`"}`)
	assert.Equal(t, CodeNotFound, code(err))

	// 渲染不可用：render_unavailable，并告诉 agent 不要重试
	withRenderer(e, &fakeRenderer{down: "找不到 Chromium"})
	_, err = agentCall(t, e.s, ca, OpScreenshot, `{"id":"`+id+`"}`)
	require.Equal(t, CodeRenderUnavailable, code(err))
	oe, _ := AsOpError(err)
	assert.Contains(t, oe.Message, "找不到 Chromium")
	assert.Contains(t, oe.Message, "不要重试")
	withRenderer(e, nil)
	_, err = agentCall(t, e.s, ca, OpScreenshot, `{"id":"`+id+`"}`)
	assert.Equal(t, CodeRenderUnavailable, code(err))
}

// 真渲染一张缩略图（ORYN_TEST_CHROMIUM=1）：得到 720×450 的 JPEG。
func TestThumbChromium(t *testing.T) {
	if os.Getenv("ORYN_TEST_CHROMIUM") != "1" {
		t.Skip("set ORYN_TEST_CHROMIUM=1 to run Chromium integration tests")
	}
	e := newEnv(t)
	h := newHost(t, e, "localhost", noAsset)
	cfg := render.Config{Site: h, CDNs: CDNs}
	if os.Geteuid() == 0 {
		if _, err := user.Lookup("nobody"); err != nil {
			t.Skip("root without a nobody account")
		}
		cfg.User = "nobody"
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	cfg.Exe = exe
	// 渲染账号要能进入作业目录的上级：/tmp 下自建一个 0711 的目录（t.TempDir 是 0700）
	base, err := os.MkdirTemp("", "oryn-render-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	require.NoError(t, os.Chmod(base, 0o711))
	cfg.Dir = filepath.Join(base, "jobs")
	r, err := render.New(cfg)
	require.NoError(t, err)
	t.Cleanup(r.Close)
	ctx := context.Background()
	r.Preflight(ctx)
	ok, reason := r.Status()
	require.True(t, ok, reason)
	withRenderer(e, r)

	a := mkPage(t, e, put("index.html", `<!doctype html><html><body style="margin:0;background:#fff">`+
		`<h1 style="color:#f00;font-size:120px">红色标题</h1></body></html>`))
	require.NoError(t, e.s.ensureThumb(ctx, a.ID, 1))
	p, served, err := e.s.Thumbnail(ctx, a.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, served)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	img, err := jpeg.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 720, 450), img.Bounds())
	red := 0
	for y := 0; y < 450; y += 2 {
		for x := 0; x < 720; x += 2 {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 > 0xc0 && cg>>8 < 0x60 && cb>>8 < 0x60 {
				red++
			}
		}
	}
	assert.Positive(t, red, "缩略图里应有红色标题")
}

// 渲染模式下类型化 artifact 的根是查看器页（不注入运行时），查看器资源从 _oryn/viewer/ 提供。
func TestRenderSiteViewer(t *testing.T) {
	e := newEnv(t)
	assets := func(name string) ([]byte, bool) {
		if name == "sandbox/viewers/document.js" {
			return []byte("console.log(1)"), true
		}
		return fakeRuntime(name)
	}
	h := newHost(t, e, "localhost", assets)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "文档", Type: TypeDocument, Files: []FileChange{put("document.md", "# 标题\n")}})
	require.NoError(t, err)
	self := "http://" + render.Host + "/tok/"
	serve := func(p string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok"+p+"?view=thumbnail", nil)
		rec := httptest.NewRecorder()
		h.ServeRender(rec, req, render.Target{ArtifactID: a.ID, Version: 1, Self: self}, p)
		return rec
	}
	rec := serve("/")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `id="oryn-viewer"`)
	assert.Contains(t, body, self+"_oryn/viewer/document.js")
	assert.NotContains(t, body, "__orynStart")
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))

	rec = serve("/_oryn/viewer/document.js")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "console.log(1)", rec.Body.String())
	assert.Equal(t, http.StatusNotFound, serve("/_oryn/viewer/evil.js").Code)

	rec = serve("/document.md")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "# 标题\n", rec.Body.String())
}
