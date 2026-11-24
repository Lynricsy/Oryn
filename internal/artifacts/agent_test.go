package artifacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func agentCall(t *testing.T, s *Service, c Caller, op, params string) (Result, error) {
	t.Helper()
	return s.Call(context.Background(), c, op, json.RawMessage(params))
}

func mustCall(t *testing.T, s *Service, c Caller, op, params string) Result {
	t.Helper()
	r, err := agentCall(t, s, c, op, params)
	require.NoError(t, err)
	return r
}

// header 解出 read_artifact 结果的第一行 JSON 与正文。
func header(t *testing.T, r Result) (map[string]any, string) {
	t.Helper()
	line, body, ok := strings.Cut(r.Text, "\n\n")
	require.True(t, ok, r.Text)
	var h map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &h))
	_, hasText := h["text"]
	assert.False(t, hasText)
	return h, body
}

func textObj(t *testing.T, r Result) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Text), &m))
	return m
}

func threadCaller(id string) Caller {
	return Caller{ThreadID: id, Label: "线程 " + id, Scope: ScopeThread}
}

func mkProject(t *testing.T, e *env, id string) {
	t.Helper()
	require.NoError(t, e.st.Q.CreateProject(context.Background(), db.CreateProjectParams{ID: id, Name: id, CreatedAt: 1, UpdatedAt: 1}))
}

func TestAgentCreateRead(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	r := mustCall(t, e.s, c, OpCreate, `{"title":"报告","html":"<h1>hi</h1>","note":"初稿"}`)
	out := textObj(t, r)
	id := out["id"].(string)
	assert.True(t, ValidID(id))
	assert.EqualValues(t, 1, out["version"])
	assert.Equal(t, KindWeb, out["kind"])
	assert.Equal(t, "http://localhost:7600/artifacts/"+id, out["url"])
	d := r.Details.(map[string]any)
	assert.Equal(t, map[string]any{"op": OpCreate, "id": id, "title": "报告", "kind": KindWeb, "version": 1}, d)

	a, err := e.s.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "thr_a", a.ThreadID)
	assert.Equal(t, OriginAgent, a.Origin)
	v, err := e.s.Version(context.Background(), id, 0)
	require.NoError(t, err)
	assert.Equal(t, "thr_a", v.AuthorThreadID)
	assert.Equal(t, "线程 thr_a", v.AuthorLabel)

	rr := mustCall(t, e.s, c, OpRead, `{"id":"`+id+`"}`)
	h, body := header(t, rr)
	assert.Equal(t, "<h1>hi</h1>", body)
	assert.Equal(t, "index.html", h["path"])
	assert.EqualValues(t, 1, h["head"])
	assert.Nil(t, h["nextOffset"])
	files := h["files"].([]any)
	require.Len(t, files, 1)
	assert.Equal(t, "index.html", files[0].(map[string]any)["path"])

	// html 与 files 都不给、未知字段、未知操作。
	_, err = agentCall(t, e.s, c, OpCreate, `{"title":"空"}`)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = agentCall(t, e.s, c, OpCreate, `{"title":"x","html":"a","bogus":1}`)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = agentCall(t, e.s, c, "drop_tables", `{}`)
	assert.Equal(t, CodeInvalid, code(err))
	// content 与 source 都给 / 都不给；内联超限。
	_, err = agentCall(t, e.s, c, OpCreate, `{"title":"x","files":[{"path":"index.html"}]}`)
	assert.Equal(t, CodeInvalid, code(err))
	big, _ := json.Marshal(strings.Repeat("a", MaxInlineBytes+1))
	_, err = agentCall(t, e.s, c, OpCreate, `{"title":"x","html":`+string(big)+`}`)
	assert.Equal(t, CodeTooLarge, code(err))
	// 能力只能是 db、files、sample；只有 ScopeAll 能指定 projectId。
	_, err = agentCall(t, e.s, c, OpCreate, `{"title":"x","html":"a","capabilities":["net"]}`)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = agentCall(t, e.s, c, OpCreate, `{"title":"x","html":"a","projectId":"prj_x"}`)
	assert.Equal(t, CodeInvalid, code(err))
}

func TestAgentReadPaging(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"长","files":[{"path":"index.html","content":"<p>x</p>"},{"path":"notes.txt","content":"一二三四五六七"}]}`))["id"].(string)

	h, body := header(t, mustCall(t, e.s, c, OpRead, `{"id":"`+id+`","path":"notes.txt","offset":2,"limit":3}`))
	assert.Equal(t, "三四五", body)
	assert.EqualValues(t, 5, h["nextOffset"])
	h, body = header(t, mustCall(t, e.s, c, OpRead, `{"id":"`+id+`","path":"notes.txt","offset":5}`))
	assert.Equal(t, "六七", body)
	assert.Nil(t, h["nextOffset"])
	_, err := agentCall(t, e.s, c, OpRead, `{"id":"`+id+`","path":"notes.txt","offset":8}`)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = agentCall(t, e.s, c, OpRead, `{"id":"`+id+`","path":"missing.txt"}`)
	assert.Equal(t, CodeInvalid, code(err))
}

func TestAgentUpdateEdits(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"页","html":"<h1>Hello</h1><p>one two two</p>"}`))["id"].(string)

	// 链式替换：第二条在第一条的结果上匹配。
	r := mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,"edits":[{"oldText":"Hello","newText":"Hi there"},{"oldText":"Hi there</h1>","newText":"Hi all</h1>"}],"note":"改标题"}`)
	assert.EqualValues(t, 2, textObj(t, r)["version"])
	assert.Equal(t, map[string]any{"op": OpUpdate, "id": id, "title": "页", "kind": KindWeb, "version": 2, "fromVersion": 1}, r.Details)
	assert.Equal(t, "<h1>Hi all</h1><p>one two two</p>", readFile(t, e.s, id, 0, "index.html"))

	// 0 次匹配 → edit_not_found；2 次 → invalid；失败时 head 不变（前面成功的 edit 也不落）。
	_, err := agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":2,"edits":[{"oldText":"one","newText":"1"},{"oldText":"nothing","newText":"x"}]}`)
	assert.Equal(t, CodeEditNotFound, code(err))
	_, err = agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":2,"edits":[{"oldText":"one","newText":"1"},{"oldText":"two","newText":"2"}]}`)
	assert.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "2 次")
	a, err := e.s.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, 2, a.Version)

	// files 先于 edits：edits 作用在本次写入的新内容上；新文件也能被 edits 改。
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":2,"files":[{"path":"style.css","content":"h1{color:red}"}],"edits":[{"path":"style.css","oldText":"red","newText":"blue"},{"oldText":"one","newText":"1"}]}`)
	assert.Equal(t, "h1{color:blue}", readFile(t, e.s, id, 0, "style.css"))
	assert.Equal(t, "<h1>Hi all</h1><p>1 two two</p>", readFile(t, e.s, id, 0, "index.html"))

	// 删除文件；edits 不能改被删的文件；没有 baseVersion、空改动、未知能力、canvas 都被拒。
	_, err = agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":3,"files":[{"path":"style.css","remove":true}],"edits":[{"path":"style.css","oldText":"blue","newText":"x"}]}`)
	assert.Equal(t, CodeInvalid, code(err))
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":3,"files":[{"path":"style.css","remove":true}]}`)
	files, err := e.s.Files(context.Background(), id, 0)
	require.NoError(t, err)
	require.Len(t, files, 1)
	for _, p := range []string{
		`{"id":"` + id + `","html":"x"}`,
		`{"id":"` + id + `","baseVersion":4}`,
		`{"id":"` + id + `","baseVersion":4,"capabilities":["net"]}`,
		`{"id":"` + id + `","baseVersion":4,"canvas":[]}`,
		`{"id":"` + id + `","baseVersion":9,"html":"x"}`,
	} {
		_, err = agentCall(t, e.s, c, OpUpdate, p)
		assert.Equal(t, CodeInvalid, code(err), p)
	}

	// 过期的 baseVersion 改被改过的文件 → conflict。
	_, err = agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,"html":"<p>old</p>"}`)
	assert.Equal(t, CodeConflict, code(err))
}

func TestAgentRenameOnly(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"旧名","html":"<p>x</p>"}`))["id"].(string)

	r := mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","title":"新名"}`)
	assert.EqualValues(t, 1, textObj(t, r)["version"])
	assert.Equal(t, map[string]any{"op": OpUpdate, "id": id, "title": "新名", "kind": KindWeb, "version": 1}, r.Details)
	vs, err := e.s.Versions(context.Background(), id)
	require.NoError(t, err)
	assert.Len(t, vs, 1)

	_, err = agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","title":"再改","baseVersion":1,"html":"<p>y</p>"}`)
	assert.Equal(t, CodeInvalid, code(err))
}

// fakeFetch 从内存 map 取字节。
type fakeFetch map[string]string

func (f fakeFetch) fetch(_ context.Context, path string) (io.ReadCloser, int64, error) {
	s, ok := f[path]
	if !ok {
		return nil, 0, opErr(CodeInvalid, "取不到 source %s。", path)
	}
	return io.NopCloser(strings.NewReader(s)), int64(len(s)), nil
}

func TestAgentSource(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	src := fakeFetch{"/work/out/page.html": "<h1>from orb</h1>", "/work/out/app.js": "console.log(1)"}
	c := threadCaller("thr_a")
	c.Fetch = src.fetch

	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"源","files":[{"path":"index.html","source":"/work/out/page.html"},{"path":"app.js","source":"/work/out/app.js"}]}`))["id"].(string)
	assert.Equal(t, "<h1>from orb</h1>", readFile(t, e.s, id, 0, "index.html"))
	assert.Equal(t, "console.log(1)", readFile(t, e.s, id, 0, "app.js"))

	// edits 作用在 source 取回的内容上。
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,"files":[{"path":"index.html","source":"/work/out/page.html"}],"edits":[{"oldText":"orb","newText":"Oryn"}]}`)
	assert.Equal(t, "<h1>from Oryn</h1>", readFile(t, e.s, id, 0, "index.html"))

	// 取不到的 source：错误原样回来，不落版本。
	_, err := agentCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":2,"files":[{"path":"b.js","source":"/nope"}]}`)
	assert.Equal(t, CodeInvalid, code(err))
	a, err := e.s.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, 2, a.Version)

	// 没有 Fetch 的调用方不接受 source。
	_, err = agentCall(t, e.s, threadCaller("thr_a"), OpCreate, `{"title":"x","files":[{"path":"index.html","source":"/work/out/page.html"}]}`)
	assert.Equal(t, CodeInvalid, code(err))
}

func TestAgentScope(t *testing.T) {
	e := newEnv(t)
	mkProject(t, e, "prj_1")
	mkProject(t, e, "prj_2")
	mkThread(t, e.st, "thr_a", "prj_1")
	mkThread(t, e.st, "thr_b", "prj_1")
	mkThread(t, e.st, "thr_c", "prj_2")
	mkThread(t, e.st, "thr_x", "")
	a := Caller{ThreadID: "thr_a", Label: "A", ProjectID: new("prj_1"), Scope: ScopeProject}
	bProject := Caller{ThreadID: "thr_b", Label: "B", ProjectID: new("prj_1"), Scope: ScopeProject}
	bThread := Caller{ThreadID: "thr_b", Label: "B", ProjectID: new("prj_1"), Scope: ScopeThread}
	cProject := Caller{ThreadID: "thr_c", Label: "C", ProjectID: new("prj_2"), Scope: ScopeProject}
	all := Caller{ThreadID: "thr_x", Label: "助手", Scope: ScopeAll}

	id := textObj(t, mustCall(t, e.s, a, OpCreate, `{"title":"A 的页","html":"<p>a</p>"}`))["id"].(string)
	got, err := e.s.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "prj_1", got.ProjectID)

	// ScopeThread：别的线程的读不到；ScopeProject：同项目读得到、别的项目读不到；ScopeAll 都读得到。
	for _, op := range []string{OpRead, OpListVersions, OpDelete} {
		_, err = agentCall(t, e.s, bThread, op, `{"id":"`+id+`"}`)
		assert.Equal(t, CodeNotFound, code(err), op)
		_, err = agentCall(t, e.s, cProject, op, `{"id":"`+id+`"}`)
		assert.Equal(t, CodeNotFound, code(err), op)
	}
	mustCall(t, e.s, bProject, OpRead, `{"id":"`+id+`"}`)
	mustCall(t, e.s, all, OpRead, `{"id":"`+id+`"}`)

	list := func(c Caller) []any { return textObj(t, mustCall(t, e.s, c, OpList, `{}`))["artifacts"].([]any) }
	assert.Len(t, list(bThread), 0)
	assert.Len(t, list(cProject), 0)
	require.Len(t, list(bProject), 1)
	assert.Equal(t, "thr_a", list(bProject)[0].(map[string]any)["thread"])

	// 写过版本的线程在 ScopeThread 下也看得到。
	mustCall(t, e.s, bProject, OpUpdate, `{"id":"`+id+`","baseVersion":1,"html":"<p>b</p>"}`)
	mustCall(t, e.s, bThread, OpRead, `{"id":"`+id+`"}`)
	assert.Len(t, list(bThread), 1)

	// 没有项目的 ScopeProject 退化成 ScopeThread；ScopeAll 能指定项目。
	noProj := Caller{ThreadID: "thr_c", Label: "C", Scope: ScopeProject}
	_, err = agentCall(t, e.s, noProj, OpRead, `{"id":"`+id+`"}`)
	assert.Equal(t, CodeNotFound, code(err))
	id2 := textObj(t, mustCall(t, e.s, all, OpCreate, `{"title":"助手的页","html":"<p>x</p>","projectId":"prj_2"}`))["id"].(string)
	mustCall(t, e.s, cProject, OpRead, `{"id":"`+id2+`"}`)
	_, err = agentCall(t, e.s, all, OpCreate, `{"title":"x","html":"<p>x</p>","projectId":"prj_none"}`)
	assert.Equal(t, CodeInvalid, code(err))

	// list 的 query 大小写不敏感。
	mustCall(t, e.s, a, OpCreate, `{"title":"Weekly Report","html":"<p>r</p>"}`)
	got2 := textObj(t, mustCall(t, e.s, bProject, OpList, `{"query":"weekly"}`))["artifacts"].([]any)
	require.Len(t, got2, 1)
	assert.Equal(t, "Weekly Report", got2[0].(map[string]any)["title"])
}

func TestAgentDeleteVersionsRestore(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	c := threadCaller("thr_a")
	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"版","html":"<p>v1</p>"}`))["id"].(string)
	mustCall(t, e.s, c, OpUpdate, `{"id":"`+id+`","baseVersion":1,"html":"<p>v2</p>","note":"第二版"}`)

	vs := textObj(t, mustCall(t, e.s, c, OpListVersions, `{"id":"`+id+`"}`))["versions"].([]any)
	require.Len(t, vs, 2)
	assert.Equal(t, "第二版", vs[0].(map[string]any)["note"])
	assert.Equal(t, "线程 thr_a", vs[0].(map[string]any)["author"])

	r := mustCall(t, e.s, c, OpRestoreVersion, `{"id":"`+id+`","version":1,"baseVersion":2}`)
	assert.EqualValues(t, 3, textObj(t, r)["version"])
	assert.Equal(t, map[string]any{"op": OpRestoreVersion, "id": id, "title": "版", "kind": KindWeb, "version": 3, "fromVersion": 1}, r.Details)
	assert.Equal(t, "<p>v1</p>", readFile(t, e.s, id, 0, "index.html"))
	_, err := agentCall(t, e.s, c, OpRestoreVersion, `{"id":"`+id+`","version":1}`)
	assert.Equal(t, CodeInvalid, code(err))

	d := mustCall(t, e.s, c, OpDelete, `{"id":"`+id+`"}`)
	assert.NotEmpty(t, textObj(t, d)["purgeAt"])
	assert.Equal(t, OpDelete, d.Details.(map[string]any)["op"])
	_, err = agentCall(t, e.s, c, OpRead, `{"id":"`+id+`"}`)
	assert.Equal(t, CodeDeleted, code(err))
	assert.Len(t, textObj(t, mustCall(t, e.s, c, OpList, `{}`))["artifacts"].([]any), 0)
}

func TestAgentGuide(t *testing.T) {
	e := newEnv(t)
	r := mustCall(t, e.s, Caller{Scope: ScopeAll}, OpReadGuide, `{"topic":"files"}`)
	assert.Contains(t, r.Text, "https://cdnjs.cloudflare.com")
	assert.Contains(t, r.Text, "baseVersion")
	r = mustCall(t, e.s, Caller{Scope: ScopeAll}, OpReadGuide, `{"topic":"canvas"}`)
	assert.Contains(t, r.Text, "canvas.json")
	assert.Contains(t, r.Text, "data-oryn-params")
	assert.Contains(t, r.Text, "copyOf")
	assert.NotContains(t, r.Text, "%!") // Sprintf 的参数与占位符对得上
	r = mustCall(t, e.s, Caller{Scope: ScopeAll}, OpReadGuide, `{"topic":"runtime"}`)
	assert.Contains(t, r.Text, "必须处理 null")
	assert.Contains(t, r.Text, "baseVersion: 0")
	assert.NotContains(t, r.Text, "%!")
	_, err := agentCall(t, e.s, Caller{Scope: ScopeAll}, OpReadGuide, `{"topic":"nope"}`)
	assert.Equal(t, CodeInvalid, code(err))
}

// fakeLinks 记下注册的 ctl 请求处理器。
type fakeLinks map[string]orbs.RequestFunc

func (f fakeLinks) HandleRequest(method string, fn orbs.RequestFunc) { f[method] = fn }

// fakeSource 按 orb 与路径取字节。
type fakeSource map[string]string

func (f fakeSource) Fetch(_ context.Context, orbID, path string) (io.ReadCloser, int64, error) {
	s, ok := f[orbID+":"+path]
	if !ok {
		return nil, 0, &link.CtlError{Code: "not_found", Message: "no such file"}
	}
	return io.NopCloser(strings.NewReader(s)), int64(len(s)), nil
}

func mkOrb(t *testing.T, e *env, id, kind string) {
	t.Helper()
	p := db.CreateOrbParams{ID: id, ProviderID: "prov_1", Type: "small", State: orbs.StateReady, StateChangedAt: 1, TokenHash: "h-" + id, CreatedAt: 1}
	if kind != "" {
		p.Kind = &kind
	}
	require.NoError(t, e.st.Q.CreateOrb(context.Background(), p))
}

func mkOrbThread(t *testing.T, e *env, id, orbID, projectID string) {
	t.Helper()
	mkThread(t, e.st, id, projectID)
	require.NoError(t, e.st.Q.SetThreadOrb(context.Background(), db.SetThreadOrbParams{ID: id, OrbID: &orbID}))
}

func TestAgentOrbMethod(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkProject(t, e, "prj_1")
	mkOrb(t, e, "orb_a", "")
	mkOrb(t, e, "orb_b", "")
	mkOrb(t, e, "orb_r", orbs.KindRunner)
	mkOrbThread(t, e, "thr_a", "orb_a", "prj_1")
	mkOrbThread(t, e, "thr_b", "orb_b", "prj_1")
	mkOrbThread(t, e, "thr_r", "orb_r", "prj_1")
	mkOrbThread(t, e, "thr_old", "orb_a", "prj_1")
	_, err := e.st.DB.ExecContext(ctx, `UPDATE threads SET archived_at = 1 WHERE id = 'thr_old'`)
	require.NoError(t, err)

	links := fakeLinks{}
	e.s.RegisterOrbMethod(links, fakeSource{"orb_a:/home/u/page.html": "<h1>orb</h1>"})
	fn := links[string(apigen.MethodArtifactCall)]
	require.NotNil(t, fn)
	call := func(orbID, thread, op, params string) (any, error) {
		raw, err := json.Marshal(map[string]any{"threadId": thread, "op": op, "params": json.RawMessage(params)})
		require.NoError(t, err)
		return fn(ctx, orbID, raw)
	}
	ctlCode := func(err error) string {
		ce, ok := err.(*link.CtlError)
		require.True(t, ok, "%v", err)
		return ce.Code
	}

	// 调用方校验：线程不在这台 orb 上、已归档、不存在 → forbidden。
	_, err = call("orb_b", "thr_a", OpList, `{}`)
	assert.Equal(t, CodeForbidden, ctlCode(err))
	_, err = call("orb_a", "thr_old", OpList, `{}`)
	assert.Equal(t, CodeForbidden, ctlCode(err))
	_, err = call("orb_a", "thr_none", OpList, `{}`)
	assert.Equal(t, CodeForbidden, ctlCode(err))

	// 成功：结果是 LinkCtlArtifactCallResult；source 经 Fetch 从发起的 orb 取回。
	res, err := call("orb_a", "thr_a", OpCreate, `{"title":"orb 页","files":[{"path":"index.html","source":"/home/u/page.html"}]}`)
	require.NoError(t, err)
	out := res.(apigen.LinkCtlArtifactCallResult)
	assert.Equal(t, OpCreate, out.Details["op"])
	id := out.Details["id"].(string)
	assert.Equal(t, "<h1>orb</h1>", readFile(t, e.s, id, 0, "index.html"))
	a, err := e.s.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "prj_1", a.ProjectID)
	assert.Equal(t, "thr_a", a.ThreadID)

	// orbd 拒绝取回 → invalid；业务错误码原样回到 link。
	_, err = call("orb_a", "thr_a", OpCreate, `{"title":"x","files":[{"path":"index.html","source":"/missing"}]}`)
	assert.Equal(t, CodeInvalid, ctlCode(err))
	_, err = call("orb_a", "thr_a", OpUpdate, `{"id":"`+id+`","baseVersion":1,"edits":[{"oldText":"nope","newText":"x"}]}`)
	assert.Equal(t, CodeEditNotFound, ctlCode(err))

	// 有项目的普通 orb 线程看得到同项目的；Runner 线程只看得到自己的。
	_, err = call("orb_b", "thr_b", OpRead, `{"id":"`+id+`"}`)
	require.NoError(t, err)
	_, err = call("orb_r", "thr_r", OpRead, `{"id":"`+id+`"}`)
	assert.Equal(t, CodeNotFound, ctlCode(err))
}

// M29：artifact.call 外壳里的 turn 进入 Caller.Turn；同一回合写两次，两版 turnId 相同、title 取首行；
// 自动消息（`[Oryn ` 开头）不当标题；list_artifact_versions 带 turnId / title，没有时省略。
func TestAgentOrbTurn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	mkOrb(t, e, "orb_a", "")
	mkOrbThread(t, e, "thr_a", "orb_a", "")
	links := fakeLinks{}
	e.s.RegisterOrbMethod(links, fakeSource{})
	fn := links[string(apigen.MethodArtifactCall)]
	call := func(turn any, op, params string) apigen.LinkCtlArtifactCallResult {
		t.Helper()
		m := map[string]any{"threadId": "thr_a", "op": op, "params": json.RawMessage(params)}
		if turn != nil {
			m["turn"] = turn
		}
		raw, err := json.Marshal(m)
		require.NoError(t, err)
		res, err := fn(ctx, "orb_a", raw)
		require.NoError(t, err)
		return res.(apigen.LinkCtlArtifactCallResult)
	}
	turn := map[string]string{"id": "turn-1", "title": "  把首页改成深色\n第二行细节\n"}
	id := call(turn, OpCreate, `{"title":"页","html":"<p>1</p>"}`).Details["id"].(string)
	call(turn, OpUpdate, `{"id":"`+id+`","baseVersion":1,"html":"<p>2</p>"}`)
	call(map[string]string{"id": "turn-2", "title": "[Oryn 设计便签] 有 2 条便签"}, OpUpdate, `{"id":"`+id+`","baseVersion":2,"html":"<p>3</p>"}`)
	call(nil, OpUpdate, `{"id":"`+id+`","baseVersion":3,"html":"<p>4</p>"}`)
	long := strings.Repeat("长", MaxNoteRunes+5)
	call(map[string]string{"id": "turn-3", "title": long}, OpRestoreVersion, `{"id":"`+id+`","version":1,"baseVersion":4}`)

	vs, err := e.s.Versions(ctx, id)
	require.NoError(t, err)
	require.Len(t, vs, 5)
	// 新的在前：v5 恢复（turn-3，标题截断）、v4 无回合、v3 自动消息、v2/v1 同一回合
	assert.Equal(t, "turn-3", vs[0].TurnID)
	assert.Equal(t, strings.Repeat("长", MaxNoteRunes), vs[0].Title)
	assert.Empty(t, vs[1].TurnID)
	assert.Empty(t, vs[1].Title)
	assert.Equal(t, "turn-2", vs[2].TurnID)
	assert.Empty(t, vs[2].Title, "自动消息不当标题")
	for _, v := range vs[3:] {
		assert.Equal(t, "turn-1", v.TurnID)
		assert.Equal(t, "把首页改成深色", v.Title)
	}

	list := textObj(t, mustCall(t, e.s, threadCaller("thr_a"), OpListVersions, `{"id":"`+id+`"}`))["versions"].([]any)
	require.Len(t, list, 5)
	v1 := list[4].(map[string]any)
	assert.Equal(t, "turn-1", v1["turnId"])
	assert.Equal(t, "把首页改成深色", v1["title"])
	v4 := list[1].(map[string]any)
	assert.NotContains(t, v4, "turnId")
	assert.NotContains(t, v4, "title")
	v3 := list[2].(map[string]any)
	assert.Equal(t, "turn-2", v3["turnId"])
	assert.NotContains(t, v3, "title")
	api := vs[3].API()
	require.NotNil(t, api.TurnID)
	assert.Equal(t, "turn-1", *api.TurnID)
	require.NotNil(t, api.Title)
	assert.Nil(t, api.Check)
}

// M29：画布的设计状态——缺省 autoNotes 开且不建行；PATCH 后读回；design 按顶层键判断；check 读最新版本的 check_json；
// 非画布 invalid。
func TestDesignState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.s.Create(ctx, CreateParams{Title: "画布", Files: []FileChange{
		put(EntryCanvas, `{"format":1,"canvases":[{"id":"c1","name":"首页"}],"pages":[{"id":"p1","name":"一","file":"a.html"}]}`),
		put("a.html", "<p>a</p>"),
	}})
	require.NoError(t, err)
	d, err := e.s.Design(ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, d.AutoNotes)
	assert.False(t, d.Design)
	assert.Nil(t, d.Check)
	assert.Nil(t, d.API().Direction)
	assert.Nil(t, d.API().Live)
	_, err = e.st.Q.GetArtifactDesign(ctx, a.ID)
	assert.ErrorIs(t, err, sql.ErrNoRows, "GET 不建行")

	d, err = e.s.SetDesignAutoNotes(ctx, a.ID, false)
	require.NoError(t, err)
	assert.False(t, d.AutoNotes)
	d, err = e.s.Design(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, d.AutoNotes)
	d, err = e.s.SetDesignAutoNotes(ctx, a.ID, true)
	require.NoError(t, err)
	assert.True(t, d.AutoNotes)

	_, err = e.st.DB.ExecContext(ctx, `UPDATE artifact_versions SET check_json = ? WHERE artifact_id = ? AND n = 1`,
		`{"status":"issues","issues":[{"rule":"text-clipped","frame":"p1","loc":null}],"fingerprint":"abc"}`, a.ID)
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(ctx, `UPDATE artifact_designs SET direction = ? WHERE artifact_id = ?`, `{"name":"深海"}`, a.ID)
	require.NoError(t, err)
	d, err = e.s.Design(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, d.Check)
	assert.Equal(t, apigen.ArtifactCheckIssues, d.Check.Status)
	require.Len(t, d.Check.Issues, 1)
	assert.Equal(t, "text-clipped", d.Check.Issues[0].Rule)
	assert.Equal(t, map[string]any{"name": "深海"}, d.API().Direction)

	// 顶层 design 键 → 设计画布（直接写 canvas.json 的字节绕过 canvas 包的校验，只测这里的判据）
	_, err = e.st.DB.ExecContext(ctx, `UPDATE artifact_versions SET check_json = NULL WHERE artifact_id = ?`, a.ID)
	require.NoError(t, err)
	sha := writeContent(t, e, `{"format":2,"design":{"engine":1,"start":null},"pages":[]}`)
	_, err = e.st.DB.ExecContext(ctx, `UPDATE artifact_files SET sha256 = ? WHERE artifact_id = ? AND path = ?`, sha, a.ID, EntryCanvas)
	require.NoError(t, err)
	d, err = e.s.Design(ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, d.Design)
	assert.Nil(t, d.Check)

	w, err := e.s.Create(ctx, CreateParams{Title: "页", Files: []FileChange{put("index.html", "<p>x</p>")}})
	require.NoError(t, err)
	_, err = e.s.Design(ctx, w.ID)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.SetDesignAutoNotes(ctx, w.ID, false)
	assert.Equal(t, CodeInvalid, code(err))

	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	_, err = e.s.SetDesignAutoNotes(ctx, a.ID, false)
	assert.Equal(t, CodeDeleted, code(err))
}

// writeContent 把字节落进内容存储，返回 sha256。
func writeContent(t *testing.T, e *env, s string) string {
	t.Helper()
	st, err := e.s.c.put(strings.NewReader(s), MaxFileBytes, false)
	require.NoError(t, err)
	return st.sha
}
