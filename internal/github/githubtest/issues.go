package githubtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
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
}

// Pull 是一个 PR。
type Pull struct {
	Issue
	Draft    bool
	MergedAt time.Time // 非零 = 已合并
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
}

// issueRoutes 登记 issue、PR、评论、提交检查与 GraphQL 端点。
func (s *Server) issueRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues", s.listIssues)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}", s.getIssue)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}/comments", s.listIssueComments)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}", s.getPull)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}/comments", s.listReviewComments)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/pulls", s.commitPulls)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/check-runs", s.checkRuns)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{sha}/status", s.commitStatus)
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

// issueJSON 是 issue（或 PR 的 issue 视图，pr 非 nil）的 REST 表示。调用方持锁。
func (s *Server) issueJSON(r *http.Request, rp *Repo, is *Issue, pr *Pull) map[string]any {
	labels := []map[string]any{}
	for _, l := range is.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	m := map[string]any{
		"number": is.Number, "title": is.Title, "body": nullable(is.Body), "state": is.State,
		"html_url": fmt.Sprintf("%s/%s/issues/%d", s.baseURL(r), rp.FullName, is.Number),
		"user":     userJSON(is.Author), "labels": labels, "updated_at": ts(is.UpdatedAt), "created_at": ts(is.UpdatedAt),
	}
	if pr != nil {
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
		head = map[string]any{"id": pr.HeadRepoID, "full_name": fmt.Sprintf("fork-%d/%s", pr.HeadRepoID, rp.FullName[strings.Index(rp.FullName, "/")+1:])}
	}
	return map[string]any{
		"number": pr.Number, "title": pr.Title, "body": nullable(pr.Body), "state": state, "draft": pr.Draft,
		"merged": !pr.MergedAt.IsZero(), "merged_at": ts(pr.MergedAt),
		"html_url":  fmt.Sprintf("%s/%s/pull/%d", s.baseURL(r), rp.FullName, pr.Number),
		"user":      userJSON(pr.Author),
		"head":      map[string]any{"ref": pr.HeadRef, "sha": pr.HeadSHA, "repo": head},
		"base":      map[string]any{"ref": pr.BaseRef, "repo": map[string]any{"id": rp.ID, "full_name": rp.FullName}},
		"additions": pr.Additions, "deletions": pr.Deletions, "changed_files": pr.ChangedFiles,
		"updated_at": ts(pr.UpdatedAt),
	}
}

func commentJSON(c *Comment) map[string]any {
	m := map[string]any{"id": c.ID, "user": userJSON(c.Author), "body": c.Body, "created_at": ts(c.CreatedAt)}
	if c.Path != "" {
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
	all := s.issuesOf(rp)
	var hits []map[string]any
	for _, e := range all {
		if state == "all" || e.is.State == state {
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
	var hits []map[string]any
	for _, c := range s.Comments {
		if c.RepoID == rp.ID && c.Number == n && (c.Path != "") == review {
			hits = append(hits, commentJSON(c))
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

func (s *Server) commitPulls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/commits/"+r.PathValue("sha")+"/pulls")
	if rp == nil {
		return
	}
	var hits []map[string]any
	for _, pr := range s.Pulls {
		if pr.RepoID == rp.ID && pr.HeadSHA == r.PathValue("sha") {
			hits = append(hits, s.pullJSON(r, rp, pr))
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

// graphql 只支持步骤 5.2 的 PR 头提交 statusCheckRollup 查询。
func (s *Server) graphql(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query     string `json:"query"`
		Variables struct {
			Owner  string `json:"owner"`
			Name   string `json:"name"`
			Number int64  `json:"number"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := bearer(r)
	if s.userOf(tok) == nil && s.instTokens[tok] == 0 {
		fail(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	if !strings.Contains(in.Query, "statusCheckRollup") {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []map[string]any{{"message": "unsupported query"}}})
		return
	}
	rp, to := s.lookupRepo(in.Variables.Owner, in.Variables.Name)
	if rp == nil && to != "" {
		owner, name, _ := strings.Cut(to, "/")
		rp, _ = s.lookupRepo(owner, name)
	}
	if rp == nil || !s.canRead(tok, rp) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"repository": nil},
			"errors": []map[string]any{{"message": "Could not resolve to a Repository"}}})
		return
	}
	pr := s.findPull(rp.ID, in.Variables.Number)
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
