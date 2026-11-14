package sandbox

import "fmt"

// MinABI 是任何条件下都要求的最低 Landlock ABI（跨目录改名需要 refer 权限）。
const MinABI = 2

// 各项 Landlock 能力出现的 ABI。低于它们时 BestEffort 会悄悄丢掉对应规则，Requirement 据此决定能否施加：
//   - refer（ABI 2）：缺了只会更严（跨目录改名被拒），但 omp 需要它 → MinABI；
//   - truncate（ABI 3）：缺了按路径的 truncate(2) 不受限 → seccomp 无条件拒绝 truncate（ftruncate 只作用于
//     Landlock 已放行写入的已打开文件），不设门槛；
//   - TCP bind / connect（ABI 4）：缺了 TCP 不受限 → 要求网络命名空间；
//   - ioctl_dev（ABI 5）：能打开的设备只有白名单里的 /dev/null、/dev/{u,}random、/dev/zero，不设门槛；
//   - 信号与 abstract unix socket 作用域（ABI 6）：abstract socket 已由 seccomp 禁掉 AF_UNIX 挡住；信号缺了
//     可以发给同一用户的任意进程 → 要求 PID 命名空间或独立用户；
//   - resolve-unix（ABI 9）、UDP（ABI 10）：seccomp 已禁止 AF_UNIX 与 UDP，不设门槛。
const (
	TCPABI    = 4
	SignalABI = 6
)

// Requirement 返回在这些条件下施加沙箱的第一条缺口；nil = 隔离完整。Apply 与协调宿主的可用性检查共用。
// separateUID 表示沙箱降权到与 orynd 不同的用户。
func Requirement(abi int, netns, pidns, separateUID bool) error {
	switch {
	case abi < MinABI:
		return fmt.Errorf("内核 Landlock ABI %d < %d：需要 Linux ≥ 5.19 并启用 landlock LSM（跨目录改名需要 refer 权限）", abi, MinABI)
	case !netns && abi < TCPABI:
		return fmt.Errorf("内核 Landlock ABI %d < %d 且没有网络命名空间，无法限制 TCP 出站：需要 Linux ≥ 6.7，或允许创建网络命名空间（以 root 运行 orynd，或开放非特权 user namespace）", abi, TCPABI)
	case !pidns && !separateUID && abi < SignalABI:
		return fmt.Errorf("内核 Landlock ABI %d < %d、没有 PID 命名空间且沙箱与 orynd 是同一用户，无法阻止向 orynd 等同用户进程发信号：需要 Linux ≥ 6.12，或以 root 运行 orynd（沙箱降权到 coordinator.user）", abi, SignalABI)
	}
	return nil
}
