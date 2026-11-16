package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// orb 文件 / 改动接口（M3 §3.3）：控制面是薄反向代理，经 orb link 的 fs 流转发到 orbd 本机 API。
// orb 一律视为不可信：只转发白名单内的请求头，只放行白名单内的状态码与响应头，丢弃 1xx 中间响应，
// JSON 响应体限长，文件字节一律以附件返回，且对 orbd 的每次调用都有截止时间（上传除外，由客户端取消）。

const (
	// MaxFileBytes 是 Files 单文件写入上限（orbd 侧再校验一次）。
	MaxFileBytes = 512 << 20
	// maxOrbJSONBody 是转发给 orbd 的 JSON 请求体上限（mkdir / rename / delete / stage / unstage）。
	maxOrbJSONBody = 1 << 20
	// maxOrbJSONResponse 是 orbd JSON 响应体上限（超出即 502 orbd_bad_response 或中止响应）。
	maxOrbJSONResponse = 32 << 20
	// orbWakeWait 是非后台请求等待 orb 唤醒的时长。
	orbWakeWait = 25 * time.Second
	// orbMutateTimeout 是可能耗时的文件变更（跨文件系统的 rename、大目录 delete）的上限。
	orbMutateTimeout = 5 * time.Minute
)

// 以下截止时间是变量仅为测试可缩短；生产代码不修改。
var (
	// orbCallTimeout 是 JSON 路由一次 orbd 调用（含响应体）的上限；orbd 的 git 调用自身限 20s。
	orbCallTimeout = 45 * time.Second
	// orbReadHeaderTimeout 是 GET fs/file 等待 orbd 响应头的上限；响应头到达后下载不限时（由客户端取消）。
	orbReadHeaderTimeout = 30 * time.Second
)

// orbProxyOpt 是单个代理路由的选项。
type orbProxyOpt struct {
	// bg 为真：后台轮询，不计用户交互、不唤醒，orb 不可用即 409 orb_not_ready。
	bg bool
	// raw：响应是文件字节（GET fs/file），强制附件下载与沙箱头；否则响应是 JSON。
	raw bool
	// upload：请求体是文件上传（PUT fs/file），不设截止时间。
	upload bool
	// timeout 覆盖 JSON 路由的 orbCallTimeout（0 = 缺省）。
	timeout time.Duration
	// maxBody > 0 时限制请求体字节数（超限 413 too_large）。
	maxBody int64
	// injectBase：query 无 base 时以 orb 所属项目的 base_branch 补上（git/status|diff|commits）。
	injectBase bool
	// filename 是 raw 响应的下载文件名。
	filename string
	// text：成功响应是纯文本（服务日志），Content-Type 置 text/plain; charset=utf-8；其余同 JSON 路由。
	text bool
	// workdir 非空时以 X-Oryn-Workdir 转给 orbd（M11b：请求带 thread 且该线程有 cwd；由 proxyOrb 填写）。
	workdir string
}

// proxyRequestHeaders 是转发给 orbd 的请求头（其余一律丢弃：Cookie、Authorization、前置代理加的身份头、
// 浏览器指纹头等都不进 orb）。
var proxyRequestHeaders = []string{"Content-Type", "Content-Length", "Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "Accept"}

// proxyResponseHeaders 是从 orbd 响应中放行的头（其余一律丢弃，含 Set-Cookie）。
var proxyResponseHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Etag", "Last-Modified"}

// proxyStatuses 是 orbd 本机 API 会返回的状态码（docs/protocol.md 错误码表）。其余（1xx / 101、3xx、401 等）
// 一律视为异常响应：401 会让前端登出，101 会让 ReverseProxy 把客户端连接劫持成直通 orb 的裸隧道。
var proxyStatuses = map[int]bool{
	http.StatusOK: true, http.StatusNoContent: true, http.StatusPartialContent: true, http.StatusNotModified: true,
	http.StatusBadRequest: true, http.StatusForbidden: true, http.StatusNotFound: true, http.StatusConflict: true,
	http.StatusPreconditionFailed: true, http.StatusRequestEntityTooLarge: true, http.StatusRequestedRangeNotSatisfiable: true,
	http.StatusUnprocessableEntity: true, http.StatusInternalServerError: true, http.StatusInsufficientStorage: true,
}

// errOrbBadResponse 表示 orbd 的响应不符合本机 API 约定（状态码不在白名单、JSON 响应体超长）。
var errOrbBadResponse = errors.New("orbd returned an invalid response")

// errOrbTimeout 表示 orbd 在截止时间内没有答复。
var errOrbTimeout = errors.New("orbd did not answer in time")

// rawMediaTypes 是 GET fs/file 按真实类型回 Content-Type 的音视频扩展名：文件面板直接把下载地址给 <video> / <audio>
// 流式播放，WebKit 按 Content-Type 选媒体管线，octet-stream 会拒播。浏览器从不把这些类型当文档或脚本执行，
// 且响应仍带 attachment、`Content-Security-Policy: sandbox` 与 nosniff。
var rawMediaTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".ogv": "video/ogg", ".mkv": "video/x-matroska",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
	".flac": "audio/flac", ".m4a": "audio/mp4", ".aac": "audio/aac", ".weba": "audio/webm",
}

// rawContentType 是文件下载响应的 Content-Type：白名单内的音视频按扩展名，其余一律 application/octet-stream。
func rawContentType(filename string) string {
	if t, ok := rawMediaTypes[strings.ToLower(path.Ext(filename))]; ok {
		return t
	}
	return "application/octet-stream"
}

// proxyOrb 把请求转发到 orb 的 orbd 本机 API 的 upstream 路径（如 `/fs/list`）。
// fs / git 路由（M11b）：查询参数 thread 须是该 orb 上的线程，线程有 cwd 时以 X-Oryn-Workdir 转给 orbd；
// runner 的 orb 必须带 thread（否则 400 thread_required）。thread 参数本身不转给 orbd。
func (s *Server) proxyOrb(w http.ResponseWriter, r *http.Request, orbID, upstream string, opt orbProxyOpt) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	clientCtx := r.Context()
	feature := strings.SplitN(strings.TrimPrefix(upstream, "/"), "/", 2)[0]
	q := r.URL.Query()
	threadID := q.Get("thread")
	q.Del("thread")
	if feature == orbs.FeatureFS || feature == orbs.FeatureGit {
		wd, ok := s.threadWorkdir(w, r, orbID, threadID)
		if !ok {
			return
		}
		opt.workdir = wd
	}
	if err := svc.EnsureUsable(clientCtx, orbID, feature, !opt.bg, orbWakeWait); err != nil {
		writeOrbHTTPErr(w, err, opt.bg)
		return
	}
	q.Del("bg")
	q.Del("offline")
	if opt.injectBase && q.Get("base") == "" {
		q.Del("base")
		if base := s.orbBaseBranch(clientCtx, orbID); base != "" {
			q.Set("base", base)
		}
	}
	serveOrbProxy(w, r, svc.Transport(), orbID, upstream, q.Encode(), opt)
}

// threadWorkdir 校验 fs / git 请求的 thread 参数并返回要转给 orbd 的工作目录（空 = 不带 X-Oryn-Workdir）。
// orb 不存在时交给 EnsureUsable 报 404。失败时已写出响应、返回 false。
func (s *Server) threadWorkdir(w http.ResponseWriter, r *http.Request, orbID, threadID string) (string, bool) {
	ctx := r.Context()
	if threadID == "" {
		if o, err := s.Store.Q.GetOrb(ctx, orbID); err == nil && orbs.IsRunner(o) {
			WriteError(w, http.StatusBadRequest, "thread_required", "runner 的文件与改动请求须带 thread")
			return "", false
		}
		return "", true
	}
	t, err := s.Store.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (t.OrbID == nil || *t.OrbID != orbID)) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "thread 不是该 orb 上的线程")
		return "", false
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return "", false
	}
	if t.Cwd == nil {
		return "", true
	}
	return *t.Cwd, true
}

// serveOrbProxy 经 rt（orb 的 fs 流 Transport）把请求反向代理到 orbd 的 upstream 路径，套上请求 / 响应白名单、
// 状态码白名单、JSON 响应体上限与截止时间。调用方已确认 orb 可用。
func serveOrbProxy(w http.ResponseWriter, r *http.Request, rt http.RoundTripper, orbID, upstream, rawQuery string, opt orbProxyOpt) {
	clientCtx := r.Context()
	if opt.maxBody > 0 {
		if r.ContentLength > opt.maxBody {
			WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "请求体超过上限 "+strconv.FormatInt(opt.maxBody, 10)+" 字节")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, opt.maxBody)
	}
	// 截止时间：不应答的 orbd 不得无限占住请求（以及其后的浏览器连接 / HTTP/2 流）。
	var timedOut atomic.Bool
	stopTimer := func() {}
	switch {
	case opt.upload:
		// 512MiB 上传在请求体传完后才有响应头：由客户端取消
	case opt.raw:
		ctx, cancel := context.WithCancel(clientCtx)
		defer cancel()
		t := time.AfterFunc(orbReadHeaderTimeout, func() { timedOut.Store(true); cancel() })
		stopTimer = func() { t.Stop() }
		r = r.WithContext(ctx)
	default:
		d := opt.timeout
		if d == 0 {
			d = orbCallTimeout
		}
		ctx, cancel := context.WithTimeout(clientCtx, d)
		defer cancel()
		r = r.WithContext(ctx)
	}
	rp := &httputil.ReverseProxy{
		Transport: noTraceTransport{rt},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = &url.URL{Scheme: "http", Host: orbID, Path: upstream, RawQuery: rawQuery}
			pr.Out.Host = orbID
			h := make(http.Header, len(proxyRequestHeaders))
			for _, k := range proxyRequestHeaders {
				if v := pr.In.Header.Values(k); len(v) > 0 {
					h[k] = v
				}
			}
			pr.Out.Header = h
			orbs.SetWorkdir(h, &opt.workdir)
		},
		ModifyResponse: func(res *http.Response) error {
			// 响应头已到：GET fs/file 的下载不再限时
			stopTimer()
			if !proxyStatuses[res.StatusCode] || (!opt.raw && res.ContentLength > maxOrbJSONResponse) {
				_ = res.Body.Close()
				return fmt.Errorf("%w: status %d, %d bytes", errOrbBadResponse, res.StatusCode, res.ContentLength)
			}
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
				hdr.Set("Content-Type", rawContentType(opt.filename))
				hdr.Set("Content-Disposition", attachments.Disposition(opt.filename))
				hdr.Set("Content-Security-Policy", "sandbox")
			} else if opt.text && ok {
				hdr.Set("Content-Type", "text/plain; charset=utf-8")
			} else if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotModified {
				hdr.Set("Content-Type", "application/json")
			}
			if !opt.raw || !ok {
				// 未声明长度的 JSON / 错误体同样限长：超出即中止响应
				res.Body = &cappedBody{ReadCloser: res.Body, left: maxOrbJSONResponse}
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
			case errors.Is(err, errOrbBadResponse):
				slog.Warn("orbd 响应异常", "orb", orbID, "path", upstream, "err", err)
				WriteError(w, http.StatusBadGateway, "orbd_bad_response", "orbd 返回了不符合约定的响应")
			case clientCtx.Err() != nil:
				// 客户端已离开：无人接收响应
			case timedOut.Load() || errors.Is(r.Context().Err(), context.DeadlineExceeded):
				slog.Warn("orbd 未按时应答", "orb", orbID, "path", upstream)
				WriteError(w, http.StatusGatewayTimeout, "orbd_timeout", errOrbTimeout.Error())
			default:
				slog.Debug("orb 代理失败", "orb", orbID, "path", upstream, "err", err)
				WriteError(w, http.StatusBadGateway, "orbd_unreachable", "无法连接 orbd: "+err.Error())
			}
		},
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	rp.ServeHTTP(w, r)
}

// noTraceTransport 去掉请求上下文里的 httptrace.ClientTrace 再交给内层 Transport。ReverseProxy 借
// Got1xxResponse 钩子把 1xx 中间响应的全部头原样写给客户端（绕过响应头白名单）；没有钩子时 http.Transport
// 自行读掉并丢弃 1xx。
type noTraceTransport struct{ rt http.RoundTripper }

func (t noTraceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.rt.RoundTrip(req.WithContext(noTraceCtx{req.Context()}))
}

// noTraceCtx 对 httptrace.ClientTrace 类型的上下文值返回 nil，其余（取消、截止时间、其它值）照旧。
type noTraceCtx struct{ context.Context }

func (c noTraceCtx) Value(key any) any {
	v := c.Context.Value(key)
	if _, ok := v.(*httptrace.ClientTrace); ok {
		return nil
	}
	return v
}

// cappedBody 在读出超过 left 字节时返回错误（ReverseProxy 随即中止响应）。
type cappedBody struct {
	io.ReadCloser
	left int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, errOrbBadResponse
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return 0, fmt.Errorf("%w: JSON body exceeds %d bytes", errOrbBadResponse, maxOrbJSONResponse)
	}
	return n, err
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

func bgFlag(p *apigen.OrbBackgroundQuery) bool { return p != nil && *p }

// ---- fs ----

// GetOrbFsRoots 实现 GET /api/v1/orbs/{id}/fs/roots。
func (s *Server) GetOrbFsRoots(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.GetOrbFsRootsParams) {
	if offlineFlag(params.Offline) && s.serveOfflineFs(w, r, id, apigen.Roots, "") {
		return
	}
	s.proxyOrb(w, r, id, "/fs/roots", orbProxyOpt{bg: bgFlag(params.Bg)})
}

// ListOrbFs 实现 GET /api/v1/orbs/{id}/fs/list。
func (s *Server) ListOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.ListOrbFsParams) {
	if offlineFlag(params.Offline) && s.serveOfflineFs(w, r, id, apigen.List, params.Path) {
		return
	}
	s.proxyOrb(w, r, id, "/fs/list", orbProxyOpt{bg: bgFlag(params.Bg)})
}

// StatOrbFs 实现 GET /api/v1/orbs/{id}/fs/stat。
func (s *Server) StatOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.StatOrbFsParams) {
	if offlineFlag(params.Offline) && s.serveOfflineFs(w, r, id, apigen.Stat, params.Path) {
		return
	}
	s.proxyOrb(w, r, id, "/fs/stat", orbProxyOpt{bg: bgFlag(params.Bg)})
}

// ReadOrbFile 实现 GET /api/v1/orbs/{id}/fs/file（支持 Range）。
func (s *Server) ReadOrbFile(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.ReadOrbFileParams) {
	if offlineFlag(params.Offline) && s.serveOfflineFs(w, r, id, apigen.Read, params.Path) {
		return
	}
	s.proxyOrb(w, r, id, "/fs/file", orbProxyOpt{raw: true, filename: path.Base(params.Path)})
}

// WriteOrbFile 实现 PUT /api/v1/orbs/{id}/fs/file（流式上传，≤512MiB）。
func (s *Server) WriteOrbFile(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, _ apigen.WriteOrbFileParams) {
	s.proxyOrb(w, r, id, "/fs/file", orbProxyOpt{maxBody: MaxFileBytes, upload: true})
}

// MkdirOrbFs 实现 POST /api/v1/orbs/{id}/fs/mkdir。
func (s *Server) MkdirOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, _ apigen.MkdirOrbFsParams) {
	s.proxyOrb(w, r, id, "/fs/mkdir", orbProxyOpt{maxBody: maxOrbJSONBody})
}

// RenameOrbFs 实现 POST /api/v1/orbs/{id}/fs/rename。
func (s *Server) RenameOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, _ apigen.RenameOrbFsParams) {
	s.proxyOrb(w, r, id, "/fs/rename", orbProxyOpt{maxBody: maxOrbJSONBody, timeout: orbMutateTimeout})
}

// DeleteOrbFs 实现 POST /api/v1/orbs/{id}/fs/delete。
func (s *Server) DeleteOrbFs(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, _ apigen.DeleteOrbFsParams) {
	s.proxyOrb(w, r, id, "/fs/delete", orbProxyOpt{maxBody: maxOrbJSONBody, timeout: orbMutateTimeout})
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
func (s *Server) StageOrbGit(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, _ apigen.StageOrbGitParams) {
	s.proxyOrb(w, r, id, "/git/stage", orbProxyOpt{maxBody: maxOrbJSONBody})
}

// UnstageOrbGit 实现 POST /api/v1/orbs/{id}/git/unstage。
func (s *Server) UnstageOrbGit(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, _ apigen.UnstageOrbGitParams) {
	s.proxyOrb(w, r, id, "/git/unstage", orbProxyOpt{maxBody: maxOrbJSONBody})
}

// ---- sync（特性名即上游路径首段 orbs.FeatureSync：旧版 orbd → 409 orbd_outdated）----

// orbSyncTimeout 是 sync/prepare 的上限（orbd 侧单次 git 调用限 5 分钟）。
const orbSyncTimeout = 6 * time.Minute

// PrepareOrbSync 实现 POST /api/v1/orbs/{id}/sync/prepare。
func (s *Server) PrepareOrbSync(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, params apigen.PrepareOrbSyncParams) {
	s.proxyOrb(w, r, id, "/sync/prepare", orbProxyOpt{bg: bgFlag(params.Bg), maxBody: maxOrbJSONBody, timeout: orbSyncTimeout})
}

// GetOrbSyncBundle 实现 GET /api/v1/orbs/{id}/sync/bundle（原始字节，以附件返回）。
func (s *Server) GetOrbSyncBundle(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath, _ apigen.GetOrbSyncBundleParams) {
	s.proxyOrb(w, r, id, "/sync/bundle", orbProxyOpt{raw: true, filename: "oryn-sync.bundle"})
}
