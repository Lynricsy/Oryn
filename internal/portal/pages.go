package portal

import (
	"encoding/json"
	"html"
	"net/http"
	"strconv"
)

// fail 写出错误：导航请求得到同状态码的极简 HTML 页，其余得到 {code, message}。
func (s *Service) fail(w http.ResponseWriter, nav bool, status int, code, msg string) {
	s.failPage(w, nav, status, code, msg, 0)
}

// failRefresh 同 fail，但导航请求的页面 3 秒后自动刷新（服务还在启动时自己会好）。
func (s *Service) failRefresh(w http.ResponseWriter, nav bool, status int, code, msg string) {
	s.failPage(w, nav, status, code, msg, 3)
}

func (s *Service) failPage(w http.ResponseWriter, nav bool, status int, code, msg string, refresh int) {
	noStore(w)
	if nav {
		page(w, s.base.Origin, status, msg, refresh,
			`<p>`+html.EscapeString(msg)+`</p><p class="code">`+html.EscapeString(code)+`</p>`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}

// wakingPage 是唤醒过渡页：503 + Retry-After: 2，每 2 秒自动刷新。
func wakingPage(w http.ResponseWriter, origin string) {
	noStore(w)
	w.Header().Set("Retry-After", "2")
	page(w, origin, http.StatusServiceUnavailable, "正在唤醒…", 2,
		`<p class="spin" aria-hidden="true"></p><p>正在唤醒 orb，页面会自动刷新…</p>`)
}

// page 写出 portal 自己的极简 HTML 页（错误、过渡、Cookie 说明），可被主站内嵌，深浅色跟随系统。
func page(w http.ResponseWriter, origin string, status int, title string, refresh int, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'self' "+origin)
	h.Set("X-Robots-Tag", "noindex, nofollow")
	noStore(w)
	w.WriteHeader(status)
	meta := ""
	if refresh > 0 {
		meta = `<meta http-equiv="refresh" content="` + strconv.Itoa(refresh) + `">`
	}
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
` + meta + `
<title>` + html.EscapeString(title) + `</title>
<style>
:root{color-scheme:light dark;--bg:#f7f7f8;--fg:#1d1d1f;--muted:#6e6e73;--accent:#5b5bd6}
@media (prefers-color-scheme:dark){:root{--bg:#141416;--fg:#ececf0;--muted:#9a9aa3;--accent:#8d8dfa}}
html,body{height:100%;margin:0}
body{display:flex;align-items:center;justify-content:center;background:var(--bg);color:var(--fg);
font:15px/1.6 system-ui,-apple-system,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
main{max-width:32rem;padding:2rem;text-align:center}
a{color:var(--accent)}
.code{color:var(--muted);font:12px ui-monospace,SFMono-Regular,Menlo,monospace}
.spin{width:22px;height:22px;margin:0 auto 1rem;border-radius:50%;border:2px solid var(--muted);
border-top-color:var(--accent);animation:r 1s linear infinite}
@keyframes r{to{transform:rotate(360deg)}}
@media (prefers-reduced-motion:reduce){.spin{animation:none}}
</style>
</head>
<body><main>` + body + `</main></body>
</html>
`))
}
