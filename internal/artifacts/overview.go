package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// overviewMarker 是生成的画布概览页开头的固定注释：导入时据此认出导出 zip 里补上的 index.html 并去掉它。
const overviewMarker = "<!-- oryn-canvas-overview -->"

// overviewAsset 是概览页脚本在嵌入的 Web 构建产物里的路径。
const overviewAsset = "sandbox/overview.js"

// overviewScript 返回可内联的概览页脚本；没构建 Web 时为空，概览页只显示画框链接的列表。
var overviewScript = sync.OnceValue(func() []byte {
	if js, ok := webui.Asset(overviewAsset); ok {
		return inlineScript(js)
	}
	return nil
})

// overviewText 是概览页的界面文案（字段与 web/sandbox/overview/main.ts 的 OverviewText 对应）。
type overviewText struct {
	Pages     string `json:"pages"`
	ZoomIn    string `json:"zoomIn"`
	ZoomOut   string `json:"zoomOut"`
	ZoomReset string `json:"zoomReset"`
	Fit       string `json:"fit"`
	Empty     string `json:"empty"`
	Open      string `json:"open"` // 含 {name} 占位
	Invalid   string `json:"invalid"`
}

var overviewTexts = map[string]overviewText{
	"zh": {
		Pages: "页面", ZoomIn: "放大", ZoomOut: "缩小", ZoomReset: "缩放到 100%", Fit: "适合全部",
		Empty: "这个页面还没有画框", Open: "打开「{name}」", Invalid: "无法显示这个画布",
	},
	"en": {
		Pages: "Pages", ZoomIn: "Zoom in", ZoomOut: "Zoom out", ZoomReset: "Zoom to 100%", Fit: "Fit all",
		Empty: "This page has no frames yet", Open: "Open “{name}”", Invalid: "This canvas can't be displayed",
	},
}

// overviewLang 把界面语言归到一套文案：zh 开头的用中文，其它给了的用英文；没给时两套都给，由页面按浏览器语言挑。
func overviewLang(locale string) map[string]overviewText {
	switch {
	case locale == "":
		return overviewTexts
	case strings.HasPrefix(strings.ToLower(locale), "zh"):
		return map[string]overviewText{"zh": overviewTexts["zh"]}
	default:
		return map[string]overviewText{"en": overviewTexts["en"]}
	}
}

// overviewCSS 是概览页的内联样式（明暗两套跟随 prefers-color-scheme）。
const overviewCSS = `:root{color-scheme:light dark;--bg:#f4f4f5;--fg:#18181b;--muted:#71717a;--line:#e4e4e7;--surface:#fff;--dot:#d4d4d8;` +
	`--ink:#18181b;--red:#dc2626;--orange:#ea580c;--yellow:#ca8a04;--green:#16a34a;--blue:#2563eb;--violet:#7c3aed}` +
	`@media (prefers-color-scheme:dark){:root{--bg:#0f0f11;--fg:#fafafa;--muted:#a1a1aa;--line:#2e2e33;--surface:#1c1c1f;--dot:#2e2e33;` +
	`--ink:#fafafa;--red:#f87171;--orange:#fb923c;--yellow:#facc15;--green:#4ade80;--blue:#60a5fa;--violet:#a78bfa}}` +
	`*{box-sizing:border-box}` +
	`html,body{margin:0;height:100%;overflow:hidden;background:var(--bg);color:var(--fg);font:14px/1.4 system-ui,-apple-system,"Segoe UI",sans-serif}` +
	`.bar{position:fixed;top:0;left:0;right:0;height:48px;display:flex;align-items:center;gap:12px;padding:0 12px;background:var(--surface);border-bottom:1px solid var(--line);z-index:2}` +
	`.bar h1{margin:0;font-size:14px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;min-width:0;flex:0 1 auto}` +
	`.tabs{display:flex;gap:4px;overflow-x:auto;flex:1 1 auto;min-width:0}` +
	`.zoom{display:flex;gap:4px;margin-left:auto;flex:none}` +
	`button{font:inherit;color:inherit;background:transparent;border:1px solid transparent;border-radius:6px;padding:4px 8px;cursor:pointer;white-space:nowrap}` +
	`button:hover{background:var(--bg)}button:focus-visible,.open:focus-visible{outline:2px solid var(--blue);outline-offset:1px}` +
	`[role=tab][aria-selected=true]{background:var(--bg);border-color:var(--line);font-weight:600}` +
	`.stage{position:fixed;inset:48px 0 0 0;overflow:hidden;touch-action:none;cursor:grab;background-image:radial-gradient(var(--dot) 1px,transparent 1px);background-size:24px 24px}` +
	`.thumbnail .stage{inset:0;background-image:none}` +
	`.world{position:absolute;left:0;top:0;transform-origin:0 0}` +
	`.page{position:absolute;background:#fff;box-shadow:0 1px 3px rgba(0,0,0,.12);outline:1px solid var(--line)}` +
	`.page iframe{display:block;width:100%;height:100%;border:0;pointer-events:none;background:#fff}` +
	`.label{position:absolute;left:0;bottom:100%;margin-bottom:calc(6px / var(--z,1));font-size:calc(12px / var(--z,1));color:var(--muted);white-space:nowrap;max-width:100%;overflow:hidden;text-overflow:ellipsis}` +
	`.open{position:absolute;inset:0;cursor:pointer}` +
	`.notes{position:absolute;left:0;top:0;overflow:visible;pointer-events:none}` +
	`.c-ink{--c:var(--ink)}.c-red{--c:var(--red)}.c-orange{--c:var(--orange)}.c-yellow{--c:var(--yellow)}` +
	`.c-green{--c:var(--green)}.c-blue{--c:var(--blue)}.c-violet{--c:var(--violet)}` +
	`.line{fill:none;stroke:var(--c);stroke-linecap:round;stroke-linejoin:round}.marker{stroke-opacity:.35}` +
	`.text{fill:var(--c)}.sticky{fill:var(--c);fill-opacity:.25;stroke:var(--c);stroke-opacity:.5}.done{opacity:.5}` +
	`.sticky-text{height:100%;overflow:hidden;padding:12px;font-size:16px;color:var(--fg);white-space:pre-wrap;overflow-wrap:anywhere}` +
	`.message{position:fixed;left:50%;top:50%;transform:translate(-50%,-50%);margin:0;color:var(--muted)}` +
	`#oryn-fallback{height:100%;overflow:auto;padding:24px}`

// overviewData 是概览页里 `<script type="application/json" id="oryn-canvas">` 的内容。
type overviewData struct {
	Title string                  `json:"title"`
	Doc   json.RawMessage         `json:"doc"`
	Text  map[string]overviewText `json:"text"`
}

// pageHref 把 artifact 里的相对路径逐段编码成相对地址（文件名里的 #、?、% 与空格不会变成片段、查询或转义）。
func pageHref(file string) string {
	segs := strings.Split(file, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// canvasOverview 生成画布的只读概览页：以 overviewMarker 开头的自包含 index.html，内联样式、数据与概览脚本。
// 画框是指向画框文件的相对路径，所以概览页要放在与画框文件同一个版本的根下提供。没有脚本（没构建 Web）时
// 页面显示按画布页分组的画框链接列表。locale 为空时两套文案都给，由页面按浏览器语言挑。
func canvasOverview(doc canvas.Doc, title, locale string) []byte {
	// json.Marshal 把 < > & 转义成 \u00XX，数据里不会出现 </script>
	data, _ := json.Marshal(overviewData{Title: title, Doc: doc.JSON(), Text: overviewLang(locale)})
	var b bytes.Buffer
	b.WriteString(overviewMarker + "\n<!doctype html>\n<html")
	if locale != "" {
		b.WriteString(` lang="` + html.EscapeString(locale) + `"`)
	}
	b.WriteString(`><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString("<title>" + html.EscapeString(title) + "</title><style>" + overviewCSS + "</style></head><body>")
	b.WriteString(`<main id="oryn-fallback"><h1>` + html.EscapeString(title) + "</h1>")
	for _, c := range doc.Canvases {
		b.WriteString("<h2>" + html.EscapeString(c.Name) + "</h2><ul>")
		for _, p := range doc.Pages {
			if p.Canvas == c.ID {
				b.WriteString(`<li><a href="` + html.EscapeString(pageHref(p.File)) + `">` + html.EscapeString(p.Name) + "</a></li>")
			}
		}
		b.WriteString("</ul>")
	}
	b.WriteString(`</main><script type="application/json" id="oryn-canvas">`)
	b.Write(data)
	b.WriteString("</script>")
	if js := overviewScript(); len(js) > 0 {
		b.WriteString("<script>")
		b.Write(js)
		b.WriteString("</script>")
	}
	b.WriteString("</body></html>\n")
	return b.Bytes()
}

// overviewFor 在 artifact a 的版本 version 是没有自己的 index.html 的画布时返回为它生成的概览页，否则 ok 为假。
// 「在浏览器中打开」的顶层帧、渲染模式（缩略图）、导出的 zip 与发布页在根路径上用它。
func (s *Service) overviewFor(ctx context.Context, a Artifact, version int, locale string) (page []byte, ok bool, err error) {
	if a.Type != "" {
		return nil, false, nil
	}
	files, err := s.Files(ctx, a.ID, version)
	if err != nil {
		return nil, false, err
	}
	hasCanvas := false
	for _, f := range files {
		switch f.Path {
		case EntryHTML:
			return nil, false, nil
		case EntryCanvas:
			hasCanvas = true
		}
	}
	if !hasCanvas {
		return nil, false, nil
	}
	b, _, err := s.ReadFile(ctx, a.ID, version, EntryCanvas)
	if err != nil {
		return nil, false, err
	}
	doc, err := canvas.Parse(b)
	if err != nil {
		return nil, false, err
	}
	return canvasOverview(doc, a.Title, locale), true, nil
}

// writePage 把生成的 HTML 页面写成 200 响应（HEAD 只写头）。
func writePage(w http.ResponseWriter, r *http.Request, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(page)
	}
}

// isOverviewPage 报告 head（文件开头，可带 UTF-8 BOM）是不是生成的概览页。
func isOverviewPage(head []byte) bool {
	return bytes.HasPrefix(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")), []byte(overviewMarker))
}
