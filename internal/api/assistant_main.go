package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// GetAssistantMain 对应 `GET assistant/main`：返回全局助手的主线对话，没有则创建（M19）。
func (s *Server) GetAssistantMain(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	t, err := svc.EnsureMain(r.Context())
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ThreadEnvelope{Thread: t})
}
