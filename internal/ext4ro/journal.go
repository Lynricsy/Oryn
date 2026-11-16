package ext4ro

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// 只读 jbd2 日志重放。guest 关机只 sync 不 umount 时，最近的元数据只在日志里：不重放就看不到新文件。
// 语义照内核 fs/jbd2/recovery.c 的 do_one_pass：从 s_start / s_sequence 顺序扫描到最后一个已提交事务，
// revoke 撤销「所在事务及之前」对该块的记录，结果是覆盖层而不是写回镜像。校验和一律不验证。

const (
	jbdMagic = 0xC03B3998

	jbdDescriptor = 1
	jbdCommit     = 2
	jbdSuperV1    = 3
	jbdSuperV2    = 4
	jbdRevoke     = 5

	jbdFeatRevoke      = 0x1
	jbdFeat64Bit       = 0x2
	jbdFeatAsyncCommit = 0x4
	jbdFeatCsumV2      = 0x8
	jbdFeatCsumV3      = 0x10
	jbdFeatFastCommit  = 0x20
	jbdFeatKnown       = jbdFeatRevoke | jbdFeat64Bit | jbdFeatAsyncCommit | jbdFeatCsumV2 | jbdFeatCsumV3 | jbdFeatFastCommit

	jbdTagEscape   = 0x1
	jbdTagSameUUID = 0x2
	jbdTagLast     = 0x8

	jbdHeaderSize       = 12
	jbdRevokeHeaderSize = 16
	jbdDefaultFCBlocks  = 256

	// maxJournalBlocks：日志长度上限（4K 块时 16GiB，远大于 mke2fs 的默认值），限制扫描量与映射内存。
	maxJournalBlocks = 1 << 22
)

// journalCopy 是某个文件系统块在日志里的最新副本。
type journalCopy struct {
	pblk    uint64 // 副本所在的镜像物理块
	escaped bool   // 首 4 字节需还原为 jbd2 magic
}

// journalTag 是 descriptor 里的一条记录：fs 块 fsBlk 的新内容在日志逻辑块 jBlk。
type journalTag struct {
	fsBlk   uint64
	jBlk    uint32
	seq     uint32
	escaped bool
}

// tidGT 对应内核 tid_gt：事务号按 32 位回绕比较。
func tidGT(x, y uint32) bool { return int32(x-y) > 0 }

// applyJournal 尝试重放并建立覆盖层；任何失败都退回未重放视图（只记 replayErr）。
func (f *FS) applyJournal() {
	overlay, err := f.replayJournal()
	f.mu.Lock()
	clear(f.groupCache) // 重放前读进来的组描述符是旧的
	f.mu.Unlock()
	if err == nil {
		f.overlay, f.replayed = overlay, true
		// 覆盖层让根 inode 不可读说明日志内容不可信：宁可退回未重放视图。
		if _, err = f.Inode(RootIno); err != nil {
			f.overlay, f.replayed = nil, false
			f.mu.Lock()
			clear(f.groupCache)
			f.mu.Unlock()
		}
	}
	f.replayErr = err
}

func (f *FS) replayJournal() (overlay map[uint64]journalCopy, err error) {
	defer recoverErr(&err)
	if f.featCompat&compatHasJournal == 0 {
		return nil, fmt.Errorf("%w: needs_recovery without has_journal", ErrUnsupported)
	}
	if f.journalInum == 0 {
		return nil, fmt.Errorf("%w: external journal device", ErrUnsupported)
	}
	jin, err := f.Inode(f.journalInum)
	if err != nil {
		return nil, fmt.Errorf("ext4ro: journal inode: %w", err)
	}
	jext, err := f.loadExtents(jin)
	if err != nil {
		return nil, fmt.Errorf("ext4ro: journal inode: %w", err)
	}
	bs := f.blockSize
	// jmap：日志逻辑块 → 镜像物理块。日志必须完整分配：空洞 / 未初始化都算损坏。
	jmap := func(lb uint32) (uint64, error) {
		l := uint64(lb)
		i := sort.Search(len(jext), func(i int) bool { return jext[i].lblk+jext[i].length > l })
		if i == len(jext) || jext[i].lblk > l || jext[i].uninit {
			return 0, corrupt("journal block %d not mapped", lb)
		}
		return jext[i].pblk + (l - jext[i].lblk), nil
	}
	buf := make([]byte, bs)
	readJ := func(lb uint32) error {
		pb, err := jmap(lb)
		if err != nil {
			return err
		}
		// 日志块本身从镜像原样读（日志不覆盖日志）。
		return f.readRaw(buf, int64(pb)*int64(bs))
	}

	be := binary.BigEndian
	if err := readJ(0); err != nil {
		return nil, err
	}
	if be.Uint32(buf[0:]) != jbdMagic {
		return nil, corrupt("bad journal superblock magic %#x", be.Uint32(buf[0:]))
	}
	var incompat uint32
	switch be.Uint32(buf[4:]) {
	case jbdSuperV1:
	case jbdSuperV2:
		incompat = be.Uint32(buf[0x28:])
	default:
		return nil, corrupt("bad journal superblock type %d", be.Uint32(buf[4:]))
	}
	if be.Uint32(buf[0xC:]) != bs {
		return nil, corrupt("journal block size %d != fs block size %d", be.Uint32(buf[0xC:]), bs)
	}
	if incompat&^jbdFeatKnown != 0 {
		return nil, fmt.Errorf("%w: journal incompat features %#x", ErrUnsupported, incompat&^jbdFeatKnown)
	}
	maxLen := be.Uint32(buf[0x10:])
	first := be.Uint32(buf[0x14:])
	seq := be.Uint32(buf[0x18:])
	start := be.Uint32(buf[0x1C:])
	if maxLen > maxJournalBlocks || uint64(maxLen) > uint64(jin.Size)/uint64(bs) {
		return nil, corrupt("journal maxlen %d exceeds journal inode (%d bytes)", maxLen, jin.Size)
	}
	// 常规事务区是 [first, last)；快速提交区在末尾，不重放。
	last := maxLen
	if incompat&jbdFeatFastCommit != 0 {
		fc := be.Uint32(buf[0x54:])
		if fc == 0 {
			fc = jbdDefaultFCBlocks
		}
		if fc >= maxLen {
			return nil, corrupt("journal fast commit blocks %d >= maxlen %d", fc, maxLen)
		}
		last = maxLen - fc
	}
	if first == 0 || first >= last {
		return nil, corrupt("journal first %d / last %d", first, last)
	}
	if start == 0 {
		return map[uint64]journalCopy{}, nil // 日志干净
	}
	if start < first || start >= last {
		return nil, corrupt("journal start %d outside [%d, %d)", start, first, last)
	}

	var tagBytes, tailBytes uint32
	switch {
	case incompat&jbdFeatCsumV3 != 0:
		tagBytes = 16
	default:
		tagBytes = 8
		if incompat&jbdFeatCsumV2 != 0 {
			tagBytes += 2
		}
		if incompat&jbdFeat64Bit != 0 {
			tagBytes += 4
		}
	}
	if incompat&(jbdFeatCsumV2|jbdFeatCsumV3) != 0 {
		tailBytes = 4
	}
	is64 := incompat&jbdFeat64Bit != 0

	var committed, pending []journalTag
	revoked := map[uint64]uint32{} // fs 块 → 撤销它的最大事务号（仅已提交事务）
	type revokeRec struct {
		blk uint64
		seq uint32
	}
	var pendingRevokes []revokeRec

	ring := last - first
	blk := start
	var scanned uint32
	next := func() {
		blk++
		if blk >= last {
			blk = first
		}
		scanned++
	}
scan:
	for scanned < ring {
		if err := readJ(blk); err != nil {
			return nil, err
		}
		if be.Uint32(buf[0:]) != jbdMagic || be.Uint32(buf[8:]) != seq {
			break // 日志尾：magic 不符或事务号不连续
		}
		next()
		switch be.Uint32(buf[4:]) {
		case jbdDescriptor:
			limit := bs - tailBytes
			for off := uint32(jbdHeaderSize); off+tagBytes <= limit; {
				t := buf[off:]
				var fsBlk uint64
				var flags uint32
				if tagBytes == 16 {
					fsBlk, flags = uint64(be.Uint32(t[0:])), be.Uint32(t[4:])
					if is64 {
						fsBlk |= uint64(be.Uint32(t[8:])) << 32
					}
				} else {
					fsBlk, flags = uint64(be.Uint32(t[0:])), uint32(be.Uint16(t[6:]))
					if is64 {
						fsBlk |= uint64(be.Uint32(t[8:])) << 32
					}
				}
				if scanned >= ring {
					break scan // 数据块越过整个环：日志损坏，按尾部处理
				}
				if len(committed)+len(pending) >= maxJournalBlocks {
					return nil, corrupt("too many journal tags")
				}
				pending = append(pending, journalTag{fsBlk: fsBlk, jBlk: blk, seq: seq, escaped: flags&jbdTagEscape != 0})
				next()
				off += tagBytes
				if flags&jbdTagSameUUID == 0 {
					off += 16
				}
				if flags&jbdTagLast != 0 {
					break
				}
			}
		case jbdCommit:
			committed = append(committed, pending...)
			pending = pending[:0]
			for _, r := range pendingRevokes {
				if old, ok := revoked[r.blk]; !ok || tidGT(r.seq, old) {
					revoked[r.blk] = r.seq
				}
			}
			pendingRevokes = pendingRevokes[:0]
			seq++
		case jbdRevoke:
			rcount := be.Uint32(buf[jbdHeaderSize:])
			if rcount > bs-tailBytes {
				return nil, corrupt("journal revoke r_count %d", rcount)
			}
			recLen := uint32(4)
			if is64 {
				recLen = 8
			}
			for off := uint32(jbdRevokeHeaderSize); off+recLen <= rcount; off += recLen {
				var b uint64
				if is64 {
					b = be.Uint64(buf[off:])
				} else {
					b = uint64(be.Uint32(buf[off:]))
				}
				if len(pendingRevokes) >= maxJournalBlocks {
					return nil, corrupt("too many journal revokes")
				}
				pendingRevokes = append(pendingRevokes, revokeRec{b, seq})
			}
		default:
			break scan
		}
	}

	overlay = map[uint64]journalCopy{}
	for _, t := range committed {
		if r, ok := revoked[t.fsBlk]; ok && !tidGT(t.seq, r) {
			continue
		}
		if t.fsBlk >= f.blocksCount {
			return nil, corrupt("journal tag block %d beyond blocks count", t.fsBlk)
		}
		pb, err := jmap(t.jBlk)
		if err != nil {
			return nil, err
		}
		overlay[t.fsBlk] = journalCopy{pblk: pb, escaped: t.escaped}
	}
	return overlay, nil
}
