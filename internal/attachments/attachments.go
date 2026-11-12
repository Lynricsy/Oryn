// Package attachments 是控制面的消息附件服务（M3 计划 5.2）：附件按 id 存在 <data_dir>/attachments/<id>，
// 随线程保存、不进仓库；发消息时经 orb link 的 fs 流投递到 orb 的 $HOME/.oryn/attachments/<threadId>/<id>/<name>。
//
// 附件不与转录 blob 共用 blobs/：那里内容寻址、没有引用计数，无法安全地按附件物理删除。
package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

const (
	// MaxBytes 是单个附件的上限（orbd 侧再校验一次）。
	MaxBytes = 100 << 20
	// MaxPerMessage 是每条消息（含新建线程）可带的附件数上限。
	MaxPerMessage = 20
	// maxName 是净化后文件名的字节上限（与 orbd 一致）。
	maxName = 200
	// unboundTTL：创建超过该时长仍未绑定线程的附件由 Run 清理。
	unboundTTL = 24 * time.Hour
	// gcInterval 是清理周期。
	gcInterval = time.Hour
	// bestEffortTimeout 是删除 orb 内投递副本的超时（不唤醒 orb）。
	bestEffortTimeout = 10 * time.Second
)

// OrbHTTP 是附件投递所需的 orb HTTP 通道（由 *orbs.Service 实现）。
type OrbHTTP interface {
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
	EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error
}

// Error 是附件服务返回给 API 的业务错误。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Coded 返回错误码与说明（线程服务据此转成自己的业务错误）。
func (e *Error) Coded() (code, message string) { return e.Code, e.Message }

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// 错误码。
const (
	CodeNotFound        = "not_found"
	CodeTooLarge        = "too_large"
	CodeInvalid         = "invalid_request"
	CodeThreadNotFound  = "thread_not_found"
	CodeAttachNotFound  = "attachment_not_found"
	CodeOrbNotReady     = "orb_not_ready"
	CodeOrbdOutdated    = "orbd_outdated"
	CodeOrbGone         = "orb_deleted"
	CodeOrbdUnreachable = "orbd_unreachable"
)

// ErrNotFound 表示附件不存在。
var ErrNotFound = &Error{Code: CodeNotFound, Message: "attachment not found"}

// Service 是附件服务。
type Service struct {
	st   *store.Store
	dir  string
	orbs OrbHTTP
	now  func() time.Time
}

// New 创建附件服务（确保存储目录存在）。
func New(st *store.Store, dataDir string, orbHTTP OrbHTTP) (*Service, error) {
	dir := filepath.Join(dataDir, "attachments")
	if err := os.MkdirAll(filepath.Join(dir, ".tmp"), 0o700); err != nil {
		return nil, err
	}
	return &Service{st: st, dir: dir, orbs: orbHTTP, now: time.Now}, nil
}

func (s *Service) path(id string) string { return filepath.Join(s.dir, id) }

// SafeName 净化附件文件名，规则与 orbd 的投递路径一致（对已净化的名字是恒等变换）：取基名，
// `\`、控制字符换成 `_`，前导 `.` 前加 `_`，保留扩展名截到 200 字节。
func SafeName(name string) string {
	name = strings.ToValidUTF8(name, "_")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, c := range name {
		if c == '\\' || c < 0x20 || c == 0x7f || (c >= 0x80 && c < 0xa0) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(c)
	}
	name = b.String()
	if name == "" {
		name = "_"
	}
	if strings.HasPrefix(name, ".") {
		name = "_" + name
	}
	if len(name) <= maxName {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) >= maxName/2 {
		ext = ""
	}
	return truncateUTF8(strings.TrimSuffix(name, ext), maxName-len(ext)) + ext
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

var idRe = regexp.MustCompile(`^att_[0-9A-Z]{26}$`)

// ValidID 报告 id 是否为附件 id 形态。
func ValidID(id string) bool { return idRe.MatchString(id) }

// sniffMime 取前 512 字节嗅探类型；嗅探不出（octet-stream）时按扩展名。
func sniffMime(head []byte, name string) string {
	m := http.DetectContentType(head)
	if m == "application/octet-stream" {
		if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExt != "" {
			m = byExt
		}
	}
	return m
}

// Upload 流式写入附件并插入行；超过 MaxBytes → too_large。threadID 非空时直接绑定（线程须存在）。
func (s *Service) Upload(ctx context.Context, name, threadID string, r io.Reader) (db.Attachment, error) {
	if strings.TrimSpace(name) == "" {
		return db.Attachment{}, errf(CodeInvalid, "name is required")
	}
	var tid *string
	if threadID != "" {
		if _, err := s.st.Q.GetThread(ctx, threadID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return db.Attachment{}, errf(CodeThreadNotFound, "thread not found")
			}
			return db.Attachment{}, err
		}
		tid = &threadID
	}
	name = SafeName(name)
	tmp, err := os.CreateTemp(filepath.Join(s.dir, ".tmp"), "up-*")
	if err != nil {
		return db.Attachment{}, err
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
	n, err := io.Copy(io.MultiWriter(tmp, h, &head), io.LimitReader(r, MaxBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return db.Attachment{}, errf(CodeTooLarge, "attachment exceeds %d bytes", MaxBytes)
		}
		return db.Attachment{}, err
	}
	if n > MaxBytes {
		return db.Attachment{}, errf(CodeTooLarge, "attachment exceeds %d bytes", MaxBytes)
	}
	if err := tmp.Sync(); err != nil {
		return db.Attachment{}, err
	}
	if err := tmp.Close(); err != nil {
		return db.Attachment{}, err
	}
	row := db.Attachment{
		ID: ids.New("att"), ThreadID: tid, Name: name, Mime: sniffMime(head.b, name),
		Size: n, Sha256: hex.EncodeToString(h.Sum(nil)), CreatedAt: s.now().UnixMilli(),
	}
	if err := os.Rename(tmp.Name(), s.path(row.ID)); err != nil {
		return db.Attachment{}, err
	}
	keep = true
	if err := s.st.Q.InsertAttachment(ctx, db.InsertAttachmentParams(row)); err != nil {
		_ = os.Remove(s.path(row.ID))
		return db.Attachment{}, err
	}
	return row, nil
}

// headBuf 保留写入的前 512 字节（MIME 嗅探用）。
type headBuf struct{ b []byte }

func (h *headBuf) Write(p []byte) (int, error) {
	if room := 512 - len(h.b); room > 0 {
		h.b = append(h.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// Get 返回附件行。
func (s *Service) Get(ctx context.Context, id string) (db.Attachment, error) {
	if !ValidID(id) {
		return db.Attachment{}, ErrNotFound
	}
	a, err := s.st.Q.GetAttachment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Attachment{}, ErrNotFound
	}
	return a, err
}

// Open 打开附件内容；调用方负责关闭文件。
func (s *Service) Open(ctx context.Context, id string) (*os.File, db.Attachment, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, a, err
	}
	f, err := os.Open(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, a, ErrNotFound
	}
	return f, a, err
}

// List 返回线程的附件（按上传时间）；线程不存在 → thread_not_found。
func (s *Service) List(ctx context.Context, threadID string) ([]db.Attachment, error) {
	if _, err := s.st.Q.GetThread(ctx, threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errf(CodeThreadNotFound, "thread not found")
		}
		return nil, err
	}
	return s.st.Q.ListAttachmentsByThread(ctx, &threadID)
}

// Delete 删除附件的行与文件；已绑定且所在 orb 可用时尽力删除 orb 内的投递副本（不唤醒）。
func (s *Service) Delete(ctx context.Context, id string) error {
	a, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	n, err := s.st.Q.DeleteAttachment(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.removeFile(id)
	if a.ThreadID != nil {
		if orbID := s.threadOrb(ctx, *a.ThreadID); orbID != "" {
			s.deleteInOrb(ctx, orbID, url.Values{"threadId": {*a.ThreadID}, "id": {id}})
		}
	}
	return nil
}

// DeleteThread 删除线程的全部附件（线程行删除之前调用；行随线程级联删除，这里先删文件）。
func (s *Service) DeleteThread(ctx context.Context, threadID string) error {
	list, err := s.st.Q.ListAttachmentsByThread(ctx, &threadID)
	if err != nil {
		return err
	}
	for _, a := range list {
		if _, err := s.st.Q.DeleteAttachment(ctx, a.ID); err != nil {
			return err
		}
		s.removeFile(a.ID)
	}
	if orbID := s.threadOrb(ctx, threadID); orbID != "" && len(list) > 0 {
		s.deleteInOrb(ctx, orbID, url.Values{"threadId": {threadID}})
	}
	return nil
}

func (s *Service) removeFile(id string) {
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("attachments: 删除附件文件失败", "id", id, "err", err)
	}
}

func (s *Service) threadOrb(ctx context.Context, threadID string) string {
	t, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil || t.OrbID == nil {
		return ""
	}
	return *t.OrbID
}

// deleteInOrb 尽力删除 orb 内的投递副本：orb 不可用（冻结、旧 orbd、已删除）时直接放弃。
func (s *Service) deleteInOrb(ctx context.Context, orbID string, q url.Values) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bestEffortTimeout)
	defer cancel()
	if s.orbs.EnsureUsable(ctx, orbID, orbs.FeatureAttachments, false, 0) != nil {
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, "/attachments?"+q.Encode(), nil)
	resp, err := s.orbs.Do(ctx, orbID, req)
	if err != nil {
		slog.Debug("attachments: 删除 orb 内副本失败", "orb", orbID, "err", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// CheckUnbound 校验 ids（≤MaxPerMessage、不重复、存在）且每个都未绑定或已绑定到 threadID（空 = 新线程，须未绑定）。
// 只做预检（新建线程前避免白建 orb）；真正的绑定在事务里做。
func (s *Service) CheckUnbound(ctx context.Context, threadID string, list []string) error {
	if len(list) > MaxPerMessage {
		return errf(CodeInvalid, "at most %d attachments per message", MaxPerMessage)
	}
	seen := make(map[string]bool, len(list))
	for _, id := range list {
		if seen[id] {
			return errf(CodeInvalid, "duplicate attachment %s", id)
		}
		seen[id] = true
		a, err := s.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return errf(CodeAttachNotFound, "attachment %s not found", id)
		}
		if err != nil {
			return err
		}
		if a.ThreadID != nil && *a.ThreadID != threadID {
			return errf(CodeAttachNotFound, "attachment %s not found", id)
		}
	}
	return nil
}

// Bind 在 q（可为事务）上把 ids 绑定到线程；任一已属于别的线程或不存在 → attachment_not_found。
func Bind(ctx context.Context, q *db.Queries, threadID string, list []string) error {
	for _, id := range list {
		n, err := q.BindAttachment(ctx, db.BindAttachmentParams{ThreadID: &threadID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return errf(CodeAttachNotFound, "attachment %s not found", id)
		}
	}
	return nil
}

// BindTx 实现 threads.Attachments：在建线程的事务里绑定附件。
func (s *Service) BindTx(ctx context.Context, q *db.Queries, threadID string, list []string) error {
	return Bind(ctx, q, threadID, list)
}

// Check 实现 threads.Attachments：新建线程前预检附件。
func (s *Service) Check(ctx context.Context, list []string) error {
	return s.CheckUnbound(ctx, "", list)
}

// Deliver 把附件绑定到线程并确保它们都在 orb 内：逐个 BindAttachment → POST /attachments/ensure →
// 不在则 PUT /attachments/file（体为本地文件，orbd 校验 sha256）。orb 须已可用（不唤醒）。
func (s *Service) Deliver(ctx context.Context, orbID, threadID string, list []string) ([]threads.DeliveredAttachment, error) {
	if len(list) > MaxPerMessage {
		return nil, errf(CodeInvalid, "at most %d attachments per message", MaxPerMessage)
	}
	rows := make([]db.Attachment, 0, len(list))
	for _, id := range list {
		if !ValidID(id) {
			return nil, errf(CodeAttachNotFound, "attachment %s not found", id)
		}
		if err := Bind(ctx, s.st.Q, threadID, []string{id}); err != nil {
			return nil, err
		}
		a, err := s.Get(ctx, id)
		if err != nil {
			return nil, errf(CodeAttachNotFound, "attachment %s not found", id)
		}
		rows = append(rows, a)
	}
	if err := s.orbs.EnsureUsable(ctx, orbID, orbs.FeatureAttachments, false, 0); err != nil {
		return nil, orbErr(err)
	}
	out := make([]threads.DeliveredAttachment, 0, len(rows))
	for _, a := range rows {
		p, err := s.ensureInOrb(ctx, orbID, threadID, a)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", a.Name, a.ID, err)
		}
		out = append(out, threads.DeliveredAttachment{ID: a.ID, Name: a.Name, Mime: a.Mime, Size: a.Size, Path: p})
	}
	return out, nil
}

func orbErr(err error) error {
	switch {
	case errors.Is(err, orbs.ErrOrbdOutdated):
		return errf(CodeOrbdOutdated, "orbd does not support attachments; start a new thread")
	case errors.Is(err, orbs.ErrOrbGone):
		return errf(CodeOrbGone, "orb deleted")
	case errors.Is(err, orbs.ErrOrbNotReady):
		return errf(CodeOrbNotReady, "orb not ready")
	}
	return err
}

func (s *Service) ensureInOrb(ctx context.Context, orbID, threadID string, a db.Attachment) (string, error) {
	body, _ := json.Marshal(apigen.OrbAttachmentEnsureRequest{ThreadID: threadID, ID: a.ID, Name: a.Name, Size: a.Size, Sha256: a.Sha256})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/attachments/ensure", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	var res apigen.OrbAttachmentEnsureResult
	if err := s.orbJSON(ctx, orbID, req, &res); err != nil {
		return "", err
	}
	if res.Present {
		return res.Path, nil
	}
	f, err := os.Open(s.path(a.ID))
	if err != nil {
		return "", errf(CodeAttachNotFound, "attachment file missing")
	}
	defer f.Close()
	q := url.Values{"threadId": {threadID}, "id": {a.ID}, "name": {a.Name}, "sha256": {a.Sha256}}
	req, _ = http.NewRequestWithContext(ctx, http.MethodPut, "/attachments/file?"+q.Encode(), f)
	req.ContentLength = a.Size
	req.Header.Set("Content-Type", "application/octet-stream")
	if err := s.orbJSON(ctx, orbID, req, &res); err != nil {
		return "", err
	}
	return res.Path, nil
}

// orbJSON 发请求并把 2xx 的 JSON 体解到 out；非 2xx 时带上 orbd 的 {code,message}。
func (s *Service) orbJSON(ctx context.Context, orbID string, req *http.Request, out any) error {
	resp, err := s.orbs.Do(ctx, orbID, req)
	if err != nil {
		if e := orbErr(err); e != err {
			return e
		}
		return errf(CodeOrbdUnreachable, "orbd unreachable: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errf(CodeOrbdUnreachable, "orbd unreachable: %v", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct{ Code, Message string }
		if json.Unmarshal(b, &e) == nil && e.Code != "" {
			return &Error{Code: e.Code, Message: e.Message}
		}
		return errf(CodeOrbdUnreachable, "orbd returned %d", resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

// Run 每小时清理一次（直到 ctx 结束）：创建超过 24h 仍未绑定的附件、没有对应行的文件。
func (s *Service) Run(ctx context.Context) {
	s.GC(ctx)
	tick := time.NewTicker(gcInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.GC(ctx)
		}
	}
}

// GC 执行一轮清理。
func (s *Service) GC(ctx context.Context) {
	stale, err := s.st.Q.ListUnboundAttachmentsBefore(ctx, s.now().Add(-unboundTTL).UnixMilli())
	if err != nil {
		slog.Warn("attachments: 列出未绑定附件失败", "err", err)
		return
	}
	for _, a := range stale {
		// 在事务里复核仍未绑定再删行：列出之后被绑定（发消息）的附件不能删
		var deleted bool
		err := s.st.Tx(ctx, func(q *db.Queries) error {
			cur, err := q.GetAttachment(ctx, a.ID)
			if err != nil || cur.ThreadID != nil {
				return nil
			}
			n, err := q.DeleteAttachment(ctx, a.ID)
			deleted = n > 0
			return err
		})
		if err == nil && deleted {
			s.removeFile(a.ID)
		}
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-time.Hour)
	for _, e := range entries {
		name := e.Name()
		if name == ".tmp" {
			s.sweepTmp(cutoff)
			continue
		}
		if !ValidID(name) {
			continue
		}
		if _, err := s.st.Q.GetAttachment(ctx, name); errors.Is(err, sql.ErrNoRows) {
			// 上传先 rename 再插行：只删足够旧的孤儿，避开这个窗口
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
