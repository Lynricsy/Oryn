package egress

import (
	"log/slog"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/lfs"
)

// 托管仓库（M24 阶段 ③）：orb 所属项目的托管仓库由控制面在本地服务，不转发上游、不注入令牌。

// HostedGitRoute 是 orb 所属项目的托管仓库：git 主机等于 Host 的请求只放行 Repo，在本地服务，不转发、不注入令牌。
type HostedGitRoute struct {
	Host      string // public_url 的 host[:port]，小写
	Repo      string // NormalizeRepo 形式，如 "git/prj_01jx…"（public_url 带路径前缀时含前缀）
	ProjectID string
}

// HostedPusher 是托管仓库 git 请求的来源：OrbID 为空 = 主人本机（/git/ 入口）；否则是经出站代理的 orb，
// ThreadID 是它唯一的未归档工作线程（没有时为空）。
type HostedPusher struct{ OrbID, ThreadID string }

// HostedGitServer 在本地服务托管仓库（*repos.Service 实现）。endpoint 是相对 <repo>.git/ 的端点（info/refs、
// git-upload-pack、git-receive-pack、info/lfs/…）。
type HostedGitServer interface {
	ServeHostedGit(w http.ResponseWriter, r *http.Request, projectID, endpoint string, pusher HostedPusher)
}

// HostedGitLookup 是 Resolver 的托管仓库通道；srv 为 nil = 没有通道（请求回 403），err 非 nil 回 503。
// 它是 Resolver 的必需部分而不是可选能力：这条通道决定放行，缺了要失败关闭，不能静默跳过。
type HostedGitLookup interface {
	HostedGit() (srv HostedGitServer, pusher HostedPusher, err error)
}

// NoHostedGit 是没有托管仓库通道的 Resolver 用的 HostedGitLookup（托管路由一律 403）。
type NoHostedGit struct{}

func (NoHostedGit) HostedGit() (HostedGitServer, HostedPusher, error) {
	return nil, HostedPusher{}, nil
}

// denyHostedOther 是托管仓库主机上访问别的仓库时的拒绝原因。
const denyHostedOther = "这个 orb 只能访问本项目的托管仓库"

// serveHosted 把 resolve 放行的托管仓库请求交给 Resolver.HostedGit 在本地服务：不做占位符替换、不转发，入站的
// Authorization 去掉（orb 不持有也不需要任何凭据）。上游推送记录（PushRecorder）不涉及，托管推送由服务端自己记账。
func (h *Handler) serveHosted(w http.ResponseWriter, r *http.Request, t target) {
	fail := func(status int, msg string) {
		if t.lfs {
			lfs.WriteError(w, status, msg)
			return
		}
		http.Error(w, msg, status)
	}
	srv, pusher, err := h.R.HostedGit()
	if err != nil {
		slog.Warn("托管仓库请求：读取 orb 的线程失败", "orb", h.OrbID, "err", err)
		fail(http.StatusServiceUnavailable, "oryn egress: 读取 orb 的线程失败")
		return
	}
	if srv == nil {
		fail(http.StatusForbidden, "oryn egress: 未知或不允许的路由")
		return
	}
	r.Header.Del("Authorization")
	r.Header.Del(MCPTokenHeader)
	srv.ServeHostedGit(w, r, t.hostedProject, t.hostedEndpoint, pusher)
}
