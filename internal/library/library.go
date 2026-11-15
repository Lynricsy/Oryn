// Package library 是全局助手的文件库（M13 计划 4.1）：文件按 id 存在 <data_dir>/assistant-files/<id>，
// 行在 assistant_files 表。与消息附件（internal/attachments）分开存放、分开回收；需要随消息投递到 orb 时，
// CopyToAttachment 复制出一个绑定到目标线程的新附件，两边此后互不影响。
//
// 线程是硬删除：所属对话被删后行的 thread_id 经外键置 NULL，文件仍留在库里。
// 上传了但还没随消息发送（bound=0）的文件在创建 24h 后由 GC 回收，其余文件永不自动回收；没有总量上限。
package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 可见范围。
const (
	// ScopeConversation 只对所属对话可见。
	ScopeConversation = "conversation"
	// ScopeShared 对所有助手对话可见。
	ScopeShared = "shared"
)

// 来源。
const (
	// SourceUpload 是用户在助手对话里上传的文件。
	SourceUpload = "upload"
	// SourceAssistant 是助手用宿主工具存进来的文件。
	SourceAssistant = "assistant"
	// SourceOrb 是 orb 内 `oryn library put` 存进来的文件。
	SourceOrb = "orb"
)

const (
	// unboundTTL：创建超过该时长仍未随消息发送的文件由 GC 回收。
	unboundTTL = 24 * time.Hour
	// gcInterval 是 Run 的清理周期。
	gcInterval = time.Hour
	// orphanGrace：没有对应行的文件（含上传临时文件）只在早于该时长时删除，避开“先 rename 再插行”的窗口。
	orphanGrace = time.Hour
)

var (
	// ErrNotFound 表示文件不存在。
	ErrNotFound = errors.New("library: file not found")
	// ErrTooLarge 表示单个文件超过 attachments.MaxBytes。
	ErrTooLarge = fmt.Errorf("library: file exceeds %d bytes", attachments.MaxBytes)
	// ErrForbidden 表示文件属于别的对话且未共享。
	ErrForbidden = errors.New("library: file belongs to another conversation")
	// ErrThreadNotFound 表示指定的线程不存在。
	ErrThreadNotFound = errors.New("library: thread not found")
	// ErrInvalid 表示参数不合法（空文件名、未知 scope / source 等）。
	ErrInvalid = errors.New("library: invalid request")
)

// File 是文件库里的一个文件。ThreadID、SourceThreadID 为空表示没有。
type File struct {
	ID, Name, Mime string
	Size           int64
	Sha256         string
	Scope          string
	ThreadID       string
	Source         string
	SourceThreadID string
	Bound          bool
	CreatedAt      time.Time
}

// PutParams 是 Put 的参数。Scope 为空按 conversation；ThreadID 为空表示不属于任何对话。
type PutParams struct {
	Name, ThreadID, Scope, Source, SourceThreadID string
	R                                             io.Reader
}

// Filter 是 List 的过滤条件，零值列出全部已绑定文件。
type Filter struct {
	// ThreadID 非空：只列该对话可见的文件（属于该对话的与共享的）。
	ThreadID string
	// Scope 非空：只列该 scope。
	Scope string
	// IncludeUnbound：同时列出还没随消息发送的文件。
	IncludeUnbound bool
}

// Service 是文件库服务。
type Service struct {
	st  *store.Store
	dir string
	att *attachments.Service
	now func() time.Time
}

// New 创建文件库服务（确保存储目录存在）；att 供 CopyToAttachment 创建附件。
func New(st *store.Store, dataDir string, att *attachments.Service) (*Service, error) {
	dir := filepath.Join(dataDir, "assistant-files")
	if err := os.MkdirAll(filepath.Join(dir, ".tmp"), 0o700); err != nil {
		return nil, err
	}
	return &Service{st: st, dir: dir, att: att, now: time.Now}, nil
}

var idRe = regexp.MustCompile(`^afl_[0-9A-Z]{26}$`)

// ValidID 报告 id 是否为文件库 id 形态。
func ValidID(id string) bool { return idRe.MatchString(id) }

func (s *Service) path(id string) string { return filepath.Join(s.dir, id) }

func fromRow(r db.AssistantFile) File {
	return File{
		ID: r.ID, Name: r.Name, Mime: r.Mime, Size: r.Size, Sha256: r.Sha256, Scope: r.Scope,
		ThreadID: deref(r.ThreadID), Source: r.Source, SourceThreadID: deref(r.SourceThreadID),
		Bound: r.Bound != 0, CreatedAt: time.UnixMilli(r.CreatedAt),
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func validScope(scope string) bool { return scope == ScopeConversation || scope == ScopeShared }

// Upload 流式存入用户在助手对话里上传的文件：scope=conversation、未绑定，threadID 可为空（新对话发送前上传）。
func (s *Service) Upload(ctx context.Context, name, threadID string, r io.Reader) (File, error) {
	return s.create(ctx, db.InsertAssistantFileParams{
		Name: name, Scope: ScopeConversation, ThreadID: nullable(threadID), Source: SourceUpload,
	}, r)
}

// Put 存入助手或 orb 产出的文件，直接视为已绑定（永不自动回收）。
func (s *Service) Put(ctx context.Context, p PutParams) (File, error) {
	if p.Scope == "" {
		p.Scope = ScopeConversation
	}
	if !validScope(p.Scope) {
		return File{}, fmt.Errorf("%w: unknown scope %q", ErrInvalid, p.Scope)
	}
	switch p.Source {
	case SourceUpload, SourceAssistant, SourceOrb:
	default:
		return File{}, fmt.Errorf("%w: unknown source %q", ErrInvalid, p.Source)
	}
	return s.create(ctx, db.InsertAssistantFileParams{
		Name: p.Name, Scope: p.Scope, ThreadID: nullable(p.ThreadID), Source: p.Source,
		SourceThreadID: nullable(p.SourceThreadID), Bound: 1,
	}, p.R)
}

// create 把 r 流式写入临时文件（sha256、MIME 嗅探同步完成），rename 到位后插行；row 的 id、名字、
// 类型、大小、摘要与创建时间在这里填。
func (s *Service) create(ctx context.Context, row db.InsertAssistantFileParams, r io.Reader) (File, error) {
	if strings.TrimSpace(row.Name) == "" {
		return File{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if row.ThreadID != nil {
		if err := s.checkThread(ctx, s.st.Q, *row.ThreadID); err != nil {
			return File{}, err
		}
	}
	row.Name = attachments.SafeName(row.Name)
	tmp, err := os.CreateTemp(filepath.Join(s.dir, ".tmp"), "up-*")
	if err != nil {
		return File{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	var head headBuf
	n, err := io.Copy(io.MultiWriter(tmp, h, &head), io.LimitReader(r, attachments.MaxBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return File{}, ErrTooLarge
		}
		return File{}, err
	}
	if n > attachments.MaxBytes {
		return File{}, ErrTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return File{}, err
	}
	if err := tmp.Close(); err != nil {
		return File{}, err
	}
	row.ID = ids.AssistantFile()
	row.Mime = attachments.SniffMime(head.b, row.Name)
	row.Size = n
	row.Sha256 = hex.EncodeToString(h.Sum(nil))
	row.CreatedAt = s.now().UnixMilli()
	if err := os.Rename(tmp.Name(), s.path(row.ID)); err != nil {
		return File{}, err
	}
	keep = true
	if err := s.st.Q.InsertAssistantFile(ctx, row); err != nil {
		s.removeFile(row.ID)
		return File{}, err
	}
	return fromRow(db.AssistantFile(row)), nil
}

// headBuf 保留写入的前 512 字节（MIME 嗅探用）。
type headBuf struct{ b []byte }

func (h *headBuf) Write(p []byte) (int, error) {
	if room := 512 - len(h.b); room > 0 {
		h.b = append(h.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (s *Service) checkThread(ctx context.Context, q *db.Queries, threadID string) error {
	if _, err := q.GetThread(ctx, threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrThreadNotFound
		}
		return err
	}
	return nil
}

// Get 返回文件。
func (s *Service) Get(ctx context.Context, id string) (File, error) {
	if !ValidID(id) {
		return File{}, ErrNotFound
	}
	r, err := s.st.Q.GetAssistantFile(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, err
	}
	return fromRow(r), nil
}

// Open 打开文件内容（可 seek，供 Range 下载）；调用方负责关闭。
func (s *Service) Open(ctx context.Context, id string) (*os.File, File, error) {
	f, err := s.Get(ctx, id)
	if err != nil {
		return nil, f, err
	}
	rc, err := os.Open(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, f, ErrNotFound
	}
	if err != nil {
		return nil, f, err
	}
	return rc, f, nil
}

// List 按创建时间倒序列出文件。
func (s *Service) List(ctx context.Context, f Filter) ([]File, error) {
	if f.Scope != "" && !validScope(f.Scope) {
		return nil, fmt.Errorf("%w: unknown scope %q", ErrInvalid, f.Scope)
	}
	p := db.ListAssistantFilesParams{ThreadID: f.ThreadID, Scope: f.Scope}
	if f.IncludeUnbound {
		p.IncludeUnbound = 1
	}
	rows, err := s.st.Q.ListAssistantFiles(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]File, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

// SetScope 切换文件的可见范围（conversation / shared）。
func (s *Service) SetScope(ctx context.Context, id, scope string) error {
	if !validScope(scope) {
		return fmt.Errorf("%w: unknown scope %q", ErrInvalid, scope)
	}
	if !ValidID(id) {
		return ErrNotFound
	}
	n, err := s.st.Q.SetAssistantFileScope(ctx, db.SetAssistantFileScopeParams{Scope: scope, ID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Bind 在一个事务里把文件绑定到对话（随消息发送）：标记为已绑定，thread_id 为空时填上 threadID。
// 任一文件不存在 → ErrNotFound；属于别的对话且未共享 → ErrForbidden；出错时全部不绑定。
func (s *Service) Bind(ctx context.Context, threadID string, list []string) error {
	return s.st.Tx(ctx, func(q *db.Queries) error {
		_, err := s.BindTx(ctx, q, threadID, list)
		return err
	})
}

// BindTx 是 Bind 的事务内版本（在调用方的事务里执行，例如新建助手对话时与线程行同一事务），
// 按 list 的顺序返回绑定后的文件。
func (s *Service) BindTx(ctx context.Context, q *db.Queries, threadID string, list []string) ([]File, error) {
	if threadID == "" {
		return nil, fmt.Errorf("%w: thread id is required", ErrInvalid)
	}
	if err := s.checkThread(ctx, q, threadID); err != nil {
		return nil, err
	}
	out := make([]File, 0, len(list))
	for _, id := range list {
		if !ValidID(id) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		n, err := q.BindAssistantFile(ctx, db.BindAssistantFileParams{ThreadID: threadID, ID: id})
		if err != nil {
			return nil, err
		}
		r, err := q.GetAssistantFile(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		} else if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("%w: %s", ErrForbidden, id)
		}
		out = append(out, fromRow(r))
	}
	return out, nil
}

// SandboxPath 返回文件在协调宿主沙箱里的路径（助手对话消息里的附件块用它；同步字节时按同一规则落盘）：
// 共享文件为 ~/library/shared/<id>/<name>，对话文件为 ~/library/threads/<threadId>/<id>/<name>。
func SandboxPath(f File) string {
	if f.Scope == ScopeShared {
		return "~/library/shared/" + f.ID + "/" + f.Name
	}
	return "~/library/threads/" + f.ThreadID + "/" + f.ID + "/" + f.Name
}

// Delete 删除文件的行与字节；由它复制出的附件不受影响。
func (s *Service) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrNotFound
	}
	n, err := s.st.Q.DeleteAssistantFile(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.removeFile(id)
	return nil
}

// CopyToAttachment 把文件复制成一个绑定到 threadID 的新附件（att_ 行与独立的字节），返回附件 id。
// 是否允许调用方引用该文件（所属对话 / 共享）由调用方判断。
func (s *Service) CopyToAttachment(ctx context.Context, id, threadID string) (string, error) {
	if threadID == "" {
		return "", fmt.Errorf("%w: thread id is required", ErrInvalid)
	}
	rc, f, err := s.Open(ctx, id)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	a, err := s.att.Upload(ctx, f.Name, threadID, rc)
	if err != nil {
		var ae *attachments.Error
		if errors.As(err, &ae) && ae.Code == attachments.CodeThreadNotFound {
			return "", ErrThreadNotFound
		}
		return "", err
	}
	return a.ID, nil
}

// Run 每小时清理一次（直到 ctx 结束）。
func (s *Service) Run(ctx context.Context) {
	s.GC(ctx, s.now())
	tick := time.NewTicker(gcInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.GC(ctx, s.now())
		}
	}
}

// GC 执行一轮清理：创建早于 now-24h 仍未绑定的文件、没有对应行的文件与上传残留的临时文件（均早于 now-1h）。
func (s *Service) GC(ctx context.Context, now time.Time) {
	stale, err := s.st.Q.ListUnboundAssistantFilesBefore(ctx, now.Add(-unboundTTL).UnixMilli())
	if err != nil {
		slog.Warn("library: 列出未绑定文件失败", "err", err)
		return
	}
	for _, f := range stale {
		// 只删仍未绑定的行：列出之后被 Bind 的文件不能删
		n, err := s.st.Q.DeleteUnboundAssistantFile(ctx, f.ID)
		if err != nil {
			slog.Warn("library: 删除未绑定文件失败", "id", f.ID, "err", err)
			continue
		}
		if n > 0 {
			s.removeFile(f.ID)
		}
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-orphanGrace)
	for _, e := range entries {
		name := e.Name()
		if name == ".tmp" {
			s.sweepTmp(cutoff)
			continue
		}
		if !ValidID(name) {
			continue
		}
		if _, err := s.st.Q.GetAssistantFile(ctx, name); errors.Is(err, sql.ErrNoRows) {
			if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
				s.removeFile(name)
			}
		}
	}
}

// sweepTmp 删除早于 cutoff 的上传临时文件（进程在上传中途退出的残留）。
func (s *Service) sweepTmp(cutoff time.Time) {
	dir := filepath.Join(s.dir, ".tmp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func (s *Service) removeFile(id string) {
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("library: 删除文件失败", "id", id, "err", err)
	}
}
