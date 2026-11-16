package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// maxRefBody 是引用附件里单条正文的字符上限。
const maxRefBody = 20000

type loginPayload struct {
	Login string `json:"login"`
}

// issuePayload 是 REST / webhook 里的 issue（PR 的 issue 视图带 pull_request）。
type issuePayload struct {
	Number      int64          `json:"number"`
	Title       string         `json:"title"`
	Body        *string        `json:"body"`
	State       string         `json:"state"`
	HTMLURL     string         `json:"html_url"`
	User        loginPayload   `json:"user"`
	Labels      []labelPayload `json:"labels"`
	UpdatedAt   string         `json:"updated_at"`
	PullRequest *struct {
		MergedAt *string `json:"merged_at"`
	} `json:"pull_request"`
}

type labelPayload struct {
	Name string `json:"name"`
}

// pullPayload 是 REST / webhook 里的 pull_request。
type pullPayload struct {
	Number       int64         `json:"number"`
	Title        string        `json:"title"`
	Body         *string       `json:"body"`
	State        string        `json:"state"`
	Draft        bool          `json:"draft"`
	Merged       bool          `json:"merged"`
	MergedAt     *string       `json:"merged_at"`
	HTMLURL      string        `json:"html_url"`
	User         loginPayload  `json:"user"`
	Head         branchPayload `json:"head"`
	Base         branchPayload `json:"base"`
	Additions    int           `json:"additions"`
	Deletions    int           `json:"deletions"`
	ChangedFiles int           `json:"changed_files"`
}

type branchPayload struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// merged 报告 PR 是否已合并（webhook 与 REST 都带 merged_at；部分载荷没有 merged）。
func (p pullPayload) merged() bool { return p.Merged || p.MergedAt != nil && *p.MergedAt != "" }

// commentPayload 是会话评论或行内评审评论。
type commentPayload struct {
	ID           int64        `json:"id"`
	User         loginPayload `json:"user"`
	Body         string       `json:"body"`
	CreatedAt    string       `json:"created_at"`
	Path         string       `json:"path"`
	Line         *int         `json:"line"`
	OriginalLine *int         `json:"original_line"`
}

// githubTimeMs 把 GitHub 的 RFC 3339 时间解析成毫秒；空或无法解析返回 0。
func githubTimeMs(v string) int64 {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// boundProject 读已绑定的项目，返回其 `owner/repo` 与安装令牌的 Authorization 头；未绑定 → github_not_bound。
func (s *Service) boundProject(ctx context.Context, projectID string) (db.Project, string, string, error) {
	prj, err := s.st.Q.GetProject(ctx, projectID)
	if err != nil {
		if isNoRows(err) {
			return prj, "", "", errf("not_found", "project not found")
		}
		return prj, "", "", err
	}
	full, auth, err := s.projectAuth(ctx, prj)
	return prj, full, auth, err
}

// projectAuth 返回已绑定项目的 `owner/repo` 与安装令牌的 Authorization 头；未绑定 → github_not_bound。
func (s *Service) projectAuth(ctx context.Context, prj db.Project) (string, string, error) {
	full, ok := githubRepoPath(prj.RepoUrl)
	if !ok || prj.GithubRepoID == nil || prj.GithubInstallationID == nil {
		return "", "", errf("github_not_bound", "project is not bound to a GitHub repository")
	}
	tok, err := s.installToken(ctx, *prj.GithubInstallationID)
	if err != nil {
		return "", "", err
	}
	return full, "Bearer " + tok, nil
}

// ListRefs 列出已绑定项目仓库的 issue / PR 候选：q 为空取最近更新的打开项，全是数字按编号取，否则搜索。
func (s *Service) ListRefs(ctx context.Context, projectID, q string) ([]apigen.GitHubRef, error) {
	_, full, auth, err := s.boundProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	var items []issuePayload
	switch {
	case q == "":
		err = s.call(ctx, http.MethodGet, "/repos/"+full+"/issues?state=open&sort=updated&direction=desc&per_page=20", auth, nil, &items)
	case isDigits(q):
		var it issuePayload
		err = s.call(ctx, http.MethodGet, "/repos/"+full+"/issues/"+q, auth, nil, &it)
		if isStatus(err, http.StatusNotFound) {
			return []apigen.GitHubRef{}, nil
		}
		items = []issuePayload{it}
	default:
		var res struct {
			Items []issuePayload `json:"items"`
		}
		err = s.call(ctx, http.MethodGet, "/search/issues?q="+url.QueryEscape(q+" repo:"+full)+"&sort=updated&order=desc&per_page=20", auth, nil, &res)
		items = res.Items
	}
	if err != nil {
		return nil, err
	}
	out := make([]apigen.GitHubRef, 0, len(items))
	for _, it := range items {
		out = append(out, refAPI(it))
	}
	return out, nil
}

func isDigits(v string) bool {
	if v == "" {
		return false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func refAPI(it issuePayload) apigen.GitHubRef {
	ref := apigen.GitHubRef{
		Number: it.Number, Kind: apigen.Issue, Title: it.Title, State: apigen.GitHubRefState(it.State),
		URL: it.HTMLURL, UpdatedAt: githubTimeMs(it.UpdatedAt),
	}
	if it.PullRequest != nil {
		ref.Kind = apigen.Pull
		if it.PullRequest.MergedAt != nil && *it.PullRequest.MergedAt != "" {
			ref.State = apigen.GitHubRefStateMerged
		}
	}
	return ref
}

// AttachRef 把已绑定项目仓库的 issue / PR（含评论）渲染成 Markdown 附件（不属于任何线程）。
func (s *Service) AttachRef(ctx context.Context, projectID string, number int64) (db.Attachment, error) {
	_, full, auth, err := s.boundProject(ctx, projectID)
	if err != nil {
		return db.Attachment{}, err
	}
	base := fmt.Sprintf("/repos/%s/issues/%d", full, number)
	var issue issuePayload
	if err := s.call(ctx, http.MethodGet, base, auth, nil, &issue); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return db.Attachment{}, errf("not_found", "issue or pull request not found")
		}
		return db.Attachment{}, err
	}
	var comments []commentPayload
	if err := s.call(ctx, http.MethodGet, base+"/comments?per_page=50", auth, nil, &comments); err != nil {
		return db.Attachment{}, err
	}
	kind := "issue"
	var pull *pullPayload
	var reviews []commentPayload
	if issue.PullRequest != nil {
		kind = "pr"
		pull = &pullPayload{}
		pbase := fmt.Sprintf("/repos/%s/pulls/%d", full, number)
		if err := s.call(ctx, http.MethodGet, pbase, auth, nil, pull); err != nil {
			return db.Attachment{}, err
		}
		if err := s.call(ctx, http.MethodGet, pbase+"/comments?per_page=50", auth, nil, &reviews); err != nil {
			return db.Attachment{}, err
		}
	}
	md := renderRef(full, issue, pull, comments, reviews)
	_, name, _ := strings.Cut(full, "/")
	return s.att.Upload(ctx, fmt.Sprintf("%s-%s-%d.md", name, kind, number), "", strings.NewReader(md))
}

// renderRef 把 issue / PR 渲染成 Markdown（格式见 docs/plan-m14.md 步骤 8.1）。
func renderRef(repo string, issue issuePayload, pull *pullPayload, comments, reviewComments []commentPayload) string {
	typ, state := "issue", issue.State
	if pull != nil {
		typ, state = "pull request", pull.State
		if pull.merged() {
			state = "merged"
		}
	}
	meta := []string{
		"- Type: " + typ,
		"- State: " + state,
		"- Author: @" + issue.User.Login,
	}
	if len(issue.Labels) > 0 {
		names := make([]string, len(issue.Labels))
		for i, l := range issue.Labels {
			names[i] = l.Name
		}
		meta = append(meta, "- Labels: "+strings.Join(names, ", "))
	}
	meta = append(meta, "- URL: "+issue.HTMLURL)
	if pull != nil {
		files := "files"
		if pull.ChangedFiles == 1 {
			files = "file"
		}
		meta = append(meta,
			fmt.Sprintf("- Branches: %s → %s", pull.Head.Ref, pull.Base.Ref),
			fmt.Sprintf("- Changes: +%d −%d in %d %s", pull.Additions, pull.Deletions, pull.ChangedFiles, files))
	}
	desc := "_No description._"
	if issue.Body != nil && strings.TrimSpace(*issue.Body) != "" {
		desc = clip(*issue.Body, maxRefBody)
	}
	blocks := []string{
		fmt.Sprintf("# %s (%s#%d)", issue.Title, repo, issue.Number),
		strings.Join(meta, "\n"),
		"## Description",
		desc,
	}
	if len(comments) > 0 {
		blocks = append(blocks, fmt.Sprintf("## Comments (%d)", len(comments)))
		for _, c := range comments {
			blocks = append(blocks, fmt.Sprintf("### @%s · %s", c.User.Login, c.CreatedAt), clip(c.Body, maxRefBody))
		}
	}
	if pull != nil && len(reviewComments) > 0 {
		blocks = append(blocks, fmt.Sprintf("## Review comments (%d)", len(reviewComments)))
		for _, c := range reviewComments {
			where := c.Path
			line := c.Line
			if line == nil {
				line = c.OriginalLine
			}
			if line != nil && *line > 0 {
				where += ":" + strconv.Itoa(*line)
			}
			blocks = append(blocks, fmt.Sprintf("### @%s · %s · %s", c.User.Login, where, c.CreatedAt), clip(c.Body, maxRefBody))
		}
	}
	return strings.Join(blocks, "\n\n") + "\n"
}
