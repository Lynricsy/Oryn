package api

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/sandboxes"
)

// SandboxesDeps 是 agent 沙箱接口（M30）的依赖。
type SandboxesDeps struct {
	Service *sandboxes.Service
}

func (s *Server) sandboxesSvc(w http.ResponseWriter) (*sandboxes.Service, bool) {
	if s.Sandboxes == nil || s.Sandboxes.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "sandboxes service not configured")
		return nil, false
	}
	return s.Sandboxes.Service, true
}

// writeSandboxErr 写出沙箱服务的错误：sandbox_not_found → 404，其余 *sandboxes.Error → 400，其它按通用映射。
func writeSandboxErr(w http.ResponseWriter, r *http.Request, err error) {
	var se *sandboxes.Error
	if errors.As(err, &se) {
		status := http.StatusBadRequest
		if se.Code == sandboxes.CodeNotFound {
			status = http.StatusNotFound
		}
		WriteError(w, status, se.Code, se.Message)
		return
	}
	writeErr(w, r, err)
}

// ListSandboxes 实现 GET /api/v1/sandboxes。
func (s *Server) ListSandboxes(w http.ResponseWriter, r *http.Request, params apigen.ListSandboxesParams) {
	svc, ok := s.sandboxesSvc(w)
	if !ok {
		return
	}
	list, err := svc.List(r.Context(), deref(params.Thread))
	if err != nil {
		writeSandboxErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.SandboxList{Sandboxes: list})
}

// DeleteSandbox 实现 DELETE /api/v1/sandboxes/{sandbox}（主人销毁，原因记为 owner）。
func (s *Server) DeleteSandbox(w http.ResponseWriter, r *http.Request, sandbox string) {
	svc, ok := s.sandboxesSvc(w)
	if !ok {
		return
	}
	if err := svc.Destroy(r.Context(), sandbox); err != nil {
		writeSandboxErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
