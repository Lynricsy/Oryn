package githubtest

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// 本文件是 M15 issue 任务用到的端点（步骤 5.2 / 5.5）：给 issue 加标签、改 issue 状态、列评论上的反应。

// Reaction 是评论上的一个反应（content 取 GitHub 的取值：+1、-1、eyes …）。
type Reaction struct {
	ID        int64
	CommentID int64
	Content   string
	User      string
}

func (s *Server) taskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/labels", s.addIssueLabels)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/issues/{number}", s.updateIssue)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/comments/{id}/reactions", s.listCommentReactions)
}

// AddReaction 以 user 的身份给评论加一个反应。
func (s *Server) AddReaction(commentID int64, user, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.Reactions = append(s.Reactions, &Reaction{ID: s.seq, CommentID: commentID, Content: content, User: user})
}

// IssueState 返回 issue 的状态（open / closed）；没有该 issue 时为空串。
func (s *Server) IssueState(repoID, number int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if is := s.findIssue(repoID, number); is != nil {
		return is.State
	}
	return ""
}

// IssueLabels 返回 issue 的标签。
func (s *Server) IssueLabels(repoID, number int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if is := s.findIssue(repoID, number); is != nil {
		return append([]string(nil), is.Labels...)
	}
	return nil
}

func (s *Server) addIssueLabels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	rp, _ := s.writer(w, r, "/issues/"+r.PathValue("number")+"/labels")
	if rp == nil {
		return
	}
	is := s.findIssue(rp.ID, n)
	if is == nil {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	var in struct {
		Labels []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Labels) == 0 {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	for _, l := range in.Labels {
		if !slices.ContainsFunc(is.Labels, func(x string) bool { return strings.EqualFold(x, l) }) {
			is.Labels = append(is.Labels, l)
		}
	}
	is.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	out := []map[string]any{}
	for _, l := range is.Labels {
		out = append(out, map[string]any{"name": l})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	rp, _ := s.writer(w, r, "/issues/"+r.PathValue("number"))
	if rp == nil {
		return
	}
	is := s.findIssue(rp.ID, n)
	if is == nil {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	var in struct {
		State       string `json:"state"`
		StateReason string `json:"state_reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.State != "" && in.State != "open" && in.State != "closed" {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	if in.State != "" {
		is.State = in.State
	}
	is.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	writeJSON(w, http.StatusOK, s.issueJSON(r, rp, is, nil))
}

func (s *Server) listCommentReactions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp := s.readableRepo(w, r, "/issues/comments/"+r.PathValue("id")+"/reactions")
	if rp == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	content := r.URL.Query().Get("content")
	var hits []map[string]any
	for _, x := range s.Reactions {
		if x.CommentID == id && (content == "" || x.Content == content) {
			hits = append(hits, map[string]any{"id": x.ID, "content": x.Content, "user": userJSON(x.User)})
		}
	}
	lo, hi := s.paginate(w, r, len(hits))
	out := []map[string]any{}
	out = append(out, hits[lo:hi]...)
	writeJSON(w, http.StatusOK, out)
}
