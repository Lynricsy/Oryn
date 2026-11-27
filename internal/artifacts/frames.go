package artifacts

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"regexp"
	"strings"
	"time"
)

// 帧表的上限与空闲过期时长。orynd 重启后帧全部失效，查看器遇到 404 时重新申请。
const (
	maxFrames         = 256
	frameIdle         = 12 * time.Hour
	topLevelFrameIdle = 10 * time.Minute
)

// FramePathPrefix 是主域回退模式下帧的挂载前缀：帧根是 `<public_url>/_af/<令牌>/`。
const FramePathPrefix = "/_af/"

// 帧的寻址方式（GET artifacts/capabilities 的 frames）。
const (
	FramesHost = "host" // 泛域名 a-<令牌>.<portal_domain>
	FramesPath = "path" // 主域回退路径 /_af/<令牌>/
)

// frame 是一个渲染帧：令牌就是访问凭证（帧请求不带 cookie）。
type frame struct {
	artifactID string
	// version 为 0 表示跟随最新版本。
	version int
	// runtime：页面运行时能力是否可用（只在跟随最新版本、非顶层的帧上为真）。
	runtime bool
	// topLevel：「在浏览器中打开」的顶层帧（不标源码、不注入运行时、更宽的 sandbox、frame-ancestors 'none'）。
	topLevel bool
	// strict：一旦用了不含 CDN 的严格策略，就在帧的生命周期内保持。
	strict bool
	// ancestors 是 frame-ancestors 的值（申请帧的 API 请求的 Origin；顶层帧为 'none'）。
	ancestors string
	// locale 是类型化 artifact 查看器的界面语言（申请帧时给出；空 = 由查看器自己决定）。
	locale string
	// view 非空时这是视图帧（M33，view.go）：每次取帧根时现取内容；artifactID 等字段不用。
	view func(context.Context) (ViewDoc, error)
	// public：公开分享页的访客申请的视图帧，计入匿名配额（maxPublicFrames）。
	public bool
	usedAt time.Time
}

func (f *frame) idle() time.Duration {
	if f.topLevel {
		return topLevelFrameIdle
	}
	return frameIdle
}

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newToken 返回 16 个随机字节的小写无填充 base32（26 个字符，`^[a-z2-7]{26}$`）。
func newToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return strings.ToLower(tokenEncoding.EncodeToString(b[:]))
}

// Mode 返回帧的寻址方式（FramesHost / FramesPath）。
func (h *Host) Mode() string {
	if h.base.Enabled() {
		return FramesHost
	}
	return FramesPath
}

// frameURL 返回帧根地址。
func (h *Host) frameURL(token string) string {
	if h.base.Enabled() {
		return h.base.ArtifactURL('a', token)
	}
	return h.base.Origin + FramePathPrefix + token + "/"
}

// frameSelf 返回帧 CSP 里代表「页面自己的文件」的源表达式：帧根地址本身（泛域名模式 `https://a-<令牌>.<域>/`，
// 回退模式 `<public_url 源>/_af/<令牌>/`）。不用 `'self'`：sandbox 不含 allow-same-origin，页面在不透明源里，
// WebKit（Safari、macOS / iOS 桌面与移动 App）里 `'self'` 不匹配页面自己的地址，查看器读入口文件、运行时端点都会被挡。
func (h *Host) frameSelf(token string) string {
	return h.frameURL(token)
}

// checkOpen 确认 artifact 存在、不在回收站里，且 version（非 0 时）存在。
func (h *Host) checkOpen(ctx context.Context, id string, version int) error {
	a, err := h.s.Get(ctx, id)
	if err != nil {
		return err
	}
	if a.Deleted() {
		return deletedErr(id)
	}
	if version != 0 {
		if _, err := h.s.Version(ctx, id, version); err != nil {
			return err
		}
	}
	return nil
}

// OpenFrame 为查看器申请一个帧，返回令牌与帧根地址。version 为 0 时跟随最新版本（带页面运行时），否则钉住
// 该版本（只读）。origin 是申请请求的 Origin 头，成为帧的 frame-ancestors；缺省或不合法时用 public_url 的源。
// locale 是类型化 artifact 查看器的界面语言（不合法时忽略）。
func (h *Host) OpenFrame(ctx context.Context, id string, version int, origin, locale string) (token, url string, err error) {
	if err := h.checkOpen(ctx, id, version); err != nil {
		return "", "", err
	}
	token = h.add(&frame{
		artifactID: id, version: version, runtime: version == 0,
		ancestors: ancestorOrigin(origin, h.base.Origin), locale: cleanLocale(locale),
	})
	return token, h.frameURL(token), nil
}

// OpenLink 为「在浏览器中打开」申请一个顶层帧，返回地址（自带令牌，不需要登录态）。
func (h *Host) OpenLink(ctx context.Context, id string, version int, locale string) (string, error) {
	if err := h.checkOpen(ctx, id, version); err != nil {
		return "", err
	}
	token := h.add(&frame{artifactID: id, version: version, topLevel: true, ancestors: "'none'", locale: cleanLocale(locale)})
	return h.frameURL(token), nil
}

var localeRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8}){0,3}$`)

// cleanLocale 只保留 BCP 47 形态的语言标签（会写进查看器页的 lang 属性与配置）；其它一律当作没给。
func cleanLocale(l string) string {
	if localeRe.MatchString(l) {
		return l
	}
	return ""
}

// CloseFrame 关闭一个帧（不存在时什么也不做）。
func (h *Host) CloseFrame(token string) {
	h.mu.Lock()
	delete(h.frames, token)
	h.mu.Unlock()
}

// add 登记一个帧：先清掉过期的，满了再淘汰最久未用的。公开帧只在公开帧之间淘汰（上限 maxPublicFrames），
// 匿名访客挤不掉主人的帧。
func (h *Host) add(f *frame) string {
	now := h.now()
	f.usedAt = now
	token := newToken()
	h.mu.Lock()
	defer h.mu.Unlock()
	for t, g := range h.frames {
		if now.Sub(g.usedAt) > g.idle() {
			delete(h.frames, t)
		}
	}
	if f.public {
		for h.evictOldest(func(g *frame) bool { return g.public }, maxPublicFrames) {
		}
	} else {
		for h.evictOldest(func(*frame) bool { return true }, maxFrames) {
		}
	}
	h.frames[token] = f
	return token
}

// evictOldest 在满足 match 的帧已有 limit 个及以上时淘汰其中最久未用的一个，返回是否淘汰了。调用方持有 h.mu。
func (h *Host) evictOldest(match func(*frame) bool, limit int) bool {
	n, oldest := 0, ""
	for t, g := range h.frames {
		if !match(g) {
			continue
		}
		n++
		if oldest == "" || g.usedAt.Before(h.frames[oldest].usedAt) {
			oldest = t
		}
	}
	if n < limit {
		return false
	}
	delete(h.frames, oldest)
	return true
}

// use 取出一个帧的快照并刷新其使用时间；不存在或已过期时 ok 为假。
func (h *Host) use(token string) (frame, bool) {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.frames[token]
	if !ok {
		return frame{}, false
	}
	if now.Sub(f.usedAt) > f.idle() {
		delete(h.frames, token)
		return frame{}, false
	}
	f.usedAt = now
	return *f, true
}

// escalate 把帧标成严格模式（只升不降），返回帧现在是否严格。
func (h *Host) escalate(token string, strict bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, ok := h.frames[token]
	if !ok {
		return strict
	}
	if strict {
		f.strict = true
	}
	return f.strict
}
