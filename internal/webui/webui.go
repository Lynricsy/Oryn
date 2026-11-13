// Package webui 内嵌 Web 客户端构建产物，并以 SPA 方式提供。
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// ContentSecurityPolicy 是 SPA 页面与静态资源的 CSP：脚本只许同源（Vite 产物无内联脚本），
// 样式允许内联（mermaid 生成的 SVG 带 <style>、KaTeX 与 React 的 style 属性），图片放行 data: / blob:
// （内联图片与附件预览），禁止任何插件、<base> 改写与被嵌入（防点击劫持）。
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"worker-src 'self' blob:; " +
	"manifest-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-src 'none'; " +
	"frame-ancestors 'none'"

// Handler 提供静态文件；找不到的非 API 路径回退到 index.html（客户端路由）。
// frameSrc 非空（启用 portal 时为 `<scheme>://*.<portal_domain>[:port]`）时，CSP 的 `frame-src 'none'`
// 换成 `frame-src <frameSrc>`，让线程页能内嵌 portal；`frame-ancestors 'none'` 与 X-Frame-Options 不变。
func Handler(frameSrc string) http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return handler(sub, frameSrc)
}

// withFrameSrc 按 frameSrc 改写 CSP 的 frame-src 指令。
func withFrameSrc(csp, frameSrc string) string {
	if frameSrc == "" {
		return csp
	}
	return strings.Replace(csp, "frame-src 'none'", "frame-src "+frameSrc, 1)
}

func setSecurityHeaders(h http.Header, csp string) {
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
}

func handler(sub fs.FS, frameSrc string) http.Handler {
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		// 未执行 make web：返回内置占位页；其内联脚本以哈希放行
		csp := withFrameSrc(placeholderCSP, frameSrc)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			setSecurityHeaders(w.Header(), csp)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write([]byte(placeholder))
		})
	}
	csp := withFrameSrc(ContentSecurityPolicy, frameSrc)
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header(), csp)
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// SPA 路由回退
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

const placeholderScript = `fetch("/healthz").then(r=>r.json()).then(j=>{document.getElementById("v").textContent=j.version})`

const placeholder = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>Oryn</title></head>
<body>
<p>Oryn 控制面 <span id="v">…</span>（Web 客户端未构建，运行 <code>make web</code>）</p>
<script>` + placeholderScript + `</script>
</body>
</html>
`

var placeholderCSP = func() string {
	sum := sha256.Sum256([]byte(placeholderScript))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	return strings.Replace(ContentSecurityPolicy, "script-src 'self'", "script-src 'self' "+hash, 1)
}()
