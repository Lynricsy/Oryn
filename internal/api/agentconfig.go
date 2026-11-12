package api

import (
	"io"
	"io/fs"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

// maxImportBody 是 tar 包上限（内容上限 + tar 头部余量）。
const maxImportBody = agentcfg.MaxTotalSize + 32<<20

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
	files, err := s.Auth.Home.List()
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

// GetAgentConfigFile 返回文件原文（models.yml 为已抽取密钥的模板）。
func (s *Server) GetAgentConfigFile(w http.ResponseWriter, r *http.Request, path apigen.AgentConfigPath) {
	b, _, err := s.Auth.Home.Get(path)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// PutAgentConfigFile 写入文件；models.yml 做密钥抽取。
func (s *Server) PutAgentConfigFile(w http.ResponseWriter, r *http.Request, path apigen.AgentConfigPath, params apigen.PutAgentConfigFileParams) {
	var mode fs.FileMode
	if params.Mode != nil {
		if *params.Mode < 0 || *params.Mode > 0o777 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "mode 须在 0–511 之间")
			return
		}
		mode = fs.FileMode(*params.Mode)
	}
	b, err := readBody(r, agentcfg.MaxFileSize)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.Auth.Home.Put(r.Context(), path, b, mode, s.Auth.Vault)
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

// DeleteAgentConfigFile 删除文件。
func (s *Server) DeleteAgentConfigFile(w http.ResponseWriter, r *http.Request, path apigen.AgentConfigPath) {
	if err := s.Auth.Home.Delete(path); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ImportAgentConfig 用 tar 包整体替换 omp-home。
func (s *Server) ImportAgentConfig(w http.ResponseWriter, r *http.Request) {
	res, err := s.Auth.Home.ImportTar(r.Context(), io.LimitReader(r.Body, maxImportBody), s.Auth.Vault)
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
