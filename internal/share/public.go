package share

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// 匿名条目分页的默认与上限。
const (
	defaultPublicLimit = 200
	maxPublicLimit     = 500
)

// untitled 是公开面上空标题的替代文字（固定英文，同 `.md` 导出）。
const untitled = "Untitled"

func publicTitle(t string) string {
	if t == "" {
		return untitled
	}
	return t
}

// EntryPage 是 `GET public/shares/{slug}/entries` 的响应（与 apigen.PublicEntryPage 同形，payload 保持原始字节）。
type EntryPage struct {
	Entries     []transcript.Entry `json:"entries"`
	HasMore     bool               `json:"hasMore"`
	LastSeq     int64              `json:"lastSeq"`
	LeafEntryID *string            `json:"leafEntryId,omitempty"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// PublicShare 对应 `GET public/shares/{slug}`：不含线程 id、orb、项目、标签、模型配置。
func (s *Service) PublicShare(ctx context.Context, slug string) (apigen.PublicShare, error) {
	sh, err := s.activeBySlug(ctx, slug)
	if err != nil {
		return apigen.PublicShare{}, err
	}
	sc, err := s.scopeOf(ctx, sh)
	if err != nil {
		return apigen.PublicShare{}, err
	}
	out := apigen.PublicShare{
		Mode: apigen.ShareMode(sh.Mode), Title: publicTitle(sc.title), SharedAt: sh.CreatedAt,
		ThreadCreatedAt: sc.thread.CreatedAt, LastSeq: sc.until, LeafEntryID: strPtr(sc.leaf),
		TranscriptRev: sc.thread.TranscriptRev, Attachments: []apigen.PublicAttachment{},
	}
	if sh.Mode == string(apigen.ShareModeLive) {
		st := apigen.ThreadState(sc.thread.State)
		out.State = &st
	}
	atts, urls, err := s.publicAttachmentsURLs(ctx, sc)
	if err != nil {
		return apigen.PublicShare{}, err
	}
	for _, a := range atts {
		pa := apigen.PublicAttachment{ID: a.ID, Name: a.Name, Size: a.Size, Mime: a.Mime}
		if u, ok := urls[a.ID]; ok {
			pa.URL = &u
		}
		out.Attachments = append(out.Attachments, pa)
	}
	return out, nil
}

// publicAttachments 返回公开面上列出的附件：已发布的，或被范围内公开（清洗后）条目引用的（F4）；
// 快照只考虑分享创建时已存在的附件。其余附件的名字与元数据不出控制面。
func (s *Service) publicAttachments(ctx context.Context, sc *scope) ([]db.Attachment, error) {
	atts, _, err := s.publicAttachmentsURLs(ctx, sc)
	return atts, err
}

// publicAttachmentsURLs 同 publicAttachments，另返回其中已发布附件的公开 URL。
func (s *Service) publicAttachmentsURLs(ctx context.Context, sc *scope) ([]db.Attachment, map[string]string, error) {
	tid := sc.thread.ID
	all, err := s.st.Q.ListAttachmentsByThread(ctx, &tid)
	if err != nil {
		return nil, nil, err
	}
	var cand []db.Attachment
	for _, a := range all {
		if sc.share.Mode == string(apigen.ShareModeSnapshot) && a.CreatedAt > sc.share.CreatedAt {
			continue
		}
		cand = append(cand, a)
	}
	urls, err := s.publicURLsOf(ctx, cand)
	if err != nil {
		return nil, nil, err
	}
	keep := make([]db.Attachment, 0, len(cand))
	for _, a := range cand {
		if _, published := urls[a.ID]; !published {
			// 消息里引用附件的是 orb 内路径 `…/.oryn/attachments/<threadId>/<id>/<name>`：以附件 id 判定
			ok, err := sc.references(ctx, s.st.Q, []byte(a.ID))
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				continue
			}
		}
		keep = append(keep, a)
	}
	return keep, urls, nil
}

// PublicEntries 对应 `GET public/shares/{slug}/entries?after=&limit=`。
func (s *Service) PublicEntries(ctx context.Context, slug string, after int64, limit int) (EntryPage, error) {
	sh, err := s.activeBySlug(ctx, slug)
	if err != nil {
		return EntryPage{}, err
	}
	sc, err := s.scopeOf(ctx, sh)
	if err != nil {
		return EntryPage{}, err
	}
	if limit <= 0 {
		limit = defaultPublicLimit
	}
	limit = min(limit, maxPublicLimit)
	es, more, err := sc.page(ctx, s.st.Q, max(after, 0), sc.until, limit)
	if err != nil {
		return EntryPage{}, err
	}
	return EntryPage{Entries: es, HasMore: more, LastSeq: sc.until, LeafEntryID: strPtr(sc.leaf)}, nil
}

// PublicBlob 对应 `GET public/shares/{slug}/blobs/{sha256}`：只放行范围内活动分支上的条目引用的 blob。
// 返回的文件由调用方关闭。
func (s *Service) PublicBlob(ctx context.Context, slug, sha string) (db.Blob, *os.File, error) {
	if !validSha(sha) {
		return db.Blob{}, nil, ErrNotFound
	}
	sh, err := s.activeBySlug(ctx, slug)
	if err != nil {
		return db.Blob{}, nil, err
	}
	sc, err := s.scopeOf(ctx, sh)
	if err != nil {
		return db.Blob{}, nil, err
	}
	// 授权以清洗后的公开内容为准：隐藏条目里的引用不算（B8）
	ok, err := sc.references(ctx, s.st.Q, []byte("blob:sha256:"+sha))
	if err != nil {
		return db.Blob{}, nil, err
	}
	if !ok {
		return db.Blob{}, nil, ErrNotFound
	}
	b, path, err := s.th.Blob(ctx, sha)
	if err != nil {
		// 线程服务的业务错误（blob 不存在等）在匿名面上一律是 404
		if te := (*threads.Error)(nil); errors.As(err, &te) {
			return db.Blob{}, nil, ErrNotFound
		}
		return db.Blob{}, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return db.Blob{}, nil, ErrNotFound
	}
	return b, f, nil
}

func validSha(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// WSHandler 处理 `GET /api/v1/public/shares/{slug}/ws?after=&rev=`（仅实时分享；快照与不存在 → 404）。
func (s *Service) WSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPublicHeaders(w.Header())
		ctx := r.Context()
		sh, err := s.activeBySlug(ctx, chi.URLParam(r, "slug"))
		if err != nil || sh.Mode != string(apigen.ShareModeLive) {
			writeNotFoundJSON(w)
			return
		}
		q := r.URL.Query()
		after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
		var rev *int64
		if v := q.Get("rev"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				rev = &n
			}
		}
		threadID := sh.ThreadID
		s.hub.ServePublic(w, r, realtime.PublicSub{
			ShareID:  sh.ID,
			ThreadID: threadID,
			Backfill: func(ctx context.Context, after, upTo int64) ([]threads.Entry, error) {
				sc, err := s.scopeOf(ctx, sh)
				if err != nil {
					return nil, err
				}
				es, err := sc.all(ctx, s.st.Q, after, upTo)
				if err != nil {
					return nil, err
				}
				out := make([]threads.Entry, len(es))
				for i, e := range es {
					out[i] = toThreads(e)
				}
				return out, nil
			},
			// 实时到达的主会话条目不再做分支判定：新条目都接在当前叶子之后。
			// 合成行（标题槽 `__title__`、会话头 `__session__`）不在任何分支上，同补发一样不发
			Filter: func(e threads.Entry) (threads.Entry, bool) {
				if strings.HasPrefix(e.EntryID, "__") {
					return threads.Entry{}, false
				}
				pe, ok := transcript.Public(fromThreads(e))
				return toThreads(pe), ok
			},
		}, max(after, 0), rev)
	})
}

// setPublicHeaders 设置公开面全部响应共有的头。
func setPublicHeaders(h http.Header) {
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Cache-Control", "no-store")
}

func writeNotFoundJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"code":"not_found","message":"not found"}` + "\n"))
}
