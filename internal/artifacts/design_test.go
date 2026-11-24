package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
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
	for _, op := range []string{OpSealDesign, OpFetchDesignFiles, OpFetchDesignRuntime, OpDesignHead} {
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
