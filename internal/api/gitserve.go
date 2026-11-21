package api

import (
	"net/http"
	"regexp"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/egress"
)

// gitPathRe 是主人入口 /git/<项目 id>.git/<端点> 接受的路径：只有 smart-HTTP 的三个端点，`..`、多余的段都进不来。
var gitPathRe = regexp.MustCompile(`^/git/(prj_[0-9A-HJKMNP-TV-Z]{26})\.git/(info/refs|git-upload-pack|git-receive-pack)$`)

// GitHandler 服务 /git/<项目 id>.git/<端点>（M24 阶段 ③ 托管仓库的主人入口）：不经 /api/v1 鉴权组，自己按设备令牌
// 鉴权（HTTP Basic 的密码位或 Bearer），从不读 Cookie。先鉴权再查项目：项目是否存在不向匿名者泄露。
// 错误一律 text/plain：git 会把正文显示给用户。
func (s *Server) GitHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := gitPathRe.FindStringSubmatch(r.URL.Path)
		if r.URL.RawPath != "" || m == nil {
			http.Error(w, "oryn: 仓库不存在", http.StatusNotFound)
			return
		}
		if _, ok := auth.GitCredential(r.Context(), s.Store, r); !ok {
			// 来自浏览器（带 Sec-Fetch-Site）时不回 WWW-Authenticate，免得弹出原生口令框
			if r.Header.Get("Sec-Fetch-Site") == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="Oryn"`)
			}
			http.Error(w, "oryn: 需要设备令牌（oryn login 后用 oryn clone 克隆）", http.StatusUnauthorized)
			return
		}
		if s.Repos == nil || s.Repos.Service == nil {
			http.Error(w, "oryn: 控制面没有可用的 git", http.StatusServiceUnavailable)
			return
		}
		s.Repos.Service.ServeHostedGit(w, r, m[1], m[2], egress.HostedPusher{})
	})
}
