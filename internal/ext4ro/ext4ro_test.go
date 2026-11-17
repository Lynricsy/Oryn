package ext4ro

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// 与真实 orb 镜像一致的特性集。
const testFeatures = "has_journal,ext_attr,resize_inode,dir_index,orphan_file,filetype,extent,64bit,flex_bg," +
	"metadata_csum_seed,sparse_super,large_file,huge_file,dir_nlink,extra_isize,metadata_csum"

// fixture 记录源目录里实际做成了哪些东西（xattr / mknod 可能因权限或文件系统不支持而跳过）。
type fixture struct {
	img       string
	files     map[string][]byte // 绝对路径 → 内容
	bigNames  []string
	slowLink  string
	mtime     time.Time
	userXattr bool
	bigXattr  []byte
	trusted   bool
	mknod     bool
	htree     bool // e2fsck -D 是否可用（把大目录转成 htree）
	nanos     bool // debugfs 是否写入了 i_mtime_extra
}

func randBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func buildFixture(t *testing.T) *fixture {
	t.Helper()
	mke2fs, err := exec.LookPath("mke2fs")
	if err != nil {
		t.Skip("mke2fs not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	fx := &fixture{img: filepath.Join(tmp, "img.ext4"), files: map[string][]byte{}}
	r := rand.New(rand.NewPCG(1, 2))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel string, data []byte) {
		p := filepath.Join(src, rel)
		must(os.MkdirAll(filepath.Dir(p), 0o755))
		must(os.WriteFile(p, data, 0o644))
		fx.files["/"+rel] = data
	}
	must(os.MkdirAll(src, 0o755))
	write("empty.txt", nil)
	write("small.txt", []byte("hello\n"))
	write("multi.bin", randBytes(r, 3*4096+123))
	write("a/b/c/deep.txt", []byte("deep\n"))
	write("deep-holder", []byte("hold")) // 日志测试：被撤销后又重写的数据块

	// 稀疏文件：10 段数据被 1MiB 级空洞隔开，末尾再留空洞，迫使 extent 树深度 ≥ 1（根只容 4 个 extent）。
	const seg = 5000
	sparseSize := int64(11<<20 + 300)
	sparse := make([]byte, sparseSize)
	for i := range 10 {
		off := i<<20 + i*517
		copy(sparse[off:], randBytes(r, seg))
	}
	fh, err := os.Create(filepath.Join(src, "sparse.bin"))
	must(err)
	for i := range 10 {
		off := int64(i<<20 + i*517)
		_, err := fh.WriteAt(sparse[off:off+seg], off)
		must(err)
	}
	must(fh.Truncate(sparseSize))
	must(fh.Close())
	fx.files["/sparse.bin"] = sparse

	// 600+ 条目的大目录，名字含 UTF-8，占多个块。
	for i := range 700 {
		name := fmt.Sprintf("文件-ünïcødé-%04d", i)
		fx.bigNames = append(fx.bigNames, name)
		write("big/"+name, []byte(name))
	}

	must(os.Symlink("small.txt", filepath.Join(src, "link-fast")))
	must(os.Symlink("a", filepath.Join(src, "link-dir")))
	fx.slowLink = strings.Repeat("x/", 50) + "target"
	must(os.Symlink(fx.slowLink, filepath.Join(src, "link-slow")))
	// mke2fs -d 不拷贝纳秒：秒数在这里设，纳秒与 epoch 位稍后用 debugfs 写进 i_mtime_extra。
	fx.mtime = time.Unix(1_700_000_000, 0)
	must(os.Chtimes(filepath.Join(src, "small.txt"), fx.mtime, fx.mtime))

	// xattr：小值放得进 inode；1000 字节放不下，只能进外部块。
	write("xa.txt", []byte("a"))
	write("xb.txt", []byte("b"))
	fx.bigXattr = randBytes(r, 1000)
	if err := unix.Setxattr(filepath.Join(src, "xa.txt"), "user.small", []byte("v1"), 0); err == nil {
		must(unix.Setxattr(filepath.Join(src, "xb.txt"), "user.big", fx.bigXattr, 0))
		must(unix.Setxattr(filepath.Join(src, "xb.txt"), "user.s", []byte("s"), 0))
		fx.userXattr = true
	} else if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EPERM) {
		t.Fatal(err)
	}
	must(os.Mkdir(filepath.Join(src, "opq"), 0o755))
	if err := unix.Setxattr(filepath.Join(src, "opq"), "trusted.overlay.opaque", []byte("y"), 0); err == nil {
		fx.trusted = true
	} else if !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.ENOTSUP) {
		t.Fatal(err)
	}

	// 先用普通设备探测权限：Linux 允许无 CAP_MKNOD 创建 0:0 whiteout，不能用它判断其余设备是否可建。
	if err := unix.Mknod(filepath.Join(src, "null"), unix.S_IFCHR|0o666, int(unix.Mkdev(1, 3))); err == nil {
		must(unix.Mknod(filepath.Join(src, "wh"), unix.S_IFCHR|0o600, 0))
		must(unix.Mknod(filepath.Join(src, "bigdev"), unix.S_IFBLK|0o600, int(unix.Mkdev(259, 300000))))
		fx.mknod = true
	} else if !errors.Is(err, unix.EPERM) {
		t.Fatal(err)
	}

	out, err := exec.Command(mke2fs, "-q", "-F", "-t", "ext4", "-b", "4096", "-I", "256",
		"-O", testFeatures, "-d", src, fx.img, "64M").CombinedOutput()
	if err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}
	// e2fsck -D 重建目录索引，让大目录变成 htree（dx_root + 叶子块），覆盖线性扫描跳过伪项的路径。
	if e2fsck, err := exec.LookPath("e2fsck"); err == nil {
		out, err := exec.Command(e2fsck, "-fyD", fx.img).CombinedOutput()
		var ee *exec.ExitError
		if err != nil && !(errors.As(err, &ee) && ee.ExitCode() < 4) {
			t.Fatalf("e2fsck: %v\n%s", err, out)
		}
		fx.htree = true
	}
	// i_mtime_extra = 纳秒<<2 | epoch 1：秒数再加 2^32（2038 之后的时间戳编码）。
	if debugfs, err := exec.LookPath("debugfs"); err == nil {
		extra := uint32(123_456_789)<<2 | 1
		out, err := exec.Command(debugfs, "-w", "-R", fmt.Sprintf("sif /small.txt mtime_extra %#x", extra), fx.img).CombinedOutput()
		if err != nil {
			t.Fatalf("debugfs: %v\n%s", err, out)
		}
		fx.mtime = time.Unix(1_700_000_000+1<<32, 123_456_789)
		fx.nanos = true
	}
	return fx
}

func openImage(t *testing.T, img string) *FS {
	t.Helper()
	fh, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fh.Close() })
	st, err := fh.Stat()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := Open(fh, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func lookup(t *testing.T, fs *FS, p string) *Inode {
	t.Helper()
	in, err := fs.Lookup(p)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", p, err)
	}
	return in
}

func TestImage(t *testing.T) {
	fx := buildFixture(t)
	fs := openImage(t, fx.img)
	if fs.NeedsRecovery() {
		t.Error("fresh image needs recovery")
	}

	t.Run("files", func(t *testing.T) {
		for p, want := range fx.files {
			in := lookup(t, fs, p)
			if !in.IsRegular() || in.Size != int64(len(want)) {
				t.Fatalf("%s: regular=%v size=%d want %d", p, in.IsRegular(), in.Size, len(want))
			}
			fl, err := fs.Open(in)
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			got, err := io.ReadAll(io.NewSectionReader(fl, 0, fl.Size()))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s: content mismatch (err %v, len %d)", p, err, len(got))
			}
		}
	})

	t.Run("readat", func(t *testing.T) {
		r := rand.New(rand.NewPCG(3, 4))
		for _, p := range []string{"/empty.txt", "/small.txt", "/multi.bin", "/sparse.bin"} {
			want := fx.files[p]
			fl, err := fs.Open(lookup(t, fs, p))
			if err != nil {
				t.Fatal(err)
			}
			size := int64(len(want))
			for range 300 {
				off := r.Int64N(size + 9000)
				buf := make([]byte, r.IntN(20000))
				n, err := fl.ReadAt(buf, off)
				wantN := int(max(0, min(int64(len(buf)), size-off)))
				if n != wantN {
					t.Fatalf("%s ReadAt(%d, %d) n=%d want %d", p, len(buf), off, n, wantN)
				}
				if n < len(buf) && err != io.EOF {
					t.Fatalf("%s short read without EOF: %v", p, err)
				}
				// io.ReaderAt 允许读满时在文件尾附带 io.EOF。
				if n == len(buf) && err != nil && (err != io.EOF || off+int64(n) < size) {
					t.Fatalf("%s full read with error: %v", p, err)
				}
				if n > 0 && !bytes.Equal(buf[:n], want[off:off+int64(n)]) {
					t.Fatalf("%s ReadAt(%d, %d) content mismatch", p, len(buf), off)
				}
			}
			if _, err := fl.ReadAt(make([]byte, 1), -1); err == nil {
				t.Fatalf("%s negative offset: no error", p)
			}
		}
	})

	t.Run("sparse", func(t *testing.T) {
		in := lookup(t, fs, "/sparse.bin")
		if depth := binary.LittleEndian.Uint16(in.block[6:]); depth < 1 {
			t.Fatalf("extent depth %d, want >= 1", depth)
		}
		fl, err := fs.Open(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(fl.extents) < 8 {
			t.Fatalf("%d extents, want >= 8", len(fl.extents))
		}
		// 空洞中间与文件尾部空洞读零。
		for _, off := range []int64{4 << 20, 11<<20 + 100} {
			buf := []byte{1, 2, 3}
			if _, err := fl.ReadAt(buf, off); err != nil || !bytes.Equal(buf, []byte{0, 0, 0}) {
				t.Fatalf("hole at %d: %v %v", off, buf, err)
			}
		}
	})

	t.Run("dirs", func(t *testing.T) {
		root := lookup(t, fs, "/")
		if root.Ino != RootIno || !root.IsDir() {
			t.Fatalf("root ino %d dir %v", root.Ino, root.IsDir())
		}
		ents, err := fs.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]DirEntry{}
		for _, e := range ents {
			names[e.Name] = e
		}
		for _, n := range []string{"empty.txt", "small.txt", "sparse.bin", "big", "a", "link-fast", "lost+found"} {
			if _, ok := names[n]; !ok {
				t.Errorf("root missing %q", n)
			}
		}
		if _, ok := names["."]; ok {
			t.Error("ReadDir returned .")
		}
		if _, ok := names[".."]; ok {
			t.Error("ReadDir returned ..")
		}
		if names["big"].Type != 2 || names["small.txt"].Type != 1 || names["link-fast"].Type != 7 {
			t.Errorf("bad file types: %+v", names)
		}

		big := lookup(t, fs, "/big/")
		if big.Size <= int64(fs.blockSize) {
			t.Fatalf("big dir size %d not multi-block", big.Size)
		}
		if fx.htree && big.flags&flagIndex == 0 {
			t.Error("big dir not htree-indexed after e2fsck -D")
		}
		ents, err = fs.ReadDir(big)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range ents {
			got = append(got, e.Name)
		}
		sort.Strings(got)
		if strings.Join(got, "\n") != strings.Join(fx.bigNames, "\n") {
			t.Fatalf("big dir: %d entries, want %d", len(got), len(fx.bigNames))
		}
		in := lookup(t, fs, "/big/"+fx.bigNames[699])
		fl, err := fs.Open(in)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, in.Size)
		if _, err := fl.ReadAt(buf, 0); err != nil || string(buf) != fx.bigNames[699] {
			t.Fatalf("big entry content %q %v", buf, err)
		}

		lookup(t, fs, "/a/b/c/deep.txt")
		lookup(t, fs, "/a/../a/./b//c/deep.txt")
		for p, want := range map[string]error{
			"/nope":            ErrNotExist,
			"/a/b/nope":        ErrNotExist,
			"/small.txt/x":     ErrNotDir,
			"/link-dir/b":      ErrNotDir,
			"/big/missing-一二三": ErrNotExist,
		} {
			if _, err := fs.Lookup(p); !errors.Is(err, want) {
				t.Errorf("Lookup(%q) = %v, want %v", p, err, want)
			}
		}
		if _, err := fs.Lookup("small.txt"); err == nil {
			t.Error("relative Lookup succeeded")
		}
		if _, err := fs.ReadDir(lookup(t, fs, "/small.txt")); !errors.Is(err, ErrNotDir) {
			t.Errorf("ReadDir(file) = %v", err)
		}
		if _, err := fs.Open(big); err == nil {
			t.Error("Open(dir) succeeded")
		}
		if _, err := fs.Inode(0); err == nil {
			t.Error("Inode(0) succeeded")
		}
		if _, err := fs.Inode(fs.inodesCount + 1); err == nil {
			t.Error("Inode(out of range) succeeded")
		}
	})

	t.Run("symlinks", func(t *testing.T) {
		for p, want := range map[string]string{"/link-fast": "small.txt", "/link-dir": "a", "/link-slow": fx.slowLink} {
			in := lookup(t, fs, p)
			if !in.IsSymlink() {
				t.Fatalf("%s not a symlink (Lookup must not follow)", p)
			}
			got, err := fs.Readlink(in)
			if err != nil || got != want {
				t.Fatalf("Readlink(%s) = %q, %v; want %q", p, got, err, want)
			}
		}
		if !lookup(t, fs, "/link-slow").hasExtents() || lookup(t, fs, "/link-fast").hasExtents() {
			t.Error("fast/slow symlink layout not as expected")
		}
	})

	t.Run("xattr", func(t *testing.T) {
		if !fx.userXattr {
			t.Skip("user xattrs unsupported on source fs")
		}
		xa := lookup(t, fs, "/xa.txt")
		if xa.fileACL != 0 {
			t.Error("small xattr unexpectedly in external block")
		}
		if v, ok, err := fs.Xattr(xa, "user.small"); err != nil || !ok || string(v) != "v1" {
			t.Fatalf("in-inode xattr: %q %v %v", v, ok, err)
		}
		xb := lookup(t, fs, "/xb.txt")
		if xb.fileACL == 0 {
			t.Fatal("big xattr not in external block")
		}
		if v, ok, err := fs.Xattr(xb, "user.big"); err != nil || !ok || !bytes.Equal(v, fx.bigXattr) {
			t.Fatalf("external xattr: len %d %v %v", len(v), ok, err)
		}
		if v, ok, err := fs.Xattr(xb, "user.s"); err != nil || !ok || string(v) != "s" {
			t.Fatalf("second xattr: %q %v %v", v, ok, err)
		}
		for _, name := range []string{"user.missing", "user.smal", "trusted.small", "security.selinux"} {
			if _, ok, err := fs.Xattr(xa, name); err != nil || ok {
				t.Errorf("Xattr(%q) = %v, %v; want not found", name, ok, err)
			}
		}
	})

	t.Run("trusted-xattr", func(t *testing.T) {
		if !fx.trusted {
			t.Skip("trusted xattr needs root")
		}
		if v, ok, err := fs.Xattr(lookup(t, fs, "/opq"), "trusted.overlay.opaque"); err != nil || !ok || string(v) != "y" {
			t.Fatalf("opaque: %q %v %v", v, ok, err)
		}
	})

	t.Run("devices", func(t *testing.T) {
		if !fx.mknod {
			t.Skip("mknod needs root")
		}
		wh := lookup(t, fs, "/wh")
		if !wh.IsCharDev() || !wh.IsWhiteout() {
			t.Fatalf("whiteout: chr=%v wh=%v", wh.IsCharDev(), wh.IsWhiteout())
		}
		null := lookup(t, fs, "/null")
		if !null.IsCharDev() || null.IsWhiteout() || null.RdevMajor != 1 || null.RdevMinor != 3 {
			t.Fatalf("null: %d:%d wh=%v", null.RdevMajor, null.RdevMinor, null.IsWhiteout())
		}
		bd := lookup(t, fs, "/bigdev")
		if bd.IsCharDev() || bd.IsWhiteout() || bd.RdevMajor != 259 || bd.RdevMinor != 300000 {
			t.Fatalf("bigdev: %d:%d", bd.RdevMajor, bd.RdevMinor)
		}
		if lookup(t, fs, "/small.txt").IsWhiteout() {
			t.Fatal("regular file reported as whiteout")
		}
	})

	t.Run("mtime", func(t *testing.T) {
		if !fx.nanos {
			t.Skip("debugfs not found")
		}
		in := lookup(t, fs, "/small.txt")
		if !in.Mtime.Equal(fx.mtime) {
			t.Fatalf("mtime %v, want %v", in.Mtime, fx.mtime)
		}
		if in.UID != uint32(os.Getuid()) {
			t.Errorf("uid %d, want %d", in.UID, os.Getuid())
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		testCorrupt(t, fs, fx.img)
	})

	t.Run("journal", func(t *testing.T) {
		testJournal(t, fs, fx.img)
	})
}

// exercise 对镜像里每个 inode 跑全部读路径；结果可以是数据也可以是错误，但不能是被 recover 兜住的 panic。
func exercise(t *testing.T, fs *FS, label string) {
	t.Helper()
	check := func(err error) {
		if err != nil && strings.Contains(err.Error(), "ext4ro: internal error") {
			t.Fatalf("%s: recovered panic: %v", label, err)
		}
	}
	buf := make([]byte, 8192)
	for ino := uint32(1); ino <= fs.inodesCount; ino++ {
		in, err := fs.Inode(ino)
		check(err)
		if err != nil {
			continue
		}
		_, err = fs.ReadDir(in)
		check(err)
		_, err = fs.Readlink(in)
		check(err)
		for _, name := range []string{"user.big", "user.small", "trusted.overlay.opaque"} {
			_, _, err = fs.Xattr(in, name)
			check(err)
		}
		fl, err := fs.Open(in)
		check(err)
		if err != nil {
			continue
		}
		for _, off := range []int64{0, fl.Size() / 2, fl.Size() - 1} {
			_, err = fl.ReadAt(buf, off)
			check(err)
		}
	}
	for _, p := range []string{"/a/b/c/deep.txt", "/big/文件-ünïcødé-0350", "/link-dir/b", "/nope"} {
		_, err := fs.Lookup(p)
		check(err)
	}
}

// hotRanges 返回元数据集中的字节区间（inode 表、目录块、extent 索引块、xattr 块），翻转它们比均匀乱翻有效得多。
func hotRanges(t *testing.T, fs *FS) [][2]int64 {
	t.Helper()
	bs := int64(fs.blockSize)
	gd, err := fs.groupDesc(0)
	if err != nil {
		t.Fatal(err)
	}
	ranges := [][2]int64{
		{1024, 2048}, // 超级块
		{bs, 2 * bs}, // 组描述符
		{int64(gd.inodeTable) * bs, int64(gd.inodeTable)*bs + int64(fs.inodesPerGroup)*int64(fs.inodeSize)},
	}
	for _, p := range []string{"/", "/big", "/a", "/link-slow"} {
		in := lookup(t, fs, p)
		ext, err := fs.loadExtents(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ext {
			ranges = append(ranges, [2]int64{int64(e.pblk) * bs, int64(e.pblk+e.length) * bs})
		}
	}
	// sparse.bin 的 extent 索引（深度 ≥ 1 时根里是 index 项）。
	sp := lookup(t, fs, "/sparse.bin")
	le := binary.LittleEndian
	for i := range int(le.Uint16(sp.block[2:])) {
		e := sp.block[12+12*i:]
		blk := int64(le.Uint16(e[8:]))<<32 | int64(le.Uint32(e[4:]))
		ranges = append(ranges, [2]int64{blk * bs, (blk + 1) * bs})
	}
	if xb, err := fs.Lookup("/xb.txt"); err == nil && xb.fileACL != 0 {
		ranges = append(ranges, [2]int64{int64(xb.fileACL) * bs, int64(xb.fileACL+1) * bs})
	}
	return ranges
}

func testCorrupt(t *testing.T, clean *FS, img string) {
	data, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(data))

	// 截断：如实报告大小（Open 应拒绝）与谎报完整大小（读到 EOF 时报错）。
	for _, n := range []int64{0, 1000, 2048, 4096, 64 << 10, size / 8, size / 2, size - 1} {
		for _, claimed := range []int64{n, size} {
			fs, err := Open(bytes.NewReader(data[:n]), claimed)
			if err != nil {
				if strings.Contains(err.Error(), "internal error") {
					t.Fatalf("truncate %d: %v", n, err)
				}
				continue
			}
			exercise(t, fs, fmt.Sprintf("truncate %d claimed %d", n, claimed))
		}
	}

	flipTrials(t, data, hotRanges(t, clean), 42, 60, "trial")
}

// flipTrials 每轮在 ranges 内（每 5 轮有一轮在全镜像）随机翻转 1–200 个字节，打开并跑全部读路径。
func flipTrials(t *testing.T, data []byte, ranges [][2]int64, seed uint64, trials int, label string) {
	t.Helper()
	size := int64(len(data))
	r := rand.New(rand.NewPCG(seed, 7))
	mut := make([]byte, len(data))
	for trial := range trials {
		copy(mut, data)
		flips := 1 + r.IntN(200)
		for range flips {
			var off int64
			if trial%5 == 4 {
				off = r.Int64N(size) // 偶尔均匀乱翻
			} else {
				rg := ranges[r.IntN(len(ranges))]
				off = rg[0] + r.Int64N(rg[1]-rg[0])
			}
			mut[off] ^= byte(1 + r.IntN(255))
		}
		fs, err := Open(bytes.NewReader(mut), size)
		if err != nil {
			if strings.Contains(err.Error(), "internal error") {
				t.Fatalf("%s %d: %v", label, trial, err)
			}
			continue
		}
		exercise(t, fs, fmt.Sprintf("%s %d", label, trial))
	}
}

// journalVariant 描述手工构造的 jbd2 日志格式。
type journalVariant struct {
	name     string
	incompat uint32
	fcBlocks uint32 // >0：启用 fast commit，末尾这么多块不属于常规事务区
	wrap     bool   // 从常规区末尾前 3 块开始写，迫使事务回绕到 s_first
}

// journalImage 在 fixture 镜像副本里手工写一段日志并置 needs_recovery，返回镜像与用到的日志块区间。
// 事务（序号从 0xFFFFFFFE 起，覆盖 32 位回绕）：
//
//	T1 /a 的目录块把 "b" 改名 "z"；multi.bin 首块以 jbd2 magic 开头（ESCAPE 存放）
//	T2 写 small.txt 数据块        T3 revoke small.txt 与 deep.txt 的数据块
//	T4 重写 deep.txt 数据块        T5 再改 /a 为 "q"，但没有 commit（不得生效）
type journalEdits struct {
	dirA, multi, small, deep uint64 // 涉及的 fs 块
	multiNew                 []byte
}

func journalImage(t *testing.T, fs *FS, data []byte, v journalVariant) ([]byte, journalEdits, [][2]int64) {
	t.Helper()
	img := bytes.Clone(data)
	bs := int(fs.blockSize)
	be := binary.BigEndian
	firstBlock := func(p string) uint64 {
		ext, err := fs.loadExtents(lookup(t, fs, p))
		if err != nil {
			t.Fatal(err)
		}
		return ext[0].pblk
	}
	jin, err := fs.Inode(fs.journalInum)
	if err != nil {
		t.Fatal(err)
	}
	jext, err := fs.loadExtents(jin)
	if err != nil {
		t.Fatal(err)
	}
	jphys := func(lb uint32) int {
		for _, e := range jext {
			if uint64(lb) >= e.lblk && uint64(lb) < e.lblk+e.length {
				return int(e.pblk+uint64(lb)-e.lblk) * bs
			}
		}
		t.Fatalf("journal block %d unmapped", lb)
		return 0
	}
	jsb := img[jphys(0):]
	if be.Uint32(jsb) != jbdMagic {
		t.Fatal("fixture has no jbd2 superblock")
	}
	maxLen, first := be.Uint32(jsb[0x10:]), be.Uint32(jsb[0x14:])
	last := maxLen
	if v.fcBlocks > 0 {
		last -= v.fcBlocks
		be.PutUint32(jsb[0x54:], v.fcBlocks)
	}
	start := first
	if v.wrap {
		start = last - 3
	}
	var seq0 uint32 = 0xFFFFFFFE // 让事务号在测试里回绕
	be.PutUint32(jsb[0x18:], seq0)
	be.PutUint32(jsb[0x1C:], start)
	be.PutUint32(jsb[0x28:], v.incompat)

	ed := journalEdits{dirA: firstBlock("/a"), multi: firstBlock("/multi.bin"), small: firstBlock("/small.txt"), deep: firstBlock("/deep-holder")}
	is64 := v.incompat&jbdFeat64Bit != 0
	var blocks [][]byte
	header := func(typ, seq uint32) []byte {
		b := make([]byte, bs)
		be.PutUint32(b[0:], jbdMagic)
		be.PutUint32(b[4:], typ)
		be.PutUint32(b[8:], seq)
		return b
	}
	type tag struct {
		blk   uint64
		flags uint32
		data  []byte
	}
	descriptor := func(seq uint32, tags ...tag) {
		d := header(jbdDescriptor, seq)
		off := jbdHeaderSize
		for i, tg := range tags {
			flags := tg.flags
			if i > 0 {
				flags |= jbdTagSameUUID
			}
			if i == len(tags)-1 {
				flags |= jbdTagLast
			}
			switch {
			case v.incompat&jbdFeatCsumV3 != 0:
				be.PutUint32(d[off:], uint32(tg.blk))
				be.PutUint32(d[off+4:], flags)
				be.PutUint32(d[off+8:], uint32(tg.blk>>32))
				off += 16
			default:
				// journal_block_tag_t：blocknr(0) checksum16(4) flags16(6) blocknr_high(8)；
				// 长度按内核 journal_tag_bytes：基础 8，CSUM_V2 +2，64BIT +4
				be.PutUint32(d[off:], uint32(tg.blk))
				be.PutUint16(d[off+6:], uint16(flags))
				if is64 {
					be.PutUint32(d[off+8:], uint32(tg.blk>>32))
				}
				off += 8
				if v.incompat&jbdFeatCsumV2 != 0 {
					off += 2
				}
				if is64 {
					off += 4
				}
			}
			if flags&jbdTagSameUUID == 0 {
				copy(d[off:], "0123456789abcdef")
				off += 16
			}
		}
		blocks = append(blocks, d)
		for _, tg := range tags {
			blocks = append(blocks, tg.data)
		}
	}
	commit := func(seq uint32) { blocks = append(blocks, header(jbdCommit, seq)) }
	block := func(fsBlk uint64) []byte { return bytes.Clone(img[int(fsBlk)*bs : int(fsBlk+1)*bs]) }
	rename := func(to byte) []byte {
		b := block(ed.dirA)
		i := bytes.Index(b, []byte{1, 2, 'b'})
		if i < 0 {
			t.Fatal("entry b not found in /a block")
		}
		b[i+2] = to
		return b
	}

	// T1：改名 + 转义块。
	ed.multiNew = block(ed.multi)
	be.PutUint32(ed.multiNew, jbdMagic)
	escaped := bytes.Clone(ed.multiNew)
	clear(escaped[:4])
	descriptor(seq0, tag{ed.dirA, 0, rename('z')}, tag{ed.multi, jbdTagEscape, escaped})
	commit(seq0)
	// T2：写 small.txt（T3 撤销，不得生效）。
	smallNew := make([]byte, bs)
	copy(smallNew, "XXXXX\n")
	descriptor(seq0+1, tag{ed.small, 0, smallNew})
	commit(seq0 + 1)
	// T3：撤销 small 与 deep。
	rv := header(jbdRevoke, seq0+2)
	recLen := 4
	if is64 {
		recLen = 8
	}
	be.PutUint32(rv[12:], uint32(jbdRevokeHeaderSize+2*recLen))
	for i, b := range []uint64{ed.small, ed.deep} {
		off := jbdRevokeHeaderSize + i*recLen
		if is64 {
			be.PutUint64(rv[off:], b)
		} else {
			be.PutUint32(rv[off:], uint32(b))
		}
	}
	blocks = append(blocks, rv)
	commit(seq0 + 2)
	// T4：撤销之后重写 deep（生效）。
	deepNew := make([]byte, bs)
	copy(deepNew, "DEEP!")
	descriptor(seq0+3, tag{ed.deep, 0, deepNew})
	commit(seq0 + 3)
	// T5：未提交。
	descriptor(seq0+4, tag{ed.dirA, 0, rename('q')})
	blocks = append(blocks, make([]byte, bs)) // 日志尾

	var used [][2]int64
	lb := start
	for _, b := range blocks {
		p := jphys(lb)
		copy(img[p:], b)
		used = append(used, [2]int64{int64(p), int64(p + bs)})
		if lb++; lb >= last {
			lb = first
		}
	}
	used = append(used, [2]int64{int64(jphys(0)), int64(jphys(0) + 0x60)})
	img[superblockOffset+0x60] |= incompatRecover
	return img, ed, used
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func readAll(t *testing.T, fs *FS, p string) []byte {
	t.Helper()
	fl, err := fs.Open(lookup(t, fs, p))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(io.NewSectionReader(fl, 0, fl.Size()))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testJournal(t *testing.T, clean *FS, img string) {
	data, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	open := func(b []byte) *FS {
		t.Helper()
		fs, err := Open(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		return fs
	}
	unreplayed := func(t *testing.T, fs *FS) {
		t.Helper()
		lookup(t, fs, "/a/b/c/deep.txt")
		if string(readAll(t, fs, "/deep-holder")) != "hold" {
			t.Fatal("unreplayed view changed")
		}
	}
	for _, v := range []journalVariant{
		{name: "csum3-64bit", incompat: jbdFeatRevoke | jbdFeat64Bit | jbdFeatCsumV3},
		{name: "plain-32bit", incompat: jbdFeatRevoke},
		{name: "csum2-64bit-wrap-fastcommit", incompat: jbdFeatRevoke | jbdFeat64Bit | jbdFeatCsumV2 | jbdFeatFastCommit, fcBlocks: 16, wrap: true},
	} {
		t.Run(v.name, func(t *testing.T) {
			jimg, ed, used := journalImage(t, clean, data, v)
			fs := open(jimg)
			if !fs.Replayed() || fs.NeedsRecovery() || fs.replayErr != nil {
				t.Fatalf("replayed=%v needsRecovery=%v err=%v", fs.Replayed(), fs.NeedsRecovery(), fs.replayErr)
			}
			if len(fs.overlay) != 3 {
				t.Errorf("overlay has %d blocks, want 3 (dirA, multi, deep)", len(fs.overlay))
			}
			lookup(t, fs, "/a/z/c/deep.txt")
			for _, p := range []string{"/a/b", "/a/q"} {
				if _, err := fs.Lookup(p); !errors.Is(err, ErrNotExist) {
					t.Errorf("Lookup(%s) = %v, want ErrNotExist", p, err)
				}
			}
			if got := readAll(t, fs, "/small.txt"); string(got) != "hello\n" {
				t.Errorf("revoked block replayed: small.txt = %q", got)
			}
			if got := readAll(t, fs, "/deep-holder"); string(got) != "DEEP" {
				t.Errorf("block rewritten after revoke not replayed: %q", got)
			}
			if got := readAll(t, fs, "/multi.bin"); !bytes.Equal(got[:fs.blockSize], ed.multiNew) {
				t.Errorf("escaped block not restored: % x", got[:8])
			}
			// 跨块的 ReadAt 起点落在转义的首 4 字节中间。
			fl, _ := fs.Open(lookup(t, fs, "/multi.bin"))
			buf := make([]byte, 6)
			if _, err := fl.ReadAt(buf, 2); err != nil || !bytes.Equal(buf, ed.multiNew[2:8]) {
				t.Errorf("escaped partial read % x %v", buf, err)
			}

			// 不置 needs_recovery：日志内容被忽略。
			noFlag := bytes.Clone(jimg)
			noFlag[superblockOffset+0x60] &^= incompatRecover
			nf := open(noFlag)
			if nf.Replayed() || nf.NeedsRecovery() {
				t.Errorf("no flag: replayed=%v needs=%v", nf.Replayed(), nf.NeedsRecovery())
			}
			unreplayed(t, nf)

			flipTrials(t, jimg, used, 99, 40, "journal flip")
		})
	}

	jimg, _, _ := journalImage(t, clean, data, journalVariant{incompat: jbdFeatRevoke})
	jsbOff := func(b []byte) []byte {
		jin, _ := clean.Inode(clean.journalInum)
		ext, _ := clean.loadExtents(jin)
		return b[int(ext[0].pblk)*int(clean.blockSize):]
	}
	t.Run("clean-journal", func(t *testing.T) {
		b := bytes.Clone(jimg)
		binary.BigEndian.PutUint32(jsbOff(b)[0x1C:], 0) // s_start = 0
		fs := open(b)
		if !fs.Replayed() || fs.NeedsRecovery() || len(fs.overlay) != 0 {
			t.Fatalf("replayed=%v needs=%v overlay=%d", fs.Replayed(), fs.NeedsRecovery(), len(fs.overlay))
		}
		unreplayed(t, fs)
	})
	t.Run("bad-journal-falls-back", func(t *testing.T) {
		for name, mutate := range map[string]func([]byte){
			"magic":     func(b []byte) { jsbOff(b)[0] ^= 0xFF },
			"blocksize": func(b []byte) { binary.BigEndian.PutUint32(jsbOff(b)[0xC:], 1024) },
			"incompat":  func(b []byte) { binary.BigEndian.PutUint32(jsbOff(b)[0x28:], 0x8000) },
			"external":  func(b []byte) { binary.LittleEndian.PutUint32(b[superblockOffset+0xE0:], 0) },
		} {
			b := bytes.Clone(jimg)
			mutate(b)
			fs := open(b)
			if fs.Replayed() || !fs.NeedsRecovery() || fs.replayErr == nil {
				t.Errorf("%s: replayed=%v needs=%v err=%v", name, fs.Replayed(), fs.NeedsRecovery(), fs.replayErr)
				continue
			}
			unreplayed(t, fs)
		}
	})
}
