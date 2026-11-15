package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// GetComposerPrefs 返回新对话输入框的偏好；尚未初始化所有者时为缺省值（role）。
func (s *Server) GetComposerPrefs(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Q.GetOwnerNewThreadTarget(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	target := apigen.ComposerPrefsDefaultTarget(v)
	if !target.Valid() {
		target = apigen.ComposerPrefsDefaultTargetRole
	}
	WriteJSON(w, http.StatusOK, apigen.ComposerPrefs{DefaultTarget: target})
}

// PutComposerPrefs 整体替换新对话输入框的偏好；尚未初始化所有者 → 409。
func (s *Server) PutComposerPrefs(w http.ResponseWriter, r *http.Request) {
	var req apigen.ComposerPrefs
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.DefaultTarget.Valid() {
		writeErr(w, r, errInvalid("defaultTarget 须为 assistant 或 role"))
		return
	}
	n, err := s.Store.Q.SetOwnerNewThreadTarget(r.Context(), string(req.DefaultTarget))
	if !savedOwner(w, r, n, err) {
		return
	}
	WriteJSON(w, http.StatusOK, req)
}
