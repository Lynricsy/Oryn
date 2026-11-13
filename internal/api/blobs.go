package api

import (
	"net/http"
	"os"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/share"
)

// GetBlob 对应 `GET blobs/{sha256}`：内容寻址，可长期缓存；仅安全栅格图片内联（F7）。
func (s *Server) GetBlob(w http.ResponseWriter, r *http.Request, sha256 apigen.Sha256) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	if len(sha256) != 64 {
		WriteError(w, http.StatusBadRequest, "invalid_request", "sha256 must be 64 lowercase hex characters")
		return
	}
	b, path, err := svc.Blob(r.Context(), sha256)
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		WriteError(w, http.StatusNotFound, "not_found", "blob file missing")
		return
	}
	defer f.Close()
	h := w.Header()
	// 与公开 blob、/f 同一白名单：类型由内容探测（orb 提供的字节可能被探测成 text/html），
	// 只有安全栅格图片按原类型内联，其余一律附件下载
	if mt, img := share.InlineImage(b.Mime); img {
		h.Set("Content-Type", mt)
		h.Set("Content-Disposition", "inline")
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", "attachment")
	}
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, "", time.UnixMilli(b.CreatedAt), f)
}
