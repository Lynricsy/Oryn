package api

import (
	"net/http"
	"strings"

	"github.com/Lynricsy/Oryn/internal/auth"
)

// AppSessionPath 是壳把主窗口导航过来兑换一次性码的端点（不在 /api/v1 下，由 Mounts 挂载）。
const AppSessionPath = "/auth/app"

// maxNextLen 是 next 参数的长度上限。
const maxNextLen = 2048

// AppSessionHandler 兑换 App 会话一次性码（M6）：`GET /auth/app?code=<oac_…>&next=<站内路径>`。
// 成功写会话 Cookie；无论成败都 302 到 next（失败时 SPA 的 `GET me` 得 401，由壳重新换取）。
func (s *Server) AppSessionHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			WriteError(w, http.StatusMethodNotAllowed, "invalid_request", "只接受 GET / HEAD")
			return
		}
		if !s.rateLimit(w, r, auth.LimitLogin) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		q := r.URL.Query()
		next := safeNext(q.Get("next"))
		cookie, _, ok, err := s.Auth.Service.RedeemAppCode(r.Context(), q.Get("code"), r.UserAgent())
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if ok {
			auth.SetAppSessionCookie(w, cookie)
		}
		http.Redirect(w, r, next, http.StatusFound)
	})
}

// safeNext 只放行站内绝对路径：以 `/` 开头、第二个字符不是 `/` 或 `\`、不含 CR / LF 等控制字符、长度 ≤2048；
// 否则返回 `/`。控制字符一律拒绝：浏览器解析 URL 时会删掉 Tab / CR / LF，`/\t/evil` 会变成 `//evil`。
func safeNext(next string) string {
	if next == "" || len(next) > maxNextLen || next[0] != '/' {
		return "/"
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	if strings.ContainsFunc(next, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		return "/"
	}
	return next
}
