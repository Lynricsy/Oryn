package api

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// runner 与 worktree 端点（M11b，计划 7.4）。

// ListRunners 实现 GET /api/v1/runners。
func (s *Server) ListRunners(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	list, err := svc.ListRunners(r.Context())
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.RunnerList{Runners: list})
}

// CreateRunner 实现 POST /api/v1/runners；ort_ 令牌只在本响应中出现。
func (s *Server) CreateRunner(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	var req apigen.RunnerCreate
	if !decodeJSON(w, r, &req) {
		return
	}
	// 与 provider 名同一规则、同一唯一约束
	if !providerNameRe.MatchString(req.Name) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "runner 名须匹配 ^[a-z0-9][a-z0-9_-]*$，至多 64 字符")
		return
	}
	runner, token, err := svc.CreateRunner(r.Context(), req.Name)
	if isUniqueViolation(err) {
		WriteError(w, http.StatusConflict, "conflict", "已有同名 runner 或 provider")
		return
	}
	if err != nil {
		writeOrbErr(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, apigen.RunnerCreated{Runner: runner, Token: token})
}

// DeleteRunner 实现 DELETE /api/v1/runners/{id}：令牌立即失效、orb 进入 deleted，线程保留转录。
func (s *Server) DeleteRunner(w http.ResponseWriter, r *http.Request, id apigen.ProviderIDPath) {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteRunner(r.Context(), id); err != nil {
		if errors.Is(err, orbs.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "not_found", "runner 不存在")
			return
		}
		writeOrbErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveThreadWorktree 实现 POST /api/v1/threads/{id}/worktree/remove。
func (s *Server) RemoveThreadWorktree(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	if err := svc.RemoveWorktree(r.Context(), id); err != nil {
		writeThreadErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
