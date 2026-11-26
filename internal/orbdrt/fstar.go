package orbdrt

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 目录传输（M30 步骤 2.3，docs/plan-m30.md C6 / C7）：
//   - GET /fs/tar?path=&exclude=…&gitignore=1 把文件或目录打成 tar 流（响应头 X-Oryn-Tar-Kind: file|dir）；
//   - PUT /fs/tar?path=&kind=file|dir 把 tar 流写到 path（path 是目标的完整路径，目录内容合并进已存在的目录）。
// 两端路径都须在 $HOME 之下、不等于 $HOME、不在 $HOME/.oryn 之内。控制面在两台 orbd 之间流式中转，不落盘。

const (
	// MaxTarBytes 是 PUT /fs/tar 的单次上限（tar 流本身的字节数）。
	MaxTarBytes = 2 << 30
	// maxTarEntries 是 PUT /fs/tar 的条目数上限。
	maxTarEntries = 500000
	// tarKindHeader 是 GET /fs/tar 的响应头：源是文件还是目录。
	tarKindHeader = "X-Oryn-Tar-Kind"
	tarKindFile   = "file"
	tarKindDir    = "dir"
)

// tarPath 解析 /fs/tar 的路径：相对路径以 $HOME 为根；（跟随符号链接后）须在 $HOME 之下且不是 $HOME 本身
// （403 outside_home / protected_path），也不在 $HOME/.oryn 之内（403 protected_path，读写都拒）。
// 返回词法上的绝对路径与展开符号链接后的真实路径。
func (r *Runtime) tarPath(p string) (abs, real string, err error) {
	if strings.TrimSpace(p) == "" {
		return "", "", badRequest("path is required")
	}
	if strings.ContainsRune(p, 0) {
		return "", "", badRequest("path contains NUL")
	}
	home, err := filepath.EvalSymlinks(r.home)
	if err != nil {
		return "", "", err
	}
	return r.mutablePath(home, r.resolvePath(p))
}

// tarExcluder 是 exclude 模式：含 `/` 的模式匹配相对路径，不含的匹配任意深度的 basename。
type tarExcluder []string

func parseExcludes(pats []string) (tarExcluder, error) {
	for _, p := range pats {
		if p == "" {
			return nil, badRequest("empty exclude pattern")
		}
		if _, err := path.Match(p, ""); err != nil {
			return nil, badRequest("invalid exclude pattern %q", p)
		}
	}
	return tarExcluder(pats), nil
}

func (ex tarExcluder) match(rel string) bool {
	base := path.Base(rel)
	for _, p := range ex {
		target := base
		if strings.Contains(p, "/") {
			target = rel
		}
		if ok, _ := path.Match(p, target); ok {
			return true
		}
	}
	return false
}

// matchAny 报告 rel 本身或它的任一上级目录是否被排除。
func (ex tarExcluder) matchAny(rel string) bool {
	for p := rel; p != "." && p != ""; p = path.Dir(p) {
		if ex.match(p) {
			return true
		}
	}
	return false
}

// fsTarEntry 是待打包的一个条目（相对源目录的路径，`/` 分隔）。
type fsTarEntry struct {
	rel  string
	info fs.FileInfo
}

func (r *Runtime) handleFsTarGet(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	_, real, err := r.tarPath(q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	ex, err := parseExcludes(q["exclude"])
	if err != nil {
		writeErr(w, err)
		return
	}
	gitignore, err := boolQuery(req, "gitignore", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := os.Stat(real)
	if err != nil {
		writeErr(w, fsError(err))
		return
	}
	var (
		kind    string
		entries []fsTarEntry
	)
	switch {
	case st.Mode().IsRegular():
		if gitignore {
			writeErr(w, badRequest("gitignore only applies to a directory"))
			return
		}
		kind, entries = tarKindFile, []fsTarEntry{{rel: filepath.Base(real), info: st}}
	case st.IsDir():
		kind = tarKindDir
		if gitignore {
			entries, err = r.gitTarEntries(req.Context(), real, ex)
		} else {
			entries, err = walkTarEntries(real, ex)
		}
		if err != nil {
			writeErr(w, err)
			return
		}
	default:
		writeErr(w, badRequest("%s is neither a regular file nor a directory", real))
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set(tarKindHeader, kind)
	w.WriteHeader(http.StatusOK)
	base := real
	if kind == tarKindFile {
		base = filepath.Dir(real)
	}
	tw := tar.NewWriter(w)
	for _, e := range entries {
		if err := writeTarEntry(tw, base, e); err != nil {
			// 响应头已发出：中止连接，让对端看到不完整的流而不是一个「成功」的残缺 tar。
			r.log.Warn("fs/tar: stream aborted", "path", real, "entry", e.rel, "err", err)
			panic(http.ErrAbortHandler)
		}
	}
	if err := tw.Close(); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// walkTarEntries 列出目录下的条目（不含根本身，父目录先于子项）：目录、普通文件与符号链接（不跟随），其余类型跳过；
// 被排除的目录连同子树跳过。
func walkTarEntries(root string, ex tarExcluder) ([]fsTarEntry, error) {
	var out []fsTarEntry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // 读不了的子项跳过
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if ex.match(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		t := d.Type()
		if !t.IsDir() && !t.IsRegular() && t&fs.ModeSymlink == 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, fsTarEntry{rel: rel, info: info})
		return nil
	})
	if err != nil {
		return nil, fsError(err)
	}
	return out, nil
}

// gitTarEntries 按 .gitignore 列出目录下的条目：git 跟踪的与未被忽略的未跟踪文件（仍存在的普通文件与符号链接）
// 及其父目录；是目录的条目（子模块）跳过；之后再应用 exclude。.git/ 因此不在其中。
func (r *Runtime) gitTarEntries(ctx context.Context, dir string, ex tarExcluder) ([]fsTarEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := r.bins.command(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil, errf(http.StatusBadRequest, "not_a_git_worktree", "%s is not inside a git work tree", dir)
	}
	cmd := r.bins.command(ctx, "git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err = cmd.Output()
	if err != nil {
		return nil, errf(http.StatusInternalServerError, "internal", "git ls-files: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var files []string
	for name := range strings.SplitSeq(string(out), "\x00") {
		if name != "" {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	files = slices.Compact(files)
	var entries []fsTarEntry
	seenDir := map[string]bool{}
	for _, rel := range files {
		if ex.matchAny(rel) {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || !(info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0) {
			continue
		}
		// 父目录（由浅到深）先于文件
		var parents []string
		for p := path.Dir(rel); p != "." && !seenDir[p]; p = path.Dir(p) {
			parents = append(parents, p)
		}
		for _, p := range slices.Backward(parents) {
			seenDir[p] = true
			if di, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p))); err == nil && di.IsDir() {
				entries = append(entries, fsTarEntry{rel: p, info: di})
			}
		}
		entries = append(entries, fsTarEntry{rel: rel, info: info})
	}
	return entries, nil
}

// writeTarEntry 写出一个条目；普通文件的内容从 base/rel 读取。
func writeTarEntry(tw *tar.Writer, base string, e fsTarEntry) error {
	m := e.info.Mode()
	hdr := &tar.Header{Name: e.rel, Mode: int64(m.Perm()), ModTime: e.info.ModTime()}
	full := filepath.Join(base, filepath.FromSlash(e.rel))
	switch {
	case m.IsDir():
		hdr.Typeflag, hdr.Name = tar.TypeDir, e.rel+"/"
	case m&fs.ModeSymlink != 0:
		target, err := os.Readlink(full)
		if err != nil {
			return err
		}
		hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, target
	default:
		hdr.Typeflag, hdr.Size = tar.TypeReg, e.info.Size()
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(tw, f, hdr.Size)
	return err
}

// tarPutError 把 PUT /fs/tar 读写过程中的错误映射为 apiError：超过上限 → 413 transfer_too_large，
// 包本身损坏 → 400 invalid_request，其余按文件系统错误。
func tarPutError(err error) error {
	var mbe *http.MaxBytesError
	var ae *apiError
	switch {
	case errors.As(err, &mbe):
		return errf(http.StatusRequestEntityTooLarge, "transfer_too_large", "transfer exceeds %d bytes", mbe.Limit)
	case errors.As(err, &ae):
		return ae
	case errors.Is(err, tar.ErrHeader), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, tar.ErrInsecurePath):
		return badRequest("read archive: %v", err)
	}
	return fsError(err)
}

func (r *Runtime) handleFsTarPut(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	kind := q.Get("kind")
	if kind != tarKindFile && kind != tarKindDir {
		writeErr(w, badRequest("kind must be file or dir"))
		return
	}
	abs, real, err := r.tarPath(q.Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	body := http.MaxBytesReader(w, req.Body, r.maxTarBytes)
	var res apigen.OrbFsTarResult
	if kind == tarKindFile {
		res, err = r.putTarFile(body, abs, real)
	} else {
		res, err = putTarDir(body, real)
	}
	if err == nil {
		// 读完 tar 结尾之后的填充
		_, err = io.Copy(io.Discard, body)
	}
	if err != nil {
		writeErr(w, tarPutError(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// putTarFile 把包里第一个普通文件覆盖写到 dst（父目录自动创建）；其余条目计入 skipped。
func (r *Runtime) putTarFile(body io.Reader, abs, real string) (apigen.OrbFsTarResult, error) {
	var res apigen.OrbFsTarResult
	if st, err := os.Stat(real); err == nil && st.IsDir() {
		return res, errf(http.StatusConflict, "is_a_directory", "%s is a directory", abs)
	}
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return res, err
	}
	tr := tar.NewReader(body)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, err
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if hdr.Typeflag != tar.TypeReg || res.Files > 0 {
			res.Skipped++
			continue
		}
		tmp, err := r.receiveFile(tr, "tar-", nil)
		if err != nil {
			return res, err
		}
		if err := os.Chmod(tmp, os.FileMode(hdr.Mode&0o777)); err != nil {
			_ = os.Remove(tmp)
			return res, err
		}
		if err := commitFile(tmp, real, false); err != nil {
			_ = os.Remove(tmp)
			return res, err
		}
		res.Files, res.Bytes = 1, hdr.Size
	}
	if res.Files == 0 {
		return res, badRequest("the archive has no regular file")
	}
	return res, nil
}

// putTarDir 把包解进目录 dst（不存在则创建，已存在则合并、同名文件覆盖）。全部写入经 os.Root（拒绝解析到根之外，
// 包括经由包里先放进来的符号链接）。指向根之外的符号链接、硬链接与其余类型计入 skipped。
// 目录的权限在全部条目写完后再设，免得只读目录挡住其中文件的写入。
func putTarDir(body io.Reader, dst string) (apigen.OrbFsTarResult, error) {
	var res apigen.OrbFsTarResult
	if st, err := os.Stat(dst); err == nil && !st.IsDir() {
		return res, errf(http.StatusConflict, "not_a_directory", "%s is not a directory", dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return res, err
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return res, err
	}
	defer root.Close()
	dirModes := map[string]os.FileMode{}
	mkParent := func(name string) error {
		if d := path.Dir(name); d != "." {
			return root.MkdirAll(d, 0o755)
		}
		return nil
	}
	// replace 删除挡在 name 上的非目录条目（文件或符号链接），返回 name 处是否是目录。
	replace := func(name string) (isDir bool, err error) {
		fi, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if fi.IsDir() {
			return true, nil
		}
		return false, root.Remove(name)
	}
	tr := tar.NewReader(body)
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, err
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if entries++; entries > maxTarEntries {
			return res, errf(http.StatusRequestEntityTooLarge, "transfer_too_large", "archive has more than %d entries", maxTarEntries)
		}
		name, ok := archiveName(hdr.Name)
		if !ok {
			return res, badRequest("entry %q: invalid path", hdr.Name)
		}
		if name == "" {
			continue // `.` 本身
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if isDir, err := replace(name); err != nil {
				return res, err
			} else if !isDir {
				if err := root.MkdirAll(name, 0o755); err != nil {
					return res, err
				}
			}
			dirModes[name] = os.FileMode(hdr.Mode & 0o777)
		case tar.TypeReg:
			if err := mkParent(name); err != nil {
				return res, err
			}
			if isDir, err := replace(name); err != nil {
				return res, err
			} else if isDir {
				return res, errf(http.StatusConflict, "is_a_directory", "entry %q: %s is a directory", hdr.Name, name)
			}
			mode := os.FileMode(hdr.Mode & 0o777)
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return res, err
			}
			n, err := io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = root.Chmod(name, mode)
			}
			if err != nil {
				return res, err
			}
			res.Files++
			res.Bytes += n
		case tar.TypeSymlink:
			target := path.Join(path.Dir(name), hdr.Linkname)
			if hdr.Linkname == "" || strings.ContainsRune(hdr.Linkname, 0) || path.IsAbs(hdr.Linkname) ||
				target == ".." || strings.HasPrefix(target, "../") {
				res.Skipped++
				continue
			}
			if err := mkParent(name); err != nil {
				return res, err
			}
			if isDir, err := replace(name); err != nil {
				return res, err
			} else if isDir {
				res.Skipped++
				continue
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return res, err
			}
		default:
			res.Skipped++
		}
	}
	// 由深到浅设目录权限（字典序倒序时子目录先于父目录）
	names := slices.Sorted(maps.Keys(dirModes))
	for _, n := range slices.Backward(names) {
		if err := root.Chmod(n, dirModes[n]); err != nil && !errors.Is(err, syscall.ENOENT) {
			return res, err
		}
	}
	return res, nil
}
