package github

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// stubAttachments 记录上传的附件。
type stubAttachments struct {
	name, threadID, body string
}

func (a *stubAttachments) Upload(_ context.Context, name, threadID string, r io.Reader) (db.Attachment, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return db.Attachment{}, err
	}
	a.name, a.threadID, a.body = name, threadID, string(b)
	return db.Attachment{ID: "att_x", Name: name, Mime: "text/markdown", Size: int64(len(b))}, nil
}

// boundWidgets 建一个绑定到 acme/widgets（id 42）的项目。
func (e *testEnv) boundWidgets() string {
	e.t.Helper()
	e.installApp()
	e.widgets()
	id := e.project("https://github.com/acme/widgets")
	require.NoError(e.t, e.s.BindProject(e.ctx, id))
	require.NotNil(e.t, e.getProject(id).GithubInstallationID)
	return id
}

func TestListRefs(t *testing.T) {
	e := newEnv(t)
	prj := e.boundWidgets()
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	e.fake.AddIssue(githubtest.Issue{RepoID: 42, Number: 7, Title: "Login broken", Author: "alice", UpdatedAt: t0})
	e.fake.AddIssue(githubtest.Issue{RepoID: 42, Number: 3, Title: "Old", State: "closed", UpdatedAt: t0})
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 12, Title: "Fix login redirect", UpdatedAt: t0.Add(time.Hour)}})
	e.fake.AddPull(githubtest.Pull{Issue: githubtest.Issue{RepoID: 42, Number: 13, Title: "Merged one", UpdatedAt: t0.Add(-time.Hour)}, MergedAt: t0})

	refs, err := e.s.ListRefs(e.ctx, prj, "")
	require.NoError(t, err)
	require.Len(t, refs, 3)
	assert.EqualValues(t, 12, refs[0].Number)
	assert.Equal(t, apigen.Pull, refs[0].Kind)
	assert.Equal(t, apigen.GitHubRefStateOpen, refs[0].State)
	assert.Equal(t, t0.Add(time.Hour).UnixMilli(), refs[0].UpdatedAt)
	assert.EqualValues(t, 7, refs[1].Number)
	assert.Equal(t, apigen.Issue, refs[1].Kind)
	assert.Equal(t, e.fake.URL()+"/Acme/Widgets/issues/7", refs[1].URL)
	assert.Equal(t, apigen.GitHubRefStateMerged, refs[2].State)

	refs, err = e.s.ListRefs(e.ctx, prj, " 7 ")
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.EqualValues(t, 7, refs[0].Number)

	refs, err = e.s.ListRefs(e.ctx, prj, "99")
	require.NoError(t, err)
	assert.Empty(t, refs)

	e.fake.ResetCalls()
	refs, err = e.s.ListRefs(e.ctx, prj, "login")
	require.NoError(t, err)
	require.Len(t, refs, 2)
	calls := e.fake.CallsTo("GET", "/search/issues")
	require.Len(t, calls, 1)
	q, err := url.ParseQuery(calls[0].Query)
	require.NoError(t, err)
	assert.Equal(t, "login repo:acme/widgets", q.Get("q"))

	_, err = e.s.ListRefs(e.ctx, e.project("https://github.com/acme/other"), "")
	assert.Equal(t, "github_not_bound", err.(*Error).Code)
}

func TestRenderRefGolden(t *testing.T) {
	body := "Redirect loops after login."
	merged := "2026-11-16T10:00:00Z"
	line := 42
	issue := issuePayload{
		Number: 12, Title: "Fix login redirect loop", Body: &body, State: "closed",
		HTMLURL: "https://github.com/acme/widgets/pull/12", User: loginPayload{Login: "carol"},
		Labels: []labelPayload{{Name: "bug"}, {Name: "auth"}},
	}
	pull := &pullPayload{
		Number: 12, State: "closed", MergedAt: &merged, Additions: 120, Deletions: 30, ChangedFiles: 4,
		Head: branchPayload{Ref: "fix-login"}, Base: branchPayload{Ref: "main"},
	}
	comments := []commentPayload{
		{User: loginPayload{Login: "alice"}, Body: "Looks good.", CreatedAt: "2026-11-16T08:00:00Z"},
		{User: loginPayload{Login: "dave"}, Body: "Thanks!", CreatedAt: "2026-11-16T08:30:00Z"},
	}
	reviews := []commentPayload{
		{User: loginPayload{Login: "bob"}, Body: "Handle the nil case.", CreatedAt: "2026-11-16T09:00:00Z", Path: "internal/auth/login.go", Line: &line},
	}
	want := `# Fix login redirect loop (acme/widgets#12)

- Type: pull request
- State: merged
- Author: @carol
- Labels: bug, auth
- URL: https://github.com/acme/widgets/pull/12
- Branches: fix-login → main
- Changes: +120 −30 in 4 files

## Description

Redirect loops after login.

## Comments (2)

### @alice · 2026-11-16T08:00:00Z

Looks good.

### @dave · 2026-11-16T08:30:00Z

Thanks!

## Review comments (1)

### @bob · internal/auth/login.go:42 · 2026-11-16T09:00:00Z

Handle the nil case.
`
	assert.Equal(t, want, renderRef("acme/widgets", issue, pull, comments, reviews))

	plain := issuePayload{Number: 7, Title: "Login broken", State: "open", HTMLURL: "https://github.com/acme/widgets/issues/7", User: loginPayload{Login: "alice"}}
	wantIssue := `# Login broken (acme/widgets#7)

- Type: issue
- State: open
- Author: @alice
- URL: https://github.com/acme/widgets/issues/7

## Description

_No description._
`
	assert.Equal(t, wantIssue, renderRef("acme/widgets", plain, nil, nil, nil))
}

func TestAttachRef(t *testing.T) {
	e := newEnv(t)
	att := &stubAttachments{}
	e.s.att = att
	prj := e.boundWidgets()
	e.fake.AddPull(githubtest.Pull{
		Issue: githubtest.Issue{RepoID: 42, Number: 12, Title: "Fix login", Body: "desc", Author: "carol"},
		HeadRef: "fix-login", Additions: 3, Deletions: 1, ChangedFiles: 2,
	})
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 12, Author: "alice", Body: "c1"})
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 12, Author: "dave", Body: "c2"})
	e.fake.AddComment(githubtest.Comment{RepoID: 42, Number: 12, Author: "bob", Body: "r1", Path: "a.go", Line: 5})

	a, err := e.s.AttachRef(e.ctx, prj, 12)
	require.NoError(t, err)
	assert.Equal(t, "widgets-pr-12.md", a.Name)
	assert.Empty(t, att.threadID)
	assert.True(t, strings.HasPrefix(att.body, "# Fix login (acme/widgets#12)\n"))
	assert.Contains(t, att.body, "- Branches: fix-login → main\n")
	assert.Contains(t, att.body, "## Comments (2)")
	assert.Contains(t, att.body, "### @bob · a.go:5 · ")
	assert.NotContains(t, att.body, "\n\nr1\n\n### @")

	e.fake.AddIssue(githubtest.Issue{RepoID: 42, Number: 7, Title: "Bug"})
	a, err = e.s.AttachRef(e.ctx, prj, 7)
	require.NoError(t, err)
	assert.Equal(t, "widgets-issue-7.md", a.Name)
	assert.NotContains(t, att.body, "## Comments")

	_, err = e.s.AttachRef(e.ctx, prj, 99)
	assert.Equal(t, "not_found", err.(*Error).Code)
	_, err = e.s.AttachRef(e.ctx, e.project(""), 7)
	assert.Equal(t, "github_not_bound", err.(*Error).Code)
}
