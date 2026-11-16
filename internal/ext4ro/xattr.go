package ext4ro

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	xattrMagic       = 0xEA020000
	xattrBlockHeader = 32 // 外部块头：magic refcount blocks hash checksum reserved[3]
	xattrEntryHeader = 16 // name_len name_index value_offs value_inum value_size hash
)

// xattrPrefixes：e_name_index → 名字前缀。2 / 3 / 8 的完整名就是前缀本身（e_name_len 为 0）。
var xattrPrefixes = map[uint8]string{
	1: "user.",
	2: "system.posix_acl_access",
	3: "system.posix_acl_default",
	4: "trusted.",
	6: "security.",
	7: "system.",
	8: "system.richacl",
}

// Xattr 取扩展属性（完整名，如 "trusted.overlay.opaque"）。先查 inode 内（128+i_extra_isize 之后），
// 再查外部块（i_file_acl）。值存放在独立 inode（ea_inode）→ ErrUnsupported。found=false 表示没有该属性。
func (f *FS) Xattr(in *Inode, name string) (val []byte, found bool, err error) {
	defer recoverErr(&err)
	le := binary.LittleEndian
	if start := goodOldInodeSize + int(in.extraIsize); int(f.inodeSize) > goodOldInodeSize && start+4 <= len(in.raw) &&
		le.Uint32(in.raw[start:]) == xattrMagic {
		// inode 内：条目紧随 magic，值偏移相对第一个条目。
		region := in.raw[start+4:]
		if val, found, err = searchXattr(region, region, name); found || err != nil {
			return val, found, err
		}
	}
	if in.fileACL == 0 {
		return nil, false, nil
	}
	blk := make([]byte, f.blockSize)
	if err := f.readBlock(in.fileACL, blk); err != nil {
		return nil, false, fmt.Errorf("ext4ro: xattr block of inode %d: %w", in.Ino, err)
	}
	if le.Uint32(blk[0:]) != xattrMagic {
		return nil, false, corrupt("inode %d xattr block bad magic %#x", in.Ino, le.Uint32(blk[0:]))
	}
	if n := le.Uint32(blk[8:]); n != 1 {
		return nil, false, corrupt("inode %d xattr block h_blocks %d", in.Ino, n)
	}
	// 外部块：条目从块头之后开始，值偏移相对块起点。
	return searchXattr(blk[xattrBlockHeader:], blk, name)
}

// searchXattr 在 entries 中找 name；值从 values[value_offs:] 取。条目表以 4 字节 0 结束。
func searchXattr(entries, values []byte, name string) ([]byte, bool, error) {
	le := binary.LittleEndian
	for off := 0; off+4 <= len(entries) && le.Uint32(entries[off:]) != 0; {
		if off+xattrEntryHeader > len(entries) {
			return nil, false, corrupt("xattr entry header truncated")
		}
		e := entries[off:]
		nameLen := int(e[0])
		if off+xattrEntryHeader+nameLen > len(entries) {
			return nil, false, corrupt("xattr entry name truncated")
		}
		if prefix, ok := xattrPrefixes[e[1]]; ok && matchXattrName(name, prefix, e[xattrEntryHeader:xattrEntryHeader+nameLen]) {
			if le.Uint32(e[4:]) != 0 {
				return nil, false, fmt.Errorf("%w: xattr %q value in ea_inode", ErrUnsupported, name)
			}
			vOff, vSize := uint64(le.Uint16(e[2:])), uint64(le.Uint32(e[8:]))
			if vOff+vSize > uint64(len(values)) {
				return nil, false, corrupt("xattr %q value out of bounds", name)
			}
			return bytes.Clone(values[vOff : vOff+vSize]), true, nil
		}
		off += (xattrEntryHeader + nameLen + 3) &^ 3
	}
	return nil, false, nil
}

func matchXattrName(full, prefix string, suffix []byte) bool {
	return len(full) == len(prefix)+len(suffix) && full[:len(prefix)] == prefix && full[len(prefix):] == string(suffix)
}
