package artifacts

import (
	"bytes"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/artifacts/htmlsrc"
	"github.com/Lynricsy/Oryn/internal/portalhost"
)

// beaconAsset 是访客心跳脚本在嵌入的 Web 构建产物里的路径。
const beaconAsset = "sandbox/beacon.js"

const (
	// VisitorWindow 内发过心跳的页面实例算一位访客。
	VisitorWindow = 45 * time.Second
	// maxVisitorsPerSlug 是每个发布页在内存里最多记多少个页面实例（防止伪造的心跳撑爆内存）。
	maxVisitorsPerSlug = 10000
	// beatMaxBytes 是心跳请求体的上限。
	beatMaxBytes = 128
)

// 发布根之内的保留路径。
const (
	unlockPath = ReservedPrefix + "/unlock"
	beatPath   = ReservedPrefix + "/beat"
)

// beatIDRe 是心跳里页面实例 id 的形状（心跳脚本生成 24 位十六进制）。
var beatIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// PublicHandler 服务主域回退路径 `/p/<slug>/…`（挂在 PublishPathPrefix 下；访客不带登录态，
// 访问控制由发布记录的访问级别与授权段决定）。
func (h *Host) PublicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, PublishPathPrefix)
		slug, p, hasSlash := strings.Cut(rest, "/")
		if _, _, valid := portalhost.ParseArtifactLabel("p-" + slug); !ok || !valid {
			writeGonePage(w, r)
			return
		}
		if !hasSlash {
			// 发布根必须以 / 结尾，页面里的相对路径才落在根之下
			http.Redirect(w, r, PublishPathPrefix+slug+"/", http.StatusFound)
			return
		}
		h.servePublic(w, r, slug, PublishPathPrefix+slug+"/", "/"+p)
	})
}

// publicSelf 返回发布页 CSP 里代表「页面自己的文件」的源表达式：泛域名模式 `'self'`，
// 主域回退模式 `<public_url 源>/p/<slug>/`（授权段在它之下，按路径前缀匹配）。
func (h *Host) publicSelf(slug string) string {
	if h.base.Enabled() {
		return "'self'"
	}
	return h.base.Origin + PublishPathPrefix + slug + "/"
}

// servePublic 处理发布根下的一个请求；root 是发布根的路径（以 / 结尾），p 是根之内的路径（以 / 开头）。
//
// URL 有两种形态：`<根><路径>`（只对 public 直接可用）与 `<根>~<授权>/<路径>`（授权段之后照常解析，
// 页面里的相对子资源因此自动带着授权）。code 没有有效授权时任何路径都回访问码页；private 没有有效授权时
// 302 到 SPA 的 `/artifacts/open/<slug>`，由登录后的所有者换一个授权再回来。
//
// 内容取发布记录的版本，HTML 只插 noindex 与访客心跳，不注入页面运行时、不标源码；回收站里的 artifact 照常提供。
func (h *Host) servePublic(w http.ResponseWriter, r *http.Request, slug, root, p string) {
	ctx := r.Context()
	pub, err := h.s.PublicationBySlug(ctx, slug)
	if err != nil {
		if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
			writeGonePage(w, r)
			return
		}
		h.fail(w, err)
		return
	}
	base := root
	granted := false
	if g, ok := strings.CutPrefix(p, "/~"); ok {
		grant, rest, hasSlash := strings.Cut(g, "/")
		if !hasSlash {
			http.Redirect(w, r, (&url.URL{Path: root + "~" + grant + "/"}).String(), http.StatusFound)
			return
		}
		granted = h.s.GrantValid(pub, grant)
		root += "~" + grant + "/"
		p = "/" + rest
	}
	rel := strings.TrimPrefix(p, "/")
	switch pub.Access {
	case AccessCode:
		if rel == unlockPath && !granted {
			h.unlock(w, r, pub, base)
			return
		}
		if !granted {
			state := gateAsk
			if pub.CodeLocked {
				state = gateLocked
			}
			writeCodePage(w, r, base+unlockPath, state)
			return
		}
	case AccessPrivate:
		if !granted {
			http.Redirect(w, r, h.base.Origin+"/artifacts/open/"+slug, http.StatusFound)
			return
		}
	}

	ancestors := ""
	if rel == "" {
		// 根不让别的站点内嵌。根之外的文件可能被根（画布的概览页）内嵌，而根运行在不透明源里，
		// 浏览器对不透明源的祖先任何 frame-ancestors 都不放行，所以不发这条指令
		ancestors = "'self'"
	}
	csp := framePolicy(sandboxTopLevel, h.publicSelf(slug), ancestors, false)
	if rel == beatPath {
		setFrameHeaders(w, r, csp)
		h.beat(w, r, pub)
		return
	}
	setFrameHeaders(w, r, csp)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		frameError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if name, ok := strings.CutPrefix(rel, viewerPrefix); ok {
		h.serveViewerAsset(w, r, name)
		return
	}
	if strings.HasPrefix(rel, ReservedPrefix+"/") {
		// 发布页只提供发布的那个版本：`_oryn/v/<n>/` 与其它保留路径一律不存在
		frameError(w, http.StatusNotFound, "文件不存在")
		return
	}
	path, _, ok := framePath(p, pub.Version)
	if !ok {
		frameError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if rel == "" {
		a, err := h.s.Get(ctx, pub.ArtifactID)
		if err != nil {
			h.fail(w, err)
			return
		}
		page, ok, err := h.publicRoot(r, a, pub.Version, root)
		if err != nil {
			h.fail(w, err)
			return
		}
		if ok {
			writePage(w, r, h.publishedHTML(page, root))
			return
		}
	}
	rc, file, err := h.s.Open(ctx, pub.ArtifactID, pub.Version, path)
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
	src = h.publishedHTML(src, root)
	w.Header().Set("Content-Length", strconv.Itoa(len(src)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(src)
	}
}

// publicRoot 返回发布根上生成的页面：类型化 artifact 是查看器页（没有页面运行时，查看器只读显示），
// 没有自己的 index.html 的画布是概览页；其余 ok 为假（根照常取 index.html）。
func (h *Host) publicRoot(r *http.Request, a Artifact, version int, root string) ([]byte, bool, error) {
	if a.Type != "" {
		v, err := h.s.Version(r.Context(), a.ID, version)
		if err != nil {
			return nil, false, err
		}
		return viewerPage(viewerConfig{
			Type: a.Type, Format: v.Format, Version: v.Version, Head: v.Version, Entry: a.Entry(), Root: root,
		}), true, nil
	}
	return h.s.overviewFor(r.Context(), a, version, "")
}

// publishedHTML 在 InsertionPoint 处插入 `<meta name="robots" content="noindex">` 与内联的访客心跳脚本
// （心跳发到 `<root>_oryn/beat`；没构建 Web 时只插 meta），之后是设调节参数的内联脚本（页面声明了参数时；
// 发布页没有页面运行时，见 ParamsPrelude）。
func (h *Host) publishedHTML(src []byte, root string) []byte {
	at := htmlsrc.InsertionPoint(src)
	prelude := ParamsPrelude(src)
	var b bytes.Buffer
	b.Grow(len(src) + len(h.beaconJS) + len(root) + len(prelude) + 96)
	b.Write(src[:at])
	b.WriteString(`<meta name="robots" content="noindex">`)
	if len(h.beaconJS) > 0 {
		b.WriteString(`<script data-oryn-beat="` + html.EscapeString(root) + `">`)
		b.Write(h.beaconJS)
		b.WriteString(`</script>`)
	}
	b.Write(prelude)
	b.Write(src[at:])
	return b.Bytes()
}

// unlock 处理访问码页的表单：码对 303 到带授权段的根；码错、已锁定重新显示访问码页并提示。
func (h *Host) unlock(w http.ResponseWriter, r *http.Request, pub Publication, base string) {
	if r.Method != http.MethodPost {
		writeCodePage(w, r, base+unlockPath, gateAsk)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, unlockMaxBytes)
	grant, err := h.s.UnlockCode(r.Context(), pub.Slug, r.PostFormValue("code"))
	if err == nil {
		http.Redirect(w, r, base+"~"+grant+"/", http.StatusSeeOther)
		return
	}
	if state, ok := unlockResult(err); ok {
		writeCodePage(w, r, base+unlockPath, state)
		return
	}
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		writeGonePage(w, r)
		return
	}
	if oe, ok := AsOpError(err); ok && oe.Code == CodeInvalid {
		// 访问级别在输入期间被改掉了：回根重新判定
		http.Redirect(w, r, base, http.StatusSeeOther)
		return
	}
	h.fail(w, err)
}

// beat 记一次访客心跳（`POST <根>_oryn/beat`，请求体是页面实例的随机 id）。
func (h *Host) beat(w http.ResponseWriter, r *http.Request, pub Publication) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		frameError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, beatMaxBytes+1))
	id := strings.TrimSpace(string(body))
	if err != nil || len(body) > beatMaxBytes || !beatIDRe.MatchString(id) {
		frameError(w, http.StatusBadRequest, "心跳格式不对")
		return
	}
	h.visitors.record(h, pub.Slug, pub.ArtifactID, id)
	w.WriteHeader(http.StatusNoContent)
}

// visitorSet 在内存里记发布页的访客：每个页面实例最后一次心跳的时间。
type visitorSet struct {
	mu sync.Mutex
	// seen：slug → 页面实例 id → 最后一次心跳；artifact：slug → artifact id；sweeping：已安排了过期清理的 slug。
	seen     map[string]map[string]time.Time
	artifact map[string]string
	sweeping map[string]bool
}

// pruneLocked 清掉 slug 下超过 VisitorWindow 的实例，返回清掉了几个。调用方持有 mu。
func (v *visitorSet) pruneLocked(slug string, now time.Time) int {
	n := 0
	for id, t := range v.seen[slug] {
		if now.Sub(t) > VisitorWindow {
			delete(v.seen[slug], id)
			n++
		}
	}
	return n
}

// record 记一次心跳。访客数变了（来了新实例，或顺带清掉了过期的）时通知在场推送；
// 每个 slug 安排一次过期清理，访客离开后在场里的访客数也会降下来。
func (v *visitorSet) record(h *Host, slug, artifactID, id string) {
	now := h.now()
	v.mu.Lock()
	if v.seen == nil {
		v.seen, v.artifact, v.sweeping = map[string]map[string]time.Time{}, map[string]string{}, map[string]bool{}
	}
	m := v.seen[slug]
	if m == nil {
		m = map[string]time.Time{}
		v.seen[slug] = m
	}
	changed := v.pruneLocked(slug, now) > 0
	if _, ok := m[id]; !ok {
		if len(m) >= maxVisitorsPerSlug {
			v.mu.Unlock()
			if changed {
				h.s.armActivity(artifactID)
			}
			return
		}
		changed = true
	}
	m[id] = now
	v.artifact[slug] = artifactID
	sweep := !v.sweeping[slug]
	v.sweeping[slug] = true
	v.mu.Unlock()
	if changed {
		h.s.armActivity(artifactID)
	}
	if sweep {
		time.AfterFunc(VisitorWindow+time.Second, func() { v.sweep(h, slug) })
	}
}

// sweep 清掉 slug 下过期的实例；还有实例时再安排一次，全部过期时忘掉这个 slug。
func (v *visitorSet) sweep(h *Host, slug string) {
	v.mu.Lock()
	removed := v.pruneLocked(slug, h.now()) > 0
	artifactID := v.artifact[slug]
	again := len(v.seen[slug]) > 0
	if !again {
		delete(v.seen, slug)
		delete(v.artifact, slug)
		delete(v.sweeping, slug)
	}
	v.mu.Unlock()
	if removed {
		h.s.armActivity(artifactID)
	}
	if again {
		time.AfterFunc(VisitorWindow+time.Second, func() { v.sweep(h, slug) })
	}
}

// count 返回 slug 当前的访客数。
func (v *visitorSet) count(slug string, now time.Time) int {
	n := 0
	for _, t := range v.seen[slug] {
		if now.Sub(t) <= VisitorWindow {
			n++
		}
	}
	return n
}

// Visitors 返回发布页 slug 当前的访客数（VisitorWindow 内发过心跳的页面实例数）；接到 PublishConfig.Visitors。
func (h *Host) Visitors(slug string) int {
	h.visitors.mu.Lock()
	defer h.visitors.mu.Unlock()
	return h.visitors.count(slug, h.now())
}

// ArtifactVisitors 返回 artifact 的发布页当前的访客数（按 artifact 汇总它用过的 slug）；接到
// Service.SetActivityVisitors。
func (h *Host) ArtifactVisitors(artifactID string) int {
	h.visitors.mu.Lock()
	defer h.visitors.mu.Unlock()
	now, n := h.now(), 0
	for slug, id := range h.visitors.artifact {
		if id == artifactID {
			n += h.visitors.count(slug, now)
		}
	}
	return n
}
