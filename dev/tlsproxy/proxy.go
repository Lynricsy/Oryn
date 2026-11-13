package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// staticPrefix 是 -static 目录的挂载点（验收 D11 的更新清单与安装包放在这里）。
const staticPrefix = "/updates/"

// newHandler 返回 TLS 终止后的处理器：staticDir 非空时 /updates/ 走本地文件，其余反代到 target。
// 反代保留原始 Host（orynd 按 Host 分流 portal，Cookie 的 __Host- 前缀也依赖主机不变）、
// 设置 X-Forwarded-For / -Host / -Proto、不缓冲（SSE 与流式响应逐块转发），
// WebSocket 升级由 httputil.ReverseProxy 原生透传。
func newHandler(target *url.URL, staticDir string) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		FlushInterval: -1,
	}
	if staticDir == "" {
		return proxy
	}
	files := http.StripPrefix(staticPrefix, http.FileServer(http.Dir(staticDir)))
	mux := http.NewServeMux()
	mux.Handle(staticPrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	}))
	mux.Handle("/", proxy)
	return mux
}
