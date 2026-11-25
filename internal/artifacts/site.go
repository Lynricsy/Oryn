package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/artifacts/htmlsrc"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

// runtimeAsset 是页面运行时在嵌入的 Web 构建产物里的路径。
const runtimeAsset = "sandbox/runtime.js"

// runtimeConfig 是注入页面的 `__orynStart(<配置>)` 的参数。Root 是帧根（以 / 结尾），运行时端点在
// `<root>_oryn/v1`。
type runtimeConfig struct {
	V       int    `json:"v"`
	Mark    string `json:"mark"`
	Path    string `json:"path"`
	Root    string `json:"root"`
	Runtime bool   `json:"runtime"`
}

const markAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// newMark 返回每次响应新生成的 8 位 [a-z0-9] 源码标记：页面脚本后来生成的元素带不上它。
func newMark() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	for i := range b {
		b[i] = markAlphabet[int(b[i])%len(markAlphabet)]
	}
	return string(b[:])
}

var scriptCloseRe = regexp.MustCompile(`(?i)</(script)`)

// inlineScript 把脚本源码转义成能放进内联 <script> 的形式（`</script` → `<\/script`，JS 里两者等价）。
func inlineScript(js []byte) []byte {
	return scriptCloseRe.ReplaceAll(js, []byte(`<\/$1`))
}

// servedHTML 返回帧里提供的 HTML：给每个起始标签加源码标记，再在 InsertionPoint 处插入设参数的 ParamsPrelude 与
// 内联的页面运行时、`__orynStart(<配置>)`。root 是帧根。runtimeJS 为空（没跑过 make web）时只标记与设参数、不注入。
// 运行时要等 DOMContentLoaded 才读参数声明；声明的值由 ParamsPrelude 在首次绘制前设上，页面不会先闪一下未设变量的样子。
func servedHTML(src []byte, path, root string, runtime bool, runtimeJS []byte) []byte {
	mark := newMark()
	out := htmlsrc.MarkSources(src, mark)
	if pre := ParamsPrelude(src); pre != nil {
		at := htmlsrc.InsertionPoint(out)
		out = slices.Concat(out[:at], pre, out[at:])
	}
	return injectRuntime(out, runtimeConfig{V: 1, Mark: mark, Path: path, Root: root, Runtime: runtime}, runtimeJS)
}

// injectRuntime 在 InsertionPoint 处插入内联的页面运行时与 `__orynStart(<cfg>)`；runtimeJS 为空时原样返回。
func injectRuntime(page []byte, cfg runtimeConfig, runtimeJS []byte) []byte {
	if len(runtimeJS) == 0 {
		return page
	}
	c, _ := json.Marshal(cfg) // json 把 < > & 转义成 \u00XX
	at := htmlsrc.InsertionPoint(page)
	var b bytes.Buffer
	b.Grow(len(page) + len(runtimeJS) + len(c) + 48)
	b.Write(page[:at])
	b.WriteString("<script>")
	b.Write(runtimeJS)
	b.WriteString("\n__orynStart(")
	b.Write(c)
	b.WriteString(");</script>")
	b.Write(page[at:])
	return b.Bytes()
}

// viewerAssets 是类型化 artifact 查看器的构建产物（在嵌入的 Web 构建产物的 sandbox/viewers/ 下），
// 帧根下经 `_oryn/viewer/<名字>` 提供；值是 Content-Type。
var viewerAssets = map[string]string{
	"document.js":  "text/javascript; charset=utf-8",
	"document.css": "text/css; charset=utf-8",
	"slides.js":    "text/javascript; charset=utf-8",
	"slides.css":   "text/css; charset=utf-8",
}

// viewerConfig 是查看器页里 `<script type="application/json" id="oryn-viewer">` 的内容。Entry 是相对路径：
// 查看器按页面地址解析它与 images/，所以读到的是页面所在的那个版本。Root 是帧根（运行时端点与查看器资源都在它下面）。
type viewerConfig struct {
	Type    string `json:"type"`
	Format  int    `json:"format"`
	Version int    `json:"version"`
	Head    int    `json:"head"`
	Entry   string `json:"entry"`
	Locale  string `json:"locale,omitempty"`
	Root    string `json:"root"`
}

// viewerPage 生成类型化 artifact 的查看器页：样式、配置、模块脚本与挂载点 #root。查看器资源从
// `<root>_oryn/viewer/<type>.{css,js}` 加载；`?view=thumbnail|print` 留在页面地址里由查看器自己读取。
func viewerPage(c viewerConfig) []byte {
	data, _ := json.Marshal(c) // < > & 被转义，配置里不可能出现 </script>
	asset := html.EscapeString(c.Root + ReservedPrefix + "/viewer/" + c.Type)
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html")
	if c.Locale != "" {
		b.WriteString(` lang="` + html.EscapeString(c.Locale) + `"`)
	}
	b.WriteString(`><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<link rel="stylesheet" href="` + asset + `.css">`)
	b.WriteString(`<script type="application/json" id="oryn-viewer">`)
	b.Write(data)
	b.WriteString(`</script><script type="module" src="` + asset + `.js"></script>`)
	b.WriteString("</head><body><div id=\"root\"></div></body></html>\n")
	return b.Bytes()
}

// Host 以渲染模式给控制面的 headless Chromium 提供内容。
var _ render.Site = (*Host)(nil)

// RenderTarget 实现 render.Site：解析渲染作业的版本（0 = 当前最新）与是否严格（最新版本声明了 files 或 sample）。
// artifact 不存在回 not_found，在回收站里回 deleted，版本不存在回 not_found。
func (h *Host) RenderTarget(ctx context.Context, id string, version int) (int, bool, error) {
	a, err := h.s.Get(ctx, id)
	if err != nil {
		return 0, false, err
	}
	if a.Deleted() {
		return 0, false, deletedErr(a.ID)
	}
	head, err := h.s.Version(ctx, a.ID, 0)
	if err != nil {
		return 0, false, err
	}
	strict := slices.Contains(head.Capabilities, "files") || slices.Contains(head.Capabilities, "sample")
	if version == 0 {
		return head.Version, strict, nil
	}
	v, err := h.s.Version(ctx, a.ID, version)
	if err != nil {
		return 0, false, err
	}
	return v.Version, strict, nil
}

// ServeRender 实现 render.Site：以「渲染模式」提供作业根之内的路径 p——版本里的文件原样提供（不注入运行时、
// 不标源码），响应头同帧，但 'self' 换成作业根的 URL、没有 frame-ancestors（渲染时页面就是顶层文档）。
// 类型化 artifact 的根是查看器页（同帧，但没有运行时：查看器只读显示），`_oryn/viewer/<名字>` 提供查看器资源。
// 设计检查（页面地址带 `__oryn_probe=1`）时 HTML 页面另注入探针配置，`dist/probe.js` 是嵌入的探针脚本（见 withProbe）。
func (h *Host) ServeRender(w http.ResponseWriter, r *http.Request, t render.Target, p string) {
	setFrameHeaders(w, r, framePolicy(sandboxFrame, t.Self, "", t.Strict))
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		frameError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if name, ok := strings.CutPrefix(strings.TrimPrefix(p, "/"), viewerPrefix); ok {
		h.serveViewerAsset(w, r, name)
		return
	}
	path, version, ok := framePath(p, t.Version)
	if !ok {
		frameError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if path == probeScriptPath {
		h.serveProbeScript(w, r)
		return
	}
	ctx := r.Context()
	if atFrameRoot(p) {
		a, err := h.s.Get(ctx, t.ArtifactID)
		if err != nil {
			h.fail(w, err)
			return
		}
		if a.Type != "" {
			v, err := h.s.Version(ctx, a.ID, version)
			if err != nil {
				h.fail(w, err)
				return
			}
			page := viewerPage(viewerConfig{Type: a.Type, Format: v.Format, Version: v.Version, Head: a.Version, Entry: a.Entry(), Root: t.Self})
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(page))
			return
		}
		// 样机导出（?view=mockup）：按查询参数生成样机页（不受画布自带 index.html 影响）
		if r.URL.Query().Get("view") == "mockup" {
			page, ok, err := h.s.mockupFor(ctx, a, version, r.URL.Query())
			if err != nil {
				h.fail(w, err)
				return
			}
			if ok {
				writePage(w, r, page)
				return
			}
		}
		// 没有自己的 index.html 的画布：根是生成的概览页（缩略图带 ?view=thumbnail，由概览脚本自己读取）
		page, ok, err := h.s.overviewFor(ctx, a, version, "")
		if err != nil {
			h.fail(w, err)
			return
		}
		if ok {
			writePage(w, r, page)
			return
		}
	}
	rc, file, err := h.s.Open(ctx, t.ArtifactID, version, path)
	if err != nil {
		h.fail(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", file.Mime)
	if !IsHTML(path) {
		http.ServeContent(w, r, "", time.Time{}, rc)
		return
	}
	// 渲染模式没有页面运行时：页面声明的调节参数由服务端算好的内联脚本设到根元素上
	src, err := io.ReadAll(rc)
	if err != nil {
		h.fail(w, err)
		return
	}
	src = withParamsPrelude(src)
	if r.URL.Query().Get(design.ProbeQuery) == "1" {
		if src, err = h.withProbe(ctx, t.ArtifactID, version, path, src); err != nil {
			h.fail(w, err)
			return
		}
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(src))
}

// 设计检查的探针（plan-m29 C8）：渲染作业的页面地址带 `__oryn_probe=1` 时，页面在渲染完成后把测量结果写进 DOM，
// 由 --dump-dom 带出（check.go）。
const (
	probeAsset = "sandbox/probe.js"
	// probeScriptPath 是设计画框（dist/<id>.html）的引擎入口在首次渲染后加载的同源 `./probe.js`。
	// 渲染模式下这个路径一律回嵌入的探针脚本，不读版本里的文件：脚本自己只在页面地址带 __oryn_probe=1 时运行，
	// 别的渲染作业（缩略图等）加载到它也不起作用。脚本请求不带页面的查询串，所以不能按查询串判断。
	probeScriptPath = "dist/probe.js"
)

// probeConfig 是注入页面的 `window.__orynProbe`：aesthetic 打开后 8 条审美规则（画布选定了设计方向时），
// surface 为 app 时探针再关掉 eyebrow-excess 与 split-repeat。
type probeConfig struct {
	Aesthetic bool   `json:"aesthetic"`
	Surface   string `json:"surface"`
}

// serveProbeScript 提供嵌入的探针脚本；没构建 Web 时 404（页面里不会出现结果节点，检查记为 unverified）。
func (h *Host) serveProbeScript(w http.ResponseWriter, r *http.Request) {
	js, ok := h.asset(probeAsset)
	if !ok {
		frameError(w, http.StatusNotFound, "探针脚本不存在（Web 没有构建）")
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(js))
}

// withProbe 在 InsertionPoint 处插入 `window.__orynProbe=<配置>`（配置取自 artifact_designs）。设计画框由引擎入口
// 在 React 首次渲染提交后加载 ./probe.js；其余 HTML 页面没有这个入口，探针脚本直接内联在配置之后。
func (h *Host) withProbe(ctx context.Context, id string, version int, path string, src []byte) ([]byte, error) {
	var cfg probeConfig
	row, err := h.s.st.Q.GetArtifactDesign(ctx, id)
	switch {
	case err == nil:
		cfg.Aesthetic = deref(row.Direction) != ""
		cfg.Surface = deref(row.Surface)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	c, _ := json.Marshal(cfg) // json 把 < > & 转义成 \u00XX
	var b bytes.Buffer
	b.WriteString("<script>window.__orynProbe=")
	b.Write(c)
	b.WriteString(";</script>")
	frame, err := h.s.isDesignFrame(ctx, id, version, path)
	if err != nil {
		return nil, err
	}
	if js, ok := h.asset(probeAsset); ok && !frame {
		b.WriteString("<script>")
		b.Write(inlineScript(js))
		b.WriteString("</script>")
	}
	at := htmlsrc.InsertionPoint(src)
	return slices.Concat(src[:at], b.Bytes(), src[at:]), nil
}
