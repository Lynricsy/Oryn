package orbdrt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// M13 全局助手文件库（计划 4.3 / 4.6）。
//
// 协调宿主：library.sync 按清单整理 ~/library/{shared,threads}（删掉清单外与内容不符的一切），回报缺失的文件，
// 控制面再经 library 流逐个推送；library.fetch 只服务 ~/library/out/ 下的普通文件（助手唯一可写的目录）。
// 普通 orbd：orb 内 `oryn library put` 经 orbd.sock 转成 ctl library.put，控制面随即对同一 orb 发 library.fetch
// 取回该路径——orbd 只服务正在经 library.put 存入的路径，控制面借不到 orb 内的其它文件。

const (
	// libraryMaxBytes 同控制面文件库的单文件上限（attachments.MaxBytes）。
	libraryMaxBytes = 100 << 20
	// libraryPutTimeout 是 library.put 转发的上限：控制面要在这次请求里经 library.fetch 取回全部字节。
	libraryPutTimeout = 10 * time.Minute
	// libraryPushTimeout 是单条同步流（读完字节、落盘、回应答）的上限。
	libraryPushTimeout = 10 * time.Minute
	// libraryReplyMax 是同步流应答行的上限。
	libraryReplyMax = 4 << 10
)

var libFileIDRe = regexp.MustCompile(`^afl_[0-9A-Z]{26}$`)

// libraryState 是文件库在本 orbd 上的状态（零值可用）。
type libraryState struct {
	mu sync.Mutex
	// wanted 是最近一次 library.sync 的清单（相对 ~/library 的路径 → 条目）：只有其中的路径能经 library 流写入。
	wanted map[string]apigen.LinkCtlLibrarySyncFile
	// stamps 记下本进程写入或校验过的文件（相对路径 → 大小、修改时间与摘要），同步时未变的文件不再重算摘要。
	stamps map[string]libStamp
	// staged 是普通 orbd 上正在经 library.put 存入的绝对路径（→ 并发计数）。
	staged map[string]int
}

type libStamp struct {
	size  int64
	mtime time.Time
	sha   string
}

func (r *Runtime) libraryRoot() string { return filepath.Join(r.home, "library") }

// libraryRel 校验同步清单里的沙箱路径，返回相对 ~/library 的路径：shared/<fileId>/<name> 或
// threads/<threadId>/<fileId>/<name>（name 为单个路径段）。
func libraryRel(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, "~/library/")
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 3 && parts[0] == "shared" && libFileIDRe.MatchString(parts[1]) && libName(parts[2]):
	case len(parts) == 4 && parts[0] == "threads" && validThreadID(parts[1]) && libFileIDRe.MatchString(parts[2]) && libName(parts[3]):
	default:
		return "", false
	}
	return rest, true
}

func libName(n string) bool {
	return n != "" && n != "." && n != ".." && len(n) <= 255 && !strings.ContainsAny(n, "/\x00")
}

// librarySync 是协调宿主的 library.sync：记下清单，删掉 shared/ 与 threads/ 下清单外或内容不符的一切，回报缺失的路径。
func (r *Runtime) librarySync(p apigen.LinkCtlLibrarySyncParams) (apigen.LinkCtlLibrarySyncResult, error) {
	wanted := make(map[string]apigen.LinkCtlLibrarySyncFile, len(p.Files))
	for _, f := range p.Files {
		rel, ok := libraryRel(f.Path)
		if !ok || !sha256Re.MatchString(f.Sha256) || f.Size < 0 || f.Size > libraryMaxBytes {
			return apigen.LinkCtlLibrarySyncResult{}, ctlErr(link.CodeInvalidParams, "invalid library entry %q", f.Path)
		}
		wanted[rel] = f
	}
	root := r.libraryRoot()
	if err := os.MkdirAll(filepath.Join(root, "out"), 0o700); err != nil {
		return apigen.LinkCtlLibrarySyncResult{}, ctlErr("failed", "%v", err)
	}
	r.lib.mu.Lock()
	defer r.lib.mu.Unlock()
	r.lib.wanted = wanted
	if r.lib.stamps == nil {
		r.lib.stamps = map[string]libStamp{}
	}
	present := map[string]bool{}
	for _, top := range []string{"shared", "threads"} {
		if err := r.pruneLibrary(root, top, wanted, present); err != nil {
			return apigen.LinkCtlLibrarySyncResult{}, ctlErr("failed", "%v", err)
		}
	}
	missing := []string{}
	for rel, f := range wanted {
		if !present[rel] {
			missing = append(missing, f.Path)
		}
	}
	slices.Sort(missing)
	return apigen.LinkCtlLibrarySyncResult{Missing: missing}, nil
}

// pruneLibrary 遍历 root/top：清单里且内容一致的文件记入 present，其余文件、符号链接与特殊文件删除，最后删掉空目录。
// 调用方持有 r.lib.mu。
func (r *Runtime) pruneLibrary(root, top string, wanted map[string]apigen.LinkCtlLibrarySyncFile, present map[string]bool) error {
	base := filepath.Join(root, top)
	var dirs []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if path != base {
				dirs = append(dirs, path)
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if f, ok := wanted[rel]; ok && d.Type().IsRegular() && r.libraryMatches(path, rel, f) {
			present[rel] = true
			return nil
		}
		delete(r.lib.stamps, rel)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	// 由深到浅删空目录（非空的删不掉，忽略）。
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
	return nil
}

// libraryMatches 报告 path 的内容是否与清单条目一致；大小与修改时间都没变时沿用记下的摘要。调用方持有 r.lib.mu。
func (r *Runtime) libraryMatches(path, rel string, f apigen.LinkCtlLibrarySyncFile) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != f.Size {
		return false
	}
	if st, ok := r.lib.stamps[rel]; ok && st.size == fi.Size() && st.mtime.Equal(fi.ModTime()) {
		return st.sha == f.Sha256
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return false
	}
	r.lib.stamps[rel] = libStamp{size: fi.Size(), mtime: fi.ModTime(), sha: sum}
	return sum == f.Sha256
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// serveLibraryPush 接收控制面推送的一个文件（同步方向的 library 流）：路径须在最近一次清单里、字节数与摘要须一致；
// 原子落盘（0444）后回一行 LinkLibraryReply。
func (r *Runtime) serveLibraryPush(st *link.Stream) {
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(libraryPushTimeout))
	reply := func(code, msg string) {
		rep := apigen.LinkLibraryReply{Ok: code == ""}
		if code != "" {
			rep.Code, rep.Message = &code, &msg
			r.log.Warn("library push rejected", "path", st.Header.Path, "code", code, "err", msg)
		}
		b, _ := json.Marshal(rep)
		_, _ = st.Write(append(b, '\n'))
	}
	h := st.Header
	rel, ok := libraryRel(h.Path)
	r.lib.mu.Lock()
	f, want := r.lib.wanted[rel]
	r.lib.mu.Unlock()
	if !ok || !want || f.Size != h.Size || f.Sha256 != h.SHA256 {
		reply("invalid_path", "path is not in the current library manifest")
		return
	}
	root := r.libraryRoot()
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := r.libraryMkdir(root, filepath.Dir(dst)); err != nil {
		reply("failed", err.Error())
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".orbd-lib-*")
	if err != nil {
		reply("failed", err.Error())
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(st, f.Size))
	if err != nil {
		reply("failed", err.Error())
		return
	}
	if n != f.Size || hex.EncodeToString(sum.Sum(nil)) != f.Sha256 {
		reply("mismatch", "size or sha256 does not match the manifest")
		return
	}
	if err := tmp.Chmod(0o444); err != nil {
		reply("failed", err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		reply("failed", err.Error())
		return
	}
	r.lib.mu.Lock()
	cur, still := r.lib.wanted[rel]
	if !still || cur != f {
		r.lib.mu.Unlock()
		reply("invalid_path", "the library manifest changed during the transfer")
		return
	}
	err = os.Rename(tmp.Name(), dst)
	if err == nil {
		keep = true
		if fi, serr := os.Lstat(dst); serr == nil {
			r.lib.stamps[rel] = libStamp{size: fi.Size(), mtime: fi.ModTime(), sha: f.Sha256}
		}
	}
	r.lib.mu.Unlock()
	if err != nil {
		reply("failed", err.Error())
		return
	}
	reply("", "")
}

// libraryMkdir 建出 dir（root 之下），并确认解析符号链接后它仍在 root 之下。
func (r *Runtime) libraryMkdir(root, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(real+string(filepath.Separator), realRoot+string(filepath.Separator)) {
		return errors.New("library directory escapes ~/library")
	}
	return nil
}

// libraryFetchPath 解析协调宿主上 library.fetch 的路径：`~/…`、绝对路径或相对 ~/library/out/ 的路径；
// 含 `..` 段、解析符号链接后既不在 ~/library/out/ 也不在 ~/tmp/（协调档 omp 的 TMPDIR，M19：generate_image
// 的结果在这里）之下、或不是普通文件一律拒绝。返回解析后的真实路径。
func (r *Runtime) libraryFetchPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", ctlErr(link.CodeInvalidParams, "path is required")
	}
	if slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..") {
		return "", ctlErr(link.CodeInvalidParams, "path must not contain ..")
	}
	out := filepath.Join(r.libraryRoot(), "out")
	tmp := filepath.Join(r.home, "tmp")
	for _, d := range []string{out, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", ctlErr("failed", "%v", err)
		}
	}
	var abs string
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"), filepath.IsAbs(p):
		abs = r.resolvePath(p)
	default:
		abs = filepath.Join(out, p)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ctlErr("not_found", "%s does not exist", p)
		}
		return "", ctlErr(link.CodeInvalidParams, "%v", err)
	}
	for _, root := range []string{out, tmp} {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", ctlErr("failed", "%v", err)
		}
		if strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", ctlErr(link.CodeInvalidParams, "only files under ~/library/out/ or ~/tmp/ can be saved")
}

// libraryFetch 是 library.fetch：校验路径后在同一 link 上另开 library 流发送原始字节（流头带请求的 id）。
func (r *Runtime) libraryFetch(ctx context.Context, c *conn, p apigen.LinkCtlLibraryFetchParams) (apigen.LinkCtlLibraryFetchResult, error) {
	if p.ID == "" || len(p.ID) > 128 || strings.ContainsAny(p.ID, "\n\x00") {
		return apigen.LinkCtlLibraryFetchResult{}, ctlErr(link.CodeInvalidParams, "invalid id")
	}
	var path string
	if r.cfg.Coordinator {
		var err error
		if path, err = r.libraryFetchPath(p.Path); err != nil {
			return apigen.LinkCtlLibraryFetchResult{}, err
		}
	} else {
		r.lib.mu.Lock()
		staged := r.lib.staged[p.Path] > 0
		r.lib.mu.Unlock()
		if !staged {
			return apigen.LinkCtlLibraryFetchResult{}, ctlErr("not_found", "%s is not being saved to the library", p.Path)
		}
		path = p.Path
	}
	f, err := os.Open(path)
	if err != nil {
		return apigen.LinkCtlLibraryFetchResult{}, ctlErr("not_found", "%v", err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return apigen.LinkCtlLibraryFetchResult{}, ctlErr("not_found", "%s is not a regular file", p.Path)
	}
	if fi.Size() > libraryMaxBytes {
		f.Close()
		return apigen.LinkCtlLibraryFetchResult{}, ctlErr(link.CodeInvalidParams, "%s exceeds %d bytes", p.Path, libraryMaxBytes)
	}
	octx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := c.sess.Open(octx, link.StreamHeader{T: link.StreamLibrary, ID: p.ID, Size: fi.Size()})
	cancel()
	if err != nil {
		f.Close()
		return apigen.LinkCtlLibraryFetchResult{}, ctlErr("failed", "open library stream: %v", err)
	}
	go func() {
		defer f.Close()
		defer st.Close()
		if _, err := io.CopyN(st, f, fi.Size()); err != nil {
			r.log.Warn("library stream failed", "path", p.Path, "err", err)
		}
	}()
	return apigen.LinkCtlLibraryFetchResult{Size: fi.Size()}, nil
}

// libraryPut 处理 library.put：orb 的 orbd.sock（`oryn library put`），以及 runner 的 runner.sock（M21，扩展的
// oryn_library_put 工具）。校验文件后只把这一条路径登记为可取回，再转成 ctl library.put；控制面在这次请求里经
// library.fetch 取回字节，结束后撤销登记。runner 上另要求文件位于调用方线程的工作目录之内（见 runnerLibraryPath）。
func (r *Runtime) libraryPut(ctx context.Context, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlLibraryPutParams
	if err := decodeSock(params, &p); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(p.Path) {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "path must be absolute")
	}
	p.Path = filepath.Clean(p.Path)
	if r.runner != nil {
		real, err := r.runnerLibraryPath(p)
		if err != nil {
			return nil, err
		}
		p.Path = real
	}
	fi, err := os.Stat(p.Path)
	if err != nil {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "%v", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "%s is not a regular file", p.Path)
	}
	if fi.Size() > libraryMaxBytes {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "%s exceeds %d bytes", p.Path, libraryMaxBytes)
	}
	defer r.stageLibraryPaths([]string{p.Path})()
	return r.ctlForwardFor(ctx, apigen.MethodLibraryPut, p, libraryPutTimeout)
}

// runnerLibraryPath 校验 runner 上 library.put 的路径（已是 Clean 过的绝对路径）：本身是普通文件（不是符号链接或目录），
// 且解析符号链接后严格位于调用方线程最近一次启动 agent 的工作目录之内。返回解析后的真实路径（登记与取回都用它）。
func (r *Runtime) runnerLibraryPath(p apigen.LinkCtlLibraryPutParams) (string, error) {
	if !validThreadID(p.ThreadID) {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "invalid threadId %q", p.ThreadID)
	}
	t := r.lookupThread(p.ThreadID)
	cwd := ""
	if t != nil {
		cwd = t.cwd()
	}
	if cwd == "" {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "thread %s has no agent on this runner", p.ThreadID)
	}
	fi, err := os.Lstat(p.Path)
	if err != nil {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%v", err)
	}
	if !fi.Mode().IsRegular() {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%s is not a regular file (directories must be archived first; give the real path instead of a symlink)", p.Path)
	}
	real, err := filepath.EvalSymlinks(p.Path)
	if err != nil {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%v", err)
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%v", err)
	}
	if real == root || !underDir(real, root) {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "only files inside the thread's working directory %s can be saved", cwd)
	}
	return real, nil
}
