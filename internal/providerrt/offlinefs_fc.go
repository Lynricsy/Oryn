package providerrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ext4ro"
)

// firecracker 驱动的停机 orb 只读预览：VM 的根是 overlay（lower = 只读基础盘 bases/<id>.ext4，
// upper = 可写盘 vms/<orb>/disk.ext4 的 /upper，见 internal/orbdrt/guestinit），停机后两块盘都不再被打开，
// 由 internal/ext4ro 在用户态解析（不挂载：镜像内容由 guest 写入，不可信，不交给宿主内核解析）。
// 合并按 overlayfs 规则：upper 的 whiteout（字符设备 0:0）删掉 lower 的同名条目；带 trusted.overlay.opaque=y 的
// upper 目录遮住 lower 的同名目录；trusted.overlay.redirect 指向 lower 中的另一目录（guest 内核默认不开，防御性支持）。

const (
	// fcUpperRoot 是可写盘里 overlay upper 目录的位置。
	fcUpperRoot = "/upper"
	// fcOvlMaxDepth 是合并时的最大目录深度（防御构造出的目录环）。
	fcOvlMaxDepth = 256
)

// fcFileRef 是索引条目在镜像中的位置：读取时重新打开镜像（索引不持有文件句柄，淘汰时无需关闭）。
// gen 是建索引时的磁盘代数：inode 号只在同一代里有意义（VM 运行过后 inode 与数据块可能已被复用）。
type fcFileRef struct {
	img string
	ino uint32
	gen uint64
}

// ---- 与生命周期操作的互斥 ----

// fcDiskGate 让停机预览对镜像的读取与改动磁盘的生命周期操作（spawn / delete）互斥：
// 生命周期操作开始时代数加一并置 busy，此后旧代数的每一次读取都失败；正在进行的单次读取由写锁等它结束。
// 因此预览只可能读到「操作开始之前」的磁盘，不会在 VM 启动后把复用了的 inode / 数据块当成旧文件读出来。
type fcDiskGate struct {
	mu   sync.RWMutex
	gen  uint64
	busy int
}

func (d *FirecrackerDriver) diskGate(orbID string) *fcDiskGate {
	v, _ := d.diskGates.LoadOrStore(orbID, &fcDiskGate{})
	return v.(*fcDiskGate)
}

// beginLifecycle 在 spawn / delete 改动磁盘之前调用：作废在途的预览读取与该 orb 的索引缓存，直到返回的 end 被调用前拒绝新的预览。
func (d *FirecrackerDriver) beginLifecycle(orbID string) (end func()) {
	g := d.diskGate(orbID)
	g.mu.Lock()
	g.gen++
	g.busy++
	g.mu.Unlock()
	d.offline.drop(orbID)
	return func() {
		g.mu.Lock()
		g.busy--
		g.mu.Unlock()
	}
}

// errOrbWoke 是预览进行中 orb 开始启动（或被删除）时，之后所有读取返回的错误。
var errOrbWoke = fsErrf(http.StatusConflict, "orb_not_stopped", "orb VM started (or was deleted) while previewing")

// fcDiskSession 是一次预览对镜像的读取资格（某一代磁盘）。nil = 不设闸（单测直接建索引时）。
type fcDiskSession struct {
	g   *fcDiskGate
	gen uint64
}

// readerAt 返回经闸门的镜像读取器：每次 ReadAt 持读锁并核对代数。
func (s *fcDiskSession) readerAt(f *os.File) io.ReaderAt {
	if s == nil {
		return f
	}
	return gatedReaderAt{f: f, s: s}
}

// valid 报告这一代磁盘是否仍未被生命周期操作作废。
func (s *fcDiskSession) valid() error {
	if s == nil {
		return nil
	}
	s.g.mu.RLock()
	defer s.g.mu.RUnlock()
	if s.g.gen != s.gen {
		return errOrbWoke
	}
	return nil
}

type gatedReaderAt struct {
	f *os.File
	s *fcDiskSession
}

func (r gatedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.s.g.mu.RLock()
	defer r.s.g.mu.RUnlock()
	if r.s.g.gen != r.s.gen {
		return 0, errOrbWoke
	}
	return r.f.ReadAt(p, off)
}

// OfflineFS 实现 OfflineFS。
func (d *FirecrackerDriver) OfflineFS(ctx, life context.Context, req apigen.ProviderFsRequest) FsReply {
	orbID := req.OrbID
	sess, key, vm, err := d.offlineSession(orbID)
	if err != nil {
		return errReply(err)
	}
	lower, upper := d.paths.base(vm.Base), d.paths.vmDisk(orbID)
	ix, err := d.offline.get(ctx, life, orbID, key, func(bctx context.Context, scanned *atomic.Int64) (*fsIndex, error) {
		return buildOverlayIndex(bctx, lower, upper, scanned, sess)
	})
	if err != nil {
		return indexErrReply(err)
	}
	return serveOffline(ctx, ix, req, fcOffline{sess: sess})
}

// offlineSession 在闸门读锁内确认 VM 已停机、没有进行中的生命周期操作，取得当前磁盘代数与索引缓存键：
// 基础盘 id + 可写盘的大小与修改时间 + 代数。可写盘只在 VM 运行时被写，代数不变即内容不变。
func (d *FirecrackerDriver) offlineSession(orbID string) (*fcDiskSession, string, vmMeta, error) {
	g := d.diskGate(orbID)
	g.mu.RLock()
	defer g.mu.RUnlock()
	vm, exists, err := d.paths.loadVM(orbID)
	if err != nil {
		return nil, "", vm, err
	}
	if !exists {
		return nil, "", vm, fsErrf(http.StatusNotFound, "not_found", "no such orb VM %s", orbID)
	}
	if g.busy > 0 || d.host.Alive(orbID) {
		return nil, "", vm, fsErrf(http.StatusConflict, "orb_not_stopped", "orb VM %s is running or starting", orbID)
	}
	st, err := os.Stat(d.paths.vmDisk(orbID))
	if err != nil {
		return nil, "", vm, err
	}
	sess := &fcDiskSession{g: g, gen: g.gen}
	return sess, fmt.Sprintf("%s %d %d %d", vm.Base, st.Size(), st.ModTime().UnixNano(), g.gen), vm, nil
}

// fcOffline 是 firecracker 的 offlineBackend：按索引里的 fcFileRef 直接读镜像（经本次请求的闸门）。
type fcOffline struct {
	sess *fcDiskSession
}

func (o fcOffline) open(n *fsNode) (*os.File, *ext4ro.File, *ext4ro.Inode, error) {
	ref, ok := n.ref.(fcFileRef)
	if !ok {
		return nil, nil, nil, errors.New("firecracker: 索引条目缺少镜像位置")
	}
	if o.sess != nil && ref.gen != o.sess.gen {
		return nil, nil, nil, errOrbWoke
	}
	f, err := os.Open(ref.img)
	if err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (*os.File, *ext4ro.File, *ext4ro.Inode, error) {
		_ = f.Close()
		return nil, nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	fs, err := ext4ro.Open(o.sess.readerAt(f), st.Size())
	if err != nil {
		return fail(err)
	}
	in, err := fs.Inode(ref.ino)
	if err != nil {
		return fail(err)
	}
	file, err := fs.Open(in)
	if err != nil {
		return fail(err)
	}
	return f, file, in, nil
}

func (o fcOffline) sniff(_ context.Context, real string, n *fsNode) (string, bool, error) {
	f, file, _, err := o.open(n)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	buf := make([]byte, offlineSniffBytes)
	k, err := file.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	m, text := sniffContent(real, buf[:k], k == offlineSniffBytes)
	return m, text, nil
}

func (o fcOffline) read(_ context.Context, _ string, n *fsNode, rangeHdr string) FsReply {
	f, file, in, err := o.open(n)
	if err != nil {
		return errReply(err)
	}
	rep, start, length, ok := rangeReply(rangeHdr, file.Size(), in.Mtime)
	if !ok {
		_ = f.Close()
		return rep
	}
	rep.Body = &fcBody{SectionReader: io.NewSectionReader(file, start, length), f: f}
	return rep
}

// fcBody 是 read 的应答体：镜像里的一段；Close 关闭镜像文件。
type fcBody struct {
	*io.SectionReader
	f *os.File
}

func (b *fcBody) Close() error { return b.f.Close() }

// ---- overlay 合并 ----

// ovlLayer 是 overlay 的一层：镜像路径与解析器。
type ovlLayer struct {
	img string
	fs  *ext4ro.FS
}

// ovlBuild 是一次合并的状态。
type ovlBuild struct {
	ctx     context.Context
	ix      *fsIndex
	lower   ovlLayer
	upper   ovlLayer
	scanned *atomic.Int64
	// gen 是这次合并读取的磁盘代数，记进每个文件条目的 fcFileRef。
	gen uint64
	// stop 为真时（条目达到上限）停止遍历。
	stop bool
	// seen 记录已展开的目录（层 + inode），防御构造出的目录环。
	seen map[ovlDirKey]bool
}

type ovlDirKey struct {
	upper bool
	ino   uint32
}

// buildOverlayIndex 打开 lower / upper 两块镜像，按 overlay 规则合并出 OrbHome 子树（不含 .oryn）的索引。
// 镜像经 sess 的闸门读取；合并途中磁盘被作废时整个索引作废（逐条目的容错会把它误当成损坏条目跳过）。
func buildOverlayIndex(ctx context.Context, lowerImg, upperImg string, scanned *atomic.Int64, sess *fcDiskSession) (ix *fsIndex, err error) {
	defer func() {
		if verr := sess.valid(); verr != nil {
			ix, err = nil, verr
		}
	}()
	open := func(p string) (*os.File, ovlLayer, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, ovlLayer{}, err
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, ovlLayer{}, err
		}
		fs, err := ext4ro.Open(sess.readerAt(f), st.Size())
		if err != nil {
			_ = f.Close()
			return nil, ovlLayer{}, fmt.Errorf("解析 %s: %w", path.Base(p), err)
		}
		return f, ovlLayer{img: p, fs: fs}, nil
	}
	var gen uint64
	if sess != nil {
		gen = sess.gen
	}
	lf, lower, err := open(lowerImg)
	if err != nil {
		return nil, err
	}
	defer lf.Close()
	uf, upper, err := open(upperImg)
	if err != nil {
		return nil, err
	}
	defer uf.Close()

	b := &ovlBuild{ctx: ctx, ix: &fsIndex{root: newDirNode()}, lower: lower, upper: upper, scanned: scanned, gen: gen, seen: map[ovlDirKey]bool{}}
	lowerHome, upperHome, err := b.locate(strings.Split(strings.TrimPrefix(OrbHome, "/"), "/"))
	if err != nil {
		return nil, err
	}
	if lowerHome == nil && upperHome == nil {
		return b.ix, nil // 合并视图里没有 OrbHome：空索引
	}
	for _, in := range []*ext4ro.Inode{upperHome, lowerHome} {
		if in != nil {
			b.ix.root.mode, b.ix.root.mtime = int(in.Mode&0o777), in.Mtime
			break
		}
	}
	if err := b.mergeDir(lowerHome, upperHome, ".", 0); err != nil {
		return nil, err
	}
	b.ix.truncated = b.stop
	return b.ix, nil
}

// locate 沿 comps 走到合并视图里的目录，返回该目录在 lower / upper 中对应的 inode（不存在的一侧为 nil）。
// 两侧都为 nil 表示该路径在合并视图里不存在或不是目录。
func (b *ovlBuild) locate(comps []string) (lower, upper *ext4ro.Inode, err error) {
	if lower, err = b.lower.fs.Inode(ext4ro.RootIno); err != nil {
		return nil, nil, err
	}
	switch upper, err = b.upper.fs.Lookup(fcUpperRoot); {
	case errors.Is(err, ext4ro.ErrNotExist) || errors.Is(err, ext4ro.ErrNotDir):
		upper = nil // 可写盘还没有 upper（从未启动过）：只有 lower
	case err != nil:
		return nil, nil, err
	case !upper.IsDir():
		upper = nil
	}
	for _, c := range comps {
		lower, upper, err = b.child(lower, upper, c)
		if err != nil {
			return nil, nil, err
		}
		if lower == nil && upper == nil {
			return nil, nil, nil
		}
	}
	return lower, upper, nil
}

// child 返回合并目录 (lower, upper) 中名为 name 的子目录在两侧的 inode；不是目录或被删除时两侧都为 nil。
func (b *ovlBuild) child(lower, upper *ext4ro.Inode, name string) (*ext4ro.Inode, *ext4ro.Inode, error) {
	var lc *ext4ro.Inode
	if lower != nil {
		in, err := b.entry(b.lower, lower, name)
		if err != nil {
			return nil, nil, err
		}
		if in != nil && in.IsDir() {
			lc = in
		}
	}
	if upper == nil {
		return lc, nil, nil
	}
	uc, err := b.entry(b.upper, upper, name)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case uc == nil:
		return lc, nil, nil
	case !uc.IsDir():
		return nil, nil, nil // whiteout 或被非目录替换
	}
	return b.lowerFor(lower, uc, lc), uc, nil
}

// lowerFor 按 upper 目录 uc 的 overlay 属性决定它对应的 lower 目录：opaque → 无；redirect → 指向的目录；
// 否则沿用同名的 lower 目录 lc。属性读不出（镜像损坏）时按没有处理。
func (b *ovlBuild) lowerFor(lowerParent, uc, lc *ext4ro.Inode) *ext4ro.Inode {
	if v, ok, err := b.upper.fs.Xattr(uc, "trusted.overlay.opaque"); err == nil && ok && string(v) == "y" {
		return nil
	}
	v, ok, err := b.upper.fs.Xattr(uc, "trusted.overlay.redirect")
	if err != nil || !ok {
		return lc
	}
	// 绝对路径相对 lower 根；相对路径是 lower 父目录里的另一个名字
	target := string(v)
	var in *ext4ro.Inode
	if strings.HasPrefix(target, "/") {
		in, err = b.lower.fs.Lookup(target)
	} else if lowerParent != nil && target != "" && !strings.Contains(target, "/") {
		in, err = b.entry(b.lower, lowerParent, target)
	}
	if err != nil || in == nil || !in.IsDir() {
		return nil
	}
	return in
}

// entry 在目录 dir 中按名查找并返回 inode；不存在返回 nil。
func (b *ovlBuild) entry(l ovlLayer, dir *ext4ro.Inode, name string) (*ext4ro.Inode, error) {
	ents, err := l.fs.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		if e.Name == name {
			return l.fs.Inode(e.Ino)
		}
	}
	return nil, nil
}

// readDir 读目录为「名 → 目录项」；dir 为 nil 时为空。
func readDirMap(l ovlLayer, dir *ext4ro.Inode) (map[string]ext4ro.DirEntry, error) {
	if dir == nil {
		return nil, nil
	}
	ents, err := l.fs.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	m := make(map[string]ext4ro.DirEntry, len(ents))
	for _, e := range ents {
		if e.Name == "" || e.Name == "." || e.Name == ".." || strings.Contains(e.Name, "/") {
			continue
		}
		m[e.Name] = e
	}
	return m, nil
}

// mergeDir 合并目录 (lower, upper) 的内容放到索引的 rel 下（rel 自身已放好），递归处理子目录。
// 单个条目或子目录读不出（镜像损坏）时跳过它，不让整个索引失败；只有 ctx 结束才返回错误。
func (b *ovlBuild) mergeDir(lower, upper *ext4ro.Inode, rel string, depth int) error {
	if b.stop || depth > fcOvlMaxDepth {
		return nil
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	for _, k := range []ovlDirKey{{false, inoOf(lower)}, {true, inoOf(upper)}} {
		if k.ino == 0 {
			continue
		}
		if b.seen[k] {
			return nil // 目录环：同一目录不重复展开
		}
		b.seen[k] = true
	}
	lents, lerr := readDirMap(b.lower, lower)
	uents, uerr := readDirMap(b.upper, upper)
	if uerr != nil {
		// upper 目录读不出时不能判断哪些 lower 条目已被删除：整个目录按空处理
		return nil
	}
	if lerr != nil {
		lents = nil
	}
	names := make([]string, 0, len(lents)+len(uents))
	for n := range uents {
		names = append(names, n)
	}
	for n := range lents {
		if _, ok := uents[n]; !ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		if b.stop {
			return nil
		}
		crel := name
		if rel != "." {
			crel = rel + "/" + name
		}
		if isOrynRel(crel) {
			continue
		}
		var lc, uc *ext4ro.Inode
		if e, ok := lents[name]; ok {
			lc, _ = b.lower.fs.Inode(e.Ino)
		}
		if e, ok := uents[name]; ok {
			var err error
			if uc, err = b.upper.fs.Inode(e.Ino); err != nil {
				continue // upper 条目损坏：同名的 lower 条目可能已被它删除或替换，一并跳过
			}
		}
		var err error
		switch {
		case uc != nil && uc.IsWhiteout():
		case uc != nil && uc.IsDir():
			var ld *ext4ro.Inode
			if lc != nil && lc.IsDir() {
				ld = lc
			}
			if b.put(crel, b.upper, uc) {
				err = b.mergeDir(b.lowerFor(lower, uc, ld), uc, crel, depth+1)
			}
		case uc != nil:
			b.put(crel, b.upper, uc)
		case lc == nil || lc.IsWhiteout():
		case lc.IsDir():
			if b.put(crel, b.lower, lc) {
				err = b.mergeDir(lc, nil, crel, depth+1)
			}
		default:
			b.put(crel, b.lower, lc)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func inoOf(in *ext4ro.Inode) uint32 {
	if in == nil {
		return 0
	}
	return in.Ino
}

// put 把 inode 转为索引条目放到 rel，返回是否放入（符号链接读不出时跳过；条目达到上限时置 stop）。
func (b *ovlBuild) put(rel string, l ovlLayer, in *ext4ro.Inode) bool {
	n := &fsNode{mode: int(in.Mode & 0o777), mtime: in.Mtime}
	switch {
	case in.IsDir():
		n.typ, n.children = apigen.FsEntryTypeDir, map[string]*fsNode{}
	case in.IsRegular():
		n.typ, n.size, n.ref = apigen.FsEntryTypeFile, in.Size, fcFileRef{img: l.img, ino: in.Ino, gen: b.gen}
	case in.IsSymlink():
		target, err := l.fs.Readlink(in)
		if err != nil {
			return false
		}
		n.typ, n.target, n.size = apigen.FsEntryTypeSymlink, target, int64(len(target))
	default:
		n.typ = apigen.FsEntryTypeOther
	}
	if !b.ix.put(rel, n) {
		b.stop = true
		return false
	}
	if b.scanned != nil {
		b.scanned.Store(int64(b.ix.count))
	}
	return true
}
