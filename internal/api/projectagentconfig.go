package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 项目级 omp 配置（M12.2）：与全局 agent-config 共用处理逻辑，配置根换成 `<data_dir>/omp-projects/<id>/`。
// 项目层不收 models.yml，所以不做密钥抽取（vault 传 nil）。

// projectHome 确认项目存在并返回其配置根；失败时已写出错误。
func (s *Server) projectHome(w http.ResponseWriter, r *http.Request, id string) (*agentcfg.Home, bool) {
	if _, err := s.Store.Q.GetProject(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "项目不存在")
		return nil, false
	} else if err != nil {
		writeErr(w, r, err)
		return nil, false
	}
	if s.Auth == nil || s.Auth.ProjectHomes == nil {
		WriteError(w, http.StatusInternalServerError, "internal", "项目配置存储未初始化")
		return nil, false
	}
	h, err := s.Auth.ProjectHomes.For(id)
	if err != nil {
		writeErr(w, r, err)
		return nil, false
	}
	return h, true
}

// ListProjectAgentConfigFiles 列出项目级配置文件（未写过时为空列表）。
func (s *Server) ListProjectAgentConfigFiles(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	if h, ok := s.projectHome(w, r, id); ok {
		listAgentFiles(w, r, h)
	}
}

// GetProjectAgentConfigFile 返回项目级配置文件原文。
func (s *Server) GetProjectAgentConfigFile(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, path apigen.AgentConfigPath) {
	if h, ok := s.projectHome(w, r, id); ok {
		getAgentFile(w, r, h, path)
	}
}

// PutProjectAgentConfigFile 写入项目级配置文件。成功后防抖下发到该项目运行中的 orb（M13）。
func (s *Server) PutProjectAgentConfigFile(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, path apigen.AgentConfigPath, params apigen.PutProjectAgentConfigFileParams) {
	if h, ok := s.projectHome(w, r, id); ok && putAgentFile(w, r, h, path, params.Mode, nil) {
		s.ompConfigChanged(id)
	}
}

// DeleteProjectAgentConfigFile 删除项目级配置文件。成功后防抖下发到该项目运行中的 orb（M13）。
func (s *Server) DeleteProjectAgentConfigFile(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, path apigen.AgentConfigPath) {
	if h, ok := s.projectHome(w, r, id); ok && deleteAgentFile(w, r, h, path) {
		s.ompConfigChanged(id)
	}
}

// ImportProjectAgentConfig 用 tar 包整体替换该项目的配置。成功后防抖下发到该项目运行中的 orb（M13）。
func (s *Server) ImportProjectAgentConfig(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	if h, ok := s.projectHome(w, r, id); ok && importAgentFiles(w, r, h, nil) {
		s.ompConfigChanged(id)
	}
}

// ApplyProjectAgentConfigBatch 批量增删项目级配置文件。成功后防抖下发到该项目运行中的 orb（M13）。
func (s *Server) ApplyProjectAgentConfigBatch(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	if h, ok := s.projectHome(w, r, id); ok && applyAgentBatch(w, r, h, nil) {
		s.ompConfigChanged(id)
	}
}
