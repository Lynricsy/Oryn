package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// GetLanguageSettings 返回全局语言设置；尚未初始化所有者时为缺省值（zh-CN）。
func (s *Server) GetLanguageSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Q.GetOwnerLanguage(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	lang := apigen.LanguageCode(v)
	if !lang.Valid() {
		lang = apigen.ZhCN
	}
	WriteJSON(w, http.StatusOK, apigen.LanguageSettings{Language: lang})
}

// PutLanguageSettings 整体替换全局语言设置；尚未初始化所有者 → 409。
// 变更后通知协调层在静止后重启未归档的协调与助手对话，并丢弃旧备用对话。
func (s *Server) PutLanguageSettings(w http.ResponseWriter, r *http.Request) {
	var req apigen.LanguageSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Language.Valid() {
		writeErr(w, r, errInvalid("language 须为 zh-CN 或 en"))
		return
	}
	prev, err := s.Store.Q.GetOwnerLanguage(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	n, err := s.Store.Q.SetOwnerLanguage(r.Context(), string(req.Language))
	if !savedOwner(w, r, n, err) {
		return
	}
	if prev != string(req.Language) && s.Coord != nil && s.Coord.Service != nil {
		s.Coord.Service.OnLanguageChanged(r.Context())
	}
	WriteJSON(w, http.StatusOK, req)
}
