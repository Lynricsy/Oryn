package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/lfs"
)

// gitPathRe 是主人入口 /git/<项目 id>.git/<端点> 接受的路径：smart-HTTP 的三个端点与 Git LFS 的 batch、对象、锁接口，
// `..`、多余的段都进不来。
var gitPathRe = regexp.MustCompile(`^/git/(prj_[0-9A-HJKMNP-TV-Z]{26})\.git/(info/refs|git-upload-pack|git-receive-pack|info/lfs/objects/batch|info/lfs/objects/[0-9a-f]{64}|info/lfs/locks(?:/verify|/[A-Za-z0-9._-]{1,128}/unlock)?)$`)

const gitNeedToken = "oryn: 需要设备令牌（oryn login 后用 oryn clone 克隆）"

// GitHandler 服务 /git/<项目 id>.git/<端点>（M24 阶段 ③ 托管仓库的主人入口）：不经 /api/v1 鉴权组，自己按设备令牌
// 鉴权（HTTP Basic 的密码位或 Bearer），从不读 Cookie。先鉴权再查项目：项目是否存在不向匿名者泄露。
// smart-HTTP 的错误是 text/plain（git 会把正文显示给用户）；info/lfs/ 端点的错误是 LFS 的 JSON 错误体（git-lfs 显示 message）。
func (s *Server) GitHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := gitPathRe.FindStringSubmatch(r.URL.Path)
		if r.URL.RawPath != "" || m == nil {
			http.Error(w, "oryn: 仓库不存在", http.StatusNotFound)
			return
		}
		isLFS := strings.HasPrefix(m[2], "info/lfs/")
		if _, ok := auth.GitCredential(r.Context(), s.Store, r); !ok {
			// 来自浏览器（带 Sec-Fetch-Site）时不回 WWW-Authenticate，免得弹出原生口令框
			if r.Header.Get("Sec-Fetch-Site") == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="Oryn"`)
				if isLFS {
					// git-lfs 据此经 git credential fill 向 oryn git-credential 要凭据（authentication.md）
					w.Header().Set("LFS-Authenticate", `Basic realm="Oryn"`)
				}
			}
			gitError(w, isLFS, http.StatusUnauthorized, gitNeedToken)
			return
		}
		if s.Repos == nil || s.Repos.Service == nil {
			gitError(w, isLFS, http.StatusServiceUnavailable, "oryn: 控制面没有可用的 git")
			return
		}
		s.Repos.Service.ServeHostedGit(w, r, m[1], m[2], egress.HostedPusher{})
	})
}

// gitError 按端点写错误：LFS 端点用 JSON 错误体，其余 text/plain。
func gitError(w http.ResponseWriter, isLFS bool, status int, msg string) {
	if isLFS {
		lfs.WriteError(w, status, msg)
		return
	}
	http.Error(w, msg, status)
}
