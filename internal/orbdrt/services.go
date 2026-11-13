package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/portalhost"
)

// 受管服务监督器（M4 计划 2.2–2.6）：合并文件服务与动态服务、分配端口、拉起并监督进程组、就绪探测、
// 退避重启、持久化（$HOME/.oryn/services/state.json）与 services.state 上报。
//
// 并发约定：opMu 串行化 Ensure / Start / Stop / Restart / StopAll（这些操作可能等待进程退出）；
// mu 保护服务表与运行状态，持有期间不做阻塞操作。每个应运行的服务有一个 runLoop goroutine（svcRun），
// 它只在自己仍是 svc.run 时改写状态；停止方先把 svc.run 置空再请求停止并等待 done。

const (
	serviceStopGrace            = 10 * time.Second
	serviceMinLife              = 60 * time.Second
	serviceMaxShort             = 5
	serviceProbeTimeout         = 2 * time.Second
	defaultServiceProbeInterval = 500 * time.Millisecond
	// serviceUnhealthyAfter 是标 unhealthy 前的探测间隔倍数（默认 500ms × 120 = 60s）；
	// 之后探测间隔放宽为 serviceSlowProbe 倍（默认 5s）。
	serviceUnhealthyAfter = 120
	serviceSlowProbe      = 10
	// serviceSettleTimeout 是 start / restart / ensure 等待状态离开 starting 的上限。
	serviceSettleTimeout = 60 * time.Second
	servicePortMin       = 20000
	servicePortMax       = 29999
	serviceStateVersion  = 1
)

// serviceLogMax 是单个服务日志的轮转阈值（超过即改名为 `.log.1`，只留一份）。
var serviceLogMax int64 = 10 << 20

var serviceBackoffSteps = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

// defaultServiceBackoff 返回第 n 次（从 1 起）连续短命退出后的重启等待。
func defaultServiceBackoff(n int) time.Duration {
	n = min(max(n, 1), len(serviceBackoffSteps))
	return serviceBackoffSteps[n-1]
}

// svcError 是服务操作的错误；code 与 orbd.sock 的错误码一致（not_ready 在本机 API 上映射为 services_not_ready）。
type svcError struct{ code, msg string }

func (e *svcError) Error() string { return e.code + ": " + e.msg }

func svcErrf(code, format string, args ...any) *svcError {
	return &svcError{code: code, msg: fmt.Sprintf(format, args...)}
}

var errServicesNotReady = svcErrf(orbsock.CodeNotReady, "orbd has not received setup parameters from the control plane yet")

// dynamicDef 是持久化的动态服务定义。
type dynamicDef struct {
	Command  string                 `json:"command"`
	Cwd      string                 `json:"cwd,omitempty"`
	Port     int                    `json:"port,omitempty"`
	Env      map[string]string      `json:"env,omitempty"`
	Health   string                 `json:"health,omitempty"`
	Links    []apigen.OrbPortalLink `json:"links"`
	ThreadID string                 `json:"threadId,omitempty"`
}

func (d dynamicDef) spec(name string) serviceSpec {
	links := slices.Clone(d.Links)
	if links == nil {
		links = []apigen.OrbPortalLink{}
	}
	return serviceSpec{
		Name: name, Source: apigen.OrbServiceSourceDynamic, Command: d.Command, Cwd: d.Cwd, Health: d.Health,
		ThreadID: d.ThreadID, Port: d.Port, PortExplicit: d.Port != 0, Env: maps.Clone(d.Env), Links: links,
	}
}

// svcState 是 state.json 的内容。
type svcState struct {
	Version int                   `json:"version"`
	Ports   map[string]int        `json:"ports"`
	Dynamic map[string]dynamicDef `json:"dynamic"`
	Stopped []string              `json:"stopped"`
}

// svc 是服务表中的一项。
type svc struct {
	spec    serviceSpec
	invalid string // 定义 / 端口错误：以 failed 列出且不启动

	status    apigen.OrbServiceStatus
	pid       int
	restarts  int
	exitCode  *int
	errMsg    string
	startedAt int64
	threadID  string // 当前（最近一次）进程拿到的 ORYN_THREAD_ID
	publicURL string // 当前（最近一次）进程拿到的 PUBLIC_URL
	run       *svcRun
}

// svcRun 是一个服务的一段监督生命周期（进程 + 退避重启），直到被停止或判定 failed。
type svcRun struct {
	// name 是创建时的服务名快照：runLoop 只用它，不在 mu 之外读会被 reload 整体替换的 sv.spec。
	name  string
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
	grace time.Duration // requestStop 写入，close(stop) 之后可读

	// 以下由 mu 保护：本段生命周期最近一次拉起进程时算出的环境（refreshPublicURLs 据此判断是否需要重启）。
	envSet      bool   // 已为进程算过环境
	url         string // 进程拿到的 PUBLIC_URL
	refsMissing bool   // env 里有 ${services.X.publicURL} 引用被替换成了空串
}

func newRun(name string) *svcRun {
	return &svcRun{name: name, stop: make(chan struct{}), done: make(chan struct{})}
}

// requestStop 请求停止：grace > 0 先对进程组 SIGTERM，超时再 SIGKILL；grace = 0 直接 SIGKILL。
func (run *svcRun) requestStop(grace time.Duration) {
	run.once.Do(func() {
		run.grace = grace
		close(run.stop)
	})
}

type serviceSupervisor struct {
	r          *Runtime
	statePath  string
	statusPath string
	logDir     string
	egressPort int
	backoff    func(int) time.Duration
	probeEvery time.Duration
	onStatus   func(apigen.OrbService)
	// hookRunExited 仅供测试：runLoop 在进程自然退出、决定是否重启之前调用（在此替换定义以固定交错）。
	hookRunExited func(sv *svc)

	opMu sync.Mutex

	mu          sync.Mutex
	st          svcState
	svcs        map[string]*svc
	order       []string
	fileEntries []fileEntry // 最近一次成功解析的文件服务
	fileErr     string
	loaded      bool // 本进程已至少合并过一次（此前不上报，免得控制面把镜像与分享清空）
	closed      bool
	urlTemplate string
	primary     string
	changed     chan struct{} // 任何状态变化时关闭并替换（WaitSettled 用）

	dirty chan struct{} // 容量 1：唤醒上报 goroutine
	// emitMu 让 OnServiceStatus 回调按状态修改的顺序发生（见 unlockAndEmit）。
	emitMu sync.Mutex

	logsMu sync.Mutex
	logs   map[string]*serviceLog

	lmu       sync.Mutex
	listeners map[int]func(name, line string)
	nextL     int
}

func newServiceSupervisor(r *Runtime) *serviceSupervisor {
	s := &serviceSupervisor{
		r:          r,
		statePath:  filepath.Join(r.home, ".oryn", "services", "state.json"),
		statusPath: filepath.Join(r.home, ".oryn", "services-status.json"),
		logDir:     filepath.Join(r.home, ".cache", "oryn", "logs", "services"),
		backoff:    r.cfg.ServiceBackoff,
		probeEvery: r.cfg.ServiceProbeInterval,
		onStatus:   r.cfg.OnServiceStatus,
		svcs:       map[string]*svc{},
		changed:    make(chan struct{}),
		dirty:      make(chan struct{}, 1),
		logs:       map[string]*serviceLog{},
		listeners:  map[int]func(string, string){},
	}
	if s.backoff == nil {
		s.backoff = defaultServiceBackoff
	}
	if s.probeEvery <= 0 {
		s.probeEvery = defaultServiceProbeInterval
	}
	if tcp, ok := r.egressLn.Addr().(*net.TCPAddr); ok {
		s.egressPort = tcp.Port
	}
	s.loadState()
	return s
}

// ---- 持久化 ----

func (s *serviceSupervisor) loadState() {
	s.st = svcState{Version: serviceStateVersion, Ports: map[string]int{}, Dynamic: map[string]dynamicDef{}}
	b, err := os.ReadFile(s.statePath)
	if err != nil {
		return
	}
	var st svcState
	if err := json.Unmarshal(b, &st); err != nil || st.Version != serviceStateVersion {
		s.r.log.Warn("ignoring unreadable services state", "path", s.statePath, "err", err)
		return
	}
	if st.Ports != nil {
		s.st.Ports = st.Ports
	}
	if st.Dynamic != nil {
		s.st.Dynamic = st.Dynamic
	}
	s.st.Stopped = st.Stopped
}

func (s *serviceSupervisor) saveLocked() {
	slices.Sort(s.st.Stopped)
	s.st.Stopped = slices.Compact(s.st.Stopped)
	b, _ := json.MarshalIndent(s.st, "", "  ")
	if err := writeFileAtomic(s.statePath, append(b, '\n'), 0o600); err != nil {
		s.r.log.Warn("write services state", "err", err)
	}
}

func (s *serviceSupervisor) isStoppedLocked(name string) bool {
	return slices.Contains(s.st.Stopped, name)
}

// unstop 把名字移出 stopped（Start / Restart）。
func (s *serviceSupervisor) unstop(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.Index(s.st.Stopped, name); i >= 0 {
		s.st.Stopped = slices.Delete(s.st.Stopped, i, i+1)
		s.saveLocked()
	}
}

// ---- 状态快照与通知 ----

func (s *serviceSupervisor) bumpLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *serviceSupervisor) markDirty() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

// unlockAndEmit 释放 mu 并按变化发生的顺序交出快照：emitMu 在释放 mu 之前取得，
// 因此不同 goroutine 的回调顺序与它们修改状态的顺序一致。调用方持有 mu。
func (s *serviceSupervisor) unlockAndEmit(snaps ...apigen.OrbService) {
	s.emitMu.Lock()
	s.mu.Unlock()
	defer s.emitMu.Unlock()
	if s.onStatus != nil {
		for _, o := range snaps {
			s.onStatus(o)
		}
	}
	s.markDirty()
}

// update 在 sv 仍由 run 驱动时（run 为 nil 则无条件）用 f 修改其状态；f 返回 false 表示无变化。
func (s *serviceSupervisor) update(sv *svc, run *svcRun, f func(*svc) bool) (apigen.OrbService, bool) {
	s.mu.Lock()
	if run != nil && sv.run != run {
		s.mu.Unlock()
		return apigen.OrbService{}, false
	}
	if !f(sv) {
		s.mu.Unlock()
		return apigen.OrbService{}, false
	}
	snap := s.snapshotLocked(sv)
	s.bumpLocked()
	s.unlockAndEmit(snap)
	return snap, true
}

func (s *serviceSupervisor) snapshotLocked(sv *svc) apigen.OrbService {
	sp := sv.spec
	links := slices.Clone(sp.Links)
	if links == nil {
		links = []apigen.OrbPortalLink{}
	}
	o := apigen.OrbService{
		Name: sp.Name, Source: sp.Source, Command: sp.Command, Links: links,
		Status: sv.status, Restarts: sv.restarts, ExitCode: sv.exitCode,
	}
	if sv.invalid != "" {
		o.Status = apigen.OrbServiceStatusFailed
		o.Error = new(sv.invalid)
	} else if sv.errMsg != "" {
		o.Error = new(sv.errMsg)
	}
	if sp.Cwd != "" {
		o.Cwd = new(sp.Cwd)
	}
	if sp.Health != "" {
		o.Health = new(sp.Health)
	}
	if sp.Port > 0 {
		o.Port = new(sp.Port)
	}
	if sv.pid > 0 {
		o.Pid = new(sv.pid)
	}
	if sv.startedAt > 0 {
		o.StartedAt = new(sv.startedAt)
	}
	if sv.threadID != "" {
		o.ThreadID = new(sv.threadID)
	}
	if sv.publicURL != "" {
		o.PublicURL = new(sv.publicURL)
	}
	return o
}

// List 返回全部服务（文件服务按文件顺序，其后动态服务按名字）。
func (s *serviceSupervisor) List() []apigen.OrbService {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]apigen.OrbService, 0, len(s.order))
	for _, name := range s.order {
		if sv := s.svcs[name]; sv != nil {
			out = append(out, s.snapshotLocked(sv))
		}
	}
	return out
}

func (s *serviceSupervisor) snapshot(name string) apigen.OrbService {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sv := s.svcs[name]; sv != nil {
		return s.snapshotLocked(sv)
	}
	return apigen.OrbService{Name: name, Status: apigen.OrbServiceStatusStopped, Links: []apigen.OrbPortalLink{}}
}

func (s *serviceSupervisor) fileError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fileErr
}

// WaitSettled 等服务状态离开 starting（或 ctx 结束），返回当时的快照。
func (s *serviceSupervisor) WaitSettled(ctx context.Context, name string) apigen.OrbService {
	for {
		s.mu.Lock()
		ch := s.changed
		sv := s.svcs[name]
		var snap apigen.OrbService
		if sv != nil {
			snap = s.snapshotLocked(sv)
		}
		s.mu.Unlock()
		if sv == nil {
			return s.snapshot(name)
		}
		if snap.Status != apigen.OrbServiceStatusStarting {
			return snap
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return snap
		}
	}
}

// Exposed 报告端口是否属于某个有 portal 的（定义有效的）服务；egress 端口永远不算。
func (s *serviceSupervisor) Exposed(port int) bool {
	if port <= 0 || port == s.egressPort {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sv := range s.svcs {
		if sv.invalid == "" && len(sv.spec.Links) > 0 && sv.spec.Port == port {
			return true
		}
	}
	return false
}

// configure 记下 portal.configure 给的地址模板与主线程，并在后台补齐「进程拉起时还算不出 PUBLIC_URL /
// 被引用服务的 URL、现在能算出来」的服务（重启它们；见 refreshPublicURLs）。portal.configure 可能晚于
// setup / resume 结束时的 Ensure 与 agent.start 到达，不能只靠 agent.start 的那次检查。
func (s *serviceSupervisor) configure(tmpl, primary string) {
	s.mu.Lock()
	s.urlTemplate, s.primary = tmpl, primary
	s.mu.Unlock()
	s.markDirty()
	go s.refreshPublicURLs()
}

// reportLoop 是上报 goroutine：每次被唤醒时取最新快照，原子写 services-status.json 并发 services.state。
// 本进程还没合并过服务定义时不上报（orbd 新进程在 resume.run 之前的空列表会让控制面清掉镜像与分享）。
func (s *serviceSupervisor) reportLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.dirty:
		}
		s.mu.Lock()
		loaded := s.loaded
		s.mu.Unlock()
		if !loaded {
			continue
		}
		params := apigen.LinkCtlServicesStateParams{Services: s.List()}
		b, _ := json.MarshalIndent(params, "", "  ")
		if err := writeFileAtomic(s.statusPath, append(b, '\n'), 0o644); err != nil {
			s.r.log.Warn("write services status", "err", err)
		}
		s.r.notify(string(apigen.MethodServicesState), params)
	}
}

// ---- 日志 ----

func (s *serviceSupervisor) logFor(name string) *serviceLog {
	s.logsMu.Lock()
	defer s.logsMu.Unlock()
	l := s.logs[name]
	if l == nil {
		l = &serviceLog{path: filepath.Join(s.logDir, name+".log")}
		s.logs[name] = l
	}
	return l
}

func (s *serviceSupervisor) closeLogs() {
	s.logsMu.Lock()
	defer s.logsMu.Unlock()
	for _, l := range s.logs {
		l.close()
	}
}

// sysLine 往服务日志写一行 `[orbd] …` 并交给订阅者（orbd.sock 调用方的实时输出）。
func (s *serviceSupervisor) sysLine(name, msg string) {
	line := "[orbd] " + msg + "\n"
	_, _ = s.logFor(name).Write([]byte(line))
	s.lmu.Lock()
	ls := slices.Collect(maps.Values(s.listeners))
	s.lmu.Unlock()
	for _, f := range ls {
		f(name, line)
	}
}

// subscribe 订阅 `[orbd]` 状态行，返回取消函数。
func (s *serviceSupervisor) subscribe(f func(name, line string)) func() {
	s.lmu.Lock()
	id := s.nextL
	s.nextL++
	s.listeners[id] = f
	s.lmu.Unlock()
	return func() {
		s.lmu.Lock()
		delete(s.listeners, id)
		s.lmu.Unlock()
	}
}

// serviceLog 是追加写的服务日志；超过 serviceLogMax 时轮转为 `.log.1`。
type serviceLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func (l *serviceLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return 0, err
		}
		l.f, l.size = f, st.Size()
	}
	if l.size > 0 && l.size+int64(len(p)) > serviceLogMax {
		_ = l.f.Close()
		l.f = nil
		_ = os.Rename(l.path, l.path+".1")
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return 0, err
		}
		l.f, l.size = f, 0
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *serviceLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// ---- 合并 ----

// reload 重读服务文件，与动态服务合并、分配端口、对比新旧定义：删除的停掉，定义变化的重启，新增的启动；
// startAll 时再拉起「应运行而未运行」的服务（含连续失败后 failed 的）。返回本次启动的服务名。调用方持有 opMu。
func (s *serviceSupervisor) reload(startAll bool) map[string]bool {
	entries, found, ferr := loadServiceFile(s.r.currentWorkdir(), func(msg string) { s.r.log.Warn(msg) })

	s.mu.Lock()
	switch {
	case ferr != nil:
		s.fileErr = ferr.Error()
		s.r.log.Warn("services file error; keeping current file services", "err", ferr)
		entries = s.fileEntries
	case !found:
		s.fileErr, s.fileEntries, entries = "", nil, nil
	default:
		s.fileErr, s.fileEntries = "", entries
	}
	s.loaded = true

	// 同名时文件服务胜出：文件新增了与动态服务同名的条目 → 丢弃动态定义。
	stateDirty := false
	desired := make([]fileEntry, 0, len(entries)+len(s.st.Dynamic))
	fileNames := map[string]bool{}
	for _, e := range entries {
		if fileNames[e.spec.Name] {
			s.r.log.Warn("duplicate service in services file ignored", "service", e.spec.Name)
			continue
		}
		fileNames[e.spec.Name] = true
		desired = append(desired, e)
	}
	for _, name := range slices.Sorted(maps.Keys(s.st.Dynamic)) {
		if fileNames[name] {
			s.r.log.Info("services file now declares a dynamic service's name; dropping the dynamic definition", "service", name)
			delete(s.st.Dynamic, name)
			stateDirty = true
			continue
		}
		e := fileEntry{spec: s.st.Dynamic[name].spec(name)}
		e.err = validateSpec(&e.spec)
		desired = append(desired, e)
	}
	for i := maxServices; i < len(desired); i++ {
		if desired[i].err == "" {
			desired[i].err = fmt.Sprintf("too many services (max %d)", maxServices)
		}
	}
	stateDirty = s.assignPortsLocked(desired) || stateDirty
	checkPublicURLRefs(desired)

	var (
		toStop  []*svcRun
		toStart []*svc
		emits   []apigen.OrbService
		names   []string
	)
	keep := map[string]bool{}
	for _, d := range desired {
		name := d.spec.Name
		keep[name] = true
		names = append(names, name)
		stopped := s.isStoppedLocked(name)
		sv := s.svcs[name]
		switch {
		case sv == nil:
			sv = &svc{spec: d.spec, invalid: d.err, status: apigen.OrbServiceStatusStopped}
			s.svcs[name] = sv
		case !sv.spec.sameDefinition(d.spec) || sv.invalid != d.err:
			if sv.run != nil {
				toStop = append(toStop, sv.run)
				sv.run = nil
			}
			sv.spec, sv.invalid = d.spec, d.err
			sv.status, sv.pid, sv.restarts, sv.exitCode, sv.errMsg = apigen.OrbServiceStatusStopped, 0, 0, nil, ""
		default:
			sv.spec.ThreadID = d.spec.ThreadID
			if !(startAll && sv.run == nil) {
				continue
			}
		}
		if d.err == "" && !stopped {
			toStart = append(toStart, sv)
		} else {
			emits = append(emits, s.snapshotLocked(sv))
		}
	}
	for name, sv := range s.svcs {
		if keep[name] {
			continue
		}
		if sv.run != nil {
			toStop = append(toStop, sv.run)
			sv.run = nil
		}
		delete(s.svcs, name)
	}
	s.order = names
	if stateDirty {
		s.saveLocked()
	}
	s.bumpLocked()
	s.unlockAndEmit(emits...)
	stopRuns(toStop, serviceStopGrace)
	started := map[string]bool{}
	for _, sv := range toStart {
		if s.launch(sv) {
			started[sv.spec.Name] = true
		}
	}
	s.markDirty()
	return started
}

// assignPortsLocked 为定义有效的服务定端口：显式端口原样使用；否则沿用 state.json 的分配，再否则从 20000 起
// 找第一个空闲端口并记下。显式端口重复 → 后者 port_conflict；等于 egress 端口 → forbidden_port。
// 自动端口与其它服务冲突时重新分配。返回 state 是否有变化。
func (s *serviceSupervisor) assignPortsLocked(desired []fileEntry) bool {
	explicit := map[int]bool{}
	for _, d := range desired {
		if d.err == "" && d.spec.PortExplicit {
			explicit[d.spec.Port] = true
		}
	}
	used := map[int]bool{}
	changed := false
	for i := range desired {
		d := &desired[i]
		if d.err != "" {
			continue
		}
		if d.spec.PortExplicit {
			switch p := d.spec.Port; {
			case p == s.egressPort:
				d.err = "forbidden_port"
			case used[p]:
				d.err = "port_conflict"
			default:
				used[p] = true
			}
			continue
		}
		p := s.st.Ports[d.spec.Name]
		if p == 0 || used[p] || explicit[p] || p == s.egressPort {
			if p = s.allocPortLocked(d.spec.Name, used, explicit); p == 0 {
				d.err = fmt.Sprintf("no free port in %d-%d", servicePortMin, servicePortMax)
				continue
			}
			s.st.Ports[d.spec.Name] = p
			changed = true
		}
		d.spec.Port = p
		used[p] = true
	}
	return changed
}

func (s *serviceSupervisor) allocPortLocked(name string, used, explicit map[int]bool) int {
	taken := map[int]bool{}
	for n, p := range s.st.Ports {
		if n != name {
			taken[p] = true
		}
	}
	for p := servicePortMin; p <= servicePortMax; p++ {
		if used[p] || explicit[p] || taken[p] || p == s.egressPort {
			continue
		}
		if ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p))); err == nil {
			_ = ln.Close()
			return p
		}
	}
	return 0
}

// checkPublicURLRefs 把 env 引用了不存在 / 无效 / 无 portal 的服务的定义标为无效（传递到不动点）。
func checkPublicURLRefs(desired []fileEntry) {
	for changed := true; changed; {
		changed = false
		byName := map[string]*fileEntry{}
		for i := range desired {
			byName[desired[i].spec.Name] = &desired[i]
		}
		for i := range desired {
			d := &desired[i]
			if d.err != "" {
				continue
			}
			for _, ref := range publicURLRefs(d.spec.Env) {
				if t := byName[ref]; t == nil || t.err != "" || len(t.spec.Links) == 0 {
					d.err = fmt.Sprintf("env references ${services.%s.publicURL} but %s is not a valid service with a portal", ref, ref)
					changed = true
					break
				}
			}
		}
	}
}

// ---- 操作 ----

// Ensure 重读文件、合并，并启动应运行而未运行的服务。
func (s *serviceSupervisor) Ensure() (list []apigen.OrbService, fileError string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.reload(true)
	return s.List(), s.fileError()
}

// Start 启动已声明的服务（Command 为 nil），或建立 / 替换动态服务。
func (s *serviceSupervisor) Start(p orbsock.ServiceStartParams) (apigen.OrbService, error) {
	if !s.r.setupParamsKnown() {
		return apigen.OrbService{}, errServicesNotReady
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !serviceNameRe.MatchString(p.Name) {
		return apigen.OrbService{}, svcErrf(orbsock.CodeInvalidService, "invalid service name %q (want ^[a-z0-9][a-z0-9-]{0,31}$)", p.Name)
	}
	if p.Command == nil {
		if p.Cwd != "" || p.Port != 0 || len(p.Env) > 0 || p.Health != "" || p.Portal {
			return apigen.OrbService{}, svcErrf(orbsock.CodeInvalidService, "cwd / port / env / health / portal can only be given together with command")
		}
		s.unstop(p.Name)
		s.reload(false)
		s.mu.Lock()
		sv := s.svcs[p.Name]
		s.mu.Unlock()
		if sv == nil {
			return apigen.OrbService{}, svcErrf(orbsock.CodeServiceNotFound, "no service named %q (declare it in .oryn/services.yaml or give --command)", p.Name)
		}
		s.launch(sv)
		return s.snapshot(p.Name), nil
	}

	s.reload(false)
	s.mu.Lock()
	if sv := s.svcs[p.Name]; sv != nil && sv.spec.Source == apigen.OrbServiceSourceFile {
		s.mu.Unlock()
		return apigen.OrbService{}, svcErrf(orbsock.CodeNameConflict, "%q is declared in services.yaml; use another name or start it without --command", p.Name)
	}
	if s.svcs[p.Name] == nil && len(s.svcs) >= maxServices {
		s.mu.Unlock()
		return apigen.OrbService{}, svcErrf(orbsock.CodeInvalidService, "too many services (max %d)", maxServices)
	}
	def := dynamicDef{Command: *p.Command, Cwd: p.Cwd, Port: p.Port, Env: p.Env, Health: p.Health, Links: []apigen.OrbPortalLink{}}
	if p.Portal {
		def.Links = []apigen.OrbPortalLink{{Title: p.Name, Path: "/"}}
	}
	if p.ThreadID != "" {
		if _, ok := s.r.reg.list()[p.ThreadID]; ok {
			def.ThreadID = p.ThreadID
		}
	}
	sp := def.spec(p.Name)
	if msg := validateSpec(&sp); msg != "" {
		s.mu.Unlock()
		return apigen.OrbService{}, svcErrf(orbsock.CodeInvalidService, "%s", msg)
	}
	def.Cwd = sp.Cwd
	s.st.Dynamic[p.Name] = def
	if i := slices.Index(s.st.Stopped, p.Name); i >= 0 {
		s.st.Stopped = slices.Delete(s.st.Stopped, i, i+1)
	}
	s.saveLocked()
	s.mu.Unlock()

	s.reload(false)
	s.mu.Lock()
	sv := s.svcs[p.Name]
	s.mu.Unlock()
	if sv != nil {
		s.launch(sv)
	}
	return s.snapshot(p.Name), nil
}

// Stop 停止服务并记入 stopped（之后的 Ensure 不再拉起）；remove 时连同动态服务的定义与端口分配一起删除。
func (s *serviceSupervisor) Stop(name string, remove bool) (apigen.OrbService, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	sv := s.svcs[name]
	if sv == nil {
		s.mu.Unlock()
		return apigen.OrbService{}, svcErrf(orbsock.CodeServiceNotFound, "no service named %q", name)
	}
	if remove && sv.spec.Source == apigen.OrbServiceSourceFile {
		s.mu.Unlock()
		return apigen.OrbService{}, svcErrf(orbsock.CodeInvalidService, "%q is declared in services.yaml and cannot be removed; delete it from the file instead", name)
	}
	run := sv.run
	sv.run = nil
	if remove {
		delete(s.st.Dynamic, name)
		delete(s.st.Ports, name)
		if i := slices.Index(s.st.Stopped, name); i >= 0 {
			s.st.Stopped = slices.Delete(s.st.Stopped, i, i+1)
		}
		delete(s.svcs, name)
		s.order = slices.DeleteFunc(s.order, func(n string) bool { return n == name })
	} else if !s.isStoppedLocked(name) {
		s.st.Stopped = append(s.st.Stopped, name)
	}
	s.saveLocked()
	s.mu.Unlock()
	if run != nil {
		stopRuns([]*svcRun{run}, serviceStopGrace)
	}
	snap, _ := s.update(sv, nil, func(sv *svc) bool {
		sv.pid = 0
		if sv.invalid == "" {
			sv.status = apigen.OrbServiceStatusStopped
		}
		return true
	})
	return snap, nil
}

// Restart 先重读文件，再重启服务（移出 stopped）。
func (s *serviceSupervisor) Restart(name string) (apigen.OrbService, error) {
	if !s.r.setupParamsKnown() {
		return apigen.OrbService{}, errServicesNotReady
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.unstop(name)
	started := s.reload(false)
	s.mu.Lock()
	sv := s.svcs[name]
	if sv == nil {
		s.mu.Unlock()
		return apigen.OrbService{}, svcErrf(orbsock.CodeServiceNotFound, "no service named %q", name)
	}
	if started[name] {
		s.mu.Unlock()
		return s.snapshot(name), nil
	}
	run := sv.run
	sv.run = nil
	s.mu.Unlock()
	if run != nil {
		s.sysLine(name, "restart requested")
		stopRuns([]*svcRun{run}, serviceStopGrace)
	}
	if !s.launch(sv) {
		s.update(sv, nil, func(sv *svc) bool { sv.pid = 0; sv.status = apigen.OrbServiceStatusStopped; return true })
	}
	return s.snapshot(name), nil
}

// StopAll 停掉全部进程（orbd 关闭 / shutdown），不改期望状态：之后的 Ensure 会重新拉起。
func (s *serviceSupervisor) StopAll(grace time.Duration) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	var svcs []*svc
	var runs []*svcRun
	for _, sv := range s.svcs {
		if sv.run != nil {
			svcs, runs = append(svcs, sv), append(runs, sv.run)
			sv.run = nil
		}
	}
	s.mu.Unlock()
	stopRuns(runs, grace)
	for _, sv := range svcs {
		s.update(sv, nil, func(sv *svc) bool { sv.pid = 0; sv.status = apigen.OrbServiceStatusStopped; return true })
	}
}

// killAll 在运行时结束时直接 SIGKILL 全部进程组，此后不再拉起任何服务。
func (s *serviceSupervisor) killAll() {
	s.mu.Lock()
	s.closed = true
	var runs []*svcRun
	for _, sv := range s.svcs {
		if sv.run != nil {
			runs = append(runs, sv.run)
			sv.run = nil
		}
	}
	s.mu.Unlock()
	stopRuns(runs, 0)
	s.closeLogs()
}

// refreshPublicURLs 重启这样的服务：进程拉起时算出的环境缺了 PUBLIC_URL（有 portal）或某个
// ${services.X.publicURL} 引用被替换成空串，而现在都能算出来了（首个线程登记、或 portal.configure
// 晚于服务启动到达；计划「假设与预案」）。还没为进程算过环境的 run 不动：它拉起时自然用上最新配置。
func (s *serviceSupervisor) refreshPublicURLs() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	var svcs []*svc
	var runs []*svcRun
	for _, sv := range s.svcs {
		run := sv.run
		if run == nil || !run.envSet {
			continue
		}
		needURL := len(sv.spec.Links) > 0 && run.url == "" && s.urlForLocked(sv.spec, s.threadLocked(sv.spec)) != ""
		needRefs := run.refsMissing && !s.refsMissingLocked(sv.spec)
		if !needURL && !needRefs {
			continue
		}
		svcs, runs = append(svcs, sv), append(runs, run)
		sv.run = nil
	}
	s.mu.Unlock()
	if len(svcs) == 0 {
		return
	}
	for _, run := range runs {
		s.sysLine(run.name, "restarting to pick up PUBLIC_URL")
	}
	stopRuns(runs, serviceStopGrace)
	for _, sv := range svcs {
		s.launch(sv)
	}
}

func stopRuns(runs []*svcRun, grace time.Duration) {
	for _, run := range runs {
		run.requestStop(grace)
	}
	for _, run := range runs {
		<-run.done
	}
}

// ---- 进程监督 ----

// launch 为服务开一段新的监督生命周期；服务已在运行、定义无效、已不在表中或监督器已关闭时返回 false。
func (s *serviceSupervisor) launch(sv *svc) bool {
	s.mu.Lock()
	if s.closed || s.svcs[sv.spec.Name] != sv || sv.run != nil || sv.invalid != "" {
		s.mu.Unlock()
		return false
	}
	run := newRun(sv.spec.Name)
	sv.run = run
	sv.status, sv.pid, sv.exitCode, sv.errMsg = apigen.OrbServiceStatusStarting, 0, nil, ""
	snap := s.snapshotLocked(sv)
	s.bumpLocked()
	s.unlockAndEmit(snap)
	go s.runLoop(sv, run)
	return true
}

// runLoop 拉起进程、等它退出，期望仍为运行时按退避重启；连续 serviceMaxShort 次存活不足 serviceMinLife 即 failed。
func (s *serviceSupervisor) runLoop(sv *svc, run *svcRun) {
	defer close(run.done)
	short := 0
	for {
		code, lived, stopped := s.runOnce(sv, run)
		if stopped {
			return
		}
		if lived < serviceMinLife {
			short++
		} else {
			short = 0
		}
		if s.hookRunExited != nil {
			s.hookRunExited(sv)
		}
		name := run.name
		if short >= serviceMaxShort {
			msg := fmt.Sprintf("exited %d times in a row within %s (last exit code %d)", short, serviceMinLife, code)
			s.sysLine(name, "giving up: "+msg)
			s.update(sv, run, func(sv *svc) bool {
				sv.status, sv.errMsg, sv.pid, sv.run = apigen.OrbServiceStatusFailed, msg, 0, nil
				return true
			})
			return
		}
		delay := s.backoff(max(short, 1))
		if _, ok := s.update(sv, run, func(sv *svc) bool {
			sv.status, sv.pid = apigen.OrbServiceStatusBackoff, 0
			sv.restarts++
			return true
		}); !ok {
			return
		}
		s.sysLine(name, fmt.Sprintf("restarting in %s", delay))
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-run.stop:
			t.Stop()
			return
		}
	}
}

// runOnce 运行一次进程直到退出或被停止；stopped 表示因停止请求（或已不再是当前 run）而结束。
func (s *serviceSupervisor) runOnce(sv *svc, run *svcRun) (code int, lived time.Duration, stopped bool) {
	s.mu.Lock()
	if sv.run != run {
		s.mu.Unlock()
		return 0, 0, true
	}
	spec := sv.spec
	env, thread, url, refsMissing := s.envLocked(spec)
	run.envSet, run.url, run.refsMissing = true, url, refsMissing
	s.mu.Unlock()
	name := spec.Name

	s.sysLine(name, fmt.Sprintf("starting %s (port %d)", name, spec.Port))
	lg := s.logFor(name)
	cmd := exec.Command("bash", "-lc", spec.Command)
	cmd.Dir = filepath.Join(s.r.currentWorkdir(), spec.Cwd)
	cmd.Env = envList(env)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = lg, lg
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	if err := cmd.Start(); err != nil {
		s.sysLine(name, "start failed: "+err.Error())
		s.update(sv, run, func(sv *svc) bool { sv.exitCode, sv.errMsg = new(-1), err.Error(); return true })
		select {
		case <-run.stop:
			return -1, 0, true
		default:
			return -1, 0, false
		}
	}
	pid := cmd.Process.Pid
	needProbe := spec.Health != "" || len(spec.Links) > 0 || spec.PortExplicit
	s.update(sv, run, func(sv *svc) bool {
		sv.pid, sv.startedAt, sv.threadID, sv.publicURL, sv.errMsg = pid, start.UnixMilli(), thread, url, ""
		if needProbe {
			sv.status = apigen.OrbServiceStatusStarting
		} else {
			sv.status = apigen.OrbServiceStatusRunning
		}
		return true
	})

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	pctx, pcancel := context.WithCancel(context.Background())
	if needProbe {
		go s.probe(pctx, sv, run, pid, spec)
	}
	select {
	case <-exited:
	case <-run.stop:
		stopped = true
		killGroup(pid, run.grace, exited)
	}
	pcancel()
	// 进程组即服务：组长退出后，组里残留的后代一并结束（否则会占着端口让下一次启动失败）。
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	code, how := -1, ""
	if ps := cmd.ProcessState; ps != nil {
		code = ps.ExitCode()
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			how = " (signal: " + ws.Signal().String() + ")"
		}
	}
	s.sysLine(name, fmt.Sprintf("exited code=%d%s", code, how))
	if !stopped {
		s.update(sv, run, func(sv *svc) bool { sv.exitCode = new(code); return true })
	}
	return code, time.Since(start), stopped
}

// killGroup 结束进程组：grace > 0 先 SIGTERM，超时再 SIGKILL；返回时组长已被回收。
func killGroup(pid int, grace time.Duration, exited <-chan error) {
	if grace > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		t := time.NewTimer(grace)
		defer t.Stop()
		select {
		case <-exited:
			return
		case <-t.C:
		}
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-exited
}

// probe 做就绪探测：通过后置 running 并停止探测；serviceUnhealthyAfter 个间隔仍未通过则标 unhealthy 并放慢。
func (s *serviceSupervisor) probe(ctx context.Context, sv *svc, run *svcRun, pid int, spec serviceSpec) {
	interval := s.probeEvery
	deadline := time.Now().Add(interval * serviceUnhealthyAfter)
	unhealthy := false
	for {
		if probeOnce(ctx, spec) {
			if _, ok := s.update(sv, run, func(sv *svc) bool {
				if sv.pid != pid || (sv.status != apigen.OrbServiceStatusStarting && sv.status != apigen.OrbServiceStatusUnhealthy) {
					return false
				}
				sv.status = apigen.OrbServiceStatusRunning
				return true
			}); ok {
				s.sysLine(spec.Name, fmt.Sprintf("%s is ready on port %d", spec.Name, spec.Port))
			}
			return
		}
		if !unhealthy && time.Now().After(deadline) {
			unhealthy = true
			interval = s.probeEvery * serviceSlowProbe
			if _, ok := s.update(sv, run, func(sv *svc) bool {
				if sv.pid != pid || sv.status != apigen.OrbServiceStatusStarting {
					return false
				}
				sv.status = apigen.OrbServiceStatusUnhealthy
				return true
			}); ok {
				s.sysLine(spec.Name, fmt.Sprintf("%s is not ready after %s; still probing", spec.Name, s.probeEvery*serviceUnhealthyAfter))
			}
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

var probeClient = &http.Client{
	Transport:     &http.Transport{DisableKeepAlives: true, Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// probeOnce 探测一次：有 health 时 GET 该路径（2xx / 3xx 通过），否则 TCP 连通即通过；
// 先试 127.0.0.1，再试 [::1]（只听 IPv6 回环的 dev server）。
func probeOnce(ctx context.Context, spec serviceSpec) bool {
	ctx, cancel := context.WithTimeout(ctx, serviceProbeTimeout)
	defer cancel()
	port := strconv.Itoa(spec.Port)
	for _, host := range []string{"127.0.0.1", "::1"} {
		addr := net.JoinHostPort(host, port)
		if spec.Health == "" {
			var d net.Dialer
			if c, err := d.DialContext(ctx, "tcp", addr); err == nil {
				_ = c.Close()
				return true
			}
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+spec.Health, nil)
		if err != nil {
			return false
		}
		resp, err := probeClient.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 400
	}
	return false
}

// ---- 环境 ----

// threadLocked 返回服务所属线程：动态服务指定且已登记的线程 → portal.configure 的主线程 → 登记表里字典序最小的线程。
func (s *serviceSupervisor) threadLocked(spec serviceSpec) string {
	reg := s.r.reg.list()
	if spec.ThreadID != "" {
		if _, ok := reg[spec.ThreadID]; ok {
			return spec.ThreadID
		}
	}
	if s.primary != "" {
		return s.primary
	}
	if ids := slices.Sorted(maps.Keys(reg)); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// urlForLocked 返回服务的 PUBLIC_URL（不带结尾 `/`）；无 portal、控制面未配置 portal 或线程没有短码时为空。
func (s *serviceSupervisor) urlForLocked(spec serviceSpec, thread string) string {
	if len(spec.Links) == 0 || s.urlTemplate == "" || spec.Port <= 0 {
		return ""
	}
	code, ok := portalhost.Code(thread)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(portalhost.Expand(s.urlTemplate, code, spec.Port), "/")
}

// refURLLocked 返回被 ${services.<ref>.publicURL} 引用的服务当前的 PUBLIC_URL；算不出时为空。
func (s *serviceSupervisor) refURLLocked(ref string) string {
	if t := s.svcs[ref]; t != nil {
		return s.urlForLocked(t.spec, s.threadLocked(t.spec))
	}
	return ""
}

// refsMissingLocked 报告 spec 的 env 里是否有引用此刻算不出 URL。
func (s *serviceSupervisor) refsMissingLocked(spec serviceSpec) bool {
	for _, ref := range publicURLRefs(spec.Env) {
		if s.refURLLocked(ref) == "" {
			return true
		}
	}
	return false
}

// envLocked 返回服务进程的环境：baseEnv + setup 环境 + 服务 env（引用已替换）+ PORT / ORYN_THREAD_ID / PUBLIC_URL；
// refsMissing 表示有引用被替换成了空串。
func (s *serviceSupervisor) envLocked(spec serviceSpec) (env map[string]string, thread, url string, refsMissing bool) {
	env = s.r.baseEnv()
	maps.Copy(env, s.r.setupEnvSnapshot())
	for k, v := range spec.Env {
		env[k] = publicURLRefRe.ReplaceAllStringFunc(v, func(m string) string {
			u := s.refURLLocked(publicURLRefRe.FindStringSubmatch(m)[1])
			if u == "" {
				refsMissing = true
			}
			return u
		})
	}
	delete(env, "PUBLIC_URL")
	delete(env, "ORYN_THREAD_ID")
	env["PORT"] = strconv.Itoa(spec.Port)
	thread = s.threadLocked(spec)
	if thread != "" {
		env["ORYN_THREAD_ID"] = thread
	}
	if url = s.urlForLocked(spec, thread); url != "" {
		env["PUBLIC_URL"] = url
	}
	return env, thread, url, refsMissing
}

// isSvcError 报告 err 是否为服务操作错误。
func isSvcError(err error) (*svcError, bool) {
	var e *svcError
	ok := errors.As(err, &e)
	return e, ok
}
