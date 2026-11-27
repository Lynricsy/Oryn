package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// mkEmptyDesign 建一个带 design、没有画框的空设计画布（同 orbd design.open 新建的那种），返回 id。
func mkEmptyDesign(t *testing.T, e *env, thread string) string {
	t.Helper()
	doc := canvas.Doc{Design: &canvas.Design{Engine: canvas.DesignEngine}, Canvases: []canvas.Canvas{{ID: canvas.DefaultCanvasID, Name: canvas.DefaultCanvasName}}}
	a, err := e.s.Create(context.Background(), CreateParams{Title: "设计", ThreadID: thread, Files: []FileChange{put(EntryCanvas, string(doc.JSON()))}})
	require.NoError(t, err)
	require.Equal(t, KindCanvas, a.Kind)
	return a.ID
}

// sealFiles 把 path → 内容 写成 seal_design 的 files（source 经 fakeFetch 取回；内容为 "" 表示 remove）。
func sealFiles(src fakeFetch, files map[string]string) []map[string]any {
	var out []map[string]any
	for p, s := range files {
		if s == "" {
			out = append(out, map[string]any{"path": p, "remove": true})
			continue
		}
		key := "/home/u/designs/x/" + p
		src[key] = s
		out = append(out, map[string]any{"path": p, "source": key})
	}
	return out
}

func sealParams(t *testing.T, id string, base int, files []map[string]any, check *design.Check) string {
	t.Helper()
	m := map[string]any{"id": id, "baseVersion": base, "files": files}
	if check != nil {
		m["check"] = check
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func framesJSON(frames ...string) string { return "[" + strings.Join(frames, ",") + "]" }

func sealVersion(t *testing.T, r Result) int {
	t.Helper()
	var v struct {
		Version int `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.Text), &v))
	return v.Version
}

func loadDoc(t *testing.T, e *env, id string, version int) canvas.Doc {
	t.Helper()
	doc, err := canvas.Parse([]byte(readFile(t, e.s, id, version, EntryCanvas)))
	require.NoError(t, err)
	return doc
}

// M29 T4：多帧封存——按 dist/frames.json 新增画框（source、file、name、尺寸、设备取自帧声明，自动排位），版本记在
// 调用方回合下、带 check（fingerprint = 新版本 src/ 的 SourceHash）；再封存时增删帧后 pages 对齐：消失的帧 remove、
// 新帧 add、留下的帧不按帧声明改 name / 尺寸（用户在画布上的改动保留）；期间用户改 canvas.json 不算冲突。
func TestSealDesignFrames(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	ctx := context.Background()
	id := mkEmptyDesign(t, e, "thr_a")
	src := fakeFetch{}
	c := threadCaller("thr_a")
	c.Internal, c.Fetch, c.Turn = true, src.fetch, Turn{ID: "turn-1", Title: "做登录页"}

	files := sealFiles(src, map[string]string{
		"src/frames/login.tsx":  "export default () => <p>login</p>",
		"src/frames/signup.tsx": "export default () => <p>signup</p>",
		"src/theme.css":         "@theme{}",
		"dist/login.html":       "<p>login</p>",
		"dist/signup.html":      "<p>signup</p>",
		"dist/assets/app.js":    "console.log(1)",
		"dist/frames.json": framesJSON(
			`{"id":"login","name":"登录","width":390,"height":844,"device":"phone","links":["signup"]}`,
			`{"id":"signup","name":"注册","width":1280,"height":800,"device":"none","links":[]}`),
	})
	chk := &design.Check{Status: design.CheckIssues, Issues: []design.Issue{{Rule: "text-clipped", Frame: "login", Loc: "src/frames/login.tsx:1:1"}}}
	r := mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, 1, files, chk))
	v2 := sealVersion(t, r)
	assert.Equal(t, 2, v2)

	doc := loadDoc(t, e, id, v2)
	require.NotNil(t, doc.Design)
	require.Len(t, doc.Pages, 2)
	login, ok := doc.PageByID("login")
	require.True(t, ok)
	assert.Equal(t, "src/frames/login.tsx", login.Source)
	assert.Equal(t, "dist/login.html", login.File)
	assert.Equal(t, "登录", login.Name)
	assert.Equal(t, []float64{390, 844}, []float64{login.Width, login.Height})
	assert.Equal(t, canvas.DevicePhone, login.Device)
	signup, ok := doc.PageByID("signup")
	require.True(t, ok)
	assert.Equal(t, "dist/signup.html", signup.File)
	assert.Empty(t, signup.Device)
	assert.NotEqual(t, [2]float64{login.X, login.Y}, [2]float64{signup.X, signup.Y}, "自动排位，不叠在一起")
	assert.Equal(t, "<p>signup</p>", readFile(t, e.s, id, v2, "dist/signup.html"))

	v, err := e.s.Version(ctx, id, v2)
	require.NoError(t, err)
	assert.Equal(t, "turn-1", v.TurnID)
	assert.Equal(t, "做登录页", v.Title)
	var stored design.Check
	require.NoError(t, json.Unmarshal(v.Check, &stored))
	assert.Equal(t, design.CheckIssues, stored.Status)
	hash, err := e.s.sourceHash(ctx, id, v2)
	require.NoError(t, err)
	assert.Equal(t, hash, stored.Fingerprint)
	assert.Len(t, stored.Issues, 1)

	// 用户在画布上改了 login 的名字与位置（只动 canvas.json，落成 v3）
	user := threadCaller("thr_a")
	mustCall(t, e.s, user, OpUpdate, `{"id":"`+id+`","baseVersion":2,"canvas":[{"op":"set","id":"login","key":"name","value":"我的登录"},{"op":"set","id":"login","key":"x","value":5000}]}`)

	// 第二次封存仍以 v2 为底：删掉 signup、新增 home；canvas.json 不参与冲突检查
	files = sealFiles(src, map[string]string{
		"src/frames/signup.tsx": "",
		"dist/signup.html":      "",
		"src/frames/home.tsx":   "export default () => <p>home</p>",
		"dist/home.html":        "<p>home</p>",
		"dist/frames.json": framesJSON(
			`{"id":"home","name":"首页","width":1440,"height":900,"device":"browser","links":[]}`,
			`{"id":"login","name":"登录（新）","width":100,"height":100,"device":"tablet","links":[]}`),
	})
	r = mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, 2, files, nil))
	v4 := sealVersion(t, r)
	assert.Equal(t, 4, v4)
	doc = loadDoc(t, e, id, v4)
	require.Len(t, doc.Pages, 2)
	_, ok = doc.PageByID("signup")
	assert.False(t, ok, "消失的帧被移除")
	login, _ = doc.PageByID("login")
	assert.Equal(t, "我的登录", login.Name, "name 只在新增时取用")
	assert.Equal(t, float64(5000), login.X, "用户的位置保留")
	assert.Equal(t, []float64{390, 844}, []float64{login.Width, login.Height})
	assert.Equal(t, canvas.DevicePhone, login.Device)
	home, ok := doc.PageByID("home")
	require.True(t, ok)
	assert.Equal(t, "src/frames/home.tsx", home.Source)
	assert.Equal(t, "dist/home.html", home.File)
	assert.Equal(t, canvas.DeviceBrowser, home.Device)
	paths := filePaths(t, e, id, v4)
	assert.NotContains(t, paths, "dist/signup.html")
	assert.NotContains(t, paths, "src/frames/signup.tsx")
	v, err = e.s.Version(ctx, id, v4)
	require.NoError(t, err)
	assert.Nil(t, v.Check, "没给 check 不写")
}

func filePaths(t *testing.T, e *env, id string, version int) []string {
	t.Helper()
	files, err := e.s.Files(context.Background(), id, version)
	require.NoError(t, err)
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// M29 T4：src/**、dist/** 在 baseVersion 之后被改过就 conflict（State.Changed 列出路径）；只有 canvas.json 变了不冲突；
// 帧缺产物、frames.json 不合法、HTML 画布都被拒，且都不落版本。
func TestSealDesignConflict(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	id := mkEmptyDesign(t, e, "thr_a")
	src := fakeFetch{}
	c := threadCaller("thr_a")
	c.Internal, c.Fetch = true, src.fetch
	one := `{"id":"a","name":"A","width":800,"height":600,"device":"none","links":[]}`
	mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, 1, sealFiles(src, map[string]string{
		"src/frames/a.tsx": "a1", "dist/a.html": "<p>a1</p>", "dist/frames.json": framesJSON(one),
	}), nil))
	// 另一处（另一个线程的副本）在 v2 上又封存了一次，改了 src/frames/a.tsx
	mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, 2, sealFiles(src, map[string]string{
		"src/frames/a.tsx": "a2", "dist/a.html": "<p>a2</p>",
	}), nil))

	// 以 v2 为底、只改 src/theme.css：别处在 v2 之后改过 src/frames/a.tsx 与 dist/a.html → conflict
	_, err := agentCall(t, e.s, c, OpSealDesign, sealParams(t, id, 2, sealFiles(src, map[string]string{
		"src/theme.css": "x", "dist/a.html": "<p>mine</p>",
	}), nil))
	require.Equal(t, CodeConflict, code(err))
	oe, _ := AsOpError(err)
	require.NotNil(t, oe.State)
	assert.Equal(t, 3, oe.State.Version)
	assert.Equal(t, []string{"dist/a.html", "src/frames/a.tsx"}, oe.State.Changed)
	a, err := e.s.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, 3, a.Version, "冲突不落版本")

	// 用户只改了 canvas.json（v4）：以 v3 为底封存不冲突
	mustCall(t, e.s, threadCaller("thr_a"), OpUpdate, `{"id":"`+id+`","baseVersion":3,"canvas":[{"op":"set","id":"a","key":"x","value":10}]}`)
	r := mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, 3, sealFiles(src, map[string]string{"src/theme.css": "y"}), nil))
	assert.Equal(t, 5, sealVersion(t, r))

	// 帧缺产物
	_, err = agentCall(t, e.s, c, OpSealDesign, sealParams(t, id, 5, sealFiles(src, map[string]string{
		"dist/frames.json": framesJSON(one, `{"id":"b","name":"B","width":800,"height":600,"device":"none","links":[]}`),
	}), nil))
	assert.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "dist/b.html")
	// frames.json 不合法
	_, err = agentCall(t, e.s, c, OpSealDesign, sealParams(t, id, 5, sealFiles(src, map[string]string{"dist/frames.json": `[{"id":"Bad"}]`}), nil))
	assert.Equal(t, CodeInvalid, code(err))
	// 只能写 src/、dist/
	_, err = agentCall(t, e.s, c, OpSealDesign, sealParams(t, id, 5, sealFiles(src, map[string]string{"canvas.json": "{}"}), nil))
	assert.Equal(t, CodeInvalid, code(err))

	// HTML 画布（有画框、没有 design）不能封存
	html, err := e.s.Create(context.Background(), CreateParams{Title: "HTML 画布", ThreadID: "thr_a", Files: []FileChange{
		put(EntryCanvas, `{"format":1,"canvases":[{"id":"c","name":"页面 1"}],"pages":[{"id":"p","canvas":"c","name":"P","file":"p.html"}]}`),
		put("p.html", "<p>p</p>"),
	}})
	require.NoError(t, err)
	_, err = agentCall(t, e.s, c, OpSealDesign, sealParams(t, html.ID, 1, sealFiles(src, map[string]string{
		"src/frames/a.tsx": "a", "dist/a.html": "<p>a</p>", "dist/frames.json": framesJSON(one),
	}), nil))
	assert.Equal(t, CodeInvalid, code(err))
	a, err = e.s.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, 5, a.Version)
}

// M29 T4：内部操作只对经普通 orb 的 artifact.call 进来的调用方开放；设计画布的 src/、dist/ 不能用 update_artifact 写
// （files 与 edits 都拒），canvas.json 与其它文件照常；非设计画布不受限。
func TestSealDesignGuards(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	id := mkEmptyDesign(t, e, "thr_a")
	src := fakeFetch{}
	c := threadCaller("thr_a")
	c.Fetch = src.fetch
	for _, op := range []string{OpSealDesign, OpFetchDesignFiles, OpFetchDesignRuntime, OpDesignHead, OpExportDesignProject} {
		_, err := agentCall(t, e.s, c, op, `{"id":"`+id+`"}`)
		require.Equal(t, CodeInvalid, code(err), op)
		assert.Contains(t, err.Error(), "没有操作", op)
		assert.NotContains(t, err.Error(), op+"、", "不出现在可用操作列表里")
	}
	_, err := agentCall(t, e.s, c, "nope", `{}`)
	assert.Contains(t, err.Error(), OpCheck, "check_artifact 在可用操作列表里")

	internal := c
	internal.Internal = true
	one := `{"id":"a","name":"A","width":800,"height":600,"device":"none","links":[]}`
	mustCall(t, e.s, internal, OpSealDesign, sealParams(t, id, 1, sealFiles(src, map[string]string{
		"src/frames/a.tsx": "export default () => <p>a</p>", "dist/a.html": "<p>a</p>", "dist/frames.json": framesJSON(one),
	}), nil))

	for _, params := range []string{
		`{"id":"` + id + `","baseVersion":2,"files":[{"path":"src/frames/a.tsx","content":"x"}]}`,
		`{"id":"` + id + `","baseVersion":2,"files":[{"path":"dist/a.html","remove":true}]}`,
		`{"id":"` + id + `","baseVersion":2,"edits":[{"path":"src/frames/a.tsx","oldText":"<p>a</p>","newText":"<p>b</p>"}]}`,
	} {
		_, err := agentCall(t, e.s, c, OpUpdate, params)
		require.Equal(t, CodeInvalid, code(err), params)
		assert.Contains(t, err.Error(), "工作副本", params)
	}
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":2,"files":[{"path":"notes.md","content":"ok"}]}`)

	plain, err := e.s.Create(context.Background(), CreateParams{Title: "页", ThreadID: "thr_a", Files: []FileChange{put(EntryHTML, "<p>x</p>")}})
	require.NoError(t, err)
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+plain.ID+`","baseVersion":1,"files":[{"path":"src/x.ts","content":"ok"}]}`)

	// design_head：最新版本、check 原文与 src/、dist/ 摘要；HTML 页不是画布
	r := mustCall(t, e.s, internal, OpDesignHead, `{"id":"`+id+`"}`)
	var head orbsock.DesignHead
	require.NoError(t, json.Unmarshal([]byte(r.Text), &head))
	assert.Equal(t, 3, head.Version)
	var paths []string
	for _, f := range head.Files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"dist/a.html", "dist/frames.json", "src/frames/a.tsx"}, paths)
	assert.False(t, head.Aesthetic, "还没选方向：不跑审美规则")
	assert.Empty(t, head.Surface)
	// 选定方向后带上审美配置（与 ServeRender 注入的 window.__orynProbe 同口径）
	surface, direction := "app", `{"id":"calm"}`
	require.NoError(t, e.st.Q.SetArtifactDesignDirection(context.Background(), db.SetArtifactDesignDirectionParams{ArtifactID: id, Surface: &surface, Direction: &direction}))
	r = mustCall(t, e.s, internal, OpDesignHead, `{"id":"`+id+`"}`)
	head = orbsock.DesignHead{}
	require.NoError(t, json.Unmarshal([]byte(r.Text), &head))
	assert.True(t, head.Aesthetic)
	assert.Equal(t, "app", head.Surface)
	_, err = agentCall(t, e.s, internal, OpDesignHead, `{"id":"`+plain.ID+`"}`)
	assert.Equal(t, CodeInvalid, code(err))
}

// M29 T4：fetch_design_files 只给 src/**（不含 node_modules），单个文件跨页分片；每页经 link 送出的 artifact.call 结果
// 序列化后不超过 DesignFilesPageBytes；按 offset 拼起来逐字节一致。Runner orb 的线程用不了内部操作。
func TestDesignFilesPaging(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb(t, e, "orb_a", "")
	mkOrb(t, e, "orb_r", orbs.KindRunner)
	mkOrbThread(t, e, "thr_a", "orb_a", "")
	mkOrbThread(t, e, "thr_r", "orb_r", "")
	id := mkEmptyDesign(t, e, "thr_a")

	big := make([]byte, 3_500_000)
	_, err := rand.Read(big)
	require.NoError(t, err)
	big2 := make([]byte, 2_100_000)
	_, err = rand.Read(big2)
	require.NoError(t, err)
	want := map[string][]byte{
		"src/assets/hero.bin":  big,
		"src/assets/photo.bin": big2,
		"src/frames/a.tsx":     []byte("export default () => <p>a</p>"),
		"src/package.json":     []byte(`{"dependencies":{}}`),
		"src/empty.txt":        {},
	}
	var files []FileChange
	for p, b := range want {
		files = append(files, FileChange{Path: p, Data: bytes.NewReader(b)})
	}
	files = append(files, put("src/node_modules/x/index.js", "skip"), put("dist/a.html", "skip"))
	_, err = e.s.Write(ctx, WriteParams{ID: id, BaseVersion: 1, Files: files})
	require.NoError(t, err)

	links := fakeLinks{}
	e.s.RegisterOrbMethod(links, fakeSource{})
	fn := links[string(apigen.MethodArtifactCall)]
	call := func(orbID, thread, op string, params any) (any, error) {
		raw, err := json.Marshal(map[string]any{"threadId": thread, "op": op, "params": params})
		require.NoError(t, err)
		return fn(ctx, orbID, raw)
	}

	_, err = call("orb_r", "thr_r", OpFetchDesignFiles, map[string]any{"id": id})
	require.Error(t, err, "Runner 不是内部调用方")

	got := map[string][]byte{}
	cursor, pages := "", 0
	for {
		params := map[string]any{"id": id, "version": 2}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := call("orb_a", "thr_a", OpFetchDesignFiles, params)
		require.NoError(t, err)
		wire, err := json.Marshal(res)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(wire), orbsock.DesignFilesPageBytes, "第 %d 页", pages)
		var page orbsock.DesignFilesPage
		require.NoError(t, json.Unmarshal([]byte(res.(apigen.LinkCtlArtifactCallResult).Text), &page))
		assert.Equal(t, 2, page.Version)
		for _, c := range page.Files {
			data, err := base64.StdEncoding.DecodeString(c.Data)
			require.NoError(t, err)
			require.Equal(t, int64(len(got[c.Path])), c.Offset, "%s 的片按顺序接上", c.Path)
			if got[c.Path] == nil {
				got[c.Path] = []byte{}
			}
			got[c.Path] = append(got[c.Path], data...)
			assert.Equal(t, int64(len(want[c.Path])), c.Size)
		}
		pages++
		if page.Next == "" {
			break
		}
		require.Less(t, pages, 20)
		cursor = page.Next
	}
	assert.GreaterOrEqual(t, pages, 3, "5.6 MiB 的二进制要分 3 页以上")
	require.Len(t, got, len(want))
	for p, b := range want {
		assert.True(t, bytes.Equal(b, got[p]), "%s 逐字节一致", p)
	}

	// 坏游标与越界游标
	for _, cur := range []string{"x", "-1:0", "99:0", fmt.Sprintf("0:%d", len(big)+1)} {
		_, err := call("orb_a", "thr_a", OpFetchDesignFiles, map[string]any{"id": id, "version": 2, "cursor": cur})
		assert.Error(t, err, cur)
	}

	// fetch_design_runtime 是空构建时返回空列表（缺哪个就不返回哪个）
	res, err := call("orb_a", "thr_a", OpFetchDesignRuntime, map[string]any{})
	require.NoError(t, err)
	var rt orbsock.DesignRuntimeFiles
	require.NoError(t, json.Unmarshal([]byte(res.(apigen.LinkCtlArtifactCallResult).Text), &rt))
	for _, f := range rt.Files {
		assert.Contains(t, []string{"runtime.js", "probe.js"}, f.Name)
	}
}

// M29 D3：export_design_project 校验调用方范围（跨项目/跨线程拒访，Runner 不可调用）、按 DesignFilesPageBytes
// 分片返回 ExportProject 生成的工程 zip（逐字节与 sha256 一致）、非设计画布与缺工程锁原样返回 Export 错误。
func TestExportDesignProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb(t, e, "orb_a", "")
	mkOrb(t, e, "orb_b", "")
	mkOrb(t, e, "orb_r", orbs.KindRunner)
	mkProject(t, e, "proj_a")
	mkProject(t, e, "proj_b")
	mkOrbThread(t, e, "thr_a", "orb_a", "proj_a")
	mkOrbThread(t, e, "thr_b", "orb_b", "proj_b")
	mkOrbThread(t, e, "thr_r", "orb_r", "proj_a")

	doc := canvas.Doc{Design: &canvas.Design{Engine: canvas.DesignEngine}, Canvases: []canvas.Canvas{{ID: canvas.DefaultCanvasID, Name: canvas.DefaultCanvasName}}}
	created, err := e.s.Create(ctx, CreateParams{
		Title: "设计", ThreadID: "thr_a", ProjectID: "proj_a",
		Files: []FileChange{put(EntryCanvas, string(doc.JSON()))},
	})
	require.NoError(t, err)
	id := created.ID
	// 塞入 3.5 MiB 不可压缩随机数据，保证工程 zip 跨越 3 页以上
	big := make([]byte, 3_500_000)
	_, err = rand.Read(big)
	require.NoError(t, err)
	_, err = e.s.Write(ctx, WriteParams{
		ID: id, BaseVersion: 1,
		Files: []FileChange{
			put("src/frames/home.tsx", "export default function Home(){ return <main>hello</main>; }"),
			{Path: "src/assets/hero.bin", Data: bytes.NewReader(big)},
		},
	})
	require.NoError(t, err)

	links := fakeLinks{}
	e.s.RegisterOrbMethod(links, fakeSource{})
	fn := links[string(apigen.MethodArtifactCall)]
	call := func(orbID, thread, op string, params any) (any, error) {
		raw, err := json.Marshal(map[string]any{"threadId": thread, "op": op, "params": params})
		require.NoError(t, err)
		return fn(ctx, orbID, raw)
	}
	linkCode := func(err error) string {
		if le, ok := err.(*link.CtlError); ok {
			return le.Code
		}
		return code(err)
	}

	// 跨项目拒访 & Runner 不是内部调用方
	_, err = call("orb_b", "thr_b", OpExportDesignProject, map[string]any{"id": id})
	require.Error(t, err)
	assert.Equal(t, CodeNotFound, linkCode(err))
	_, err = call("orb_r", "thr_r", OpExportDesignProject, map[string]any{"id": id})
	require.Error(t, err)
	assert.Equal(t, CodeInvalid, linkCode(err))
	// 分页拉取全部字节，确认每页外送序列化不超过 DesignFilesPageBytes
	var (
		got      []byte
		wantSize int64
		wantSha  string
		cursor   string
		pages    int
	)
	for {
		params := map[string]any{"id": id}
		if pages > 0 {
			params["version"] = 2
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := call("orb_a", "thr_a", OpExportDesignProject, params)
		require.NoError(t, err)
		wire, err := json.Marshal(res)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(wire), orbsock.DesignFilesPageBytes, "第 %d 页", pages)

		var page orbsock.DesignProjectPage
		require.NoError(t, json.Unmarshal([]byte(res.(apigen.LinkCtlArtifactCallResult).Text), &page))
		assert.Equal(t, 2, page.Version)
		assert.Equal(t, int64(len(got)), page.Offset)
		if pages == 0 {
			wantSize = page.Size
			wantSha = page.Sha256
		} else {
			assert.Equal(t, wantSize, page.Size)
			assert.Equal(t, wantSha, page.Sha256)
		}
		chunk, err := base64.StdEncoding.DecodeString(page.Data)
		require.NoError(t, err)
		got = append(got, chunk...)
		pages++
		if page.Next == "" {
			break
		}
		require.Less(t, pages, 20)
		cursor = page.Next
	}
	assert.GreaterOrEqual(t, pages, 3, "3.5 MiB 随机数据应分 3 页以上")
	assert.Equal(t, wantSize, int64(len(got)))
	sum := sha256.Sum256(got)
	assert.Equal(t, wantSha, hex.EncodeToString(sum[:]))

	// 与直接调用 Export(..., ExportProject) 的 zip 逐字节一致
	exp, err := e.s.Export(ctx, id, 2, ExportProject)
	require.NoError(t, err)
	var direct bytes.Buffer
	require.NoError(t, exp.Write(&direct))
	assert.True(t, bytes.Equal(direct.Bytes(), got), "分片导出结果应与 ExportProject 逐字节一致")

	// 坏游标、越界游标与负版本
	for _, params := range []map[string]any{
		{"id": id, "version": -1},
		{"id": id, "version": 2, "cursor": "abc"},
		{"id": id, "version": 2, "cursor": "-1"},
		{"id": id, "version": 2, "cursor": fmt.Sprintf("%d", wantSize)},
		{"id": id, "version": 2, "cursor": fmt.Sprintf("%d", wantSize+1)},
	} {
		_, err := call("orb_a", "thr_a", OpExportDesignProject, params)
		require.Error(t, err, params)
		assert.Equal(t, CodeInvalid, linkCode(err), params)
	}

	// 非设计画布（HTML 页面与普通 HTML 画布）报 invalid
	plain, err := e.s.Create(ctx, CreateParams{Title: "普通页", ThreadID: "thr_a", ProjectID: "proj_a", Files: []FileChange{put(EntryHTML, "<p>x</p>")}})
	require.NoError(t, err)
	_, err = call("orb_a", "thr_a", OpExportDesignProject, map[string]any{"id": plain.ID})
	require.Error(t, err)
	assert.Equal(t, CodeInvalid, linkCode(err))

	htmlCanvas, err := e.s.Create(ctx, CreateParams{Title: "HTML 画布", ThreadID: "thr_a", ProjectID: "proj_a", Files: []FileChange{
		put(EntryCanvas, string(canvas.Doc{Canvases: []canvas.Canvas{{ID: canvas.DefaultCanvasID, Name: canvas.DefaultCanvasName}}}.JSON())),
	}})
	require.NoError(t, err)
	_, err = call("orb_a", "thr_a", OpExportDesignProject, map[string]any{"id": htmlCanvas.ID})
	require.Error(t, err)
	assert.Equal(t, CodeInvalid, linkCode(err))

	// 有额外依赖但缺少工程锁：原样返回 Export 的 invalid 错误
	_, err = e.s.Write(ctx, WriteParams{
		ID: id, BaseVersion: 2,
		Files: []FileChange{put("src/package.json", `{"dependencies":{"recharts":"^2.12.0"}}`)},
	})
	require.NoError(t, err)
	_, err = call("orb_a", "thr_a", OpExportDesignProject, map[string]any{"id": id, "version": 3})
	require.Error(t, err)
	assert.Equal(t, CodeInvalid, linkCode(err))
	assert.Contains(t, err.Error(), "缺少工程锁")
}

// M34 S4：封存按 src/variants.json 同步方案组——新组每个方案一列、放在第一个画布页现有画框下方；撞了 id 加 -2 后缀的帧
// 映射到对应画框；已有方案里的新帧接在该方案最下面；不在方案里的新帧照旧接在右边；重封存保留 picked，选中的方案从
// 清单删掉后 picked 置 null；清单不合法或组 id 撞上标注回 invalid；删掉清单就删掉组。
func TestSealDesignVariants(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	ctx := context.Background()
	id := mkEmptyDesign(t, e, "thr_a")
	src := fakeFetch{}
	c := threadCaller("thr_a")
	c.Internal, c.Fetch = true, src.fetch
	frame := func(id string, w, h int) string {
		return fmt.Sprintf(`{"id":%q,"name":%q,"width":%d,"height":%d,"device":"none","links":[]}`, id, id, w, h)
	}
	login := frame("login", 1280, 800)
	r := mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, 1, sealFiles(src, map[string]string{
		"src/frames/login.tsx": "login", "dist/login.html": "<p>login</p>", "dist/frames.json": framesJSON(login),
	}), nil))
	require.Equal(t, 2, sealVersion(t, r))
	// 用户在画布上贴了一张 id 为 hero 的便签：之后的帧 hero 只能用 hero-2
	user := threadCaller("thr_a")
	mustCall(t, e.s, user, OpUpdate, `{"id":"`+id+`","baseVersion":2,"canvas":[{"op":"add","object":{"type":"note","id":"hero","kind":"sticky","canvas":"canvas-1","x":10,"y":10,"text":"t"}}]}`)

	all := []string{login, frame("home-a", 400, 800), frame("home-b", 600, 800), frame("pricing-b", 700, 300),
		frame("home-c", 500, 800), frame("hero", 390, 844), frame("about", 800, 600)}
	files := map[string]string{
		"dist/frames.json":    framesJSON(all...),
		"src/themes/calm.css": `:root[data-oryn-theme="calm"]{--color-accent:#0f766e}`,
		"src/variants.json": `{"groups":[{"id":"home","name":"首页首屏","variants":[
			{"id":"a","name":"冷静","theme":"calm","frames":["home-a"]},
			{"id":"b","name":"暖调","frames":["home-b","pricing-b"]},
			{"id":"c","name":"杂志","frames":["home-c","hero"]}]}]}`,
	}
	for _, f := range []string{"home-a", "home-b", "pricing-b", "home-c", "hero", "about"} {
		files["src/frames/"+f+".tsx"], files["dist/"+f+".html"] = f, "<p>"+f+"</p>"
	}
	r = mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, 3, sealFiles(src, files), nil))
	v := sealVersion(t, r)
	doc := loadDoc(t, e, id, v)
	pos := func(doc canvas.Doc, id string) [3]any {
		p, ok := doc.PageByID(id)
		require.True(t, ok, id)
		return [3]any{p.Canvas, p.X, p.Y}
	}
	// 上边缘 = login 的下边缘 800 + 48*4；列宽取该方案最宽的帧，列间距 96，列内间距 48
	top := 800.0 + canvas.PageGap*4
	assert.Equal(t, [3]any{"canvas-1", 0.0, top}, pos(doc, "home-a"))
	assert.Equal(t, [3]any{"canvas-1", 496.0, top}, pos(doc, "home-b"))
	assert.Equal(t, [3]any{"canvas-1", 496.0, top + 800 + 48}, pos(doc, "pricing-b"))
	assert.Equal(t, [3]any{"canvas-1", 1292.0, top}, pos(doc, "home-c"))
	assert.Equal(t, [3]any{"canvas-1", 1292.0, top + 800 + 48}, pos(doc, "hero-2"))
	assert.Equal(t, [3]any{"canvas-1", 1840.0, 0.0}, pos(doc, "about"), "不在方案里的新帧照旧接在右边")
	hero, _ := doc.PageByID("hero-2")
	assert.Equal(t, "src/frames/hero.tsx", hero.Source)
	require.Len(t, doc.Groups, 1)
	g := doc.Groups[0]
	assert.Equal(t, "home", g.ID)
	assert.Equal(t, "canvas-1", g.Canvas)
	assert.Equal(t, "首页首屏", g.Name)
	assert.Nil(t, g.Picked)
	pages := func(g canvas.Group) [][]string {
		var out [][]string
		for _, v := range g.Variants {
			out = append(out, v.Pages)
		}
		return out
	}
	assert.Equal(t, [][]string{{"home-a"}, {"home-b", "pricing-b"}, {"home-c", "hero-2"}}, pages(g), "帧 hero 映射到画框 hero-2")
	assert.Equal(t, "calm", g.Variants[0].Theme)

	// 已有方案里的新帧接在该方案最下面那个画框的下方；改组名
	all = append(all, frame("home-a2", 400, 500))
	r = mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, v, sealFiles(src, map[string]string{
		"src/frames/home-a2.tsx": "a2", "dist/home-a2.html": "<p>a2</p>", "dist/frames.json": framesJSON(all...),
		"src/variants.json": `{"groups":[{"id":"home","name":"首页","variants":[
			{"id":"a","name":"冷静","theme":"calm","frames":["home-a","home-a2"]},
			{"id":"b","name":"暖调","frames":["home-b","pricing-b"]},
			{"id":"c","name":"杂志","frames":["home-c","hero"]}]}]}`,
	}), nil))
	v = sealVersion(t, r)
	doc = loadDoc(t, e, id, v)
	assert.Equal(t, [3]any{"canvas-1", 0.0, top + 800 + 48}, pos(doc, "home-a2"))
	assert.Equal(t, "首页", doc.Groups[0].Name)

	// 用户选定 b；重封存（改了 b 的说明）后 picked 保留
	r = mustCall(t, e.s, user, OpUpdate, `{"id":"`+id+`","baseVersion":`+fmt.Sprint(v)+`,"canvas":[{"op":"set","id":"home","key":"picked","value":"b"}]}`)
	a, err := e.s.Get(ctx, id)
	require.NoError(t, err)
	v = a.Version
	require.Equal(t, "b", *loadDoc(t, e, id, v).Groups[0].Picked)
	r = mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, v, sealFiles(src, map[string]string{
		"src/variants.json": `{"groups":[{"id":"home","name":"首页","variants":[
			{"id":"a","name":"冷静","theme":"calm","frames":["home-a","home-a2"]},
			{"id":"b","name":"暖调","note":"杂志感更强","frames":["home-b","pricing-b"]},
			{"id":"c","name":"杂志","frames":["home-c","hero"]}]}]}`,
	}), nil))
	v = sealVersion(t, r)
	g = loadDoc(t, e, id, v).Groups[0]
	require.NotNil(t, g.Picked)
	assert.Equal(t, "b", *g.Picked)
	assert.Equal(t, "杂志感更强", g.Variants[1].Note)

	// 选中的方案从清单删掉：picked 置 null，它的画框留在画布上、不再属于方案
	r = mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, v, sealFiles(src, map[string]string{
		"src/variants.json": `{"groups":[{"id":"home","name":"首页","variants":[
			{"id":"a","name":"冷静","theme":"calm","frames":["home-a","home-a2"]},
			{"id":"c","name":"杂志","frames":["home-c","hero"]}]}]}`,
	}), nil))
	v = sealVersion(t, r)
	doc = loadDoc(t, e, id, v)
	g = doc.Groups[0]
	assert.Nil(t, g.Picked)
	assert.Nil(t, g.PickedBy)
	assert.Equal(t, [][]string{{"home-a", "home-a2"}, {"home-c", "hero-2"}}, pages(g))
	_, ok := doc.PageByID("home-b")
	assert.True(t, ok)

	// 清单不合法：invalid，信息带文件与出错位置，不落版本
	_, err = agentCall(t, e.s, c, OpSealDesign, sealParams(t, id, v, sealFiles(src, map[string]string{
		"src/variants.json": `{"groups":[{"id":"home","name":"首页","variants":[
			{"id":"a","name":"冷静","frames":["nope"]},{"id":"c","name":"杂志","frames":["home-c"]}]}]}`,
	}), nil))
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "src/variants.json")
	assert.Contains(t, err.Error(), "groups[0].variants[0].frames[0]")
	// 组 id 撞上便签 hero
	_, err = agentCall(t, e.s, c, OpSealDesign, sealParams(t, id, v, sealFiles(src, map[string]string{
		"src/variants.json": `{"groups":[{"id":"hero","name":"首页","variants":[
			{"id":"a","name":"冷静","frames":["home-a"]},{"id":"c","name":"杂志","frames":["home-c"]}]}]}`,
	}), nil))
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "groups[0].id")
	a, err = e.s.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, v, a.Version)

	// 删掉清单：组跟着删，画框保留
	r = mustCall(t, e.s, c, OpSealDesign, sealParams(t, id, v, sealFiles(src, map[string]string{"src/variants.json": ""}), nil))
	doc = loadDoc(t, e, id, sealVersion(t, r))
	assert.Empty(t, doc.Groups)
	assert.Len(t, doc.Pages, 8)
}
