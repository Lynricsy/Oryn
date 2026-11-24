package artifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/artifacts/render"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// probeDOM 是 --dump-dom 输出里带一个探针结果节点的页面。
func probeDOM(issues string) string {
	return `<html><head></head><body><div id="root"><p>x</p></div><script id="oryn-probe" type="application/json">` +
		`{"v":1,"frame":"home","height":800,"issues":` + issues + `}</script></body></html>`
}

func TestCheckParseProbe(t *testing.T) {
	got, err := parseProbe([]byte(probeDOM(`[{"rule":"text-clipped","loc":"src/frames/home.tsx:3:5","text":"立即开始","detail":64},` +
		`{"rule":"contrast-low","loc":null,"text":"浅灰","detail":2.32},` +
		`{"rule":"Bad Rule","loc":null,"text":"x"},` +
		`{"rule":"raw-color","loc":null,"text":"红","detail":{"evil":true}}]`)))
	require.NoError(t, err)
	assert.Equal(t, []design.Issue{
		{Rule: "text-clipped", Loc: "src/frames/home.tsx:3:5", Text: "立即开始", Detail: json.RawMessage(`64`)},
		{Rule: "contrast-low", Text: "浅灰", Detail: json.RawMessage(`2.32`)},
		{Rule: "raw-color", Text: "红"},
	}, got)

	got, err = parseProbe([]byte(probeDOM(`[]`)))
	require.NoError(t, err)
	assert.Empty(t, got)

	// 页面自己伪造的节点在前，探针追加的在 body 末尾：取最后一个
	forged := `<html><body><script id="oryn-probe" type="application/json">{"v":1,"issues":[]}</script>` +
		`<script id="oryn-probe" type="application/json">{"v":1,"issues":[{"rule":"dash-in-copy","loc":null,"text":"a—b"}]}</script></body></html>`
	got, err = parseProbe([]byte(forged))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "dash-in-copy", got[0].Rule)

	long := fmt.Sprintf(`[%s{"rule":"x","loc":null,"text":"%s"}]`, strings.Repeat(`{"rule":"y","loc":null,"text":"t"},`, 60), strings.Repeat("长", 300))
	got, err = parseProbe([]byte(probeDOM(long)))
	require.NoError(t, err)
	assert.Len(t, got, maxProbeIssues)

	got, err = parseProbe([]byte(probeDOM(`[{"rule":"x","loc":null,"text":"` + strings.Repeat("长", 300) + `"}]`)))
	require.NoError(t, err)
	assert.Equal(t, maxIssueTextRunes, len([]rune(got[0].Text)))

	_, err = parseProbe([]byte(`<html><body><p>没有探针</p></body></html>`))
	assert.ErrorContains(t, err, "没有探针结果")
	_, err = parseProbe([]byte(`<html><body><script id="oryn-probe" type="application/json">{"v":2}</script></body></html>`))
	assert.ErrorContains(t, err, "不合法")
}

// designCanvas 建一个两帧（home、about）的设计画布，另带一个 HTML 画框 legacy.html；override 按路径替换缺省文件。
func designCanvas(t *testing.T, e *env, override ...FileChange) Artifact {
	t.Helper()
	doc := `{"format":2,"design":{"engine":1,"start":"home"},"canvases":[{"id":"c1","name":"首页"}],"pages":[` +
		`{"id":"home","canvas":"c1","name":"首页","file":"dist/home.html","source":"src/frames/home.tsx","width":390,"height":844},` +
		`{"id":"about","canvas":"c1","name":"关于","file":"dist/about.html","source":"src/frames/about.tsx"},` +
		`{"id":"legacy","canvas":"c1","name":"旧页","file":"legacy.html"}],"notes":[]}`
	files := []FileChange{
		put("canvas.json", doc),
		put("src/frames/home.tsx", "export default function Home() { return <p>home</p> }\n"),
		put("src/frames/about.tsx", "export default function About() { return <p>about</p> }\n"),
		put("src/theme.css", "@theme { --color-accent: #0f766e; }\n"),
		put("dist/home.html", `<!doctype html><html><head><script type="module" src="./assets/home.js"></script></head><body><div id="root"></div></body></html>`),
		put("dist/about.html", `<!doctype html><html><head></head><body><div id="root"></div></body></html>`),
		put("legacy.html", "<p>旧</p>"),
	}
	for _, o := range override {
		files[slices.IndexFunc(files, func(f FileChange) bool { return f.Path == o.Path })] = o
	}
	a, err := e.s.Create(context.Background(), CreateParams{Title: "设计稿", ThreadID: "thr_a", Files: files})
	require.NoError(t, err)
	require.Equal(t, KindCanvas, a.Kind)
	return a
}

// domRenderer 按作业路径回固定的 DOM；没登记的路径回渲染失败。
type domRenderer struct {
	mu   sync.Mutex
	doms map[string]string
	jobs []render.Job
	down string
}

func (d *domRenderer) Status() (bool, string) { return d.down == "", d.down }

func (d *domRenderer) Run(_ context.Context, j render.Job) ([]byte, error) {
	if d.down != "" {
		return nil, fmt.Errorf("%w：%s", render.ErrUnavailable, d.down)
	}
	d.mu.Lock()
	d.jobs = append(d.jobs, j)
	d.mu.Unlock()
	dom, ok := d.doms[j.Path]
	if !ok {
		return nil, fmt.Errorf("render: Chromium 超过 30s 没有完成")
	}
	return []byte(dom), nil
}

func storedCheck(t *testing.T, e *env, id string, n int) *design.Check {
	t.Helper()
	v, err := e.s.Version(context.Background(), id, n)
	require.NoError(t, err)
	if len(v.Check) == 0 {
		return nil
	}
	var c design.Check
	require.NoError(t, json.Unmarshal(v.Check, &c))
	return &c
}

func TestCheckArtifactDesignCanvas(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	a := designCanvas(t, e)
	ctx := context.Background()
	r := &domRenderer{doms: map[string]string{
		"dist/home.html":  probeDOM(`[{"rule":"text-clipped","loc":"src/frames/home.tsx:3:5","text":"立即开始","detail":64}]`),
		"dist/about.html": probeDOM(`[]`),
	}}
	withRenderer(e, r)

	res, err := e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	// 只检查带 source 的画框，窗口取画框尺寸，查询串带探针开关
	require.Len(t, r.jobs, 2)
	byPath := map[string]render.Job{}
	for _, j := range r.jobs {
		byPath[j.Path] = j
	}
	assert.Equal(t, render.Job{ArtifactID: a.ID, Version: 1, Path: "dist/home.html", Query: "__oryn_probe=1", Width: 390, Height: 844, Scale: 1, DumpDOM: true}, byPath["dist/home.html"])
	assert.Equal(t, 1280, byPath["dist/about.html"].Width)

	lines := strings.Split(res.Text, "\n")
	assert.Contains(t, lines[0], "issues，1 处问题（2 个页面）")
	assert.Equal(t, `text-clipped home src/frames/home.tsx:3:5 立即开始 (64)`, lines[1])
	assert.Equal(t, obj{"op": OpCheck, "id": a.ID, "title": a.Title, "kind": KindCanvas, "version": 1, "status": design.CheckIssues, "issues": 1, "frames": 2}, res.Details)

	got := storedCheck(t, e, a.ID, 1)
	require.NotNil(t, got)
	assert.Equal(t, design.CheckIssues, got.Status)
	assert.Equal(t, []design.Issue{{Rule: "text-clipped", Frame: "home", Loc: "src/frames/home.tsx:3:5", Text: "立即开始", Detail: json.RawMessage(`64`)}}, got.Issues)
	src := map[string][]byte{
		"src/frames/home.tsx":  []byte("export default function Home() { return <p>home</p> }\n"),
		"src/frames/about.tsx": []byte("export default function About() { return <p>about</p> }\n"),
		"src/theme.css":        []byte("@theme { --color-accent: #0f766e; }\n"),
	}
	assert.Equal(t, design.SourceHash(src), got.Fingerprint)
	// REST 的设计状态读到同一份结果
	d, err := e.s.Design(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, d.Check)
	assert.EqualValues(t, design.CheckIssues, d.Check.Status)

	// 修好后再查：clean 覆盖
	r.doms["dist/home.html"] = probeDOM(`[]`)
	res, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	assert.Equal(t, design.CheckClean, res.Details.(obj)["status"])
	assert.Equal(t, design.CheckClean, storedCheck(t, e, a.ID, 1).Status)

	// 只查一个画框（按 file 认出画框）
	r.jobs = nil
	_, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`","path":"dist/about.html"}`))
	require.NoError(t, err)
	require.Len(t, r.jobs, 1)
	assert.Equal(t, "dist/about.html", r.jobs[0].Path)

	// 单帧检查只回给调用方，不改版本徽标：home 有问题时查干净的 about，版本仍是 issues
	r.doms["dist/home.html"] = probeDOM(`[{"rule":"text-clipped","loc":"src/frames/home.tsx:3:5","text":"立即开始"}]`)
	_, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	require.Equal(t, design.CheckIssues, storedCheck(t, e, a.ID, 1).Status)
	res, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`","path":"dist/about.html"}`))
	require.NoError(t, err)
	assert.Equal(t, design.CheckClean, res.Details.(obj)["status"])
	got = storedCheck(t, e, a.ID, 1)
	assert.Equal(t, design.CheckIssues, got.Status)
	require.Len(t, got.Issues, 1)
	assert.Equal(t, "home", got.Issues[0].Frame)
	// 反过来：版本 clean 时单查一张有问题的帧，也不把版本改成 issues
	r.doms["dist/home.html"] = probeDOM(`[]`)
	_, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	r.doms["dist/about.html"] = probeDOM(`[{"rule":"dash-in-copy","text":"a—b"}]`)
	res, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`","path":"dist/about.html"}`))
	require.NoError(t, err)
	assert.Equal(t, design.CheckIssues, res.Details.(obj)["status"])
	assert.Equal(t, design.CheckClean, storedCheck(t, e, a.ID, 1).Status)

	_, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`","path":"dist/nope.html"}`))
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`","bogus":1}`))
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.agentCheckArtifact(ctx, threadCaller("thr_other"), json.RawMessage(`{"id":"`+a.ID+`"}`))
	assert.Equal(t, CodeNotFound, code(err))
}

func TestCheckArtifactUnverified(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	a := designCanvas(t, e)
	ctx := context.Background()

	// 没接渲染器：unverified，不报错，结果照样落库（带指纹）
	res, err := e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	assert.Equal(t, design.CheckUnverified, res.Details.(obj)["status"])
	assert.Contains(t, res.Text, "不要重试 check_artifact")
	got := storedCheck(t, e, a.ID, 1)
	require.NotNil(t, got)
	assert.Equal(t, design.CheckUnverified, got.Status)
	assert.NotEmpty(t, got.Fingerprint)

	// 有过结论后渲染不可用：不覆盖已有结论
	withRenderer(e, &domRenderer{doms: map[string]string{"dist/home.html": probeDOM(`[]`), "dist/about.html": probeDOM(`[]`)}})
	_, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	require.Equal(t, design.CheckClean, storedCheck(t, e, a.ID, 1).Status)
	withRenderer(e, &domRenderer{down: "找不到 Chromium"})
	res, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	assert.Equal(t, design.CheckUnverified, res.Details.(obj)["status"])
	assert.Contains(t, res.Text, "找不到 Chromium")
	assert.Equal(t, design.CheckClean, storedCheck(t, e, a.ID, 1).Status)

	// 页面里没有结果节点（探针没加载）：该页面 unverified，有问题的页面照样报 issues
	withRenderer(e, &domRenderer{doms: map[string]string{
		"dist/home.html":  `<html><body><div id="root"></div></body></html>`,
		"dist/about.html": probeDOM(`[{"rule":"dash-in-copy","loc":null,"text":"a—b","detail":"U+2014"}]`),
	}})
	res, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	assert.Equal(t, design.CheckIssues, res.Details.(obj)["status"])
	assert.Contains(t, res.Text, "\ndash-in-copy about - a—b (\"U+2014\")")
	assert.Contains(t, res.Text, "\n未检查 home：页面里没有探针结果")
}

func TestCheckArtifactWebPage(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "页", ThreadID: "thr_a", Files: []FileChange{put("index.html", "<p>x</p>"), put("b.html", "<p>b</p>"), put("s.css", "p{}")}})
	require.NoError(t, err)
	r := &domRenderer{doms: map[string]string{"index.html": probeDOM(`[]`), "b.html": probeDOM(`[]`)}}
	withRenderer(e, r)

	res, err := e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`"}`))
	require.NoError(t, err)
	require.Len(t, r.jobs, 1)
	assert.Equal(t, "index.html", r.jobs[0].Path)
	assert.Equal(t, design.CheckClean, res.Details.(obj)["status"])
	// 网页没有源码指纹
	assert.Empty(t, storedCheck(t, e, a.ID, 1).Fingerprint)

	_, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`","path":"b.html"}`))
	require.NoError(t, err)
	assert.Equal(t, "b.html", r.jobs[1].Path)
	_, err = e.s.agentCheckArtifact(ctx, c, json.RawMessage(`{"id":"`+a.ID+`","path":"s.css"}`))
	assert.Equal(t, CodeInvalid, code(err))
}

// 渲染模式下 `__oryn_probe=1`：设计画框只注入配置（引擎入口自己加载 ./probe.js），HTML 页面另内联探针脚本；
// dist/probe.js 一律是嵌入的探针脚本。配置取自 artifact_designs。
func TestCheckProbeRenderSite(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	assets := func(name string) ([]byte, bool) {
		if name == probeAsset {
			return []byte(`/*probe*/var s="</script>";`), true
		}
		return fakeRuntime(name)
	}
	h := newHost(t, e, "localhost", assets)
	a := designCanvas(t, e)
	ctx := context.Background()
	self := "http://" + render.Host + "/tok/"
	serve := func(p, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok"+p+query, nil)
		rec := httptest.NewRecorder()
		h.ServeRender(rec, req, render.Target{ArtifactID: a.ID, Version: 1, Self: self}, p)
		return rec
	}

	plain := serve("/dist/home.html", "")
	require.Equal(t, http.StatusOK, plain.Code)
	assert.NotContains(t, plain.Body.String(), "__orynProbe")

	rec := serve("/dist/home.html", "?__oryn_probe=1")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `<head><script>window.__orynProbe={"aesthetic":false,"surface":""};</script><script type="module"`)
	assert.NotContains(t, body, "/*probe*/")

	rec = serve("/legacy.html", "?__oryn_probe=1")
	body = rec.Body.String()
	assert.Contains(t, body, `window.__orynProbe={"aesthetic":false,"surface":""};</script><script>/*probe*/var s="<\/script>";</script>`)

	_, err := e.st.DB.ExecContext(ctx, `INSERT INTO artifact_designs (artifact_id, surface, direction) VALUES (?, 'app', '{"name":"深海"}')`, a.ID)
	require.NoError(t, err)
	assert.Contains(t, serve("/dist/home.html", "?__oryn_probe=1").Body.String(), `window.__orynProbe={"aesthetic":true,"surface":"app"};`)

	js := serve("/dist/probe.js", "")
	require.Equal(t, http.StatusOK, js.Code)
	assert.Equal(t, `/*probe*/var s="</script>";`, js.Body.String())
	assert.Equal(t, "text/javascript; charset=utf-8", js.Header().Get("Content-Type"))
	// 同源 self：CSP 的 script-src 放行作业根下的 probe.js 与内联脚本
	assert.Contains(t, js.Header().Get("Content-Security-Policy"), "script-src "+self+" 'unsafe-inline'")

	noProbe := newHost(t, e, "localhost", noAsset)
	req := httptest.NewRequest(http.MethodGet, "http://"+render.Host+"/tok/dist/probe.js", nil)
	missing := httptest.NewRecorder()
	noProbe.ServeRender(missing, req, render.Target{ArtifactID: a.ID, Version: 1, Self: self}, "/dist/probe.js")
	assert.Equal(t, http.StatusNotFound, missing.Code)
}

// 真 Chromium（ORYN_TEST_CHROMIUM=1，并且 internal/webui/dist 里有探针脚本）：一个 overflow:hidden 截断的按钮，
// HTML 画框（内联探针）与设计画框（页面加载后自己加载 ./probe.js，同引擎入口）都报出 text-clipped 与它的 loc。
func TestCheckProbeChromium(t *testing.T) {
	if os.Getenv("ORYN_TEST_CHROMIUM") != "1" {
		t.Skip("set ORYN_TEST_CHROMIUM=1 to run Chromium integration tests")
	}
	if _, ok := webui.Asset(probeAsset); !ok {
		t.Fatal("嵌入的 Web 产物里没有 sandbox/probe.js：先 make web（或 vite build -c vite.sandbox.config.ts --mode probe 后拷进 internal/webui/dist）")
	}
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	h := newHost(t, e, "localhost", webui.Asset)
	cfg := render.Config{Site: h, CDNs: CDNs}
	if os.Geteuid() == 0 {
		if _, err := user.Lookup("nobody"); err != nil {
			t.Skip("root without a nobody account")
		}
		cfg.User = "nobody"
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	cfg.Exe = exe
	base, err := os.MkdirTemp("", "oryn-render-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	require.NoError(t, os.Chmod(base, 0o711))
	cfg.Dir = filepath.Join(base, "jobs")
	rr, err := render.New(cfg)
	require.NoError(t, err)
	t.Cleanup(rr.Close)
	ctx := context.Background()
	rr.Preflight(ctx)
	ok, reason := rr.Status()
	require.True(t, ok, reason)
	withRenderer(e, rr)

	const button = `<button data-oryn-loc="src/frames/home.tsx:7:9" style="width:80px;height:32px;overflow:hidden;white-space:nowrap;` +
		`background:#0f766e;color:#ffffff;border:0">立即开始免费试用</button>`
	// 设计画框：模拟引擎入口——页面脚本渲染出按钮后加载同源 ./probe.js
	frame := `<!doctype html><html><head><meta charset="utf-8"></head><body><div id="root"></div><script>` +
		`document.getElementById("root").innerHTML = ` + jsString(button) + `;` +
		`if (new URLSearchParams(location.search).get("__oryn_probe") === "1") {` +
		` const s = document.createElement("script"); s.src = new URL("./probe.js", location.href).href; document.head.append(s); }` +
		`</script></body></html>`
	a := designCanvas(t, e, put("dist/home.html", frame), put("legacy.html", `<!doctype html><html><body>`+button+`</body></html>`))

	res, err := e.s.agentCheckArtifact(ctx, threadCaller("thr_a"), json.RawMessage(`{"id":"`+a.ID+`","path":"dist/home.html"}`))
	require.NoError(t, err)
	t.Logf("design frame:\n%s", res.Text)
	assert.Contains(t, res.Text, "\ntext-clipped home src/frames/home.tsx:7:9 立即开始免费试用")

	res, err = e.s.agentCheckArtifact(ctx, threadCaller("thr_a"), json.RawMessage(`{"id":"`+a.ID+`","path":"legacy.html"}`))
	require.NoError(t, err)
	t.Logf("html frame:\n%s", res.Text)
	assert.Contains(t, res.Text, "\ntext-clipped legacy src/frames/home.tsx:7:9 立即开始免费试用")
	got := storedCheck(t, e, a.ID, 1)
	require.NotNil(t, got)
	assert.Equal(t, design.CheckIssues, got.Status)
}

// jsString 把 s 写成 JS 字符串字面量（JSON 字符串即合法的 JS 字符串）。
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
