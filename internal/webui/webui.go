// Package webui 内嵌 Web 客户端构建产物，并以 SPA 方式提供。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler 提供静态文件；找不到的非 API 路径回退到 index.html（客户端路由）。
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		// 未执行 make web：返回内置占位页
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write([]byte(placeholder))
		})
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

const placeholder = `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><title>Oryn</title></head>
<body>
<p>Oryn 控制面 <span id="v">…</span>（Web 客户端未构建，运行 <code>make web</code>）</p>
<script>fetch("/healthz").then(r=>r.json()).then(j=>{document.getElementById("v").textContent=j.version})</script>
</body>
</html>
`
