package api

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

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

// ListProjectRepoRefs 列出项目仓库镜像的分支或标签。
func (s *Server) ListProjectRepoRefs(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, params apigen.ListProjectRepoRefsParams) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	out, err := svc.Refs(r.Context(), id, params.Kind, deref(params.Cursor), params.Limit)
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// ListProjectRepoCommits 列出项目仓库镜像上 ref 的提交。
func (s *Server) ListProjectRepoCommits(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, params apigen.ListProjectRepoCommitsParams) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	out, err := svc.Commits(r.Context(), id, deref(params.Ref), deref(params.Path), deref(params.Cursor), params.Limit)
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetProjectRepoCommit 返回项目仓库镜像里一个提交的详情。
func (s *Server) GetProjectRepoCommit(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, sha apigen.RepoShaPath) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	out, err := svc.Commit(r.Context(), id, sha)
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetProjectRepoCommitAvatars 返回提交作者与共同作者对上的 GitHub 账号与头像（查不到时为空列表）。
func (s *Server) GetProjectRepoCommitAvatars(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, sha apigen.RepoShaPath) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	out, err := svc.CommitAvatars(r.Context(), id, sha)
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetProjectRepoCommitDiff 返回提交里一个文件的 diff。
func (s *Server) GetProjectRepoCommitDiff(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, sha apigen.RepoShaPath, params apigen.GetProjectRepoCommitDiffParams) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	out, err := svc.CommitDiff(r.Context(), id, sha, params.Path, deref(params.OldPath))
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetProjectRepoTree 返回项目仓库镜像在 ref 下的目录或文件。
func (s *Server) GetProjectRepoTree(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, params apigen.GetProjectRepoTreeParams) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	lastCommits := params.LastCommits == nil || *params.LastCommits
	out, err := svc.Tree(r.Context(), id, deref(params.Ref), deref(params.Path), lastCommits)
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetProjectRepoBlob 返回项目仓库镜像里一个文件的内容：ETag 是 blob oid；安全的栅格图片内联（download 时除外），其余附件下载。
// Git LFS 对象是本地文件时经 http.ServeContent（支持 Range），其余流式输出。
func (s *Server) GetProjectRepoBlob(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, params apigen.GetProjectRepoBlobParams) {
	svc, ok := s.reposSvc(w)
	if !ok {
		return
	}
	b, err := svc.Blob(r.Context(), id, deref(params.Ref), params.Path, deref(params.IfNoneMatch), deref(params.Download))
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	h := w.Header()
	h.Set("ETag", `"`+b.OID+`"`)
	if b.Immutable {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	if b.NotModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if b.File != nil {
		defer b.File.Close()
	} else {
		defer b.Body.Close()
	}
	disp := "attachment"
	if b.Inline {
		disp = "inline"
	} else if v := mime.FormatMediaType("attachment", map[string]string{"filename": b.Name}); v != "" {
		disp = v
	}
	h.Set("Content-Type", b.ContentType)
	h.Set("Content-Disposition", disp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	if b.File != nil {
		http.ServeContent(w, r, "", time.Time{}, b.File)
		return
	}
	h.Set("Content-Length", strconv.FormatInt(b.Size, 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, b.Body); err != nil {
		// 头已发出：中止连接，让客户端把这次下载当作失败而不是收到半截内容
		slog.Warn("输出项目仓库文件", "project", id, "path", params.Path, "err", err)
		panic(http.ErrAbortHandler)
	}
}
