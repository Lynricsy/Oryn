package githubtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Issue 是一个 issue；PR 的会话部分也用它（嵌在 Pull 里）。
type Issue struct {
	RepoID    int64
	Number    int64
	Title     string
	Body      string
	State     string // open | closed
	Author    string
	Labels    []string
	UpdatedAt time.Time
	// CreatedAt 缺省等于 UpdatedAt。
	CreatedAt time.Time
}

// Pull 是一个 PR。
type Pull struct {
	Issue
	Draft    bool
	MergedAt time.Time // 非零 = 已合并
	MergedBy string    // 合并者登录名；空 = 不出现 merged_by
	HeadRef  string
	HeadSHA  string
	BaseRef  string
	// HeadRepoID 是头分支所在仓库；0 = 与 base 同仓库。
	HeadRepoID   int64
	Additions    int
	Deletions    int
	ChangedFiles int
}

// Comment 是 issue / PR 的会话评论；Path 非空时是 PR 的行内评审评论。
type Comment struct {
	ID        int64
	RepoID    int64
	Number    int64
	Author    string
	Body      string
	CreatedAt time.Time
	Path      string
	Line      int
}

// Review 是 PR 上的一次评审（M15）；State 为 PENDING 表示待提交。
type Review struct {
	ID     int64
	RepoID int64
	Number int64
	Author string
	Body   string
	State  string // PENDING | COMMENTED | APPROVED | CHANGES_REQUESTED
	// SubmittedAt 是提交时间（M15 REST 轮询）；AddReview 缺省为当前时间。
	SubmittedAt time.Time
}

// 假服务的 node id（M15）：仓库 R_<id>，issue I_<仓库 id>_<编号>，PR PR_<仓库 id>_<编号>，会话评论 IC_<id>，
// 行内评审评论 PRRC_<id>，评审 PRR_<id>。测试据此拼 webhook 载荷。
func RepoNodeID(id int64) string              { return fmt.Sprintf("R_%d", id) }
func IssueNodeID(repoID, number int64) string { return fmt.Sprintf("I_%d_%d", repoID, number) }
func PullNodeID(repoID, number int64) string  { return fmt.Sprintf("PR_%d_%d", repoID, number) }
func CommentNodeID(id int64) string           { return fmt.Sprintf("IC_%d", id) }
func ReviewCommentNodeID(id int64) string     { return fmt.Sprintf("PRRC_%d", id) }
func ReviewNodeID(id int64) string            { return fmt.Sprintf("PRR_%d", id) }

// CheckRun 是某个提交上的一个检查。
type CheckRun struct {
	Name       string
	Status     string // 缺省 completed
	Conclusion string
	URL        string
}

// CommitStatus 是某个提交上的一个（旧式）提交状态。
type CommitStatus struct {
	Context string
	State   string // pending | success | failure | error
	URL     string
}

// AddIssue 登记一个 issue（State 缺省 open，UpdatedAt 缺省当前时间）。
func (s *Server) AddIssue(is Issue) *Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	fillIssue(&is)
	p := &is
	s.Issues = append(s.Issues, p)
	return p
}

// AddPull 登记一个 PR（State 缺省 open，BaseRef 缺省 main）。
func (s *Server) AddPull(pr Pull) *Pull {
	s.mu.Lock()
	defer s.mu.Unlock()
	fillIssue(&pr.Issue)
	if pr.BaseRef == "" {
		pr.BaseRef = "main"
	}
	p := &pr
	s.Pulls = append(s.Pulls, p)
	return p
}

// AddComment 登记一条评论（ID 缺省自增，CreatedAt 缺省当前时间）。
func (s *Server) AddComment(c Comment) *Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == 0 {
		s.seq++
		c.ID = 1000 + s.seq
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	p := &c
	s.Comments = append(s.Comments, p)
	return p
}

// SetRollup 设置提交 sha 的 statusCheckRollup 状态（SUCCESS / FAILURE / ERROR / PENDING / EXPECTED；空串 = 没有）。
func (s *Server) SetRollup(sha, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Rollups == nil {
		s.Rollups = map[string]string{}
	}
	s.Rollups[sha] = state
}

// CommitAuthor 是 GraphQL `Commit.authors` 的一个节点；Login 为空表示邮箱对不上 GitHub 账号（user 为 null）。
type CommitAuthor struct {
	Email, Name, Login, AvatarURL string
}

// SetCommitAuthors 设置提交 sha 在 GraphQL `Commit.authors` 里返回的人。
func (s *Server) SetCommitAuthors(sha string, authors []CommitAuthor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.CommitAuthors == nil {
		s.CommitAuthors = map[string][]CommitAuthor{}
	}
	s.CommitAuthors[sha] = authors
}

// SetChecks 设置提交 sha 上的检查与提交状态。
func (s *Server) SetChecks(sha string, runs []CheckRun, statuses []CommitStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.CheckRuns == nil {
		s.CheckRuns = map[string][]CheckRun{}
	}
	if s.Statuses == nil {
		s.Statuses = map[string][]CommitStatus{}
	}
	s.CheckRuns[sha] = runs
	s.Statuses[sha] = statuses
}

func fillIssue(is *Issue) {
	if is.State == "" {
		is.State = "open"
	}
	if is.UpdatedAt.IsZero() {
		is.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	}
	if is.CreatedAt.IsZero() {
		is.CreatedAt = is.UpdatedAt
	}
}

// issueRoutes 登记 issue、PR、评论、提交检查、fork 与 GraphQL 端点。
func (s *Server) issueRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues", s.etagged(s.listIssues))
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}", s.etagged(s.getIssue))
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}/comments", s.etagged(s.listIssueComments))
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}", s.etagged(s.getPull))
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}/comments", s.etagged(s.listReviewComments))
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/pulls", s.commitPulls)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/check-runs", s.etagged(s.checkRuns))
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/status", s.etagged(s.commitStatus))
	mux.HandleFunc("POST /repos/{owner}/{repo}/forks", s.createFork)
	mux.HandleFunc("GET /search/issues", s.searchIssues)
	mux.HandleFunc("POST /graphql", s.graphql)
}

// readableRepo 解析路径里的仓库并校验令牌可读；失败时已写出响应（改名回 301）。调用方持锁。
func (s *Server) readableRepo(w http.ResponseWriter, r *http.Request, tail string) *Repo {
	rp, to := s.lookupRepo(r.PathValue("owner"), r.PathValue("repo"))
	if to != "" {
		s.redirectRepo(w, r, to, tail)
		return nil
	}
	if rp == nil || !s.canRead(bearer(r), rp) {
		fail(w, http.StatusNotFound, "Not Found")
		return nil
	}
	return rp
}

// findPull / findIssue 按仓库与编号查找。调用方持锁。
func (s *Server) findPull(repoID, number int64) *Pull {
	for _, p := range s.Pulls {
		if p.RepoID == repoID && p.Number == number {
			return p
		}
	}
	return nil
}

func (s *Server) findIssue(repoID, number int64) *Issue {
	for _, is := range s.Issues {
		if is.RepoID == repoID && is.Number == number {
			return is
		}
	}
	return nil
}

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func userJSON(login string) map[string]any {
	return map[string]any{"login": login, "type": "User"}
}

// IssueID / PullID 是假服务里 issue 与 PR 的数值 id（REST 的 id 字段，M15 REST 轮询的 guid 用）。
func IssueID(repoID, number int64) int64 { return repoID*1_000_000 + number }
func PullID(repoID, number int64) int64  { return repoID*1_000_000 + 500_000 + number }

// createdOf 是 issue 的创建时间（没填时取更新时间）。
func createdOf(is *Issue) time.Time {
	if is.CreatedAt.IsZero() {
		return is.UpdatedAt
	}
	return is.CreatedAt
}

// assocOf 是 login 与仓库的 author_association：owner 段同名为 OWNER，有写权限为 COLLABORATOR，否则 NONE。调用方持锁。
func (s *Server) assocOf(rp *Repo, login string) string {
	owner, _, _ := strings.Cut(rp.FullName, "/")
	switch {
	case strings.EqualFold(owner, login):
		return "OWNER"
	case permissionsJSON(s.permOf(rp, login))["push"]:
		return "COLLABORATOR"
	}
	return "NONE"
}

// issueJSON 是 issue（或 PR 的 issue 视图，pr 非 nil）的 REST 表示。调用方持锁。
func (s *Server) issueJSON(r *http.Request, rp *Repo, is *Issue, pr *Pull) map[string]any {
	labels := []map[string]any{}
	for _, l := range is.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	m := map[string]any{
		"id": IssueID(rp.ID, is.Number), "number": is.Number, "node_id": IssueNodeID(rp.ID, is.Number), "title": is.Title,
		"body": nullable(is.Body), "state": is.State,
		"html_url": fmt.Sprintf("%s/%s/issues/%d", s.baseURL(r), rp.FullName, is.Number),
		"user":     userJSON(is.Author), "labels": labels, "updated_at": ts(is.UpdatedAt), "created_at": ts(createdOf(is)),
		"author_association": s.assocOf(rp, is.Author),
	}
	if pr != nil {
		m["id"] = PullID(rp.ID, is.Number)
		m["node_id"] = PullNodeID(rp.ID, is.Number)
		m["html_url"] = fmt.Sprintf("%s/%s/pull/%d", s.baseURL(r), rp.FullName, is.Number)
		m["pull_request"] = map[string]any{"html_url": m["html_url"], "merged_at": ts(pr.MergedAt)}
	}
	return m
}

// pullJSON 是 PR 的 REST 表示（与 webhook 载荷里的 pull_request 同形）。调用方持锁。
func (s *Server) pullJSON(r *http.Request, rp *Repo, pr *Pull) map[string]any {
	state := pr.State
	if !pr.MergedAt.IsZero() {
		state = "closed"
	}
	head := map[string]any{"id": rp.ID, "full_name": rp.FullName}
	if pr.HeadRepoID != 0 && pr.HeadRepoID != rp.ID {
		name := fmt.Sprintf("fork-%d/%s", pr.HeadRepoID, rp.FullName[strings.Index(rp.FullName, "/")+1:])
		if hr := s.repoByID(pr.HeadRepoID); hr != nil {
			name = hr.FullName
		}
		head = map[string]any{"id": pr.HeadRepoID, "full_name": name}
	}
	m := map[string]any{
		"id": PullID(rp.ID, pr.Number), "number": pr.Number, "node_id": PullNodeID(rp.ID, pr.Number), "title": pr.Title,
		"body": nullable(pr.Body), "state": state, "draft": pr.Draft,
		"merged": !pr.MergedAt.IsZero(), "merged_at": ts(pr.MergedAt), "merged_by": nil,
		"html_url":  fmt.Sprintf("%s/%s/pull/%d", s.baseURL(r), rp.FullName, pr.Number),
		"user":      userJSON(pr.Author),
		"head":      map[string]any{"ref": pr.HeadRef, "sha": pr.HeadSHA, "repo": head},
		"base":      map[string]any{"ref": pr.BaseRef, "repo": map[string]any{"id": rp.ID, "full_name": rp.FullName}},
		"additions": pr.Additions, "deletions": pr.Deletions, "changed_files": pr.ChangedFiles,
		"updated_at": ts(pr.UpdatedAt), "created_at": ts(createdOf(&pr.Issue)), "author_association": s.assocOf(rp, pr.Author),
	}
	if pr.MergedBy != "" {
		m["merged_by"] = userJSON(pr.MergedBy)
	}
	return m
}

// commentJSON 是会话评论或行内评审评论的 REST 表示。调用方持锁。
func (s *Server) commentJSON(r *http.Request, rp *Repo, c *Comment) map[string]any {
	m := map[string]any{
		"id": c.ID, "node_id": CommentNodeID(c.ID), "user": userJSON(c.Author), "body": c.Body,
		"created_at": ts(c.CreatedAt), "updated_at": ts(c.CreatedAt), "author_association": s.assocOf(rp, c.Author),
		"issue_url": fmt.Sprintf("%s/repos/%s/issues/%d", s.baseURL(r), rp.FullName, c.Number),
		"html_url":  fmt.Sprintf("%s/%s/issues/%d#issuecomment-%d", s.baseURL(r), rp.FullName, c.Number, c.ID),
	}
	if c.Path != "" {
		delete(m, "issue_url")
		m["node_id"] = ReviewCommentNodeID(c.ID)
		m["html_url"] = fmt.Sprintf("%s/%s/pull/%d#discussion_r%d", s.baseURL(r), rp.FullName, c.Number, c.ID)
		m["path"] = c.Path
		m["line"] = c.Line
		m["original_line"] = c.Line
	}
	return m
}

// issuesOf 返回仓库的全部 issue 视图（含 PR），按更新时间倒序。调用方持锁。
func (s *Server) issuesOf(rp *Repo) []struct {
	is *Issue
	pr *Pull
} {
	type entry = struct {
		is *Issue
		pr *Pull
	}
	var out []entry
	for _, is := range s.Issues {
		if is.RepoID == rp.ID {
			out = append(out, entry{is: is})
		}
	}
	for _, pr := range s.Pulls {
		if pr.RepoID == rp.ID {
			out = append(out, entry{is: &pr.Issue, pr: pr})
		}
	}
	slices.SortStableFunc(out, func(a, b entry) int { return b.is.UpdatedAt.Compare(a.is.UpdatedAt) })
	return out
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/issues")
	if rp == nil {
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	// sort 恒按更新时间（Oryn 只用 sort=updated）；since 过滤更新时间，direction=asc 时正序
	since := sinceOf(r)
	all := s.issuesOf(rp)
	if r.URL.Query().Get("direction") == "asc" {
		slices.Reverse(all)
	}
	var hits []map[string]any
	for _, e := range all {
		if (state == "all" || e.is.State == state) && !e.is.UpdatedAt.Before(since) {
			hits = append(hits, s.issueJSON(r, rp, e.is, e.pr))
		}
	}
	lo, hi := s.paginate(w, r, len(hits))
	out := []map[string]any{}
	out = append(out, hits[lo:hi]...)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	rp := s.readableRepo(w, r, "/issues/"+r.PathValue("number"))
	if rp == nil {
		return
	}
	if pr := s.findPull(rp.ID, n); pr != nil {
		writeJSON(w, http.StatusOK, s.issueJSON(r, rp, &pr.Issue, pr))
		return
	}
	if is := s.findIssue(rp.ID, n); is != nil {
		writeJSON(w, http.StatusOK, s.issueJSON(r, rp, is, nil))
		return
	}
	fail(w, http.StatusNotFound, "Not Found")
}

func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	rp := s.readableRepo(w, r, "/pulls/"+r.PathValue("number"))
	if rp == nil {
		return
	}
	pr := s.findPull(rp.ID, n)
	if pr == nil {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, s.pullJSON(r, rp, pr))
}

// comments 列出一个编号下的会话评论（review = false）或行内评审评论（review = true）。
func (s *Server) comments(w http.ResponseWriter, r *http.Request, review bool, tail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	rp := s.readableRepo(w, r, tail)
	if rp == nil {
		return
	}
	if s.findPull(rp.ID, n) == nil && (review || s.findIssue(rp.ID, n) == nil) {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	since := sinceOf(r)
	var hits []map[string]any
	for _, c := range s.Comments {
		if c.RepoID == rp.ID && c.Number == n && (c.Path != "") == review && !c.CreatedAt.Before(since) {
			hits = append(hits, s.commentJSON(r, rp, c))
		}
	}
	lo, hi := s.paginate(w, r, len(hits))
	out := []map[string]any{}
	out = append(out, hits[lo:hi]...)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listIssueComments(w http.ResponseWriter, r *http.Request) {
	s.comments(w, r, false, "/issues/"+r.PathValue("number")+"/comments")
}

func (s *Server) listReviewComments(w http.ResponseWriter, r *http.Request) {
	s.comments(w, r, true, "/pulls/"+r.PathValue("number")+"/comments")
}

// commitPulls 列出与提交相关的 PR：base 是该仓库，或头分支在该仓库（如从 fork 向上游开的 PR，与 GitHub 一致）。
func (s *Server) commitPulls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/commits/"+r.PathValue("sha")+"/pulls")
	if rp == nil {
		return
	}
	var hits []map[string]any
	for _, pr := range s.Pulls {
		if pr.HeadSHA != r.PathValue("sha") {
			continue
		}
		if pr.RepoID == rp.ID {
			hits = append(hits, s.pullJSON(r, rp, pr))
		} else if base := s.repoByID(pr.RepoID); base != nil && pr.HeadRepoID == rp.ID {
			hits = append(hits, s.pullJSON(r, base, pr))
		}
	}
	lo, hi := s.paginate(w, r, len(hits))
	out := []map[string]any{}
	out = append(out, hits[lo:hi]...)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) checkRuns(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/commits/"+r.PathValue("sha")+"/check-runs")
	if rp == nil {
		return
	}
	runs := []map[string]any{}
	for _, c := range s.CheckRuns[r.PathValue("sha")] {
		status := c.Status
		if status == "" {
			status = "completed"
		}
		runs = append(runs, map[string]any{"name": c.Name, "status": status, "conclusion": nullable(c.Conclusion), "html_url": c.URL})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(runs), "check_runs": runs})
}

func (s *Server) commitStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/commits/"+r.PathValue("sha")+"/status")
	if rp == nil {
		return
	}
	combined := "success"
	statuses := []map[string]any{}
	for _, c := range s.Statuses[r.PathValue("sha")] {
		statuses = append(statuses, map[string]any{"context": c.Context, "state": c.State, "target_url": nullable(c.URL)})
		switch c.State {
		case "failure", "error":
			combined = "failure"
		case "pending":
			if combined == "success" {
				combined = "pending"
			}
		}
	}
	if len(statuses) == 0 {
		combined = "pending"
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": combined, "sha": r.PathValue("sha"), "total_count": len(statuses), "statuses": statuses})
}

// searchIssues 支持 `<词…> repo:<owner/repo>`：标题或正文含全部词（忽略大小写）。
func (s *Server) searchIssues(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var repo string
	var words []string
	for _, f := range strings.Fields(r.URL.Query().Get("q")) {
		if v, ok := strings.CutPrefix(f, "repo:"); ok {
			repo = v
			continue
		}
		words = append(words, strings.ToLower(f))
	}
	owner, name, _ := strings.Cut(repo, "/")
	rp, _ := s.lookupRepo(owner, name)
	if rp == nil || !s.canRead(bearer(r), rp) {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	var hits []map[string]any
	for _, e := range s.issuesOf(rp) {
		text := strings.ToLower(e.is.Title + "\n" + e.is.Body)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(text, w) }) {
			hits = append(hits, s.issueJSON(r, rp, e.is, e.pr))
		}
	}
	lo, hi := s.paginate(w, r, len(hits))
	items := []map[string]any{}
	items = append(items, hits[lo:hi]...)
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(hits), "incomplete_results": false, "items": items})
}

// graphql 支持步骤 5.2 的 PR 头提交 statusCheckRollup 查询，以及 gh 用到的几个写 mutation（M15，见 mutate）。
func (s *Server) graphql(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var in struct {
		Query         string          `json:"query"`
		OperationName string          `json:"operationName"`
		Variables     json.RawMessage `json:"variables"`
	}
	var vars struct {
		Owner  string `json:"owner"`
		Name   string `json:"name"`
		Number int64  `json:"number"`
	}
	var anyVars map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		fail(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	if len(in.Variables) > 0 {
		if json.Unmarshal(in.Variables, &anyVars) != nil {
			fail(w, http.StatusBadRequest, "Problems parsing JSON")
			return
		}
		_ = json.Unmarshal(in.Variables, &vars)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := bearer(r)
	if s.userOf(tok) == nil {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	if strings.Contains(in.Query, "authors(first") {
		s.commitAuthors(w, tok, anyVars)
		return
	}
	if doc, err := parser.ParseQuery(&ast.Source{Input: in.Query}); err == nil {
		for _, op := range doc.Operations {
			if op.Operation == ast.Mutation && (in.OperationName == "" || op.Name == in.OperationName) {
				s.graphqlMutation(w, r, op, anyVars)
				return
			}
		}
		// gh 的只读查询（RepositoryInfo、RepositoryNetwork、PullRequestForBranch 等，见 gqlquery.go）
		if op := doc.Operations.ForName(in.OperationName); op != nil && !strings.Contains(in.Query, "statusCheckRollup") && gqlGeneric(op, doc.Fragments) {
			if anyVars == nil {
				anyVars = map[string]any{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": gqlExec(s.gqlRoot(r, s.userOf(tok)), op.SelectionSet, doc.Fragments, anyVars)})
			return
		}
	}
	if strings.Contains(in.Query, "nameWithOwner") {
		// node(id) { ... on Repository { nameWithOwner } }：按仓库 node id 取 owner/name
		id, _ := anyVars["id"].(string)
		var node any
		if rp := s.repoByNode(id); rp != nil {
			node = map[string]any{"nameWithOwner": rp.FullName}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"node": node}})
		return
	}
	if !strings.Contains(in.Query, "statusCheckRollup") {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []map[string]any{{"message": "unsupported query"}}})
		return
	}
	rp, to := s.lookupRepo(vars.Owner, vars.Name)
	if rp == nil && to != "" {
		owner, name, _ := strings.Cut(to, "/")
		rp, _ = s.lookupRepo(owner, name)
	}
	if rp == nil || !s.canRead(tok, rp) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": nil},
			"errors": []map[string]any{{"message": "Could not resolve to a Repository"}}})
		return
	}
	pr := s.findPull(rp.ID, vars.Number)
	if pr == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": nil}},
			"errors": []map[string]any{{"message": "Could not resolve to a PullRequest"}}})
		return
	}
	var rollup any
	if st := s.Rollups[pr.HeadSHA]; st != "" {
		rollup = map[string]any{"state": st}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"headRefOid": pr.HeadSHA,
		"commits":    map[string]any{"nodes": []map[string]any{{"commit": map[string]any{"statusCheckRollup": rollup}}}},
	}}}})
}

// commitAuthors 回答 `repository(owner,name){object(oid){... on Commit{authors{nodes{email name user{login avatarUrl}}}}}}`。
// 调用方持锁。
func (s *Server) commitAuthors(w http.ResponseWriter, tok string, vars map[string]any) {
	owner, _ := vars["owner"].(string)
	name, _ := vars["name"].(string)
	oid, _ := vars["oid"].(string)
	rp, _ := s.lookupRepo(owner, name)
	if rp == nil || !s.canRead(tok, rp) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": nil},
			"errors": []map[string]any{{"message": "Could not resolve to a Repository"}}})
		return
	}
	authors, ok := s.CommitAuthors[oid]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{"object": nil}}})
		return
	}
	nodes := make([]map[string]any, len(authors))
	for i, a := range authors {
		var user any
		if a.Login != "" {
			user = map[string]any{"login": a.Login, "avatarUrl": a.AvatarURL}
		}
		nodes[i] = map[string]any{"email": a.Email, "name": a.Name, "user": user}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{
		"object": map[string]any{"authors": map[string]any{"nodes": nodes}},
	}}})
}

// graphqlMutation 依次执行 mutation 的根字段（只认直接写出的字段，不展开 fragment），按别名回结果。调用方持锁。
func (s *Server) graphqlMutation(w http.ResponseWriter, r *http.Request, op *ast.OperationDefinition, vars map[string]any) {
	u := s.userOf(bearer(r))
	if u == nil {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []map[string]any{{"message": "Resource not accessible by integration"}}})
		return
	}
	data := map[string]any{}
	var errs []map[string]any
	for _, sel := range op.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		in := map[string]any{}
		for _, a := range f.Arguments {
			if a.Name != "input" {
				continue
			}
			if v, err := a.Value.Value(vars); err == nil {
				if m, ok := v.(map[string]any); ok {
					in = m
				}
			}
		}
		res, err := s.mutate(r, u, f.Name, in)
		data[f.Alias] = res
		if err != nil {
			errs = append(errs, map[string]any{"message": err.Error(), "path": []string{f.Alias}})
		}
	}
	out := map[string]any{"data": data}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	writeJSON(w, http.StatusOK, out)
}

// mutate 执行一个 mutation 字段：createPullRequest、addComment、addPullRequestReview、submitPullRequestReview、
// mergePullRequest（附录 A 的形态；响应给出 id / url 等常用字段）。调用方持锁。
func (s *Server) mutate(r *http.Request, u *User, name string, in map[string]any) (map[string]any, error) {
	str := func(k string) string { v, _ := in[k].(string); return v }
	now := time.Now().UTC().Truncate(time.Second)
	switch name {
	case "createPullRequest":
		rp := s.repoByNode(str("repositoryId"))
		if rp == nil {
			return nil, fmt.Errorf("Could not resolve to a node with the global id of '%s'", str("repositoryId"))
		}
		if str("baseRefName") == "" || str("headRefName") == "" {
			return nil, fmt.Errorf("baseRefName and headRefName are required")
		}
		var n int64
		for _, is := range s.issuesOf(rp) {
			n = max(n, is.is.Number)
		}
		headRepoID, head, headSHA := s.pullHead(rp, str("headRepositoryId"), str("headRefName"))
		draft, _ := in["draft"].(bool)
		pr := &Pull{
			Issue:   Issue{RepoID: rp.ID, Number: n + 1, Title: str("title"), Body: str("body"), State: "open", Author: u.Login, UpdatedAt: now},
			HeadRef: head, HeadSHA: headSHA, BaseRef: str("baseRefName"), Draft: draft,
		}
		if headRepoID != rp.ID {
			pr.HeadRepoID = headRepoID
		}
		s.Pulls = append(s.Pulls, pr)
		return map[string]any{"pullRequest": s.pullGQL(r, rp, pr)}, nil
	case "addComment":
		rp, number, isPull := s.subjectByNode(str("subjectId"))
		if rp == nil {
			return nil, fmt.Errorf("Could not resolve to a node with the global id of '%s'", str("subjectId"))
		}
		if str("body") == "" {
			return nil, fmt.Errorf("body is required")
		}
		s.seq++
		c := &Comment{ID: 1000 + s.seq, RepoID: rp.ID, Number: number, Author: u.Login, Body: str("body"), CreatedAt: now}
		s.Comments = append(s.Comments, c)
		kind := "issues"
		if isPull {
			kind = "pull"
		}
		url := fmt.Sprintf("%s/%s/%s/%d#issuecomment-%d", s.baseURL(r), rp.FullName, kind, number, c.ID)
		return map[string]any{
			"commentEdge": map[string]any{"node": map[string]any{"id": CommentNodeID(c.ID), "databaseId": c.ID, "url": url}},
			"subject":     map[string]any{"id": str("subjectId")},
		}, nil
	case "addPullRequestReview":
		rp, pr := s.pullByNode(str("pullRequestId"))
		if pr == nil {
			return nil, fmt.Errorf("Could not resolve to a node with the global id of '%s'", str("pullRequestId"))
		}
		s.seq++
		rv := &Review{ID: 1000 + s.seq, RepoID: rp.ID, Number: pr.Number, Author: u.Login, Body: str("body"), State: reviewState(str("event"))}
		s.Reviews = append(s.Reviews, rv)
		return map[string]any{"pullRequestReview": s.reviewGQL(r, rp, rv), "clientMutationId": nil}, nil
	case "submitPullRequestReview":
		var rv *Review
		if id := str("pullRequestReviewId"); id != "" {
			rv = s.reviewByNode(id)
		} else if _, pr := s.pullByNode(str("pullRequestId")); pr != nil {
			for _, x := range s.Reviews {
				if x.RepoID == pr.RepoID && x.Number == pr.Number && x.Author == u.Login && x.State == "PENDING" {
					rv = x
				}
			}
		}
		if rv == nil || rv.State != "PENDING" || rv.Author != u.Login {
			return nil, fmt.Errorf("Could not resolve to a pending review")
		}
		if st := reviewState(str("event")); st != "PENDING" {
			rv.State = st
		} else {
			return nil, fmt.Errorf("event is required")
		}
		if b := str("body"); b != "" {
			rv.Body = b
		}
		return map[string]any{"pullRequestReview": s.reviewGQL(r, s.repoByID(rv.RepoID), rv), "clientMutationId": nil}, nil
	case "mergePullRequest":
		rp, pr := s.pullByNode(str("pullRequestId"))
		if pr == nil {
			return nil, fmt.Errorf("Could not resolve to a node with the global id of '%s'", str("pullRequestId"))
		}
		if pr.State != "open" || !pr.MergedAt.IsZero() || pr.Draft {
			return nil, fmt.Errorf("Pull Request is not mergeable")
		}
		pr.MergedAt, pr.UpdatedAt = now, now
		return map[string]any{"pullRequest": s.pullGQL(r, rp, pr)}, nil
	}
	return nil, fmt.Errorf("unsupported mutation %s", name)
}

// reviewState 把 review 的 event 换成状态；空 = 待提交。
func reviewState(event string) string {
	switch strings.ToUpper(event) {
	case "APPROVE":
		return "APPROVED"
	case "REQUEST_CHANGES":
		return "CHANGES_REQUESTED"
	case "COMMENT":
		return "COMMENTED"
	}
	return "PENDING"
}

func (s *Server) pullGQL(r *http.Request, rp *Repo, pr *Pull) map[string]any {
	state := "OPEN"
	switch {
	case !pr.MergedAt.IsZero():
		state = "MERGED"
	case pr.State == "closed":
		state = "CLOSED"
	}
	return map[string]any{
		"id": PullNodeID(rp.ID, pr.Number), "number": pr.Number, "state": state, "merged": !pr.MergedAt.IsZero(),
		"url": fmt.Sprintf("%s/%s/pull/%d", s.baseURL(r), rp.FullName, pr.Number),
	}
}

func (s *Server) reviewGQL(r *http.Request, rp *Repo, rv *Review) map[string]any {
	return map[string]any{
		"id": ReviewNodeID(rv.ID), "databaseId": rv.ID, "state": rv.State, "body": rv.Body,
		"url": fmt.Sprintf("%s/%s/pull/%d#pullrequestreview-%d", s.baseURL(r), rp.FullName, rv.Number, rv.ID),
	}
}

// nodeNums 解析 "<prefix>_<a>[_<b>]" 形式的假 node id。
func nodeNums(id, prefix string, n int) []int64 {
	rest, ok := strings.CutPrefix(id, prefix+"_")
	if !ok {
		return nil
	}
	parts := strings.Split(rest, "_")
	if len(parts) != n {
		return nil
	}
	out := make([]int64, n)
	for i, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil
		}
		out[i] = v
	}
	return out
}

// repoByID / repoByNode / pullByNode / reviewByNode / subjectByNode 按 id 或 node id 查找。调用方持锁。
func (s *Server) repoByID(id int64) *Repo {
	for _, rp := range s.Repos {
		if rp.ID == id {
			return rp
		}
	}
	return nil
}

func (s *Server) repoByNode(id string) *Repo {
	if v := nodeNums(id, "R", 1); v != nil {
		return s.repoByID(v[0])
	}
	return nil
}

func (s *Server) pullByNode(id string) (*Repo, *Pull) {
	v := nodeNums(id, "PR", 2)
	if v == nil {
		return nil, nil
	}
	rp, pr := s.repoByID(v[0]), s.findPull(v[0], v[1])
	if rp == nil || pr == nil {
		return nil, nil
	}
	return rp, pr
}

func (s *Server) reviewByNode(id string) *Review {
	if v := nodeNums(id, "PRR", 1); v != nil {
		for _, rv := range s.Reviews {
			if rv.ID == v[0] {
				return rv
			}
		}
	}
	return nil
}

// subjectByNode 解析 addComment 的 subjectId：issue 或 PR。
func (s *Server) subjectByNode(id string) (rp *Repo, number int64, isPull bool) {
	if rp, pr := s.pullByNode(id); pr != nil {
		return rp, pr.Number, true
	}
	if v := nodeNums(id, "I", 2); v != nil {
		if rp := s.repoByID(v[0]); rp != nil && s.findIssue(v[0], v[1]) != nil {
			return rp, v[1], false
		}
	}
	return nil, 0, false
}

// createFork 以令牌对应的用户（或 body 的 organization）fork 仓库：已有同名仓库时原样返回它（同 GitHub），否则新建；回 202。
func (s *Server) createFork(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, u := s.writer(w, r, "/forks")
	if rp == nil {
		return
	}
	var in struct {
		Organization string `json:"organization"`
		Name         string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	owner := u.Login
	if in.Organization != "" {
		owner = in.Organization
	}
	name := in.Name
	if name == "" {
		_, name, _ = strings.Cut(rp.FullName, "/")
	}
	fork, _ := s.lookupRepo(owner, name)
	if fork == nil {
		s.seq++
		fork = &Repo{ID: 5000 + s.seq, FullName: owner + "/" + name, OwnerID: u.ID, DefaultBranch: rp.DefaultBranch, Private: rp.Private}
		s.Repos = append(s.Repos, fork)
		if s.forkParent == nil {
			s.forkParent = map[int64]int64{}
		}
		s.forkParent[fork.ID] = rp.ID
	}
	if err := s.forkBare(rp.FullName, fork.FullName); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	m := s.repoJSON(r, fork)
	m["node_id"] = RepoNodeID(fork.ID)
	m["url"] = s.baseURL(r) + "/repos/" + fork.FullName
	m["fork"] = true
	m["parent"] = map[string]any{"id": rp.ID, "node_id": RepoNodeID(rp.ID), "full_name": rp.FullName}
	writeJSON(w, http.StatusAccepted, m)
}
