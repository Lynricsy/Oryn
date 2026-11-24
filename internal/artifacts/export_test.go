package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/Lynricsy/Oryn/internal/boxcfg"
	"github.com/Lynricsy/Oryn/internal/designengine"
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

// projectFrames 是导出工程测试用的两帧设计：login 与 signup 互相跳转，有 _layout 外壳与 theme.css token。
var projectFrames = map[string]string{
	"src/frames/login.tsx": `import { Link } from "oryn:design";

export const frame = { name: "登录", width: 1280, height: 800 };

export default function Login() {
	return (
		<main className="p-8 text-brand">
			<h1 className="text-2xl font-semibold">登录</h1>
			<Link to="signup" className="underline">去注册</Link>
		</main>
	);
}
`,
	"src/frames/signup.tsx": `import { Link } from "oryn:design";

export default function Signup() {
	return (
		<main className="p-8">
			<h1 className="text-2xl font-semibold text-brand">注册</h1>
			<Link to="login">已有账号？登录</Link>
		</main>
	);
}
`,
	"src/frames/_layout.tsx": `import type { ReactNode } from "react";

export default function Layout({ frame, children }: { frame: string; children?: ReactNode }) {
	return <div data-frame={frame} className="min-h-screen bg-white">{children}</div>;
}
`,
	"src/theme.css": "@theme {\n\t--color-brand: #0f766e;\n}\n",
	"src/DESIGN.md": "# 登录流程\n",
}

const projectCanvas = `{"format":2,"design":{"engine":1,"start":"p2"},"canvases":[{"id":"c1","name":"流程"}],"pages":[` +
	`{"id":"p1","canvas":"c1","name":"登录页","file":"dist/login.html","source":"src/frames/login.tsx","x":0,"y":0,"width":1280,"height":800},` +
	`{"id":"p2","canvas":"c1","name":"注册页","file":"dist/signup.html","source":"src/frames/signup.tsx","x":1400,"y":0,"width":1280,"height":800}]}`

// mkDesignCanvas 建一个两帧设计画布（extra 追加或覆盖文件）。
func mkDesignCanvas(t *testing.T, e *env, title string, extra map[string]string) Artifact {
	t.Helper()
	files := map[string]string{
		EntryCanvas:        projectCanvas,
		"dist/login.html":  "<!doctype html><p>登录</p>",
		"dist/signup.html": "<!doctype html><p>注册</p>",
		"dist/frames.json": `[]`,
		"dist/assets/a.js": "console.log(1)",
	}
	maps.Copy(files, projectFrames)
	maps.Copy(files, extra)
	var changes []FileChange
	for _, p := range slices.Sorted(maps.Keys(files)) {
		changes = append(changes, put(p, files[p]))
	}
	a, err := e.s.Create(context.Background(), CreateParams{Title: title, Files: changes})
	require.NoError(t, err)
	return a
}

func unzip(t *testing.T, b []byte) map[string]string {
	t.Helper()
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
	return got
}

// dumpProject 在设了 ORYN_TEST_PROJECT_DIR 时把导出的 zip 写到该目录（手工验收：解压后 npm ci && npm run build）。
func dumpProject(t *testing.T, x Export, b []byte) {
	if dir := os.Getenv("ORYN_TEST_PROJECT_DIR"); dir != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, x.Name), b, 0o644))
	}
}

// project：设计画布导出为工程 zip——src/**、工程外壳、合并后的 package.json 与严格对应的锁；box.yaml 能被 boxcfg 解析。
// 锁的三种来源：没有设计依赖时现场派生；版本里有 dist/_project 锁时用它（对不上回 invalid）；有额外依赖却没有锁回 invalid。
// 非设计画布（网页、HTML 画布、文档）回 invalid。
func TestExportProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// 1) 没有设计依赖：锁由引擎锁改写
	a := mkDesignCanvas(t, e, "登录/注册", nil)
	x, err := e.s.Export(ctx, a.ID, 0, ExportProject)
	require.NoError(t, err)
	assert.Equal(t, "登录_注册-v1-project.zip", x.Name)
	assert.Equal(t, "application/zip", x.ContentType)
	b := exportBytes(t, x)
	dumpProject(t, x, b)
	got := unzip(t, b)
	want := []string{
		".gitignore", ".oryn/box.yaml", "README.md", "index.html", "main.tsx", "oryn-design.tsx", "oryn-frames.json",
		"package-lock.json", "package.json", "server.mjs",
		"src/DESIGN.md", "src/frames/_layout.tsx", "src/frames/login.tsx", "src/frames/signup.tsx", "src/theme.css",
		"styles.css", "tsconfig.json", "vite.config.ts",
	}
	assert.Equal(t, want, slices.Sorted(maps.Keys(got)), "不带 canvas.json 与 dist/**")
	for p, c := range projectFrames {
		assert.Equal(t, c, got[p], p)
	}
	wantManifest, err := designengine.ProjectManifest(nil)
	require.NoError(t, err)
	assert.Equal(t, string(wantManifest), got["package.json"])
	wantLock, ok, err := designengine.ProjectLock(nil)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, string(wantLock), got["package-lock.json"])
	require.NoError(t, designengine.VerifyProjectLock(nil, []byte(got["package-lock.json"])))

	cfg, warnings, err := boxcfg.Parse([]byte(got[".oryn/box.yaml"]))
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Equal(t, "npm ci && npm run build", cfg.Build)
	assert.False(t, cfg.Source)
	assert.Equal(t, []string{"dist", "server.mjs"}, cfg.Artifacts)
	assert.Equal(t, []boxcfg.Service{{Name: "web", Command: "node server.mjs"}}, cfg.Services)

	var meta struct {
		Title, Start string
		Frames       []designengine.ProjectFrame
	}
	require.NoError(t, json.Unmarshal([]byte(got["oryn-frames.json"]), &meta))
	assert.Equal(t, "登录/注册", meta.Title)
	assert.Equal(t, "signup", meta.Start, "design.start 指向画框 p2")
	assert.Equal(t, []designengine.ProjectFrame{{ID: "login", Name: "登录页"}, {ID: "signup", Name: "注册页"}}, meta.Frames)
	assert.Contains(t, got["styles.css"], `@import "./src/theme.css";`)
	assert.Contains(t, got["vite.config.ts"], "oryn:design")
	assert.Contains(t, got["main.tsx"], `import.meta.glob<FrameModule>("./src/frames/*.tsx"`)
	assert.True(t, strings.HasPrefix(got["README.md"], "# 登录/注册\n"))
	assert.NotContains(t, got["README.md"], "依赖版本冲突")
	for _, p := range []string{"main.tsx", "vite.config.ts", "styles.css", "index.html"} {
		assert.NotContains(t, got[p], "data-oryn-loc", p)
		assert.NotContains(t, got[p], "probe.js", p)
	}

	// 2) 有额外依赖且封存时生成了 dist/_project 锁：用它；冲突列进 README
	design := `{"dependencies":{"clsx":"2.1.1","lucide-react":"1.78.0"}}`
	deps := map[string]string{}
	require.NoError(t, json.Unmarshal(designengine.EnginePackageJSON(), &struct {
		Dependencies *map[string]string `json:"dependencies"`
	}{&deps}))
	deps["clsx"] = "2.1.1"
	deps["lucide-react"] = "1.78.0"
	sealed, err := json.Marshal(map[string]any{"name": designengine.ProjectName, "lockfileVersion": 3, "requires": true,
		"packages": map[string]any{"": map[string]any{"name": designengine.ProjectName, "dependencies": deps}}})
	require.NoError(t, err)
	manifest, err := designengine.ProjectManifest([]byte(design))
	require.NoError(t, err)
	withLock := mkDesignCanvas(t, e, "带依赖", map[string]string{
		"src/package.json":                design,
		"dist/_project/package.json":      string(manifest),
		"dist/_project/package-lock.json": string(sealed),
	})
	x, err = e.s.Export(ctx, withLock.ID, 0, ExportProject)
	require.NoError(t, err)
	got = unzip(t, exportBytes(t, x))
	assert.Equal(t, string(manifest), got["package.json"])
	assert.Equal(t, string(sealed), got["package-lock.json"])
	assert.Equal(t, design, got["src/package.json"])
	assert.NotContains(t, got, "dist/_project/package-lock.json")
	assert.Contains(t, got["README.md"], "| `lucide-react` | `1.79.0` | `1.78.0` |")

	// 封存的锁与 src/package.json 对不上（例如之后 src/package.json 又多了依赖）：invalid
	stale := mkDesignCanvas(t, e, "旧锁", map[string]string{
		"src/package.json":                `{"dependencies":{"clsx":"2.1.1","zod":"4.1.0","lucide-react":"1.78.0"}}`,
		"dist/_project/package-lock.json": string(sealed),
	})
	_, err = e.s.Export(ctx, stale.ID, 0, ExportProject)
	assert.Equal(t, CodeInvalid, code(err))

	// 3) 有额外依赖但缺少工程锁：invalid，不降级
	noLock := mkDesignCanvas(t, e, "缺锁", map[string]string{"src/package.json": design})
	_, err = e.s.Export(ctx, noLock.ID, 0, ExportProject)
	require.Equal(t, CodeInvalid, code(err))
	oe, _ := AsOpError(err)
	assert.Contains(t, oe.Message, "缺少工程锁")
	assert.Contains(t, oe.Message, "重新封存")

	// 设计依赖与引擎一致（没有额外依赖）：仍可现场派生
	same := mkDesignCanvas(t, e, "同版本", map[string]string{"src/package.json": `{"dependencies":{"react":"19.1.1"}}`})
	_, err = e.s.Export(ctx, same.ID, 0, ExportProject)
	require.NoError(t, err)

	// 非设计画布
	_, err = e.s.Export(ctx, mkPage(t, e).ID, 0, ExportProject)
	assert.Equal(t, CodeInvalid, code(err), "网页")
	html, err := e.s.Create(ctx, CreateParams{Title: "画布", Files: []FileChange{
		put(EntryCanvas, `{"format":1,"canvases":[{"id":"c1","name":"首页"}],"pages":[{"id":"p1","canvas":"c1","name":"一","file":"a.html"}]}`),
		put("a.html", "<p>a</p>"),
	}})
	require.NoError(t, err)
	_, err = e.s.Export(ctx, html.ID, 0, ExportProject)
	assert.Equal(t, CodeInvalid, code(err), "HTML 画布")
	doc, err := createTyped(t, e, TypeDocument, put("document.md", "# x\n"))
	require.NoError(t, err)
	_, err = e.s.Export(ctx, doc.ID, 0, ExportProject)
	assert.Equal(t, CodeInvalid, code(err), "文档")
}
