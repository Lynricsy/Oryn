package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/sshgw"
)

// SSHDeps 是 SSH 主机与网关接口（M18）的依赖。
type SSHDeps struct {
	Service *sshgw.Service
}

func (s *Server) sshSvc(w http.ResponseWriter) (*sshgw.Service, bool) {
	if s.SSH == nil || s.SSH.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "ssh service not configured")
		return nil, false
	}
	return s.SSH.Service, true
}

// writeSSHErr 写出 SSH 服务的错误：*sshgw.Error 按码映射为 400 / 404 / 409 并原样输出文案，其余按通用映射。
func writeSSHErr(w http.ResponseWriter, r *http.Request, err error) {
	var se *sshgw.Error
	if errors.As(err, &se) {
		status := http.StatusBadRequest
		switch se.Code {
		case "not_found":
			status = http.StatusNotFound
		case "conflict":
			status = http.StatusConflict
		}
		WriteError(w, status, se.Code, se.Message)
		return
	}
	writeErr(w, r, err)
}

// ListSSHHosts 对应 `GET /api/v1/ssh/hosts`。
func (s *Server) ListSSHHosts(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	list, err := svc.ListHosts(r.Context())
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, list)
}

// CreateSSHHost 对应 `POST /api/v1/ssh/hosts`。
func (s *Server) CreateSSHHost(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	var in apigen.SSHHostCreate
	if !decodeJSON(w, r, &in) {
		return
	}
	h, err := svc.CreateHost(r.Context(), in)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, h)
}

// GetSSHHost 对应 `GET /api/v1/ssh/hosts/{id}`。
func (s *Server) GetSSHHost(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	h, err := svc.GetHost(r.Context(), id)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, h)
}

// UpdateSSHHost 对应 `PATCH /api/v1/ssh/hosts/{id}`。`jumpHostId` / `compat` 为 null 表示清掉，缺省表示不改：
// 生成的结构体分不出两者，另看原始 JSON 里有没有这两个键。
func (s *Server) UpdateSSHHost(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "读取请求体失败: "+err.Error())
		return
	}
	var p sshgw.HostPatch
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(body, &p.SSHHostUpdate); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON: "+err.Error())
		return
	}
	if err := json.Unmarshal(body, &keys); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "请求体必须是 JSON 对象")
		return
	}
	_, p.JumpHostSet = keys["jumpHostId"]
	_, p.CompatSet = keys["compat"]
	h, err := svc.UpdateHost(r.Context(), id, p)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, h)
}

// DeleteSSHHost 对应 `DELETE /api/v1/ssh/hosts/{id}`。
func (s *Server) DeleteSSHHost(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteHost(r.Context(), id); err != nil {
		writeSSHErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResetSSHHostKey 对应 `POST /api/v1/ssh/hosts/{id}/reset-host-key`。
func (s *Server) ResetSSHHostKey(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	h, err := svc.ResetHostKey(r.Context(), id)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, h)
}

// ListSSHKeys 对应 `GET /api/v1/ssh/keys`。
func (s *Server) ListSSHKeys(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	list, err := svc.ListKeys(r.Context())
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, list)
}

// CreateSSHKey 对应 `POST /api/v1/ssh/keys`。
func (s *Server) CreateSSHKey(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	var in apigen.SSHKeyCreate
	if !decodeJSON(w, r, &in) {
		return
	}
	k := sshgw.KeyInput{Name: in.Name, Mode: string(in.Mode)}
	if in.PrivateKey != nil {
		k.PrivateKey = *in.PrivateKey
	}
	if in.Passphrase != nil {
		k.Passphrase = *in.Passphrase
	}
	key, err := svc.CreateKey(r.Context(), k)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, key)
}

// RenameSSHKey 对应 `PATCH /api/v1/ssh/keys/{id}`。
func (s *Server) RenameSSHKey(w http.ResponseWriter, r *http.Request, id apigen.SSHKeyIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	var in apigen.SSHKeyRename
	if !decodeJSON(w, r, &in) {
		return
	}
	key, err := svc.RenameKey(r.Context(), id, in.Name)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, key)
}

// DeleteSSHKey 对应 `DELETE /api/v1/ssh/keys/{id}`。
func (s *Server) DeleteSSHKey(w http.ResponseWriter, r *http.Request, id apigen.SSHKeyIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteKey(r.Context(), id); err != nil {
		writeSSHErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListSSHAudit 对应 `GET /api/v1/ssh/audit`。
func (s *Server) ListSSHAudit(w http.ResponseWriter, r *http.Request, params apigen.ListSSHAuditParams) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	var q sshgw.AuditQuery
	if params.Host != nil {
		q.HostID = *params.Host
	}
	if params.Thread != nil {
		q.ThreadID = *params.Thread
	}
	if params.Before != nil {
		if *params.Before < 1 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "before must be positive")
			return
		}
		q.Before = *params.Before
	}
	if params.Limit != nil {
		if *params.Limit < 1 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		q.Limit = *params.Limit
	}
	list, err := svc.Audit(r.Context(), q)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, list)
}

// ListThreadSSHHosts 对应 `GET /api/v1/threads/{id}/ssh-hosts`。
func (s *Server) ListThreadSSHHosts(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	list, err := svc.ThreadHosts(r.Context(), id)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, list)
}
