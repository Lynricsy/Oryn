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

// etagFor 返回文件的 ETag：`"<mtimeUnixNano>-<size>"`（含引号，可直接用作 If-Match）。
func etagFor(fi fs.FileInfo) string {
	return fmt.Sprintf(`"%d-%d"`, fi.ModTime().UnixNano(), fi.Size())
}

// entryFor 由 Lstat 结果构造 FsEntry；符号链接带 target 与（指向 $HOME 之内时的）targetType。
func (r *Runtime) entryFor(abs string, fi fs.FileInfo) apigen.FsEntry {
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
			if _, ok := r.relHome(real); ok {
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

func (r *Runtime) lstatEntry(abs string) (apigen.FsEntry, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return apigen.FsEntry{}, err
	}
	return r.entryFor(abs, fi), nil
}

// isDirEntry：目录或指向目录的符号链接（排序时排在前面）。
func isDirEntry(e apigen.FsEntry) bool {
	return e.Type == apigen.FsEntryTypeDir || (e.TargetType != nil && *e.TargetType == apigen.FsEntryTargetTypeDir)
}

// ---- 只读路由 ----

func (r *Runtime) handleFsRoots(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, apigen.FsRoots{Home: r.home, Workdir: r.currentWorkdir()})
}

func (r *Runtime) handleFsList(w http.ResponseWriter, req *http.Request) {
	abs, real, err := r.userPath(req.URL.Query().Get("path"), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := os.Stat(real)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !st.IsDir() {
		writeErr(w, errf(http.StatusConflict, "not_a_directory", "%s is not a directory", abs))
		return
	}
	des, err := os.ReadDir(real)
	if err != nil {
		writeErr(w, err)
		return
	}
	entries := make([]apigen.FsEntry, 0, min(len(des), maxListEntries))
	for _, de := range des {
		p := filepath.Join(abs, de.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			continue // 列目录与 Lstat 之间被删除
		}
		entries = append(entries, r.entryFor(p, fi))
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
	abs, real, err := r.userPath(req.URL.Query().Get("path"), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	e, err := r.lstatEntry(abs)
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
	abs, real, err := r.userPath(req.URL.Query().Get("path"), true)
	if err != nil {
		writeErr(w, err)
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

func (r *Runtime) handleFsWrite(w http.ResponseWriter, req *http.Request) {
	abs, real, err := r.mutablePath(req.URL.Query().Get("path"))
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
	// 目标是符号链接时写入其指向的文件（real 已展开）。
	parent, err := os.Stat(filepath.Dir(real))
	if err != nil {
		writeErr(w, errf(http.StatusNotFound, "not_found", "parent directory of %s does not exist", abs))
		return
	}
	if !parent.IsDir() {
		writeErr(w, errf(http.StatusConflict, "not_a_directory", "parent of %s is not a directory", abs))
		return
	}
	mode := os.FileMode(0o644)
	cur := ""
	if st, err := os.Stat(real); err == nil {
		switch {
		case st.IsDir():
			writeErr(w, errf(http.StatusConflict, "is_a_directory", "%s is a directory", abs))
			return
		case !st.Mode().IsRegular():
			writeErr(w, badPath("%s is not a regular file", abs))
			return
		case !overwrite:
			writeErr(w, errf(http.StatusConflict, "exists", "%s already exists", abs))
			return
		}
		mode, cur = st.Mode().Perm(), etagFor(st)
	} else if !errors.Is(err, fs.ErrNotExist) {
		writeErr(w, err)
		return
	}
	if h := req.Header.Get("If-Match"); h != "" && !ifMatch(h, cur) {
		writeErr(w, errf(http.StatusPreconditionFailed, "modified", "%s changed since it was read", abs))
		return
	}
	tmp, err := r.receiveFile(http.MaxBytesReader(w, req.Body, r.maxFileBytes), "up-", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	err = os.Chmod(tmp, mode)
	if err == nil {
		err = movePath(tmp, real)
	}
	if err != nil {
		_ = os.Remove(tmp)
		writeErr(w, err)
		return
	}
	st, err := os.Stat(real)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 以请求路径回报，元数据取写入的文件（其 etag 即下次保存的 If-Match）。
	e := r.entryFor(real, st)
	e.Path, e.Name = abs, filepath.Base(abs)
	writeJSON(w, http.StatusOK, e)
}

func (r *Runtime) handleFsMkdir(w http.ResponseWriter, req *http.Request) {
	var body apigen.FsPathRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	abs, _, err := r.mutablePath(body.Path)
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
	e, err := r.lstatEntry(abs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (r *Runtime) handleFsRename(w http.ResponseWriter, req *http.Request) {
	var body apigen.FsRenameRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	src, _, err := r.removablePath(body.From)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 目标同样不得是工作目录或其上级（overwrite 会先删除目标）。
	dst, _, err := r.removablePath(body.To)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := os.Lstat(src); err != nil {
		writeErr(w, err)
		return
	}
	if src == dst {
		e, err := r.lstatEntry(dst)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, e)
		return
	}
	if rel, err := filepath.Rel(src, dst); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		writeErr(w, badRequest("cannot move %s into itself", src))
		return
	}
	if rel, err := filepath.Rel(dst, src); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
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
	e, err := r.lstatEntry(dst)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (r *Runtime) handleFsDelete(w http.ResponseWriter, req *http.Request) {
	var body apigen.FsPathRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	abs, _, err := r.removablePath(body.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
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
