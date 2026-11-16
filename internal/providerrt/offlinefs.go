package providerrt

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 停机 orb 只读预览（offline fs）：provider 长轮询 `/provider/v1/fs/claim` 取得请求，由驱动读取停机 orb 的文件，
// 再把用户响应体流式 POST 到 `/provider/v1/fs/{id}/reply`（状态码经 X-Oryn-Status 给出）。
// 本文件含协议、runner 侧 worker，以及与驱动无关的索引 / 路径 / 列表 / Range 逻辑（便于单测）；
// 驱动只负责建索引与读文件（offlineBackend），docker 见 offlinefs_docker.go，firecracker 见 offlinefs_fc.go。

// OfflineFS 是驱动可选实现的停机 orb 只读文件访问；实现它的驱动应在 Info 中声明 Capabilities.OfflineFs。
type OfflineFS interface {
	// OfflineFS 处理一个请求。ctx 是本次请求（含回传应答体）的上下文，结束时驱动须终止为它启动的进程；
	// life 是 provider 的生命周期，供不随单个请求结束的后台工作（建索引）使用。
	OfflineFS(ctx, life context.Context, req apigen.ProviderFsRequest) FsReply
}

// FsReply 是一次只读文件请求的应答：Status 为用户请求的 HTTP 状态码，Body 为用户响应体（流式；可为 nil）。
type FsReply struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
	// Length 是 Body 的字节数；-1 = 未知（回传时用 chunked）。
	Length int64
}

// fsWorkers 是并行处理只读文件请求的 worker 数。
const fsWorkers = 4

const (
	// offlineListMax 是目录列表上限（同 orbd）。
	offlineListMax = 5000
	// offlineSniffBytes 是 stat 判断 mime / 文本读取的前缀长度（同 orbd）。
	offlineSniffBytes = 8 << 10
	// offlineMaxHops 是解析路径时最多跟随的符号链接数。
	offlineMaxHops = 40
	// offlineRetryAfter 是索引未就绪时 503 的 Retry-After 秒数。
	offlineRetryAfter = "2"
)

// 可调参数（测试替换）。
var (
	// offlineIndexMaxEntries 是单个索引的条目上限；超出后停止扫描并标记 truncated。
	offlineIndexMaxEntries = 500_000
	// offlineIndexCacheSize 是保留的索引份数（LRU，按 orb）。
	offlineIndexCacheSize = 4
	// offlineIndexErrTTL 是建索引失败结果的缓存时长（期间直接回 500，不反复重建）。
	offlineIndexErrTTL = 30 * time.Second
	// offlineIndexTimeout 是单次建索引的时限。
	offlineIndexTimeout = 10 * time.Minute
	// offlineIndexWait 是请求触发建索引后最多等待它完成的时长；仍未完成则回 503 offline_indexing。
	offlineIndexWait = 2 * time.Second
)

// ---- 错误与应答 ----

// fsErr 是带用户状态码与错误码的失败（应答体为 apigen.Error）。
type fsErr struct {
	status int
	code   string
	msg    string
}

func (e *fsErr) Error() string { return e.code + ": " + e.msg }

func fsErrf(status int, code, format string, args ...any) *fsErr {
	return &fsErr{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

// jsonReply 构造 JSON 应答。
func jsonReply(status int, v any) FsReply {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"code":"internal","message":"encode reply"}`)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return FsReply{Status: status, Header: h, Body: io.NopCloser(bytes.NewReader(b)), Length: int64(len(b))}
}

// errReply 把错误转为 apigen.Error 应答；非 fsErr 一律 500 internal。
func errReply(err error) FsReply {
	var fe *fsErr
	if !errors.As(err, &fe) {
		fe = fsErrf(http.StatusInternalServerError, "internal", "%v", err)
	}
	return jsonReply(fe.status, apigen.Error{Code: fe.code, Message: fe.msg})
}

// ---- 内存索引 ----

// fsNode 是索引里的一个条目（Lstat 语义）。
type fsNode struct {
	typ   apigen.FsEntryType
	size  int64
	mode  int
	mtime time.Time
	// target 是符号链接原文。
	target string
	// children 只对目录非空。
	children map[string]*fsNode
	// ref 是驱动读取该文件所需的引用（firecracker：fcFileRef；docker 按路径读取，不用）。
	ref any
}

// offlineBackend 是停机预览中与驱动相关的两步：读取文件前缀（stat 补 mime / text）与流式读取文件（单区间 Range）。
// real 是解析符号链接后的绝对路径，n 是它在索引里的条目（普通文件）。
type offlineBackend interface {
	sniff(ctx context.Context, real string, n *fsNode) (mime string, text bool, err error)
	read(ctx context.Context, real string, n *fsNode, rangeHdr string) FsReply
}

// indexErrReply 把取索引的失败转为应答；索引仍在建（503 offline_indexing）时带 Retry-After。
func indexErrReply(err error) FsReply {
	rep := errReply(err)
	if rep.Status == http.StatusServiceUnavailable {
		rep.Header.Set("Retry-After", offlineRetryAfter)
	}
	return rep
}

// serveOffline 用已建好的索引回答一个请求：roots / list 只查索引，stat 必要时经 b 嗅探前缀，read 经 b 读取。
func serveOffline(ctx context.Context, ix *fsIndex, req apigen.ProviderFsRequest, b offlineBackend) FsReply {
	p := ""
	if req.Path != nil {
		p = *req.Path
	}
	switch req.Op {
	case apigen.Roots:
		return jsonReply(http.StatusOK, ix.roots(req.Workdirs))
	case apigen.List:
		res, err := ix.list(p)
		if err != nil {
			return errReply(err)
		}
		return jsonReply(http.StatusOK, res)
	case apigen.Stat:
		e, sniff, err := ix.stat(p)
		if err != nil {
			return errReply(err)
		}
		if sniff != "" {
			n, _ := ix.lookup(sniff)
			if m, text, err := b.sniff(ctx, sniff, n); err == nil {
				e.Mime, e.Text = &m, &text
			}
		}
		return jsonReply(http.StatusOK, e)
	case apigen.Read:
		_, real, err := ix.readTarget(p)
		if err != nil {
			return errReply(err)
		}
		n, _ := ix.lookup(real)
		rng := ""
		if req.Range != nil {
			rng = *req.Range
		}
		return b.read(ctx, real, n, rng)
	default:
		return errReply(fsErrf(http.StatusBadRequest, "invalid_request", "unknown op %q", req.Op))
	}
}

// rangeReply 按 Range 头构造 read 的应答头；Range 不合法时返回 416 应答与 ok=false。etag 与 entry 的 Etag 一致。
func rangeReply(rangeHdr string, size int64, mtime time.Time) (rep FsReply, start, length int64, ok bool) {
	start, length, partial, ok := parseRange(rangeHdr, size)
	if !ok {
		rep = errReply(fsErrf(http.StatusRequestedRangeNotSatisfiable, "range_not_satisfiable", "invalid range %q for %d bytes", rangeHdr, size))
		rep.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		return rep, 0, 0, false
	}
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Accept-Ranges", "bytes")
	h.Set("ETag", fmt.Sprintf(`"%d-%d"`, mtime.UnixNano(), size))
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	status := http.StatusOK
	if partial {
		status = http.StatusPartialContent
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
	}
	return FsReply{Status: status, Header: h, Length: length}, start, length, true
}

// fsIndex 是 OrbHome 子树（不含 .oryn）的只读快照。
type fsIndex struct {
	root      *fsNode
	count     int
	truncated bool
}

// ancestorNode 代表 OrbHome 的上级目录（/、/home）：解析 `..` 时可以经过，但内容未知。
var ancestorNode = &fsNode{typ: apigen.FsEntryTypeDir, mode: 0o755}

func newDirNode() *fsNode {
	return &fsNode{typ: apigen.FsEntryTypeDir, mode: 0o755, children: map[string]*fsNode{}}
}

// tarRel 把 `docker cp <orb>:/home/user -` 的条目名（以源目录基名开头，如 `user/a/b`）转为相对 OrbHome 的路径；
// 根条目返回 "."；非法名（`..` 逃逸）返回 ok=false。
func tarRel(name string) (string, bool) {
	name = strings.Trim(strings.TrimPrefix(name, "./"), "/")
	if name == "" {
		return "", false
	}
	_, rest, found := strings.Cut(name, "/")
	if !found || rest == "" {
		return ".", true
	}
	rel := path.Clean(rest)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// isOrynRel 判断相对 OrbHome 的路径是否落在 .oryn 子树里。
func isOrynRel(rel string) bool { return rel == ".oryn" || strings.HasPrefix(rel, ".oryn/") }

// buildIndex 读取 tar 流（只读头部、丢弃内容）建立索引；scanned 每处理一个条目回调一次（进度）。
// 条目数达到 offlineIndexMaxEntries 时停止读取并标记 truncated（调用方负责结束产生 tar 流的进程）。
func buildIndex(r io.Reader, scanned func(n int)) (*fsIndex, error) {
	ix := &fsIndex{root: newDirNode()}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return ix, nil
		}
		if err != nil {
			return nil, fmt.Errorf("读取 tar 流: %w", err)
		}
		rel, ok := tarRel(hdr.Name)
		if !ok || isOrynRel(rel) {
			continue
		}
		n := &fsNode{size: hdr.Size, mode: int(hdr.Mode & 0o777), mtime: hdr.ModTime}
		switch hdr.Typeflag {
		case tar.TypeDir:
			n.typ, n.size, n.children = apigen.FsEntryTypeDir, 0, map[string]*fsNode{}
		case tar.TypeReg, tar.TypeGNUSparse, '\x00':
			n.typ = apigen.FsEntryTypeFile
		case tar.TypeLink:
			// 硬链接按普通文件处理；大小取被链接的条目（tar 里硬链接条目本身不带内容）。
			n.typ = apigen.FsEntryTypeFile
			if lrel, ok := tarRel(hdr.Linkname); ok {
				if ln, _ := ix.lookup(path.Join(OrbHome, lrel)); ln != nil && ln.typ == apigen.FsEntryTypeFile {
					n.size = ln.size
				}
			}
		case tar.TypeSymlink:
			n.typ, n.target, n.size = apigen.FsEntryTypeSymlink, hdr.Linkname, int64(len(hdr.Linkname))
		default:
			n.typ, n.size = apigen.FsEntryTypeOther, 0
		}
		if !ix.put(rel, n) {
			ix.truncated = true
			return ix, nil
		}
		if scanned != nil {
			scanned(ix.count)
		}
	}
}

// put 把条目放到 rel 处（缺失的上级目录隐式创建）；达到条目上限时返回 false。
func (ix *fsIndex) put(rel string, n *fsNode) bool {
	if rel == "." {
		if n.typ == apigen.FsEntryTypeDir {
			ix.root.mode, ix.root.mtime = n.mode, n.mtime
		}
		return true
	}
	parts := strings.Split(rel, "/")
	cur := ix.root
	for _, p := range parts[:len(parts)-1] {
		next := cur.children[p]
		if next == nil || next.typ != apigen.FsEntryTypeDir {
			if next == nil {
				if ix.count >= offlineIndexMaxEntries {
					return false
				}
				ix.count++
			}
			next = newDirNode()
			cur.children[p] = next
		}
		cur = next
	}
	name := parts[len(parts)-1]
	old := cur.children[name]
	if old == nil {
		if ix.count >= offlineIndexMaxEntries {
			return false
		}
		ix.count++
	} else if old.typ == apigen.FsEntryTypeDir && n.typ == apigen.FsEntryTypeDir {
		// 目录条目晚于其内容出现：保留已有子项，只更新属性。
		old.mode, old.mtime = n.mode, n.mtime
		return true
	}
	cur.children[name] = n
	return true
}

// lookup 按已解析（不含符号链接）的绝对路径取条目。known=false 表示路径在 OrbHome 之外且不是它的上级目录（内容未知）。
func (ix *fsIndex) lookup(p string) (n *fsNode, known bool) {
	if p == OrbHome {
		return ix.root, true
	}
	if rel, ok := strings.CutPrefix(p, OrbHome+"/"); ok {
		n := ix.root
		for c := range strings.SplitSeq(rel, "/") {
			if n = n.children[c]; n == nil {
				return nil, true
			}
		}
		return n, true
	}
	if p == "/" || strings.HasPrefix(OrbHome, strings.TrimSuffix(p, "/")+"/") {
		return ancestorNode, true
	}
	return nil, false
}

// inHome 判断已解析的绝对路径是否为 OrbHome 本身或在其下。
func inHome(p string) bool { return p == OrbHome || strings.HasPrefix(p, OrbHome+"/") }

// homeRel 返回 OrbHome 下路径的相对形式（OrbHome 本身为 "."）；不在其下时 ok=false。
func homeRel(p string) (string, bool) {
	if p == OrbHome {
		return ".", true
	}
	rel, ok := strings.CutPrefix(p, OrbHome+"/")
	return rel, ok
}

func outsideHome(p string) *fsErr {
	return fsErrf(http.StatusForbidden, "outside_home", "path %q is outside %s", p, OrbHome)
}

func protectedPath(p string) *fsErr {
	return fsErrf(http.StatusForbidden, "protected_path", "%q is managed by Oryn and not readable offline", p)
}

func notFound(p string) *fsErr {
	return fsErrf(http.StatusNotFound, "not_found", "%s: no such file or directory", p)
}

// eval 在索引里逐段解析 abs（已 Clean 的绝对路径）的符号链接（链接目标按容器内路径解释，最多 offlineMaxHops 跳），
// 返回真实路径与条目。经过 OrbHome 之外（上级目录除外）→ outside_home；经过 .oryn → protected_path。
func (ix *fsIndex) eval(abs string) (string, *fsNode, error) {
	comps := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	cur, node, hops := "/", ancestorNode, 0
	for len(comps) > 0 {
		c := comps[0]
		comps = comps[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = path.Dir(cur)
			node, _ = ix.lookup(cur)
			continue
		}
		next := path.Join(cur, c)
		if rel, ok := homeRel(next); ok && isOrynRel(rel) {
			return "", nil, protectedPath(abs)
		}
		n, known := ix.lookup(next)
		if !known {
			return "", nil, outsideHome(abs)
		}
		if n == nil {
			return "", nil, notFound(abs)
		}
		if n.typ == apigen.FsEntryTypeSymlink {
			if hops++; hops > offlineMaxHops {
				return "", nil, fsErrf(http.StatusBadRequest, "invalid_path", "%s: too many levels of symbolic links", abs)
			}
			tc := strings.Split(n.target, "/")
			if strings.HasPrefix(n.target, "/") {
				cur = "/"
			}
			comps = append(tc, comps...)
			continue
		}
		if len(comps) > 0 && n.typ != apigen.FsEntryTypeDir {
			return "", nil, fsErrf(http.StatusConflict, "not_a_directory", "%s: not a directory", abs)
		}
		cur, node = next, n
	}
	if !inHome(cur) {
		return "", nil, outsideHome(abs)
	}
	return cur, node, nil
}

// userPath 校验并展开请求路径（`~` / `~/…` / 绝对路径，Clean 之后）；词法上落在 .oryn 下 → protected_path。
func userPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fsErrf(http.StatusBadRequest, "invalid_path", "empty path")
	}
	if strings.ContainsRune(p, 0) {
		return "", fsErrf(http.StatusBadRequest, "invalid_path", "path contains NUL")
	}
	var abs string
	switch {
	case p == "~":
		abs = OrbHome
	case strings.HasPrefix(p, "~/"):
		abs = path.Join(OrbHome, p[2:])
	case path.IsAbs(p):
		abs = path.Clean(p)
	default:
		return "", fsErrf(http.StatusBadRequest, "invalid_path", "path must be absolute or start with ~/: %q", p)
	}
	if rel, ok := homeRel(abs); ok && isOrynRel(rel) {
		return "", protectedPath(p)
	}
	return abs, nil
}

// lstat 按 Lstat 语义取 abs 的条目（上级目录跟随符号链接，末段不跟随），同时要求 abs 完全解析后仍在 OrbHome 之下：
// 指向 OrbHome 之外 / .oryn 的链接 → 403；悬空链接照常返回链接本身。
// real 是完全解析后的路径（悬空时为空）。
func (ix *fsIndex) lstat(abs string) (n *fsNode, real string, err error) {
	lpath := OrbHome
	if abs != OrbHome {
		dir, pn, err := ix.eval(path.Dir(abs))
		if err != nil {
			if fe := (*fsErr)(nil); errors.As(err, &fe) && fe.code == "outside_home" {
				// 上级目录在 OrbHome 之外（例如 abs 就是 /home）：按 abs 本身报告。
				return nil, "", outsideHome(abs)
			}
			return nil, "", err
		}
		if pn.typ != apigen.FsEntryTypeDir {
			return nil, "", fsErrf(http.StatusConflict, "not_a_directory", "%s: not a directory", path.Dir(abs))
		}
		lpath = path.Join(dir, path.Base(abs))
	}
	if rel, ok := homeRel(lpath); ok && isOrynRel(rel) {
		return nil, "", protectedPath(abs)
	}
	n, known := ix.lookup(lpath)
	if !known || n == ancestorNode {
		return nil, "", outsideHome(abs)
	}
	if n == nil {
		return nil, "", notFound(abs)
	}
	if n.typ != apigen.FsEntryTypeSymlink {
		return n, lpath, nil
	}
	real, _, err = ix.eval(abs)
	if err != nil {
		var fe *fsErr
		if errors.As(err, &fe) && (fe.code == "outside_home" || fe.code == "protected_path") {
			return nil, "", err
		}
		return n, "", nil
	}
	return n, real, nil
}

// entry 由条目构造 FsEntry（Path 为词法路径 abs）；符号链接带 target 与（解析后在 OrbHome 内时的）targetType。
func (ix *fsIndex) entry(abs string, n *fsNode) apigen.FsEntry {
	e := apigen.FsEntry{
		Path:  abs,
		Name:  path.Base(abs),
		Size:  n.size,
		Mtime: n.mtime.UnixMilli(),
		Mode:  n.mode,
		Type:  n.typ,
	}
	switch n.typ {
	case apigen.FsEntryTypeFile:
		tag := fmt.Sprintf(`"%d-%d"`, n.mtime.UnixNano(), n.size)
		e.Etag = &tag
	case apigen.FsEntryTypeSymlink:
		target := n.target
		e.Target = &target
		if _, tn, err := ix.eval(abs); err == nil {
			var tt apigen.FsEntryTargetType
			switch tn.typ {
			case apigen.FsEntryTypeDir:
				tt = apigen.FsEntryTargetTypeDir
			case apigen.FsEntryTypeFile:
				tt = apigen.FsEntryTargetTypeFile
			}
			if tt != "" {
				e.TargetType = &tt
			}
		}
	}
	return e
}

// roots：工作目录取 workdirs 中第一个解析后为 OrbHome 内目录的候选；都不是时为 OrbHome。
func (ix *fsIndex) roots(workdirs []string) apigen.FsRoots {
	res := apigen.FsRoots{Home: OrbHome, Workdir: OrbHome}
	for _, w := range workdirs {
		abs, err := userPath(w)
		if err != nil {
			continue
		}
		if _, n, err := ix.eval(abs); err == nil && n.typ == apigen.FsEntryTypeDir {
			res.Workdir = abs
			break
		}
	}
	return res
}

// list 列出目录（跟随符号链接）：目录在前、按名不分大小写排序，至多 offlineListMax 条；索引被截断时 truncated 恒为 true。
func (ix *fsIndex) list(p string) (apigen.FsListing, error) {
	abs, err := userPath(p)
	if err != nil {
		return apigen.FsListing{}, err
	}
	_, n, err := ix.eval(abs)
	if err != nil {
		return apigen.FsListing{}, err
	}
	if n.typ != apigen.FsEntryTypeDir {
		return apigen.FsListing{}, fsErrf(http.StatusConflict, "not_a_directory", "%s is not a directory", abs)
	}
	entries := make([]apigen.FsEntry, 0, min(len(n.children), offlineListMax))
	for name, c := range n.children {
		entries = append(entries, ix.entry(path.Join(abs, name), c))
	}
	slices.SortFunc(entries, func(a, b apigen.FsEntry) int {
		if da, db := isDirLike(a), isDirLike(b); da != db {
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
	res := apigen.FsListing{Path: abs, Entries: entries, Truncated: ix.truncated}
	if len(entries) > offlineListMax {
		res.Entries, res.Truncated = entries[:offlineListMax], true
	}
	return res, nil
}

// isDirLike：目录或指向目录的符号链接（排序时排在前面）。
func isDirLike(e apigen.FsEntry) bool {
	return e.Type == apigen.FsEntryTypeDir || (e.TargetType != nil && *e.TargetType == apigen.FsEntryTargetTypeDir)
}

// stat 返回 Lstat 语义的条目；sniff 非空时是需要读取前缀补 mime / text 的真实文件路径（普通文件或指向普通文件的链接）。
func (ix *fsIndex) stat(p string) (e apigen.FsEntry, sniff string, err error) {
	abs, err := userPath(p)
	if err != nil {
		return e, "", err
	}
	n, real, err := ix.lstat(abs)
	if err != nil {
		return e, "", err
	}
	e = ix.entry(abs, n)
	if real != "" {
		if t, _ := ix.lookup(real); t != nil && t.typ == apigen.FsEntryTypeFile {
			sniff = real
		}
	}
	return e, sniff, nil
}

// readTarget 解析 read 的目标：最终必须是普通文件（目录 → 409 is_a_directory，其它 → 400 invalid_path）。
func (ix *fsIndex) readTarget(p string) (abs, real string, err error) {
	abs, err = userPath(p)
	if err != nil {
		return "", "", err
	}
	real, n, err := ix.eval(abs)
	if err != nil {
		return "", "", err
	}
	switch n.typ {
	case apigen.FsEntryTypeFile:
		return abs, real, nil
	case apigen.FsEntryTypeDir:
		return "", "", fsErrf(http.StatusConflict, "is_a_directory", "%s is a directory", abs)
	default:
		return "", "", fsErrf(http.StatusBadRequest, "invalid_path", "%s is not a regular file", abs)
	}
}

// sniffContent：mime 先按扩展名，缺省按内容嗅探；text 同 orbd（无 NUL 且为合法 UTF-8，读满时允许末尾截断的码点）。
func sniffContent(name string, b []byte, full bool) (string, bool) {
	m := mime.TypeByExtension(path.Ext(name))
	if m == "" {
		m = http.DetectContentType(b)
	}
	return m, isTextPrefix(b, full)
}

// isTextPrefix 同 internal/orbdrt 的 isText：无 NUL 且为合法 UTF-8；truncated 时允许末尾一个不完整的码点。
func isTextPrefix(b []byte, truncated bool) bool {
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

// parseRange 解析单区间 Range 头（`bytes=a-b` / `bytes=a-` / `bytes=-n`）。空头 → 整个文件（partial=false）；
// 语法错误、多区间或不可满足 → ok=false（416）。
func parseRange(h string, size int64) (start, length int64, partial, ok bool) {
	if h == "" {
		return 0, size, false, true
	}
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, false
	}
	a, b, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, 0, false, false
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" {
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 || size == 0 {
			return 0, 0, false, false
		}
		n = min(n, size)
		return size - n, n, true, true
	}
	s, err := strconv.ParseInt(a, 10, 64)
	if err != nil || s < 0 || s >= size {
		return 0, 0, false, false
	}
	end := size - 1
	if b != "" {
		e, err := strconv.ParseInt(b, 10, 64)
		if err != nil || e < s {
			return 0, 0, false, false
		}
		end = min(e, size-1)
	}
	return s, end - s + 1, true, true
}

// ---- 索引缓存 ----

// fsBuild 是一个 orb 的一次建索引（进行中或已完成）。
type fsBuild struct {
	key     string
	done    chan struct{}
	cancel  context.CancelFunc
	scanned atomic.Int64
	// 以下字段在 done 关闭后只读。
	ix  *fsIndex
	err error
	at  time.Time
}

// indexCache 按 orb 缓存索引：同一 orb 单飞，全局同时只建一个，LRU 保留 offlineIndexCacheSize 份，失败缓存 offlineIndexErrTTL。零值可用。
type indexCache struct {
	mu     sync.Mutex
	builds map[string]*fsBuild
	// order 是 orb 的最近使用顺序（末尾最新）。
	order []string
	sem   chan struct{}
}

// buildFunc 建立索引；scanned 记录已扫描条目数。
type buildFunc func(ctx context.Context, scanned *atomic.Int64) (*fsIndex, error)

// get 取 orb（缓存键 key）的索引：就绪 → 返回；失败（未过期）→ 返回错误；否则启动 / 等待后台建立，
// 至多等 offlineIndexWait，仍未完成 → 503 offline_indexing。后台建立受 life 约束，不随 ctx 结束。
func (c *indexCache) get(ctx, life context.Context, orbID, key string, build buildFunc) (*fsIndex, error) {
	b := c.acquire(life, orbID, key, build)
	t := time.NewTimer(offlineIndexWait)
	defer t.Stop()
	select {
	case <-b.done:
		if b.err != nil {
			return nil, b.err
		}
		return b.ix, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, fsErrf(http.StatusServiceUnavailable, "offline_indexing", "indexing: %d entries scanned", b.scanned.Load())
	}
}

// acquire 返回 orb 当前可用的 fsBuild（键不符或失败已过期时重新启动一次）。
func (c *indexCache) acquire(life context.Context, orbID, key string, build buildFunc) *fsBuild {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.builds == nil {
		c.builds, c.sem = map[string]*fsBuild{}, make(chan struct{}, 1)
	}
	c.touch(orbID)
	if b := c.builds[orbID]; b != nil {
		if b.key == key && !(b.finished() && b.err != nil && time.Since(b.at) > offlineIndexErrTTL) {
			return b
		}
		b.cancel() // 容器已变（或失败已过期）：放弃旧的
	}
	bctx, cancel := context.WithTimeout(life, offlineIndexTimeout)
	b := &fsBuild{key: key, done: make(chan struct{}), cancel: cancel}
	c.builds[orbID] = b
	go func() {
		defer close(b.done)
		defer cancel()
		select {
		case c.sem <- struct{}{}:
			defer func() { <-c.sem }()
			b.ix, b.err = build(bctx, &b.scanned)
		case <-bctx.Done():
			b.err = bctx.Err()
		}
		if b.err != nil {
			slog.Warn("停机 orb 文件索引失败", "orb", orbID, "err", b.err)
		} else {
			slog.Info("停机 orb 文件索引就绪", "orb", orbID, "entries", b.ix.count, "truncated", b.ix.truncated)
		}
		b.at = time.Now()
	}()
	c.evict()
	return b
}

func (b *fsBuild) finished() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// touch 把 orb 移到最近使用（调用方持有 mu）。
func (c *indexCache) touch(orbID string) {
	c.order = slices.DeleteFunc(c.order, func(s string) bool { return s == orbID })
	c.order = append(c.order, orbID)
}

// evict 淘汰最久未用的已完成索引，直到不超过 offlineIndexCacheSize（进行中的不淘汰；调用方持有 mu）。
func (c *indexCache) evict() {
	for i := 0; len(c.builds) > offlineIndexCacheSize && i < len(c.order); {
		id := c.order[i]
		if b := c.builds[id]; b != nil && !b.finished() {
			i++
			continue
		}
		delete(c.builds, id)
		c.order = slices.Delete(c.order, i, i+1)
	}
}

// ---- runner 侧 ----

// fsWorker 长轮询只读文件请求并回传应答，直到 ctx 结束；令牌被拒时返回 ErrUnauthorized。
// 应答使用 jobCtx（退出时给在途请求宽限），后台建索引同样以 jobCtx 为生命周期。
func (r *Runner) fsWorker(ctx, jobCtx context.Context, fsd OfflineFS) error {
	backoff := time.Second
	for ctx.Err() == nil {
		req, ok, err := r.claimFs(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, ErrUnauthorized):
			return err
		case errors.Is(err, errLeaseConflict):
			slog.Warn("租约被其他实例持有，重新 hello")
			if !sleepCtx(ctx, 5*time.Second) {
				return nil
			}
			_ = r.hello(ctx)
			continue
		case err != nil:
			slog.Warn("认领只读文件请求失败", "err", err, "retryIn", backoff)
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			backoff = min(backoff*2, 15*time.Second)
			continue
		}
		backoff = time.Second
		if !ok {
			continue
		}
		if err := r.serveFs(jobCtx, fsd, req); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) claimFs(ctx context.Context) (apigen.ProviderFsRequest, bool, error) {
	var res apigen.ProviderFsClaimResponse
	req := apigen.ProviderFsClaimRequest{InstanceID: r.instanceID, WaitSeconds: int(r.ClaimWait / time.Second)}
	c, cancel := context.WithTimeout(ctx, r.ClaimWait+15*time.Second)
	defer cancel()
	status, err := r.do(c, "/provider/v1/fs/claim", req, &res)
	if err != nil {
		return res.Request, false, err
	}
	return res.Request, status == http.StatusOK, nil
}

// serveFs 执行一个只读文件请求并把应答流式 POST 回控制面。回传没有总时限（大文件），只受 ctx 约束；
// 回传结束（成功或失败）时取消本次请求的上下文，驱动为它启动的进程随之结束。只在令牌被拒时返回错误。
func (r *Runner) serveFs(ctx context.Context, fsd OfflineFS, fr apigen.ProviderFsRequest) error {
	start := time.Now()
	log := slog.With("fsReq", fr.ID, "op", fr.Op, "orb", fr.OrbID)
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rep := fsd.OfflineFS(rctx, ctx, fr)
	body := rep.Body
	if body == nil || rep.Length == 0 {
		if body != nil {
			_ = body.Close()
		}
		body = http.NoBody
	}
	defer body.Close()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost,
		strings.TrimRight(r.URL, "/")+"/provider/v1/fs/"+url.PathEscape(fr.ID)+"/reply", body)
	if err != nil {
		log.Warn("构造只读文件应答失败", "err", err)
		return nil
	}
	for k, vs := range rep.Header {
		req.Header[k] = slices.Clone(vs)
	}
	switch {
	case body == http.NoBody:
		req.ContentLength = 0
	case rep.Length > 0:
		req.ContentLength = rep.Length
	default:
		req.ContentLength = -1
	}
	req.Header.Set("X-Oryn-Status", strconv.Itoa(rep.Status))
	req.Header.Set("Authorization", "Bearer "+r.Token)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		log.Warn("回传只读文件应答失败", "err", err)
		return nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode >= 300:
		log.Warn("控制面拒绝只读文件应答", "status", resp.StatusCode, "body", strings.TrimSpace(string(raw)))
	default:
		log.Debug("只读文件请求完成", "status", rep.Status, "durationMs", time.Since(start).Milliseconds())
	}
	return nil
}
