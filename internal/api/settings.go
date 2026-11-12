package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/gitident"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// gitIdentityJSON 在两项都已设置时返回身份，否则 nil。
func gitIdentityJSON(name, email *string) *apigen.GitIdentity {
	if name == nil || email == nil || *name == "" || *email == "" {
		return nil
	}
	return &apigen.GitIdentity{Name: *name, Email: *email}
}

// GetGitIdentity 返回 orb 内 git 提交身份的全局默认值；尚未初始化所有者时视为未设置。
func (s *Server) GetGitIdentity(w http.ResponseWriter, r *http.Request) {
	row, err := s.Store.Q.GetOwnerGitIdentity(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitIdentitySettings{Identity: gitIdentityJSON(row.GitName, row.GitEmail)})
}

// PutGitIdentity 设置全局默认身份；下次 setup / 唤醒时写入 orb。
func (s *Server) PutGitIdentity(w http.ResponseWriter, r *http.Request) {
	var req apigen.GitIdentity
	if !decodeJSON(w, r, &req) {
		return
	}
	name, email := strings.TrimSpace(req.Name), strings.TrimSpace(req.Email)
	if err := gitident.Validate(name, email); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.setOwnerGitIdentity(w, r, &name, &email) {
		return
	}
	WriteJSON(w, http.StatusOK, apigen.GitIdentitySettings{Identity: &apigen.GitIdentity{Name: name, Email: email}})
}

// DeleteGitIdentity 清除全局默认身份（已有 orb 的 git 配置保持不变）。
func (s *Server) DeleteGitIdentity(w http.ResponseWriter, r *http.Request) {
	if !s.setOwnerGitIdentity(w, r, nil, nil) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setOwnerGitIdentity(w http.ResponseWriter, r *http.Request, name, email *string) bool {
	n, err := s.Store.Q.SetOwnerGitIdentity(r.Context(), db.SetOwnerGitIdentityParams{GitName: name, GitEmail: email})
	if err != nil {
		writeErr(w, r, err)
		return false
	}
	if n == 0 {
		WriteError(w, http.StatusConflict, "conflict", "尚未完成初始化（没有所有者）")
		return false
	}
	return true
}

// projectGitIdentity 校验项目的身份覆盖：两项须同时给出（或同时缺省）；两项都是空串表示无覆盖
// （更新时即清除），返回 (nil, nil, true, nil)。set 为假表示请求未涉及这两项。
func projectGitIdentity(name, email *string) (n, e *string, set bool, err error) {
	if name == nil && email == nil {
		return nil, nil, false, nil
	}
	if name == nil || email == nil {
		return nil, nil, false, errInvalid("gitAuthorName 与 gitAuthorEmail 须同时给出")
	}
	nv, ev := strings.TrimSpace(*name), strings.TrimSpace(*email)
	if nv == "" && ev == "" {
		return nil, nil, true, nil
	}
	if err := gitident.Validate(nv, ev); err != nil {
		return nil, nil, false, errInvalid(err.Error())
	}
	return &nv, &ev, true, nil
}
