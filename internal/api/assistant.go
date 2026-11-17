package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 全局助手的设置与 MCP 列表（M19）。

// GetAssistantSettings 对应 `GET settings/assistant`。
func (s *Server) GetAssistantSettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	out, err := svc.Settings(r.Context())
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutAssistantSettings 对应 `PUT settings/assistant`：整体替换，保存后立即刷新协调宿主的出站策略。
func (s *Server) PutAssistantSettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	var in apigen.AssistantSettings
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutSettings(r.Context(), in)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// ListAssistantMcpServers 对应 `GET assistant/mcp-servers`。
func (s *Server) ListAssistantMcpServers(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	servers, err := svc.MCPServers(r.Context())
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.AssistantMcpServerList{Servers: servers})
}
