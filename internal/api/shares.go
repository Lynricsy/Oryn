package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// ShareDeps 是公开分享与文件发布领域的依赖（M5 步骤 2）。
type ShareDeps struct {
	Service *share.Service
}

func (s *Server) shareSvc(w http.ResponseWriter) (*share.Service, bool) {
	if s.Shares == nil || s.Shares.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "share service not configured")
		return nil, false
	}
	return s.Shares.Service, true
}

// writeThreadShareErr 写出分享服务的错误：*share.Error 按其状态码，附件服务的错误按附件领域的映射。
func writeThreadShareErr(w http.ResponseWriter, err error) {
	var se *share.Error
	if errors.As(err, &se) {
		WriteError(w, se.Status, se.Code, se.Message)
		return
	}
	var ae *attachments.Error
	if errors.As(err, &ae) {
		writeAttachErr(w, err)
		return
	}
	slog.Error("api: 分享请求失败", "err", err)
	WriteError(w, http.StatusInternalServerError, "internal", "internal error")
}

// ListThreadShares 对应 `GET threads/{id}/shares`。
func (s *Server) ListThreadShares(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	list, err := svc.List(r.Context(), id)
	if err != nil {
		writeThreadShareErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ThreadShareList{Shares: list})
}

// CreateThreadShare 对应 `POST threads/{id}/shares`：扫描有命中且未确认 → 409 ShareScanConflict。
func (s *Server) CreateThreadShare(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	var req apigen.CreateThreadShareRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid", "invalid request body: "+err.Error())
		return
	}
	if t, err := s.Store.Q.GetThread(r.Context(), id); err == nil && t.Kind != threads.KindWork {
		WriteError(w, http.StatusBadRequest, threads.CodeCoordinatorRestricted, "coordinator conversations cannot be shared")
		return
	}
	ack := req.AcknowledgeFindings != nil && *req.AcknowledgeFindings
	sh, err := svc.Create(r.Context(), id, req.Mode, ack)
	var scanErr *share.ScanError
	if errors.As(err, &scanErr) {
		WriteJSON(w, http.StatusConflict, apigen.ShareScanConflict{
			Code: apigen.ShareScanConflictCodeSecretsFound, Message: "transcript contains secret-like strings", Scan: scanErr.Scan,
		})
		return
	}
	if err != nil {
		writeThreadShareErr(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, sh)
}

// ListShares 对应 `GET shares`。
func (s *Server) ListShares(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	ov, err := svc.Overview(r.Context())
	if err != nil {
		writeThreadShareErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, ov)
}

// RevokeShare 对应 `DELETE shares/{id}`。
func (s *Server) RevokeShare(w http.ResponseWriter, r *http.Request, id apigen.ShareIDPath) {
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	if err := svc.Revoke(r.Context(), id); err != nil {
		writeThreadShareErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PublishAttachment 对应 `POST attachments/{id}/publish`（幂等）。
func (s *Server) PublishAttachment(w http.ResponseWriter, r *http.Request, id apigen.AttachmentIDPath) {
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	pf, err := svc.Publish(r.Context(), id)
	if err != nil {
		writeThreadShareErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, pf)
}

// PublishOrbFile 对应 `POST orbs/{id}/fs/publish`：把 orb 文件此刻的副本存为附件并发布。
func (s *Server) PublishOrbFile(w http.ResponseWriter, r *http.Request, id apigen.OrbIDPath) {
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	var req apigen.PublishOrbFileRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid", "invalid request body: "+err.Error())
		return
	}
	pf, err := svc.PublishOrbFile(r.Context(), id, req.ThreadID, req.Path)
	if err != nil {
		writeThreadShareErr(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, pf)
}

// RevokePublishedFile 对应 `DELETE published-files/{id}`。
func (s *Server) RevokePublishedFile(w http.ResponseWriter, r *http.Request, id apigen.PublishedFileIDPath) {
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	if err := svc.Unpublish(r.Context(), id); err != nil {
		writeThreadShareErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- 匿名（/api/v1/public/，免鉴权；全部响应 noindex + no-store，未命中一律 404 not_found） ----

func setPublicNoIndex(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "no-store")
}

// GetPublicShare 对应 `GET public/shares/{slug}`。
func (s *Server) GetPublicShare(w http.ResponseWriter, r *http.Request, slug apigen.ShareSlugPath) {
	setPublicNoIndex(w)
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	ps, err := svc.PublicShare(r.Context(), slug)
	if err != nil {
		writeThreadShareErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, ps)
}

// ListPublicShareEntries 对应 `GET public/shares/{slug}/entries?after=&limit=`。
func (s *Server) ListPublicShareEntries(w http.ResponseWriter, r *http.Request, slug apigen.ShareSlugPath, params apigen.ListPublicShareEntriesParams) {
	setPublicNoIndex(w)
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	var after int64
	if params.After != nil {
		after = *params.After
	}
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	page, err := svc.PublicEntries(r.Context(), slug, after, limit)
	if err != nil {
		writeThreadShareErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, page)
}

// GetPublicShareBlob 对应 `GET public/shares/{slug}/blobs/{sha256}`：只放行范围内公开条目引用的图片。
// 与 `/f` 同一白名单（F7）：安全栅格图片按原类型内联，其余（类型由内容探测，可能是 text/html）一律附件下载。
func (s *Server) GetPublicShareBlob(w http.ResponseWriter, r *http.Request, slug apigen.ShareSlugPath, sha256 apigen.Sha256) {
	setPublicNoIndex(w)
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	b, f, err := svc.PublicBlob(r.Context(), slug, sha256)
	if err != nil {
		writeThreadShareErr(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	if mt, img := share.InlineImage(b.Mime); img {
		h.Set("Content-Type", mt)
		h.Set("Content-Disposition", "inline")
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", "attachment")
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, "", time.UnixMilli(b.CreatedAt), f)
}
