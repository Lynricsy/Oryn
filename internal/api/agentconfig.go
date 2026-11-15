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

// PutAgentConfigFile 写入文件；models.yml 做密钥抽取。成功后防抖下发到运行中的 orb（M13）。
func (s *Server) PutAgentConfigFile(w http.ResponseWriter, r *http.Request, path apigen.AgentConfigPath, params apigen.PutAgentConfigFileParams) {
	if putAgentFile(w, r, s.Auth.Home, path, params.Mode, s.Auth.Vault) {
		s.ompConfigChanged("")
	}
}

// DeleteAgentConfigFile 删除文件。成功后防抖下发到运行中的 orb（M13）。
func (s *Server) DeleteAgentConfigFile(w http.ResponseWriter, r *http.Request, path apigen.AgentConfigPath) {
	if deleteAgentFile(w, r, s.Auth.Home, path) {
		s.ompConfigChanged("")
	}
}

// ImportAgentConfig 用 tar 包整体替换 omp-home。成功后防抖下发到运行中的 orb（M13）。
func (s *Server) ImportAgentConfig(w http.ResponseWriter, r *http.Request) {
	if importAgentFiles(w, r, s.Auth.Home, s.Auth.Vault) {
		s.ompConfigChanged("")
	}
}

// ompConfigChanged 在一层 omp 配置（projectID 空 = 全局 omp-home）写入成功后安排防抖下发（M13 6.2）；
// 未注入 orb 服务时不做事。
func (s *Server) ompConfigChanged(projectID string) {
	if s.Orbs != nil && s.Orbs.Service != nil {
		s.Orbs.Service.ScheduleOmpApply(projectID)
	}
}

// GetAgentConfigApplyStatus 返回 omp 配置在会话上的生效状态：需重启的会话、等待 reload 的会话数，以及全局层的
// 下发进度（M13 6.3）。进度在会话分类之前读：读到 pending=false 时，后面读到的会话部分一定已含这次下发的结果。
func (s *Server) GetAgentConfigApplyStatus(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	push := apigen.AgentConfigPushState{Failed: []apigen.AgentConfigPushFailure{}}
	if s.Orbs != nil && s.Orbs.Service != nil {
		ps := s.Orbs.Service.OmpApplyState("")
		push.Pending = ps.Pending
		if !ps.AppliedAt.IsZero() {
			at := ps.AppliedAt.UnixMilli()
			push.AppliedAt = &at
		}
		for _, f := range ps.Failed {
			push.Failed = append(push.Failed, apigen.AgentConfigPushFailure{OrbID: f.OrbID, Error: f.Err})
		}
		if ps.Err != "" {
			push.Error = &ps.Err
		}
	}
	st := svc.ApplyStatus(r.Context())
	out := apigen.AgentConfigApplyStatus{
		Threads: make([]apigen.AgentConfigRestartThread, 0, len(st.Restart)), PendingReloads: st.PendingReloads, Push: push,
	}
	for _, n := range st.Restart {
		out.Threads = append(out.Threads, apigen.AgentConfigRestartThread{
			ThreadID: n.ThreadID, Kind: apigen.ThreadKind(n.Kind), Title: n.Title, Reasons: n.Reasons, Scheduled: n.Scheduled,
		})
	}
	WriteJSON(w, http.StatusOK, out)
}

// RestartAgentConfigSessions 让需重启的会话在静止后停掉 omp，下一条消息以原会话自动拉起（M13 6.3）。
func (s *Server) RestartAgentConfigSessions(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	WriteJSON(w, http.StatusOK, apigen.AgentConfigRestartResult{ThreadIds: svc.RestartSessions(r.Context())})
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

// putAgentFile 写入配置根 h 的一个文件；请求体上限为 h 的单文件上限。返回是否写入成功（失败时已写出错误）。
func putAgentFile(w http.ResponseWriter, r *http.Request, h *agentcfg.Home, path string, modeParam *int, vault *secrets.Vault) bool {
	var mode fs.FileMode
	if modeParam != nil {
		if *modeParam < 0 || *modeParam > 0o777 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "mode 须在 0–511 之间")
			return false
		}
		mode = fs.FileMode(*modeParam)
	}
	b, err := readBody(r, int64(h.MaxFileSize()))
	if err != nil {
		writeErr(w, r, err)
		return false
	}
	res, err := h.Put(r.Context(), path, b, mode, vault)
	if err != nil {
		writeErr(w, r, err)
		return false
	}
	extracted := res.ExtractedSecrets
	if extracted == nil {
		extracted = []string{}
	}
	WriteJSON(w, http.StatusOK, apigen.AgentConfigWriteResult{
		File: agentFileJSON(res.File), ExtractedSecrets: extracted, Warnings: agentWarningsJSON(res.Warnings),
	})
	return true
}

// deleteAgentFile 删除配置根 h 的一个文件。返回是否删除成功（失败时已写出错误）。
func deleteAgentFile(w http.ResponseWriter, r *http.Request, h *agentcfg.Home, path string) bool {
	if err := h.Delete(path); err != nil {
		writeErr(w, r, err)
		return false
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

// importAgentFiles 用 tar 包整体替换配置根 h；包体上限 = h 的合计上限 + 头部余量。返回是否导入成功（失败时已写出错误）。
func importAgentFiles(w http.ResponseWriter, r *http.Request, h *agentcfg.Home, vault *secrets.Vault) bool {
	res, err := h.ImportTar(r.Context(), io.LimitReader(r.Body, int64(h.MaxTotalSize())+importHeaderSlack), vault)
	if err != nil {
		writeErr(w, r, err)
		return false
	}
	out := apigen.AgentConfigImportResult{
		Files: make([]apigen.AgentConfigFile, 0, len(res.Files)), ExtractedSecrets: res.ExtractedSecrets,
		Warnings: agentWarningsJSON(res.Warnings),
	}
	for _, f := range res.Files {
		out.Files = append(out.Files, agentFileJSON(f))
	}
	WriteJSON(w, http.StatusOK, out)
	return true
}
