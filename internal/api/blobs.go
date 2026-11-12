package api

import (
	"net/http"
	"os"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// GetBlob 对应 `GET blobs/{sha256}`：内容寻址，可长期缓存。
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
	mime := b.Mime
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, "", time.UnixMilli(b.CreatedAt), f)
}
