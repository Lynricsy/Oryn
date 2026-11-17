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

// GetAssistantNotes 对应 `GET assistant/main/notes`：主线备忘（M19）。
func (s *Server) GetAssistantNotes(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	out, err := svc.Notes(r.Context())
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutAssistantNotes 对应 `PUT assistant/main/notes`：整体改写，上限 1500 字（超出 400 `invalid_request`）。
func (s *Server) PutAssistantNotes(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	var in apigen.AssistantNotes
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutNotes(r.Context(), in.Text)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}
