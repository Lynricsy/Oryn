package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
)

// 密码 + TOTP 登录（不用 Passkey 的认证方式）。按主人的决定，这些端点不做限流。

func totpEnrollmentJSON(e auth.TOTPEnrollment) apigen.TotpEnrollment {
	return apigen.TotpEnrollment{EnrollmentID: e.ID, Secret: e.Secret, OtpauthURI: e.URI}
}

// AuthPasswordSetupBegin 开始用密码 + TOTP 初始化（初始化令牌或恢复会话）。
func (s *Server) AuthPasswordSetupBegin(w http.ResponseWriter, r *http.Request) {
	var req apigen.PasswordSetupBeginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var token string
	if req.Token != nil {
		token = *req.Token
	}
	e, err := s.Auth.Service.BeginPasswordSetup(r.Context(), r, token)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, totpEnrollmentJSON(e))
}

// AuthPasswordSetupFinish 完成密码 + TOTP 初始化并建立网页会话。
func (s *Server) AuthPasswordSetupFinish(w http.ResponseWriter, r *http.Request) {
	var req apigen.PasswordEnrollFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.Auth.Service.FinishPasswordSetup(r.Context(), r, req.EnrollmentID, req.Password, req.Code)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	auth.SetSessionCookie(w, res.Cookie, res.Expires)
	me, err := s.me(r.Context(), auth.Principal{Kind: auth.KindWeb})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.AuthSetupFinishResponse{Me: me, RecoveryCodes: res.RecoveryCodes})
}

// AuthPasswordLogin 用密码 + 验证码登录并建立网页会话。
func (s *Server) AuthPasswordLogin(w http.ResponseWriter, r *http.Request) {
	var req apigen.PasswordCredentials
	if !decodeJSON(w, r, &req) {
		return
	}
	tok, exp, err := s.Auth.Service.LoginWithPassword(r.Context(), r, req.Password, req.Code)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	auth.SetSessionCookie(w, tok, exp)
	me, err := s.me(r.Context(), auth.Principal{Kind: auth.KindWeb})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, me)
}

// PasswordReauth 用密码 + 验证码重新验证当前会话；此后 5 分钟内可做敏感操作。
func (s *Server) PasswordReauth(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req apigen.PasswordCredentials
	if !decodeJSON(w, r, &req) {
		return
	}
	at, err := s.Auth.Service.ReauthWithPassword(r.Context(), p, req.Password, req.Code)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	verified := at.UnixMilli()
	until, _ := auth.ReauthValidUntil(auth.Principal{VerifiedAt: verified}, at)
	WriteJSON(w, http.StatusOK, apigen.ReauthResult{VerifiedAt: verified, ValidUntil: until.UnixMilli()})
}

// SetPasswordBegin 开始设置或重设密码登录（要求 5 分钟内的验证）。
func (s *Server) SetPasswordBegin(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	e, err := s.Auth.Service.BeginSetPassword(r.Context(), p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, totpEnrollmentJSON(e))
}

// SetPasswordFinish 完成设置密码登录；吊销其它用密码登录的会话。
func (s *Server) SetPasswordFinish(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req apigen.PasswordEnrollFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.Auth.Service.FinishSetPassword(r.Context(), p, req.EnrollmentID, req.Password, req.Code); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeletePassword 关闭密码登录（要求 5 分钟内的验证，且至少还有一把 Passkey）。
func (s *Server) DeletePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := s.Auth.Service.DeletePassword(r.Context(), p); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
