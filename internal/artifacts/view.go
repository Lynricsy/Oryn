package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"golang.org/x/net/html"
)

// 视图帧（M33，docs/plan-m33.md 的 C8）：转录里 render_html 调用的宿主。寻址与令牌沿用 artifact 帧
// （`a-<令牌>.<portal_domain>` 或 `<public_url>/_af/<令牌>/`），内容与策略独立：帧只有根一个地址，
// 每次取根时经 load 现取模型写的 HTML，拼上视图运行时与内置库后整页返回。

// 视图帧用到的嵌入资源（web/vite.sandbox.config.ts 的 `--mode view`）。
const (
	viewRuntimeAsset  = "sandbox/view.js"
	viewChartAsset    = "sandbox/lib/chart.js"
	viewTailwindAsset = "sandbox/lib/tailwind.js"
)

// maxPublicFrames 是公开分享页（匿名访客）申请的视图帧的上限；超出时只淘汰最旧的公开帧，访客挤不掉主人的帧。
const maxPublicFrames = 64

// 内置库（render_html 的 libs）。
const (
	ViewLibTailwind = "tailwind"
	ViewLibChart    = "chartjs"
)

// ViewTool 是由视图帧承载的工具（schema/view-tools.json）。
const ViewTool = "render_html"

// ViewDoc 是一个视图帧的内容：模型写的 HTML（完整文档或 body 片段）与要离线注入的内置库。
type ViewDoc struct {
	HTML string
	Libs []string
}

// ErrViewNotFound 由 load 返回：这次调用已不在转录里、分享已撤销等，帧根回 404。
var ErrViewNotFound = errors.New("artifacts: 视图不存在")

// ViewDocFromArgs 从 render_html 调用的参数取出视图内容：html 必须是非空字符串（参数修复不改它）；
// libs 兼容单个字符串与数组，只保留认得的库并去重。html 不合法时 ok 为假。
func ViewDocFromArgs(args json.RawMessage) (ViewDoc, bool) {
	var p struct {
		HTML any `json:"html"`
		Libs any `json:"libs"`
	}
	if json.Unmarshal(args, &p) != nil {
		return ViewDoc{}, false
	}
	s, ok := p.HTML.(string)
	if !ok || s == "" {
		return ViewDoc{}, false
	}
	doc := ViewDoc{HTML: s}
	var libs []any
	switch v := p.Libs.(type) {
	case string:
		libs = []any{v}
	case []any:
		libs = v
	}
	for _, l := range libs {
		name, _ := l.(string)
		if (name == ViewLibTailwind || name == ViewLibChart) && !slices.Contains(doc.Libs, name) {
			doc.Libs = append(doc.Libs, name)
		}
	}
	return doc, true
}

// viewPolicy 返回视图帧的 CSP：页面可以执行内联脚本、从任意 https 主机加载脚本 / 样式 / 字体 / 图片 / 媒体，
// 但不能发起连接（fetch / XHR / WebSocket）、不能提交表单。ancestors 是 frame-ancestors 的值。
func viewPolicy(ancestors string) string {
	return "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' https:; style-src 'unsafe-inline' https:; " +
		"img-src data: blob: https:; font-src data: https:; media-src data: blob: https:; connect-src 'none'; " +
		"form-action 'none'; base-uri 'none'; webrtc 'block'; frame-ancestors " + ancestors
}

// viewAssets 是拼装视图文档用的资源，都已经过 inlineScript 转义；没跑过 make web 时为空（只注入 meta）。
type viewAssets struct {
	runtime, chart, tailwind []byte
}

// viewTailwindTheme 把 Tailwind v4 的主题接到视图的 CSS 变量上（bg-surface、text-fg、border-border……），
// 不带 preflight：原生元素的样式由视图运行时的基础样式负责。暗色变体跟随 `<html data-scheme="dark">`。
const viewTailwindTheme = `@layer theme, base, components, utilities;
@import "tailwindcss/theme.css" layer(theme);
@import "tailwindcss/utilities.css" layer(utilities);
@custom-variant dark (&:where([data-scheme=dark], [data-scheme=dark] *));
@theme inline {
  --color-bg: var(--bg);
  --color-fg: var(--fg);
  --color-muted: var(--muted);
  --color-border: var(--border);
  --color-surface: var(--surface);
  --color-accent: var(--accent);
  --color-success: var(--success);
  --color-warning: var(--warning);
  --color-error: var(--error);
  --color-c1: var(--c1);
  --color-c2: var(--c2);
  --color-c3: var(--c3);
  --color-c4: var(--c4);
  --color-c5: var(--c5);
  --color-c6: var(--c6);
  --font-sans: var(--font);
  --font-mono: var(--mono);
  --radius-lg: var(--radius);
}`

// viewChartInit 在 Chart.js 之后执行：按视图主题设默认配色（视图运行时提供 __orynViewChart）。
const viewChartInit = `window.__orynViewChart&&window.__orynViewChart();`

// viewHead 返回注入 `<head>` 开头的内容。
func viewHead(doc ViewDoc, a viewAssets) []byte {
	var b bytes.Buffer
	b.WriteString(`<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">`)
	script := func(js []byte) {
		if len(js) == 0 {
			return
		}
		b.WriteString("<script>")
		b.Write(js)
		b.WriteString("</script>")
	}
	script(a.runtime)
	if slices.Contains(doc.Libs, ViewLibTailwind) && len(a.tailwind) > 0 {
		b.WriteString(`<style type="text/tailwindcss">`)
		b.WriteString(viewTailwindTheme)
		b.WriteString("</style>")
		script(a.tailwind)
	}
	if slices.Contains(doc.Libs, ViewLibChart) && len(a.chart) > 0 {
		script(a.chart)
		script([]byte(viewChartInit))
	}
	return b.Bytes()
}

// viewDocument 把模型写的 HTML 拼成帧里的整页：越过开头的空白、注释与 doctype 后，`<html>` 之后紧跟 `<head>`
// 时插在 `<head>` 起始标签之后；以 `<head>` 开头同样；只有 `<html>` 时在它之后补一个 `<head>`；都不是
// （body 片段）时整体包成文档。
func viewDocument(doc ViewDoc, a viewAssets) []byte {
	src := []byte(doc.HTML)
	head := viewHead(doc, a)
	afterHTML, afterHead := leadingTags(src)
	var b bytes.Buffer
	b.Grow(len(src) + len(head) + 96)
	switch {
	case afterHead >= 0:
		b.Write(src[:afterHead])
		b.Write(head)
		b.Write(src[afterHead:])
	case afterHTML >= 0:
		b.Write(src[:afterHTML])
		b.WriteString("<head>")
		b.Write(head)
		b.WriteString("</head>")
		b.Write(src[afterHTML:])
	default:
		b.WriteString("<!doctype html><html><head>")
		b.Write(head)
		b.WriteString("</head><body>")
		b.Write(src)
		b.WriteString("</body></html>")
	}
	return b.Bytes()
}

// leadingTags 扫描文档开头（BOM、空白、注释、doctype 之后）的 `<html>` 与 `<head>` 起始标签，返回各自结束的偏移；
// 没有时为 -1。只认开头连续出现的这两个标签，脚本或正文里的 `<head>` 字样不算。
func leadingTags(src []byte) (afterHTML, afterHead int) {
	afterHTML, afterHead = -1, -1
	z := html.NewTokenizer(bytes.NewReader(src))
	pos := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		raw := z.Raw()
		end := pos + len(raw)
		switch tt {
		case html.CommentToken, html.DoctypeToken:
		case html.TextToken:
			text := raw
			if pos == 0 {
				text = bytes.TrimPrefix(text, []byte("\uFEFF"))
			}
			if len(bytes.TrimSpace(text)) != 0 {
				return
			}
		case html.StartTagToken:
			name, _ := z.TagName()
			switch {
			case string(name) == "html" && afterHTML < 0 && afterHead < 0:
				afterHTML = end
			case string(name) == "head":
				afterHead = end
				return
			default:
				return
			}
		default:
			return
		}
		pos = end
	}
}

// ErrFramesFull：帧表已满且全是主人的帧，公开分享页（匿名访客）的申请被拒。
var ErrFramesFull = errors.New("artifacts: frame table full")

// OpenViewFrame 申请一个视图帧；load 在每次取帧根时调用。public 的帧计入匿名配额，帧表满且没有公开帧可淘汰时
// 返回 ErrFramesFull（主人的申请总能成功）。origin 是申请请求的 Origin 头，成为帧的 frame-ancestors；缺省或不合法时
// 用 public_url 的源。
func (h *Host) OpenViewFrame(load func(context.Context) (ViewDoc, error), origin string, public bool) (token, url string, err error) {
	token, ok := h.add(&frame{view: load, public: public, ancestors: ancestorOrigin(origin, h.base.Origin)})
	if !ok {
		return "", "", ErrFramesFull
	}
	return token, h.frameURL(token), nil
}

// serveView 提供视图帧：只有帧根的 GET / HEAD，其余一律 404。
func (h *Host) serveView(w http.ResponseWriter, r *http.Request, f frame, p string) {
	setFrameHeaders(w, r, viewPolicy(f.ancestors))
	if p != "/" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		frameError(w, http.StatusNotFound, "文件不存在")
		return
	}
	doc, err := f.view(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, ErrViewNotFound):
			frameError(w, http.StatusNotFound, "视图不存在")
		case errors.Is(err, context.Canceled):
			frameError(w, http.StatusInternalServerError, "暂时无法提供这个视图")
		default:
			slog.Warn("artifacts: 视图帧请求失败", "err", err)
			frameError(w, http.StatusInternalServerError, "暂时无法提供这个视图")
		}
		return
	}
	writePage(w, r, viewDocument(doc, h.view))
}

// loadViewAssets 读出视图帧的嵌入资源（转义成可内联的形式）。
func loadViewAssets(asset func(string) ([]byte, bool)) viewAssets {
	read := func(name string) []byte {
		if b, ok := asset(name); ok {
			return inlineScript(b)
		}
		return nil
	}
	return viewAssets{runtime: read(viewRuntimeAsset), chart: read(viewChartAsset), tailwind: read(viewTailwindAsset)}
}
