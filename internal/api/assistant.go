package api

import (
	"context"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/coord"
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

// ListAssistantMcpServerTools 对应 `GET assistant/mcp-servers/{name}/tools`（M26）：以 Streamable HTTP 连上该服务器列出工具，
// 给「研究可用工具」白名单用。
func (s *Server) ListAssistantMcpServerTools(w http.ResponseWriter, r *http.Request, name string) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	tools, err := svc.MCPServerTools(r.Context(), name)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.AssistantMcpToolList{Tools: tools})
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

// 助手的电脑（M19 第二层）。

// GetAssistantComputer 对应 `GET assistant/computer`：还没有电脑时 orbId 省略。
func (s *Server) GetAssistantComputer(w http.ResponseWriter, r *http.Request) {
	s.assistantComputer(w, r, (*coord.Service).ComputerStatus)
}

// WakeAssistantComputer 对应 `POST assistant/computer/wake`：没有则创建，等到就绪（超时 409 computer_starting）。
func (s *Server) WakeAssistantComputer(w http.ResponseWriter, r *http.Request) {
	s.assistantComputer(w, r, (*coord.Service).WakeComputer)
}

// PauseAssistantComputer 对应 `POST assistant/computer/pause`：冻结（没有 → 404）。
func (s *Server) PauseAssistantComputer(w http.ResponseWriter, r *http.Request) {
	s.assistantComputer(w, r, (*coord.Service).PauseComputer)
}

// ResetAssistantComputer 对应 `POST assistant/computer/reset`：删除，下次用到时重新创建（没有时幂等）。
func (s *Server) ResetAssistantComputer(w http.ResponseWriter, r *http.Request) {
	s.assistantComputer(w, r, (*coord.Service).ResetComputer)
}

func (s *Server) assistantComputer(w http.ResponseWriter, r *http.Request,
	op func(*coord.Service, context.Context) (apigen.AssistantComputer, error)) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	out, err := op(svc, r.Context())
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}
