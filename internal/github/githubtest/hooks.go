package githubtest

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// hookRoutes 登记协作者权限、反应与评论端点（M14 阶段 4、7）。
func (s *Server) hookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /repos/{owner}/{repo}/collaborators/{login}/permission", s.collaboratorPermission)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/reactions", s.createReaction)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/comments/{id}/reactions", s.createReaction)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/comments", s.createIssueComment)
}

func newGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// collaboratorPermission 按仓库的协作者权限表回答；表里没有的已知用户为 none，未知用户 404。
// 与真实 GitHub 一样要求调用者有 push 权限，否则回 403（docs/plan-m15.md 附录 B-9）。
func (s *Server) collaboratorPermission(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, to := s.lookupRepo(r.PathValue("owner"), r.PathValue("repo"))
	if to != "" {
		s.redirectRepo(w, r, to, "/collaborators/"+r.PathValue("login")+"/permission")
		return
	}
	if rp == nil || !s.canRead(bearer(r), rp) {
		fail(w, http.StatusNotFound, "Not Found")
		return
	}
	if u := s.userOf(bearer(r)); u == nil || !permissionsJSON(s.permOf(rp, u.Login))["push"] {
		fail(w, http.StatusForbidden, "Must have push access to view collaborator permission.")
		return
	}
	login := r.PathValue("login")
	perm, known := "", false
	for k, v := range rp.Permissions {
		if strings.EqualFold(k, login) {
			perm, known = v, true
		}
	}
	if !known {
		if _, ok := s.Users[strings.ToLower(login)]; !ok {
			fail(w, http.StatusNotFound, "Not Found")
			return
		}
		perm = "none"
	}
	writeJSON(w, http.StatusOK, map[string]any{"permission": perm, "role_name": perm, "user": map[string]any{"login": login, "type": "User"}})
}

// writer 返回能写该仓库的用户令牌对应的用户；失败时已写出响应。调用方持锁。
func (s *Server) writer(w http.ResponseWriter, r *http.Request, tail string) (*Repo, *User) {
	rp, to := s.lookupRepo(r.PathValue("owner"), r.PathValue("repo"))
	if to != "" {
		s.redirectRepo(w, r, to, tail)
		return nil, nil
	}
	u := s.userOf(bearer(r))
	if rp == nil || u == nil || !s.canRead(bearer(r), rp) {
		fail(w, http.StatusNotFound, "Not Found")
		return nil, nil
	}
	return rp, u
}

// createReaction 接受 issue 或评论上的反应；评论上的反应记进 Reactions（issue 上的只记进 Calls）。
func (s *Server) createReaction(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rp, u := s.writer(w, r, strings.TrimPrefix(r.URL.Path, "/repos/"+r.PathValue("owner")+"/"+r.PathValue("repo")))
	if rp == nil {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Content == "" {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	s.seq++
	if id, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err == nil {
		s.Reactions = append(s.Reactions, &Reaction{ID: s.seq, CommentID: id, Content: in.Content, User: u.Login})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": s.seq, "content": in.Content, "user": map[string]any{"login": u.Login}})
}

// createIssueComment 以令牌对应的用户在 issue / PR 下发一条会话评论。
func (s *Server) createIssueComment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	number, _ := strconv.ParseInt(r.PathValue("number"), 10, 64)
	rp, u := s.writer(w, r, fmt.Sprintf("/issues/%d/comments", number))
	if rp == nil {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == "" {
		fail(w, http.StatusUnprocessableEntity, "Validation Failed")
		return
	}
	s.seq++
	c := &Comment{ID: 1000 + s.seq, RepoID: rp.ID, Number: number, Author: u.Login, Body: in.Body, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	s.Comments = append(s.Comments, c)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": c.ID, "body": c.Body, "user": map[string]any{"login": u.Login, "type": "User"},
		"html_url":   fmt.Sprintf("%s/%s/issues/%d#issuecomment-%d", s.baseURL(r), rp.FullName, number, c.ID),
		"created_at": c.CreatedAt.Format(time.RFC3339),
	})
}
