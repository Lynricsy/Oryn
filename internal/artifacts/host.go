package artifacts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// HostConfig 是帧服务的配置。
type HostConfig struct {
	// Base 由 public_url 与 portal_domain 推出；配置了 portal_domain 时帧在 `a-<令牌>.<portal_domain>`，
	// 否则在主域的 /_af/<令牌>/。
	Base portalhost.Base
	// Now 为空时用 time.Now。
	Now func() time.Time
	// Asset 读嵌入的 Web 构建产物；为空时用 webui.Asset。
	Asset func(name string) ([]byte, bool)
}

// Host 在独立源里提供 artifact 的渲染帧：内存里的帧表、帧根下的文件服务与按主机名分流的中间件。
type Host struct {
	s         *Service
	base      portalhost.Base
	now       func() time.Time
	runtimeJS []byte

	mu     sync.Mutex
	frames map[string]*frame
}

// NewHost 创建帧服务。页面运行时在构造时读一次（嵌入的构建产物在进程内不变）。
func NewHost(s *Service, cfg HostConfig) *Host {
	h := &Host{s: s, base: cfg.Base, now: cfg.Now, frames: map[string]*frame{}}
	if h.now == nil {
		h.now = time.Now
	}
	asset := cfg.Asset
	if asset == nil {
		asset = webui.Asset
	}
	if js, ok := asset(runtimeAsset); ok {
		h.runtimeJS = inlineScript(js)
	}
	return h
}

// Handler 服务主域回退路径 `/_af/<令牌>/…`（挂在 FramePathPrefix 下；帧请求不带 cookie，令牌就是凭证）。
func (h *Host) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, FramePathPrefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		token, p, hasSlash := strings.Cut(rest, "/")
		if !hasSlash {
			// 帧根必须以 / 结尾，页面里的相对路径才落在帧根之下
			http.Redirect(w, r, FramePathPrefix+token+"/", http.StatusFound)
			return
		}
		h.serve(w, r, token, "/"+p)
	})
}

// Middleware 把 `a-<令牌>.<portal_domain>` 主机的请求交给帧服务，其余交给 next（portal 的 `t-…` 主机照旧）。
func (h *Host) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if label, ok := h.base.Match(r.Host); ok {
			if kind, token, ok := portalhost.ParseArtifactLabel(label); ok && kind == 'a' {
				h.serve(w, r, token, r.URL.Path)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// serve 处理帧根下的一个请求；p 是帧根之内的路径（以 / 开头）。
func (h *Host) serve(w http.ResponseWriter, r *http.Request, token, p string) {
	ctx := r.Context()
	f, ok := h.use(token)
	if !ok {
		frameError(w, http.StatusNotFound, "帧不存在或已过期")
		return
	}
	sandbox := sandboxFrame
	if f.topLevel {
		sandbox = sandboxTopLevel
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		setFrameHeaders(w, r, framePolicy(sandbox, h.frameSelf(token), f.ancestors, true))
		w.Header().Set("Allow", "GET, HEAD")
		frameError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	strict, err := h.strict(ctx, token, f)
	if err != nil {
		setFrameHeaders(w, r, framePolicy(sandbox, h.frameSelf(token), f.ancestors, true))
		h.fail(w, err)
		return
	}
	setFrameHeaders(w, r, framePolicy(sandbox, h.frameSelf(token), f.ancestors, strict))
	path, version, ok := framePath(p, f.version)
	if !ok {
		frameError(w, http.StatusNotFound, "文件不存在")
		return
	}
	rc, file, err := h.s.Open(ctx, f.artifactID, version, path)
	if err != nil {
		h.fail(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", file.Mime)
	if !IsHTML(path) {
		http.ServeContent(w, r, "", time.Time{}, rc)
		return
	}
	src, err := io.ReadAll(rc)
	if err != nil {
		h.fail(w, err)
		return
	}
	if !f.topLevel {
		src = servedHTML(src, path, f.runtime, h.runtimeJS)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(src)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(src)
	}
}

// strict 判定帧这次响应是否用严格策略：最新版本声明了 files 或 sample 能力，或帧之前已经严格。只升不降。
// artifact 不存在回 not_found，在回收站里回 deleted。
func (h *Host) strict(ctx context.Context, token string, f frame) (bool, error) {
	a, err := h.s.Get(ctx, f.artifactID)
	if err != nil {
		return true, err
	}
	if a.Deleted() {
		return true, deletedErr(a.ID)
	}
	head, err := h.s.Version(ctx, a.ID, 0)
	if err != nil {
		return true, err
	}
	declared := false
	for _, c := range head.Capabilities {
		if c == "files" || c == "sample" {
			declared = true
		}
	}
	return h.escalate(token, declared || f.strict), nil
}

// framePath 把帧根之内的路径解析成 artifact 里的文件路径与版本：以 / 结尾补 index.html；
// `/_oryn/v/<n>/<路径>` 取版本 n，否则取帧绑定的版本（0 = 最新）。其它 `/_oryn/…` 与不合法的路径 ok 为假。
func framePath(p string, bound int) (path string, version int, ok bool) {
	rel := strings.TrimPrefix(p, "/")
	version = bound
	if pinned, isPinned := strings.CutPrefix(rel, ReservedPrefix+"/v/"); isPinned {
		n, rest, found := strings.Cut(pinned, "/")
		v, err := strconv.Atoi(n)
		if !found || err != nil || v < 1 || strconv.Itoa(v) != n {
			return "", 0, false
		}
		rel, version = rest, v
	}
	if rel == "" || strings.HasSuffix(rel, "/") {
		rel += EntryHTML
	}
	if PathProblem(rel) != "" {
		return "", 0, false
	}
	return rel, version, true
}

// fail 把错误写成帧响应：not_found 404、deleted 410，其余 500（记日志）。
func (h *Host) fail(w http.ResponseWriter, err error) {
	if oe, ok := AsOpError(err); ok {
		switch oe.Code {
		case CodeNotFound:
			frameError(w, http.StatusNotFound, "文件不存在")
			return
		case CodeDeleted:
			frameError(w, http.StatusGone, "这个 artifact 已被删除")
			return
		}
	}
	if !errors.Is(err, context.Canceled) {
		slog.Warn("artifacts: 帧请求失败", "err", err)
	}
	frameError(w, http.StatusInternalServerError, "暂时无法提供这个文件")
}

func frameError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg+"\n")
}
