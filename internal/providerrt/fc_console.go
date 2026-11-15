package providerrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// 串口日志的总量上限（Firecracker 的串口限速只能拖慢 guest 写入，挡不住累计）分两层：
//   - 硬上限：串口日志放在 jail 里一个容量为 fcConsoleFSBytes 的 tmpfs 上（Launch 挂载、Kill 卸载，见 mountConsole）。
//     它与 provider 进程无关：VM 以 --daemonize 跨 provider 重启存活，provider 停机期间 guest 写满也只是丢字节。
//   - 补充：provider 运行时定期检查，实际占用超过 fcConsoleMaxBytes 就把末尾 fcConsoleKeepBytes 之前的部分打孔释放，
//     让启动失败时带进任务输出的末尾是最新的输出，而不是写满时停住的旧输出。
//
// 不用 RLIMIT_FSIZE（它同样限制 disk.ext4 大偏移处的写入）；也不用 FIFO（provider 不读时 guest 的 vCPU 会卡在串口写上）。
const (
	fcConsoleFSBytes   = 4 << 20
	fcConsoleMaxBytes  = 2 << 20
	fcConsoleKeepBytes = 512 << 10
	// FCConsoleTrimEvery 是检查间隔：按串口限速（每秒 16 KiB，开机突发 1 MiB），两次检查之间至多再写约 1.5 MiB。
	FCConsoleTrimEvery = 30 * time.Second
	// fcConsoleTailBytes 是启动失败时带进任务输出的串口日志末尾长度。
	fcConsoleTailBytes = 4 << 10
)

// Housekeeper 是驱动可选实现的后台维护：`oryn provider run` 在 provider 运行期间以 goroutine 调用，ctx 结束即返回。
type Housekeeper interface {
	Housekeep(ctx context.Context)
}

// Housekeep 实现 Housekeeper：立即检查一次串口日志，之后每 FCConsoleTrimEvery 一次，直到 ctx 结束。
func (d *FirecrackerDriver) Housekeep(ctx context.Context) {
	t := time.NewTicker(FCConsoleTrimEvery)
	defer t.Stop()
	for {
		d.trimConsoles()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// trimConsoles 对每台 VM（含停机的：jail 已删则跳过）的串口日志执行 trimConsole；失败只记日志。
// 升级前启动、仍在运行的 VM 的串口日志还在 jail 根下（没有 tmpfs），一并检查，直到它们重启。
func (d *FirecrackerDriver) trimConsoles() {
	vms, err := d.paths.listVMs()
	if err != nil {
		slog.Warn("串口日志检查：读取 VM 列表失败", "err", err)
		return
	}
	limit, keep := d.consoleMax, d.consoleKeep
	if limit == 0 {
		limit = fcConsoleMaxBytes
	}
	if keep == 0 {
		keep = fcConsoleKeepBytes
	}
	for _, vm := range vms {
		for _, p := range []string{d.consolePath(vm.OrbID), filepath.Join(d.paths.jailRoot(vm.OrbID), fcConsoleLog)} {
			if err := trimConsole(p, vm.UID, limit, keep); err != nil && !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("串口日志检查失败", "orb", vm.OrbID, "err", err)
			}
		}
	}
}

func (d *FirecrackerDriver) consolePath(orbID string) string {
	return filepath.Join(d.paths.jailRoot(orbID), fcConsoleDir, fcConsoleLog)
}

// openConsole 打开 jail 里的串口日志，并确认它仍是 Launch 建的那个文件：普通文件、不是符号链接、属于槽位 uid、
// 只有一个链接。jail 根归槽位 uid 所有，被攻破的 firecracker 可以把 base.ext4 / disk.ext4 的硬链接改名成
// console.log，或换成指向宿主文件的符号链接；不核对就会打孔或读出别的文件。
func openConsole(path string, flag, uid int) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	f, err := os.OpenFile(path, flag|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, st, err
	}
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, st, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != uid || st.Nlink != 1 {
		f.Close()
		return nil, st, fmt.Errorf("%s 不是槽位 uid %d 的普通文件（mode %o uid %d nlink %d），跳过", path, uid, st.Mode, st.Uid, st.Nlink)
	}
	return f, st, nil
}

// trimConsole 在串口日志实际占用（st_blocks）超过 limit 时，把末尾 keep 字节之前（按 4 KiB 向下对齐）打孔释放，
// 文件大小不变：firecracker 可能不以 O_APPEND 打开它，截断后其写偏移不会回退，打孔与写偏移无关。
// 文件系统不支持打孔时截断为 0（之后的写入在原偏移处，前面是空洞，同样不占空间）。
func trimConsole(path string, uid int, limit, keep int64) error {
	f, st, err := openConsole(path, os.O_WRONLY, uid)
	if err != nil {
		return err
	}
	defer f.Close()
	if st.Blocks*512 <= limit {
		return nil
	}
	end := (st.Size - keep) &^ (4<<10 - 1)
	if end <= 0 {
		return nil
	}
	err = unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, 0, end)
	if errors.Is(err, unix.EOPNOTSUPP) {
		return f.Truncate(0)
	}
	return err
}

// consoleTail 把 jail 里串口日志的末尾写进任务输出（Kill 会删掉 jail，之前调用）；没有日志则不写。
// 打孔或截断留下的空洞读出来是 0 字节，去掉。
func (d *FirecrackerDriver) consoleTail(vm vmMeta, out io.Writer) {
	f, st, err := openConsole(d.consolePath(vm.OrbID), os.O_RDONLY, vm.UID)
	if err != nil {
		return
	}
	defer f.Close()
	if st.Size > fcConsoleTailBytes {
		_, _ = f.Seek(st.Size-fcConsoleTailBytes, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, fcConsoleTailBytes))
	b = bytes.ReplaceAll(b, []byte{0}, nil)
	if len(b) > 0 {
		fmt.Fprintf(out, "oryn: guest 串口输出末尾：\n%s\n", strings.ToValidUTF8(string(b), ""))
	}
}
