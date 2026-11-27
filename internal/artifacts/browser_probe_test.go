package artifacts

// 浏览器回归探针的服务端：设了 ORYN_ARTIFACT_BROWSER_PROBE 时，用真实嵌入的 Web 产物（查看器、页面运行时）起泛域名
// （a-<令牌>.localhost / p-<slug>.localhost）帧与发布页服务，把要访问的地址写进该文件，再等 web/scripts/
// artifact-browser-probe.ts 用 WebKit 与 Chromium 逐个打开。未设时跳过，不影响 go test ./...。
//
// 为什么要真浏览器：帧与发布页运行在不透明源里（sandbox 不含 allow-same-origin），CSP 的 `'self'` 在 WebKit 里不匹配
// 页面自己的地址，Chromium 却放行——服务端单测与 Chromium 冒烟都看不出来，Safari 与 macOS / iOS App 里查看器读不到入口文件。

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/webui"
	"github.com/stretchr/testify/require"
)

// browserProbeTargets 是探针脚本读取的清单：每项是一个要打开的地址与页面里必须出现的文字。
type browserProbeTargets struct {
	Targets []browserProbeTarget `json:"targets"`
	// Done 是探针跑完后要请求的地址，收到后服务端退出。
	Done string `json:"done"`
}

type browserProbeTarget struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Want  string `json:"want"`
	Image bool   `json:"image"`
	// Shot 为真时探针脚本把页面截图存到 .dev/m31-office/probe/（人工核对 Office 样本的清单项）。
	Shot bool `json:"shot,omitempty"`
}

// officeSamples 是 web/sandbox/viewers/testdata/office/ 下的样本（web/scripts/office-fixtures.py 生成）与它们的类型。
var officeSamples = []struct{ file, typ string }{
	{"sample.docx", TypeDocx},
	{"sample.xlsx", TypeXlsx},
	{"sample.pptx", TypePptx},
	{"sample.pdf", TypePDF},
}

// probePNG 是能真正解码的 1×1 红色 PNG（pngBytes 只有文件头，浏览器解码不出来、naturalWidth 为 0）。
const probePNG = "\x89\x50\x4e\x47\x0d\x0a\x1a\x0a\x00\x00\x00\x0d\x49\x48\x44\x52\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90\x77\x53\xde\x00\x00\x00\x0c\x49\x44\x41\x54\x78\x9c\x63\xf8\xcf\xc0\x00\x00\x03\x01\x01\x00\xc9\xfe\x92\xef\x00\x00\x00\x00\x49\x45\x4e\x44\xae\x42\x60\x82"

func TestArtifactBrowserProbeServer(t *testing.T) {
	out := os.Getenv("ORYN_ARTIFACT_BROWSER_PROBE")
	if out == "" {
		t.Skip("只在 web/scripts/artifact-browser-probe.ts 里运行")
	}
	if _, ok := webui.Asset("sandbox/viewers/document.js"); !ok {
		t.Fatal("嵌入的 Web 产物里没有查看器：先 make web")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	origin := "http://localhost:" + strings.TrimPrefix(ln.Addr().String()[strings.LastIndex(ln.Addr().String(), ":"):], ":")

	e := newEnv(t)
	base, err := portalhost.NewBase(origin, "localhost")
	require.NoError(t, err)
	h := NewHost(e.s, HostConfig{Base: base, Now: e.clk.now, Asset: webui.Asset})
	e.s.ConfigurePublishing(PublishConfig{Base: base, GrantKey: DeriveGrantKey([]byte("probe")), Visitors: h.Visitors})
	ctx := context.Background()

	doc, err := e.s.Create(ctx, CreateParams{Title: "探针文档", Type: TypeDocument, Files: []FileChange{
		put("document.md", "# 探针文档\n\nDOC_PROBE_OK\n\n![图](images/a.png)\n"), put("images/a.png", probePNG),
	}})
	require.NoError(t, err)
	_, docURL, err := h.OpenFrame(ctx, doc.ID, 0, "", "zh-CN")
	require.NoError(t, err)

	// 页面自己 fetch 同帧里的数据文件、加载同帧图片：覆盖 connect-src 与 img-src 的自身源
	page := `<!doctype html><title>p</title><img src="images/a.png" alt="a"><p id="o">…</p>` +
		`<script>fetch("data.txt").then(r=>r.text()).then(t=>{document.getElementById("o").textContent=t},()=>{document.getElementById("o").textContent="FETCH_FAILED"})</script>`
	web := mkPage(t, e, put("index.html", page), put("data.txt", "PAGE_PROBE_OK"), put("images/a.png", probePNG))
	_, webURL, err := h.OpenFrame(ctx, web.ID, 0, "", "")
	require.NoError(t, err)
	pub, err := e.s.Publish(ctx, web.ID, PublishParams{Access: AccessPublic})
	require.NoError(t, err)

	// Office 查看器：四个样本各开一个帧（zh-CN，渲染完成后页面里出现「已渲染」）；Word 另发布一次，看独占窗口的发布页
	var office []browserProbeTarget
	var docxID string
	for _, s := range officeSamples {
		data, err := os.ReadFile("../../web/sandbox/viewers/testdata/office/" + s.file)
		require.NoError(t, err, "先 uv run web/scripts/office-fixtures.py")
		a, err := e.s.Create(ctx, CreateParams{Title: "样本 " + s.typ, Type: s.typ, Files: []FileChange{{Path: TypeEntry(s.typ), Data: bytes.NewReader(data)}}})
		require.NoError(t, err)
		_, u, err := h.OpenFrame(ctx, a.ID, 0, "", "zh-CN")
		require.NoError(t, err)
		office = append(office, browserProbeTarget{Name: s.typ + " 查看器（帧）", URL: u, Want: "已渲染", Shot: true})
		if s.typ == TypeDocx {
			docxID = a.ID
		}
	}
	docxPub, err := e.s.Publish(ctx, docxID, PublishParams{Access: AccessPublic})
	require.NoError(t, err)
	office = append(office, browserProbeTarget{Name: "docx 发布页", URL: base.ArtifactURL('p', docxPub.Slug), Want: "Rendered", Shot: true})

	done := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/__probe_done", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		select {
		case <-done:
		default:
			close(done)
		}
	})
	srv := &http.Server{Handler: h.Middleware(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	targets := browserProbeTargets{
		Targets: append([]browserProbeTarget{
			{Name: "文档查看器（帧）", URL: docURL, Want: "DOC_PROBE_OK", Image: true},
			{Name: "网页 artifact（帧）", URL: webURL, Want: "PAGE_PROBE_OK", Image: true},
			{Name: "发布页", URL: base.ArtifactURL('p', pub.Slug), Want: "PAGE_PROBE_OK", Image: true},
		}, office...),
		Done: origin + "/__probe_done",
	}
	b, err := json.Marshal(targets)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(out, b, 0o600))

	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("探针脚本 3 分钟内没跑完")
	}
}
