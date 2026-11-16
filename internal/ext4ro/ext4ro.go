// Package ext4ro 是纯 Go 的只读 ext4 解析器：不挂载、不依赖第三方库，直接从镜像文件读目录、文件、
// 符号链接与扩展属性，供停机 Firecracker orb 的磁盘预览使用。
//
// 输入来自 guest（guest root 可构造任意字节），因此所有长度 / 偏移字段都先校验再使用，
// 不按镜像里的长度预分配大内存，所有对外方法用 recover 兜底，保证损坏镜像只得到错误而不会 panic。
// 校验和一律不验证（只读预览不需要，且日志未重放的镜像本就可能不一致）。
package ext4ro

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
	"time"
)

// RootIno 是根目录的 inode 号。
const RootIno uint32 = 2

var (
	// ErrNotExist：Lookup 找不到路径分量。
	ErrNotExist = errors.New("ext4ro: no such file or directory")
	// ErrNotDir：对非目录做目录操作（含路径中间分量是符号链接）。
	ErrNotDir = errors.New("ext4ro: not a directory")
	// ErrUnsupported：遇到本包不支持的特性（inline_data、非 extent 块映射、ea_inode 值等）。
	ErrUnsupported = errors.New("ext4ro: unsupported feature")
)

const (
	superblockOffset = 1024
	superblockSize   = 1024
	sbMagic          = 0xEF53

	// 特性位（只列用到的）。
	compatHasJournal     = 0x4
	compatSparseSuper2   = 0x200
	incompatRecover      = 0x4
	incompatMetaBG       = 0x10
	incompat64Bit        = 0x80
	roCompatSparseSuper  = 0x1
	roCompatGdtCsum      = 0x10
	roCompatMetadataCsum = 0x400

	// i_flags。
	flagIndex      = 0x1000
	flagExtents    = 0x80000
	flagInlineData = 0x10000000

	bgInodeUninit = 0x1

	goodOldInodeSize = 128

	// groupCacheMax：组描述符缓存上限；超出后不再缓存（只影响性能），避免恶意组数撑爆内存。
	groupCacheMax = 4096
)

// 文件类型位（i_mode & 0xF000）。
const (
	modeTypeMask = 0xF000
	modeChar     = 0x2000
	modeDir      = 0x4000
	modeBlock    = 0x6000
	modeRegular  = 0x8000
	modeSymlink  = 0xA000
)

// FS 是一个已打开的只读 ext4 镜像。Open 之后只读不可变（组描述符缓存有锁），可并发使用。
type FS struct {
	r io.ReaderAt

	blockSize      uint32
	blocksCount    uint64
	inodesCount    uint32
	inodesPerGroup uint32
	blocksPerGroup uint32
	firstDataBlock uint32
	inodeSize      uint32
	descSize       uint32
	groupCount     uint64
	firstMetaBG    uint32
	backupBGs      [2]uint32

	featCompat   uint32
	featIncompat uint32
	featRoCompat uint32
	journalInum  uint32

	// 日志重放覆盖层：文件系统块号 → 日志里最新已提交副本。Open 期间建好，之后只读。
	overlay   map[uint64]journalCopy
	replayed  bool
	replayErr error // 放弃重放的原因（测试与排障用）

	mu         sync.Mutex
	groupCache map[uint32]groupDesc
}

type groupDesc struct {
	inodeTable uint64
	flags      uint16
}

// corrupt 生成「镜像损坏」类错误。
func corrupt(format string, args ...any) error {
	return fmt.Errorf("ext4ro: corrupt image: "+format, args...)
}

// recoverErr 把意外 panic 转成错误：解析不可信输入，宁可报错也不能让调用方进程崩溃。
func recoverErr(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("ext4ro: internal error: %v\n%s", r, debug.Stack())
	}
}

// readRaw 直接从镜像 off 读满 p；镜像被截断（读到 EOF）视为损坏而不是正常结束。
func (f *FS) readRaw(p []byte, off int64) error {
	n, err := f.r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return fmt.Errorf("ext4ro: read at %d: %w", off, err)
}

// readFull 按文件系统视角读：命中日志覆盖层的块从日志副本读，其余连续段合并成一次镜像读。
func (f *FS) readFull(p []byte, off int64) error {
	if len(f.overlay) == 0 {
		return f.readRaw(p, off)
	}
	bs := int64(f.blockSize)
	for len(p) > 0 {
		n := min(int64(len(p)), bs-off%bs)
		if jc, ok := f.overlay[uint64(off/bs)]; ok {
			inBlk := off % bs
			if err := f.readRaw(p[:n], int64(jc.pblk)*bs+inBlk); err != nil {
				return err
			}
			if jc.escaped && inBlk < 4 {
				// 日志里首 4 字节恰为 jbd2 magic 的块被转义成 0 存放，读出时还原。
				var m [4]byte
				binary.BigEndian.PutUint32(m[:], jbdMagic)
				copy(p[:n], m[inBlk:])
			}
		} else {
			for n < int64(len(p)) {
				if _, hit := f.overlay[uint64((off+n)/bs)]; hit {
					break
				}
				n += min(int64(len(p))-n, bs)
			}
			if err := f.readRaw(p[:n], off); err != nil {
				return err
			}
		}
		p, off = p[n:], off+n
	}
	return nil
}

// readBlockRange 读物理块 blk 内 [off, off+len(p)) 的字节，块号越过 blocks_count 报错。
func (f *FS) readBlockRange(blk uint64, off uint32, p []byte) error {
	if blk >= f.blocksCount {
		return corrupt("block %d beyond blocks count %d", blk, f.blocksCount)
	}
	if uint64(off)+uint64(len(p)) > uint64(f.blockSize) {
		return corrupt("read past block end")
	}
	return f.readFull(p, int64(blk)*int64(f.blockSize)+int64(off))
}

func (f *FS) readBlock(blk uint64, p []byte) error {
	return f.readBlockRange(blk, 0, p[:f.blockSize])
}

// Open 读超级块并校验几何参数：magic、块大小 1K–64K、inode 大小、组数与 inode 总数、镜像大小。
// 带 needs_recovery 时尝试只读重放内部 jbd2 日志（结果作为读取覆盖层，不写回镜像）；
// 重放失败不让 Open 失败，退回未重放视图，由 NeedsRecovery 报告。
// size 是镜像字节数，用于拒绝被截断的镜像。
func Open(r io.ReaderAt, size int64) (fs *FS, err error) {
	defer recoverErr(&err)
	if size < superblockOffset+superblockSize {
		return nil, corrupt("image too small (%d bytes)", size)
	}
	sb := make([]byte, superblockSize)
	f := &FS{r: r, groupCache: make(map[uint32]groupDesc)}
	if err := f.readFull(sb, superblockOffset); err != nil {
		return nil, err
	}
	le := binary.LittleEndian
	if le.Uint16(sb[0x38:]) != sbMagic {
		return nil, corrupt("bad superblock magic %#x", le.Uint16(sb[0x38:]))
	}
	logBS := le.Uint32(sb[0x18:])
	if logBS > 6 {
		return nil, corrupt("bad block size log %d", logBS)
	}
	f.blockSize = 1024 << logBS
	f.inodesCount = le.Uint32(sb[0x0:])
	f.firstDataBlock = le.Uint32(sb[0x14:])
	f.blocksPerGroup = le.Uint32(sb[0x20:])
	f.inodesPerGroup = le.Uint32(sb[0x28:])
	f.featCompat = le.Uint32(sb[0x5C:])
	f.featIncompat = le.Uint32(sb[0x60:])
	f.featRoCompat = le.Uint32(sb[0x64:])
	f.firstMetaBG = le.Uint32(sb[0x104:])
	f.journalInum = le.Uint32(sb[0xE0:])
	f.backupBGs = [2]uint32{le.Uint32(sb[0x24C:]), le.Uint32(sb[0x250:])}
	f.blocksCount = uint64(le.Uint32(sb[0x4:]))
	if f.featIncompat&incompat64Bit != 0 {
		f.blocksCount |= uint64(le.Uint32(sb[0x150:])) << 32
	}

	if le.Uint32(sb[0x4C:]) == 0 { // rev 0：固定 128 字节 inode
		f.inodeSize = goodOldInodeSize
	} else {
		f.inodeSize = uint32(le.Uint16(sb[0x58:]))
	}
	if f.inodeSize < goodOldInodeSize || f.inodeSize > f.blockSize || f.inodeSize&(f.inodeSize-1) != 0 {
		return nil, corrupt("bad inode size %d", f.inodeSize)
	}

	if f.featIncompat&incompat64Bit != 0 {
		f.descSize = uint32(le.Uint16(sb[0xFE:]))
		if f.descSize < 64 || f.descSize > 1024 || f.descSize&(f.descSize-1) != 0 {
			return nil, corrupt("bad group descriptor size %d", f.descSize)
		}
	} else {
		f.descSize = 32
	}

	bitsPerBlock := f.blockSize * 8
	if f.blocksPerGroup == 0 || f.blocksPerGroup > bitsPerBlock {
		return nil, corrupt("bad blocks per group %d", f.blocksPerGroup)
	}
	if f.inodesPerGroup == 0 || f.inodesPerGroup > bitsPerBlock {
		return nil, corrupt("bad inodes per group %d", f.inodesPerGroup)
	}
	if f.blocksCount > uint64(size)/uint64(f.blockSize) {
		return nil, corrupt("blocks count %d exceeds image size %d", f.blocksCount, size)
	}
	if uint64(f.firstDataBlock) >= f.blocksCount {
		return nil, corrupt("first data block %d beyond blocks count %d", f.firstDataBlock, f.blocksCount)
	}
	f.groupCount = (f.blocksCount - uint64(f.firstDataBlock) + uint64(f.blocksPerGroup) - 1) / uint64(f.blocksPerGroup)
	if f.groupCount*uint64(f.inodesPerGroup) != uint64(f.inodesCount) {
		return nil, corrupt("inodes count %d != %d groups * %d", f.inodesCount, f.groupCount, f.inodesPerGroup)
	}
	if f.inodesCount < RootIno {
		return nil, corrupt("inodes count %d too small", f.inodesCount)
	}
	// 组描述符按需读取（Inode 时），这里只确认最后一个描述符所在块在镜像内、根 inode 可读。
	if _, err := f.descBlockLoc(uint32((f.groupCount - 1) * uint64(f.descSize) / uint64(f.blockSize))); err != nil {
		return nil, err
	}
	if f.featIncompat&incompatRecover != 0 {
		f.applyJournal()
	}
	if _, err := f.Inode(RootIno); err != nil {
		return nil, fmt.Errorf("ext4ro: root inode: %w", err)
	}
	return f, nil
}

// NeedsRecovery 报告镜像带 needs_recovery 且日志未能重放：此时读到的内容可能缺最近的写入。
func (f *FS) NeedsRecovery() bool { return f.featIncompat&incompatRecover != 0 && !f.replayed }

// Replayed 报告是否已处理日志并应用覆盖层（日志本身干净、没有待重放事务时也为真）。
func (f *FS) Replayed() bool { return f.replayed }

// bgHasSuper 对应内核 ext4_bg_has_super：该组是否带超级块备份（meta_bg 下决定描述符块位置）。
func (f *FS) bgHasSuper(g uint64) bool {
	if g == 0 {
		return true
	}
	if f.featCompat&compatSparseSuper2 != 0 {
		return g == uint64(f.backupBGs[0]) || g == uint64(f.backupBGs[1])
	}
	if g <= 1 || f.featRoCompat&roCompatSparseSuper == 0 {
		return true
	}
	if g&1 == 0 {
		return false
	}
	for _, base := range []uint64{3, 5, 7} {
		n := base
		for n < g {
			n *= base
		}
		if n == g {
			return true
		}
	}
	return false
}

// descBlockLoc 返回第 nr 个组描述符块的物理块号（内核 descriptor_loc）。
func (f *FS) descBlockLoc(nr uint32) (uint64, error) {
	// 超级块固定在字节 1024：1K 块时位于块 1，否则位于块 0；描述符表紧随其后。
	var sbBlock uint64
	if f.blockSize == 1024 {
		sbBlock = 1
	}
	var blk uint64
	if f.featIncompat&incompatMetaBG == 0 || nr < f.firstMetaBG {
		blk = sbBlock + uint64(nr) + 1
	} else {
		g := uint64(nr) * uint64(f.blockSize/f.descSize)
		blk = uint64(f.firstDataBlock) + g*uint64(f.blocksPerGroup)
		if f.bgHasSuper(g) {
			blk++
		}
		if f.blockSize == 1024 && nr == 0 && f.firstDataBlock == 0 {
			blk++
		}
	}
	if blk >= f.blocksCount {
		return 0, corrupt("group descriptor block %d beyond blocks count", blk)
	}
	return blk, nil
}

func (f *FS) groupDesc(g uint32) (groupDesc, error) {
	f.mu.Lock()
	gd, ok := f.groupCache[g]
	f.mu.Unlock()
	if ok {
		return gd, nil
	}
	descPerBlock := f.blockSize / f.descSize
	blk, err := f.descBlockLoc(g / descPerBlock)
	if err != nil {
		return gd, err
	}
	buf := make([]byte, f.descSize)
	if err := f.readBlockRange(blk, (g%descPerBlock)*f.descSize, buf); err != nil {
		return gd, err
	}
	le := binary.LittleEndian
	gd.inodeTable = uint64(le.Uint32(buf[0x8:]))
	if f.descSize >= 64 {
		gd.inodeTable |= uint64(le.Uint32(buf[0x28:])) << 32
	}
	gd.flags = le.Uint16(buf[0x12:])
	f.mu.Lock()
	if len(f.groupCache) < groupCacheMax {
		f.groupCache[g] = gd
	}
	f.mu.Unlock()
	return gd, nil
}

// Inode 是解析后的 inode。
type Inode struct {
	Ino   uint32
	Mode  uint16 // 原始 i_mode（类型位 + 权限位）
	Size  int64
	Mtime time.Time // 秒 + i_mtime_extra 的纳秒 / epoch 位（extra_isize 足够时）
	UID   uint32
	GID   uint32
	// 设备号（字符 / 块设备），按内核 ext4 规则：i_block[0] 非零用旧编码，否则用 i_block[1] 的新编码。
	RdevMajor, RdevMinor uint32

	flags      uint32
	block      [60]byte // i_block 原样：extent 树根 / 快速符号链接内容 / 设备号
	fileACL    uint64   // 外部 xattr 块
	extraIsize uint32
	raw        []byte // 整个磁盘 inode，用于 inode 内 xattr
}

func (in *Inode) IsDir() bool     { return in.Mode&modeTypeMask == modeDir }
func (in *Inode) IsRegular() bool { return in.Mode&modeTypeMask == modeRegular }
func (in *Inode) IsSymlink() bool { return in.Mode&modeTypeMask == modeSymlink }
func (in *Inode) IsCharDev() bool { return in.Mode&modeTypeMask == modeChar }

// IsWhiteout：字符设备且设备号 0:0，即 overlayfs 的 whiteout。
func (in *Inode) IsWhiteout() bool { return in.IsCharDev() && in.RdevMajor == 0 && in.RdevMinor == 0 }

func (in *Inode) hasExtents() bool    { return in.flags&flagExtents != 0 }
func (in *Inode) hasInlineData() bool { return in.flags&flagInlineData != 0 }

// Inode 读取并解析第 ino 号 inode；ino 越界或所在组 inode 表未初始化 → 错误。
func (f *FS) Inode(ino uint32) (in *Inode, err error) {
	defer recoverErr(&err)
	if ino == 0 || ino > f.inodesCount {
		return nil, fmt.Errorf("ext4ro: inode %d out of range (1..%d)", ino, f.inodesCount)
	}
	g := (ino - 1) / f.inodesPerGroup
	idx := (ino - 1) % f.inodesPerGroup
	gd, err := f.groupDesc(g)
	if err != nil {
		return nil, err
	}
	// INODE_UNINIT 只在有组校验和时有效；此时 inode 表可能是未清零的旧数据，不能当真。
	if gd.flags&bgInodeUninit != 0 && f.featRoCompat&(roCompatGdtCsum|roCompatMetadataCsum) != 0 {
		return nil, corrupt("inode %d in uninitialized group %d", ino, g)
	}
	byteOff := uint64(idx) * uint64(f.inodeSize)
	blk := gd.inodeTable + byteOff/uint64(f.blockSize)
	if blk < gd.inodeTable {
		return nil, corrupt("inode table block overflow")
	}
	raw := make([]byte, f.inodeSize)
	if err := f.readBlockRange(blk, uint32(byteOff%uint64(f.blockSize)), raw); err != nil {
		return nil, err
	}
	return f.parseInode(ino, raw)
}

func (f *FS) parseInode(ino uint32, raw []byte) (*Inode, error) {
	le := binary.LittleEndian
	in := &Inode{
		Ino:     ino,
		Mode:    le.Uint16(raw[0x0:]),
		UID:     uint32(le.Uint16(raw[0x2:])) | uint32(le.Uint16(raw[0x78:]))<<16,
		GID:     uint32(le.Uint16(raw[0x18:])) | uint32(le.Uint16(raw[0x7A:]))<<16,
		flags:   le.Uint32(raw[0x20:]),
		fileACL: uint64(le.Uint32(raw[0x68:])) | uint64(le.Uint16(raw[0x76:]))<<32,
		raw:     raw,
	}
	size := uint64(le.Uint32(raw[0x4:])) | uint64(le.Uint32(raw[0x6C:]))<<32
	if size > 1<<62 {
		return nil, corrupt("inode %d size %d too large", ino, size)
	}
	in.Size = int64(size)
	copy(in.block[:], raw[0x28:0x28+60])

	if f.inodeSize > goodOldInodeSize {
		in.extraIsize = uint32(le.Uint16(raw[0x80:]))
		if in.extraIsize&3 != 0 || goodOldInodeSize+in.extraIsize > f.inodeSize {
			return nil, corrupt("inode %d bad extra_isize %d", ino, in.extraIsize)
		}
	}
	sec := int64(int32(le.Uint32(raw[0x10:])))
	var nsec int64
	// i_mtime_extra 位于 0x88，需 extra_isize 覆盖到 0x8C：低 2 位是 epoch 扩展，高 30 位是纳秒。
	if goodOldInodeSize+in.extraIsize >= 0x8C {
		extra := le.Uint32(raw[0x88:])
		sec += int64(extra&3) << 32
		nsec = int64(extra >> 2)
		if nsec > 999_999_999 {
			nsec = 0
		}
	}
	in.Mtime = time.Unix(sec, nsec)

	if t := in.Mode & modeTypeMask; t == modeChar || t == modeBlock {
		if b0 := le.Uint32(in.block[0:]); b0 != 0 {
			in.RdevMajor, in.RdevMinor = (b0>>8)&0xFF, b0&0xFF
		} else {
			b1 := le.Uint32(in.block[4:])
			in.RdevMajor = (b1 & 0xFFF00) >> 8
			in.RdevMinor = (b1 & 0xFF) | ((b1 >> 12) & 0xFFF00)
		}
	}
	return in, nil
}
