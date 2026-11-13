// Package share 实现线程的公开分享与文件发布（M5 步骤 2）。
//
// 公开面是一条独立、默认拒绝的出口：匿名 REST 在 /api/v1/public/ 下，匿名 WebSocket 走 hub 的独立连接表，
// 页面在 /s/<slug>、文件在 /f/<slug>/<name>。公开范围 = 分享范围内活动分支上的主会话条目（scope.go），
// 密钥扫描、`.md` 导出、条目分页、WS 补发与图片授权都只经由同一个范围函数，因此「扫过的」与「公开的」恒等。
package share

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// MaxSharesPerThread 是每个线程未撤销分享的上限。
const MaxSharesPerThread = 20

// Threads 是分享所需的线程服务能力（*threads.Service 实现）。
type Threads interface {
	// Publish 取线程锁后推一次 index.patch（Thread.publicShares 随之变化）。
	Publish(ctx context.Context, threadID string)
	// Blob 返回 blob 元数据与文件路径。
	Blob(ctx context.Context, sha string) (db.Blob, string, error)
}

// Hub 是匿名 WebSocket 的宿主（*realtime.Hub 实现）。
type Hub interface {
	ServePublic(w http.ResponseWriter, r *http.Request, sub realtime.PublicSub, after int64, rev *int64)
	ClosePublic(shareID string)
}

// Attachments 是附件服务（*attachments.Service 实现）。
type Attachments interface {
	Get(ctx context.Context, id string) (db.Attachment, error)
	Open(ctx context.Context, id string) (*os.File, db.Attachment, error)
	Upload(ctx context.Context, name, threadID string, r io.Reader) (db.Attachment, error)
}

// OrbFiles 是经 orb link 访问 orbd 本机 API 的通道（*orbs.Service 实现）。
type OrbFiles interface {
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
	EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error
}

// Secrets 提供密钥库全部明文（*secrets.Vault 实现），用于 vault_secret 规则。
type Secrets interface {
	All(ctx context.Context) (map[string]string, error)
}

// Config 是分享服务参数。
type Config struct {
	// PublicURL 是控制面公开地址（分享与文件 URL 的前缀）。
	PublicURL string
	// Now 默认 time.Now。
	Now func() time.Time
}

// Service 是公开分享服务。
type Service struct {
	cfg  Config
	st   *store.Store
	th   Threads
	hub  Hub
	att  Attachments
	orbs OrbFiles
	sec  Secrets
}

// New 创建分享服务。
func New(cfg Config, st *store.Store, th Threads, hub Hub, att Attachments, orbs OrbFiles, sec Secrets) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	return &Service{cfg: cfg, st: st, th: th, hub: hub, att: att, orbs: orbs, sec: sec}
}

// Error 是分享服务返回给 API 的业务错误（Status 为 HTTP 状态码）。
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

// ErrNotFound 表示分享 / 线程 / 文件不存在或已撤销（匿名面一律以它回应，不区分原因）。
var ErrNotFound = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "not found"}

// ErrSecretsFound 是 Create 在扫描有命中且未确认时返回的错误（errors.Is 判定；具体命中见 *ScanError）。
var ErrSecretsFound = errors.New("share: secrets found")

// ScanError 携带创建分享时的扫描结果。
type ScanError struct{ Scan apigen.ShareScan }

func (e *ScanError) Error() string {
	return fmt.Sprintf("share: %d secret-like findings", len(e.Scan.Findings))
}

// Is 让 errors.Is(err, ErrSecretsFound) 成立。
func (e *ScanError) Is(target error) bool { return target == ErrSecretsFound }

var slugRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidSlug 报告 s 是否为 slug 形态（32 位小写十六进制）。
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// newSlug 生成 16 字节 crypto/rand 的小写十六进制 slug。
func newSlug() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (s *Service) now() int64 { return s.cfg.Now().UnixMilli() }

func (s *Service) shareURL(slug string) string { return s.cfg.PublicURL + "/s/" + slug }

func (s *Service) getThread(ctx context.Context, id string) (db.Thread, error) {
	t, err := s.st.Q.GetThread(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Thread{}, ErrNotFound
	}
	return t, err
}

// activeBySlug 返回未撤销的分享；slug 形态不对、不存在或已撤销一律 ErrNotFound。
func (s *Service) activeBySlug(ctx context.Context, slug string) (db.ThreadShare, error) {
	if !ValidSlug(slug) {
		return db.ThreadShare{}, ErrNotFound
	}
	sh, err := s.st.Q.GetActiveThreadShareBySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return db.ThreadShare{}, ErrNotFound
	}
	return sh, err
}

func (s *Service) shareAPI(sh db.ThreadShare, title string) apigen.ThreadShare {
	return apigen.ThreadShare{
		ID: sh.ID, ThreadID: sh.ThreadID, ThreadTitle: title, Mode: apigen.ShareMode(sh.Mode), Slug: sh.Slug,
		URL: s.shareURL(sh.Slug), CreatedAt: sh.CreatedAt, Findings: int(sh.Findings), UntilSeq: sh.UntilSeq,
	}
}

// Create 为线程创建公开分享：先以创建时刻的范围扫描密钥，有命中且 ack=false 时返回 *ScanError（ErrSecretsFound）。
// 快照写入 until_seq / leaf_entry_id（= 创建时线程的 last_seq 与叶子）；实时两者为 NULL。
func (s *Service) Create(ctx context.Context, threadID string, mode apigen.ShareMode, ack bool) (apigen.ThreadShare, error) {
	if mode != apigen.ShareModeLive && mode != apigen.ShareModeSnapshot {
		return apigen.ThreadShare{}, errf(http.StatusBadRequest, "invalid", "mode must be live or snapshot")
	}
	th, err := s.getThread(ctx, threadID)
	if err != nil {
		return apigen.ThreadShare{}, err
	}
	n, err := s.st.Q.CountActiveThreadShares(ctx, threadID)
	if err != nil {
		return apigen.ThreadShare{}, err
	}
	if n >= MaxSharesPerThread {
		return apigen.ThreadShare{}, errf(http.StatusConflict, "invalid", "a thread can have at most %d active shares", MaxSharesPerThread)
	}
	until := th.LastSeq
	row := db.InsertThreadShareParams{ID: ids.New("shr"), ThreadID: threadID, Slug: newSlug(), Mode: string(mode), CreatedAt: s.now()}
	if mode == apigen.ShareModeSnapshot {
		row.UntilSeq, row.LeafEntryID = &until, th.LeafEntryID
	}
	// 扫描的范围与快照即将冻结的范围相同（实时分享同样以此刻为界扫描）
	sc, err := s.scopeFor(ctx, db.ThreadShare{ThreadID: threadID, Mode: string(apigen.ShareModeSnapshot), UntilSeq: &until, LeafEntryID: th.LeafEntryID}, th)
	if err != nil {
		return apigen.ThreadShare{}, err
	}
	scan, err := s.scan(ctx, sc)
	if err != nil {
		return apigen.ThreadShare{}, err
	}
	if len(scan.Findings) > 0 && !ack {
		return apigen.ThreadShare{}, &ScanError{Scan: scan}
	}
	row.Findings = int64(len(scan.Findings))
	if err := s.st.Q.InsertThreadShare(ctx, row); err != nil {
		return apigen.ThreadShare{}, err
	}
	s.th.Publish(ctx, threadID)
	return s.shareAPI(db.ThreadShare{
		ID: row.ID, ThreadID: row.ThreadID, Slug: row.Slug, Mode: row.Mode, UntilSeq: row.UntilSeq,
		LeafEntryID: row.LeafEntryID, Findings: row.Findings, CreatedAt: row.CreatedAt,
	}, th.Title), nil
}

// Revoke 撤销分享：已连接的匿名 WS 收到 share.revoked 后断开，此后页面、接口、图片一律 404。
func (s *Service) Revoke(ctx context.Context, id string) error {
	sh, err := s.st.Q.GetThreadShare(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	n, err := s.st.Q.RevokeThreadShare(ctx, db.RevokeThreadShareParams{Now: s.now(), ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.hub.ClosePublic(id)
	s.th.Publish(ctx, sh.ThreadID)
	return nil
}

// List 返回未撤销的分享（新的在前）；threadID 为空时返回全部。
func (s *Service) List(ctx context.Context, threadID string) ([]apigen.ThreadShare, error) {
	var rows []db.ThreadShare
	var err error
	if threadID != "" {
		if _, err := s.getThread(ctx, threadID); err != nil {
			return nil, err
		}
		rows, err = s.st.Q.ListActiveThreadSharesByThread(ctx, threadID)
	} else {
		rows, err = s.st.Q.ListActiveThreadShares(ctx)
	}
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	out := make([]apigen.ThreadShare, 0, len(rows))
	for _, r := range rows {
		title, ok := titles[r.ThreadID]
		if !ok {
			if t, err := s.st.Q.GetThread(ctx, r.ThreadID); err == nil {
				title = t.Title
			}
			titles[r.ThreadID] = title
		}
		out = append(out, s.shareAPI(r, title))
	}
	return out, nil
}

// Overview 对应 `GET shares`：全部未撤销的线程分享与已发布文件。
func (s *Service) Overview(ctx context.Context) (apigen.ShareOverview, error) {
	shares, err := s.List(ctx, "")
	if err != nil {
		return apigen.ShareOverview{}, err
	}
	rows, err := s.st.Q.ListActivePublishedFiles(ctx)
	if err != nil {
		return apigen.ShareOverview{}, err
	}
	files := make([]apigen.PublishedFile, 0, len(rows))
	for _, r := range rows {
		files = append(files, s.fileAPI(r.PublishedFile, r.Attachment))
	}
	return apigen.ShareOverview{Shares: shares, Files: files}, nil
}
