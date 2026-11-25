package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/Lynricsy/Oryn/internal/designengine"
)

// 导出格式。
const (
	ExportHTML      = "html"
	ExportZip       = "zip"
	ExportMarkdown  = "md"
	ExportPDF       = "pdf"
	ExportProject   = "project"
	ExportMockupPNG = "mockup-png"
	ExportMockupPDF = "mockup-pdf"
)

// 样机布局常量（与 web/sandbox/overview/main.ts 同值）。
const (
	mockupPadding = 48
	mockupGap     = 40
	mockupMaxSide = 8000
)

// MockupOptions 是样机导出（mockup-png / mockup-pdf）的渲染选项（M29 D1）。
// 调用方应先从 DefaultMockupOptions() 起步再覆写需要的字段；nil 等价于缺省值。
type MockupOptions struct {
	Frames []string // 空 = 全部画框
	Scale  int      // 1–3，缺省 2
	Radius int      // 0–64，缺省 12
	Border bool     // 缺省 true
	Bg     string   // transparent、light、dark 或 #rrggbb，缺省 light
	Shadow bool     // 缺省 true
	Shell  bool     // 缺省 true
	Long   bool     // 缺省 false
}

// DefaultMockupOptions 返回样机导出的缺省选项。
func DefaultMockupOptions() MockupOptions {
	return MockupOptions{
		Scale:  2,
		Radius: 12,
		Border: true,
		Bg:     "light",
		Shadow: true,
		Shell:  true,
		Long:   false,
	}
}

// Export 是一次导出：Name 是建议的下载文件名（`<标题>.<扩展名>`，调用方再做文件名清洗），
// Write 把内容流式写给 w。
type Export struct {
	Name, ContentType string
	write             func(w io.Writer) error
}

// Write 把导出的内容写给 w。
func (e Export) Write(w io.Writer) error { return e.write(w) }

// Export 准备导出某个版本（0 = 最新）：`html` 只对恰好一个 HTML 文件的版本可用，直接给那个文件；
// `zip` 打包该版本的全部文件；`md` 只对文档可用（没有图片时是 `document.md` 本身，有图片时是含 `images/` 的 zip）；
// `pdf` 只对幻灯片可用，由控制面渲染器打印查看器的 `?view=print`（渲染不可用回 render_unavailable）；
// `project` 只对设计画布可用，打包成可独立构建、可部署到 box 的 Vite 工程（见 exportProject）；
// `mockup-png` / `mockup-pdf` 只对画布可用，以缺省样机选项渲染（需自定义选项请调 ExportWithOptions）。
func (s *Service) Export(ctx context.Context, id string, version int, format string) (Export, error) {
	return s.ExportWithOptions(ctx, id, version, format, nil)
}

// ExportWithOptions 与 Export 相同，另接受样机导出选项 opts（仅 `mockup-png` / `mockup-pdf` 使用，nil 取缺省值）。
func (s *Service) ExportWithOptions(ctx context.Context, id string, version int, format string, opts *MockupOptions) (Export, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Export{}, err
	}
	v, err := s.Version(ctx, id, version)
	if err != nil {
		return Export{}, err
	}
	files, err := s.Files(ctx, id, v.Version)
	if err != nil {
		return Export{}, err
	}
	// 标题里的 / 会被当成目录分隔，换掉
	base := strings.ReplaceAll(a.Title, "/", "_")
	switch format {
	case ExportHTML:
		if len(files) != 1 || !IsHTML(files[0].Path) {
			return Export{}, opErr(CodeInvalid, "版本 %d 有 %d 个文件，只能导出为 zip。", v.Version, len(files))
		}
		f := files[0]
		return Export{Name: base + ".html", ContentType: f.Mime, write: func(w io.Writer) error {
			rc, err := s.c.open(f.Sha256)
			if err != nil {
				return err
			}
			defer rc.Close()
			_, err = io.Copy(w, rc)
			return err
		}}, nil
	case ExportZip:
		// 没有自己的 index.html 的画布补一个生成的概览页，解开后直接打开 index.html 就能看；再导入时会被去掉
		overview, _, err := s.overviewFor(ctx, a, v.Version, "")
		if err != nil {
			return Export{}, err
		}
		return Export{Name: base + ".zip", ContentType: "application/zip", write: func(w io.Writer) error {
			zw := zip.NewWriter(w)
			for _, f := range files {
				if err := s.zipFile(zw, f, v); err != nil {
					return err
				}
			}
			if overview != nil {
				iw, err := zw.CreateHeader(&zip.FileHeader{Name: EntryHTML, Method: zip.Deflate, Modified: v.CreatedAt})
				if err != nil {
					return err
				}
				if _, err := iw.Write(overview); err != nil {
					return err
				}
			}
			return zw.Close()
		}}, nil
	case ExportMarkdown:
		if a.Type != TypeDocument {
			return Export{}, opErr(CodeInvalid, "artifact %s 不是文档，不能导出为 md。", id)
		}
		if len(files) == 1 {
			f := files[0]
			return Export{Name: base + ".md", ContentType: "text/markdown; charset=utf-8", write: func(w io.Writer) error {
				rc, err := s.c.open(f.Sha256)
				if err != nil {
					return err
				}
				defer rc.Close()
				_, err = io.Copy(w, rc)
				return err
			}}, nil
		}
		// 文档版本里只有 document.md 与 images/ 下的图片：整版打包，解开后相对路径照常能引用
		return Export{Name: base + ".zip", ContentType: "application/zip", write: func(w io.Writer) error {
			zw := zip.NewWriter(w)
			for _, f := range files {
				if err := s.zipFile(zw, f, v); err != nil {
					return err
				}
			}
			return zw.Close()
		}}, nil
	case ExportPDF:
		if a.Type != TypeSlides {
			return Export{}, opErr(CodeInvalid, "artifact %s 不是幻灯片，不能导出为 pdf。", id)
		}
		out, err := s.slidesPDF(ctx, a.ID, v.Version)
		if err != nil {
			return Export{}, err
		}
		return Export{Name: base + ".pdf", ContentType: "application/pdf", write: func(w io.Writer) error {
			_, err := w.Write(out)
			return err
		}}, nil
	case ExportProject:
		return s.exportProject(ctx, a, v, files, base)
	case ExportMockupPNG:
		return s.exportMockup(ctx, a, v, base, false, opts)
	case ExportMockupPDF:
		return s.exportMockup(ctx, a, v, base, true, opts)
	}
	return Export{}, opErr(CodeInvalid, "artifact %s 不能导出为 %q（页面与画布可导出为 html 或 zip，画布另可导出为 mockup-png / mockup-pdf，设计画布另可导出为 project，文档另可导出为 md，幻灯片另可导出为 pdf）。", id, format)
}

// exportProject 把设计画布的一个版本导出为工程 zip（M29 D2）：版本里的 src/**、designengine 的工程外壳
// （index.html、main.tsx、vite.config.ts、server.mjs、.oryn/box.yaml、README.md 等）、合并后的 package.json
// 与严格对应的 package-lock.json（box 构建用 npm ci）。锁的来源依次是：版本里封存时生成的 dist/_project/package-lock.json
// （根依赖须与当前 src/package.json 合并的结果一致）；没有额外设计依赖时由引擎锁现场改写；都不行回 invalid，不降级成 npm install。
func (s *Service) exportProject(ctx context.Context, a Artifact, v Version, files []File, base string) (Export, error) {
	if a.Kind != KindCanvas {
		return Export{}, opErr(CodeInvalid, "artifact %s 不是设计画布，不能导出为工程。", a.ID)
	}
	raw, _, err := s.ReadFile(ctx, a.ID, v.Version, EntryCanvas)
	if err != nil {
		return Export{}, err
	}
	doc, err := canvas.Parse(raw)
	if err != nil || doc.Design == nil {
		return Export{}, opErr(CodeInvalid, "artifact %s 版本 %d 不是设计画布（canvas.json 没有 design），不能导出为工程。", a.ID, v.Version)
	}
	has := func(p string) bool { return slices.ContainsFunc(files, func(f File) bool { return f.Path == p }) }
	var designPkg []byte
	if has("src/package.json") {
		if designPkg, _, err = s.ReadFile(ctx, a.ID, v.Version, "src/package.json"); err != nil {
			return Export{}, err
		}
	}
	manifest, err := designengine.ProjectManifest(designPkg)
	if err != nil {
		return Export{}, opErr(CodeInvalid, "版本 %d 的 %s", v.Version, err)
	}
	conflicts, _ := designengine.ProjectConflicts(designPkg) // 与 ProjectManifest 同一解析，上面已成功
	var lock []byte
	if sealed := path.Join(designengine.ProjectDir, "package-lock.json"); has(sealed) {
		if lock, _, err = s.ReadFile(ctx, a.ID, v.Version, sealed); err != nil {
			return Export{}, err
		}
		if err := designengine.VerifyProjectLock(designPkg, lock); err != nil {
			return Export{}, opErr(CodeInvalid, "版本 %d 的 %s 与 src/package.json 对不上（%s），请让 agent 重新封存一次。", v.Version, sealed, err)
		}
	} else {
		var ok bool
		if lock, ok, err = designengine.ProjectLock(designPkg); err != nil {
			return Export{}, err
		}
		if !ok {
			return Export{}, opErr(CodeInvalid, "版本 %d 有额外依赖但缺少工程锁（%s），请让 agent 重新封存一次。", v.Version, sealed)
		}
	}

	// 帧按画布上的顺序，名字取画框名；起始帧取 design.start 指向的画框
	p := designengine.Project{Title: a.Title, Theme: has("src/theme.css"), Conflicts: conflicts}
	for _, pg := range doc.Pages {
		id := strings.TrimSuffix(strings.TrimPrefix(pg.Source, "src/frames/"), ".tsx")
		if pg.Source == "" || slices.ContainsFunc(p.Frames, func(f designengine.ProjectFrame) bool { return f.ID == id }) {
			continue
		}
		p.Frames = append(p.Frames, designengine.ProjectFrame{ID: id, Name: pg.Name})
		if doc.Design.Start != nil && *doc.Design.Start == pg.ID {
			p.Start = id
		}
	}
	var src []File // 版本里的 src/**（封存不同步 node_modules，这里再防一道）
	for _, f := range files {
		if strings.HasPrefix(f.Path, "src/") && !strings.Contains(f.Path, "/node_modules/") {
			src = append(src, f)
		}
	}
	shell := designengine.ProjectFiles(p)
	shell = append(shell, designengine.EjectFile{Path: "package.json", Data: manifest}, designengine.EjectFile{Path: "package-lock.json", Data: lock})
	name := fmt.Sprintf("%s-v%d-project.zip", base, v.Version)
	return Export{Name: name, ContentType: "application/zip", write: func(w io.Writer) error {
		zw := zip.NewWriter(w)
		for _, f := range src {
			if err := s.zipFile(zw, f, v); err != nil {
				return err
			}
		}
		for _, f := range shell {
			fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: v.CreatedAt})
			if err != nil {
				return err
			}
			if _, err := io.Copy(fw, bytes.NewReader(f.Data)); err != nil {
				return err
			}
		}
		return zw.Close()
	}}, nil
}

// slidesPDF 用控制面渲染器把幻灯片某个版本的查看器打印视图（`?view=print`，每张一页，`@page` 等于幻灯片尺寸）
// 打印成 PDF：窗口等于幻灯片尺寸、倍率 1。渲染要在写响应头之前完成，失败才能映射成状态码。
func (s *Service) slidesPDF(ctx context.Context, id string, version int) ([]byte, error) {
	entry, _, err := s.ReadFile(ctx, id, version, EntrySlides)
	if err != nil {
		return nil, err
	}
	var deck struct {
		Aspect string `json:"aspect"`
	}
	_ = json.Unmarshal(entry, &deck) // 入口已通过校验；万一解析不了按缺省的 16:9
	width, height := slideSize(deck.Aspect)
	r := s.renderer()
	if r == nil {
		return nil, opErr(CodeRenderUnavailable, "控制面没有配置渲染器，不能导出 PDF。")
	}
	out, err := r.Run(ctx, render.Job{ArtifactID: id, Version: version, Query: "view=print",
		Width: width, Height: height, Scale: 1, PDF: true})
	if errors.Is(err, render.ErrUnavailable) {
		return nil, opErr(CodeRenderUnavailable, "控制面现在不能渲染（%s），所以不能导出 PDF。", err)
	}
	if err != nil {
		return nil, fmt.Errorf("把 artifact %s 版本 %d 打印成 PDF 失败：%w", id, version, err)
	}
	return out, nil
}

// slideSize 返回幻灯片画板的尺寸（CSS 像素），与查看器的 slideSize 同值：16:9 为 1280×720，4:3 为 960×720。
func slideSize(aspect string) (width, height int) {
	if aspect == "4:3" {
		return 960, 720
	}
	return 1280, 720
}

// zipFile 把一个文件流式写进 zip。
func (s *Service) zipFile(zw *zip.Writer, f File, v Version) error {
	rc, err := s.c.open(f.Sha256)
	if err != nil {
		return err
	}
	defer rc.Close()
	w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: v.CreatedAt})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rc)
	return err
}

var mockupHexBgRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func validMockupBg(bg string) bool {
	return bg == "transparent" || bg == "light" || bg == "dark" || mockupHexBgRe.MatchString(bg)
}

// validateMockupOptions 校验样机导出选项，非法值回 invalid。
func validateMockupOptions(o MockupOptions) error {
	if o.Scale < 1 || o.Scale > 3 {
		return opErr(CodeInvalid, "样机缩放倍数 scale 必须是 1–3（收到 %d）。", o.Scale)
	}
	if o.Radius < 0 || o.Radius > 64 {
		return opErr(CodeInvalid, "样机圆角 radius 必须是 0–64（收到 %d）。", o.Radius)
	}
	if !validMockupBg(o.Bg) {
		return opErr(CodeInvalid, "样机背景 bg 必须是 transparent、light、dark 或 #rrggbb（收到 %q）。", o.Bg)
	}
	return nil
}

// mockupShellChrome 返回设备外壳四周合计占用的宽高增量（CSS px），与 web/sandbox/overview/main.ts 的 shellChrome 同值。
func mockupShellChrome(device string, shell bool) (w, h int) {
	if !shell {
		return 0, 0
	}
	switch device {
	case canvas.DevicePhone:
		return 24, 60
	case canvas.DeviceTablet:
		return 32, 48
	case canvas.DeviceBrowser:
		return 0, 36
	default:
		return 0, 0
	}
}

// selectMockupPages 按请求的帧 id 列表筛选画框（去重、保持请求顺序；空则取全部画框）。
func selectMockupPages(doc canvas.Doc, ids []string) ([]canvas.Page, error) {
	if len(doc.Pages) == 0 {
		return nil, opErr(CodeInvalid, "画布没有画框，不能导出样机。")
	}
	if len(ids) == 0 {
		return slices.Clone(doc.Pages), nil
	}
	byID := make(map[string]canvas.Page, len(doc.Pages))
	for _, p := range doc.Pages {
		byID[p.ID] = p
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]canvas.Page, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, opErr(CodeInvalid, "frames 里不能有空的画框 id。")
		}
		p, ok := byID[id]
		if !ok {
			return nil, opErr(CodeInvalid, "画布里没有 id 为 %q 的画框。", id)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

// mockupCanvasSize 计算样机画布的视口宽高（CSS px），不超过 render 的 8000 上限。
func mockupCanvasSize(pages []canvas.Page, heights map[string]int, shell bool) (width, height int) {
	rawW := 2 * mockupPadding
	maxH := 0
	for i, p := range pages {
		pw := max(1, int(math.Round(p.Width)))
		ph := max(1, int(math.Round(p.Height)))
		if h, ok := heights[p.ID]; ok && h > 0 {
			ph = h
		}
		sw, sh := mockupShellChrome(p.Device, shell)
		if i > 0 {
			rawW += mockupGap
		}
		rawW += pw + sw
		if boxH := ph + sh; boxH > maxH {
			maxH = boxH
		}
	}
	rawH := 2*mockupPadding + maxH
	if rawW <= mockupMaxSide {
		return max(1, rawW), max(1, min(rawH, mockupMaxSide))
	}
	scaledH := int(math.Round(float64(rawH) * float64(mockupMaxSide) / float64(rawW)))
	return mockupMaxSide, max(1, min(scaledH, mockupMaxSide))
}

// parseProbeHeight 从 --dump-dom 的输出里提取最后一个 `<script id="oryn-probe">` 的 height 字段。
func parseProbeHeight(dom []byte) (int, bool) {
	var raw []byte
	found := false
	z := html.NewTokenizer(bytes.NewReader(dom))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		if string(name) != "script" || !hasAttr {
			continue
		}
		isProbe := false
		for more := true; more; {
			var k, v []byte
			k, v, more = z.TagAttr()
			if string(k) == "id" && string(v) == design.ProbeResultID {
				isProbe = true
			}
		}
		if !isProbe {
			continue
		}
		found = true
		raw = nil
		if z.Next() == html.TextToken {
			raw = slices.Clone(z.Raw())
		}
	}
	if !found {
		return 0, false
	}
	var pr struct {
		V      int     `json:"v"`
		Height float64 `json:"height"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil || pr.V != 1 || pr.Height <= 0 {
		return 0, false
	}
	return int(math.Ceil(pr.Height)), true
}

// mockupRenderQuery 构造传给样机渲染页（?view=mockup&…）的查询串。
func mockupRenderQuery(pages []canvas.Page, heights map[string]int, o MockupOptions, w, h int) string {
	q := url.Values{}
	q.Set("view", "mockup")
	ids := make([]string, len(pages))
	for i, p := range pages {
		ids[i] = p.ID
	}
	q.Set("frames", strings.Join(ids, ","))
	q.Set("radius", strconv.Itoa(o.Radius))
	q.Set("border", boolFlag(o.Border))
	q.Set("bg", o.Bg)
	q.Set("shadow", boolFlag(o.Shadow))
	q.Set("shell", boolFlag(o.Shell))
	q.Set("w", strconv.Itoa(w))
	q.Set("h", strconv.Itoa(h))
	if len(heights) > 0 {
		parts := make([]string, 0, len(pages))
		for _, p := range pages {
			if hh, ok := heights[p.ID]; ok && hh > 0 {
				parts = append(parts, p.ID+":"+strconv.Itoa(hh))
			}
		}
		if len(parts) > 0 {
			q.Set("heights", strings.Join(parts, ","))
		}
	}
	return q.Encode()
}

func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// exportMockup 把画布的某个版本渲染成样机 PNG 或 PDF（M29 D1）。
// 长图模式（opts.Long）先经探针（--dump-dom）测量每帧内容高度，钳制在 8000 CSS px 内，再渲染整张样机页。
func (s *Service) exportMockup(ctx context.Context, a Artifact, v Version, base string, asPDF bool, opts *MockupOptions) (Export, error) {
	if a.Kind != KindCanvas || a.Type != "" {
		return Export{}, opErr(CodeInvalid, "artifact %s 不是画布，不能导出样机。", a.ID)
	}
	o := DefaultMockupOptions()
	if opts != nil {
		o = *opts
	}
	if err := validateMockupOptions(o); err != nil {
		return Export{}, err
	}
	raw, _, err := s.ReadFile(ctx, a.ID, v.Version, EntryCanvas)
	if err != nil {
		return Export{}, err
	}
	doc, err := canvas.Parse(raw)
	if err != nil {
		return Export{}, opErr(CodeInvalid, "artifact %s 版本 %d 的 canvas.json 不合法：%s", a.ID, v.Version, err)
	}
	pages, err := selectMockupPages(doc, o.Frames)
	if err != nil {
		return Export{}, err
	}
	r := s.renderer()
	if r == nil {
		return Export{}, opErr(CodeRenderUnavailable, "控制面没有配置渲染器，不能导出样机。")
	}
	if ok, reason := r.Status(); !ok {
		return Export{}, opErr(CodeRenderUnavailable, "控制面现在不能渲染（%s），所以不能导出样机。", reason)
	}

	var heights map[string]int
	if o.Long {
		measured := make([]int, len(pages))
		var (
			wg         sync.WaitGroup
			mu         sync.Mutex
			unavailErr error
		)
		for i, pg := range pages {
			wg.Go(func() {
				pw := max(1, int(math.Round(pg.Width)))
				ph := max(1, int(math.Round(pg.Height)))
				_, sh := mockupShellChrome(pg.Device, o.Shell)
				maxFrameH := max(1, mockupMaxSide-2*mockupPadding-sh)
				h := ph
				dom, err := r.Run(ctx, render.Job{
					ArtifactID: a.ID,
					Version:    v.Version,
					Path:       pg.File,
					Query:      design.ProbeQuery + "=1",
					Width:      pw,
					Height:     ph,
					Scale:      1,
					DumpDOM:    true,
				})
				if errors.Is(err, render.ErrUnavailable) {
					mu.Lock()
					if unavailErr == nil {
						unavailErr = err
					}
					mu.Unlock()
					return
				}
				if err == nil {
					if probed, ok := parseProbeHeight(dom); ok && probed > h {
						h = probed
					}
				}
				measured[i] = min(h, maxFrameH)
			})
		}
		wg.Wait()
		if unavailErr != nil {
			return Export{}, opErr(CodeRenderUnavailable, "控制面现在不能渲染（%s），所以不能导出样机。", unavailErr)
		}
		heights = make(map[string]int, len(pages))
		for i, pg := range pages {
			heights[pg.ID] = measured[i]
		}
	}

	width, height := mockupCanvasSize(pages, heights, o.Shell)
	q := mockupRenderQuery(pages, heights, o, width, height)
	job := render.Job{
		ArtifactID: a.ID,
		Version:    v.Version,
		Query:      q,
		Width:      width,
		Height:     height,
		Scale:      float64(o.Scale),
		PDF:        asPDF,
	}
	if asPDF {
		job.Scale = 1
	}
	out, err := r.Run(ctx, job)
	if errors.Is(err, render.ErrUnavailable) {
		return Export{}, opErr(CodeRenderUnavailable, "控制面现在不能渲染（%s），所以不能导出样机。", err)
	}
	if err != nil {
		return Export{}, fmt.Errorf("把 artifact %s 版本 %d 渲染成样机失败：%w", a.ID, v.Version, err)
	}
	ext, ct := "png", "image/png"
	if asPDF {
		ext, ct = "pdf", "application/pdf"
	}
	name := fmt.Sprintf("%s-v%d-mockup.%s", base, v.Version, ext)
	return Export{Name: name, ContentType: ct, write: func(w io.Writer) error {
		_, err := w.Write(out)
		return err
	}}, nil
}
