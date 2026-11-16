package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// ListGitHubTasks 对应 `GET /api/v1/github/tasks`：issue 任务与发布哨兵的状态（R24 的 status）。
func (s *Server) ListGitHubTasks(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	tasks, err := svc.Tasks(r.Context())
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitHubTaskList{Tasks: tasks})
}

// CleanupGitHubTask 对应 `POST /api/v1/github/tasks/cleanup`：把 issue 任务置为 abandoned 并冻结 orb（不改动远端）。
func (s *Server) CleanupGitHubTask(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	var req apigen.GitHubTaskRef
	if !decodeJSON(w, r, &req) {
		return
	}
	task, err := svc.CleanupTaskAPI(r.Context(), req.Repo, req.Number)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, task)
}

// TriageGitHubIssue 对应 `POST /api/v1/github/triage`：拉取线上 issue，合成 issues.opened 交给触发规则，回线程 id。
func (s *Server) TriageGitHubIssue(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.githubSvc(w)
	if !ok {
		return
	}
	var req apigen.GitHubTriageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var rule string
	if req.RuleID != nil {
		rule = *req.RuleID
	}
	id, err := svc.Triage(r.Context(), req.Repo, req.Number, rule)
	if err != nil {
		writeGitHubErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitHubTriageResult{ThreadID: id})
}
