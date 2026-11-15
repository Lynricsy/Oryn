package api

import (
	"io"
	"io/fs"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

// importHeaderSlack 是 tar 包体在内容合计上限之外允许的头部余量。
const importHeaderSlack = 32 << 20

func agentFileJSON(f agentcfg.FileInfo) apigen.AgentConfigFile {
	return apigen.AgentConfigFile{Path: f.Path, Size: f.Size, Mode: int(f.Mode.Perm()), Sha256: f.SHA256, UpdatedAt: f.UpdatedAt}
}

func agentWarningsJSON(ws []agentcfg.Warning) []apigen.AgentConfigWarning {
	out := make([]apigen.AgentConfigWarning, 0, len(ws))
	for _, w := range ws {
		out = append(out, apigen.AgentConfigWarning{Path: w.Path, Kind: w.Kind, Message: w.Message})
	}
	return out
}

// ListAgentConfigFiles 列出 omp-home 文件。
func (s *Server) ListAgentConfigFiles(w http.ResponseWriter, r *http.Request) {
	listAgentFiles(w, r, s.Auth.Home)
}

// GetAgentConfigFile 返回文件原文（models.yml 为已抽取密钥的模板）。
func (s *Server) GetAgentConfigFile(w http.ResponseWriter, r *http.Request, path apigen.AgentConfigPath) {
	getAgentFile(w, r, s.Auth.Home, path)
}

// PutAgentConfigFile 写入文件；models.yml 做密钥抽取。
func (s *Server) PutAgentConfigFile(w http.ResponseWriter, r *http.Request, path apigen.AgentConfigPath, params apigen.PutAgentConfigFileParams) {
	putAgentFile(w, r, s.Auth.Home, path, params.Mode, s.Auth.Vault)
}

// DeleteAgentConfigFile 删除文件。
func (s *Server) DeleteAgentConfigFile(w http.ResponseWriter, r *http.Request, path apigen.AgentConfigPath) {
	deleteAgentFile(w, r, s.Auth.Home, path)
}

// ImportAgentConfig 用 tar 包整体替换 omp-home。
func (s *Server) ImportAgentConfig(w http.ResponseWriter, r *http.Request) {
	importAgentFiles(w, r, s.Auth.Home, s.Auth.Vault)
}

// listAgentFiles 列出配置根 h 的文件。
func listAgentFiles(w http.ResponseWriter, r *http.Request, h *agentcfg.Home) {
	files, err := h.List()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := apigen.AgentConfigFileList{Files: make([]apigen.AgentConfigFile, 0, len(files))}
	for _, f := range files {
		out.Files = append(out.Files, agentFileJSON(f))
	}
	WriteJSON(w, http.StatusOK, out)
}

// getAgentFile 返回配置根 h 里一个文件的原文。
func getAgentFile(w http.ResponseWriter, r *http.Request, h *agentcfg.Home, path string) {
	b, _, err := h.Get(path)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// putAgentFile 写入配置根 h 的一个文件；请求体上限为 h 的单文件上限。
func putAgentFile(w http.ResponseWriter, r *http.Request, h *agentcfg.Home, path string, modeParam *int, vault *secrets.Vault) {
	var mode fs.FileMode
	if modeParam != nil {
		if *modeParam < 0 || *modeParam > 0o777 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "mode 须在 0–511 之间")
			return
		}
		mode = fs.FileMode(*modeParam)
	}
	b, err := readBody(r, int64(h.MaxFileSize()))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := h.Put(r.Context(), path, b, mode, vault)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	extracted := res.ExtractedSecrets
	if extracted == nil {
		extracted = []string{}
	}
	WriteJSON(w, http.StatusOK, apigen.AgentConfigWriteResult{
		File: agentFileJSON(res.File), ExtractedSecrets: extracted, Warnings: agentWarningsJSON(res.Warnings),
	})
}

// deleteAgentFile 删除配置根 h 的一个文件。
func deleteAgentFile(w http.ResponseWriter, r *http.Request, h *agentcfg.Home, path string) {
	if err := h.Delete(path); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// importAgentFiles 用 tar 包整体替换配置根 h；包体上限 = h 的合计上限 + 头部余量。
func importAgentFiles(w http.ResponseWriter, r *http.Request, h *agentcfg.Home, vault *secrets.Vault) {
	res, err := h.ImportTar(r.Context(), io.LimitReader(r.Body, int64(h.MaxTotalSize())+importHeaderSlack), vault)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := apigen.AgentConfigImportResult{
		Files: make([]apigen.AgentConfigFile, 0, len(res.Files)), ExtractedSecrets: res.ExtractedSecrets,
		Warnings: agentWarningsJSON(res.Warnings),
	}
	for _, f := range res.Files {
		out.Files = append(out.Files, agentFileJSON(f))
	}
	WriteJSON(w, http.StatusOK, out)
}
