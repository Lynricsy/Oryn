package share

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// MaxOrbFileBytes 是「发布 orb 文件」的单文件上限（= 附件上限）。
	MaxOrbFileBytes = attachments.MaxBytes
	// orbWait 是发布 orb 文件时等待 orb 唤醒的上限。
	orbWait = 25 * time.Second
)

// inlineImageMimes 是可按原类型内联显示的安全栅格图片（SVG 等能执行脚本的类型不在内）。
var inlineImageMimes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// InlineImage 返回 mime 去掉参数、转小写后的媒体类型，以及它是否在可内联的栅格图片白名单里。
// 公开文件、公开 blob 与私有附件的内联判定都只用它（F7）。
func InlineImage(mime string) (string, bool) {
	mt, _, _ := strings.Cut(mime, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	return mt, inlineImageMimes[mt]
}

// encodeURIComponent 与 JS 同名函数一致：除 A-Z a-z 0-9 - _ . ! ~ * ' ( ) 外按 UTF-8 字节百分号编码。
func encodeURIComponent(s string) string {
	const unreserved = "-_.!~*'()"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func (s *Service) fileURL(slug, name string) string {
	return s.cfg.PublicURL + "/f/" + slug + "/" + encodeURIComponent(name)
}

func (s *Service) fileAPI(p db.PublishedFile, a db.Attachment) apigen.PublishedFile {
	return apigen.PublishedFile{
		ID: p.ID, AttachmentID: a.ID, Name: a.Name, Mime: a.Mime, Size: a.Size, URL: s.fileURL(p.Slug, a.Name),
		CreatedAt: p.CreatedAt, ThreadID: a.ThreadID,
	}
}

// Publish 把附件发布为公开 URL（幂等：已发布则返回现有）。附件须已绑定线程。
func (s *Service) Publish(ctx context.Context, attachmentID string) (apigen.PublishedFile, error) {
	a, err := s.att.Get(ctx, attachmentID)
	if err != nil {
		return apigen.PublishedFile{}, err
	}
	if a.ThreadID == nil {
		return apigen.PublishedFile{}, errf(http.StatusBadRequest, "invalid", "attachment is not bound to a thread")
	}
	for range 2 {
		p, err := s.st.Q.GetActivePublishedFileByAttachment(ctx, a.ID)
		if err == nil {
			return s.fileAPI(p, a), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return apigen.PublishedFile{}, err
		}
		row := db.InsertPublishedFileParams{ID: ids.New("pub"), AttachmentID: a.ID, Slug: newSlug(), CreatedAt: s.now()}
		if err := s.st.Q.InsertPublishedFile(ctx, row); err != nil {
			// 并发发布撞上唯一索引：回到开头取已存在的那一行
			continue
		}
		return s.fileAPI(db.PublishedFile{ID: row.ID, AttachmentID: row.AttachmentID, Slug: row.Slug, CreatedAt: row.CreatedAt}, a), nil
	}
	return apigen.PublishedFile{}, errf(http.StatusConflict, "invalid", "concurrent publish, retry")
}

// PublishOrbFile 把 orb 内的文件复制为线程附件后发布（发布的是此刻的副本，之后 orb 里的修改不影响它）。
func (s *Service) PublishOrbFile(ctx context.Context, orbID, threadID, p string) (apigen.PublishedFile, error) {
	th, err := s.getThread(ctx, threadID)
	if err != nil {
		return apigen.PublishedFile{}, err
	}
	if th.OrbID == nil || *th.OrbID != orbID {
		return apigen.PublishedFile{}, errf(http.StatusBadRequest, "invalid", "thread is not on this orb")
	}
	if strings.TrimSpace(p) == "" {
		return apigen.PublishedFile{}, errf(http.StatusBadRequest, "invalid", "path is required")
	}
	if err := s.orbs.EnsureUsable(ctx, orbID, orbs.FeatureFS, true, orbWait); err != nil {
		return apigen.PublishedFile{}, orbError(err)
	}
	q := url.Values{"path": {p}}.Encode()
	var st apigen.FsEntry
	if err := s.orbJSON(ctx, orbID, "/fs/stat?"+q, &st); err != nil {
		return apigen.PublishedFile{}, err
	}
	kind := st.Type
	if kind == apigen.FsEntryTypeSymlink && st.TargetType != nil {
		kind = apigen.FsEntryType(*st.TargetType)
	}
	switch {
	case kind == apigen.FsEntryTypeDir:
		return apigen.PublishedFile{}, errf(http.StatusConflict, "is_a_directory", "path is a directory")
	case kind != apigen.FsEntryTypeFile:
		return apigen.PublishedFile{}, errf(http.StatusBadRequest, "invalid", "path is not a regular file")
	case st.Size > MaxOrbFileBytes:
		return apigen.PublishedFile{}, errf(http.StatusRequestEntityTooLarge, "too_large", "file exceeds %d bytes", MaxOrbFileBytes)
	}
	resp, err := s.orbGet(ctx, orbID, "/fs/file?"+q)
	if err != nil {
		return apigen.PublishedFile{}, err
	}
	defer resp.Body.Close()
	name := path.Base(st.Path)
	if name == "." || name == "/" || name == "" {
		name = path.Base(p)
	}
	a, err := s.att.Upload(ctx, name, threadID, resp.Body)
	if err != nil {
		return apigen.PublishedFile{}, err
	}
	pf, err := s.Publish(ctx, a.ID)
	if err != nil {
		return apigen.PublishedFile{}, err
	}
	return pf, nil
}

// orbGet 发 GET 到 orbd；非 2xx 时把 orbd 的 {code,message} 转成 *Error（状态码沿用）。
func (s *Service) orbGet(ctx context.Context, orbID, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://orb"+target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.orbs.Do(ctx, orbID, req)
	if err != nil {
		if e := orbError(err); e != err {
			return nil, e
		}
		return nil, errf(http.StatusBadGateway, "orbd_unreachable", "orbd unreachable: %v", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return resp, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct{ Code, Message string }
	if json.Unmarshal(b, &e) == nil && e.Code != "" && resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return nil, &Error{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	return nil, errf(http.StatusBadGateway, "orbd_bad_response", "orbd returned %d", resp.StatusCode)
}

func (s *Service) orbJSON(ctx context.Context, orbID, target string, out any) error {
	resp, err := s.orbGet(ctx, orbID, target)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errf(http.StatusBadGateway, "orbd_bad_response", "decode orbd response: %v", err)
	}
	return nil
}

func orbError(err error) error {
	switch {
	case errors.Is(err, orbs.ErrOrbdOutdated):
		return errf(http.StatusConflict, "orbd_outdated", "orbd does not support files; start a new thread")
	case errors.Is(err, orbs.ErrOrbGone):
		return errf(http.StatusGone, "orb_deleted", "orb deleted")
	case errors.Is(err, orbs.ErrOrbNotReady):
		return errf(http.StatusConflict, "orb_not_ready", "orb not ready")
	}
	return err
}

// Unpublish 撤回已发布的文件；不存在或已撤回 → ErrNotFound。
func (s *Service) Unpublish(ctx context.Context, id string) error {
	n, err := s.st.Q.RevokePublishedFile(ctx, db.RevokePublishedFileParams{Now: s.now(), ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PublicURLs 返回附件 id → 公开 URL（只含已发布的）。
func (s *Service) PublicURLs(ctx context.Context, attachmentIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if len(attachmentIDs) == 0 {
		return out, nil
	}
	rows, err := s.st.Q.ListActivePublishedFilesByAttachments(ctx, attachmentIDs)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		a, err := s.att.Get(ctx, p.AttachmentID)
		if err != nil {
			continue // 刚被删除
		}
		out[p.AttachmentID] = s.fileURL(p.Slug, a.Name)
	}
	return out, nil
}

// publicURLsOf 与 PublicURLs 相同，但附件行已在手（不再逐个读）。
func (s *Service) publicURLsOf(ctx context.Context, atts []db.Attachment) (map[string]string, error) {
	out := map[string]string{}
	if len(atts) == 0 {
		return out, nil
	}
	idList := make([]string, len(atts))
	byID := make(map[string]db.Attachment, len(atts))
	for i, a := range atts {
		idList[i] = a.ID
		byID[a.ID] = a
	}
	rows, err := s.st.Q.ListActivePublishedFilesByAttachments(ctx, idList)
	if err != nil {
		return nil, err
	}
	for _, p := range rows {
		out[p.AttachmentID] = s.fileURL(p.Slug, byID[p.AttachmentID].Name)
	}
	return out, nil
}

// FileHandler 挂在 `/f/*`：`/f/<slug>/<name>`，name 解码后须等于附件名。只接受 GET / HEAD；支持 Range。
func (s *Service) FileHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowRead(w, r) {
			return
		}
		h := w.Header()
		setPublicHeaders(h)
		slug, name, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/f/"), "/")
		if !ok || !ValidSlug(slug) || name == "" {
			http.NotFound(w, r)
			return
		}
		row, err := s.st.Q.GetActivePublishedFileBySlug(r.Context(), slug)
		if err != nil || row.Attachment.Name != name {
			http.NotFound(w, r)
			return
		}
		f, a, err := s.att.Open(r.Context(), row.Attachment.ID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		mt, img := InlineImage(a.Mime)
		switch {
		case img:
			h.Set("Content-Type", mt)
			h.Set("Content-Disposition", inlineDisposition(a.Name))
		case strings.HasPrefix(mt, "text/") && mt != "text/html":
			h.Set("Content-Type", "text/plain; charset=utf-8")
			h.Set("Content-Disposition", inlineDisposition(a.Name))
		default:
			h.Set("Content-Type", "application/octet-stream")
			h.Set("Content-Disposition", attachments.Disposition(a.Name))
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "sandbox")
		http.ServeContent(w, r, "", time.UnixMilli(a.CreatedAt), f)
	})
}
