package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/browserrelay"
)

// BrowserRelayDeps 是 M27 客户端浏览器中继 REST 处理器的依赖。
type BrowserRelayDeps struct {
	Service *browserrelay.Service
}

func (s *Server) browserRelayReady(w http.ResponseWriter) bool {
	if s.BrowserRelay == nil || s.BrowserRelay.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "browser relay is not enabled")
		return false
	}
	return true
}

// ListBrowserRelayClients 列出当前在线的已配对浏览器扩展客户端（M27）。
func (s *Server) ListBrowserRelayClients(w http.ResponseWriter, r *http.Request) {
	if !s.browserRelayReady(w) {
		return
	}
	clients := s.BrowserRelay.Service.ListClients(r.Context())
	WriteJSON(w, http.StatusOK, apigen.BrowserRelayClientList{Clients: clients})
}

// GetThreadBrowserRelay 查询指定线程的本机浏览器绑定状态（M27）。
func (s *Server) GetThreadBrowserRelay(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	if !s.browserRelayReady(w) {
		return
	}
	st, err := s.BrowserRelay.Service.GetThreadState(r.Context(), id)
	if err != nil {
		status, code := browserrelay.HTTPStatusForError(err)
		WriteError(w, status, code, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, st)
}

// BindThreadBrowserRelay 绑定线程到指定的 browser_extension 设备（M27）。
func (s *Server) BindThreadBrowserRelay(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	if !s.browserRelayReady(w) {
		return
	}
	var req apigen.ThreadBrowserRelayBindRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	st, err := s.BrowserRelay.Service.BindThread(r.Context(), id, req.DeviceID)
	if err != nil {
		status, code := browserrelay.HTTPStatusForError(err)
		WriteError(w, status, code, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, st)
}

// UnbindThreadBrowserRelay 解除线程与本机浏览器扩展的绑定（幂等，M27）。
func (s *Server) UnbindThreadBrowserRelay(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	if !s.browserRelayReady(w) {
		return
	}
	st, err := s.BrowserRelay.Service.UnbindThread(r.Context(), id)
	if err != nil {
		status, code := browserrelay.HTTPStatusForError(err)
		WriteError(w, status, code, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, st)
}
