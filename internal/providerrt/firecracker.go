package providerrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/fcguest"
)

// Firecracker 驱动的时限。
const (
	// FCStartTimeout 是 InstanceStart 之后 fcguest start 的重试时限（guest 内核与 guest-init 起来之前 vsock 连不上）。
	FCStartTimeout = 15 * time.Second
	// FCSyncTimeout 是冻结前 vsock sync 的时限（尽力）。
	FCSyncTimeout = 5 * time.Second
	// FCShutdownGrace 是 stop 时给 guest 内 orbd 的宽限秒数；FCShutdownWait 是等 firecracker 进程退出的时限。
	FCShutdownGrace = 30
	FCShutdownWait  = 40 * time.Second
	// fcGuestOpTimeout 是单个 fcguest 请求（token / fsfreeze / fsthaw …）的时限。
	fcGuestOpTimeout = 10 * time.Second
)

// FirecrackerDriver 管理每个 orb 一台的 Firecracker microVM（计划 8.0 / 8.3）。
// 所有副作用只经 host / api / guest 三个接口；状态目录（元数据、槽位、快照、基础盘）直接读写。
type FirecrackerDriver struct {
	Cfg config.Firecracker
	// OrbURL 是 provider.yaml 的 orb_url（预检核对其端口在 nft 放行列表里）。
	OrbURL string

	paths fcPaths
	host  fcHost
	api   fcAPI
	guest fcGuest

	// now 是时钟（nil = time.Now）；startTimeout / shutdownWait / pollEvery 可被测试缩短。
	now          func() time.Time
	startTimeout time.Duration
	shutdownWait time.Duration
	pollEvery    time.Duration
	// consoleMax / consoleKeep 覆盖串口日志的占用上限与打孔后保留的末尾（0 = 缺省，测试用）。
	consoleMax, consoleKeep int64
	// etcDir 是预检读取 subuid / subgid / passwd / group 的目录（空 = /etc，测试注入）。
	etcDir string

	digests digestCache

	// launchMu 串行化「核对容量 → 启动」与槽位、基础盘的增删，保证并发 spawn 不会一起越过 limits。
	launchMu sync.Mutex
	// orbMu 让同一台 VM 的任务串行执行。
	orbMu sync.Map // orbID → *sync.Mutex
	// diskGates 让停机预览的读盘与 spawn / delete 互斥（见 offlinefs_fc.go）。
	diskGates sync.Map // orbID → *fcDiskGate

	// offline 缓存停机 orb 的文件索引（只读预览，见 offlinefs_fc.go）。
	offline indexCache
}

// NewFirecracker 创建 firecracker 驱动（真实现：jailer / ip / mke2fs、api.sock、v.sock）。
func NewFirecracker(cfg config.Firecracker, orbURL string) *FirecrackerDriver {
	if len(cfg.Types) == 0 {
		cfg.Types = config.DefaultFirecrackerTypes()
	}
	p := newFCPaths(cfg)
	return &FirecrackerDriver{
		Cfg: cfg, OrbURL: orbURL, paths: p,
		host:  newOSHost(cfg),
		api:   unixAPI{sock: p.apiSock},
		guest: vsockGuest{sock: p.vsockPath},
	}
}

func (d *FirecrackerDriver) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func (d *FirecrackerDriver) lockOrb(orbID string) func() {
	v, _ := d.orbMu.LoadOrStore(orbID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Info 实现 Driver。
func (d *FirecrackerDriver) Info() Info {
	types := make([]apigen.OrbType, 0, len(d.Cfg.Types))
	for _, name := range slices.Sorted(maps.Keys(d.Cfg.Types)) {
		t := d.Cfg.Types[name]
		types = append(types, apigen.OrbType{Name: name, Cpus: float32(t.CPUs), MemoryMB: t.MemoryMB()})
	}
	var limits *apigen.ProviderHostLimits
	if d.Cfg.Limits.MaxVMs > 0 || d.Cfg.Limits.MaxMemoryMB > 0 {
		limits = &apigen.ProviderHostLimits{}
		if d.Cfg.Limits.MaxVMs > 0 {
			limits.MaxVMs = new(d.Cfg.Limits.MaxVMs)
		}
		if d.Cfg.Limits.MaxMemoryMB > 0 {
			limits.MaxMemoryMB = new(d.Cfg.Limits.MaxMemoryMB)
		}
	}
	return Info{
		Driver:       apigen.Firecracker,
		Capabilities: apigen.ProviderCapabilities{Freeze: true, Snapshot: true, OfflineFs: new(true)},
		Types:        types,
		DefaultType:  d.Cfg.DefaultType,
		BaseImageID:  d.baseImageID(),
		DiskPath:     d.Cfg.StateDir,
		Limits:       limits,
	}
}

// baseImageID = "fc:" + sha256(rootfs)[:16] + ":" + sha256(vmlinux)[:16]（按文件大小 + mtime 缓存）；读取失败给空串。
func (d *FirecrackerDriver) baseImageID() string {
	r, err := d.digests.sum(d.Cfg.Rootfs)
	if err == nil {
		var k string
		if k, err = d.digests.sum(d.Cfg.Kernel); err == nil {
			return "fc:" + r[:16] + ":" + k[:16]
		}
	}
	slog.Warn("读取 Firecracker 产物摘要失败（本次 hello 不声明 baseImageId）", "err", err)
	return ""
}

// Exec 实现 Driver。
func (d *FirecrackerDriver) Exec(ctx context.Context, j Job, out io.Writer) (*apigen.JobResult, error) {
	if j.OrbID != "" {
		defer d.lockOrb(j.OrbID)()
		// spawn / delete 会改动（或删除）可写盘：先作废进行中的停机预览，操作期间不接受新的预览
		if j.Op == "spawn" || j.Op == "delete" {
			defer d.beginLifecycle(j.OrbID)()
		}
	}
	switch j.Op {
	case "spawn":
		return d.spawn(ctx, j, out)
	case "pause":
		return nil, d.pause(ctx, j, out)
	case "snapshot":
		return d.snapshot(ctx, j, out)
	case "delete":
		return nil, d.delete(j.OrbID, out)
	case "delete_snapshot":
		return nil, d.deleteSnapshot(j.SnapshotID, out)
	}
	return nil, fmt.Errorf("firecracker: 不支持的操作 %q", j.Op)
}

// vmState 返回 VM 的运行状态：进程不在 → ""；在但 API 不可达 → ("", err)。
func (d *FirecrackerDriver) vmState(ctx context.Context, orbID string) (string, error) {
	if !d.host.Alive(orbID) {
		return "", nil
	}
	return d.api.State(ctx, orbID)
}

// spawn 新建 / 恢复 / 重启 VM（8.0 表的前四行）。请求了快照时结果带 snapshotUsed：
// 新 VM 看快照盘是否存在并被复制；已有 VM 看其可写盘当初复制自哪个快照（meta.snapshot）。
func (d *FirecrackerDriver) spawn(ctx context.Context, j Job, out io.Writer) (*apigen.JobResult, error) {
	p := j.Spawn
	if p == nil {
		return nil, errors.New("firecracker: spawn 缺少参数")
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
	vm, exists, err := d.paths.loadVM(j.OrbID)
	if err != nil {
		return nil, err
	}
	if !exists {
		if vm, err = d.create(j, out); err != nil {
			return nil, err
		}
	} else {
		state, err := d.vmState(ctx, j.OrbID)
		switch {
		case err != nil:
			// 进程在但 API 不可达：杀掉按停机处理，磁盘原样。
			fmt.Fprintf(out, "oryn: VM %s 的 API 不可达（%v），重启\n", j.OrbID, err)
			if err := d.host.Kill(j.OrbID); err != nil {
				return nil, err
			}
		case state == fcStatePaused || state == fcStateRunning:
			if fileExists(d.paths.vmThawPending(j.OrbID)) {
				// 上次运行中快照没收尾：先恢复 VM 并解冻文件系统（start 要在可写盘上写文件）。
				if err := d.finishThaw(ctx, j.OrbID); err != nil {
					return nil, err
				}
			} else if state == fcStatePaused {
				if err := d.api.SetState(ctx, j.OrbID, "Resumed"); err != nil {
					return nil, err
				}
			}
			// Firecracker 在运行不等于 orbd 已拉起（上次可能在 InstanceStart 之后、guest start 之前中断）：
			// 发带完整参数的 start，guest 已拉起过时把它当作 token（只换令牌），没拉起过则现在拉起。
			if err := d.startGuest(ctx, j.OrbID, d.startRequest(vm, p), out); err != nil {
				return nil, err
			}
			return result(vm.Snapshot != "" && vm.Snapshot == snap), nil
		case state != "":
			// Not started 等：上一次启动没走完，重新走启动一段。
			if err := d.host.Kill(j.OrbID); err != nil {
				return nil, err
			}
		}
	}
	if err := d.boot(ctx, vm, p, out); err != nil {
		return nil, err
	}
	return result(vm.Snapshot != "" && vm.Snapshot == snap), nil
}

// create 新建 VM 的元数据与可写盘：分配槽位，有快照则稀疏复制快照盘（基础盘取快照的），否则建空盘（基础盘取当前 rootfs）。
// 快照不存在（或其基础盘已不在）：snapshotOptional 时退回空盘，否则报错。meta.json 最后写入，它在即 VM 在。
func (d *FirecrackerDriver) create(j Job, out io.Writer) (vmMeta, error) {
	p := j.Spawn
	typeName := p.Type
	typ, ok := d.Cfg.Types[typeName]
	if !ok {
		typeName = d.Cfg.DefaultType
		typ = d.Cfg.Types[typeName]
	}
	d.launchMu.Lock()
	defer d.launchMu.Unlock()
	// 先核对容量：起不来的 VM 不建盘、不占槽位（启动前 launch 还会再核对一次）。
	if err := d.checkCapacity(vmMeta{OrbID: j.OrbID, MemMB: typ.MemoryMB()}); err != nil {
		return vmMeta{}, err
	}
	snap, base := "", ""
	if p.SnapshotID != nil && *p.SnapshotID != "" {
		id := *p.SnapshotID
		var sm snapMeta
		found, err := readJSONFile(d.paths.snapMeta(id), &sm)
		if err != nil {
			return vmMeta{}, err
		}
		if found {
			found = fileExists(d.paths.snapDisk(id)) && fileExists(d.paths.base(sm.Base))
		}
		switch {
		case found:
			snap, base = id, sm.Base
		case p.SnapshotOptional != nil && *p.SnapshotOptional:
			fmt.Fprintf(out, "oryn: 快照 %s 不存在，改用空盘\n", id)
		default:
			return vmMeta{}, fmt.Errorf("firecracker: 快照 %s 不存在", id)
		}
	}
	if base == "" {
		var err error
		if base, err = d.ensureBase(); err != nil {
			return vmMeta{}, err
		}
	}
	slot, err := d.paths.allocSlot(j.OrbID)
	if err != nil {
		return vmMeta{}, err
	}
	vm := newVMMeta(d.Cfg, j.OrbID, slot)
	vm.Base, vm.Snapshot, vm.Type, vm.CPUs, vm.MemMB = base, snap, typeName, typ.CPUs, typ.MemoryMB()
	if err := os.MkdirAll(d.paths.vmDir(j.OrbID), 0o700); err != nil {
		return vmMeta{}, err
	}
	disk := d.paths.vmDisk(j.OrbID)
	tmp := disk + ".tmp"
	_ = os.Remove(tmp)
	if snap != "" {
		if _, err := d.host.CopySparse(d.paths.snapDisk(snap), tmp, vm.UID); err != nil {
			return vmMeta{}, err
		}
	} else if err := d.host.MakeDisk(tmp, d.Cfg.DiskGB, vm.UID); err != nil {
		return vmMeta{}, err
	}
	if err := os.Rename(tmp, disk); err != nil {
		return vmMeta{}, err
	}
	if err := writeJSONFile(d.paths.vmMeta(j.OrbID), vm); err != nil {
		return vmMeta{}, err
	}
	return vm, nil
}

// ensureBase 返回当前 rootfs 的基础盘 id，首次见到时硬链接进 bases/。
func (d *FirecrackerDriver) ensureBase() (string, error) {
	sum, err := d.digests.sum(d.Cfg.Rootfs)
	if err != nil {
		return "", fmt.Errorf("firecracker: 读取 rootfs: %w", err)
	}
	id := sum[:16]
	dst := d.paths.base(id)
	if fileExists(dst) {
		return id, nil
	}
	if err := os.MkdirAll(d.paths.basesDir(), 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Link(d.Cfg.Rootfs, tmp); err != nil {
		return "", fmt.Errorf("firecracker: 硬链接 rootfs 为基础盘（state_dir 须与产物在同一文件系统）: %w", err)
	}
	return id, os.Rename(tmp, dst)
}

// boot 走「启动」一段：核对容量 → tap → jailer → API 配置并 InstanceStart → vsock start（重试至多 startTimeout）。
// 任何一步失败都杀掉进程、拆 tap，下一次 spawn 从头再来（磁盘与槽位保留）。
func (d *FirecrackerDriver) boot(ctx context.Context, vm vmMeta, p *apigen.JobSpawnParams, out io.Writer) (err error) {
	defer func() {
		if err != nil {
			d.consoleTail(vm, out)
			if kerr := d.host.Kill(vm.OrbID); kerr != nil {
				fmt.Fprintf(out, "oryn: 清理启动失败的 VM: %v\n", kerr)
			}
			if terr := d.host.TapDown(vm); terr != nil {
				fmt.Fprintf(out, "oryn: 拆 tap: %v\n", terr)
			}
		}
	}()
	if err := d.launch(ctx, vm); err != nil {
		return err
	}
	// 旧进程已被 Launch 清掉，遗留的冻结随之失效。
	if err := removeIfExists(d.paths.vmThawPending(vm.OrbID)); err != nil {
		return err
	}
	if err := d.api.Boot(ctx, vm); err != nil {
		return err
	}
	return d.startGuest(ctx, vm.OrbID, d.startRequest(vm, p), out)
}

// startRequest 是 guest 的 start 请求（令牌与 CA 只走 vsock，不进输出）。
func (d *FirecrackerDriver) startRequest(vm vmMeta, p *apigen.JobSpawnParams) fcguest.Request {
	req := fcguest.Request{
		Op: fcguest.OpStart, URL: p.Env.OrynURL, OrbID: vm.OrbID, Token: p.Env.OrynOrbToken,
		DNS: d.Cfg.Network.DNS, NowMs: d.clock().UnixMilli(),
	}
	if p.OrbCaPem != nil {
		req.CAPEM = *p.OrbCaPem
	}
	return req
}

// launch 在 launchMu 下核对 limits、建 tap 并启动进程（之后的 spawn 才能把它算进容量）。
func (d *FirecrackerDriver) launch(ctx context.Context, vm vmMeta) error {
	d.launchMu.Lock()
	defer d.launchMu.Unlock()
	if err := d.checkCapacity(vm); err != nil {
		return err
	}
	if err := d.host.TapUp(vm); err != nil {
		return err
	}
	return d.host.Launch(ctx, vm)
}

// checkCapacity 核对加上 vm 之后运行中（含暂停）的 VM 数与内存总和不超过 limits。
func (d *FirecrackerDriver) checkCapacity(vm vmMeta) error {
	vms, err := d.paths.listVMs()
	if err != nil {
		return err
	}
	n, mem := 1, vm.MemMB
	for _, m := range vms {
		if m.OrbID != vm.OrbID && d.host.Alive(m.OrbID) {
			n++
			mem += m.MemMB
		}
	}
	l := d.Cfg.Limits
	if n > l.MaxVMs {
		return fmt.Errorf("capacity: 运行中的 VM 将达 %d 台，超过 limits.max_vms %d", n, l.MaxVMs)
	}
	if mem > l.MaxMemoryMB {
		return fmt.Errorf("capacity: 运行中的 VM 内存将达 %d MiB，超过 limits.max_memory_mb %d", mem, l.MaxMemoryMB)
	}
	return nil
}

// startGuest 反复发 start 直到成功或超过 startTimeout（guest 内核启动期间 vsock 连不上）。
// guest 明确拒绝（RemoteError）时不再重试。令牌与 CA 只走 vsock，不进输出。
func (d *FirecrackerDriver) startGuest(ctx context.Context, orbID string, req fcguest.Request, out io.Writer) error {
	timeout := d.startTimeout
	if timeout == 0 {
		timeout = FCStartTimeout
	}
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		err := d.guestDo(sctx, orbID, req)
		if err == nil {
			return nil
		}
		var re *fcguest.RemoteError
		if errors.As(err, &re) {
			return err
		}
		select {
		case <-sctx.Done():
			fmt.Fprintf(out, "oryn: guest %s 在 %s 内没有应答 start\n", orbID, timeout)
			return fmt.Errorf("firecracker: guest 未就绪: %w", err)
		case <-time.After(d.poll()):
		}
	}
}

func (d *FirecrackerDriver) poll() time.Duration {
	if d.pollEvery > 0 {
		return d.pollEvery
	}
	return 200 * time.Millisecond
}

// guestDo 发一个 fcguest 请求，受 fcGuestOpTimeout 约束。
func (d *FirecrackerDriver) guestDo(ctx context.Context, orbID string, req fcguest.Request) error {
	c, cancel := context.WithTimeout(ctx, fcGuestOpTimeout)
	defer cancel()
	return d.guest.Do(c, orbID, req)
}

// pause 实现 8.0 的 pause{freeze} / pause{stop}。
func (d *FirecrackerDriver) pause(ctx context.Context, j Job, out io.Writer) error {
	vm, exists, err := d.paths.loadVM(j.OrbID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("firecracker: VM %s 不存在", j.OrbID)
	}
	state, serr := d.vmState(ctx, j.OrbID)
	if j.PauseMode == "freeze" {
		if serr != nil {
			return serr
		}
		if state != "" && fileExists(d.paths.vmThawPending(j.OrbID)) {
			// 上次运行中快照没收尾（VM 可能停在 Paused、文件系统冻结）：先收尾，再按运行中冻结。
			if err := d.finishThaw(ctx, j.OrbID); err != nil {
				return err
			}
			state = fcStateRunning
		}
		if state != fcStateRunning {
			return nil // 已暂停或不在运行
		}
		sctx, cancel := context.WithTimeout(ctx, FCSyncTimeout)
		if err := d.guest.Do(sctx, j.OrbID, fcguest.Request{Op: fcguest.OpSync}); err != nil {
			fmt.Fprintf(out, "oryn: 冻结前 sync 失败（继续）: %v\n", err)
		}
		cancel()
		return d.api.SetState(ctx, j.OrbID, "Paused")
	}
	if !d.host.Alive(j.OrbID) {
		return d.host.TapDown(vm) // 已停机：只确保 tap 拆掉
	}
	if state == fcStatePaused {
		if err := d.api.SetState(ctx, j.OrbID, "Resumed"); err != nil {
			fmt.Fprintf(out, "oryn: 恢复 VM 以便关机失败（继续）: %v\n", err)
		}
	}
	gctx, cancel := context.WithTimeout(ctx, (FCShutdownGrace+5)*time.Second)
	err = d.guest.Do(gctx, j.OrbID, fcguest.Request{Op: fcguest.OpShutdown, GraceSeconds: FCShutdownGrace})
	cancel()
	switch {
	case err == nil:
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		// guest 应答后立即重启，应答可能来不及经 vsock 送达（真机上实测如此）：连接被关即视为正在关机，只等进程退出。
	default:
		fmt.Fprintf(out, "oryn: guest shutdown 失败，改发 Ctrl+Alt+Del: %v\n", err)
		if err := d.api.CtrlAltDel(ctx, j.OrbID); err != nil {
			fmt.Fprintf(out, "oryn: Ctrl+Alt+Del 失败: %v\n", err)
		}
	}
	if !d.waitExit(ctx, j.OrbID) {
		fmt.Fprintf(out, "oryn: VM %s 未在时限内退出，cgroup.kill\n", j.OrbID)
	}
	if err := d.host.Kill(j.OrbID); err != nil {
		return err
	}
	// 进程已不在，遗留的冻结随之失效。
	if err := removeIfExists(d.paths.vmThawPending(j.OrbID)); err != nil {
		return err
	}
	return d.host.TapDown(vm)
}

// waitExit 等 firecracker 进程退出，至多 shutdownWait；返回是否已退出。
func (d *FirecrackerDriver) waitExit(ctx context.Context, orbID string) bool {
	wait := d.shutdownWait
	if wait == 0 {
		wait = FCShutdownWait
	}
	deadline := time.Now().Add(wait)
	for {
		if !d.host.Alive(orbID) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d.poll()):
		}
	}
}

// snapshot 实现 8.0 的 snapshot：运行中先 fsfreeze → 暂停 → 稀疏复制 → 恢复 → fsthaw；已暂停或已停机直接复制。
// 快照已存在时直接返回它的大小（幂等）。上次运行中快照没恢复成功（thaw-pending 在）时，先完成源 VM 的恢复：
// 遗留的 Paused 不是用户要求的冻结，不能当作「已暂停」直接复制，也不能因快照已存在就直接成功。
func (d *FirecrackerDriver) snapshot(ctx context.Context, j Job, out io.Writer) (*apigen.JobResult, error) {
	if fileExists(d.paths.vmThawPending(j.OrbID)) {
		if err := d.finishThaw(ctx, j.OrbID); err != nil {
			return nil, err
		}
	}
	var sm snapMeta
	found, err := readJSONFile(d.paths.snapMeta(j.SnapshotID), &sm)
	if err != nil {
		return nil, err
	}
	if found && fileExists(d.paths.snapDisk(j.SnapshotID)) {
		return &apigen.JobResult{SnapshotBytes: &sm.Bytes}, nil
	}
	vm, exists, err := d.paths.loadVM(j.OrbID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("firecracker: VM %s 不存在", j.OrbID)
	}
	if err := os.MkdirAll(d.paths.snapsDir(), 0o700); err != nil {
		return nil, err
	}
	state, err := d.vmState(ctx, j.OrbID)
	if err != nil {
		return nil, err
	}
	tmp := d.paths.snapDisk(j.SnapshotID) + ".tmp"
	_ = os.Remove(tmp)
	var bytes int64
	if state == fcStateRunning {
		bytes, err = d.copyRunning(ctx, j.OrbID, tmp)
	} else {
		bytes, err = d.host.CopySparse(d.paths.vmDisk(j.OrbID), tmp, 0)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, d.paths.snapDisk(j.SnapshotID)); err != nil {
		return nil, err
	}
	if err := writeJSONFile(d.paths.snapMeta(j.SnapshotID), snapMeta{Base: vm.Base, Bytes: bytes}); err != nil {
		return nil, err
	}
	return &apigen.JobResult{SnapshotBytes: &bytes}, nil
}

// copyRunning 在运行中的 VM 上拍快照：fsfreeze → Paused → 复制 → Resumed → fsthaw。复制失败也恢复并解冻。
// 恢复或解冻失败即整个快照失败（不发布）：VM 停在 Paused 时 guest 的 120 秒自动解冻也跑不了。
// 冻结之前先落下 thaw-pending 标记，直到恢复与解冻都成功才删掉；provider 中途崩溃或恢复失败时，
// 后续的 snapshot / spawn / pause{freeze} 据此先完成恢复。
func (d *FirecrackerDriver) copyRunning(ctx context.Context, orbID, dst string) (int64, error) {
	if err := os.WriteFile(d.paths.vmThawPending(orbID), nil, 0o600); err != nil {
		return 0, err
	}
	// 任务 ctx 超时也要恢复与解冻。
	thaw := func() error { return d.finishThaw(context.WithoutCancel(ctx), orbID) }
	if err := d.guestDo(ctx, orbID, fcguest.Request{Op: fcguest.OpFSFreeze}); err != nil {
		// 传输错误时请求可能已经生效：照样解冻（fsthaw 对未冻结的文件系统是无操作）。
		return 0, errors.Join(fmt.Errorf("firecracker: 冻结 guest 文件系统: %w", err), thaw())
	}
	if err := d.api.SetState(ctx, orbID, "Paused"); err != nil {
		return 0, errors.Join(err, thaw())
	}
	n, err := d.host.CopySparse(d.paths.vmDisk(orbID), dst, 0)
	if terr := thaw(); terr != nil {
		return 0, errors.Join(err, terr)
	}
	return n, err
}

// finishThaw 收尾运行中快照：VM 暂停着就 PATCH Resumed，再 fsthaw；两步都成功才删掉 thaw-pending 标记。
// VM 已不在运行（停机、未启动）时没有可恢复的冻结，直接删标记。
func (d *FirecrackerDriver) finishThaw(ctx context.Context, orbID string) error {
	state, err := d.vmState(ctx, orbID)
	if err != nil {
		return fmt.Errorf("firecracker: 快照后查询 VM 状态: %w", err)
	}
	switch state {
	case fcStatePaused:
		if err := d.api.SetState(ctx, orbID, "Resumed"); err != nil {
			return fmt.Errorf("firecracker: 快照后恢复 VM: %w", err)
		}
		fallthrough
	case fcStateRunning:
		if err := d.guestDo(ctx, orbID, fcguest.Request{Op: fcguest.OpFSThaw}); err != nil {
			return fmt.Errorf("firecracker: 快照后解冻 guest 文件系统: %w", err)
		}
	}
	return removeIfExists(d.paths.vmThawPending(orbID))
}

// delete 实现 8.0 的 delete：cgroup.kill → 拆 tap → 删 jail 与 VM 目录 → 释放槽位 → 回收基础盘。幂等。
func (d *FirecrackerDriver) delete(orbID string, out io.Writer) error {
	vm, exists, err := d.paths.loadVM(orbID)
	if err != nil {
		return err
	}
	if !exists {
		vm = vmMeta{OrbID: orbID, Tap: tapName(orbID)}
	}
	if err := d.host.Kill(orbID); err != nil {
		return err
	}
	if err := d.host.TapDown(vm); err != nil {
		return err
	}
	d.launchMu.Lock()
	defer d.launchMu.Unlock()
	if err := os.RemoveAll(d.paths.vmDir(orbID)); err != nil {
		return err
	}
	if err := d.paths.releaseSlot(orbID); err != nil {
		return err
	}
	d.gcBases(out)
	return nil
}

// deleteSnapshot 删快照盘与元数据，再回收基础盘。幂等。
func (d *FirecrackerDriver) deleteSnapshot(id string, out io.Writer) error {
	d.launchMu.Lock()
	defer d.launchMu.Unlock()
	for _, p := range []string{d.paths.snapMeta(id), d.paths.snapDisk(id), d.paths.snapDisk(id) + ".tmp"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	d.gcBases(out)
	return nil
}

// gcBases 删掉没有 VM / 快照引用、也不是当前 rootfs 的基础盘（调用方持有 launchMu）；失败只记进输出。
func (d *FirecrackerDriver) gcBases(out io.Writer) {
	refs, err := d.paths.baseRefs()
	if err != nil {
		fmt.Fprintf(out, "oryn: 回收基础盘: %v\n", err)
		return
	}
	if sum, err := d.digests.sum(d.Cfg.Rootfs); err == nil {
		refs[sum[:16]] = true
	}
	ents, err := os.ReadDir(d.paths.basesDir())
	if err != nil {
		return
	}
	for _, e := range ents {
		id := e.Name()
		if filepath.Ext(id) != ".ext4" || refs[id[:len(id)-len(".ext4")]] {
			continue
		}
		if err := os.Remove(filepath.Join(d.paths.basesDir(), id)); err != nil {
			fmt.Fprintf(out, "oryn: 删除基础盘 %s: %v\n", id, err)
		}
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
