package providerrt

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
)

// BaseImageIDTTL 是 docker 驱动缓存基础镜像 id 的时长（hello 每次心跳都读 Info）。
const BaseImageIDTTL = 60 * time.Second

// Docker 驱动通过 `docker` CLI（os/exec）管理 orb 容器（计划 C1 表中的命令）。
type Docker struct {
	Cfg config.Docker
	// Bin 是 docker 可执行文件，默认 "docker"（按 PATH 查找）。
	Bin string
	// Now 是时钟（测试替换；nil = time.Now）。
	Now func() time.Time

	// hookSnapshotStopped（测试用）在 sysbox 快照停机之后、commit 之前调用。
	hookSnapshotStopped func()

	baseMu       sync.Mutex
	baseID       string
	baseAt       time.Time
	diskDir      string
	diskDirSet   bool
	orbDiskMu    sync.Mutex
	orbDiskAt    time.Time
	orbDiskTotal int64
	orbDiskByOrb map[string]int64
	orbDiskValid bool
	// offline 缓存停机 orb 的文件索引（只读预览，见 offlinefs_docker.go）。
	offline indexCache
}

// NewDocker 创建 docker 驱动。
func NewDocker(cfg config.Docker) *Docker {
	if len(cfg.Types) == 0 {
		cfg.Types = config.DefaultDockerTypes()
	}
	return &Docker{Cfg: cfg, Bin: "docker"}
}

// Info 实现 Driver。
func (d *Docker) Info() Info {
	return Info{
		Driver:       apigen.Docker,
		Capabilities: apigen.ProviderCapabilities{Freeze: true, Snapshot: true, OfflineFs: new(true)},
		Types:        typeInfos(d.Cfg.Types),
		DefaultType:  d.Cfg.DefaultType,
		BaseImageID:  d.baseImageID(),
		DiskPath:     d.diskPath(),
	}
}

// diskPath 返回 Docker 数据目录（用于宿主机存储采样）；首次通过 docker info 探测并缓存，失败回退 /var/lib/docker 或 /。
func (d *Docker) diskPath() string {
	d.baseMu.Lock()
	defer d.baseMu.Unlock()
	if d.diskDirSet {
		return d.diskDir
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	if err := d.exec(ctx, &stdout, io.Discard, nil, "info", "--format", "{{.DockerRootDir}}"); err == nil {
		if dir := strings.TrimSpace(stdout.String()); dir != "" {
			d.diskDir, d.diskDirSet = dir, true
			return dir
		}
	}
	if _, err := os.Stat("/var/lib/docker"); err == nil {
		d.diskDir = "/var/lib/docker"
	} else {
		d.diskDir = "/"
	}
	d.diskDirSet = true
	return d.diskDir
}

// baseImageID 返回基础镜像（cfg.image）的镜像 id，成功结果缓存 BaseImageIDTTL；读取失败给空串（不缓存）。
func (d *Docker) baseImageID() string {
	if d.Cfg.Image == "" {
		return ""
	}
	d.baseMu.Lock()
	defer d.baseMu.Unlock()
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	t := now()
	if d.baseID != "" && t.Sub(d.baseAt) < BaseImageIDTTL {
		return d.baseID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	if err := d.exec(ctx, &stdout, io.Discard, nil, "image", "inspect", "--format", "{{.Id}}", d.Cfg.Image); err != nil {
		slog.Warn("读取基础镜像 id 失败（本次 hello 不声明 baseImageId）", "image", d.Cfg.Image, "err", err)
		d.baseID = ""
		return ""
	}
	d.baseID, d.baseAt = strings.TrimSpace(stdout.String()), t
	return d.baseID
}

// Preflight 实现 Preflighter：类型配置的 runtime 必须已在 Docker 中登记；extra_run_args 里的特权写法只警告；
// 类型所用的 runtime 带 --no-kernel-check（关掉了 sysbox 的内核兼容性检查）时只警告（S-P2-6）。
func (d *Docker) Preflight(ctx context.Context) error {
	for _, a := range d.Cfg.ExtraRunArgs {
		if a == "--privileged" || (strings.HasPrefix(a, "--privileged=") && a != "--privileged=false") {
			slog.Warn("docker.extra_run_args 含 --privileged：等于把宿主 root 交给 orb 内的 agent，不受支持；嵌套 Docker 请用类型的 nested_docker + sysbox-runc，见 docs/deploy.md")
		}
		if strings.Contains(a, "docker.sock") {
			slog.Warn("docker.extra_run_args 挂载了宿主的 docker.sock：orb 内的 agent 可借此控制宿主 Docker（等同宿主 root），不受支持；见 docs/deploy.md", "arg", a)
		}
	}
	need := map[string][]string{}
	for name, t := range d.Cfg.Types {
		if t.Runtime != "" {
			need[t.Runtime] = append(need[t.Runtime], name)
		}
	}
	if len(need) == 0 {
		return nil
	}
	var stdout bytes.Buffer
	if err := d.exec(ctx, &stdout, io.Discard, nil, "info", "--format", "{{json .Runtimes}}"); err != nil {
		return fmt.Errorf("docker: 读取已登记的容器运行时: %w", err)
	}
	var registered map[string]struct {
		RuntimeArgs []string `json:"runtimeArgs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &registered); err != nil {
		return fmt.Errorf("docker: 解析 docker info 的运行时列表 %q: %w", strings.TrimSpace(stdout.String()), err)
	}
	have := make([]string, 0, len(registered))
	for rt := range registered {
		have = append(have, rt)
	}
	sort.Strings(have)
	runtimes := make([]string, 0, len(need))
	for rt := range need {
		runtimes = append(runtimes, rt)
	}
	sort.Strings(runtimes)
	for _, rt := range runtimes {
		types := need[rt]
		sort.Strings(types)
		reg, ok := registered[rt]
		if !ok {
			return fmt.Errorf("docker: 类型 %s 配置的 runtime %q 没有在 Docker 中登记（docker info 里只有 %q）；安装与登记方法见 docs/deploy.md",
				strings.Join(types, ", "), rt, have)
		}
		if slices.Contains(reg.RuntimeArgs, "--no-kernel-check") {
			slog.Warn("容器运行时带 --no-kernel-check：sysbox 不再检查宿主内核是否受支持，在未验证的内核上以 root 运行的 sysbox-mgr / sysbox-fs 是额外的宿主攻击面；请改用受支持的内核并去掉该参数，见 docs/deploy.md",
				"runtime", rt, "types", strings.Join(types, ","))
		}
	}
	return nil
}

// Exec 实现 Driver。
func (d *Docker) Exec(ctx context.Context, j Job, out io.Writer) (*apigen.JobResult, error) {
	switch j.Op {
	case "spawn":
		return d.spawn(ctx, j, out)
	case "pause":
		return nil, d.pause(ctx, j, out)
	case "snapshot":
		exists, err := d.imageExists(ctx, j.SnapshotID, out)
		if err != nil {
			return nil, err
		}
		if !exists {
			if err := d.commit(ctx, j.OrbID, j.SnapshotID, out); err != nil {
				return nil, err
			}
		}
		return d.snapshotResult(ctx, j.SnapshotID, out), nil
	case "delete":
		err := d.run(ctx, out, nil, "rm", "-f", j.OrbID)
		if isNoSuch(err) {
			return nil, nil
		}
		return nil, err
	case "delete_snapshot":
		err := d.run(ctx, out, nil, "rmi", j.SnapshotID)
		if isNoSuch(err) {
			return nil, nil
		}
		return nil, err
	}
	return nil, fmt.Errorf("docker: 不支持的操作 %q", j.Op)
}

// snapshotClearEnv 是 spawn 经 `-e` 写进容器配置的变量；docker commit 会把容器配置的 Env 原样写进镜像配置
// （本机 `docker image inspect` 核对：快照镜像的 Config.Env 有 ORYN_URL / ORYN_ORB_ID / ORYN_ORB_TOKEN / ORYN_NESTED_DOCKER，
// 基础镜像一个都没有；ORYN_ORB_CA_PEM 同样经 `-e` 下发）。commit 时全部置空（S-P2-3）：令牌与 CA 不留在镜像里，
// 身份与类型属性由下一次 spawn 的 `-e` 重新给出。Docker 不能从镜像里删掉 ENV，只能置空，orbd 把空值当作未设置。
var snapshotClearEnv = []string{"ORYN_URL", "ORYN_ORB_ID", "ORYN_ORB_TOKEN", "ORYN_ORB_CA_PEM", "ORYN_NESTED_DOCKER"}

// SnapshotStopSeconds 是 sysbox 容器快照前 `docker stop -t` 的宽限秒数。
const SnapshotStopSeconds = "10"

// SnapshotRestartTimeout 是 sysbox 容器快照后重新启动的时限；不受任务 ctx 取消影响（任务超时也要把容器拉起来）。
const SnapshotRestartTimeout = 2 * time.Minute

// commitArgs 是 `docker commit` 的 argv：逐个 --change 清空 snapshotClearEnv。
func commitArgs(orbID, snapshotID string) []string {
	args := []string{"commit"}
	for _, k := range snapshotClearEnv {
		args = append(args, "--change", "ENV "+k+"=")
	}
	return append(args, orbID, snapshotID)
}

// isSysboxRuntime 判断容器的 HostConfig.Runtime 是否为 sysbox（按名称；Docker 的默认运行时也可能被配成 sysbox）。
func isSysboxRuntime(rt string) bool { return strings.Contains(rt, "sysbox") }

// commit 把 orb 容器提交为快照镜像 snapshotID。
//
// sysbox 容器（X22）：sysbox 自己挂着容器的 overlay，Docker 处在 naive diff 模式看不到上层内容，
// 运行或暂停时 commit 得到空层（本机实测 0B），停机后才完整。所以：
//   - running / restarting：stop → commit → start（commit 失败也 start）；start 后令牌文件不变，orbd 自行重连；
//   - paused：unpause → stop → commit，拍完保持停机（进程已被 stop 结束；控制面唤醒时 spawn 走 start 分支，
//     此时 start 反而会让「冻结」的 orb 意外连回控制面）；stop 失败时尽力恢复 pause；
//   - 其它状态（已停机）：直接 commit。
//
// 普通容器照旧直接 commit。
func (d *Docker) commit(ctx context.Context, orbID, snapshotID string, out io.Writer) error {
	var stdout bytes.Buffer
	if err := d.exec(ctx, &stdout, out, nil, "container", "inspect", "--format", "{{.HostConfig.Runtime}} {{.State.Status}}", orbID); err != nil {
		return err
	}
	rt, status, _ := strings.Cut(strings.TrimSpace(stdout.String()), " ")
	args := commitArgs(orbID, snapshotID)
	if !isSysboxRuntime(rt) {
		return d.run(ctx, out, nil, args...)
	}
	switch status {
	case "running", "restarting":
		fmt.Fprintf(out, "oryn: %s 容器运行中 commit 拍不到上层内容，先停机再快照\n", rt)
		err := d.run(ctx, out, nil, "stop", "-t", SnapshotStopSeconds, orbID)
		if err == nil {
			if d.hookSnapshotStopped != nil {
				d.hookSnapshotStopped()
			}
			err = d.run(ctx, out, nil, args...)
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SnapshotRestartTimeout)
		defer cancel()
		if serr := d.run(rctx, out, nil, "start", orbID); serr != nil {
			err = errors.Join(err, fmt.Errorf("快照后重新启动容器: %w", serr))
		}
		return err
	case "paused":
		fmt.Fprintf(out, "oryn: %s 容器暂停中 commit 拍不到上层内容，停机后快照，拍完保持停机\n", rt)
		if err := d.run(ctx, out, nil, "unpause", orbID); err != nil {
			return err
		}
		if err := d.run(ctx, out, nil, "stop", "-t", SnapshotStopSeconds, orbID); err != nil {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SnapshotRestartTimeout)
			defer cancel()
			if perr := d.run(rctx, out, nil, "pause", orbID); perr != nil {
				err = errors.Join(err, fmt.Errorf("恢复暂停: %w", perr))
			}
			return err
		}
	}
	return d.run(ctx, out, nil, args...)
}

// snapshotResult 计算快照相对基础镜像多占的字节数 max(0, Size(快照) − Size(基础镜像))。
// 读取失败只记进输出并不给结果（快照本身已经提交成功，不因统计失败而重做）。
func (d *Docker) snapshotResult(ctx context.Context, snapshotID string, out io.Writer) *apigen.JobResult {
	snap, err := d.imageSize(ctx, snapshotID, out)
	if err == nil {
		var base int64
		if base, err = d.imageSize(ctx, d.Cfg.Image, out); err == nil {
			n := max(0, snap-base)
			return &apigen.JobResult{SnapshotBytes: &n}
		}
	}
	fmt.Fprintf(out, "oryn: 读取快照大小失败（不上报 snapshotBytes）: %v\n", err)
	return nil
}

func (d *Docker) imageSize(ctx context.Context, image string, out io.Writer) (int64, error) {
	var stdout bytes.Buffer
	if err := d.exec(ctx, &stdout, out, nil, "image", "inspect", "--format", "{{.Size}}", image); err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(stdout.String()), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("docker: 镜像 %s 的大小 %q 无法解析: %w", image, strings.TrimSpace(stdout.String()), err)
	}
	return n, nil
}

// OrbHome 是 orb 镜像内 orbd 用户的 HOME（镜像约定，见 images/orb/Dockerfile）。
const OrbHome = "/home/user"

// OrbTokenFile 是 orb 内 orbd 优先读取的令牌文件（相对 $HOME）。每次 spawn 都会轮换 orb 令牌，
// 对已存在的容器只能经此文件下发新令牌（容器的环境变量在创建时就固定了）。
const OrbTokenFile = ".oryn/orb-token"

// writeTokenScript 在容器内以 orbd 用户原子替换令牌文件（内容取自 stdin，0600）。
const writeTokenScript = `umask 077 && mkdir -p "$HOME/.oryn" && cat > "$HOME/.oryn/orb-token.tmp" && mv -f "$HOME/.oryn/orb-token.tmp" "$HOME/.oryn/orb-token"`

// spawn 创建 / 解冻 / 启动 orb 容器。请求了快照时结果带 snapshotUsed（S-P1-1）：新容器看快照镜像是否存在；
// 已有容器看它创建时所用的镜像（.Config.Image）是不是这个快照。
func (d *Docker) spawn(ctx context.Context, j Job, out io.Writer) (*apigen.JobResult, error) {
	p := j.Spawn
	if p == nil {
		return nil, errors.New("docker: spawn 缺少参数")
	}
	snap := ""
	if p.SnapshotID != nil {
		snap = *p.SnapshotID
	}
	result := func(used bool) *apigen.JobResult {
		if snap == "" {
			return nil
		}
		return &apigen.JobResult{SnapshotUsed: &used}
	}
	status, exists, err := d.status(ctx, j.OrbID, out)
	if err != nil {
		return nil, err
	}
	if exists {
		switch status {
		case "running", "restarting":
		case "paused":
			if err := d.run(ctx, out, nil, "unpause", j.OrbID); err != nil {
				return nil, err
			}
		case "exited", "created":
			if err := d.run(ctx, out, nil, "start", j.OrbID); err != nil {
				return nil, err
			}
		case "dead":
			if err := d.run(ctx, out, nil, "rm", "-f", j.OrbID); err != nil {
				return nil, err
			}
			exists = false
		default:
			return nil, fmt.Errorf("docker: 容器 %s 处于 %q，无法 spawn", j.OrbID, status)
		}
	}
	if exists {
		// 已有容器：写入本次轮换后的令牌，再让 orbd 立即用它重建 link。
		if err := d.writeToken(ctx, j.OrbID, p.Env.OrynOrbToken, out); err != nil {
			return nil, err
		}
		d.hupOrbd(ctx, j.OrbID, out)
		if snap == "" {
			return nil, nil
		}
		var img bytes.Buffer
		if err := d.exec(ctx, &img, out, nil, "container", "inspect", "--format", "{{.Config.Image}}", j.OrbID); err != nil {
			return nil, err
		}
		return result(strings.TrimSpace(img.String()) == snap), nil
	}
	typ, ok := d.Cfg.Types[p.Type]
	if !ok {
		typ = d.Cfg.Types[d.Cfg.DefaultType]
	}
	image := d.Cfg.Image
	if p.Image != "" {
		image = p.Image
	}
	used := false
	if snap != "" {
		found, err := d.imageExists(ctx, snap, out)
		switch {
		case err != nil:
			return nil, err
		case found:
			image, used = snap, true
		case p.SnapshotOptional != nil && *p.SnapshotOptional:
			fmt.Fprintf(out, "oryn: 快照 %s 不存在，改用基础镜像\n", snap)
		default:
			return nil, fmt.Errorf("docker: 快照镜像 %s 不存在", snap)
		}
	}
	args := []string{"create", "--init",
		"--name", j.OrbID, "--hostname", j.OrbID, "--label", "oryn-orb=1",
		"--cpus", strconv.FormatFloat(typ.CPUs, 'f', -1, 64), "--memory", typ.Memory}
	if d.Cfg.PidsLimit > 0 {
		// 所有类型都限制进程数（S-P2-6）：sysbox 宿主调高了 kernel.pid_max，fork 炸弹的上限随之放大。
		args = append(args, "--pids-limit", strconv.Itoa(d.Cfg.PidsLimit))
	}
	args = append(args, "--shm-size", "1g", "--add-host", "host.docker.internal:host-gateway")
	if !typ.NestedDocker {
		// agent 在容器内有 root（免密 sudo）：去掉原始套接字与网络配置能力（防 ARP 欺骗 / 改路由），
		// 网络隔离另见 docs/deploy.md「安全」一节。
		// 嵌套 Docker 的类型保留这两项：内层 dockerd 要建网桥、写 iptables；sysbox 下它们只作用于容器自己的
		// 网络命名空间，宿主须按 docs/deploy.md 建隔离网桥。
		args = append(args, "--cap-drop", "NET_RAW", "--cap-drop", "NET_ADMIN")
	}
	args = append(args, "-e", "ORYN_URL", "-e", "ORYN_ORB_ID", "-e", "ORYN_ORB_TOKEN")
	// 令牌与 CA 经环境变量传给 docker CLI（`-e NAME` 取调用方环境），不出现在 argv 中。
	env := []string{"ORYN_URL=" + p.Env.OrynURL, "ORYN_ORB_ID=" + j.OrbID, "ORYN_ORB_TOKEN=" + p.Env.OrynOrbToken}
	if p.OrbCaPem != nil && *p.OrbCaPem != "" {
		args = append(args, "-e", "ORYN_ORB_CA_PEM")
		env = append(env, "ORYN_ORB_CA_PEM="+*p.OrbCaPem)
	}
	if typ.NestedDocker {
		args = append(args, "-e", "ORYN_NESTED_DOCKER")
		env = append(env, "ORYN_NESTED_DOCKER=1")
	}
	if typ.Runtime != "" {
		args = append(args, "--runtime", typ.Runtime)
	}
	if !hasSeccompOpt(d.Cfg.ExtraRunArgs) {
		// docker CLI 在客户端读取 profile 文件并内联发给守护进程，create 返回后即可删除。
		f, err := writeSeccompProfile()
		if err != nil {
			return nil, err
		}
		defer os.Remove(f)
		args = append(args, "--security-opt", "seccomp="+f)
	}
	args = append(args, d.Cfg.ExtraRunArgs...)
	args = append(args, image, "orbd", "run")
	if err := d.run(ctx, out, env, args...); err != nil {
		return nil, err
	}
	// 快照镜像里带着源 orb 的令牌文件，而 orbd 优先读文件：启动前替换成本 orb 的令牌，
	// 否则新容器会以源 orb 的身份连上控制面。
	if err := d.replaceStaleToken(ctx, j.OrbID, p.Env.OrynOrbToken, out); err != nil {
		return nil, err
	}
	if err := d.run(ctx, out, nil, "start", j.OrbID); err != nil {
		return nil, err
	}
	return result(used), nil
}

// orbSeccompProfile 是 orb 容器的 seccomp 配置：moby/profiles 的 Docker 默认配置
// （seccomp/default.json，提交 f862455628f78f84c937e967e4a03902fb1dee11）末尾追加一条规则，无条件放行
// clone / unshare（含 CLONE_NEWUSER），让桌面上的 Chromium 能建立 user namespace 沙箱、不必 --no-sandbox。
// 其余规则与默认配置相同（mount、setns、clone3 等仍按默认只给 CAP_SYS_ADMIN）。
// 升级时取 moby/profiles 新版本的 default.json，再追加同一条规则。
//
//go:embed orb-seccomp.json
var orbSeccompProfile []byte

// hasSeccompOpt 报告 extra_run_args 里是否已自带 seccomp 配置（此时不再追加 Oryn 的配置）。
func hasSeccompOpt(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "seccomp=") {
			return true
		}
	}
	return false
}

// writeSeccompProfile 把 orbSeccompProfile 写进一个新的临时文件并返回路径（调用方负责删除）。
func writeSeccompProfile() (string, error) {
	f, err := os.CreateTemp("", "oryn-orb-seccomp-*.json")
	if err != nil {
		return "", fmt.Errorf("docker: 写 seccomp 配置: %w", err)
	}
	_, err = f.Write(orbSeccompProfile)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("docker: 写 seccomp 配置: %w", err)
	}
	return f.Name(), nil
}

// writeToken 在运行中的容器内写入令牌文件（令牌经 stdin 传入，不进 argv 与输出）。
func (d *Docker) writeToken(ctx context.Context, orbID, token string, out io.Writer) error {
	return d.execIn(ctx, strings.NewReader(token), out, out, nil, "exec", "-i", orbID, "sh", "-c", writeTokenScript)
}

// replaceStaleToken 在尚未启动的容器里，若镜像自带令牌文件，则以相同属主与 0600 写入新令牌；没有则什么也不做。
func (d *Docker) replaceStaleToken(ctx context.Context, orbID, token string, out io.Writer) error {
	dir := OrbHome + "/" + path.Dir(OrbTokenFile)
	var archive bytes.Buffer
	err := d.exec(ctx, &archive, io.Discard, nil, "cp", orbID+":"+OrbHome+"/"+OrbTokenFile, "-")
	if isNoSuch(err) {
		return nil
	}
	if err != nil {
		return err
	}
	hdr, err := tar.NewReader(&archive).Next()
	if err != nil {
		return fmt.Errorf("docker: 读取镜像内令牌文件: %w", err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: path.Base(OrbTokenFile), Mode: 0o600, Size: int64(len(token)),
		Uid: hdr.Uid, Gid: hdr.Gid, Uname: hdr.Uname, Gname: hdr.Gname, ModTime: time.Now(),
	}); err != nil {
		return err
	}
	if _, err := io.WriteString(tw, token); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	fmt.Fprintln(out, "oryn: 替换快照中遗留的 orb 令牌文件")
	return d.execIn(ctx, &buf, out, out, nil, "cp", "-a", "-", orbID+":"+dir)
}

// hupOrbd 让 orbd 立即用新令牌重建 link（计划 Assumptions 的 docker pause 预案）；失败只记录。
func (d *Docker) hupOrbd(ctx context.Context, orbID string, out io.Writer) {
	if err := d.run(ctx, out, nil, "exec", orbID, "pkill", "-HUP", "-x", "orbd"); err != nil {
		fmt.Fprintf(out, "oryn: 通知 orbd 重连失败（忽略）: %v\n", err)
	}
}

func (d *Docker) pause(ctx context.Context, j Job, out io.Writer) error {
	status, exists, err := d.status(ctx, j.OrbID, out)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("docker: 容器 %s 不存在", j.OrbID)
	}
	if j.PauseMode == "freeze" {
		if status != "running" && status != "restarting" {
			return nil
		}
		return d.run(ctx, out, nil, "pause", j.OrbID)
	}
	switch status {
	case "exited", "created", "dead":
		return nil
	case "paused":
		if err := d.run(ctx, out, nil, "unpause", j.OrbID); err != nil {
			return err
		}
	}
	return d.run(ctx, out, nil, "stop", "-t", "30", j.OrbID)
}

// status 返回容器状态（created / running / paused / restarting / exited / dead …）。
func (d *Docker) status(ctx context.Context, orbID string, out io.Writer) (string, bool, error) {
	var stdout bytes.Buffer
	err := d.exec(ctx, &stdout, out, nil, "container", "inspect", "--format", "{{.State.Status}}", orbID)
	if isNoSuch(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(stdout.String()), true, nil
}

func (d *Docker) imageExists(ctx context.Context, image string, out io.Writer) (bool, error) {
	err := d.exec(ctx, io.Discard, out, nil, "image", "inspect", "--format", "{{.Id}}", image)
	if isNoSuch(err) {
		return false, nil
	}
	return err == nil, err
}

func (d *Docker) run(ctx context.Context, out io.Writer, env []string, args ...string) error {
	return d.exec(ctx, out, out, env, args...)
}

// cmdError 是 docker 命令的非零退出，带 stderr 便于识别 "No such …"。
type cmdError struct {
	args   []string
	err    error
	stderr string
}

func (e *cmdError) Error() string {
	return fmt.Sprintf("docker %s: %v: %s", strings.Join(e.args, " "), e.err, strings.TrimSpace(e.stderr))
}

func (d *Docker) exec(ctx context.Context, stdout, out io.Writer, env []string, args ...string) error {
	return d.execIn(ctx, nil, stdout, out, env, args...)
}

// execIn 同 exec，stdin 非空时作为命令的标准输入。
func (d *Docker) execIn(ctx context.Context, stdin io.Reader, stdout, out io.Writer, env []string, args ...string) error {
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	fmt.Fprintf(out, "$ docker %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(&stderr, out)
	if err := cmd.Run(); err != nil {
		return &cmdError{args: args, err: err, stderr: stderr.String()}
	}
	return nil
}

// streamCmd 是以流方式读取标准输出的 docker 命令（见 execStream）。
type streamCmd struct {
	io.Reader
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stderr *tailBuffer
	args   []string
	once   sync.Once
	err    error
}

// execStream 启动 docker 命令并返回其标准输出的读端（不缓冲）。读完后调用 Wait 取退出结果；
// 提前放弃时调用 Close 结束进程。两者都可重复调用，只等待进程一次。
func (d *Docker) execStream(ctx context.Context, args ...string) (*streamCmd, error) {
	bin := d.Bin
	if bin == "" {
		bin = "docker"
	}
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, bin, args...)
	s := &streamCmd{cmd: cmd, cancel: cancel, stderr: newTailBuffer(4096), args: args}
	cmd.Stderr = s.stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, &cmdError{args: args, err: err}
	}
	s.Reader = stdout
	return s, nil
}

func (s *streamCmd) wait() error {
	s.once.Do(func() {
		if err := s.cmd.Wait(); err != nil {
			s.err = &cmdError{args: s.args, err: err, stderr: s.stderr.String()}
		}
		s.cancel()
	})
	return s.err
}

// Wait 丢弃剩余输出并等待进程退出；非零退出返回 *cmdError（带 stderr）。
func (s *streamCmd) Wait() error {
	_, _ = io.Copy(io.Discard, s.Reader)
	return s.wait()
}

// Close 结束进程并回收。
func (s *streamCmd) Close() error {
	s.cancel()
	return s.wait()
}

// isNoSuch 判断错误是否为「对象不存在」。
func isNoSuch(err error) bool {
	var ce *cmdError
	if !errors.As(err, &ce) {
		return false
	}
	s := strings.ToLower(ce.stderr)
	return strings.Contains(s, "no such container") || strings.Contains(s, "no such object") ||
		strings.Contains(s, "no such image") || strings.Contains(s, "could not find the file")
}
