package orbdrt

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const (
	// maxListEntries 是目录列表上限。
	maxListEntries = 5000
	// textSniffBytes 是判断「文本文件」读取的前缀长度。
	textSniffBytes = 8 << 10
)

// ConfinedHeader 是 fs/list 与 fs/file 在带 confine 时回的响应头（值为 1）：控制面据此确认 orbd 认得 confine，
// 不认得它的旧 orbd 的应答一律不用（不回落到不受限的读取）。
const ConfinedHeader = "X-Oryn-Confined"

// etagFor 返回文件的 ETag：`"<mtimeUnixNano>-<size>"`（含引号，可直接用作 If-Match）。
func etagFor(fi fs.FileInfo) string {
	return fmt.Sprintf(`"%d-%d"`, fi.ModTime().UnixNano(), fi.Size())
}

// entryFor 由 Lstat 结果构造 FsEntry；符号链接带 target 与（指向 root 之内时的）targetType。
func (r *Runtime) entryFor(root, abs string, fi fs.FileInfo) apigen.FsEntry {
	e := apigen.FsEntry{
		Path:  abs,
		Name:  filepath.Base(abs),
		Size:  fi.Size(),
		Mtime: fi.ModTime().UnixMilli(),
		Mode:  int(fi.Mode().Perm()),
	}
	switch m := fi.Mode(); {
	case m.IsDir():
		e.Type = apigen.FsEntryTypeDir
	case m.IsRegular():
		e.Type = apigen.FsEntryTypeFile
		tag := etagFor(fi)
		e.Etag = &tag
	case m&fs.ModeSymlink != 0:
		e.Type = apigen.FsEntryTypeSymlink
		if target, err := os.Readlink(abs); err == nil {
			e.Target = &target
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			if _, ok := relTo(root, real); ok {
				if st, err := os.Stat(real); err == nil {
					var tt apigen.FsEntryTargetType
					switch {
					case st.IsDir():
						tt = apigen.FsEntryTargetTypeDir
					case st.Mode().IsRegular():
						tt = apigen.FsEntryTargetTypeFile
					}
					if tt != "" {
						e.TargetType = &tt
					}
				}
			}
		}
	default:
		e.Type = apigen.FsEntryTypeOther
	}
	return e
}

func (r *Runtime) lstatEntry(root, abs string) (apigen.FsEntry, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	return r.entryFor(root, abs, fi), nil
}

// isDirEntry：目录或指向目录的符号链接（排序时排在前面）。
func isDirEntry(e apigen.FsEntry) bool {
	return e.Type == apigen.FsEntryTypeDir || (e.TargetType != nil && *e.TargetType == apigen.FsEntryTargetTypeDir)
}

// ---- 只读路由 ----

// handleFsRoots：orb 为 {$HOME, 工作目录}；runner 两者都是线程的工作目录（范围的根）。
// strict=1（控制面的 artifact 文件能力，M23）：orb 形态的请求带了 X-Oryn-Workdir 却没被采用时 404 workdir_missing，
// 不退回缺省工作目录——artifact 的读取范围不能因 app 下线而变宽。
func (r *Runtime) handleFsRoots(w http.ResponseWriter, req *http.Request) {
	strict, err := boolQuery(req, "strict", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	if strict && r.runner == nil {
		if _, given, ok := r.headerWorkdir(req); given && !ok {
			writeErr(w, errf(http.StatusNotFound, "workdir_missing", "working directory %q does not exist", req.Header.Get(WorkdirHeader)))
			return
		}
	}
	root, wd, err := r.scope(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apigen.FsRoots{Home: root, Workdir: wd})
}

func (r *Runtime) handleFsList(w http.ResponseWriter, req *http.Request) {
	root, _, err := r.scope(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	confine, err := confineDir(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	abs, real, err := r.userPath(root, req.URL.Query().Get("path"), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	// lstat 取目录项的 Lstat；带 confine 时经 os.Root 相对已打开的目录解析，列表进行中把目录换成链接也读不到范围外
	lstat := func(name string) (fs.FileInfo, error) { return os.Lstat(filepath.Join(abs, name)) }
	var (
		des []fs.DirEntry
		c   *confinedFile
	)
	if confine != "" {
		if !confined(real, confine) {
			writeErr(w, outsideConfine(abs))
			return
		}
		w.Header().Set(ConfinedHeader, "1")
		if c, err = openConfined(confine, abs, real); err != nil {
			writeErr(w, err)
			return
		}
		defer c.Close()
		if !c.st.IsDir() {
			writeErr(w, errf(http.StatusConflict, "not_a_directory", "%s is not a directory", abs))
			return
		}
		if des, err = c.f.ReadDir(-1); err != nil {
			writeErr(w, err)
			return
		}
		lstat = func(name string) (fs.FileInfo, error) { return c.root.Lstat(filepath.Join(c.rel, name)) }
	} else {
		st, err := os.Stat(real)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !st.IsDir() {
			writeErr(w, errf(http.StatusConflict, "not_a_directory", "%s is not a directory", abs))
			return
		}
		if des, err = os.ReadDir(real); err != nil {
			writeErr(w, err)
			return
		}
	}
	entries := make([]apigen.FsEntry, 0, min(len(des), maxListEntries))
	for _, de := range des {
		p := filepath.Join(abs, de.Name())
		fi, err := lstat(de.Name())
		if err != nil {
			continue // 列目录与 Lstat 之间被删除
		}
		e := r.entryFor(root, p, fi)
		if c != nil && !c.keepEntry(&e, filepath.Join(real, de.Name()), fi, confine) {
			continue
		}
		entries = append(entries, e)
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
	if len(entries) > maxListEntries {
		res.Entries, res.Truncated = entries[:maxListEntries], true
	}
	writeJSON(w, http.StatusOK, res)
}

func (r *Runtime) handleFsStat(w http.ResponseWriter, req *http.Request) {
	root, _, err := r.scope(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	abs, real, err := r.userPath(root, req.URL.Query().Get("path"), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	e, err := r.lstatEntry(root, abs)
	if err != nil {
		writeErr(w, err)
		return
	}
	// mime / text 针对内容：符号链接按其指向的文件计算。
	if st, err := os.Stat(real); err == nil && st.Mode().IsRegular() {
		if m, text, err := sniffFile(real); err == nil {
			e.Mime, e.Text = &m, &text
		}
	}
	writeJSON(w, http.StatusOK, e)
}

// sniffFile 读前 8KiB：mime 先按扩展名，缺省按内容嗅探；text = 无 NUL 且为合法 UTF-8（读满 8KiB 时允许末尾截断的码点）。
func sniffFile(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	buf := make([]byte, textSniffBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	b := buf[:n]
	m := mime.TypeByExtension(filepath.Ext(path))
	if m == "" {
		m = http.DetectContentType(b)
	}
	return m, isText(b, n == textSniffBytes), nil
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

func (r *Runtime) handleFsRead(w http.ResponseWriter, req *http.Request) {
	root, _, err := r.scope(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	confine, err := confineDir(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	abs, real, err := r.userPath(root, req.URL.Query().Get("path"), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	if confine != "" {
		if !confined(real, confine) {
			writeErr(w, outsideConfine(abs))
			return
		}
		w.Header().Set(ConfinedHeader, "1")
		c, err := openConfined(confine, abs, real)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer c.Close()
		serveRead(w, req, abs, c.f, c.st)
		return
	}
	st, err := os.Stat(real)
	if err != nil {
		writeErr(w, err)
		return
	}
	if st.IsDir() {
		writeErr(w, errf(http.StatusConflict, "is_a_directory", "%s is a directory", abs))
		return
	}
	if !st.Mode().IsRegular() {
		// 不打开 FIFO / 设备：读它们可能永远阻塞。
		writeErr(w, badPath("%s is not a regular file", abs))
		return
	}
	f, err := os.Open(real)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()
	st, err = f.Stat()
	if err != nil {
		writeErr(w, err)
		return
	}
	serveRead(w, req, abs, f, st)
}

// serveRead 提供已打开的文件 f（st 是它的 Stat）：目录回 409、不是普通文件回 400，其余按 Range / ETag 提供。
func serveRead(w http.ResponseWriter, req *http.Request, abs string, f *os.File, st fs.FileInfo) {
	if st.IsDir() {
		writeErr(w, errf(http.StatusConflict, "is_a_directory", "%s is a directory", abs))
		return
	}
	if !st.Mode().IsRegular() {
		writeErr(w, badPath("%s is not a regular file", abs))
		return
	}
	w.Header().Set("ETag", etagFor(st))
	// Content-Type 由 ServeContent 按扩展名推断（控制面强制附件形式、nosniff 与 CSP sandbox）。
	http.ServeContent(w, req, filepath.Base(abs), st.ModTime(), f)
}

// ---- 变更路由 ----

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

// writeTarget 核对写入目标：父目录存在且是目录、目标若存在须是普通文件、overwrite=false 时不得已存在、
// If-Match（非空时）与当前 ETag 相符。返回要沿用的权限与目标是否已存在。
func writeTarget(abs, real string, overwrite bool, ifMatchHdr string) (os.FileMode, bool, error) {
	// 目标是符号链接时写入其指向的文件（real 已展开）。
	parent, err := os.Stat(filepath.Dir(real))
	if err != nil {
		return 0, false, errf(http.StatusNotFound, "not_found", "parent directory of %s does not exist", abs)
	}
	if !parent.IsDir() {
		return 0, false, errf(http.StatusConflict, "not_a_directory", "parent of %s is not a directory", abs)
	}
	mode, cur, exists := os.FileMode(0o644), "", false
	if st, err := os.Stat(real); err == nil {
		switch {
		case st.IsDir():
			return 0, false, errf(http.StatusConflict, "is_a_directory", "%s is a directory", abs)
		case !st.Mode().IsRegular():
			return 0, false, badPath("%s is not a regular file", abs)
		case !overwrite:
			return 0, false, errf(http.StatusConflict, "exists", "%s already exists", abs)
		}
		mode, cur, exists = st.Mode().Perm(), etagFor(st), true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, false, err
	}
	if ifMatchHdr != "" && !ifMatch(ifMatchHdr, cur) {
		return 0, false, errf(http.StatusPreconditionFailed, "modified", "%s changed since it was read", abs)
	}
	return mode, exists, nil
}

func (r *Runtime) handleFsWrite(w http.ResponseWriter, req *http.Request) {
	root, _, err := r.scope(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	p := req.URL.Query().Get("path")
	abs, real, err := r.mutablePath(root, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	overwrite, err := boolQuery(req, "overwrite", true)
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.ContentLength > r.maxFileBytes {
		writeErr(w, errf(http.StatusRequestEntityTooLarge, "too_large", "file exceeds %d bytes", r.maxFileBytes))
		return
	}
	ifm := req.Header.Get("If-Match")
	// 预检只为尽早失败（不必先收完一个注定被拒的上传）；真正的判定在收完之后、持锁提交之前。
	if _, _, err := writeTarget(abs, real, overwrite, ifm); err != nil {
		writeErr(w, err)
		return
	}
	tmp, err := r.receiveFile(http.MaxBytesReader(w, req.Body, r.maxFileBytes), "up-", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer func() { _ = os.Remove(tmp) }() // 提交成功后 tmp 已不存在
	// 上传期间路径上的符号链接可能已变化：重新解析并校验，再按规范化目标加变更锁、重新核对前置条件。
	if abs, real, err = r.mutablePath(root, p); err != nil {
		writeErr(w, err)
		return
	}
	unlock := lockPaths(real)
	defer unlock()
	mode, exists, err := writeTarget(abs, real, overwrite, ifm)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := os.Chmod(tmp, mode); err != nil {
		writeErr(w, err)
		return
	}
	if fsCommitHook != nil {
		fsCommitHook(real)
	}
	// 只创建（overwrite=false）以 RENAME_NOREPLACE 提交：锁外的写入者（agent）在核对之后创建的文件也不会被覆盖。
	if err := commitFile(tmp, real, !overwrite); err != nil {
		if errors.Is(err, fs.ErrExist) && !exists {
			err = errf(http.StatusConflict, "exists", "%s already exists", abs)
		}
		writeErr(w, err)
		return
	}
	st, err := os.Stat(real)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 以请求路径回报，元数据取写入的文件（其 etag 即下次保存的 If-Match）。
	e := r.entryFor(root, real, st)
	e.Path, e.Name = abs, filepath.Base(abs)
	writeJSON(w, http.StatusOK, e)
}

// fsCommitHook 仅供测试：上传在前置条件复核之后、提交之前调用它（参数为规范化目标路径）。
var fsCommitHook func(dst string)

// commitFile 把 $HOME/.oryn/tmp 下的临时文件提交到 dst（noReplace 时 dst 已存在即失败，错误满足 fs.ErrExist）。
// 跨文件系统时先复制到 dst 同目录的隐藏临时文件，再在目标文件系统内原子提交。
func commitFile(tmp, dst string, noReplace bool) error {
	err := renameFile(tmp, dst, noReplace)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	staged, err := copyFileTo(tmp, filepath.Dir(dst))
	if err != nil {
		return err
	}
	if err = renameFile(staged, dst, noReplace); err != nil {
		_ = os.Remove(staged)
	}
	return err
}

// renameFile 是 os.Rename；noReplace 时改用 renameNoReplace（不覆盖已存在的目标）。
func renameFile(src, dst string, noReplace bool) error {
	if !noReplace {
		return os.Rename(src, dst)
	}
	return renameNoReplace(src, dst)
}

// linkNoReplace 是不支持 RENAME_NOREPLACE 的文件系统上的退路（仅限文件）：link 不会覆盖已有目标，成功后删除源。
func linkNoReplace(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// copyFileTo 把 src 复制为 dir 下的隐藏临时文件（保留权限）并 Sync，返回其路径。
func copyFileTo(src, dir string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return "", err
	}
	out, err := os.CreateTemp(dir, ".orbd-up-*")
	if err != nil {
		return "", err
	}
	name := out.Name()
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, st.Mode().Perm())
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// fsLocks 是按规范化路径的变更锁：同一目标上的上传提交、重命名与删除互斥。锁外的写入者（agent）
// 由提交前的复核与 RENAME_NOREPLACE 兜底。
var fsLocks = struct {
	mu sync.Mutex
	m  map[string]*pathLock
}{m: map[string]*pathLock{}}

type pathLock struct {
	mu   sync.Mutex
	refs int
}

// lockPaths 按字典序锁住 keys（去重，避免互相等待），返回解锁函数。
func lockPaths(keys ...string) (unlock func()) {
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	locks := make([]*pathLock, len(keys))
	fsLocks.mu.Lock()
	for i, k := range keys {
		l := fsLocks.m[k]
		if l == nil {
			l = &pathLock{}
			fsLocks.m[k] = l
		}
		l.refs++
		locks[i] = l
	}
	fsLocks.mu.Unlock()
	for _, l := range locks {
		l.mu.Lock()
	}
	return func() {
		for _, l := range locks {
			l.mu.Unlock()
		}
		fsLocks.mu.Lock()
		for i, k := range keys {
			if locks[i].refs--; locks[i].refs == 0 {
				delete(fsLocks.m, k)
			}
		}
		fsLocks.mu.Unlock()
	}
}

// entryPath 返回 abs 所指目录项的规范化路径：父目录展开符号链接，最后一段保持原样（目录项本身可能是符号链接，
// 重命名 / 删除作用于链接而不是其目标）。经不同别名（符号链接父目录）指向同一目录项的路径得到同一结果。
func entryPath(abs string) (string, error) {
	parent, err := evalExisting(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

// within 报告 p 是否等于 dir 或位于其下。
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// confineDir 解析 fs/list 与 fs/file 可选的 confine 查询参数（artifact 的 files 能力用的只读视图）：必须是已存在
// 目录的绝对路径，返回展开符号链接后的真实路径；没给时回空串（不额外限制）。
func confineDir(req *http.Request) (string, error) {
	c := req.URL.Query().Get("confine")
	if c == "" {
		return "", nil
	}
	if !filepath.IsAbs(c) || strings.ContainsRune(c, 0) {
		return "", badRequest("confine must be an absolute directory: %q", c)
	}
	real, err := filepath.EvalSymlinks(c)
	if err != nil {
		return "", fsError(err)
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", fsError(err)
	}
	if !st.IsDir() {
		return "", errf(http.StatusConflict, "not_a_directory", "confine %s is not a directory", c)
	}
	return real, nil
}

// confined 报告真实路径 real 是否在 confine 的范围里：等于 confine 或在其下（按路径段比较，`/w/ab` 不在 `/w/a` 里），
// 且相对 confine 的任何一段都不是 `.git`（不分大小写）。
func confined(real, confine string) bool {
	if !within(real, confine) {
		return false
	}
	rel, _ := filepath.Rel(confine, real)
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if strings.EqualFold(seg, ".git") {
			return false
		}
	}
	return true
}

// keepEntry 判定 confine 下目录列表里的一项是否保留：real 是它在已展开的父目录下的路径（目录项本身不展开）。
// 符号链接须指向范围之内（悬空的不保留），指向文件时 size 改为目标文件的大小（经 os.Root 取，不会取到范围外）。
func (c *confinedFile) keepEntry(e *apigen.FsEntry, real string, fi fs.FileInfo, confine string) bool {
	if !confined(real, confine) {
		return false
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		return true
	}
	target, err := filepath.EvalSymlinks(real)
	if err != nil || !confined(target, confine) {
		return false
	}
	st, err := c.root.Stat(filepath.Join(c.rel, filepath.Base(real)))
	if err != nil {
		return false
	}
	if st.Mode().IsRegular() {
		e.Size = st.Size()
	}
	return true
}

// outsideConfine 是 confine 范围之外的路径的应答：与不存在一样回 404。
func outsideConfine(abs string) *apiError {
	return errf(http.StatusNotFound, "not_found", "%s: no such file or directory", abs)
}

// confinedFile 是在 confine 之内打开的文件或目录。
type confinedFile struct {
	root *os.Root
	rel  string // 相对 confine 的路径
	f    *os.File
	st   fs.FileInfo
}

func (c *confinedFile) Close() {
	_ = c.f.Close()
	_ = c.root.Close()
}

// confineOpenHook 在 openConfined 打开之前调用（测试用：在「检查」与「打开」之间换掉目录项）。
var confineOpenHook func()

// openConfined 打开已检查过的真实路径 real（confined(real, confine) 为真），堵住「检查之后、打开之前把路径上的
// 目录项换成符号链接」的竞态：
//   - 经 os.Root 以 confine 的目录句柄逐段解析 real 相对 confine 的路径，换成指向范围外的链接时打不开；
//   - 以 O_NONBLOCK 打开（换成 FIFO 也不会卡住），文件类型由调用方按句柄的 Stat 判断；
//   - 打开之后按原路径 abs 重新求真实路径，要求仍在范围内（含 .git 规则）且与打开的是同一个文件，挡住换成指向
//     范围内 .git 的链接。
//
// 不符时与不存在一样回 404。
func openConfined(confine, abs, real string) (*confinedFile, error) {
	rel, err := filepath.Rel(confine, real)
	if err != nil {
		return nil, outsideConfine(abs)
	}
	root, err := os.OpenRoot(confine)
	if err != nil {
		return nil, fsError(err)
	}
	if confineOpenHook != nil {
		confineOpenHook()
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		_ = root.Close()
		return nil, outsideConfine(abs)
	}
	c := &confinedFile{root: root, rel: rel, f: f}
	if c.st, err = f.Stat(); err != nil {
		c.Close()
		return nil, fsError(err)
	}
	again, err := filepath.EvalSymlinks(abs)
	if err != nil || !confined(again, confine) {
		c.Close()
		return nil, outsideConfine(abs)
	}
	if st, err := os.Stat(again); err != nil || !os.SameFile(st, c.st) {
		c.Close()
		return nil, outsideConfine(abs)
	}
	return c, nil
}

func (r *Runtime) handleFsMkdir(w http.ResponseWriter, req *http.Request) {
	root, _, err := r.scope(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body apigen.FsPathRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	abs, _, err := r.mutablePath(root, body.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		if errors.Is(err, syscall.EEXIST) || errors.Is(err, syscall.ENOTDIR) {
			writeErr(w, errf(http.StatusConflict, "not_a_directory", "%s (or a parent) exists and is not a directory", abs))
			return
		}
		writeErr(w, err)
		return
	}
	e, err := r.lstatEntry(root, abs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (r *Runtime) handleFsRename(w http.ResponseWriter, req *http.Request) {
	root, wd, err := r.scope(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body apigen.FsRenameRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	src, _, err := r.removablePath(root, wd, body.From)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 目标同样不得是工作目录或其上级（overwrite 会先删除目标）。
	dst, _, err := r.removablePath(root, wd, body.To)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 同一性与包含关系一律按规范化目录项判断：经符号链接父目录的别名可能指向源目录项本身，
	// 词法比较会把它当成另一个已存在的目标，overwrite 时先删掉源再移动。
	srcEntry, err := entryPath(src)
	if err != nil {
		writeErr(w, err)
		return
	}
	dstEntry, err := entryPath(dst)
	if err != nil {
		writeErr(w, err)
		return
	}
	unlock := lockPaths(srcEntry, dstEntry)
	defer unlock()
	if _, err := os.Lstat(src); err != nil {
		writeErr(w, err)
		return
	}
	if srcEntry == dstEntry {
		// 改名到自身（含经别名）：不做任何改动
		e, err := r.lstatEntry(root, dst)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, e)
		return
	}
	if within(dstEntry, srcEntry) {
		writeErr(w, badRequest("cannot move %s into itself", src))
		return
	}
	if within(srcEntry, dstEntry) {
		writeErr(w, badRequest("destination %s contains the source", dst))
		return
	}
	if _, err := os.Lstat(dst); err == nil {
		if body.Overwrite == nil || !*body.Overwrite {
			writeErr(w, errf(http.StatusConflict, "exists", "%s already exists", dst))
			return
		}
		if err := os.RemoveAll(dst); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := movePath(src, dst); err != nil {
		writeErr(w, err)
		return
	}
	e, err := r.lstatEntry(root, dst)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (r *Runtime) handleFsDelete(w http.ResponseWriter, req *http.Request) {
	root, wd, err := r.scope(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body apigen.FsPathRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	abs, _, err := r.removablePath(root, wd, body.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	entry, err := entryPath(abs)
	if err != nil {
		writeErr(w, err)
		return
	}
	unlock := lockPaths(entry)
	defer unlock()
	if _, err := os.Lstat(abs); err != nil {
		writeErr(w, err)
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
