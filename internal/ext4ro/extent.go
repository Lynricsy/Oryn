package ext4ro

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
)

const (
	extentMagic    = 0xF30A
	maxExtentDepth = 5
	// maxExtents：单个文件 extent 总数上限（防恶意镜像让 Open 吃光内存）。
	maxExtents = 1 << 20
	// maxExtentNodes：遍历的树节点上限。空叶子 / 指向同一块的环不产生 extent，单靠 maxExtents 拦不住；
	// 合法的 1<<20 个 extent 也只需几千个叶子。
	maxExtentNodes = 1 << 16
	// initMaxLen：ee_len 超过它表示未初始化 extent（长度 = ee_len - initMaxLen）。
	initMaxLen = 32768
)

// extent 是一段逻辑块到物理块的连续映射。
type extent struct {
	lblk   uint64 // 起始逻辑块
	length uint64
	pblk   uint64
	uninit bool // 未初始化：已分配但读为零
}

// extentWalker 收集一个 inode 的全部 extent，并计数以保证有界。
type extentWalker struct {
	f       *FS
	nodes   int
	extents []extent
	buf     []byte
}

// loadExtents 收集 inode 的 extent 列表：按逻辑块排序，校验无重叠、物理块在范围内。
func (f *FS) loadExtents(in *Inode) ([]extent, error) {
	if in.hasInlineData() {
		return nil, fmt.Errorf("%w: inline data (inode %d)", ErrUnsupported, in.Ino)
	}
	if !in.hasExtents() {
		return nil, fmt.Errorf("%w: non-extent block map (inode %d)", ErrUnsupported, in.Ino)
	}
	w := &extentWalker{f: f}
	if err := w.node(in.block[:], -1); err != nil {
		return nil, fmt.Errorf("inode %d: %w", in.Ino, err)
	}
	ext := w.extents
	if !slices.IsSortedFunc(ext, cmpExtent) {
		slices.SortFunc(ext, cmpExtent)
	}
	for i := 1; i < len(ext); i++ {
		if ext[i-1].lblk+ext[i-1].length > ext[i].lblk {
			return nil, corrupt("inode %d: overlapping extents at logical block %d", in.Ino, ext[i].lblk)
		}
	}
	return ext, nil
}

func cmpExtent(a, b extent) int {
	switch {
	case a.lblk < b.lblk:
		return -1
	case a.lblk > b.lblk:
		return 1
	}
	return 0
}

// node 解析一个 extent 树节点。wantDepth<0 表示根（深度取自头部）；否则子节点深度必须恰为 wantDepth。
func (w *extentWalker) node(b []byte, wantDepth int) error {
	w.nodes++
	if w.nodes > maxExtentNodes {
		return corrupt("extent tree has more than %d nodes", maxExtentNodes)
	}
	le := binary.LittleEndian
	if le.Uint16(b[0:]) != extentMagic {
		return corrupt("bad extent header magic %#x", le.Uint16(b[0:]))
	}
	entries := int(le.Uint16(b[2:]))
	maxEntries := int(le.Uint16(b[4:]))
	depth := int(le.Uint16(b[6:]))
	// 节点容量：根在 i_block（60 字节），其余是整块；块尾可能有 4 字节校验和，整除时自然落在余数里。
	if capacity := (len(b) - 12) / 12; maxEntries != capacity {
		return corrupt("extent node max %d != capacity %d", maxEntries, capacity)
	}
	if entries > maxEntries {
		return corrupt("extent node entries %d > max %d", entries, maxEntries)
	}
	if wantDepth < 0 {
		if depth > maxExtentDepth {
			return corrupt("extent tree depth %d > %d", depth, maxExtentDepth)
		}
	} else if depth != wantDepth {
		return corrupt("extent node depth %d, want %d", depth, wantDepth)
	}

	if depth == 0 {
		for i := range entries {
			e := b[12+12*i:]
			n := uint64(le.Uint16(e[4:]))
			x := extent{
				lblk: uint64(le.Uint32(e[0:])),
				pblk: uint64(le.Uint16(e[6:]))<<32 | uint64(le.Uint32(e[8:])),
			}
			if n > initMaxLen {
				n -= initMaxLen
				x.uninit = true
			}
			if n == 0 {
				return corrupt("zero-length extent at logical block %d", x.lblk)
			}
			x.length = n
			if x.lblk+n > 1<<32 {
				return corrupt("extent beyond 2^32 logical blocks")
			}
			if x.pblk+n > w.f.blocksCount {
				return corrupt("extent physical blocks %d+%d beyond blocks count %d", x.pblk, n, w.f.blocksCount)
			}
			if len(w.extents) >= maxExtents {
				return corrupt("more than %d extents", maxExtents)
			}
			w.extents = append(w.extents, x)
		}
		return nil
	}

	// 索引节点：先把子节点块号抄出来，因为子节点复用同一层缓冲区。
	children := make([]uint64, entries)
	for i := range entries {
		e := b[12+12*i:]
		children[i] = uint64(le.Uint16(e[8:]))<<32 | uint64(le.Uint32(e[4:]))
	}
	child := make([]byte, w.f.blockSize)
	for _, blk := range children {
		if err := w.f.readBlock(blk, child); err != nil {
			return err
		}
		if err := w.node(child, depth-1); err != nil {
			return err
		}
	}
	return nil
}

// File 是普通文件的只读视图，实现 io.ReaderAt。
type File struct {
	fs      *FS
	size    int64
	extents []extent // 按逻辑块排序、无重叠
}

// Open 打开普通文件：一次性收集 extent 列表，之后 ReadAt 二分查找。非普通文件 → 错误。
func (f *FS) Open(in *Inode) (fl *File, err error) {
	defer recoverErr(&err)
	if !in.IsRegular() {
		return nil, fmt.Errorf("ext4ro: inode %d is not a regular file", in.Ino)
	}
	return f.openData(in)
}

// openData 为任意带 extent 数据的 inode（文件 / 目录 / 慢符号链接）建立读视图。
func (f *FS) openData(in *Inode) (*File, error) {
	// 无数据的空文件不看 i_block：老工具可能留下未设 EXTENTS_FL 的空 inode。
	if in.Size == 0 && !in.hasInlineData() && !in.hasExtents() {
		return &File{fs: f}, nil
	}
	ext, err := f.loadExtents(in)
	if err != nil {
		return nil, err
	}
	return &File{fs: f, size: in.Size, extents: ext}, nil
}

// Size 返回文件字节数。
func (fl *File) Size() int64 { return fl.size }

// ReadAt 满足 io.ReaderAt：空洞与未初始化 extent 读为零；读到文件尾时返回 io.EOF。
func (fl *File) ReadAt(p []byte, off int64) (n int, err error) {
	defer recoverErr(&err)
	if off < 0 {
		return 0, errors.New("ext4ro: negative offset")
	}
	if off >= fl.size {
		return 0, io.EOF
	}
	want := p
	if rem := fl.size - off; int64(len(want)) > rem {
		want = want[:rem]
	}
	bs := int64(fl.fs.blockSize)
	for n < len(want) {
		pos := off + int64(n)
		lblk := uint64(pos / bs)
		inBlk := pos % bs
		chunk := want[n:]
		// 第一个尾部越过 lblk 的 extent。
		i := sort.Search(len(fl.extents), func(i int) bool {
			e := fl.extents[i]
			return e.lblk+e.length > lblk
		})
		if i == len(fl.extents) || fl.extents[i].lblk > lblk {
			// 空洞：填零到下一个 extent 起点。
			if i < len(fl.extents) {
				if holeEnd := int64(fl.extents[i].lblk)*bs - pos; int64(len(chunk)) > holeEnd {
					chunk = chunk[:holeEnd]
				}
			}
			clear(chunk)
			n += len(chunk)
			continue
		}
		e := fl.extents[i]
		if extEnd := int64(e.lblk+e.length)*bs - pos; int64(len(chunk)) > extEnd {
			chunk = chunk[:extEnd]
		}
		if e.uninit {
			clear(chunk)
		} else {
			phys := int64(e.pblk+(lblk-e.lblk))*bs + inBlk
			if err := fl.fs.readFull(chunk, phys); err != nil {
				return n, err
			}
		}
		n += len(chunk)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
