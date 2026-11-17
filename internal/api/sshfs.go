package api

import (
	"errors"
	"net/http"
	"path"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
)

// ---- SSH 主机的文件浏览（M18，经 SFTP；响应形状与错误码同 orb 的 fs/*）----

// GetSSHFsRoots 对应 `GET /api/v1/ssh/hosts/{id}/fs/roots`。
func (s *Server) GetSSHFsRoots(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	roots, err := svc.FsRoots(r.Context(), id)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, roots)
}

// ListSSHFs 对应 `GET /api/v1/ssh/hosts/{id}/fs/list`。
func (s *Server) ListSSHFs(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath, params apigen.ListSSHFsParams) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	list, err := svc.FsList(r.Context(), id, params.Path)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, list)
}

// StatSSHFs 对应 `GET /api/v1/ssh/hosts/{id}/fs/stat`。
func (s *Server) StatSSHFs(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath, params apigen.StatSSHFsParams) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	e, err := svc.FsStat(r.Context(), id, params.Path)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, e)
}

// ReadSSHFile 对应 `GET /api/v1/ssh/hosts/{id}/fs/file`（支持 Range）：一律以附件形式返回。
func (s *Server) ReadSSHFile(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath, params apigen.ReadSSHFileParams) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	f, err := svc.FsOpen(r.Context(), id, params.Path)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", attachments.Disposition(path.Base(f.Path)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "no-store")
	h.Set("ETag", f.Etag())
	http.ServeContent(w, r, "", f.Info.ModTime(), f)
}

// WriteSSHFile 对应 `PUT /api/v1/ssh/hosts/{id}/fs/file`（流式上传，≤512MiB）。
func (s *Server) WriteSSHFile(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath, params apigen.WriteSSHFileParams) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	if r.ContentLength > MaxFileBytes {
		WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "文件超过 512MiB")
		return
	}
	overwrite := params.Overwrite == nil || *params.Overwrite
	ifm := ""
	if params.IfMatch != nil {
		ifm = *params.IfMatch
	}
	e, err := svc.FsWrite(r.Context(), id, params.Path, overwrite, ifm, http.MaxBytesReader(w, r.Body, MaxFileBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "文件超过 512MiB")
			return
		}
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, e)
}

// MkdirSSHFs 对应 `POST /api/v1/ssh/hosts/{id}/fs/mkdir`。
func (s *Server) MkdirSSHFs(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	var in apigen.FsPathRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	e, err := svc.FsMkdir(r.Context(), id, in.Path)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, e)
}

// RenameSSHFs 对应 `POST /api/v1/ssh/hosts/{id}/fs/rename`。
func (s *Server) RenameSSHFs(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	var in apigen.FsRenameRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	e, err := svc.FsRename(r.Context(), id, in.From, in.To, in.Overwrite != nil && *in.Overwrite)
	if err != nil {
		writeSSHErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, e)
}

// DeleteSSHFs 对应 `POST /api/v1/ssh/hosts/{id}/fs/delete`。
func (s *Server) DeleteSSHFs(w http.ResponseWriter, r *http.Request, id apigen.SSHHostIDPath) {
	svc, ok := s.sshSvc(w)
	if !ok {
		return
	}
	var in apigen.FsPathRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := svc.FsDelete(r.Context(), id, in.Path); err != nil {
		writeSSHErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
