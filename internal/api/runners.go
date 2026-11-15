package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// runner 与 worktree 端点（M11b）：契约已落地，实现由后续切片补上，此前一律 501。

// ListRunners 实现 GET /api/v1/runners。
func (s *Server) ListRunners(w http.ResponseWriter, _ *http.Request) {
	writeRunnersNotImplemented(w)
}

// CreateRunner 实现 POST /api/v1/runners。
func (s *Server) CreateRunner(w http.ResponseWriter, _ *http.Request) {
	writeRunnersNotImplemented(w)
}

// DeleteRunner 实现 DELETE /api/v1/runners/{id}。
func (s *Server) DeleteRunner(w http.ResponseWriter, _ *http.Request, _ apigen.ProviderIDPath) {
	writeRunnersNotImplemented(w)
}

// RemoveThreadWorktree 实现 POST /api/v1/threads/{id}/worktree/remove。
func (s *Server) RemoveThreadWorktree(w http.ResponseWriter, _ *http.Request, _ apigen.ThreadIDPath) {
	writeRunnersNotImplemented(w)
}

func writeRunnersNotImplemented(w http.ResponseWriter) {
	WriteError(w, http.StatusNotImplemented, "not_implemented", "runner 尚未实现")
}
