package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// GetGenerativeUISettings 对应 `GET settings/generative-ui`；尚未初始化所有者时为缺省值（开启）。
func (s *Server) GetGenerativeUISettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Q.GetOwnerGenerativeUI(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		v, err = 1, nil
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GenerativeUISettings{Enabled: v == 1})
}

// PutGenerativeUISettings 对应 `PUT settings/generative-ui`：整体替换；尚未初始化所有者 → 409。
// 值变化时让全局助手的对话在静止后重启（工具表与系统提示词随之更新）；工作线程在下次 agent 启动时生效。
func (s *Server) PutGenerativeUISettings(w http.ResponseWriter, r *http.Request) {
	// enabled 必填：解到指针，缺省或 null 回 400，而不是静默当成关闭
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeErr(w, r, errInvalid("enabled 须为布尔值"))
		return
	}
	prev, err := s.Store.Q.GetOwnerGenerativeUI(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	var v int64
	if *req.Enabled {
		v = 1
	}
	n, err := s.Store.Q.SetOwnerGenerativeUI(r.Context(), v)
	if !savedOwner(w, r, n, err) {
		return
	}
	if prev != v && s.Coord != nil && s.Coord.Service != nil {
		s.Coord.Service.OnGenerativeUIChanged(r.Context())
	}
	WriteJSON(w, http.StatusOK, apigen.GenerativeUISettings{Enabled: *req.Enabled})
}
