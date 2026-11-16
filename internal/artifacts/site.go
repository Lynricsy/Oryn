package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/artifacts/htmlsrc"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

// runtimeAsset 是页面运行时在嵌入的 Web 构建产物里的路径。
const runtimeAsset = "sandbox/runtime.js"

// runtimeConfig 是注入页面的 `__orynStart(<配置>)` 的参数。
type runtimeConfig struct {
	V       int    `json:"v"`
	Mark    string `json:"mark"`
	Path    string `json:"path"`
	Runtime bool   `json:"runtime"`
}

const markAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// newMark 返回每次响应新生成的 8 位 [a-z0-9] 源码标记：页面脚本后来生成的元素带不上它。
func newMark() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	for i := range b {
		b[i] = markAlphabet[int(b[i])%len(markAlphabet)]
	}
	return string(b[:])
}

var scriptCloseRe = regexp.MustCompile(`(?i)</(script)`)

// inlineScript 把脚本源码转义成能放进内联 <script> 的形式（`</script` → `<\/script`，JS 里两者等价）。
func inlineScript(js []byte) []byte {
	return scriptCloseRe.ReplaceAll(js, []byte(`<\/$1`))
}

// servedHTML 返回帧里提供的 HTML：给每个起始标签加源码标记，再在 InsertionPoint 处插入内联的页面运行时
// 与 `__orynStart(<配置>)`。runtimeJS 为空（没跑过 make web）时只标记、不注入。
func servedHTML(src []byte, path string, runtime bool, runtimeJS []byte) []byte {
	mark := newMark()
	out := htmlsrc.MarkSources(src, mark)
	if len(runtimeJS) == 0 {
		return out
	}
	cfg, _ := json.Marshal(runtimeConfig{V: 1, Mark: mark, Path: path, Runtime: runtime}) // json 把 < > & 转义成 \u00XX
	at := htmlsrc.InsertionPoint(out)
	var b bytes.Buffer
	b.Grow(len(out) + len(runtimeJS) + len(cfg) + 48)
	b.Write(out[:at])
	b.WriteString("<script>")
	b.Write(runtimeJS)
	b.WriteString("\n__orynStart(")
	b.Write(cfg)
	b.WriteString(");</script>")
	b.Write(out[at:])
	return b.Bytes()
}

// Host 以渲染模式给控制面的 headless Chromium 提供内容。
var _ render.Site = (*Host)(nil)

// RenderTarget 实现 render.Site：解析渲染作业的版本（0 = 当前最新）与是否严格（最新版本声明了 files 或 sample）。
// artifact 不存在回 not_found，在回收站里回 deleted，版本不存在回 not_found。
func (h *Host) RenderTarget(ctx context.Context, id string, version int) (int, bool, error) {
	a, err := h.s.Get(ctx, id)
	if err != nil {
		return 0, false, err
	}
	if a.Deleted() {
		return 0, false, deletedErr(a.ID)
	}
	head, err := h.s.Version(ctx, a.ID, 0)
	if err != nil {
		return 0, false, err
	}
	strict := slices.Contains(head.Capabilities, "files") || slices.Contains(head.Capabilities, "sample")
	if version == 0 {
		return head.Version, strict, nil
	}
	v, err := h.s.Version(ctx, a.ID, version)
	if err != nil {
		return 0, false, err
	}
	return v.Version, strict, nil
}

// ServeRender 实现 render.Site：以「渲染模式」提供作业根之内的路径 p——版本里的文件原样提供（不注入运行时、
// 不标源码），响应头同帧，但 'self' 换成作业根的 URL、没有 frame-ancestors（渲染时页面就是顶层文档）。
func (h *Host) ServeRender(w http.ResponseWriter, r *http.Request, t render.Target, p string) {
	csp, _, _ := strings.Cut(framePolicy(sandboxFrame, t.Self, "'none'", t.Strict), "; frame-ancestors ")
	setFrameHeaders(w, r, csp)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		frameError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	path, version, ok := framePath(p, t.Version)
	if !ok {
		frameError(w, http.StatusNotFound, "文件不存在")
		return
	}
	rc, file, err := h.s.Open(r.Context(), t.ArtifactID, version, path)
	if err != nil {
		h.fail(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", file.Mime)
	http.ServeContent(w, r, "", time.Time{}, rc)
}
