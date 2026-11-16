package ext4ro

import (
	"encoding/binary"
	"fmt"
	"path"
	"strings"
)

const (
	// maxDirEntries：单目录条目数上限。
	maxDirEntries = 1_000_000
	// maxSymlinkLen：符号链接目标上限（PATH_MAX）。
	maxSymlinkLen = 4096
)

// DirEntry 是一条目录项。
type DirEntry struct {
	Name string
	Ino  uint32
	Type uint8 // 目录项 file_type（1 普通 2 目录 3 字符设备 4 块设备 5 FIFO 6 socket 7 符号链接）
}

// ReadDir 线性扫描目录全部数据块，返回除 "." 与 ".." 外的条目。htree 的 dx_root / dx_node 伪目录项与
// metadata_csum 的尾部伪项 inode 都是 0（或藏在 ".." 的 rec_len 里），线性扫描自然跳过；叶子块里有全部条目。
func (f *FS) ReadDir(in *Inode) (ents []DirEntry, err error) {
	defer recoverErr(&err)
	overflow := false
	err = f.walkDir(in, func(e DirEntry) bool {
		if len(ents) >= maxDirEntries {
			overflow = true
			return false
		}
		ents = append(ents, e)
		return true
	})
	if err != nil {
		return nil, err
	}
	if overflow {
		return nil, fmt.Errorf("ext4ro: directory inode %d has more than %d entries", in.Ino, maxDirEntries)
	}
	return ents, nil
}

// walkDir 逐块读目录并对每个条目调用 fn；fn 返回 false 时提前结束。
func (f *FS) walkDir(in *Inode, fn func(DirEntry) bool) error {
	if !in.IsDir() {
		return ErrNotDir
	}
	fl, err := f.openData(in)
	if err != nil {
		return err
	}
	bs := uint64(f.blockSize)
	nblocks := (uint64(in.Size) + bs - 1) / bs
	buf := make([]byte, bs)
	for _, e := range fl.extents {
		if e.lblk >= nblocks {
			break
		}
		if e.uninit {
			continue // 未初始化 extent 读为零：没有有效条目
		}
		n := min(e.length, nblocks-e.lblk)
		for i := range n {
			if err := f.readBlock(e.pblk+i, buf); err != nil {
				return err
			}
			more, err := f.parseDirBlock(buf, in.Ino, fn)
			if err != nil {
				return err
			}
			if !more {
				return nil
			}
		}
	}
	return nil
}

// parseDirBlock 解析一个目录块。rec_len 非法（<8、未按 4 对齐、越过块尾、容不下 name_len）→ 错误。
func (f *FS) parseDirBlock(b []byte, dirIno uint32, fn func(DirEntry) bool) (bool, error) {
	le := binary.LittleEndian
	bs := len(b)
	for off := 0; off < bs; {
		if bs-off < 8 {
			return false, corrupt("dir inode %d: truncated entry at %d", dirIno, off)
		}
		ino := le.Uint32(b[off:])
		recLen := int(le.Uint16(b[off+4:]))
		// 64K 块时 rec_len 用 0 / 65535 表示 65536（内核 ext4_rec_len_from_disk）。
		if bs == 65536 && (recLen == 0 || recLen == 65535) {
			recLen = 65536
		}
		nameLen := int(b[off+6])
		ftype := b[off+7]
		if recLen < 8 || recLen%4 != 0 || off+recLen > bs || 8+nameLen > recLen {
			return false, corrupt("dir inode %d: bad rec_len %d (name_len %d) at %d", dirIno, recLen, nameLen, off)
		}
		if ino != 0 {
			name := string(b[off+8 : off+8+nameLen])
			if name != "." && name != ".." {
				if ino > f.inodesCount {
					return false, corrupt("dir inode %d: entry %q inode %d out of range", dirIno, name, ino)
				}
				if !fn(DirEntry{Name: name, Ino: ino, Type: ftype}) {
					return false, nil
				}
			}
		}
		off += recLen
	}
	return true, nil
}

// Lookup 从根按绝对路径逐级查找，不跟随符号链接（中间分量是符号链接或文件 → ErrNotDir）。
// 路径先按字面 Clean（".." 按字面回退，因为不跟随符号链接，这与按目录树回退一致）。
func (f *FS) Lookup(p string) (in *Inode, err error) {
	defer recoverErr(&err)
	if !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("ext4ro: lookup %q: path must be absolute", p)
	}
	cur, err := f.Inode(RootIno)
	if err != nil {
		return nil, err
	}
	clean := path.Clean(p)
	if clean == "/" {
		return cur, nil
	}
	for _, name := range strings.Split(clean[1:], "/") {
		if !cur.IsDir() {
			return nil, fmt.Errorf("ext4ro: lookup %q: %w", p, ErrNotDir)
		}
		var found uint32
		err := f.walkDir(cur, func(e DirEntry) bool {
			if e.Name == name {
				found = e.Ino
				return false
			}
			return true
		})
		if err != nil {
			return nil, fmt.Errorf("ext4ro: lookup %q: %w", p, err)
		}
		if found == 0 {
			return nil, fmt.Errorf("ext4ro: lookup %q: %w", p, ErrNotExist)
		}
		if cur, err = f.Inode(found); err != nil {
			return nil, fmt.Errorf("ext4ro: lookup %q: %w", p, err)
		}
	}
	return cur, nil
}

// Readlink 读符号链接目标：快速链接（无 EXTENTS_FL 且 size<60）内容在 i_block，其余经 extent 读数据块。
func (f *FS) Readlink(in *Inode) (target string, err error) {
	defer recoverErr(&err)
	if !in.IsSymlink() {
		return "", fmt.Errorf("ext4ro: inode %d is not a symlink", in.Ino)
	}
	if in.Size > maxSymlinkLen {
		return "", corrupt("symlink inode %d size %d > %d", in.Ino, in.Size, maxSymlinkLen)
	}
	if in.hasInlineData() {
		return "", fmt.Errorf("%w: inline data symlink (inode %d)", ErrUnsupported, in.Ino)
	}
	if in.Size == 0 {
		return "", nil
	}
	if !in.hasExtents() && in.Size < int64(len(in.block)) {
		return string(in.block[:in.Size]), nil
	}
	fl, err := f.openData(in)
	if err != nil {
		return "", err
	}
	buf := make([]byte, in.Size)
	if _, err := fl.ReadAt(buf, 0); err != nil {
		return "", err
	}
	return string(buf), nil
}
