package api

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/repos"
)

// ReposDeps 是项目仓库镜像接口（M24）的依赖。
type ReposDeps struct {
	Service *repos.Service
}

func (s *Server) reposSvc(w http.ResponseWriter) (*repos.Service, bool) {
	if s.Repos == nil || s.Repos.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "repos service not configured")
		return nil, false
	}
	return s.Repos.Service, true
}

// writeRepoErr 写出仓库服务的错误：*repos.Error 按其 Status / Code 原样输出，其余按通用映射。
func writeRepoErr(w http.ResponseWriter, r *http.Request, err error) {
	var re *repos.Error
	if errors.As(err, &re) {
		WriteError(w, re.Status, re.Code, re.Message)
		return
	}
	writeErr(w, r, err)
}

// GetProjectRepo 返回项目仓库镜像状态；未归档项目按需在后台克隆或拉取。
func (s *Server) GetProjectRepo(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	st, err := svc.Status(r.Context(), id)
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, st)
}

// SyncProjectRepo 立即在后台克隆或拉取项目仓库镜像（已归档项目也可以）。
func (s *Server) SyncProjectRepo(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	st, err := svc.Sync(r.Context(), id)
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, st)
}
