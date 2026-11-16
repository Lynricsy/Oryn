package main

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// runGithub 用假服务端执行 `oryn github …`，返回 stdout、stderr 与错误。
func runGithub(f *fakeAPI, args ...string) (string, string, error) {
	cmd := newGithubCmd(func() (*apiClient, error) { return f.client(), nil })
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func TestGithubParseRef(t *testing.T) {
	repo, n, err := parseGithubRef("octo-org/my.repo_x#42")
	require.NoError(t, err)
	assert.Equal(t, "octo-org/my.repo_x", repo)
	assert.Equal(t, int64(42), n)

	for _, bad := range []string{
		"", "octo/demo", "octo/demo#", "octo/demo#0", "octo/demo#012", "octo/demo#-1", "octo/demo#1a",
		"octo#1", "/demo#1", "octo/#1", "a/b/c#1", "octo/demo#1#2", " octo/demo#1", "octo/demo #1",
		"-octo/demo#1", "octo/..#1", "octo/.#1", "octo/de mo#1", "octo/demo#99999999999999999999",
	} {
		_, _, err := parseGithubRef(bad)
		require.Error(t, err, bad)
		requireUsageErr(t, err)
		assert.Contains(t, err.Error(), "不是有效的 issue 引用", bad)
	}
}

func TestGithubTriage(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/github/triage", 200, apigen.GitHubTriageResult{ThreadID: "T-1"})

	out, _, err := runGithub(f, "triage", "octo/demo#12")
	require.NoError(t, err)
	assert.Equal(t, "T-1\n", out)
	reqs := f.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "POST", reqs[0].Method)
	assert.Equal(t, "/api/v1/github/triage", reqs[0].Path)
	assert.JSONEq(t, `{"repo":"octo/demo","number":12}`, string(reqs[0].Body))

	rule := "ght_01JCZ8M3W4X5Y6Z7A8B9C0D1E2"
	_, _, err = runGithub(f, "triage", "octo/demo#12", "--rule", rule)
	require.NoError(t, err)
	reqs = f.requests()
	assert.JSONEq(t, `{"repo":"octo/demo","number":12,"ruleId":"`+rule+`"}`, string(reqs[len(reqs)-1].Body))

	// 非法引用与规则 id：不发请求
	_, _, err = runGithub(f, "triage", "octo/demo")
	requireUsageErr(t, err)
	assert.ErrorContains(t, err, "不是有效的 issue 引用")
	_, _, err = runGithub(f, "triage", "octo/demo#1", "--rule", "ght_lower")
	requireUsageErr(t, err)
	assert.Len(t, f.requests(), 2)
}

func TestGithubTriageJSONPassthrough(t *testing.T) {
	f := newFakeAPI(t)
	body := `{"threadId":"T-9",  "extra":true}`
	f.raw("POST /api/v1/github/triage", "application/json", body)
	out, _, err := runGithub(f, "triage", "octo/demo#3", "--json")
	require.NoError(t, err)
	assert.Equal(t, body+"\n", out)
}

func TestGithubReplay(t *testing.T) {
	f := newFakeAPI(t)
	guid := "a/b c"
	f.json("POST /api/v1/github/deliveries/"+guid+"/replay", 202, nil)

	out, _, err := runGithub(f, "replay", guid)
	require.NoError(t, err)
	assert.Equal(t, guid+" 已重新入队\n", out)
	reqs := f.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "POST", reqs[0].Method)
	assert.Equal(t, "/api/v1/github/deliveries/"+guid+"/replay", reqs[0].Path)
	assert.Empty(t, reqs[0].Body)

	out, _, err = runGithub(f, "replay", guid, "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"guid":"a/b c","queued":true}`, out)

	// 服务端拒绝（如超过 7 天）时返回错误
	_, _, err = runGithub(f, "replay", "missing")
	assert.ErrorContains(t, err, "no route")
}

func TestGithubStatus(t *testing.T) {
	f := newFakeAPI(t)
	thr := "T-abc"
	ver := "1.2.3"
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	src := apigen.GitHubHookLastDeliverySource("webhook")
	f.json("GET /api/v1/github/tasks", 200, apigen.GitHubTaskList{Tasks: []apigen.GitHubTask{
		{Kind: apigen.GitHubTaskKindIssue, Repo: "octo/demo", Number: new(int64(7)), State: "fixing", ThreadID: &thr},
		{Kind: apigen.GitHubTaskKindRelease, Repo: "octo/lib", Version: &ver, State: "watching"},
	}})
	f.json("GET /api/v1/github/hooks", 200, apigen.GitHubHookList{Hooks: []apigen.GitHubHook{
		{FullName: "octo/demo", Status: "active", LastDeliveryAt: &at, LastDeliverySource: &src},
		{FullName: "octo/lib", Status: "error", Error: new("410 gone")},
	}})

	out, _, err := runGithub(f, "status")
	require.NoError(t, err)
	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "GET", reqs[0].Method)
	assert.Equal(t, "/api/v1/github/tasks", reqs[0].Path)
	assert.Equal(t, "/api/v1/github/hooks", reqs[1].Path)

	lines := strings.Split(out, "\n")
	assert.Contains(t, out, "KIND")
	assert.Contains(t, out, "LAST DELIVERY")
	assert.Regexp(t, `issue\s+octo/demo\s+#7\s+fixing\s+T-abc`, out)
	assert.Regexp(t, `release\s+octo/lib\s+1\.2\.3\s+watching\s+-`, out)
	assert.Regexp(t, `octo/demo\s+active\s+\S+ \S+ \(webhook\)`, out)
	assert.Regexp(t, `octo/lib\s+error: 410 gone\s+-`, out)
	assert.Contains(t, lines, "", "两张表之间空一行")
}

func TestGithubStatusJSON(t *testing.T) {
	f := newFakeAPI(t)
	f.raw("GET /api/v1/github/tasks", "application/json", `{"tasks":[{"kind":"issue","repo":"o/r","state":"new","updatedAt":1}]}`)
	f.raw("GET /api/v1/github/hooks", "application/json", `{"hooks":[]}`)
	out, _, err := runGithub(f, "status", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"tasks":{"tasks":[{"kind":"issue","repo":"o/r","state":"new","updatedAt":1}]},"hooks":{"hooks":[]}}`, out)
}

func TestGithubCleanup(t *testing.T) {
	f := newFakeAPI(t)
	thr := "T-c"
	f.json("POST /api/v1/github/tasks/cleanup", 200, apigen.GitHubTask{Kind: apigen.GitHubTaskKindIssue, Repo: "octo/demo", Number: new(int64(5)), State: "abandoned", ThreadID: &thr})

	out, _, err := runGithub(f, "cleanup", "octo/demo#5")
	require.NoError(t, err)
	assert.Contains(t, out, "octo/demo#5")
	assert.Contains(t, out, "abandoned")
	assert.Contains(t, out, "T-c")
	reqs := f.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "POST", reqs[0].Method)
	assert.Equal(t, "/api/v1/github/tasks/cleanup", reqs[0].Path)
	assert.JSONEq(t, `{"repo":"octo/demo","number":5}`, string(reqs[0].Body))

	body := `{"kind":"issue","repo":"octo/demo","number":5,"state":"abandoned","updatedAt":1}`
	f.raw("POST /api/v1/github/tasks/cleanup", "application/json", body)
	out, _, err = runGithub(f, "cleanup", "octo/demo#5", "--json")
	require.NoError(t, err)
	assert.Equal(t, body+"\n", out)

	_, _, err = runGithub(f, "cleanup", "octo/demo#x")
	requireUsageErr(t, err)
	assert.Len(t, f.requests(), 2)
}

func TestGithubAudit(t *testing.T) {
	f := newFakeAPI(t)
	thr := "T-a"
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	f.json("GET /api/v1/github/audit", 200, apigen.GitHubAuditList{
		Entries: []apigen.GitHubAuditEntry{
			{ID: 9, At: at, ThreadID: &thr, Slot: "agent", Method: "POST", Op: "/repos/{owner}/{repo}/pulls", Repo: new("octo/demo"), Status: 201, Gate: new("pass")},
			{ID: 8, At: at, Slot: "user", Method: "MUTATION", Op: "addComment", Status: 200},
		},
		NextBefore: new(int64(8)),
	})

	out, errOut, err := runGithub(f, "audit", "--thread", thr, "--repo", "octo/demo", "--limit", "20", "--before", "100")
	require.NoError(t, err)
	reqs := f.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "GET", reqs[0].Method)
	assert.Equal(t, "/api/v1/github/audit", reqs[0].Path)
	q, err := url.ParseQuery(reqs[0].RawQuery)
	require.NoError(t, err)
	assert.Equal(t, url.Values{"thread": {thr}, "repo": {"octo/demo"}, "limit": {"20"}, "before": {"100"}}, q)

	for _, h := range []string{"TIME", "THREAD", "SLOT", "METHOD", "OP", "REPO", "STATUS", "GATE"} {
		assert.Contains(t, out, h)
	}
	assert.Regexp(t, `T-a\s+agent\s+POST\s+/repos/\{owner\}/\{repo\}/pulls\s+octo/demo\s+201\s+pass`, out)
	assert.Regexp(t, `-\s+user\s+MUTATION\s+addComment\s+-\s+200\s+-`, out)
	assert.Contains(t, errOut, "--before 8")

	// 无参数时不带查询串
	_, _, err = runGithub(f, "audit")
	require.NoError(t, err)
	reqs = f.requests()
	assert.Empty(t, reqs[len(reqs)-1].RawQuery)

	_, _, err = runGithub(f, "audit", "--limit", "0")
	requireUsageErr(t, err)
	assert.Len(t, f.requests(), 2)
}

func TestGithubAuditJSONPassthrough(t *testing.T) {
	f := newFakeAPI(t)
	body := `{"entries":[],   "nextBefore":3}`
	f.raw("GET /api/v1/github/audit", "application/json", body)
	out, _, err := runGithub(f, "audit", "--json")
	require.NoError(t, err)
	assert.Equal(t, body+"\n", out)
}
