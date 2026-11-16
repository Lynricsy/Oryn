package api

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
)

// MemoryDeps 是 M16 记忆管理接口的依赖。
type MemoryDeps struct {
	Service *memory.Service
}

func (s *Server) memorySvc(w http.ResponseWriter) (*memory.Service, bool) {
	if s.Memory == nil || s.Memory.Service == nil {
		WriteError(w, http.StatusNotImplemented, "memory_unavailable", "记忆服务未装配")
		return nil, false
	}
	return s.Memory.Service, true
}

// writeMemoryErr 写出记忆服务的错误：领域错误按自带的状态与码，其余按通用映射。
func writeMemoryErr(w http.ResponseWriter, r *http.Request, err error) {
	var me *memory.Error
	if errors.As(err, &me) {
		WriteError(w, me.Status, me.Code, me.Message)
		return
	}
	writeErr(w, r, err)
}

// GetMemorySettings 对应 `GET /api/v1/memory/settings`。
func (s *Server) GetMemorySettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Settings(r.Context())
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutMemorySettings 对应 `PUT /api/v1/memory/settings`。
func (s *Server) PutMemorySettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemorySettingsUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutSettings(r.Context(), in)
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetMemoryStatus 对应 `GET /api/v1/memory/status`。
func (s *Server) GetMemoryStatus(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Status(r.Context())
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateMemoryEnvFile 对应 `POST /api/v1/memory/env-file`。
func (s *Server) CreateMemoryEnvFile(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	out, err := svc.GenerateEnvFile(r.Context())
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, out)
}

// ListMemoryProjects 对应 `GET /api/v1/memory/projects`。
func (s *Server) ListMemoryProjects(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	out, err := svc.ListProjects(r.Context())
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutMemoryProject 对应 `PUT /api/v1/memory/projects/{id}`。
func (s *Server) PutMemoryProject(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	svc, ok := s.memorySvc(w)
	if !ok {
		return
	}
	var in apigen.MemoryProjectUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutProject(r.Context(), id, in.Enabled)
	if err != nil {
		writeMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}
