package egress

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gateFake 是门禁现查的控制面状态；字段可在请求之间修改（模拟线程期间的变化）。
type gateFake struct {
	mu         sync.Mutex
	prepared   map[string]prepRec // sha → 通过记录
	implAuth   bool
	heads      map[string]string // repo + "|" + ref → sha
	headCalls  []string          // HeadSHA 的 repo|ref
	defaults   map[string]string // repo → 默认分支
	defCalls   int
	hooks      map[int64]bool
	hookCalls  []string
	release    bool
	relCalls   int
	defaultErr error
	thread     ThreadGates // 线程任务的限制（每次写请求现查）
	threadErr  error
	threadN    int
}

type prepRec struct {
	level   string
	skipped bool
	stale   bool // 登记于门禁配置改动之前
}

// ThreadGates：gl 为 nil（没有门禁的代理）时没有线程限制。
func (f fakeR) ThreadGates() (ThreadGates, error) {
	if f.gl == nil {
		return ThreadGates{}, nil
	}
	f.gl.mu.Lock()
	defer f.gl.mu.Unlock()
	f.gl.threadN++
	return f.gl.thread, f.gl.threadErr
}

// setThread 在请求之间改线程任务的限制。
func (e *gateEnv) setThread(tg ThreadGates, err error) {
	e.gl.mu.Lock()
	defer e.gl.mu.Unlock()
	e.gl.thread, e.gl.threadErr = tg, err
}

// newThreadEnv 是没有项目门禁、只有线程限制的代理。
func newThreadEnv(t *testing.T, tg ThreadGates) *gateEnv {
	t.Helper()
	e := newGateEnv(t, GitHubAccess{})
	e.setThread(tg, nil)
	return e
}

func (f fakeR) Prepared(sha, level string) PrepareState {
	if f.gl == nil {
		return PrepareNone
	}
	f.gl.mu.Lock()
	defer f.gl.mu.Unlock()
	r, ok := f.gl.prepared[sha]
	switch {
	case !ok || (level == levelPR && r.level != levelPR):
		return PrepareNone
	case r.stale:
		return PrepareStale
	case r.skipped:
		return PrepareSkipped
	}
	return PreparePassed
}

func (f fakeR) ImplAuthorized() bool {
	if f.gl == nil {
		return false
	}
	f.gl.mu.Lock()
	defer f.gl.mu.Unlock()
	return f.gl.implAuth
}

func (f fakeR) HeadSHA(repo, ref string) (string, error) {
	if f.gl == nil {
		return "", errors.New("no lookup")
	}
	f.gl.mu.Lock()
	defer f.gl.mu.Unlock()
	f.gl.headCalls = append(f.gl.headCalls, repo+"|"+ref)
	sha, ok := f.gl.heads[repo+"|"+ref]
	if !ok {
		return "", errors.New("ref not found")
	}
	return sha, nil
}

func (f fakeR) DefaultBranch(repo string) (string, error) {
	if f.gl == nil {
		return "", errors.New("no lookup")
	}
	f.gl.mu.Lock()
	defer f.gl.mu.Unlock()
	f.gl.defCalls++
	if f.gl.defaultErr != nil {
		return "", f.gl.defaultErr
	}
	return f.gl.defaults[repo], nil
}

func (f fakeR) IsOrynHook(repo string, id int64) bool {
	if f.gl == nil {
		return true
	}
	f.gl.mu.Lock()
	defer f.gl.mu.Unlock()
	f.gl.hookCalls = append(f.gl.hookCalls, fmt.Sprintf("%s#%d", repo, id))
	return f.gl.hooks[id]
}

func (f fakeR) ReleaseThread() bool {
	if f.gl == nil {
		return false
	}
	f.gl.mu.Lock()
	defer f.gl.mu.Unlock()
	f.gl.relCalls++
	return f.gl.release
}

const (
	shaPrepared = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaPR       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaSkipped  = "cccccccccccccccccccccccccccccccccccccccc"
	shaRaw      = "dddddddddddddddddddddddddddddddddddddddd" // 没有通过记录
	shaStale    = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" // 只有门禁配置改动之前登记的记录
)

func newGateFake() *gateFake {
	return &gateFake{
		prepared: map[string]prepRec{
			shaPrepared: {level: levelPush},
			shaPR:       {level: levelPR},
			shaSkipped:  {level: levelPR, skipped: true},
			shaStale:    {level: levelPR, stale: true},
		},
		heads:    map[string]string{},
		defaults: map[string]string{"acme/widgets": "main", "R_1": "main", "42": "main"},
		hooks:    map[int64]bool{},
	}
}

// gateEnv 是一个带门禁的代理：API 与 git 上游都是同一个记录请求的假服务。
type gateEnv struct {
	t     *testing.T
	px    string
	rec   *writeRec
	gl    *gateFake
	mu    sync.Mutex
	calls int
}

func newGateEnv(t *testing.T, ga GitHubAccess) *gateEnv {
	t.Helper()
	e := &gateEnv{t: t, rec: &writeRec{}, gl: newGateFake()}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		e.mu.Lock()
		e.calls++
		e.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/git-receive-pack"):
			w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
			_, _ = w.Write(pkt("unpack ok\n", "0000"))
		case strings.HasSuffix(r.URL.Path, "/info/refs"):
			w.Header().Set("Content-Type", "application/x-"+r.URL.Query().Get("service")+"-advertisement")
		case r.URL.Path == "/graphql":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":{}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(up.Close)
	ga.APIOrigin, ga.GitOrigin, ga.UploadsOrigin = up.URL, up.URL, up.URL
	srv := httptest.NewServer(NewHandler("orb-x", fakeR{
		gh: "T0KEN", git: map[string]string{"github.com": "tok"}, pol: Policy{GitHub: &ga}, rec: e.rec, gl: e.gl,
	}))
	t.Cleanup(srv.Close)
	e.px = srv.URL
	return e
}

func (e *gateEnv) upstreamCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.calls
	e.calls = 0
	return n
}

// send 发一个请求，返回状态码与响应体。
func (e *gateEnv) send(method, path string, body []byte, hdr map[string]string) (int, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.px+path, bytes.NewReader(body))
	require.NoError(e.t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// allowed 断言请求被转发：上游收到请求，Begin 一次（或 nBegin 次），结论为 verdict。
func (e *gateEnv) allowed(name, method, path string, body []byte, verdict string) {
	e.t.Helper()
	code, out := e.send(method, path, body, nil)
	assert.Less(e.t, code, 300, "%s: %s", name, out)
	assert.Equal(e.t, 1, e.upstreamCalls(), "%s：转发到上游", name)
	begins, ends := e.rec.take()
	if assert.NotEmpty(e.t, begins, name) {
		for _, b := range begins {
			assert.Equal(e.t, verdict, b.Gate, name)
			assert.False(e.t, b.Rejected, name)
		}
	}
	assert.Len(e.t, ends, len(begins), name)
}

// denied 断言请求被门禁拒绝：403 `oryn: <原因>`（含 want），不触达上游，不 Begin，End 记 Rejected 与原因。
func (e *gateEnv) denied(name, method, path string, body []byte, want string) {
	e.t.Helper()
	code, out := e.send(method, path, body, nil)
	assert.Equal(e.t, http.StatusForbidden, code, name)
	assert.True(e.t, strings.HasPrefix(out, "oryn: "), "%s: %s", name, out)
	assert.Contains(e.t, out, want, name)
	assert.Zero(e.t, e.upstreamCalls(), "%s：不触达上游", name)
	begins, ends := e.rec.take()
	assert.Empty(e.t, begins, "%s：被拒绝的请求不预登记", name)
	if assert.NotEmpty(e.t, ends, name) {
		for _, w := range ends {
			assert.True(e.t, w.w.Rejected, name)
			assert.Contains(e.t, w.w.Gate, want, name)
			assert.Equal(e.t, http.StatusForbidden, w.status, name)
		}
	}
}

func gql(t *testing.T, doc string, vars map[string]any, op string) []byte {
	t.Helper()
	return gqlBody(t, doc, vars, op)
}

const prBody = "## Summary\n修好了\n\n## Testing\nmake test\n\nFixes #12\n"

// 覆盖表「创建 PR」：PR 门禁查 head SHA 的 pr 级通过记录与正文规则；REST / GraphQL / fork / head_repo 各形态的 head。
func TestGatePRCreate(t *testing.T) {
	e := newGateEnv(t, GitHubAccess{Gates: &GateConfig{PR: true, PRHeadings: []string{"Summary", "Testing"}, RequireClosingRef: true}})
	e.gl.heads["acme/widgets|feat"] = shaPR
	e.gl.heads["acme/widgets|push-only"] = shaPrepared
	e.gl.heads["acme/widgets|skip"] = shaSkipped
	e.gl.heads["acme/widgets|stale"] = shaStale
	e.gl.heads["acme/widgets|raw"] = shaRaw
	e.gl.heads["R_9|feat"] = shaPR
	e.gl.heads["R_1|me:fix"] = shaPR
	e.gl.heads["me/widgets-fork|feat"] = shaPR
	e.gl.heads["42|feat"] = shaPR
	rest := func(head, body string, extra string) []byte {
		return fmt.Appendf(nil, `{"title":"t","head":%q,"base":"main","body":%q%s}`, head, body, extra)
	}
	pulls := "/gh/api.github.com/repos/Acme/Widgets/pulls"

	e.allowed("REST 放行", "POST", pulls, rest("feat", prBody, ""), GatePass)
	e.allowed("/repositories/{id} 形态", "POST", "/gh/api.github.com/repositories/42/pulls/", rest("feat", prBody, ""), GatePass)
	e.allowed("跳过检查的记录同样放行", "POST", pulls, rest("skip", prBody, ""), GateSkipped)
	e.denied("没有通过记录", "POST", pulls, rest("raw", prBody, ""), "没有 pr 级 github_prepare 通过记录")
	e.denied("push 级记录不够", "POST", pulls, rest("push-only", prBody, ""), "没有 pr 级")
	e.denied("门禁配置改动前的记录", "POST", pulls, rest("stale", prBody, ""), "门禁配置已变，请重新运行 github_prepare level=pr")
	e.denied("缺少二级标题", "POST", pulls, rest("feat", "## Summary\nFixes #1", ""), "缺少二级标题「## Testing」")
	e.denied("缺少关闭引用", "POST", pulls, rest("feat", "## Summary\n## Testing\n", ""), "Fixes #N")
	e.denied("head 查询失败", "POST", pulls, rest("missing", prBody, ""), "查询 head missing 的提交失败")

	// REST head_repo：owner 取 head 的 owner: 前缀
	e.allowed("REST head_repo", "POST", pulls, rest("me:feat", prBody, `,"head_repo":"widgets-fork"`), GatePass)
	assert.Contains(t, e.gl.headCalls, "me/widgets-fork|feat")

	gqlURL := "/gh/api.github.com/graphql"
	create := `mutation PullRequestCreate($input: CreatePullRequestInput!) { createPullRequest(input: $input) { pullRequest { id url } } }`
	in := func(extra map[string]any) map[string]any {
		m := map[string]any{"repositoryId": "R_1", "baseRefName": "main", "headRefName": "me:fix", "title": "t", "body": prBody}
		for k, v := range extra {
			m[k] = v
		}
		return map[string]any{"input": m}
	}
	e.allowed("GraphQL fork head", "POST", gqlURL, gql(t, create, in(nil), "PullRequestCreate"), GatePass)
	e.allowed("GraphQL headRepositoryId", "POST", gqlURL, gql(t, create, in(map[string]any{"headRepositoryId": "R_9", "headRefName": "feat"}), ""), GatePass)
	e.denied("GraphQL 正文不合规", "POST", gqlURL, gql(t, create, in(map[string]any{"body": "nothing"}), ""), "缺少二级标题")
	e.denied("revertPullRequest 无法确定 head", "POST", gqlURL,
		gql(t, `mutation { revertPullRequest(input: {pullRequestId: "PR_1"}) { clientMutationId } }`, nil, ""), "head 分支")

	// 门禁关闭：不查 head
	off := newGateEnv(t, GitHubAccess{})
	off.allowed("PR 门禁关闭", "POST", pulls, rest("raw", "", ""), "")
	assert.Empty(t, off.gl.headCalls)
}

// 覆盖表「创建 PR」的 ImplAuth：授权状态每个请求现查，线程期间变为已授权后放行。
func TestGateImplAuth(t *testing.T) {
	e := newThreadEnv(t, ThreadGates{ImplAuth: true})
	body := []byte(`{"title":"t","head":"feat","base":"main","body":"x"}`)
	pulls := "/gh/api.github.com/repos/acme/widgets/pulls"
	e.denied("未授权", "POST", pulls, body, "尚未获得实现授权")
	e.denied("GraphQL 未授权", "POST", "/gh/api.github.com/graphql",
		gql(t, `mutation { createPullRequest(input: {repositoryId: "R_1", baseRefName: "main", headRefName: "feat", title: "t"}) { clientMutationId } }`, nil, ""),
		"尚未获得实现授权")
	e.gl.mu.Lock()
	e.gl.implAuth = true
	e.gl.mu.Unlock()
	e.allowed("分类后已授权", "POST", pulls, body, GatePass)
	// 其它写操作不受影响
	e.allowed("评论不受 ImplAuth 影响", "POST", "/gh/api.github.com/repos/acme/widgets/issues/1/comments", []byte(`{"body":"hi"}`), "")
}

// 覆盖表「改 ref（可知 SHA）」：推送门禁查 Prepared(sha,"push")；只管 refs/heads/*；默认分支只许发布线程改；
// 门禁配置改动前登记的记录不放行。
func TestGateRefUpdate(t *testing.T) {
	e := newGateEnv(t, GitHubAccess{Gates: &GateConfig{Push: true}})
	refs := "/gh/api.github.com/repos/acme/widgets/git/refs"
	e.allowed("POST git/refs 有记录", "POST", refs, fmt.Appendf(nil, `{"ref":"refs/heads/feat","sha":%q}`, shaPrepared), GatePass)
	e.allowed("pr 级记录满足 push", "POST", refs, fmt.Appendf(nil, `{"ref":"refs/heads/feat","sha":%q}`, shaPR), GatePass)
	e.allowed("跳过检查", "POST", refs, fmt.Appendf(nil, `{"ref":"refs/heads/feat","sha":%q}`, shaSkipped), GateSkipped)
	e.denied("门禁配置改动前的记录", "POST", refs, fmt.Appendf(nil, `{"ref":"refs/heads/feat","sha":%q}`, shaStale), "门禁配置已变，请重新运行 github_prepare")
	e.denied("POST git/refs 无记录", "POST", refs, fmt.Appendf(nil, `{"ref":"refs/heads/feat","sha":%q}`, shaRaw), "没有 github_prepare 通过记录")
	e.allowed("tag 不经推送门禁", "POST", refs, fmt.Appendf(nil, `{"ref":"refs/tags/v1","sha":%q}`, shaRaw), "")
	e.denied("PATCH git/refs 无记录", "PATCH", refs+"/heads/feat/", fmt.Appendf(nil, `{"sha":%q,"force":true}`, shaRaw), "没有 github_prepare")
	e.allowed("PATCH 编码路径", "PATCH", refs+"/heads%2Ffeat", fmt.Appendf(nil, `{"sha":%q}`, shaPrepared), GatePass)
	e.denied("PATCH 默认分支", "PATCH", refs+"/heads/main", fmt.Appendf(nil, `{"sha":%q}`, shaPrepared), "非发布任务不能推送默认分支 main")
	e.allowed("删除分支", "DELETE", refs+"/heads/feat", nil, GatePass)
	e.denied("删除默认分支", "DELETE", refs+"/heads/main", nil, "默认分支")

	gqlURL := "/gh/api.github.com/graphql"
	e.allowed("createRef 有记录", "POST", gqlURL, gql(t,
		`mutation($oid: GitObjectID!) { createRef(input: {repositoryId: "R_1", name: "refs/heads/x", oid: $oid}) { clientMutationId } }`,
		map[string]any{"oid": shaPrepared}, ""), GatePass)
	// 别名与 fragment 不能绕开：updateRefs 里任一 ref 没有记录即整个请求被拒
	e.denied("updateRefs 经别名与 fragment", "POST", gqlURL, gql(t,
		`mutation M { ...F } fragment F on Mutation { ok: updateRefs(input: {repositoryId: "R_1", refUpdates: [
		  {name: "refs/heads/a", afterOid: "`+shaPrepared+`"}, {name: "refs/heads/b", afterOid: "`+shaRaw+`"}]}) { clientMutationId } }`, nil, "M"),
		"b 的 dddddddddddd 没有 github_prepare")
	e.denied("updateRef 只带 refId", "POST", gqlURL, gql(t,
		`mutation { updateRef(input: {refId: "REF_1", oid: "`+shaPrepared+`"}) { clientMutationId } }`, nil, ""), "无法确定 ref 名")

	off := newGateEnv(t, GitHubAccess{})
	off.allowed("推送门禁关闭", "PATCH", refs+"/heads/main", fmt.Appendf(nil, `{"sha":%q}`, shaRaw), "")
	assert.Zero(t, off.gl.defCalls, "门禁关闭时不查默认分支")
}

// 覆盖表「改 ref（不可知 SHA）」：推送门禁开启或 ReviewOnly 时拒绝。
func TestGateRefUnknown(t *testing.T) {
	base := "/gh/api.github.com/repos/acme/widgets"
	cases := []struct {
		name, method, path string
		body               []byte
	}{
		{"PUT contents", "PUT", base + "/contents/docs/a.md", []byte(`{"message":"m","content":"eA==","branch":"feat"}`)},
		{"DELETE contents", "DELETE", base + "/contents/a.md", []byte(`{"message":"m","sha":"x"}`)},
		{"POST merges", "POST", base + "/merges", []byte(`{"base":"main","head":"feat"}`)},
		{"POST merge-upstream", "POST", base + "/merge-upstream", []byte(`{"branch":"main"}`)},
		{"PUT update-branch", "PUT", base + "/pulls/3/update-branch", []byte(`{}`)},
		{"createCommitOnBranch", "POST", "/gh/api.github.com/graphql",
			[]byte(`{"query":"mutation { createCommitOnBranch(input: {branch: {branchName: \"feat\"}, expectedHeadOid: \"x\"}) { clientMutationId } }"}`)},
		{"mergeBranch", "POST", "/gh/api.github.com/graphql",
			[]byte(`{"query":"mutation { mergeBranch(input: {repositoryId: \"R_1\", base: \"main\", head: \"feat\"}) { clientMutationId } }"}`)},
		{"updatePullRequestBranch", "POST", "/gh/api.github.com/graphql",
			[]byte(`{"query":"mutation { updatePullRequestBranch(input: {pullRequestId: \"PR_1\"}) { clientMutationId } }"}`)},
	}
	on := newGateEnv(t, GitHubAccess{Gates: &GateConfig{Push: true}})
	ro := newThreadEnv(t, ThreadGates{ReviewOnly: true})
	off := newGateEnv(t, GitHubAccess{Gates: &GateConfig{PR: true}})
	for _, c := range cases {
		on.denied(c.name+"（推送门禁）", c.method, c.path, c.body, "无法确定新 SHA")
		ro.denied(c.name+"（ReviewOnly）", c.method, c.path, c.body, denyReviewOnly)
		off.allowed(c.name+"（推送门禁关闭）", c.method, c.path, c.body, "")
	}
}

// 覆盖表「合并」与 R21 的只读工作区：ReviewOnly 拒绝推送、创建 PR、合并、改 ref；评论与 review 照常。
func TestGateReviewOnly(t *testing.T) {
	e := newThreadEnv(t, ThreadGates{ReviewOnly: true})
	base := "/gh/api.github.com/repos/acme/widgets"
	gqlURL := "/gh/api.github.com/graphql"
	e.denied("PUT merge", "PUT", base+"/pulls/3/merge", []byte(`{"merge_method":"squash"}`), denyReviewOnly)
	e.denied("PUT merge.json", "PUT", base+"/pulls/3/merge.json", []byte(`{}`), denyReviewOnly)
	for _, m := range []string{"mergePullRequest", "enablePullRequestAutoMerge", "enqueuePullRequest"} {
		e.denied(m, "POST", gqlURL, []byte(`{"query":"mutation { x: `+m+`(input: {pullRequestId: \"PR_1\"}) { clientMutationId } }"}`), denyReviewOnly)
	}
	e.denied("创建 PR", "POST", base+"/pulls", []byte(`{"head":"feat","base":"main"}`), denyReviewOnly)
	e.denied("POST git/refs", "POST", base+"/git/refs", fmt.Appendf(nil, `{"ref":"refs/heads/x","sha":%q}`, shaPR), denyReviewOnly)
	e.allowed("评论", "POST", base+"/issues/3/comments", []byte(`{"body":"looks good"}`), "")
	e.allowed("review", "POST", base+"/pulls/3/reviews", []byte(`{"event":"APPROVE","body":"ok"}`), "")

	// git：推送协商阶段就 403；fetch 照常
	code, out := e.send("GET", "/git/github.com/acme/widgets.git/info/refs?service=git-receive-pack", nil, nil)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, out, "oryn: "+denyReviewOnly)
	code, _ = e.send("GET", "/git/github.com/acme/widgets.git/info/refs?service=git-upload-pack", nil, nil)
	assert.Equal(t, http.StatusOK, code)
	e.upstreamCalls()
	// 推送本身（绕过协商直接 POST）回 ng
	body := append(pkt(oidA+" "+oidB+" refs/heads/feat\x00report-status", "0000"), "PACK"...)
	code, out = e.send("POST", "/git/github.com/acme/widgets.git/git-receive-pack", body, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, string(ReportStatusNG([]string{"refs/heads/feat"}, "oryn: "+denyReviewOnly, false)), out)
	assert.Zero(t, e.upstreamCalls())
	e.rec.take()

	off := newGateEnv(t, GitHubAccess{})
	off.allowed("不是评审线程时合并放行", "PUT", base+"/pulls/3/merge", []byte(`{}`), "")
}

// 覆盖表「review 事件」：CommentOnlyReviews 只放行 COMMENT 与没有 event 的待提交 review；别名、fragment 不能绕开。
func TestGateReviewEvents(t *testing.T) {
	e := newThreadEnv(t, ThreadGates{CommentOnlyReviews: true})
	base := "/gh/api.github.com/repos/acme/widgets/pulls/3/reviews"
	gqlURL := "/gh/api.github.com/graphql"
	e.allowed("COMMENT", "POST", base, []byte(`{"event":"COMMENT","body":"nit","comments":[{"path":"a.go","line":1,"body":"x"}]}`), GatePass)
	e.allowed("待提交 review", "POST", base, []byte(`{"body":"draft"}`), GatePass)
	e.denied("APPROVE", "POST", base, []byte(`{"event":"approve"}`), "只能提交 COMMENT")
	e.denied("REQUEST_CHANGES 提交", "POST", base+"/9/events", []byte(`{"event":"REQUEST_CHANGES","body":"no"}`), "REQUEST_CHANGES")
	e.allowed("COMMENT 提交", "POST", base+"/9/events", []byte(`{"event":"COMMENT"}`), GatePass)
	e.denied("GraphQL 别名 + fragment", "POST", gqlURL, gql(t,
		`query Q { viewer { login } }
mutation R($e: PullRequestReviewEvent) { ...A }
fragment A on Mutation { ... on Mutation { c: addPullRequestReview(input: {pullRequestId: "PR_1", event: $e, body: "ok"}) { clientMutationId } } }`,
		map[string]any{"e": "APPROVE"}, "R"), "APPROVE")
	e.allowed("GraphQL COMMENT", "POST", gqlURL, gql(t,
		`mutation { submitPullRequestReview(input: {pullRequestReviewId: "PRR_1", event: COMMENT}) { clientMutationId } }`, nil, ""), GatePass)
	e.denied("GraphQL event 变量缺失", "POST", gqlURL, gql(t,
		`mutation($e: PullRequestReviewEvent!) { submitPullRequestReview(input: {pullRequestReviewId: "PRR_1", event: $e}) { clientMutationId } }`, nil, ""),
		denyUnparsed)

	off := newGateEnv(t, GitHubAccess{})
	off.allowed("不限 review 类型", "POST", base, []byte(`{"event":"APPROVE"}`), "")
}

// 覆盖表「Oryn 的 hook」：不论门禁开关，改删 Oryn 管理的 hook 一律拒绝（请求体解析不了时按路径里的 hook id）。
func TestGateOrynHook(t *testing.T) {
	e := newGateEnv(t, GitHubAccess{})
	e.gl.hooks[77] = true
	base := "/gh/api.github.com/repos/acme/widgets/hooks"
	e.denied("PATCH", "PATCH", base+"/77", []byte(`{"active":false}`), denyOrynHook)
	e.denied("DELETE", "DELETE", base+"/77/", nil, denyOrynHook)
	e.denied("PATCH config", "PATCH", base+"/77/config", []byte(`{"url":"https://evil.example"}`), denyOrynHook)
	e.denied("/repositories/{id}", "DELETE", "/gh/api.github.com/repositories/42/hooks/77", nil, denyOrynHook)
	e.denied("超限请求体", "PATCH", base+"/77", []byte(`{"config":"`+strings.Repeat("a", ghWriteBodyMax)+`"}`), denyOrynHook)
	e.allowed("别的 hook", "PATCH", base+"/78", []byte(`{"active":false}`), "")
	e.allowed("ping 不改 hook", "POST", base+"/77/pings", nil, "")
	assert.Contains(t, e.gl.hookCalls, "acme/widgets#77")
	assert.Contains(t, e.gl.hookCalls, "#77", "/repositories/{id} 形态 repo 为空")
}

// R19「失败即拒绝」：门禁开启时超限、压缩编码、无法静态求值的写请求一律拒绝；门禁关闭时只审计。
func TestGateUnparsed(t *testing.T) {
	big := []byte(`{"body":"` + strings.Repeat("a", ghWriteBodyMax) + `"}`)
	comment := "/gh/api.github.com/repos/acme/widgets/issues/5/comments"
	gqlURL := "/gh/api.github.com/graphql"
	unresolved := gql(t, `mutation($in: CreatePullRequestInput!) { createPullRequest(input: $in) { clientMutationId } }`, nil, "")
	type env struct {
		ga  GitHubAccess
		tg  ThreadGates
		err error
	}
	for name, c := range map[string]env{
		"推送门禁":               {ga: GitHubAccess{Gates: &GateConfig{Push: true}}},
		"PR 门禁":              {ga: GitHubAccess{Gates: &GateConfig{PR: true}}},
		"ReviewOnly":         {tg: ThreadGates{ReviewOnly: true}},
		"CommentOnlyReviews": {tg: ThreadGates{CommentOnlyReviews: true}},
		"ImplAuth":           {tg: ThreadGates{ImplAuth: true}},
		"线程限制查询失败":           {err: errors.New("db down")},
	} {
		e := newGateEnv(t, c.ga)
		e.setThread(c.tg, c.err)
		e.denied(name+"：超限请求体", "POST", comment, big, denyUnparsed)
		e.denied(name+"：压缩编码", "POST", comment, []byte("\x1f\x8b"), denyUnparsed)
		code, _ := e.send("POST", comment, []byte(`{"body":"x"}`), map[string]string{"Content-Encoding": "gzip"})
		assert.Equal(t, http.StatusForbidden, code)
		e.rec.take()
		e.denied(name+"：参数无法静态求值的受管控 mutation", "POST", gqlURL, unresolved, denyUnparsed)
		e.denied(name+"：GraphQL 语法错误", "POST", gqlURL, []byte(`{"query":"mutation { createPullRequest("}`), denyUnparsed)
		// 推送命令段解析不了（push-cert）
		code, out := e.send("POST", "/git/github.com/acme/widgets.git/git-receive-pack", pkt("push-cert\x00report-status", "0000"), nil)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Contains(t, out, denyUnparsed)
		assert.Zero(t, e.upstreamCalls())
		e.rec.take()
	}
	off := newGateEnv(t, GitHubAccess{Gates: &GateConfig{}})
	code, _ := off.send("POST", comment, big, nil)
	assert.Equal(t, http.StatusCreated, code, "门禁关闭时超限请求体照常转发")
	off.upstreamCalls()
	off.rec.take()
	off.allowed("门禁关闭：无法求值的 mutation 只审计", "POST", gqlURL, unresolved, "")
}

// 覆盖表「git 推送」：推送门禁检查每个 refs/heads/* 的新 SHA；tag 与删除不查 prepare；空命令段放行；
// 拒绝时读完请求体，回 200 + `unpack ok` + 每个 ref 的 `ng <ref> oryn: <原因>`（协商了 side-band-64k 时放在 band 1）。
func TestGateReceivePack(t *testing.T) {
	e := newGateEnv(t, GitHubAccess{Gates: &GateConfig{Push: true}})
	url := "/git/github.com/Acme/Widgets.git/git-receive-pack"
	pack := "PACK\x00\x00\x00\x02" + strings.Repeat("x", 1<<16)
	push := func(caps string, cmds ...string) []byte {
		cmds[0] += "\x00" + caps
		return append(pkt(append(cmds, "0000")...), pack...)
	}

	ok := push("report-status side-band-64k", oidA+" "+shaPrepared+" refs/heads/feat", oidZ+" "+shaRaw+" refs/tags/v1", oidA+" "+oidZ+" refs/heads/old")
	code, _ := e.send("POST", url, ok, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 1, e.upstreamCalls())
	begins, _ := e.rec.take()
	require.Len(t, begins, 1)
	assert.Equal(t, GatePass, begins[0].Gate)

	skip := push("report-status", oidA+" "+shaSkipped+" refs/heads/feat")
	code, _ = e.send("POST", url, skip, nil)
	assert.Equal(t, http.StatusOK, code)
	begins, _ = e.rec.take()
	require.Len(t, begins, 1)
	assert.Equal(t, GateSkipped, begins[0].Gate)
	e.upstreamCalls()

	for _, sideband := range []bool{true, false} {
		caps := "report-status"
		if sideband {
			caps += " side-band-64k"
		}
		bad := push(caps, oidA+" "+shaPrepared+" refs/heads/feat", oidZ+" "+shaRaw+" refs/heads/wip")
		resp, err := http.Post(e.px+url, "application/x-git-receive-pack-request", bytes.NewReader(bad))
		require.NoError(t, err)
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/x-git-receive-pack-result", resp.Header.Get("Content-Type"))
		reason := "oryn: 推送门禁：wip 的 dddddddddddd 没有 github_prepare 通过记录；先运行 github_prepare"
		assert.Equal(t, string(ReportStatusNG([]string{"refs/heads/feat", "refs/heads/wip"}, reason, sideband)), string(out))
		if sideband {
			assert.Contains(t, string(out), "\x01000eunpack ok\n", "band 1 里是 report-status")
		} else {
			assert.True(t, strings.HasPrefix(string(out), "000eunpack ok\n"))
		}
		assert.Zero(t, e.upstreamCalls())
		begins, ends := e.rec.take()
		assert.Empty(t, begins)
		require.Len(t, ends, 1)
		assert.True(t, ends[0].w.Rejected)
		assert.Equal(t, strings.TrimPrefix(reason, "oryn: "), ends[0].w.Gate)
		assert.Len(t, ends[0].w.Refs, 2, "审计每个 ref 一行")
	}

	code, _ = e.send("POST", url, []byte("0000"), nil)
	assert.Equal(t, http.StatusOK, code, "空命令段放行")
	assert.Equal(t, 1, e.upstreamCalls())
	e.rec.take()
}

// v4：推送门禁开启时非发布线程不能推默认分支，发布线程放行；推送门禁关闭（含无项目 orb）时普通线程推默认分支放行。
// 默认分支按仓库缓存；查询失败按拒绝处理。
func TestGateDefaultBranch(t *testing.T) {
	url := "/git/github.com/acme/widgets.git/git-receive-pack"
	toMain := append(pkt(oidA+" "+shaPrepared+" refs/heads/main\x00report-status side-band-64k", "0000"), "PACK"...)

	e := newGateEnv(t, GitHubAccess{Gates: &GateConfig{Push: true, PR: true}})
	for range 2 {
		code, out := e.send("POST", url, toMain, nil)
		assert.Equal(t, http.StatusOK, code)
		assert.Contains(t, out, "ng refs/heads/main oryn: 推送门禁：非发布任务不能推送默认分支 main")
		assert.Zero(t, e.upstreamCalls())
		e.rec.take()
	}
	assert.Equal(t, 1, e.gl.defCalls, "默认分支按仓库缓存")

	e.gl.mu.Lock()
	e.gl.release = true
	e.gl.mu.Unlock()
	code, out := e.send("POST", url, toMain, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, out, "ng ")
	assert.Equal(t, 1, e.upstreamCalls(), "发布线程推默认分支放行")
	begins, _ := e.rec.take()
	require.Len(t, begins, 1)
	assert.Equal(t, GatePass, begins[0].Gate)
	// 发布线程的新 SHA 仍须有 prepare 记录
	raw := append(pkt(oidA+" "+shaRaw+" refs/heads/main\x00report-status", "0000"), "PACK"...)
	_, out = e.send("POST", url, raw, nil)
	assert.Contains(t, out, "没有 github_prepare 通过记录")
	e.upstreamCalls()
	e.rec.take()

	for name, ga := range map[string]GitHubAccess{
		"推送门禁关闭（只开 PR 门禁）": {Gates: &GateConfig{PR: true}},
		"无项目 orb": {},
	} {
		o := newGateEnv(t, ga)
		code, out := o.send("POST", url, raw, nil)
		assert.Equal(t, http.StatusOK, code, name)
		assert.NotContains(t, out, "ng ", name)
		assert.Equal(t, 1, o.upstreamCalls(), name)
		begins, _ := o.rec.take()
		require.Len(t, begins, 1, name)
		assert.Empty(t, begins[0].Gate, name)
		assert.Zero(t, o.gl.defCalls, name+"：不查默认分支")
		assert.Zero(t, o.gl.relCalls, name+"：不查发布任务")
	}

	failing := newGateEnv(t, GitHubAccess{Gates: &GateConfig{Push: true}})
	failing.gl.defaultErr = errors.New("boom")
	feat := append(pkt(oidA+" "+shaPrepared+" refs/heads/feat\x00report-status", "0000"), "PACK"...)
	_, out = failing.send("POST", url, feat, nil)
	assert.Contains(t, out, "ng refs/heads/feat oryn: 推送门禁：查询 acme/widgets 的默认分支失败（boom）")
	assert.Zero(t, failing.upstreamCalls())
}

// 线程限制每次写请求现查（修订 v6）：orb 完成 setup（策略里没有线程限制）之后才写入 review_only 的任务，
// 下一次推送与 createPullRequest 就被拒；查询失败按最严处理（review-only 视为开启）。
func TestGateThreadGatesLive(t *testing.T) {
	e := newGateEnv(t, GitHubAccess{})
	push := "/git/github.com/acme/widgets.git/git-receive-pack"
	body := append(pkt(oidA+" "+shaRaw+" refs/heads/feat\x00report-status", "0000"), "PACK"...)
	gqlURL := "/gh/api.github.com/graphql"
	create := []byte(`{"query":"mutation { createPullRequest(input: {repositoryId: \"R_1\", baseRefName: \"main\", headRefName: \"feat\", title: \"t\"}) { clientMutationId } }"}`)
	advertise := "/git/github.com/acme/widgets.git/info/refs?service=git-receive-pack"

	// 任务写入之前：照常放行
	code, out := e.send("POST", push, body, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.NotContains(t, out, "ng ")
	assert.Equal(t, 1, e.upstreamCalls())
	e.rec.take()
	e.allowed("任务写入前开 PR", "POST", gqlURL, create, "")

	// 线程任务之后才写入 review_only=1：同一台 orb、同一份策略，下一次写请求即被拒
	e.setThread(ThreadGates{ReviewOnly: true}, nil)
	code, out = e.send("POST", push, body, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, string(ReportStatusNG([]string{"refs/heads/feat"}, "oryn: "+denyReviewOnly, false)), out)
	assert.Zero(t, e.upstreamCalls())
	e.rec.take()
	e.denied("任务写入后开 PR", "POST", gqlURL, create, denyReviewOnly)
	code, out = e.send("GET", advertise, nil, nil)
	assert.Equal(t, http.StatusForbidden, code, "推送协商阶段同样现查")
	assert.Contains(t, out, "oryn: "+denyReviewOnly)
	assert.Zero(t, e.upstreamCalls())

	// 查询失败：按最严处理
	e.setThread(ThreadGates{}, errors.New("db down"))
	code, out = e.send("POST", push, body, nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, out, "ng refs/heads/feat oryn: "+denyReviewOnly)
	e.rec.take()
	e.denied("查询失败时开 PR", "POST", gqlURL, create, denyReviewOnly)
	e.denied("查询失败时 APPROVE", "POST", "/gh/api.github.com/repos/acme/widgets/pulls/3/reviews", []byte(`{"event":"APPROVE"}`), "只能提交 COMMENT")
	code, _ = e.send("GET", advertise, nil, nil)
	assert.Equal(t, http.StatusForbidden, code)
	e.allowed("查询失败不影响评论", "POST", "/gh/api.github.com/repos/acme/widgets/issues/3/comments", []byte(`{"body":"hi"}`), "")

	// 限制解除后恢复
	e.setThread(ThreadGates{}, nil)
	e.allowed("限制解除", "POST", gqlURL, create, "")
}
