package api

// owner_memory.go：M17 全局助手主人记忆的管理接口。

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/ownermem"
)

// OwnerMemoryDeps 是 M17 主人记忆接口的依赖。
type OwnerMemoryDeps struct {
	Service *ownermem.Service
}

func (s *Server) ownerMemorySvc(w http.ResponseWriter) (*ownermem.Service, bool) {
	if s.OwnerMemory == nil || s.OwnerMemory.Service == nil {
		WriteError(w, http.StatusNotImplemented, "owner_memory_unavailable", "助手记忆服务未装配")
		return nil, false
	}
	return s.OwnerMemory.Service, true
}

// writeOwnerMemoryErr 写出主人记忆的错误：领域错误按自带的状态与码，其余按通用映射。
func writeOwnerMemoryErr(w http.ResponseWriter, r *http.Request, err error) {
	var me *memory.Error
	if errors.As(err, &me) {
		WriteError(w, me.Status, me.Code, me.Message)
		return
	}
	writeErr(w, r, err)
}

// GetOwnerMemorySettings 对应 `GET /api/v1/owner-memory/settings`。
func (s *Server) GetOwnerMemorySettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Settings(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutOwnerMemorySettings 对应 `PUT /api/v1/owner-memory/settings`。
func (s *Server) PutOwnerMemorySettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	var in apigen.OwnerMemorySettingsUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutSettings(r.Context(), in)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutOwnerMemoryDecideEndpoint 对应 `PUT /api/v1/owner-memory/decide-endpoint`。
func (s *Server) PutOwnerMemoryDecideEndpoint(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	var in apigen.OwnerMemoryDecideUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutDecideEndpoint(r.Context(), in)
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// DeleteOwnerMemoryDecideEndpoint 对应 `DELETE /api/v1/owner-memory/decide-endpoint`。
func (s *Server) DeleteOwnerMemoryDecideEndpoint(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.DeleteDecideEndpoint(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetOwnerMemoryStatus 对应 `GET /api/v1/owner-memory/status`。
func (s *Server) GetOwnerMemoryStatus(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.ownerMemorySvc(w)
	if !ok {
		return
	}
	out, err := svc.Status(r.Context())
	if err != nil {
		writeOwnerMemoryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}
