package githubtest

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"time"
)

// M15 步骤 3.1 的 REST 轮询：带 since / sort / direction 的列表、PR 列表与评审列表、issue 时间线事件、Actions 运行，
// 以及轮询接口的 ETag（If-None-Match 命中回 304）与核心额度头。

// IssueEvent 是 issue / PR 时间线上的一个事件（GET …/issues/{n}/events）。
type IssueEvent struct {
	ID     int64
	RepoID int64
	Number int64
	// Event 是时间线事件名：labeled | unlabeled | closed | reopened | merged | ready_for_review | convert_to_draft …
	Event     string
	Actor     string
	Label     string // labeled / unlabeled 的标签名
	CreatedAt time.Time
}

// WorkflowRun 是一次 Actions 运行（GET …/actions/runs）。
type WorkflowRun struct {
	ID         int64
	RepoID     int64
	Name       string
	Event      string // 缺省 push
	Status     string // 缺省 completed
	Conclusion string
	HeadSHA    string
	HeadBranch string
	Message    string // head 提交信息
	Actor      string
	UpdatedAt  time.Time
}

// AddIssueEvent 登记一个时间线事件（ID 缺省自增，CreatedAt 缺省当前时间）。
func (s *Server) AddIssueEvent(ev IssueEvent) *IssueEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.ID == 0 {
		s.seq++
		ev.ID = 9000 + s.seq
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	p := &ev
	s.IssueEvents = append(s.IssueEvents, p)
	return p
}

// AddReview 登记一次评审（ID 缺省自增，State 缺省 COMMENTED，SubmittedAt 缺省当前时间）。
func (s *Server) AddReview(rv Review) *Review {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rv.ID == 0 {
		s.seq++
		rv.ID = 1000 + s.seq
	}
	if rv.State == "" {
		rv.State = "COMMENTED"
	}
	if rv.SubmittedAt.IsZero() && rv.State != "PENDING" {
		rv.SubmittedAt = time.Now().UTC().Truncate(time.Second)
	}
	p := &rv
	s.Reviews = append(s.Reviews, p)
	return p
}

// AddRun 登记一次 Actions 运行（ID 缺省自增，Event 缺省 push，Status 缺省 completed，UpdatedAt 缺省当前时间）。
func (s *Server) AddRun(run WorkflowRun) *WorkflowRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.ID == 0 {
		s.seq++
		run.ID = 7000 + s.seq
	}
	if run.Event == "" {
		run.Event = "push"
	}
	if run.Status == "" {
		run.Status = "completed"
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	}
	p := &run
	s.Runs = append(s.Runs, p)
	return p
}

func (s *Server) watchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/comments", s.etagged(s.listRepoComments))
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}/events", s.etagged(s.listIssueEvents))
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls", s.etagged(s.listPulls))
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls/{number}/reviews", s.etagged(s.listReviews))
	mux.HandleFunc("GET /repos/{owner}/{repo}/actions/runs", s.etagged(s.listRuns))
}

// etagged 给 GET 接口加上 ETag：200 的响应体取摘要作 ETag，请求的 If-None-Match 与之相同时回 304（无正文）。
// CoreRemaining 非 0 时另写核心额度头。
func (s *Server) etagged(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		h(rec, r)
		s.mu.Lock()
		left, reset := s.CoreRemaining, s.CoreReset
		s.mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		if left != 0 {
			if reset.IsZero() {
				reset = time.Now().Add(time.Hour)
			}
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(max(left, 0)))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			w.Header().Set("X-RateLimit-Resource", "core")
		}
		if rec.Code == http.StatusOK {
			sum := sha256.Sum256(rec.Body.Bytes())
			tag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
			w.Header().Set("ETag", tag)
			if r.Header.Get("If-None-Match") == tag {
				w.Header().Del("Content-Type")
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}
}

// sinceOf 解析 since 参数；没有或无法解析时为零值（不过滤）。
func sinceOf(r *http.Request) time.Time {
	t, _ := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
	return t
}

// listRepoComments 是仓库全部 issue / PR 的会话评论（GET …/issues/comments）：since 过滤创建时间，按创建时间正序
// （direction=desc 时倒序）。
func (s *Server) listRepoComments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/issues/comments")
	if rp == nil {
		return
	}
	since := sinceOf(r)
	var cs []*Comment
	for _, c := range s.Comments {
		if c.RepoID == rp.ID && c.Path == "" && !c.CreatedAt.Before(since) {
			cs = append(cs, c)
		}
	}
	slices.SortStableFunc(cs, func(a, b *Comment) int { return a.CreatedAt.Compare(b.CreatedAt) })
	if r.URL.Query().Get("direction") == "desc" {
		slices.Reverse(cs)
	}
	lo, hi := s.paginate(w, r, len(cs))
	out := []map[string]any{}
	for _, c := range cs[lo:hi] {
		out = append(out, s.commentJSON(r, rp, c))
	}
	writeJSON(w, http.StatusOK, out)
}

// listIssueEvents 是一个 issue / PR 的时间线事件，按时间正序。
func (s *Server) listIssueEvents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	rp := s.readableRepo(w, r, "/issues/"+r.PathValue("number")+"/events")
	if rp == nil {
		return
	}
	if s.findPull(rp.ID, n) == nil && s.findIssue(rp.ID, n) == nil {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	var evs []*IssueEvent
	for _, ev := range s.IssueEvents {
		if ev.RepoID == rp.ID && ev.Number == n {
			evs = append(evs, ev)
		}
	}
	slices.SortStableFunc(evs, func(a, b *IssueEvent) int { return a.CreatedAt.Compare(b.CreatedAt) })
	lo, hi := s.paginate(w, r, len(evs))
	out := []map[string]any{}
	for _, ev := range evs[lo:hi] {
		m := map[string]any{"id": ev.ID, "node_id": "IE_" + strconv.FormatInt(ev.ID, 10), "event": ev.Event,
			"actor": userJSON(ev.Actor), "created_at": ts(ev.CreatedAt)}
		if ev.Label != "" {
			m["label"] = map[string]any{"name": ev.Label}
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

// listPulls 是仓库的 PR 列表：state（open | closed | all，缺省 open）、sort（created | updated，缺省 created）、
// direction（缺省 desc）。
func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/pulls")
	if rp == nil {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	var prs []*Pull
	for _, pr := range s.Pulls {
		open := pr.State == "open" && pr.MergedAt.IsZero()
		if pr.RepoID == rp.ID && (state == "all" || state == "open" && open || state == "closed" && !open) {
			prs = append(prs, pr)
		}
	}
	key := func(p *Pull) time.Time { return createdOf(&p.Issue) }
	if q.Get("sort") == "updated" {
		key = func(p *Pull) time.Time { return p.UpdatedAt }
	}
	slices.SortStableFunc(prs, func(a, b *Pull) int {
		if c := key(b).Compare(key(a)); c != 0 {
			return c
		}
		return int(b.Number - a.Number)
	})
	if q.Get("direction") == "asc" {
		slices.Reverse(prs)
	}
	lo, hi := s.paginate(w, r, len(prs))
	out := []map[string]any{}
	for _, pr := range prs[lo:hi] {
		out = append(out, s.pullJSON(r, rp, pr))
	}
	writeJSON(w, http.StatusOK, out)
}

// listReviews 是 PR 的评审列表（含待提交的，按提交顺序）。
func (s *Server) listReviews(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	rp := s.readableRepo(w, r, "/pulls/"+r.PathValue("number")+"/reviews")
	if rp == nil {
		return
	}
	if s.findPull(rp.ID, n) == nil {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	var rvs []*Review
	for _, rv := range s.Reviews {
		if rv.RepoID == rp.ID && rv.Number == n {
			rvs = append(rvs, rv)
		}
	}
	lo, hi := s.paginate(w, r, len(rvs))
	out := []map[string]any{}
	for _, rv := range rvs[lo:hi] {
		out = append(out, map[string]any{
			"id": rv.ID, "node_id": ReviewNodeID(rv.ID), "user": userJSON(rv.Author), "body": rv.Body, "state": rv.State,
			"author_association": s.assocOf(rp, rv.Author), "submitted_at": ts(rv.SubmittedAt),
			"html_url": s.baseURL(r) + "/" + rp.FullName + "/pull/" + strconv.FormatInt(rv.Number, 10) + "#pullrequestreview-" + strconv.FormatInt(rv.ID, 10),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// listRuns 是仓库的 Actions 运行（按更新时间倒序），支持 event / status / head_sha 过滤。
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/actions/runs")
	if rp == nil {
		return
	}
	q := r.URL.Query()
	var runs []*WorkflowRun
	for _, run := range s.Runs {
		if run.RepoID == rp.ID && (q.Get("event") == "" || q.Get("event") == run.Event) && (q.Get("status") == "" || q.Get("status") == run.Status) &&
			(q.Get("head_sha") == "" || q.Get("head_sha") == run.HeadSHA) {
			runs = append(runs, run)
		}
	}
	slices.SortStableFunc(runs, func(a, b *WorkflowRun) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	lo, hi := s.paginate(w, r, len(runs))
	items := []map[string]any{}
	for _, run := range runs[lo:hi] {
		items = append(items, map[string]any{
			"id": run.ID, "name": run.Name, "event": run.Event, "status": run.Status, "conclusion": nullable(run.Conclusion),
			"head_sha": run.HeadSHA, "head_branch": run.HeadBranch, "head_commit": map[string]any{"id": run.HeadSHA, "message": run.Message},
			"actor": userJSON(run.Actor), "triggering_actor": userJSON(run.Actor), "updated_at": ts(run.UpdatedAt),
			"html_url": s.baseURL(r) + "/" + rp.FullName + "/actions/runs/" + strconv.FormatInt(run.ID, 10),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(runs), "workflow_runs": items})
}
