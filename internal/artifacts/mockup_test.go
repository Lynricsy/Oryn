package artifacts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/Lynricsy/Oryn/internal/webui"
)

const mockupDesignCanvasJSON = `{
	"format": 2,
	"design": {"engine": 1, "start": "home"},
	"canvases": [{"id": "c1", "name": "主画布"}],
	"pages": [
		{"id": "home", "canvas": "c1", "name": "首页", "file": "dist/home.html", "source": "src/frames/home.tsx", "device": "browser", "x": 0, "y": 0, "width": 1280, "height": 800, "locked": false},
		{"id": "mobile", "canvas": "c1", "name": "移动端", "file": "dist/mobile.html", "source": "src/frames/mobile.tsx", "device": "phone", "x": 1328, "y": 0, "width": 390, "height": 844, "locked": false},
		{"id": "pad", "canvas": "c1", "name": "平板", "file": "dist/pad.html", "source": "src/frames/pad.tsx", "device": "tablet", "x": 1766, "y": 0, "width": 820, "height": 1180, "locked": false}
	],
	"notes": []
}`

func mkMockupCanvas(t *testing.T, e *env, extra ...FileChange) Artifact {
	t.Helper()
	files := append([]FileChange{
		put("canvas.json", mockupDesignCanvasJSON),
		put("dist/home.html", "<!doctype html><html><body style='margin:0;background:#fff'><h1>Home</h1></body></html>"),
		put("dist/mobile.html", "<!doctype html><html><body style='margin:0;background:#fff'><h1>Mobile</h1></body></html>"),
		put("dist/pad.html", "<!doctype html><html><body style='margin:0;background:#fff'><h1>Pad</h1></body></html>"),
	}, extra...)
	a, err := e.s.Create(context.Background(), CreateParams{Title: "产品/原型", Files: files})
	require.NoError(t, err)
	require.Equal(t, KindCanvas, a.Kind)
	return a
}

// mockupFakeRenderer 支持同时返回 DumpDOM 探针结果与 PNG/PDF 渲染字节。
type mockupFakeRenderer struct {
	mu      sync.Mutex
	down    string
	doms    map[string][]byte
	domErrs map[string]error
	runErr  error
	jobs    []render.Job
}

func (f *mockupFakeRenderer) Status() (bool, string) {
	if f.down != "" {
		return false, f.down
	}
	return true, ""
}

func (f *mockupFakeRenderer) Run(_ context.Context, j render.Job) ([]byte, error) {
	f.mu.Lock()
	f.jobs = append(f.jobs, j)
	down := f.down
	dom := f.doms[j.Path]
	domErr := f.domErrs[j.Path]
	runErr := f.runErr
	f.mu.Unlock()
	if down != "" {
		return nil, fmt.Errorf("%w：%s", render.ErrUnavailable, down)
	}
	if j.DumpDOM {
		if domErr != nil {
			return nil, domErr
		}
		if dom != nil {
			return dom, nil
		}
		return []byte("<html><body></body></html>"), nil
	}
	if runErr != nil {
		return nil, runErr
	}
	if j.PDF {
		return []byte("%PDF-1.4\n%%EOF\n"), nil
	}
	scale := j.Scale
	if scale == 0 {
		scale = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, int(float64(j.Width)*scale), int(float64(j.Height)*scale)))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mockupProbeDOM(frame string, height int) []byte {
	return fmt.Appendf(nil, `<html><body><script id="%s" type="application/json">{"v":1,"frame":%q,"height":%d,"issues":[]}</script></body></html>`, design.ProbeResultID, frame, height)
}

func TestExportMockupValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := mkMockupCanvas(t, e)
	withRenderer(e, &mockupFakeRenderer{})

	// 非画布回 invalid
	page := mkPage(t, e)
	_, err := e.s.ExportWithOptions(ctx, page.ID, 0, ExportMockupPNG, nil)
	assert.Equal(t, CodeInvalid, code(err))
	doc, err := createTyped(t, e, TypeDocument, put("document.md", "# hi\n"))
	require.NoError(t, err)
	_, err = e.s.ExportWithOptions(ctx, doc.ID, 0, ExportMockupPDF, nil)
	assert.Equal(t, CodeInvalid, code(err))
	deck, err := createTyped(t, e, TypeSlides, put("slides.json", deckJSON(t, validDeck())), put("images/a.png", pngBytes))
	require.NoError(t, err)
	_, err = e.s.ExportWithOptions(ctx, deck.ID, 0, ExportMockupPNG, nil)
	assert.Equal(t, CodeInvalid, code(err))

	// 空画布回 invalid
	emptyCanvas, err := e.s.Create(ctx, CreateParams{
		Title: "空画布",
		Files: []FileChange{put("canvas.json", `{"format":1,"canvases":[{"id":"c1","name":"页"}],"pages":[],"notes":[]}`)},
	})
	require.NoError(t, err)
	_, err = e.s.ExportWithOptions(ctx, emptyCanvas.ID, 0, ExportMockupPNG, nil)
	assert.Equal(t, CodeInvalid, code(err))

	// 选项边界校验
	badCases := []struct {
		name string
		mod  func(*MockupOptions)
	}{
		{"scale=0", func(o *MockupOptions) { o.Scale = 0 }},
		{"scale=4", func(o *MockupOptions) { o.Scale = 4 }},
		{"radius=-1", func(o *MockupOptions) { o.Radius = -1 }},
		{"radius=65", func(o *MockupOptions) { o.Radius = 65 }},
		{"bg=empty", func(o *MockupOptions) { o.Bg = "" }},
		{"bg=red", func(o *MockupOptions) { o.Bg = "red" }},
		{"bg=#fff", func(o *MockupOptions) { o.Bg = "#fff" }},
		{"bg=#12345g", func(o *MockupOptions) { o.Bg = "#12345g" }},
		{"unknown frame", func(o *MockupOptions) { o.Frames = []string{"nope"} }},
		{"empty frame id", func(o *MockupOptions) { o.Frames = []string{"home", ""} }},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			o := DefaultMockupOptions()
			tc.mod(&o)
			_, err := e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPNG, &o)
			assert.Equal(t, CodeInvalid, code(err))
		})
	}
}

func TestExportMockupRenderUnavailable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := mkMockupCanvas(t, e)

	// 未配置渲染器
	_, err := e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPNG, nil)
	assert.Equal(t, CodeRenderUnavailable, code(err))

	// Status 不可用
	withRenderer(e, &mockupFakeRenderer{down: "找不到 Chromium"})
	_, err = e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPDF, nil)
	require.Equal(t, CodeRenderUnavailable, code(err))
	oe, _ := AsOpError(err)
	assert.Contains(t, oe.Message, "找不到 Chromium")

	// long 模式下 probe 返回 ErrUnavailable
	o := DefaultMockupOptions()
	o.Long = true
	withRenderer(e, &mockupFakeRenderer{
		domErrs: map[string]error{"dist/home.html": fmt.Errorf("%w：沙箱失效", render.ErrUnavailable)},
	})
	_, err = e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPNG, &o)
	assert.Equal(t, CodeRenderUnavailable, code(err))
}

func TestExportMockupFramesAndFormats(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := mkMockupCanvas(t, e)
	r := &mockupFakeRenderer{}
	withRenderer(e, r)

	// 1. 缺省选项：全部 3 帧、shell=true、scale=2
	// boxW: home(1280+0) + mobile(390+24) + pad(820+32) = 2546; + 2*48 + 2*40 = 2722
	// boxH: max(800+36, 844+60, 1180+48) = 1228; + 2*48 = 1324
	x, err := e.s.Export(ctx, a.ID, 0, ExportMockupPNG)
	require.NoError(t, err)
	assert.Equal(t, "产品_原型-v1-mockup.png", x.Name)
	assert.Equal(t, "image/png", x.ContentType)
	img, err := png.Decode(bytes.NewReader(exportBytes(t, x)))
	require.NoError(t, err)
	assert.Equal(t, image.Point{X: 2722 * 2, Y: 1324 * 2}, img.Bounds().Size())
	require.Len(t, r.jobs, 1)
	j := r.jobs[0]
	assert.Equal(t, 2722, j.Width)
	assert.Equal(t, 1324, j.Height)
	assert.Equal(t, 2.0, j.Scale)
	assert.False(t, j.PDF)
	q, err := url.ParseQuery(j.Query)
	require.NoError(t, err)
	assert.Equal(t, "mockup", q.Get("view"))
	assert.Equal(t, "home,mobile,pad", q.Get("frames"))
	assert.Equal(t, "12", q.Get("radius"))
	assert.Equal(t, "1", q.Get("border"))
	assert.Equal(t, "light", q.Get("bg"))
	assert.Equal(t, "1", q.Get("shadow"))
	assert.Equal(t, "1", q.Get("shell"))

	// 2. 指定帧（含去重）、关闭外壳/边框/阴影、自定义色、scale=3
	o := MockupOptions{
		Frames: []string{"mobile", "home", "mobile"},
		Scale:  3,
		Radius: 0,
		Border: false,
		Bg:     "#1a2b3c",
		Shadow: false,
		Shell:  false,
	}
	// rawW = 96 + 390 + 40 + 1280 = 1806; rawH = 96 + max(844, 800) = 940
	x, err = e.s.ExportWithOptions(ctx, a.ID, 1, ExportMockupPNG, &o)
	require.NoError(t, err)
	img, err = png.Decode(bytes.NewReader(exportBytes(t, x)))
	require.NoError(t, err)
	assert.Equal(t, image.Point{X: 1806 * 3, Y: 940 * 3}, img.Bounds().Size())
	j2 := r.jobs[1]
	assert.Equal(t, 3.0, j2.Scale)
	q2, err := url.ParseQuery(j2.Query)
	require.NoError(t, err)
	assert.Equal(t, "mobile,home", q2.Get("frames"))
	assert.Equal(t, "0", q2.Get("radius"))
	assert.Equal(t, "0", q2.Get("border"))
	assert.Equal(t, "#1a2b3c", q2.Get("bg"))
	assert.Equal(t, "0", q2.Get("shadow"))
	assert.Equal(t, "0", q2.Get("shell"))

	// 3. PDF 分支：Scale 固定 1，PDF=true，文件名与 Content-Type 正确
	oPDF := DefaultMockupOptions()
	oPDF.Frames = []string{"mobile"}
	oPDF.Scale = 3
	oPDF.Bg = "transparent"
	x, err = e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPDF, &oPDF)
	require.NoError(t, err)
	assert.Equal(t, "产品_原型-v1-mockup.pdf", x.Name)
	assert.Equal(t, "application/pdf", x.ContentType)
	assert.True(t, bytes.HasPrefix(exportBytes(t, x), []byte("%PDF-")))
	j3 := r.jobs[2]
	assert.True(t, j3.PDF)
	assert.Equal(t, 1.0, j3.Scale)
	// mobile(390+24=414, 844+60=904) + 96 -> 510 x 1000
	assert.Equal(t, 510, j3.Width)
	assert.Equal(t, 1000, j3.Height)
}

func TestExportMockupLongModeAndClamp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := mkMockupCanvas(t, e)
	r := &mockupFakeRenderer{
		doms: map[string][]byte{
			"dist/home.html":   mockupProbeDOM("home", 15000), // 超过 8000，应被钳制
			"dist/mobile.html": mockupProbeDOM("mobile", 2400),
		},
		domErrs: map[string]error{
			"dist/pad.html": errors.New("probe timeout"), // 探针失败应回退声明高度 1180
		},
	}
	withRenderer(e, r)

	o := DefaultMockupOptions()
	o.Long = true
	o.Scale = 1
	x, err := e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPNG, &o)
	require.NoError(t, err)
	assert.NotEmpty(t, exportBytes(t, x))

	// 前 3 个是 DumpDOM 探针作业，最后 1 个是样机渲染作业
	require.Len(t, r.jobs, 4)
	dumpPaths := map[string]render.Job{}
	for _, j := range r.jobs[:3] {
		assert.True(t, j.DumpDOM)
		assert.Equal(t, design.ProbeQuery+"=1", j.Query)
		dumpPaths[j.Path] = j
	}
	assert.Contains(t, dumpPaths, "dist/home.html")
	assert.Contains(t, dumpPaths, "dist/mobile.html")
	assert.Contains(t, dumpPaths, "dist/pad.html")

	final := r.jobs[3]
	assert.False(t, final.DumpDOM)
	// home 为 browser(+36)，maxFrameH = 8000 - 96 - 36 = 7868，总高恰为 8000
	assert.Equal(t, 8000, final.Height)
	q, err := url.ParseQuery(final.Query)
	require.NoError(t, err)
	assert.Equal(t, "home:7868,mobile:2400,pad:1180", q.Get("heights"))
}

func TestMockupPageServedAtRenderRoot(t *testing.T) {
	e := newEnv(t)
	// 即使画布自带 index.html，?view=mockup 也应返回样机页
	a := mkMockupCanvas(t, e, put(EntryHTML, "<p>own index</p>"))
	h := newHost(t, e, "localhost", fakeRuntime)

	req := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok/?view=mockup&frames=mobile&radius=20&border=0&bg=dark&shadow=1&shell=1&w=510&h=1000", nil)
	rec := httptest.NewRecorder()
	h.ServeRender(rec, req, render.Target{ArtifactID: a.ID, Version: 1, Self: "http://" + render.Host + "/tok/"}, "/")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "@page{size:510px 1000px;margin:0}")
	assert.Contains(t, body, "background:#111114")

	d := overviewJSON(t, body)
	assert.Equal(t, "mockup", d.View)
	require.NotNil(t, d.Mockup)
	assert.Equal(t, []string{"mobile"}, d.Mockup.Frames)
	assert.Equal(t, 20, d.Mockup.Radius)
	assert.False(t, d.Mockup.Border)
	assert.Equal(t, "dark", d.Mockup.Bg)
	assert.True(t, d.Mockup.Shadow)
	assert.True(t, d.Mockup.Shell)
	assert.Equal(t, 510, d.Mockup.Width)
	assert.Equal(t, 1000, d.Mockup.Height)

	// 普通渲染请求（无 view=mockup）仍回自带的 index.html
	req2 := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok/", nil)
	rec2 := httptest.NewRecorder()
	h.ServeRender(rec2, req2, render.Target{ArtifactID: a.ID, Version: 1, Self: "http://" + render.Host + "/tok/"}, "/")
	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Equal(t, "<p>own index</p>", rec2.Body.String())
}

// 真机 Chromium 样机导出测试（ORYN_TEST_CHROMIUM=1）：验证真 PNG 像素宽高与选项相符，及 PDF 合法产出。
func TestExportMockupChromium(t *testing.T) {
	if os.Getenv("ORYN_TEST_CHROMIUM") != "1" {
		t.Skip("set ORYN_TEST_CHROMIUM=1 to run Chromium integration tests")
	}
	if _, ok := webui.Asset(overviewAsset); !ok {
		t.Skip("internal/webui/dist/sandbox/overview.js is missing; build overview first")
	}
	e := newEnv(t)
	h := newHost(t, e, "localhost", distAsset)
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
	base, err := os.MkdirTemp("", "oryn-mockup-render-*")
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

	a := mkMockupCanvas(t, e)

	// mobile (390x844, phone shell +24/+60 -> 414x904) + padding 96 -> 510x1000; scale=2 -> 1020x2000
	oPhone := DefaultMockupOptions()
	oPhone.Frames = []string{"mobile"}
	oPhone.Scale = 2
	oPhone.Shell = true
	x, err := e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPNG, &oPhone)
	require.NoError(t, err)
	pngRaw := exportBytes(t, x)
	img, err := png.Decode(bytes.NewReader(pngRaw))
	require.NoError(t, err)
	assert.Equal(t, image.Point{X: 1020, Y: 2000}, img.Bounds().Size())

	// home (1280x800, shell=false) + padding 96 -> 1376x896; scale=1 -> 1376x896
	oPlain := DefaultMockupOptions()
	oPlain.Frames = []string{"home"}
	oPlain.Scale = 1
	oPlain.Shell = false
	oPlain.Bg = "#1e293b"
	x2, err := e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPNG, &oPlain)
	require.NoError(t, err)
	img2, err := png.Decode(bytes.NewReader(exportBytes(t, x2)))
	require.NoError(t, err)
	assert.Equal(t, image.Point{X: 1376, Y: 896}, img2.Bounds().Size())

	// PDF 导出：合法 PDF 且单页宽高比等于 510/1000
	xPDF, err := e.s.ExportWithOptions(ctx, a.ID, 0, ExportMockupPDF, &oPhone)
	require.NoError(t, err)
	pdf := exportBytes(t, xPDF)
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF-")))
	boxes := pdfMediaBoxRE.FindAllSubmatch(pdf, -1)
	require.NotEmpty(t, boxes)
	pw, _ := strconv.ParseFloat(string(boxes[0][1]), 64)
	ph, _ := strconv.ParseFloat(string(boxes[0][2]), 64)
	assert.InDelta(t, 510.0/1000.0, pw/ph, 0.02)
}
