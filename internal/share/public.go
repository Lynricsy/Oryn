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
		Mode: apigen.ShareMode(sh.Mode), Title: publicTitle(sc.thread.Title), SharedAt: sh.CreatedAt,
		ThreadCreatedAt: sc.thread.CreatedAt, LastSeq: sc.until, LeafEntryID: strPtr(sc.leaf),
		TranscriptRev: sc.thread.TranscriptRev, Attachments: []apigen.PublicAttachment{},
	}
	if sh.Mode == string(apigen.ShareModeLive) {
		st := apigen.ThreadState(sc.thread.State)
		out.State = &st
	}
	tid := sh.ThreadID
	atts, err := s.st.Q.ListAttachmentsByThread(ctx, &tid)
	if err != nil {
		return apigen.PublicShare{}, err
	}
	var keep []db.Attachment
	for _, a := range atts {
		// 快照只含分享创建时已存在的附件
		if sh.Mode == string(apigen.ShareModeSnapshot) && a.CreatedAt > sh.CreatedAt {
			continue
		}
		keep = append(keep, a)
	}
	urls, err := s.publicURLsOf(ctx, keep)
	if err != nil {
		return apigen.PublicShare{}, err
	}
	for _, a := range keep {
		pa := apigen.PublicAttachment{ID: a.ID, Name: a.Name, Size: a.Size, Mime: a.Mime}
		if u, ok := urls[a.ID]; ok {
			pa.URL = &u
		}
		out.Attachments = append(out.Attachments, pa)
	}
	return out, nil
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
	seqs, err := s.st.Q.ListEntrySeqsContaining(ctx, db.ListEntrySeqsContainingParams{
		ThreadID: sh.ThreadID, UntilSeq: sc.until, Needle: []byte("blob:sha256:" + sha),
	})
	if err != nil {
		return db.Blob{}, nil, err
	}
	allowed := false
	for _, q := range seqs {
		if sc.onPath[q] {
			allowed = true
			break
		}
	}
	if !allowed {
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
