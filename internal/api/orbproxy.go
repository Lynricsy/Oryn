package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// orb 文件 / 改动接口（M3 §3.3）：控制面是薄反向代理，经 orb link 的 fs 流转发到 orbd 本机 API。
// orb 一律视为不可信：不转发 Cookie / Authorization，只放行白名单内的响应头，文件字节一律以附件返回。

const (
	// MaxFileBytes 是 Files 单文件写入上限（orbd 侧再校验一次）。
	MaxFileBytes = 512 << 20
	// maxOrbJSONBody 是转发给 orbd 的 JSON 请求体上限（mkdir / rename / delete / stage / unstage）。
	maxOrbJSONBody = 1 << 20
	// orbWakeWait 是非后台请求等待 orb 唤醒的时长。
	orbWakeWait = 25 * time.Second
)

// orbProxyOpt 是单个代理路由的选项。
type orbProxyOpt struct {
	// bg 为真：后台轮询，不计用户交互、不唤醒，orb 不可用即 409 orb_not_ready。
	bg bool
	// raw：响应是文件字节（GET fs/file），强制附件下载与沙箱头；否则响应是 JSON。
	raw bool
	// maxBody > 0 时限制请求体字节数（超限 413 too_large）。
	maxBody int64
	// injectBase：query 无 base 时以 orb 所属项目的 base_branch 补上（git/status|diff|commits）。
	injectBase bool
	// filename 是 raw 响应的下载文件名。
	filename string
}

// proxyResponseHeaders 是从 orbd 响应中放行的头（其余一律丢弃，含 Set-Cookie）。
var proxyResponseHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Etag", "Last-Modified"}

// proxyOrb 把请求转发到 orb 的 orbd 本机 API 的 upstream 路径（如 `/fs/list`）。
func (s *Server) proxyOrb(w http.ResponseWriter, r *http.Request, orbID, upstream string, opt orbProxyOpt) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	ctx := r.Context()
	feature := strings.SplitN(strings.TrimPrefix(upstream, "/"), "/", 2)[0]
	if err := svc.EnsureUsable(ctx, orbID, feature, !opt.bg, orbWakeWait); err != nil {
		writeOrbHTTPErr(w, err, opt.bg)
		return
	}
	q := r.URL.Query()
	q.Del("bg")
	if opt.injectBase && q.Get("base") == "" {
		q.Del("base")
		if base := s.orbBaseBranch(ctx, orbID); base != "" {
			q.Set("base", base)
		}
	}
	if opt.maxBody > 0 {
		if r.ContentLength > opt.maxBody {
			WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "请求体超过上限 "+strconv.FormatInt(opt.maxBody, 10)+" 字节")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, opt.maxBody)
	}
	rawQuery := q.Encode()
	rp := &httputil.ReverseProxy{
		Transport: svc.Transport(),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = &url.URL{Scheme: "http", Host: orbID, Path: upstream, RawQuery: rawQuery}
			pr.Out.Host = orbID
			h := pr.Out.Header
			for _, k := range []string{"Cookie", "Authorization", "X-Oryn-Csrf", "Forwarded", "X-Real-Ip", "Origin", "Referer"} {
				h.Del(k)
			}
			for k := range h {
				if strings.HasPrefix(k, "X-Forwarded-") {
					h.Del(k)
				}
			}
		},
		ModifyResponse: func(res *http.Response) error {
			hdr := make(http.Header, len(proxyResponseHeaders)+5)
			for _, k := range proxyResponseHeaders {
				if v := res.Header.Values(k); len(v) > 0 {
					hdr[k] = v
				}
			}
			hdr.Set("X-Content-Type-Options", "nosniff")
			hdr.Set("Cache-Control", "no-store")
			ok := res.StatusCode >= 200 && res.StatusCode < 300
			if opt.raw && ok {
				hdr.Set("Content-Type", "application/octet-stream")
				hdr.Set("Content-Disposition", attachmentDisposition(opt.filename))
				hdr.Set("Content-Security-Policy", "sandbox")
			} else if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotModified {
				hdr.Set("Content-Type", "application/json")
			}
			res.Header = hdr
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var mbe *http.MaxBytesError
			switch {
			case errors.As(err, &mbe):
				WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "请求体超过上限 "+strconv.FormatInt(mbe.Limit, 10)+" 字节")
			case errors.Is(err, orbs.ErrOrbNotReady), errors.Is(err, orbs.ErrOrbdOutdated), errors.Is(err, orbs.ErrOrbGone):
				writeOrbHTTPErr(w, err, opt.bg)
			case r.Context().Err() != nil:
				// 客户端已离开：无人接收响应
			default:
				slog.Debug("orb 代理失败", "orb", orbID, "path", upstream, "err", err)
				WriteError(w, http.StatusBadGateway, "orbd_unreachable", "无法连接 orbd: "+err.Error())
			}
		},
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	rp.ServeHTTP(w, r)
}

// orbBaseBranch 返回 orb 所属项目的基线分支（无项目或未配置时为空）。
func (s *Server) orbBaseBranch(ctx context.Context, orbID string) string {
	o, err := s.Store.Q.GetOrb(ctx, orbID)
	if err != nil || o.ProjectID == nil {
		return ""
	}
	p, err := s.Store.Q.GetProject(ctx, *o.ProjectID)
	if err != nil || p.BaseBranch == nil {
		return ""
	}
	return strings.TrimSpace(*p.BaseBranch)
}

// attachmentDisposition 构造 RFC 6266 的附件头，文件名按 RFC 5987 以 UTF-8 百分号编码放进 filename*。
func attachmentDisposition(name string) string {
	if name == "" || name == "." || name == "/" {
		name = "download"
	}
	const attrChar = "!#$&+-.^_`|~"
	var b strings.Builder
	b.WriteString("attachment; filename*=UTF-8''")
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(attrChar, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}

func bgFlag(p *apigen.OrbBackgroundQuery) bool { return p != nil && *p }

// ---- fs ----

// GetOrbFsRoots 实现 GET /api/v1/orbs/{id}/fs/roots。
func (s *Server) GetOrbFsRoots(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.GetOrbFsRootsParams) {
	s.proxyOrb(w, r, id, "/fs/roots", orbProxyOpt{bg: bgFlag(params.Bg)})
}

// ListOrbFs 实现 GET /api/v1/orbs/{id}/fs/list。
func (s *Server) ListOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.ListOrbFsParams) {
	s.proxyOrb(w, r, id, "/fs/list", orbProxyOpt{bg: bgFlag(params.Bg)})
}

// StatOrbFs 实现 GET /api/v1/orbs/{id}/fs/stat。
func (s *Server) StatOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.StatOrbFsParams) {
	s.proxyOrb(w, r, id, "/fs/stat", orbProxyOpt{bg: bgFlag(params.Bg)})
}

// ReadOrbFile 实现 GET /api/v1/orbs/{id}/fs/file（支持 Range）。
func (s *Server) ReadOrbFile(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.ReadOrbFileParams) {
	s.proxyOrb(w, r, id, "/fs/file", orbProxyOpt{raw: true, filename: path.Base(params.Path)})
}

// WriteOrbFile 实现 PUT /api/v1/orbs/{id}/fs/file（流式上传，≤512MiB）。
func (s *Server) WriteOrbFile(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, _ apigen.WriteOrbFileParams) {
	s.proxyOrb(w, r, id, "/fs/file", orbProxyOpt{maxBody: MaxFileBytes})
}

// MkdirOrbFs 实现 POST /api/v1/orbs/{id}/fs/mkdir。
func (s *Server) MkdirOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	s.proxyOrb(w, r, id, "/fs/mkdir", orbProxyOpt{maxBody: maxOrbJSONBody})
}

// RenameOrbFs 实现 POST /api/v1/orbs/{id}/fs/rename。
func (s *Server) RenameOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	s.proxyOrb(w, r, id, "/fs/rename", orbProxyOpt{maxBody: maxOrbJSONBody})
}

// DeleteOrbFs 实现 POST /api/v1/orbs/{id}/fs/delete。
func (s *Server) DeleteOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	s.proxyOrb(w, r, id, "/fs/delete", orbProxyOpt{maxBody: maxOrbJSONBody})
}

// ---- git ----

// GetOrbGitStatus 实现 GET /api/v1/orbs/{id}/git/status。
func (s *Server) GetOrbGitStatus(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.GetOrbGitStatusParams) {
	s.proxyOrb(w, r, id, "/git/status", orbProxyOpt{bg: bgFlag(params.Bg), injectBase: true})
}

// GetOrbGitDiff 实现 GET /api/v1/orbs/{id}/git/diff。
func (s *Server) GetOrbGitDiff(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.GetOrbGitDiffParams) {
	s.proxyOrb(w, r, id, "/git/diff", orbProxyOpt{bg: bgFlag(params.Bg), injectBase: true})
}

// ListOrbGitCommits 实现 GET /api/v1/orbs/{id}/git/commits。
func (s *Server) ListOrbGitCommits(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.ListOrbGitCommitsParams) {
	s.proxyOrb(w, r, id, "/git/commits", orbProxyOpt{bg: bgFlag(params.Bg), injectBase: true})
}

// StageOrbGit 实现 POST /api/v1/orbs/{id}/git/stage。
func (s *Server) StageOrbGit(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	s.proxyOrb(w, r, id, "/git/stage", orbProxyOpt{maxBody: maxOrbJSONBody})
}

// UnstageOrbGit 实现 POST /api/v1/orbs/{id}/git/unstage。
func (s *Server) UnstageOrbGit(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	s.proxyOrb(w, r, id, "/git/unstage", orbProxyOpt{maxBody: maxOrbJSONBody})
}
