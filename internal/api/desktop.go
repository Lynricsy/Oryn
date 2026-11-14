package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/desktop"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// DesktopDeps 是 M8 桌面与浏览器 Profile 接口的依赖。桌面 WebSocket（desktop.WSPath）经 Deps.Mounts 挂载。
type DesktopDeps struct {
	Service *desktop.Service
}

// desktopSvc 返回 Profile 服务；未注入时写出 501。
func (s *Server) desktopSvc(w http.ResponseWriter) (*desktop.Service, bool) {
	if s.Desktop == nil || s.Desktop.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "desktop service not configured")
		return nil, false
	}
	return s.Desktop.Service, true
}

// writeDesktopErr 把 Profile 服务的错误映射为响应：*desktop.Error 按其状态与码（profile_in_use 的体为
// ProfileInUseConflict），orb 通道的错误经 writeOrbHTTPErr。
func writeDesktopErr(w http.ResponseWriter, r *http.Request, err error) {
	var de *desktop.Error
	if errors.As(err, &de) {
		if body := de.ErrorBody(); body != nil {
			WriteJSON(w, de.Status, body)
			return
		}
		WriteError(w, de.Status, de.Code, de.Message)
		return
	}
	if isOrbChannelErr(err) {
		writeOrbHTTPErr(w, err, false)
		return
	}
	writeErr(w, r, err)
}

// ListBrowserProfiles 实现 GET /api/v1/browser-profiles。
func (s *Server) ListBrowserProfiles(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.desktopSvc(w)
	if !ok {
		return
	}
	list, err := svc.List(r.Context())
	if err != nil {
		writeDesktopErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.BrowserProfileList{Profiles: list})
}

// CreateBrowserProfile 实现 POST /api/v1/browser-profiles。
func (s *Server) CreateBrowserProfile(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.desktopSvc(w)
	if !ok {
		return
	}
	var req apigen.BrowserProfileCreate
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := svc.Create(r.Context(), req.Name)
	if err != nil {
		writeDesktopErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, p)
}

// UpdateBrowserProfile 实现 PATCH /api/v1/browser-profiles/{id}。
func (s *Server) UpdateBrowserProfile(w http.ResponseWriter, r *http.Request, id apigen.BrowserProfileIDPath) {
	svc, ok := s.desktopSvc(w)
	if !ok {
		return
	}
	var req apigen.BrowserProfilePatch
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := svc.Rename(r.Context(), id, req.Name)
	if err != nil {
		writeDesktopErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, p)
}

// DeleteBrowserProfile 实现 DELETE /api/v1/browser-profiles/{id}。
func (s *Server) DeleteBrowserProfile(w http.ResponseWriter, r *http.Request, id apigen.BrowserProfileIDPath) {
	svc, ok := s.desktopSvc(w)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), id); err != nil {
		writeDesktopErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetOrbDesktopProfile 实现 PUT /api/v1/orbs/{id}/desktop/profile。
func (s *Server) SetOrbDesktopProfile(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	svc, ok := s.desktopSvc(w)
	if !ok {
		return
	}
	var req apigen.OrbDesktopProfilePut
	if !decodeJSON(w, r, &req) {
		return
	}
	var mode apigen.BrowserProfileMode
	if req.ProfileMode != nil {
		mode = *req.ProfileMode
	}
	if err := svc.SetOrbProfile(r.Context(), id, req.ProfileID, mode); err != nil {
		writeDesktopErr(w, r, err)
		return
	}
	s.writeOrb(w, r, id, http.StatusOK)
}

// SaveOrbDesktopProfile 实现 POST /api/v1/orbs/{id}/desktop/profile/save。
func (s *Server) SaveOrbDesktopProfile(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	svc, ok := s.desktopSvc(w)
	if !ok {
		return
	}
	p, err := svc.Save(r.Context(), id)
	if err != nil {
		writeDesktopErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, p)
}

// StartOrbBrowser 实现 POST /api/v1/orbs/{id}/desktop/browser/start。
func (s *Server) StartOrbBrowser(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	s.setOrbBrowser(w, r, id, true)
}

// StopOrbBrowser 实现 POST /api/v1/orbs/{id}/desktop/browser/stop。
func (s *Server) StopOrbBrowser(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	s.setOrbBrowser(w, r, id, false)
}

func (s *Server) setOrbBrowser(w http.ResponseWriter, r *http.Request, id string, run bool) {
	svc, ok := s.desktopSvc(w)
	if !ok {
		return
	}
	if err := svc.SetBrowser(r.Context(), id, run); err != nil {
		writeDesktopErr(w, r, err)
		return
	}
	s.writeOrb(w, r, id, http.StatusOK)
}

// isOrbChannelErr 判断 err 是否来自 orb 通道（唤醒 / 可用性判定 / 拨号）。
func isOrbChannelErr(err error) bool {
	return errors.Is(err, orbs.ErrNotFound) || errors.Is(err, orbs.ErrOrbGone) || errors.Is(err, orbs.ErrOrbdOutdated) ||
		errors.Is(err, orbs.ErrOrbNotReady) || errors.Is(err, context.Canceled)
}
