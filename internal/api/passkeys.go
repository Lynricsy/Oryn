package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
)

// passkeyJSON 把服务层的 Passkey 转成 API 形状。
func passkeyJSON(p auth.PasskeyInfo) apigen.Passkey {
	return apigen.Passkey{
		ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, LastUsedAt: p.LastUsedAt, Transports: p.Transports,
		BackupEligible: p.BackupEligible, BackedUp: p.BackedUp, Current: p.Current,
	}
}

// ListPasskeys 列出所有者的 Passkey 与当前会话的重新验证有效期。
func (s *Server) ListPasskeys(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	list, err := s.Auth.Service.ListPasskeys(r.Context(), p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := apigen.PasskeyList{Items: make([]apigen.Passkey, 0, len(list))}
	for _, pk := range list {
		out.Items = append(out.Items, passkeyJSON(pk))
	}
	if until, ok := auth.ReauthValidUntil(p, time.Now()); ok {
		out.ReauthValidUntil = new(until.UnixMilli())
	}
	WriteJSON(w, http.StatusOK, out)
}

// AddPasskeyBegin 开始追加 Passkey（要求 5 分钟内的 Passkey 验证）。
func (s *Server) AddPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitSetup) {
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req apigen.AddPasskeyBeginRequest
	// 请求体可选：空体按未给名称处理
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	var name string
	if req.Name != nil {
		name = *req.Name
	}
	c, err := s.Auth.Service.BeginAddPasskey(r.Context(), p, name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.WebAuthnCeremony{CeremonyID: c.ID, Options: c.Options})
}

// AddPasskeyFinish 完成追加 Passkey；不签发新会话。
func (s *Server) AddPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitSetup) {
		return
	}
	p, ok := principal(w, r)
	if !ok {
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
	pk, err := s.Auth.Service.FinishAddPasskey(r.Context(), p, req.CeremonyID, cred)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, passkeyJSON(pk))
}

// RenamePasskey 给 Passkey 改名。
func (s *Server) RenamePasskey(w http.ResponseWriter, r *http.Request, passkeyID string) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req apigen.RenamePasskeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	pk, err := s.Auth.Service.RenamePasskey(r.Context(), p, passkeyID, req.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, passkeyJSON(pk))
}

// DeletePasskey 删除 Passkey（要求 5 分钟内的 Passkey 验证），吊销用它登录的其它会话。
func (s *Server) DeletePasskey(w http.ResponseWriter, r *http.Request, passkeyID string) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := s.Auth.Service.DeletePasskey(r.Context(), p, passkeyID); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReauthBegin 开始对当前会话做一次 Passkey 断言。
func (s *Server) ReauthBegin(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitLogin) {
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	c, err := s.Auth.Service.BeginReauth(r.Context(), p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.WebAuthnCeremony{CeremonyID: c.ID, Options: c.Options})
}

// ReauthFinish 完成重新验证；此后 5 分钟内可做敏感操作。
func (s *Server) ReauthFinish(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitLogin) {
		return
	}
	p, ok := principal(w, r)
	if !ok {
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
	at, err := s.Auth.Service.FinishReauth(r.Context(), p, req.CeremonyID, cred)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	verified := at.UnixMilli()
	until, _ := auth.ReauthValidUntil(auth.Principal{VerifiedAt: verified}, at)
	WriteJSON(w, http.StatusOK, apigen.ReauthResult{VerifiedAt: verified, ValidUntil: until.UnixMilli()})
}

// GetRecoveryCodeStatus 返回恢复码剩余情况。
func (s *Server) GetRecoveryCodeStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	st, err := s.Auth.Service.RecoveryStatus(r.Context(), p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := apigen.RecoveryCodeStatus{Total: st.Total, Remaining: st.Remaining}
	if st.GeneratedAt > 0 {
		out.GeneratedAt = new(st.GeneratedAt)
	}
	WriteJSON(w, http.StatusOK, out)
}

// RegenerateRecoveryCodes 作废全部旧码并生成 10 个新码（要求 5 分钟内的 Passkey 验证）。
func (s *Server) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	codes, err := s.Auth.Service.RegenerateRecoveryCodes(r.Context(), p)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.RecoveryCodes{RecoveryCodes: codes})
}
