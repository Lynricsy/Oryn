package coordhost

import (
	"bufio"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 协调宿主的降权账号（M9 审查修复 C10.1′ / C10.2′）。
//
// orynd 以 root 运行时，沙箱降权到一个只属于本实例的系统账号：缺省（coordinator.user 为空、nobody、nfsnobody，
// 或解析出 uid 65534）用 `oryn-coord-<8 位十六进制>`，后缀取 fnv32a(协调目录的绝对路径)，不存在时由 useradd 真正
// 创建（号由 passwd 分配，不自己挑号）。显式写了别的账号则用它。无论哪种，每次拉起前都校验该账号此刻没有被本实例
// 以外的进程使用；不满足就不可用，绝不去杀别人的进程。
//
// 残留进程：有 PID 命名空间时子进程是命名空间的 init，它退出时内核杀掉命名空间里的全部进程；没有 PID 命名空间时，
// 只有本实例的专用账号在子进程退出后按 uid 清扫（独占校验已保证该 uid 的进程都是本实例的）。显式账号一律不清扫。

const (
	// sharedUID 是 nobody 的 uid：与系统里其它服务共用的身份，不能作为沙箱的独立用户。
	sharedUID = 65534
	// killRounds / killInterval 是按 uid 清扫残留进程的轮数与间隔。
	killRounds   = 20
	killInterval = 100 * time.Millisecond
)

// account 是协调宿主降权到的账号。
type account struct {
	name     string
	uid, gid int
	// auto 为真表示这是本实例的专用账号 oryn-coord-<后缀>：没有 PID 命名空间时允许按 uid 清扫残留进程。
	auto bool
}

// noAccount 表示不切换用户（orynd 不是 root）。
var noAccount = account{uid: -1, gid: -1}

// AutoUserName 返回协调目录 dir 对应的专用账号名（root 部署时 orynd 自动创建 / 使用它）：oryn-coord-<fnv32a(dir 的绝对路径) 的 8 位十六进制>。
func AutoUserName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(abs))
	return fmt.Sprintf("oryn-coord-%08x", h.Sum32())
}

// sharedSetting 报告 coordinator.user 的写法是否表示「自动」（空串，或共享身份 nobody / nfsnobody）。
func sharedSetting(name string) bool { return name == "" || name == "nobody" || name == "nfsnobody" }

// nologinShells 是专用账号登录 shell 的候选（取第一个存在的）。
var nologinShells = []string{"/usr/sbin/nologin", "/sbin/nologin", "/usr/bin/nologin", "/bin/false"}

func nologinShell() string {
	for _, p := range nologinShells {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/bin/false"
}

// useraddArgs 是创建专用账号的 useradd 参数。
func useraddArgs(name string) []string {
	return []string{"--system", "--no-create-home", "--home-dir", "/nonexistent", "--shell", nologinShell(), "--user-group", name}
}

// manualUseradd 是给管理员手工执行的同一条命令。
func manualUseradd(name string) string { return "useradd " + strings.Join(useraddArgs(name), " ") }

// runUseradd 是 Host.addUser 的缺省实现：执行 useradd，失败时错误里带它的输出。
func runUseradd(name string) error {
	bin, err := exec.LookPath("useradd")
	if err != nil {
		for _, p := range []string{"/usr/sbin/useradd", "/sbin/useradd", "/usr/bin/useradd"} {
			if _, serr := os.Stat(p); serr == nil {
				bin, err = p, nil
				break
			}
		}
	}
	if err != nil {
		return errors.New("useradd not found")
	}
	out, err := exec.Command(bin, useraddArgs(name)...).CombinedOutput()
	if err != nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return fmt.Errorf("%v: %s", err, s)
		}
		return err
	}
	return nil
}

// lookupAccount 解析账号的 uid / gid；root 一律拒绝。
func (h *Host) lookupAccount(name string) (account, error) {
	u, err := h.lookupUser(name)
	if err != nil {
		return account{}, err
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return account{}, fmt.Errorf("account %q has non-numeric ids %s/%s", name, u.Uid, u.Gid)
	}
	if uid == 0 {
		return account{}, fmt.Errorf("account %q must not be root", name)
	}
	return account{name: name, uid: uid, gid: gid}, nil
}

// target 返回降权目标：orynd 的 euid 不是 0 时为 noAccount（不切换用户）；否则按 coordinator.user 解析，「自动」时
// 用（必要时创建）本实例的专用账号。不做独占校验（见 exclusive）。
func (h *Host) target() (account, error) {
	if os.Geteuid() != 0 {
		return noAccount, nil
	}
	h.acctMu.Lock()
	defer h.acctMu.Unlock()
	name, autoName := h.cfg.User, AutoUserName(h.cfg.Dir)
	if !sharedSetting(name) {
		a, err := h.lookupAccount(name)
		if err != nil {
			return account{}, fmt.Errorf("coordinator.user %q: %v", name, err)
		}
		if a.uid != sharedUID {
			// 显式写了本实例的专用账号名，与「自动」同等对待。
			a.auto = name == autoName
			return a, nil
		}
	}
	if name != "" && !h.warnedShared {
		h.warnedShared = true
		h.log.Warn("coordinator.user is a shared identity; using a dedicated account instead", "user", name, "account", autoName)
	}
	a, err := h.lookupAccount(autoName)
	if err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return account{}, fmt.Errorf("coordinator account %q: %v", autoName, err)
		}
		if err := h.addUser(autoName); err != nil {
			return account{}, fmt.Errorf("cannot create the coordinator account %q: %v; create it manually with `%s`, or set coordinator.user to a dedicated account", autoName, err, manualUseradd(autoName))
		}
		if a, err = h.lookupAccount(autoName); err != nil {
			return account{}, fmt.Errorf("coordinator account %q after useradd: %v", autoName, err)
		}
		h.log.Info("created the dedicated coordinator account", "account", autoName, "uid", a.uid)
	}
	if a.uid == sharedUID {
		return account{}, fmt.Errorf("coordinator account %q has the shared uid %d; recreate it with `%s`", autoName, sharedUID, manualUseradd(autoName))
	}
	a.auto = true
	return a, nil
}

// exclusive 校验账号此刻只被本实例的进程使用（自动与显式账号都做）；不满足时协调功能不可用，不杀任何进程。
func (h *Host) exclusive(a account) error {
	if a.uid < 0 {
		return nil
	}
	if pid := h.uidBusy(a.uid); pid > 0 {
		return fmt.Errorf("coordinator account %q (uid %d) is used by other processes (pid %d); use a dedicated account", a.name, a.uid, pid)
	}
	return nil
}

// procInfo 是 /proc/<pid>/status 里用到的字段。
type procInfo struct {
	ppid   int
	uids   [4]int
	zombie bool
}

func (p procInfo) hasUID(uid int) bool { return slices.Contains(p.uids[:], uid) }

// scanProcs 读出全部进程的父进程、四个 uid 与是否僵尸（读不到的进程跳过：它可能刚退出）。
func scanProcs() map[int]procInfo {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make(map[int]procInfo, len(ents))
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if info, ok := readProc(pid); ok {
			out[pid] = info
		}
	}
	return out
}

func readProc(pid int) (procInfo, bool) {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return procInfo{}, false
	}
	defer f.Close()
	var info procInfo
	var gotUID bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		switch k {
		case "State":
			info.zombie = len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X")
		case "PPid":
			if len(fields) > 0 {
				info.ppid, _ = strconv.Atoi(fields[0])
			}
		case "Uid":
			if len(fields) < 4 {
				return procInfo{}, false
			}
			for i := range 4 {
				n, err := strconv.Atoi(fields[i])
				if err != nil {
					return procInfo{}, false
				}
				info.uids[i] = n
			}
			gotUID = true
		}
	}
	return info, gotUID
}

// isCoordinatorHostOf 报告 pid 是否是本协调目录的 coordinator-host 子进程（命令行带 coordinator-host 与
// `--home <dir>/…`）。同一协调目录的另一个 orynd 进程（例如 orynd 运行时执行的 `coordinator check`）也据此认出
// 正在运行的子进程是本实例的。
func isCoordinatorHostOf(pid int, dirPrefix string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	if !slices.Contains(args, "coordinator-host") {
		return false
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--home" && strings.HasPrefix(args[i+1], dirPrefix) {
			return true
		}
	}
	return false
}

// childPIDs 返回本 Host 登记的子进程的 pid。
func (h *Host) childPIDs() map[int]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	own := map[int]bool{}
	for _, c := range h.children {
		if c.pid > 0 {
			own[c.pid] = true
		}
	}
	return own
}

// procUIDBusy 是 Host.uidBusy 的缺省实现：返回以该 uid 运行（Uid 四列任一相等）、且不在本实例子进程树里的
// 最小 pid；没有则 0。子进程树按父进程链判断（有 PID 命名空间时，命名空间里的进程在宿主视角下同样是子进程的后代）。
func (h *Host) procUIDBusy(uid int) int {
	procs := scanProcs()
	own := h.childPIDs()
	abs, err := filepath.Abs(h.cfg.Dir)
	if err != nil {
		abs = h.cfg.Dir
	}
	prefix := strings.TrimSuffix(abs, "/") + "/"
	hostOf := map[int]bool{}
	ours := func(pid int) bool {
		for p, depth := pid, 0; p > 1 && depth < 256; depth++ {
			if own[p] {
				return true
			}
			v, ok := hostOf[p]
			if !ok {
				v = isCoordinatorHostOf(p, prefix)
				hostOf[p] = v
			}
			if v {
				return true
			}
			info, ok := procs[p]
			if !ok {
				return false
			}
			p = info.ppid
		}
		return false
	}
	var busy []int
	for pid, info := range procs {
		if info.zombie || !info.hasUID(uid) || ours(pid) {
			continue
		}
		busy = append(busy, pid)
	}
	if len(busy) == 0 {
		return 0
	}
	return slices.Min(busy)
}

// procsOfUID 返回以该 uid 运行的全部非僵尸进程。
func procsOfUID(uid int) []int {
	var out []int
	for pid, info := range scanProcs() {
		if !info.zombie && info.hasUID(uid) {
			out = append(out, pid)
		}
	}
	return out
}

// killUIDProcs 是 Host.killUID 的缺省实现：SIGKILL 该 uid 的全部进程，重复到没有为止（至多 killRounds 轮，
// 每轮之间等 killInterval）。只在本实例专用账号、没有 PID 命名空间时由子进程的退出路径调用。
func (h *Host) killUIDProcs(uid int) {
	if uid <= 0 || uid == sharedUID {
		return
	}
	for range killRounds {
		pids := procsOfUID(uid)
		if len(pids) == 0 {
			return
		}
		for _, p := range pids {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
		h.sleep(killInterval)
	}
	h.log.Warn("processes of the coordinator account survived SIGKILL", "uid", uid, "pids", procsOfUID(uid))
}
