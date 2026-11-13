package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
)

// AuthSetupBegin 开始注册 Passkey（初始化令牌或恢复会话）。
func (s *Server) AuthSetupBegin(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitSetup) {
		return
	}
	var req apigen.AuthSetupBeginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var token, name string
	if req.Token != nil {
		token = *req.Token
	}
	if req.Name != nil {
		name = *req.Name
	}
	c, err := s.Auth.Service.BeginSetup(r.Context(), r, token, name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.WebAuthnCeremony{CeremonyID: c.ID, Options: c.Options})
}

// AuthSetupFinish 完成注册并建立网页会话。
func (s *Server) AuthSetupFinish(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitSetup) {
		return
	}
	var req apigen.AuthSetupFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cred, err := json.Marshal(req.Credential)
	if err != nil || req.Credential == nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "缺少 credential")
		return
	}
	res, err := s.Auth.Service.FinishSetup(r.Context(), r, req.CeremonyID, cred)
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

// AuthLoginBegin 开始可发现凭据登录。
func (s *Server) AuthLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitLogin) {
		return
	}
	c, err := s.Auth.Service.BeginLogin(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.WebAuthnCeremony{CeremonyID: c.ID, Options: c.Options})
}

// AuthLoginFinish 完成登录并建立网页会话。
func (s *Server) AuthLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitLogin) {
		return
	}
	var req apigen.AuthLoginFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cred, err := json.Marshal(req.Credential)
	if err != nil || req.Credential == nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "缺少 credential")
		return
	}
	tok, exp, err := s.Auth.Service.FinishLogin(r.Context(), r, req.CeremonyID, cred)
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

// AuthRecovery 用恢复码换取一次性恢复会话。
func (s *Server) AuthRecovery(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitRecovery) {
		return
	}
	var req apigen.AuthRecoveryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.Auth.Service.Recover(r.Context(), r, req.Code)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	auth.SetSessionCookie(w, res.Cookie, res.Expires)
	WriteJSON(w, http.StatusOK, apigen.AuthRecoveryResponse{ExpiresAt: res.Expires.UnixMilli(), RemainingCodes: res.Remaining})
}

// AuthLogout 注销当前网页会话。
func (s *Server) AuthLogout(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := s.Auth.Service.Logout(r.Context(), p); err != nil {
		writeErr(w, r, err)
		return
	}
	auth.ClearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// GetMe 返回当前身份。
func (s *Server) GetMe(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	me, err := s.me(r.Context(), p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, me)
}

// me 组装 Me；尚无所有者（例如先用 admin create-device 建了设备）时 displayName 为空。
func (s *Server) me(ctx context.Context, p auth.Principal) (apigen.Me, error) {
	var me apigen.Me
	switch {
	case p.Kind == auth.KindDevice:
		me.Auth.Kind = apigen.MeAuthKindDevice
		me.Auth.DeviceID = new(p.ID)
	case p.Recovery:
		me.Auth.Kind = apigen.MeAuthKindRecovery
	default:
		me.Auth.Kind = apigen.MeAuthKindWeb
		// App 会话（设备换来的网页会话）同样给出设备
		if p.DeviceID != "" {
			me.Auth.DeviceID = new(p.DeviceID)
		}
	}
	o, err := s.Store.Q.GetOwner(ctx)
	if err == nil {
		me.DisplayName, me.CreatedAt = o.DisplayName, o.CreatedAt
	} else if !errors.Is(err, sql.ErrNoRows) {
		return me, err
	}
	n, err := s.Store.Q.CountPasskeys(ctx)
	if err != nil {
		return me, err
	}
	me.PasskeyCount = new(int(n))
	return me, nil
}

// CreateWsTicket 签发 WebSocket 票据。
func (s *Server) CreateWsTicket(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	t, exp := s.Auth.Tickets.Issue(p)
	WriteJSON(w, http.StatusOK, apigen.WsTicket{Ticket: t, ExpiresIn: exp})
}

// CreateAppSession 为 desktop / mobile 设备令牌签发 App 会话一次性码（M6）。
func (s *Server) CreateAppSession(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	code, exp, err := s.Auth.Service.IssueAppCode(r.Context(), p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.AppSessionCode{Code: code, ExpiresIn: exp})
}
