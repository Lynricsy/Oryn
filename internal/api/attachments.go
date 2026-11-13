package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// AttachmentsDeps 是消息附件领域的依赖（M3 计划 5.2）。
type AttachmentsDeps struct {
	Service *attachments.Service
}

// attachErrStatus 把附件服务的错误码映射为 HTTP 状态。
var attachErrStatus = map[string]int{
	attachments.CodeNotFound:        http.StatusNotFound,
	attachments.CodeThreadNotFound:  http.StatusNotFound,
	attachments.CodeAttachNotFound:  http.StatusNotFound,
	attachments.CodeTooLarge:        http.StatusRequestEntityTooLarge,
	attachments.CodeInvalid:         http.StatusBadRequest,
	attachments.CodeOrbNotReady:     http.StatusConflict,
	attachments.CodeOrbdOutdated:    http.StatusConflict,
	attachments.CodeOrbGone:         http.StatusGone,
	attachments.CodeOrbdUnreachable: http.StatusBadGateway,
}

func writeAttachErr(w http.ResponseWriter, err error) {
	var ae *attachments.Error
	if errors.As(err, &ae) {
		status, ok := attachErrStatus[ae.Code]
		if !ok {
			status = http.StatusBadRequest
		}
		if ae.Code == attachments.CodeNotFound || ae.Code == attachments.CodeThreadNotFound {
			WriteError(w, status, "not_found", ae.Message)
			return
		}
		WriteError(w, status, ae.Code, ae.Message)
		return
	}
	slog.Error("api: 附件请求失败", "err", err)
	WriteError(w, http.StatusInternalServerError, "internal", "internal error")
}

func (s *Server) attachmentsSvc(w http.ResponseWriter) (*attachments.Service, bool) {
	if s.Attachments == nil || s.Attachments.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "attachments service not configured")
		return nil, false
	}
	return s.Attachments.Service, true
}

// fillPublicURLs 为已发布的附件填 publicUrl（分享服务未装配或查询失败时省略）。
func (s *Server) fillPublicURLs(ctx context.Context, list []*apigen.Attachment) {
	if s.Shares == nil || s.Shares.Service == nil || len(list) == 0 {
		return
	}
	idList := make([]string, len(list))
	for i, a := range list {
		idList[i] = a.ID
	}
	urls, err := s.Shares.Service.PublicURLs(ctx, idList)
	if err != nil {
		slog.Warn("api: 读取附件公开 URL 失败", "err", err)
		return
	}
	for _, a := range list {
		if u, ok := urls[a.ID]; ok {
			a.PublicURL = &u
		}
	}
}

func attachmentAPI(a db.Attachment) apigen.Attachment {
	return apigen.Attachment{ID: a.ID, ThreadID: a.ThreadID, Name: a.Name, Mime: a.Mime, Size: a.Size, Sha256: a.Sha256, CreatedAt: a.CreatedAt}
}

// UploadAttachment 对应 `POST attachments?name=&threadId=`：请求体为原始字节（≤100MiB）。
func (s *Server) UploadAttachment(w http.ResponseWriter, r *http.Request, params apigen.UploadAttachmentParams) {
	svc, ok := s.attachmentsSvc(w)
	if !ok {
		return
	}
	if r.ContentLength > attachments.MaxBytes {
		WriteError(w, http.StatusRequestEntityTooLarge, attachments.CodeTooLarge, "attachment exceeds 100MiB")
		return
	}
	var threadID string
	if params.ThreadID != nil {
		threadID = *params.ThreadID
	}
	a, err := svc.Upload(r.Context(), params.Name, threadID, http.MaxBytesReader(w, r.Body, attachments.MaxBytes))
	if err != nil {
		writeAttachErr(w, err)
		return
	}
	out := attachmentAPI(a)
	s.fillPublicURLs(r.Context(), []*apigen.Attachment{&out})
	WriteJSON(w, http.StatusCreated, out)
}

// ListThreadAttachments 对应 `GET threads/{id}/attachments`。
func (s *Server) ListThreadAttachments(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.attachmentsSvc(w)
	if !ok {
		return
	}
	list, err := svc.List(r.Context(), id)
	if err != nil {
		writeAttachErr(w, err)
		return
	}
	out := apigen.AttachmentList{Attachments: make([]apigen.Attachment, 0, len(list))}
	for _, a := range list {
		out.Attachments = append(out.Attachments, attachmentAPI(a))
	}
	ptrs := make([]*apigen.Attachment, len(out.Attachments))
	for i := range out.Attachments {
		ptrs[i] = &out.Attachments[i]
	}
	s.fillPublicURLs(r.Context(), ptrs)
	WriteJSON(w, http.StatusOK, out)
}

// GetAttachmentContent 对应 `GET attachments/{id}/content?inline=`：默认附件下载；仅安全栅格图片可内联。
func (s *Server) GetAttachmentContent(w http.ResponseWriter, r *http.Request, id apigen.AttachmentIDPath, params apigen.GetAttachmentContentParams) {
	svc, ok := s.attachmentsSvc(w)
	if !ok {
		return
	}
	f, a, err := svc.Open(r.Context(), id)
	if err != nil {
		writeAttachErr(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	mt, img := share.InlineImage(a.Mime)
	if params.Inline != nil && *params.Inline && img {
		h.Set("Content-Type", mt)
		h.Set("Content-Disposition", "inline")
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", attachments.Disposition(a.Name))
	}
	h.Set("Cache-Control", "private, no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, "", time.UnixMilli(a.CreatedAt), f)
}

// DeleteAttachment 对应 `DELETE attachments/{id}`。
func (s *Server) DeleteAttachment(w http.ResponseWriter, r *http.Request, id apigen.AttachmentIDPath) {
	svc, ok := s.attachmentsSvc(w)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), id); err != nil {
		writeAttachErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
