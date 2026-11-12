package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

func secretJSON(m secrets.Meta) apigen.SecretMeta {
	return apigen.SecretMeta{
		Name: m.Name, Mode: apigen.SecretMode(m.Mode), Hosts: m.Hosts, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// ListSecrets 列出密钥元数据（不含值）。
func (s *Server) ListSecrets(w http.ResponseWriter, r *http.Request) {
	ms, err := s.Auth.Vault.List(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := apigen.SecretList{Secrets: make([]apigen.SecretMeta, 0, len(ms))}
	for _, m := range ms {
		out.Secrets = append(out.Secrets, secretJSON(m))
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutSecret 写入或覆盖密钥。
func (s *Server) PutSecret(w http.ResponseWriter, r *http.Request, name apigen.SecretNamePath) {
	var req apigen.SecretPut
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Mode.Valid() {
		WriteError(w, http.StatusBadRequest, "invalid_request", "mode 必须是 proxy 或 env")
		return
	}
	if err := secrets.ValidName(name, string(req.Mode)); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Value == "" {
		WriteError(w, http.StatusBadRequest, "invalid_request", "密钥值不能为空")
		return
	}
	m, err := s.Auth.Vault.Put(r.Context(), name, string(req.Mode), req.Value, req.Hosts)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, secretJSON(m))
}

// DeleteSecret 删除密钥。
func (s *Server) DeleteSecret(w http.ResponseWriter, r *http.Request, name apigen.SecretNamePath) {
	ok, err := s.Auth.Vault.Delete(r.Context(), name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		WriteError(w, http.StatusNotFound, "not_found", "密钥不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
