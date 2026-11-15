package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// LibraryDeps 是全局助手文件库的依赖（M13 计划 4.1）。
type LibraryDeps struct {
	Service *library.Service
}

func (s *Server) librarySvc(w http.ResponseWriter) (*library.Service, bool) {
	if s.Library == nil || s.Library.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "assistant file library not configured")
		return nil, false
	}
	return s.Library.Service, true
}

// writeLibraryErr 把文件库的错误映射为 HTTP 状态。
func writeLibraryErr(w http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, library.ErrNotFound), errors.Is(err, library.ErrThreadNotFound):
		WriteError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, library.ErrTooLarge), errors.As(err, &mbe):
		WriteError(w, http.StatusRequestEntityTooLarge, attachments.CodeTooLarge, "file exceeds 100MiB")
	case errors.Is(err, library.ErrInvalid), errors.Is(err, library.ErrForbidden):
		WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeErr(w, r, err)
	}
}

// ListAssistantFiles 对应 `GET assistant/files?threadId=&scope=`：只列已随消息发送的文件；threadId 只看该对话
// 可见的（它自己的与共享的）。
func (s *Server) ListAssistantFiles(w http.ResponseWriter, r *http.Request, params apigen.ListAssistantFilesParams) {
	svc, ok := s.librarySvc(w)
	if !ok {
		return
	}
	var f library.Filter
	if params.ThreadID != nil {
		f.ThreadID = *params.ThreadID
	}
	if params.Scope != nil {
		f.Scope = string(*params.Scope)
	}
	list, err := svc.List(r.Context(), f)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	out := apigen.AssistantFileList{Files: make([]apigen.AssistantFile, 0, len(list))}
	for _, f := range list {
		out.Files = append(out.Files, f.API())
	}
	WriteJSON(w, http.StatusOK, out)
}

// UploadAssistantFile 对应 `POST assistant/files?name=&threadId=`：请求体为原始字节（≤100MiB）。
// threadId 给出时必须是全局助手对话；未随消息发送的文件 24h 后被清理。
func (s *Server) UploadAssistantFile(w http.ResponseWriter, r *http.Request, params apigen.UploadAssistantFileParams) {
	svc, ok := s.librarySvc(w)
	if !ok {
		return
	}
	if r.ContentLength > attachments.MaxBytes {
		WriteError(w, http.StatusRequestEntityTooLarge, attachments.CodeTooLarge, "file exceeds 100MiB")
		return
	}
	var threadID string
	if params.ThreadID != nil {
		threadID = *params.ThreadID
		t, err := s.Store.Q.GetThread(r.Context(), threadID)
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusNotFound, "not_found", "thread not found")
			return
		}
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if t.Kind != threads.KindAssistant {
			WriteError(w, http.StatusBadRequest, "invalid_request", "threadId 必须是全局助手对话")
			return
		}
	}
	f, err := svc.Upload(r.Context(), params.Name, threadID, http.MaxBytesReader(w, r.Body, attachments.MaxBytes))
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, f.API())
}

// PatchAssistantFile 对应 `PATCH assistant/files/{id}`：切换可见范围。
func (s *Server) PatchAssistantFile(w http.ResponseWriter, r *http.Request, id apigen.AssistantFileIDPath) {
	svc, ok := s.librarySvc(w)
	if !ok {
		return
	}
	var req apigen.AssistantFilePatch
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := svc.SetScope(r.Context(), id, string(req.Scope)); err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	f, err := svc.Get(r.Context(), id)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, f.API())
}

// DeleteAssistantFile 对应 `DELETE assistant/files/{id}`。
func (s *Server) DeleteAssistantFile(w http.ResponseWriter, r *http.Request, id apigen.AssistantFileIDPath) {
	svc, ok := s.librarySvc(w)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), id); err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetAssistantFileContent 对应 `GET assistant/files/{id}/content?inline=`：响应头与 Range 同 getAttachmentContent。
func (s *Server) GetAssistantFileContent(w http.ResponseWriter, r *http.Request, id apigen.AssistantFileIDPath, params apigen.GetAssistantFileContentParams) {
	svc, ok := s.librarySvc(w)
	if !ok {
		return
	}
	content, f, err := svc.Open(r.Context(), id)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	defer content.Close()
	serveStoredFile(w, r, content, f.Name, f.Mime, f.CreatedAt, params.Inline != nil && *params.Inline)
}
