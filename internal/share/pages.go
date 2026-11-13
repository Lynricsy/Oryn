package share

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// notFoundPage 是撤销 / 不存在的分享页（HTTP 404）：不加载 SPA，随系统深浅色。
const notFoundPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="robots" content="noindex">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Not found · Oryn</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;font:15px/1.5 system-ui,sans-serif;background:#fafafa;color:#3f3f46}
main{text-align:center;padding:24px}
h1{font-size:18px;font-weight:600;margin:0 0 6px;color:#18181b}
p{margin:0;color:#71717a}
@media (prefers-color-scheme: dark){body{background:#0f0f11;color:#d4d4d8}h1{color:#fafafa}p{color:#a1a1aa}}
</style>
</head>
<body><main><h1>This link is no longer available</h1><p>The share may have been revoked or never existed.</p></main></body>
</html>
`

// notFoundCSP 只放行页面自带的内联样式。
const notFoundCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func writeNotFoundPage(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	setPublicHeaders(h)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", notFoundCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(notFoundPage))
	}
}

func allowRead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

// PageHandler 挂在 `/s/*`：`/s/<slug>` 交给 spa（返回 index.html），`/s/<slug>.md` 导出 Markdown；
// 分享不存在 / 已撤销 → HTTP 404 极简页。只接受 GET / HEAD。
func (s *Service) PageHandler(spa http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowRead(w, r) {
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/s/")
		md := strings.HasSuffix(rest, ".md")
		slug := strings.TrimSuffix(rest, ".md")
		if r.URL.RawPath != "" {
			slug = "" // 含编码字符的路径不可能是 slug
		}
		sh, err := s.activeBySlug(r.Context(), slug)
		if err != nil {
			writeNotFoundPage(w, r)
			return
		}
		if md {
			s.serveMarkdown(w, r, sh)
			return
		}
		setPublicHeaders(w.Header())
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = "/", ""
		spa.ServeHTTP(&noStoreWriter{ResponseWriter: w}, r2)
	})
}

// noStoreWriter 在写出响应头时把 Cache-Control 定为 no-store（SPA 处理器会把 index.html 设成 no-cache）。
type noStoreWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *noStoreWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *noStoreWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (s *Service) serveMarkdown(w http.ResponseWriter, r *http.Request, sh db.ThreadShare) {
	ctx := r.Context()
	sc, err := s.scopeOf(ctx, sh)
	if err != nil {
		writeNotFoundPage(w, r)
		return
	}
	branch, err := sc.branchEntries(ctx, s.st.Q)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	title := publicTitle(sc.title)
	var buf bytes.Buffer
	meta := transcript.MarkdownMeta{Title: title, Mode: sh.Mode, SharedAt: time.UnixMilli(sh.CreatedAt).UTC()}
	if err := transcript.Markdown(&buf, meta, branch); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	setPublicHeaders(h)
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	h.Set("Content-Disposition", inlineDisposition(title+".md"))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

// inlineDisposition 是带 UTF-8 文件名的 inline 头（文件名编码与附件下载同一处）。
func inlineDisposition(name string) string {
	return "inline" + strings.TrimPrefix(attachments.Disposition(name), "attachment")
}
