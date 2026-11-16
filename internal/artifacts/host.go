package artifacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	asset     func(name string) ([]byte, bool)
	runtimeJS []byte
	// caps 是页面运行时端点认得的能力（runtime.go）。
	caps map[string]runtimeCap

	mu     sync.Mutex
	frames map[string]*frame
}

// NewHost 创建帧服务。页面运行时在构造时读一次（嵌入的构建产物在进程内不变）。
func NewHost(s *Service, cfg HostConfig) *Host {
	h := &Host{s: s, base: cfg.Base, now: cfg.Now, frames: map[string]*frame{}}
	if h.now == nil {
		h.now = time.Now
	}
	h.asset = cfg.Asset
	if h.asset == nil {
		h.asset = webui.Asset
	}
	if js, ok := h.asset(runtimeAsset); ok {
		h.runtimeJS = inlineScript(js)
	}
	h.caps = h.runtimeCaps()
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
		h.serve(w, r, token, FramePathPrefix+token+"/", "/"+p)
	})
}

// Middleware 把 `a-<令牌>.<portal_domain>` 主机的请求交给帧服务，其余交给 next（portal 的 `t-…` 主机照旧）。
func (h *Host) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if label, ok := h.base.Match(r.Host); ok {
			if kind, token, ok := portalhost.ParseArtifactLabel(label); ok && kind == 'a' {
				h.serve(w, r, token, "/", r.URL.Path)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// serve 处理帧根下的一个请求；root 是帧根的路径（以 / 结尾），p 是帧根之内的路径（以 / 开头）。
//
// 跟随最新版本的帧：不带 `/_oryn/v/<n>/` 的请求一律 302 到 `<root>_oryn/v/<当前 head>/<p>`，让页面的 URL 先钉在
// 这一次实际取到的版本上——之后页面里的相对子资源都解析到同一版本，加载途中 head 变了也不会混用两版文件。
// 热更新由查看器把帧重新导航到帧根完成，那时再落到新的 head。
func (h *Host) serve(w http.ResponseWriter, r *http.Request, token, root, p string) {
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
	rel := strings.TrimPrefix(p, "/")
	if rel == runtimeEndpoint {
		h.serveRuntime(w, r, token, f)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		setFrameHeaders(w, r, framePolicy(sandbox, h.frameSelf(token), f.ancestors, true))
		w.Header().Set("Allow", "GET, HEAD")
		frameError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	strict, a, head, err := h.strict(ctx, token, f)
	if err != nil {
		setFrameHeaders(w, r, framePolicy(sandbox, h.frameSelf(token), f.ancestors, true))
		h.fail(w, err)
		return
	}
	setFrameHeaders(w, r, framePolicy(sandbox, h.frameSelf(token), f.ancestors, strict))
	if name, ok := strings.CutPrefix(rel, viewerPrefix); ok {
		h.serveViewerAsset(w, r, name)
		return
	}
	if f.version == 0 && !strings.HasPrefix(rel, ReservedPrefix+"/") {
		// p 是已解码的路径：经 url.URL 重新编码，文件名里的 #、%、? 与空格才不会变成片段、查询或别的转义
		loc := (&url.URL{Path: root + ReservedPrefix + "/v/" + strconv.Itoa(head) + "/" + rel, RawQuery: r.URL.RawQuery}).String()
		http.Redirect(w, r, loc, http.StatusFound)
		return
	}
	path, version, ok := framePath(p, f.version)
	if !ok {
		frameError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if a.Type != "" && atFrameRoot(p) {
		// 类型化 artifact 的根是查看器页；入口文件与 images/ 由查看器按页面地址相对读取，落在同一版本
		h.serveViewer(w, r, f, a, root, version, head)
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
		src = servedHTML(src, path, root, f.runtime, h.runtimeJS)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(src)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(src)
	}
}

// strict 判定帧这次响应是否用严格策略：最新版本声明了 files 或 sample 能力，或帧之前已经严格。只升不降。
// 同时返回 artifact 与当前 head。artifact 不存在回 not_found，在回收站里回 deleted。
func (h *Host) strict(ctx context.Context, token string, f frame) (bool, Artifact, int, error) {
	a, err := h.s.Get(ctx, f.artifactID)
	if err != nil {
		return true, Artifact{}, 0, err
	}
	if a.Deleted() {
		return true, Artifact{}, 0, deletedErr(a.ID)
	}
	head, err := h.s.Version(ctx, a.ID, 0)
	if err != nil {
		return true, Artifact{}, 0, err
	}
	declared := false
	for _, c := range head.Capabilities {
		if c == "files" || c == "sample" {
			declared = true
		}
	}
	return h.escalate(token, declared || f.strict), a, head.Version, nil
}

// viewerPrefix 是帧根之内查看器资源的路径前缀。
const viewerPrefix = ReservedPrefix + "/viewer/"

// atFrameRoot 报告帧根之内的路径 p 是不是（某个版本的）根：`/` 或 `/_oryn/v/<n>/`。
func atFrameRoot(p string) bool {
	rel := strings.TrimPrefix(p, "/")
	if pinned, ok := strings.CutPrefix(rel, ReservedPrefix+"/v/"); ok {
		_, rel, _ = strings.Cut(pinned, "/")
	}
	return rel == ""
}

// serveViewer 提供类型化 artifact 的查看器页（版本 version，当前 head）。非顶层帧注入页面运行时（查看器经它的
// `oryn.use("content")` 读写入口文件）；顶层帧没有运行时，查看器只读显示。
func (h *Host) serveViewer(w http.ResponseWriter, r *http.Request, f frame, a Artifact, root string, version, head int) {
	v, err := h.s.Version(r.Context(), a.ID, version)
	if err != nil {
		h.fail(w, err)
		return
	}
	page := viewerPage(viewerConfig{
		Type: a.Type, Format: v.Format, Version: v.Version, Head: head, Entry: a.Entry(), Locale: f.locale, Root: root,
	})
	if !f.topLevel {
		page = injectRuntime(page, runtimeConfig{V: 1, Path: a.Entry(), Root: root, Runtime: f.runtime}, h.runtimeJS)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(page)
	}
}

// serveViewerAsset 提供查看器的构建产物（只认 viewerAssets 里的名字；没构建 Web 时 404）。
func (h *Host) serveViewerAsset(w http.ResponseWriter, r *http.Request, name string) {
	ct, ok := viewerAssets[name]
	if !ok {
		frameError(w, http.StatusNotFound, "文件不存在")
		return
	}
	b, ok := h.asset("sandbox/viewers/" + name)
	if !ok {
		frameError(w, http.StatusNotFound, "查看器资源不存在（Web 没有构建）")
		return
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
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
