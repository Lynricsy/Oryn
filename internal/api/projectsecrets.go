package api

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

// 项目级环境变量与 Git 令牌（M24 阶段 ④）：只对本项目的 orb 生效，与全局同名时以项目为准。
// 已归档的项目照常可读写（同项目级 Agent 配置）；写入或删除后让预热池按新的配置哈希对账。
// 出站策略里没有由密钥派生的内容（令牌每个 git 请求现取），所以不作废策略缓存。

// projectSecretJSON 映射项目级条目；值只对不保密的条目给出。
func projectSecretJSON(e secrets.ProjectEntry) apigen.ProjectSecret {
	out := apigen.ProjectSecret{
		Name: e.Name, Mode: apigen.SecretMode(e.Mode), Secret: e.Secret, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
	if !e.Secret {
		out.Value = e.Value
	}
	return out
}

// ListProjectSecrets 列出项目级条目（按名称排序；保密条目不含值）。
func (s *Server) ListProjectSecrets(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	if !s.projectExists(w, r, id) {
		return
	}
	es, err := s.Auth.Vault.ListProject(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := apigen.ProjectSecretList{Secrets: make([]apigen.ProjectSecret, 0, len(es))}
	for _, e := range es {
		out.Secrets = append(out.Secrets, projectSecretJSON(e))
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutProjectSecret 写入或覆盖项目级条目（覆盖保留原 id）。
func (s *Server) PutProjectSecret(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, name apigen.SecretNamePath) {
	var req apigen.ProjectSecretPut
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Mode.Valid() {
		WriteError(w, http.StatusBadRequest, "invalid_request", "mode 必须是 proxy 或 env")
		return
	}
	secret := req.Secret == nil || *req.Secret
	if err := secrets.ValidProjectEntry(name, string(req.Mode), secret); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Value == "" {
		WriteError(w, http.StatusBadRequest, "invalid_request", "值不能为空")
		return
	}
	m, err := s.Auth.Vault.PutProject(r.Context(), id, name, string(req.Mode), req.Value, secret)
	if errors.Is(err, secrets.ErrNoProject) {
		WriteError(w, http.StatusNotFound, "not_found", "项目不存在")
		return
	} else if err != nil {
		writeErr(w, r, err)
		return
	}
	s.poolKick(id)
	WriteJSON(w, http.StatusOK, projectSecretJSON(secrets.ProjectEntry{Meta: m, Value: &req.Value}))
}

// DeleteProjectSecret 删除项目级条目。
func (s *Server) DeleteProjectSecret(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, name apigen.SecretNamePath) {
	if !s.projectExists(w, r, id) {
		return
	}
	ok, err := s.Auth.Vault.DeleteProject(r.Context(), id, name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		WriteError(w, http.StatusNotFound, "not_found", "条目不存在")
		return
	}
	s.poolKick(id)
	w.WriteHeader(http.StatusNoContent)
}
