package sshgw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	fsReapEvery     = 30 * time.Second
	fsIdleTTL       = 2 * time.Minute
	fsMaxList       = 5000
	fsSniffBytes    = 8 << 10
	fsNewFileMode   = 0o644
	fsTmpInfix      = ".oryn-tmp-"
	codeUnreachable = "ssh_unreachable"
)

// fsConn 是缓存的一条 SFTP 连接（按主机）。busy、last、closed、untrack 由 Service.fsMu 保护。
type fsConn struct {
	host db.SshHost // 拨号时的主机快照（审计用名称）
	up   *upstream
	c    *sftp.Client
	home string

	busy    int
	last    time.Time
	closed  bool
	untrack func()
	once    sync.Once
}

func (fc *fsConn) close() {
	fc.once.Do(func() {
		_ = fc.c.Close()
		fc.up.Close()
	})
}

func fsErrf(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// acquire 取主机的 SFTP 连接（没有就拨号建立并缓存），返回用完要调的 release。
// 主机不存在 → not_found；拨号失败 → ssh_unreachable（文案同拨号错误）。
func (s *Service) acquire(ctx context.Context, hostID string) (*fsConn, func(), error) {
	if fc := s.cachedFs(hostID); fc != nil {
		return fc, s.releaser(fc), nil
	}
	h, err := s.host(ctx, hostID)
	if err != nil {
		return nil, nil, err
	}
	since := s.snapshot()
	up, err := s.dial(ctx, h)
	if err != nil {
		return nil, nil, fsErrf(codeUnreachable, "%s", err.Error())
	}
	c, err := sftp.NewClient(up.client)
	if err != nil {
		up.Close()
		return nil, nil, fsErrf(codeUnreachable, "打开 SFTP 失败：%s", err.Error())
	}
	home, err := c.Getwd()
	if err != nil || !strings.HasPrefix(home, "/") {
		home = "/"
	}
	fc := &fsConn{host: h, up: up, c: c, home: path.Clean(home), busy: 1, last: time.Now()}
	untrack, ok := s.track(up.hostIDs, since, func() { s.evictFs(fc) })
	if !ok {
		fc.close()
		return nil, nil, fsErrf(codeUnreachable, "%s", errStale.Error())
	}
	s.fsMu.Lock()
	fc.untrack = untrack
	if fc.closed { // 登记之后、入缓存之前主机被改
		s.fsMu.Unlock()
		return nil, nil, fsErrf(codeUnreachable, "%s", errStale.Error())
	}
	if cur := s.fsConns[hostID]; cur != nil { // 并发拨通了两条：用先入缓存的那条
		cur.busy++
		fc.closed = true
		s.fsMu.Unlock()
		untrack()
		fc.close()
		return cur, s.releaser(cur), nil
	}
	s.fsConns[hostID] = fc
	s.fsMu.Unlock()
	go func() { _ = up.client.Wait(); s.evictFs(fc) }() // 连接断了就移出缓存
	return fc, s.releaser(fc), nil
}

func (s *Service) cachedFs(hostID string) *fsConn {
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	fc := s.fsConns[hostID]
	if fc != nil {
		fc.busy++
	}
	return fc
}

func (s *Service) releaser(fc *fsConn) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.fsMu.Lock()
			fc.busy--
			fc.last = time.Now()
			s.fsMu.Unlock()
		})
	}
}

// evictFs 把连接移出缓存并关闭（幂等）：主机失效、连接断开、闲置回收时调用。
func (s *Service) evictFs(fc *fsConn) {
	s.fsMu.Lock()
	untrack := s.evictLocked(fc)
	s.fsMu.Unlock()
	untrack()
	fc.close()
}

func (s *Service) evictLocked(fc *fsConn) (untrack func()) {
	fc.closed = true
	if s.fsConns[fc.host.ID] == fc {
		delete(s.fsConns, fc.host.ID)
	}
	if fc.untrack == nil {
		return func() {}
	}
	return fc.untrack
}

// reapFs 关掉空闲超过 fsIdleTTL 的 SFTP 连接。
func (s *Service) reapFs() {
	now := time.Now()
	var victims []*fsConn
	var untracks []func()
	s.fsMu.Lock()
	for _, fc := range s.fsConns {
		if fc.busy == 0 && now.Sub(fc.last) > fsIdleTTL {
			victims = append(victims, fc)
			untracks = append(untracks, s.evictLocked(fc))
		}
	}
	s.fsMu.Unlock()
	for i, fc := range victims {
		untracks[i]()
		fc.close()
	}
}

// fsAudit 记一行 SFTP 审计（只记会改动或下载的操作）。
func (s *Service) fsAudit(ctx context.Context, h db.SshHost, detail string, err error) {
	row := auditRow{source: srcOwner, host: h, kind: "sftp", detail: detail, ok: err == nil}
	if err != nil {
		row.detail += "：" + err.Error()
	}
	s.audit(ctx, row)
}

// cleanPath 规范化请求路径：path.Clean 之后必须是绝对路径。
func cleanPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", invalid("路径不能为空")
	}
	c := path.Clean(p)
	if !strings.HasPrefix(c, "/") {
		return "", invalid("路径必须是绝对路径：%s", p)
	}
	return c, nil
}

// fsErr 把 SFTP 错误映射成业务错误：不存在 → not_found，无权限 → forbidden，其余原样。
func fsErr(err error, p string) error {
	var se *Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &se):
		return se
	case errors.Is(err, fs.ErrNotExist):
		return notFound("%s 不存在", p)
	case errors.Is(err, fs.ErrPermission):
		return fsErrf("forbidden", "没有权限访问 %s", p)
	default:
		return err
	}
}

func fsEtag(fi os.FileInfo) string {
	return fmt.Sprintf(`"%d-%d"`, fi.ModTime().UnixNano(), fi.Size())
}

// entry 由 Lstat 结果构造 FsEntry；符号链接带 target 与（Stat 成功时的）targetType。
func (fc *fsConn) entry(abs string, fi os.FileInfo) apigen.FsEntry {
	e := apigen.FsEntry{
		Path: abs, Name: path.Base(abs), Size: fi.Size(), Mtime: fi.ModTime().UnixMilli(), Mode: int(fi.Mode().Perm()),
	}
	switch m := fi.Mode(); {
	case m.IsDir():
		e.Type = apigen.FsEntryTypeDir
	case m.IsRegular():
		e.Type = apigen.FsEntryTypeFile
		e.Etag = new(fsEtag(fi))
	case m&fs.ModeSymlink != 0:
		e.Type = apigen.FsEntryTypeSymlink
		if target, err := fc.c.ReadLink(abs); err == nil {
			e.Target = &target
		}
		if st, err := fc.c.Stat(abs); err == nil {
			switch {
			case st.IsDir():
				e.TargetType = new(apigen.FsEntryTargetTypeDir)
			case st.Mode().IsRegular():
				e.TargetType = new(apigen.FsEntryTargetTypeFile)
			}
		}
	default:
		e.Type = apigen.FsEntryTypeOther
	}
	return e
}

func isDirEntry(e apigen.FsEntry) bool {
	return e.Type == apigen.FsEntryTypeDir || (e.TargetType != nil && *e.TargetType == apigen.FsEntryTargetTypeDir)
}

// FsRoots 返回目标机上登录用户的主目录（取不到时为 /）。
func (s *Service) FsRoots(ctx context.Context, hostID string) (apigen.SSHFsRoots, error) {
	fc, release, err := s.acquire(ctx, hostID)
	if err != nil {
		return apigen.SSHFsRoots{}, err
	}
	defer release()
	return apigen.SSHFsRoots{Home: fc.home}, nil
}

// FsList 列目录：目录在前、按名不分大小写排序，最多 fsMaxList 条。
func (s *Service) FsList(ctx context.Context, hostID, p string) (apigen.FsListing, error) {
	abs, err := cleanPath(p)
	if err != nil {
		return apigen.FsListing{}, err
	}
	fc, release, err := s.acquire(ctx, hostID)
	if err != nil {
		return apigen.FsListing{}, err
	}
	defer release()
	st, err := fc.c.Stat(abs)
	if err != nil {
		return apigen.FsListing{}, fsErr(err, abs)
	}
	if !st.IsDir() {
		return apigen.FsListing{}, fsErrf("not_a_directory", "%s 不是目录", abs)
	}
	fis, err := fc.c.ReadDir(abs)
	if err != nil {
		return apigen.FsListing{}, fsErr(err, abs)
	}
	entries := make([]apigen.FsEntry, 0, min(len(fis), fsMaxList))
	for _, fi := range fis {
		entries = append(entries, fc.entry(path.Join(abs, fi.Name()), fi))
	}
	slices.SortFunc(entries, func(a, b apigen.FsEntry) int {
		if da, db := isDirEntry(a), isDirEntry(b); da != db {
			if da {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	res := apigen.FsListing{Path: abs, Entries: entries}
	if len(entries) > fsMaxList {
		res.Entries, res.Truncated = entries[:fsMaxList], true
	}
	return res, nil
}

// FsStat 返回单项元数据；普通文件（含指向普通文件的符号链接）另给 mime 与 text。
func (s *Service) FsStat(ctx context.Context, hostID, p string) (apigen.FsEntry, error) {
	abs, err := cleanPath(p)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	fc, release, err := s.acquire(ctx, hostID)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	defer release()
	fi, err := fc.c.Lstat(abs)
	if err != nil {
		return apigen.FsEntry{}, fsErr(err, abs)
	}
	e := fc.entry(abs, fi)
	if st, err := fc.c.Stat(abs); err == nil && st.Mode().IsRegular() {
		if m, text, err := sniffRemote(fc.c, abs); err == nil {
			e.Mime, e.Text = &m, &text
		}
	}
	return e, nil
}

// sniffRemote 读前 8KiB：mime 先按扩展名，缺省按内容嗅探；text = 无 NUL 且为合法 UTF-8。
func sniffRemote(c *sftp.Client, p string) (string, bool, error) {
	f, err := c.Open(p)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	buf := make([]byte, fsSniffBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	b := buf[:n]
	m := mime.TypeByExtension(path.Ext(p))
	if m == "" {
		m = http.DetectContentType(b)
	}
	return m, isText(b, n == fsSniffBytes), nil
}

// isText：无 NUL 且为合法 UTF-8；truncated 时允许末尾一个不完整的码点。
func isText(b []byte, truncated bool) bool {
	if slices.Contains(b, 0) {
		return false
	}
	if truncated {
		for i := 1; i <= utf8.UTFMax-1 && i <= len(b); i++ {
			if utf8.RuneStart(b[len(b)-i]) {
				if !utf8.FullRune(b[len(b)-i:]) {
					b = b[:len(b)-i]
				}
				break
			}
		}
	}
	return utf8.Valid(b)
}

// FsFile 是打开供下载的远端文件；Close 同时归还 SFTP 连接。
type FsFile struct {
	*sftp.File
	Info    os.FileInfo
	Path    string
	release func()
}

// Close 关闭文件并归还连接。
func (f *FsFile) Close() error {
	err := f.File.Close()
	f.release()
	return err
}

// Etag 返回文件的 ETag。
func (f *FsFile) Etag() string { return fsEtag(f.Info) }

// FsOpen 打开文件供下载：目录 → is_a_directory，不是普通文件 → invalid_path。
func (s *Service) FsOpen(ctx context.Context, hostID, p string) (*FsFile, error) {
	abs, err := cleanPath(p)
	if err != nil {
		return nil, err
	}
	fc, release, err := s.acquire(ctx, hostID)
	if err != nil {
		return nil, err
	}
	f, err := s.openRead(fc, abs)
	s.fsAudit(ctx, fc.host, "get "+abs, err)
	if err != nil {
		release()
		return nil, err
	}
	f.release = release
	return f, nil
}

func (s *Service) openRead(fc *fsConn, abs string) (*FsFile, error) {
	st, err := fc.c.Stat(abs)
	if err != nil {
		return nil, fsErr(err, abs)
	}
	if st.IsDir() {
		return nil, fsErrf("is_a_directory", "%s 是目录", abs)
	}
	if !st.Mode().IsRegular() {
		return nil, fsErrf("invalid_path", "%s 不是普通文件", abs)
	}
	f, err := fc.c.Open(abs)
	if err != nil {
		return nil, fsErr(err, abs)
	}
	if st, err = f.Stat(); err != nil {
		_ = f.Close()
		return nil, fsErr(err, abs)
	}
	return &FsFile{File: f, Info: st, Path: abs}, nil
}

// FsWrite 整文件写入：写同目录临时文件，沿用原文件权限（新文件 0644）后原子替换。
// 已存在且 overwrite=false → exists；ifMatch 非空且与当前 etag 不符（含文件不存在）→ modified。
// body 的读取错误原样返回（调用方据此识别超限）。
func (s *Service) FsWrite(ctx context.Context, hostID, p string, overwrite bool, ifMatch string, body io.Reader) (apigen.FsEntry, error) {
	abs, err := cleanPath(p)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	fc, release, err := s.acquire(ctx, hostID)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	defer release()
	e, err := writeRemote(fc, abs, overwrite, ifMatch, body)
	s.fsAudit(ctx, fc.host, "put "+abs, err)
	return e, err
}

func writeRemote(fc *fsConn, abs string, overwrite bool, ifMatchHdr string, body io.Reader) (apigen.FsEntry, error) {
	c := fc.c
	// 目标是符号链接时写入其指向的文件。
	real := abs
	if lfi, err := c.Lstat(abs); err == nil && lfi.Mode()&fs.ModeSymlink != 0 {
		if rp, err := c.RealPath(abs); err == nil {
			real = rp
		}
	}
	parent, err := c.Stat(path.Dir(real))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return apigen.FsEntry{}, notFound("%s 的父目录不存在", abs)
		}
		return apigen.FsEntry{}, fsErr(err, abs)
	}
	if !parent.IsDir() {
		return apigen.FsEntry{}, fsErrf("not_a_directory", "%s 的父目录不是目录", abs)
	}
	mode, cur, exists := os.FileMode(fsNewFileMode), "", false
	if st, err := c.Stat(real); err == nil {
		switch {
		case st.IsDir():
			return apigen.FsEntry{}, fsErrf("is_a_directory", "%s 是目录", abs)
		case !st.Mode().IsRegular():
			return apigen.FsEntry{}, fsErrf("invalid_path", "%s 不是普通文件", abs)
		case !overwrite:
			return apigen.FsEntry{}, fsErrf("exists", "%s 已存在", abs)
		}
		mode, cur, exists = st.Mode().Perm(), fsEtag(st), true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return apigen.FsEntry{}, fsErr(err, abs)
	}
	if ifMatchHdr != "" && !ifMatch(ifMatchHdr, cur) {
		return apigen.FsEntry{}, fsErrf("modified", "%s 在读取之后被改过", abs)
	}

	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	tmp := path.Join(path.Dir(real), "."+path.Base(real)+fsTmpInfix+hex.EncodeToString(rnd[:]))
	f, err := c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return apigen.FsEntry{}, fsErr(err, abs)
	}
	committed := false
	defer func() {
		if !committed {
			_ = c.Remove(tmp)
		}
	}()
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		return apigen.FsEntry{}, err
	}
	if err := f.Close(); err != nil {
		return apigen.FsEntry{}, fsErr(err, abs)
	}
	if err := c.Chmod(tmp, mode); err != nil {
		return apigen.FsEntry{}, fsErr(err, abs)
	}
	if exists {
		err = replace(c, tmp, real)
	} else {
		err = c.Rename(tmp, real)
	}
	if err != nil {
		return apigen.FsEntry{}, fsErr(err, abs)
	}
	committed = true
	st, err := c.Stat(real)
	if err != nil {
		return apigen.FsEntry{}, fsErr(err, abs)
	}
	// 以请求路径回报，元数据取写入的文件（其 etag 即下次保存的 If-Match）。
	e := fc.entry(real, st)
	e.Path, e.Name = abs, path.Base(abs)
	return e, nil
}

// replace 把 src 改名为已存在的 dst：优先 posix-rename 扩展；不支持时先删 dst 再 rename。
func replace(c *sftp.Client, src, dst string) error {
	if err := c.PosixRename(src, dst); err == nil {
		return nil
	}
	if err := c.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return c.Rename(src, dst)
}

// ifMatch 报告 If-Match 头（逗号分隔的强 ETag 或 `*`）是否与 cur（文件不存在时为空）相符。
func ifMatch(header, cur string) bool {
	for v := range strings.SplitSeq(header, ",") {
		v = strings.TrimSpace(v)
		if v == "*" && cur != "" {
			return true
		}
		if v != "" && v == cur {
			return true
		}
	}
	return false
}

// FsMkdir 新建目录（含缺失的上级目录）。
func (s *Service) FsMkdir(ctx context.Context, hostID, p string) (apigen.FsEntry, error) {
	abs, err := cleanPath(p)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	fc, release, err := s.acquire(ctx, hostID)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	defer release()
	e, err := func() (apigen.FsEntry, error) {
		if err := fc.c.MkdirAll(abs); err != nil {
			return apigen.FsEntry{}, fsErr(err, abs)
		}
		fi, err := fc.c.Lstat(abs)
		if err != nil {
			return apigen.FsEntry{}, fsErr(err, abs)
		}
		return fc.entry(abs, fi), nil
	}()
	s.fsAudit(ctx, fc.host, "mkdir "+abs, err)
	return e, err
}

// FsRename 移动 / 重命名；目标已存在且未 overwrite → exists。
func (s *Service) FsRename(ctx context.Context, hostID, from, to string, overwrite bool) (apigen.FsEntry, error) {
	src, err := cleanPath(from)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	dst, err := cleanPath(to)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	fc, release, err := s.acquire(ctx, hostID)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	defer release()
	e, err := func() (apigen.FsEntry, error) {
		c := fc.c
		if _, err := c.Lstat(src); err != nil {
			return apigen.FsEntry{}, fsErr(err, src)
		}
		if src == dst {
			return apigen.FsEntry{}, invalid("源与目标相同")
		}
		_, lerr := c.Lstat(dst)
		switch {
		case lerr == nil && !overwrite:
			return apigen.FsEntry{}, fsErrf("exists", "%s 已存在", dst)
		case lerr == nil:
			err = replace(c, src, dst)
		case errors.Is(lerr, fs.ErrNotExist):
			err = c.Rename(src, dst)
		default:
			err = lerr
		}
		if err != nil {
			return apigen.FsEntry{}, fsErr(err, dst)
		}
		fi, err := c.Lstat(dst)
		if err != nil {
			return apigen.FsEntry{}, fsErr(err, dst)
		}
		return fc.entry(dst, fi), nil
	}()
	s.fsAudit(ctx, fc.host, "rename "+src+" → "+dst, err)
	return e, err
}

// FsDelete 递归删除文件或目录（不跟随符号链接）；/ 与登录用户主目录 → protected_path。
func (s *Service) FsDelete(ctx context.Context, hostID, p string) error {
	abs, err := cleanPath(p)
	if err != nil {
		return err
	}
	fc, release, err := s.acquire(ctx, hostID)
	if err != nil {
		return err
	}
	defer release()
	if abs == "/" || abs == fc.home {
		return fsErrf("protected_path", "不能删除 %s", abs)
	}
	err = func() error {
		fi, err := fc.c.Lstat(abs)
		if err != nil {
			return fsErr(err, abs)
		}
		return fsErr(removeAll(fc.c, abs, fi), abs)
	}()
	s.fsAudit(ctx, fc.host, "delete "+abs, err)
	return err
}

// removeAll 递归删除；按 Lstat 判断类型，指向目录的符号链接只删链接本身。
func removeAll(c *sftp.Client, p string, fi os.FileInfo) error {
	if !fi.IsDir() {
		return c.Remove(p)
	}
	kids, err := c.ReadDir(p)
	if err != nil {
		return err
	}
	for _, k := range kids {
		if err := removeAll(c, path.Join(p, k.Name()), k); err != nil {
			return err
		}
	}
	return c.RemoveDirectory(p)
}
