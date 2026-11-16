package artifacts

import (
	"net/http"
	"net/url"
	"strings"
)

// CDNs 是 artifact 页面除自身文件之外唯一可以加载资源的公共 CDN（非严格模式的沙箱策略放行它们；给 agent 的教程列出它们）。
var CDNs = []string{
	"https://cdnjs.cloudflare.com",
	"https://cdn.jsdelivr.net",
	"https://unpkg.com",
	"https://esm.sh",
	"https://cdn.tailwindcss.com",
	"https://fonts.googleapis.com",
	"https://fonts.gstatic.com",
}

// 帧 CSP 的 sandbox 指令。不含 allow-same-origin：页面一律运行在不透明源里。
const (
	// sandboxFrame 用于内嵌在查看器里的帧。
	sandboxFrame = "sandbox allow-scripts allow-forms allow-downloads"
	// sandboxTopLevel 用于「在浏览器中打开」的顶层帧：允许弹窗、对话框与用户触发的顶层跳转。
	sandboxTopLevel = sandboxFrame + " allow-popups allow-popups-to-escape-sandbox allow-modals allow-top-navigation-by-user-activation"
)

// permissionsPolicy 关掉页面能申请的全部设备与剪贴板权限。
const permissionsPolicy = "camera=(), microphone=(), geolocation=(), display-capture=(), usb=(), serial=(), hid=(), " +
	"bluetooth=(), midi=(), payment=(), clipboard-read=(), clipboard-write=(), fullscreen=()"

// framePolicy 返回帧响应的 CSP。self 是页面自己的文件所在的源（泛域名模式 `'self'`，主域回退模式
// `<public_url 源>/_af/<令牌>/`）；strict 时不放行任何 CDN；ancestors 是 frame-ancestors 的值。
func framePolicy(sandbox, self, ancestors string, strict bool) string {
	cdn := ""
	if !strict {
		cdn = " " + strings.Join(CDNs, " ")
	}
	return sandbox + "; " +
		"default-src 'none'; " +
		"script-src " + self + " 'unsafe-inline' 'unsafe-eval' blob:" + cdn + "; " +
		"style-src " + self + " 'unsafe-inline'" + cdn + "; " +
		"font-src " + self + " data:" + cdn + "; " +
		"img-src " + self + " data: blob:" + cdn + "; " +
		"media-src " + self + " data: blob:; " +
		"connect-src " + self + cdn + "; " +
		"worker-src " + self + " blob:; " +
		"frame-src " + self + "; " +
		"form-action 'none'; " +
		"base-uri " + self + "; " +
		"webrtc 'block'; " +
		"frame-ancestors " + ancestors
}

// setFrameHeaders 写出帧响应的安全头。请求带 `Origin: null`（不透明源的页面读自己的文件、加载 module 脚本
// 与字体）时回 `Access-Control-Allow-Origin: null`。不发 Cross-Origin-Resource-Policy：不透明源与任何源都
// 不同源，发了会把页面自己的子资源挡掉。
func setFrameHeaders(w http.ResponseWriter, r *http.Request, csp string) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("Permissions-Policy", permissionsPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	if r.Header.Get("Origin") == "null" {
		h.Set("Access-Control-Allow-Origin", "null")
		h.Add("Vary", "Origin")
	}
}

// ancestorOrigin 把申请帧的 API 请求的 Origin 头规整成 frame-ancestors 的源表达式；不是合法的
// http(s) 源（含缺省）时返回 fallback。规整后才拼进 CSP，避免请求头往策略里注入指令。
func ancestorOrigin(origin, fallback string) string {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(u.Host, " ;,'\"") {
		return fallback
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}
