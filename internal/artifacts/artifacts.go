// Package artifacts 是 M14 的 Artifacts：agent 产出的版本化交付物（页面、画布、文档、幻灯片）。
//
// 一个 artifact 是一组带版本的文件。每次写入产生一个新版本（完整的文件清单，不是增量），文件内容按 sha256
// 寻址存在 <data_dir>/artifacts/content/ 下、同内容只存一份；行在 artifacts / artifact_versions / artifact_files。
// 并发按文件粒度做乐观检查：写入带 baseVersion，只有本次要写的文件在 baseVersion 之后被别人改过才冲突。
// 删除是软删除（「最近删除」保留 KeepDeleted），到期由 Run 清理。
package artifacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 类型（kind）。
const (
	KindWeb      = "web"
	KindCanvas   = "canvas"
	KindDocument = "document"
	KindSlides   = "slides"
)

// 来源（origin）。
const (
	OriginAgent  = "agent"
	OriginImport = "import"
)

// 入口文件。
const (
	EntryHTML   = "index.html"
	EntryCanvas = "canvas.json"
)

// 错误码（agent 工具与 REST 共用）。
const (
	CodeNotFound          = "not_found"
	CodeDeleted           = "deleted"
	CodeConflict          = "conflict"
	CodeEditNotFound      = "edit_not_found"
	CodeTooLarge          = "too_large"
	CodeInvalid           = "invalid"
	CodeRateLimited       = "rate_limited"
	CodeForbidden         = "forbidden"
	CodeRenderUnavailable = "render_unavailable"
)

// State 是冲突时返回给调用方的当前状态。
type State struct {
	Version int
	Deleted bool
	// Changed 是在调用方的 baseVersion 之后被改过的路径。
	Changed []string
}

// OpError 是带错误码的操作错误；Message 是给 agent / 用户看的中文说明，并告诉对方下一步该做什么。
type OpError struct {
	Code, Message string
	State         *State
}

func (e *OpError) Error() string { return e.Code + ": " + e.Message }

// Status 返回错误码对应的 HTTP 状态。
func (e *OpError) Status() int {
	switch e.Code {
	case CodeNotFound:
		return http.StatusNotFound
	case CodeDeleted:
		return http.StatusGone
	case CodeConflict:
		return http.StatusConflict
	case CodeEditNotFound:
		return http.StatusUnprocessableEntity
	case CodeTooLarge, CodeTooMany:
		return http.StatusRequestEntityTooLarge
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeForbidden:
		return http.StatusForbidden
	case CodeRenderUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

func opErr(code, format string, args ...any) *OpError {
	return &OpError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsOpError 取出 err 链上的 *OpError。
func AsOpError(err error) (*OpError, bool) {
	var oe *OpError
	ok := errors.As(err, &oe)
	return oe, ok
}

// Hub 是实时通知的出口（realtime.Hub 实现）。
type Hub interface {
	// ArtifactsChanged 通知全部连接：artifact 列表该重取。
	ArtifactsChanged()
	// ArtifactVersion 通知正在观看该 artifact 的连接：有了新版本。
	ArtifactVersion(id string, version int)
	// ArtifactComments 通知正在观看该 artifact 的连接：评论有变化。
	ArtifactComments(id string)
}

// Config 是服务配置。Now 为空时用 time.Now。
type Config struct {
	DataDir, PublicURL string
	Now                func() time.Time
}

// Artifact 是一个 artifact（取最新版本的大小与页数）。可选的字符串为空、时间为零值表示没有。
type Artifact struct {
	ID, ProjectID, ThreadID string
	Origin, Type, Kind      string
	Title                   string
	Version                 int
	Size                    int64
	Pages                   int
	CreatedAt, UpdatedAt    time.Time
	OpenedAt, DeletedAt     time.Time
	// Published 是发布的访问级别；未发布（或调用方没有取）时为空。只有 Get / List 填。
	Published string
}

// Deleted 报告是否在「最近删除」里。
func (a Artifact) Deleted() bool { return !a.DeletedAt.IsZero() }

// Entry 返回入口文件路径（plain 的画布为 canvas.json，网页为 index.html；类型化的见 TypeEntry）。
func (a Artifact) Entry() string {
	if e := TypeEntry(a.Type); e != "" {
		return e
	}
	if a.Kind == KindCanvas {
		return EntryCanvas
	}
	return EntryHTML
}

// API 返回 REST 表示。
func (a Artifact) API() apigen.Artifact {
	out := apigen.Artifact{
		ID: a.ID, Origin: apigen.ArtifactOrigin(a.Origin), Kind: apigen.ArtifactKind(a.Kind), Title: a.Title,
		Version: a.Version, Size: a.Size, Pages: a.Pages,
		CreatedAt: a.CreatedAt.UnixMilli(), UpdatedAt: a.UpdatedAt.UnixMilli(),
	}
	if a.ProjectID != "" {
		out.ProjectID = &a.ProjectID
	}
	if a.ThreadID != "" {
		out.ThreadID = &a.ThreadID
	}
	if a.Type != "" {
		t := apigen.ArtifactType(a.Type)
		out.Type = &t
	}
	if !a.OpenedAt.IsZero() {
		v := a.OpenedAt.UnixMilli()
		out.OpenedAt = &v
	}
	if !a.DeletedAt.IsZero() {
		v := a.DeletedAt.UnixMilli()
		out.DeletedAt = &v
	}
	if a.Published != "" {
		out.Published = &struct {
			Access apigen.ArtifactAccess `json:"access"`
		}{Access: apigen.ArtifactAccess(a.Published)}
	}
	return out
}

// Version 是一个版本。AuthorThreadID 为空表示用户。
type Version struct {
	Version        int
	CreatedAt      time.Time
	AuthorThreadID string
	AuthorLabel    string
	Note           string
	Size           int64
	Pages          int
	Capabilities   []string
	Format         int
	FilesKey       string
}

// API 返回 REST 表示。
func (v Version) API() apigen.ArtifactVersion {
	out := apigen.ArtifactVersion{
		Version: v.Version, CreatedAt: v.CreatedAt.UnixMilli(), AuthorLabel: v.AuthorLabel, Size: v.Size, Pages: v.Pages,
	}
	if v.AuthorThreadID != "" {
		out.AuthorThreadID = &v.AuthorThreadID
	}
	if v.Note != "" {
		out.Note = &v.Note
	}
	return out
}

// File 是某个版本里的一个文件。
type File struct {
	Path, Sha256, Mime string
	Size               int64
}

// API 返回 REST 表示。
func (f File) API() apigen.ArtifactFile {
	return apigen.ArtifactFile{Path: f.Path, Sha256: f.Sha256, Mime: f.Mime, Size: f.Size}
}

// Filter 是 List 的过滤条件；零值列出全部未删除的。
type Filter struct {
	// Deleted 为真时只列「最近删除」里的。
	Deleted bool
	// ThreadID 非空：该线程创建或写过版本的。
	ThreadID string
	// ProjectID 非空：该项目的。
	ProjectID string
}

// Service 是 artifact 服务。
type Service struct {
	cfg   Config
	st    *store.Store
	hub   Hub
	c     *contents
	now   func() time.Time
	locks keyLocks
	lim   *limiter
	// contentMu：写入方从落内容到提交引用行持读锁（holdContent），内容清理逐个候选持写锁，
	// 防止清理删掉「刚被复用、引用行还没提交」的旧孤儿内容。
	contentMu sync.RWMutex
	// 只给测试用的交错钩子；生产为 nil。
	afterStage   func()
	gcBeforeLock func(sha string)
	// pub 是发布配置（ConfigurePublishing）；nil = 发布未配置。
	pub *PublishConfig
}

// holdContent 取内容存储的读锁并返回释放函数。凡是先把内容落进存储、再在事务里插引用行的写入路径，
// 都必须从落内容之前一直持有到事务提交之后。
func (s *Service) holdContent() func() {
	s.contentMu.RLock()
	return s.contentMu.RUnlock
}

// New 创建服务（确保 <data_dir>/artifacts 存在）。hub 可为 nil（不发实时通知）。
func New(cfg Config, st *store.Store, hub Hub) (*Service, error) {
	c, err := newContents(filepath.Join(cfg.DataDir, "artifacts"))
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{cfg: cfg, st: st, hub: hub, c: c, now: now, lim: newLimiter()}, nil
}

// PublicURL 返回控制面的公开地址（不带结尾斜杠）。
func (s *Service) PublicURL() string { return strings.TrimRight(s.cfg.PublicURL, "/") }

func (s *Service) changed() {
	if s.hub != nil {
		s.hub.ArtifactsChanged()
	}
}

func (s *Service) versioned(id string, n int) {
	if s.hub != nil {
		s.hub.ArtifactVersion(id, n)
		s.hub.ArtifactsChanged()
	}
}

var idRe = regexp.MustCompile(`^art_[0-9A-Z]{26}$`)

// ValidID 报告 id 是否为 artifact id 形态。
func ValidID(id string) bool { return idRe.MatchString(id) }

func notFound(id string) *OpError {
	return opErr(CodeNotFound, "找不到 artifact %s。用 list_artifacts 查看你能访问的 artifact。", id)
}

func deletedErr(id string) *OpError {
	return &OpError{Code: CodeDeleted, Message: fmt.Sprintf("artifact %s 已被删除（在「最近删除」里）。需要的话请用户先恢复它，或者新建一个。", id), State: &State{Deleted: true}}
}

func fromRow(r db.Artifact) Artifact {
	a := Artifact{
		ID: r.ID, Origin: r.Origin, Kind: r.Kind, Title: r.Title, Version: int(r.Head), Size: r.Size, Pages: int(r.Pages),
		CreatedAt: time.UnixMilli(r.CreatedAt), UpdatedAt: time.UnixMilli(r.UpdatedAt),
	}
	a.ProjectID = deref(r.ProjectID)
	a.ThreadID = deref(r.ThreadID)
	a.Type = deref(r.Type)
	if r.OpenedAt != nil {
		a.OpenedAt = time.UnixMilli(*r.OpenedAt)
	}
	if r.DeletedAt != nil {
		a.DeletedAt = time.UnixMilli(*r.DeletedAt)
	}
	return a
}

func versionFromRow(r db.ArtifactVersion) Version {
	v := Version{
		Version: int(r.N), CreatedAt: time.UnixMilli(r.CreatedAt), AuthorThreadID: deref(r.AuthorThreadID),
		AuthorLabel: r.AuthorLabel, Note: deref(r.Note), Size: r.Size, Pages: int(r.Pages), FilesKey: r.FilesKey,
	}
	_ = json.Unmarshal([]byte(r.Capabilities), &v.Capabilities)
	if r.Format != nil {
		v.Format = int(*r.Format)
	}
	return v
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// getRow 读 artifact 行；不存在 → not_found。
func getRow(ctx context.Context, q *db.Queries, id string) (db.Artifact, error) {
	if !ValidID(id) {
		return db.Artifact{}, notFound(id)
	}
	r, err := q.GetArtifact(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Artifact{}, notFound(id)
	}
	return r, err
}

// Get 返回 artifact（含回收站里的）。
func (s *Service) Get(ctx context.Context, id string) (Artifact, error) {
	r, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return Artifact{}, err
	}
	a := fromRow(r)
	pr, err := s.st.Q.GetArtifactPublication(ctx, id)
	switch {
	case err == nil:
		a.Published = pr.Access
	case !errors.Is(err, sql.ErrNoRows):
		return Artifact{}, err
	}
	return a, nil
}

// List 按最近更新列出 artifact。
func (s *Service) List(ctx context.Context, f Filter) ([]Artifact, error) {
	p := db.ListArtifactsParams{ThreadID: f.ThreadID, ProjectID: f.ProjectID}
	if f.Deleted {
		p.Deleted = 1
	}
	rows, err := s.st.Q.ListArtifacts(ctx, p)
	if err != nil {
		return nil, err
	}
	acc, err := s.st.Q.ListArtifactPublishedAccess(ctx)
	if err != nil {
		return nil, err
	}
	published := make(map[string]string, len(acc))
	for _, x := range acc {
		published[x.ArtifactID] = x.Access
	}
	out := make([]Artifact, 0, len(rows))
	for _, r := range rows {
		a := fromRow(r)
		a.Published = published[r.ID]
		out = append(out, a)
	}
	return out, nil
}

// resolveVersion 把 0 换成 head，并检查版本存在。
func resolveVersion(a db.Artifact, version int) (int64, error) {
	if version == 0 {
		return a.Head, nil
	}
	if version < 1 || int64(version) > a.Head {
		return 0, opErr(CodeNotFound, "artifact %s 没有版本 %d（最新是版本 %d）。", a.ID, version, a.Head)
	}
	return int64(version), nil
}

// Version 返回一个版本（version 0 = 最新）。
func (s *Service) Version(ctx context.Context, id string, version int) (Version, error) {
	a, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return Version{}, err
	}
	n, err := resolveVersion(a, version)
	if err != nil {
		return Version{}, err
	}
	r, err := s.st.Q.GetArtifactVersion(ctx, db.GetArtifactVersionParams{ArtifactID: id, N: n})
	if err != nil {
		return Version{}, err
	}
	return versionFromRow(r), nil
}

// Files 返回某个版本的文件清单（按路径排序）；version 0 = 最新。
func (s *Service) Files(ctx context.Context, id string, version int) ([]File, error) {
	a, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return nil, err
	}
	n, err := resolveVersion(a, version)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.Q.ListArtifactFiles(ctx, db.ListArtifactFilesParams{ArtifactID: id, N: n})
	if err != nil {
		return nil, err
	}
	out := make([]File, 0, len(rows))
	for _, r := range rows {
		out = append(out, File{Path: r.Path, Sha256: r.Sha256, Size: r.Size, Mime: MimeOf(r.Path)})
	}
	return out, nil
}

// Open 打开某个版本里的一个文件；调用方负责关闭。
func (s *Service) Open(ctx context.Context, id string, version int, path string) (io.ReadSeekCloser, File, error) {
	a, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return nil, File{}, err
	}
	n, err := resolveVersion(a, version)
	if err != nil {
		return nil, File{}, err
	}
	r, err := s.st.Q.GetArtifactFile(ctx, db.GetArtifactFileParams{ArtifactID: id, N: n, Path: path})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, File{}, opErr(CodeNotFound, "artifact %s 版本 %d 里没有文件 %s。", id, n, path)
	}
	if err != nil {
		return nil, File{}, err
	}
	f, err := s.c.open(r.Sha256)
	if err != nil {
		return nil, File{}, fmt.Errorf("artifacts: 打开内容文件 %s: %w", r.Sha256, err)
	}
	return f, File{Path: r.Path, Sha256: r.Sha256, Size: r.Size, Mime: MimeOf(r.Path)}, nil
}

// ReadFile 读出某个版本里一个文件的全部字节。
func (s *Service) ReadFile(ctx context.Context, id string, version int, path string) ([]byte, File, error) {
	rc, f, err := s.Open(ctx, id, version, path)
	if err != nil {
		return nil, f, err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return b, f, err
}

// Versions 返回版本列表（新的在前）。
func (s *Service) Versions(ctx context.Context, id string) ([]Version, error) {
	if _, err := getRow(ctx, s.st.Q, id); err != nil {
		return nil, err
	}
	rows, err := s.st.Q.ListArtifactVersions(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(rows))
	for _, r := range rows {
		out = append(out, versionFromRow(r))
	}
	return out, nil
}

// AuthorThreads 返回写过版本的线程 id（最近写入的在前）。
func (s *Service) AuthorThreads(ctx context.Context, id string) ([]string, error) {
	rows, err := s.st.Q.ListArtifactAuthorThreads(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.AuthorThreadID != nil {
			out = append(out, *r.AuthorThreadID)
		}
	}
	return out, nil
}

// CleanTitle 校验并整理标题：控制字符换成空格、去掉首尾空白，非空且不超过 MaxTitleRunes。
func CleanTitle(t string) (string, error) {
	t = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, t)
	t = strings.TrimSpace(t)
	if t == "" {
		return "", opErr(CodeInvalid, "标题不能为空。")
	}
	if n := utf8.RuneCountInString(t); n > MaxTitleRunes {
		return "", opErr(CodeInvalid, "标题有 %d 个字符，上限是 %d。请缩短标题。", n, MaxTitleRunes)
	}
	return t, nil
}

// Rename 改标题（不产生版本；回收站里的不能改）。
func (s *Service) Rename(ctx context.Context, id, title string) (Artifact, error) {
	title, err := CleanTitle(title)
	if err != nil {
		return Artifact{}, err
	}
	r, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return Artifact{}, err
	}
	if r.DeletedAt != nil {
		return Artifact{}, deletedErr(id)
	}
	if _, err := s.st.Q.RenameArtifact(ctx, db.RenameArtifactParams{ID: id, Title: title}); err != nil {
		return Artifact{}, err
	}
	s.changed()
	return s.Get(ctx, id)
}

// Delete 把 artifact 移到「最近删除」（已在其中时不变）。
func (s *Service) Delete(ctx context.Context, id string) (Artifact, error) {
	r, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return Artifact{}, err
	}
	if r.DeletedAt == nil {
		now := s.now().UnixMilli()
		if _, err := s.st.Q.SetArtifactDeleted(ctx, db.SetArtifactDeletedParams{ID: id, DeletedAt: &now}); err != nil {
			return Artifact{}, err
		}
		s.changed()
	}
	return s.Get(ctx, id)
}

// Undelete 从「最近删除」恢复。
func (s *Service) Undelete(ctx context.Context, id string) (Artifact, error) {
	r, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return Artifact{}, err
	}
	if r.DeletedAt != nil {
		if _, err := s.st.Q.SetArtifactDeleted(ctx, db.SetArtifactDeletedParams{ID: id, DeletedAt: nil}); err != nil {
			return Artifact{}, err
		}
		s.changed()
	}
	return s.Get(ctx, id)
}

// Erase 永久删除回收站里的 artifact（不在回收站里的回 invalid）。内容文件留给清理回收。
func (s *Service) Erase(ctx context.Context, id string) error {
	r, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return err
	}
	if r.DeletedAt == nil {
		return opErr(CodeInvalid, "只能永久删除「最近删除」里的 artifact。")
	}
	// 资格（仍在回收站里）与删除在同一条语句里判定：与 Undelete 交错时恢复胜出
	n, err := s.st.Q.EraseArtifact(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return opErr(CodeInvalid, "只能永久删除「最近删除」里的 artifact。")
	}
	s.changed()
	return nil
}

// MarkOpened 记一次用户打开。
func (s *Service) MarkOpened(ctx context.Context, id string) error {
	if _, err := getRow(ctx, s.st.Q, id); err != nil {
		return err
	}
	now := s.now().UnixMilli()
	_, err := s.st.Q.SetArtifactOpened(ctx, db.SetArtifactOpenedParams{ID: id, OpenedAt: &now})
	return err
}

// Run 启动时清理一次，之后每 PurgeEvery 清理一次（直到 ctx 结束）。
func (s *Service) Run(ctx context.Context) {
	s.Purge(ctx)
	tick := time.NewTicker(PurgeEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.Purge(ctx)
		}
	}
}

// Purge 执行一轮清理：删掉在回收站里超过 KeepDeleted 的 artifact，再删掉没有任何行引用的内容文件与残留的临时文件。
func (s *Service) Purge(ctx context.Context) {
	now := s.now()
	cutoff := now.Add(-KeepDeleted).UnixMilli()
	stale, err := s.st.Q.ListArtifactsDeletedBefore(ctx, &cutoff)
	if err != nil {
		slog.Warn("artifacts: 列出过期的已删除 artifact 失败", "err", err)
		return
	}
	purged := 0
	for _, a := range stale {
		if s.purgeOne(ctx, a.ID, cutoff) {
			purged++
		}
	}
	if purged > 0 {
		s.changed()
	}
	s.c.gc(ctx, &s.contentMu, func(ctx context.Context, sha string) (bool, error) {
		n, err := s.st.Q.CountArtifactFilesBySHA(ctx, sha)
		return n > 0, err
	}, now.Add(-orphanGrace), s.gcBeforeLock)
}

// purgeOne 删掉一个过期的回收站条目；删除语句里重查 deleted_at < cutoff，列出之后被恢复（或重新删除）的不删。
func (s *Service) purgeOne(ctx context.Context, id string, cutoff int64) bool {
	n, err := s.st.Q.PurgeArtifact(ctx, db.PurgeArtifactParams{ID: id, Ts: &cutoff})
	if err != nil {
		slog.Warn("artifacts: 清理已删除 artifact 失败", "id", id, "err", err)
		return false
	}
	return n > 0
}

// keyLocks 是按 key 的互斥锁（同一 artifact 的写入串行；单进程内足够，orynd 只有一个）。
type keyLocks struct {
	mu sync.Mutex
	m  map[string]*keyLock
}

type keyLock struct {
	mu sync.Mutex
	n  int
}

func (k *keyLocks) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyLock{}
	}
	l := k.m[key]
	if l == nil {
		l = &keyLock{}
		k.m[key] = l
	}
	l.n++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.n--
		if l.n == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// limiter 是按调用线程的每分钟写入计数（内存滑动窗口，控制面重启后清零）。只记成功的写入。
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

func (l *limiter) prune(key string, now time.Time) []time.Time {
	hs := l.hits[key]
	keep := hs[:0]
	for _, h := range hs {
		if now.Sub(h) < time.Minute {
			keep = append(keep, h)
		}
	}
	if len(keep) == 0 {
		delete(l.hits, key)
		return nil
	}
	l.hits[key] = keep
	return keep
}

// allow 报告 key 在最近一分钟内的写入是否还没到 n 次。
func (l *limiter) allow(key string, now time.Time, n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, now)) < n
}

// record 记一次成功的写入。
func (l *limiter) record(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[key] = append(l.prune(key, now), now)
}
