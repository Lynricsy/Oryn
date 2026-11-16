package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

func exportBytes(t *testing.T, x Export) []byte {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, x.Write(&out))
	return out.Bytes()
}

// md：没有图片时是 document.md 本身；有图片时是含 document.md 与 images/ 的 zip；不是文档回 invalid。
func TestExportMarkdown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	md := "# 周报\n\n正文。\n"
	plain, err := e.s.Create(ctx, CreateParams{Title: "周报", Type: TypeDocument, Files: []FileChange{put("document.md", md)}})
	require.NoError(t, err)
	x, err := e.s.Export(ctx, plain.ID, 0, ExportMarkdown)
	require.NoError(t, err)
	assert.Equal(t, "周报.md", x.Name)
	assert.Equal(t, "text/markdown; charset=utf-8", x.ContentType)
	assert.Equal(t, md, string(exportBytes(t, x)))

	withImg := "# 图\n\n![图](images/a.png)\n"
	pics, err := e.s.Create(ctx, CreateParams{Title: "a/b", Type: TypeDocument,
		Files: []FileChange{put("document.md", withImg), put("images/a.png", pngBytes)}})
	require.NoError(t, err)
	x, err = e.s.Export(ctx, pics.ID, 0, ExportMarkdown)
	require.NoError(t, err)
	assert.Equal(t, "a_b.zip", x.Name)
	assert.Equal(t, "application/zip", x.ContentType)
	b := exportBytes(t, x)
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		c, _ := io.ReadAll(rc)
		_ = rc.Close()
		got[f.Name] = string(c)
	}
	assert.Equal(t, map[string]string{"document.md": withImg, "images/a.png": pngBytes}, got)

	page := mkPage(t, e)
	_, err = e.s.Export(ctx, page.ID, 0, ExportMarkdown)
	assert.Equal(t, CodeInvalid, code(err))
	deck, err := createTyped(t, e, TypeSlides, put("slides.json", deckJSON(t, validDeck())), put("images/a.png", pngBytes))
	require.NoError(t, err)
	_, err = e.s.Export(ctx, deck.ID, 0, ExportMarkdown)
	assert.Equal(t, CodeInvalid, code(err))
}

// pdf：只对幻灯片；渲染器打印查看器的 view=print，窗口等于幻灯片尺寸；没有渲染器或不可用回 render_unavailable。
func TestExportPDF(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	deck, err := createTyped(t, e, TypeSlides, put("slides.json", deckJSON(t, validDeck())), put("images/a.png", pngBytes))
	require.NoError(t, err)

	_, err = e.s.Export(ctx, deck.ID, 0, ExportPDF)
	assert.Equal(t, CodeRenderUnavailable, code(err), "没接渲染器")
	withRenderer(e, &fakeRenderer{down: "找不到 Chromium"})
	_, err = e.s.Export(ctx, deck.ID, 0, ExportPDF)
	require.Equal(t, CodeRenderUnavailable, code(err))
	oe, _ := AsOpError(err)
	assert.Contains(t, oe.Message, "找不到 Chromium")

	r := &fakeRenderer{}
	withRenderer(e, r)
	x, err := e.s.Export(ctx, deck.ID, 0, ExportPDF)
	require.NoError(t, err)
	assert.Equal(t, "t.pdf", x.Name)
	assert.Equal(t, "application/pdf", x.ContentType)
	assert.NotEmpty(t, exportBytes(t, x))
	// validDeck 是 4:3
	assert.Equal(t, render.Job{ArtifactID: deck.ID, Version: 1, Query: "view=print", Width: 960, Height: 720, Scale: 1, PDF: true}, r.lastJob())

	// 16:9（缺省）的新版本：尺寸跟着变，版本取请求的那个
	d2 := validDeck()
	delete(d2, "aspect")
	_, err = e.s.Write(ctx, WriteParams{ID: deck.ID, BaseVersion: 1, Files: []FileChange{put("slides.json", deckJSON(t, d2))}})
	require.NoError(t, err)
	_, err = e.s.Export(ctx, deck.ID, 0, ExportPDF)
	require.NoError(t, err)
	assert.Equal(t, render.Job{ArtifactID: deck.ID, Version: 2, Query: "view=print", Width: 1280, Height: 720, Scale: 1, PDF: true}, r.lastJob())
	_, err = e.s.Export(ctx, deck.ID, 1, ExportPDF)
	require.NoError(t, err)
	assert.Equal(t, 1, r.lastJob().Version)

	doc, err := createTyped(t, e, TypeDocument, put("document.md", "# x\n"))
	require.NoError(t, err)
	_, err = e.s.Export(ctx, doc.ID, 0, ExportPDF)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.Export(ctx, mkPage(t, e).ID, 0, ExportPDF)
	assert.Equal(t, CodeInvalid, code(err))
	assert.Len(t, r.jobs, 3, "不适用的类型不渲染")
}

// distAsset 从 web/dist 读查看器的构建产物（`cd web && bun run build` 之后才有）。
func distAsset(name string) ([]byte, bool) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "dist", filepath.FromSlash(name)))
	return b, err == nil
}

var (
	pdfPageRE     = regexp.MustCompile(`/Type\s*/Page[^s]`)
	pdfMediaBoxRE = regexp.MustCompile(`/MediaBox\s*\[\s*0\s+0\s+([0-9.]+)\s+([0-9.]+)\s*\]`)
)

// 真渲染 PDF（ORYN_TEST_CHROMIUM=1，并且先构建了 web/dist 里的幻灯片查看器）：页数等于幻灯片张数，每页宽高比等于幻灯片。
func TestExportPDFChromium(t *testing.T) {
	if os.Getenv("ORYN_TEST_CHROMIUM") != "1" {
		t.Skip("set ORYN_TEST_CHROMIUM=1 to run Chromium integration tests")
	}
	if _, ok := distAsset("sandbox/viewers/slides.js"); !ok {
		t.Skip("web/dist/sandbox/viewers/slides.js is missing; run `cd web && bun run build` first")
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

	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var pic bytes.Buffer
	require.NoError(t, png.Encode(&pic, img))
	d := validDeck()
	slides := len(d["slides"].([]any))
	deck, err := createTyped(t, e, TypeSlides, put("slides.json", deckJSON(t, d)), put("images/a.png", pic.String()))
	require.NoError(t, err)
	x, err := e.s.Export(ctx, deck.ID, 0, ExportPDF)
	require.NoError(t, err)
	pdf := exportBytes(t, x)
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF-")))
	assert.Len(t, pdfPageRE.FindAll(pdf, -1), slides, "每张幻灯片一页")
	boxes := pdfMediaBoxRE.FindAllSubmatch(pdf, -1)
	require.NotEmpty(t, boxes)
	for _, m := range boxes {
		w, _ := strconv.ParseFloat(string(m[1]), 64)
		h, _ := strconv.ParseFloat(string(m[2]), 64)
		assert.InDelta(t, 4.0/3.0, w/h, 0.01, "validDeck 是 4:3：%s×%s", m[1], m[2])
	}
}
