package orbdrt

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/portalhost"
)

// box 的 app 运行时（M23 步骤 2.2）：release 上传（PUT /box/incoming/{releaseId}）、激活 / 回滚 / 重启 / 下线
// （/box/apps/{app}/…）与状态快照（GET /box/apps）。app 的服务是监督器的第三种来源（source = app），
// 定义由这里经 publish 交给监督器。
//
// 目录：~/apps/<app>/releases/<releaseId>/、~/apps/<app>/current（指向 releases/<id> 的相对符号链接）、
// ~/apps/<app>/data/；状态 ~/.oryn/box/apps.json（0600）；上传落点 ~/.oryn/box/incoming/<releaseId>.tar.gz（0700）。
//
// 锁的次序：boxManager.mu（串行化全部 app 操作）→ 监督器的 opMu → 监督器的 mu。监督器只读 publish 存下的快照。

const (
	boxStateVersion = 1
	// maxBoxArchiveBytes / maxBoxUnpackedBytes / maxBoxEntries 是 release 压缩包与解包后的上限。
	maxBoxArchiveBytes  = 512 << 20
	maxBoxUnpackedBytes = 4 << 30
	maxBoxEntries       = 500000
	// maxBoxJSONBody 是带 env 的请求体（activate、restart）上限：1 MiB 的密钥经 JSON 转义最坏约 6 倍。
	maxBoxJSONBody = 8 << 20
	// boxIncomingTTL：上传了却没等到激活的包，过这么久在下一次上传时删除。
	boxIncomingTTL = time.Hour
	// boxLogTail 是失败结果里各服务日志末尾的合计上限。
	boxLogTail = 16 << 10
	// maxBoxAppServices 是每个 app 的服务数上限；maxBoxReadyTimeout 是等就绪的上限（秒）。
	maxBoxAppServices  = 4
	maxBoxReadyTimeout = 600
	// boxDataDirEnv 的值在 publish 时从 `~/…` 展开成绝对路径。
	boxDataDirEnv = "ORYN_DEPLOYMENT_DATA_DIRECTORY"
)

var (
	boxReleaseRe = regexp.MustCompile(`^bxr_[0-9A-Z]{26}$`)
	boxServiceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,9}$`)
)

// boxApp 是一个 app 在 box 上的状态。没有 Trial 时 Live 是确认过就绪的 release；有 Trial 时 Live 是待确认的。
type boxApp struct {
	Port     int               `json:"port"`
	Live     string            `json:"live"`
	Releases []boxRelease      `json:"releases"` // 新到旧，含 live
	Env      map[string]string `json:"env"`
	Trial    *boxTrial         `json:"trial,omitempty"` // 非空 = 正在试运行一个还没确认就绪的 release
}

// boxTrial 是一次试运行：Prev 是试运行之前的整个 app（nil = 之前没有这个 app），其中的 Trial 恒为 nil。
type boxTrial struct {
	Release string  `json:"release"`
	Prev    *boxApp `json:"prev"`
}

type boxRelease struct {
	ID       string                 `json:"id"`
	Source   bool                   `json:"source"`
	Services []apigen.OrbBoxService `json:"services"`
}

type boxStateFile struct {
	Version int                `json:"version"`
	Apps    map[string]*boxApp `json:"apps"`
}

func (a *boxApp) release(id string) *boxRelease {
	if a == nil {
		return nil
	}
	for i := range a.Releases {
		if a.Releases[i].ID == id {
			return &a.Releases[i]
		}
	}
	return nil
}

// clone 深拷贝（不含 Trial）。
func (a *boxApp) clone() *boxApp {
	if a == nil {
		return nil
	}
	c := *a
	c.Env = maps.Clone(a.Env)
	c.Releases = make([]boxRelease, len(a.Releases))
	for i, rel := range a.Releases {
		rel.Services = slices.Clone(rel.Services)
		c.Releases[i] = rel
	}
	c.Trial = nil
	return &c
}

// boxFail 是 app 操作的业务失败（本机 API 以 200 + ok:false 回报）。
type boxFail struct{ code, msg string }

func (e *boxFail) Error() string { return e.code + ": " + e.msg }

func boxFailf(code, format string, args ...any) *boxFail {
	return &boxFail{code: code, msg: fmt.Sprintf(format, args...)}
}

type boxManager struct {
	r    *Runtime
	path string
	// 上限与轮询间隔（测试可调小）。
	maxArchive  int64
	maxUnpacked int64
	maxEntries  int
	readyPoll   time.Duration
	// hookExtract 仅供测试：activate 在解包之前调用（此时 activating 已经发布）。
	hookExtract func(app, release string)
	// hookTrial 仅供测试：switchApp 换过去之后、等就绪之前调用（不持有监督器的锁）。
	hookTrial func(app, release string)

	mu         sync.Mutex
	apps       map[string]*boxApp
	activating map[string]string // app → 正在处理的 activate / rollback 的 release id

	// status 是 GET /box/apps 的快照：只读它、不取 mu（activate 持有 mu 直到等就绪结束）。
	status atomic.Pointer[apigen.OrbBoxAppsState]
}

// newBoxManager 加载状态文件，把 orbd 在试运行中途退出时留下的 Trial 退回 Prev（不经 switchApp、不起服务：
// 此刻还没有进程在跑，服务由 resume 结尾的 Ensure 统一拉起），再 publish。
func newBoxManager(r *Runtime) *boxManager {
	b := &boxManager{
		r:           r,
		path:        filepath.Join(r.home, ".oryn", "box", "apps.json"),
		maxArchive:  maxBoxArchiveBytes,
		maxUnpacked: maxBoxUnpackedBytes,
		maxEntries:  maxBoxEntries,
		readyPoll:   250 * time.Millisecond,
		apps:        map[string]*boxApp{},
		activating:  map[string]string{},
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loadLocked()
	recovered := false
	for _, name := range slices.Sorted(maps.Keys(b.apps)) {
		a := b.apps[name]
		if a.Trial == nil {
			continue
		}
		r.log.Warn("box app was mid-activation when orbd stopped; restoring the previous release", "app", name, "release", a.Trial.Release)
		trial, prev := a.Trial.Release, a.Trial.Prev
		if err := b.restoreLocked(name, prev); err != nil {
			r.log.Warn("restore box app", "app", name, "err", err)
		}
		b.cleanupTrial(name, trial, prev)
		recovered = true
	}
	if recovered {
		if err := b.saveLocked(); err != nil {
			r.log.Warn("write box state", "err", err)
		}
	}
	b.publishLocked()
	return b
}

func (b *boxManager) loadLocked() {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			b.r.log.Warn("read box state", "path", b.path, "err", err)
		}
		return
	}
	var st boxStateFile
	if err := json.Unmarshal(data, &st); err != nil || st.Version != boxStateVersion {
		b.r.log.Warn("ignoring unreadable box state", "path", b.path, "err", err)
		return
	}
	for name, a := range st.Apps {
		if a == nil || !portalhost.ValidAppName(name) {
			continue
		}
		if a.Trial != nil && a.Trial.Prev != nil {
			a.Trial.Prev.Trial = nil
		}
		b.apps[name] = a
	}
}

func (b *boxManager) saveLocked() error {
	data, err := json.MarshalIndent(boxStateFile{Version: boxStateVersion, Apps: b.apps}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(b.path, append(data, '\n'), 0o600)
}

// ---- 路径 ----

func (b *boxManager) incomingDir() string { return filepath.Join(b.r.home, ".oryn", "box", "incoming") }
func (b *boxManager) appDir(app string) string {
	return filepath.Join(b.r.home, "apps", app)
}
func (b *boxManager) releaseDir(app, id string) string {
	return filepath.Join(b.appDir(app), "releases", id)
}
func (b *boxManager) currentLink(app string) string { return filepath.Join(b.appDir(app), "current") }

// linkCurrent 原子地把 current 指向 releases/<id>：在同一目录建临时符号链接再 rename。
func (b *boxManager) linkCurrent(app, id string) error {
	dir := b.appDir(app)
	tmp := filepath.Join(dir, ".current-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := os.Symlink(filepath.Join("releases", id), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "current")); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ---- 快照 ----

// publishLocked 把全部 live app 的服务定义与全部 app 的端口交给监督器，并重算状态快照。调用方持有 mu（或在构造中）。
func (b *boxManager) publishLocked() {
	var entries []fileEntry
	ports := map[int]bool{}
	for _, name := range slices.Sorted(maps.Keys(b.apps)) {
		a := b.apps[name]
		ports[a.Port] = true
		rel := a.release(a.Live)
		if rel == nil {
			continue
		}
		env := maps.Clone(a.Env)
		if v, ok := env[boxDataDirEnv]; ok && strings.HasPrefix(v, "~") {
			env[boxDataDirEnv] = b.r.resolvePath(v)
		}
		for _, sv := range rel.Services {
			e := fileEntry{spec: serviceSpec{
				Name: name + "--" + sv.Name, App: name, Source: apigen.OrbServiceSourceApp,
				Command: sv.Command, Cwd: sv.Cwd, Dir: b.currentLink(name),
				Port: a.Port, PortExplicit: true, Env: env, Links: []apigen.OrbPortalLink{},
			}}
			e.err = validateSpec(&e.spec)
			entries = append(entries, e)
		}
	}
	b.r.svc.apps.Store(&entries)
	b.r.svc.appPorts.Store(&ports)
	b.refreshStatusLocked()
}

// refreshStatusLocked 重算 GET /box/apps 的快照：试运行中的 app 报 Trial.Prev 的（确认过的）状态。
func (b *boxManager) refreshStatusLocked() {
	names := map[string]bool{}
	for n := range b.apps {
		names[n] = true
	}
	for n := range b.activating {
		names[n] = true
	}
	out := apigen.OrbBoxAppsState{Apps: []apigen.OrbBoxAppState{}}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		st := apigen.OrbBoxAppState{Name: name, Releases: []string{}}
		if a := b.apps[name]; a != nil {
			view := a
			if a.Trial != nil {
				view = a.Trial.Prev
			}
			st.Port = a.Port
			if view != nil {
				st.Port, st.Live = view.Port, view.Live
				for _, rel := range view.Releases {
					st.Releases = append(st.Releases, rel.ID)
				}
			}
		}
		if id := b.activating[name]; id != "" {
			st.Activating = new(id)
		}
		out.Apps = append(out.Apps, st)
	}
	b.status.Store(&out)
}

// beginLocked / endLocked 标出一次 activate / rollback：从进来到返回，快照都带着 activating。
func (b *boxManager) beginLocked(app, id string) {
	b.activating[app] = id
	b.refreshStatusLocked()
}

func (b *boxManager) endLocked(app string) {
	delete(b.activating, app)
	b.refreshStatusLocked()
}

// ---- 操作 ----

// activate 解包 incoming 里的 release、切换过去并等就绪；不就绪则退回之前的 release。
func (b *boxManager) activate(ctx context.Context, app string, req apigen.OrbBoxActivateRequest) apigen.OrbBoxResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := req.ReleaseID
	cur := b.apps[app]
	if cur != nil && cur.Trial == nil && cur.Live == id {
		return boxOK(id, nil) // 控制面重放同一次激活
	}
	if cur.release(id) != nil {
		return boxFailResult(boxFailf("release_exists", "release %s is already on the box; roll back to it instead", id), "")
	}
	b.beginLocked(app, id)
	defer b.endLocked(app)
	prev := cur.clone()
	relDir := b.releaseDir(app, id)
	archive := filepath.Join(b.incomingDir(), id+".tar.gz")

	// 1. 打开包（文件名由校验过的 releaseId 拼出）。
	f, err := openIncoming(b.incomingDir(), id+".tar.gz")
	if err != nil {
		return boxFailResult(boxFailf("archive_missing", "release archive %s is missing: %v", id, err), "")
	}
	defer f.Close()

	// 2. box 上的未提交改动。
	liveHead := ""
	if req.Source && cur != nil && cur.Live != "" && (req.DiscardBoxChanges == nil || !*req.DiscardBoxChanges) {
		if lr := cur.release(cur.Live); lr != nil && lr.Source {
			current := b.currentLink(app)
			if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
				if out, err := b.git(ctx, current, "status", "--porcelain"); err == nil {
					if lines := nonEmptyLines(out); len(lines) > 0 {
						return boxFailResult(boxFailf("box_changes",
							"the live release has uncommitted changes on the box:\n%s\ncommit and push them from a thread on the box, or redeploy with --discard-box-changes",
							strings.Join(lines[:min(len(lines), 20)], "\n")), "")
					}
					if head, err := b.git(ctx, current, "rev-parse", "HEAD"); err == nil {
						liveHead = strings.TrimSpace(head)
					}
				}
			}
		}
	}

	// 3. 解包到 releases/<id>.tmp，再 rename。
	if b.hookExtract != nil {
		b.hookExtract(app, id)
	}
	tmp := relDir + ".tmp"
	err = os.RemoveAll(tmp)
	if err == nil {
		err = os.MkdirAll(tmp, 0o755)
	}
	if err == nil {
		err = b.extract(ctx, f, tmp)
	}
	if err == nil {
		if err = os.RemoveAll(relDir); err == nil {
			err = os.Rename(tmp, relDir)
		}
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		b.removeEmptyApp(app, prev)
		return boxFailResult(asBoxFail(err), "")
	}
	_ = f.Close()
	_ = os.Remove(archive)

	// 4. box 上的提交须是新 release 的祖先。
	if liveHead != "" {
		if _, err := b.git(ctx, relDir, "merge-base", "--is-ancestor", liveHead, "HEAD"); err != nil {
			_ = os.RemoveAll(relDir)
			return boxFailResult(boxFailf("box_changes", "commits made on the box are not in the deployed branch: %s", liveHead[:min(len(liveHead), 12)]), "")
		}
	}

	// 5. 从这里起不再理会请求的取消。
	if err := os.MkdirAll(filepath.Join(b.appDir(app), "data"), 0o755); err != nil {
		_ = os.RemoveAll(relDir)
		b.removeEmptyApp(app, prev)
		return boxFailResult(asBoxFail(err), "")
	}
	next := &boxApp{
		Port: req.Port, Env: maps.Clone(req.Env), Live: id,
		Releases: []boxRelease{{ID: id, Source: req.Source, Services: slices.Clone(req.Services)}},
		Trial:    &boxTrial{Release: id, Prev: prev},
	}
	if prev != nil {
		next.Releases = append(next.Releases, prev.clone().Releases...)
	}
	res := b.tryLocked(app, next, prev, time.Duration(req.ReadyTimeoutSec)*time.Second)
	if !res.Ok {
		return res
	}
	// 8（续）. 裁剪：超过 keep 的从最旧删起（不删 live）。
	res.Pruned = b.pruneLocked(app, next, req.Keep)
	if err := b.saveLocked(); err != nil {
		b.r.log.Warn("write box state", "err", err)
	}
	b.publishLocked()
	return res
}

// rollback 把 app 切回一个保留着的 release（不解包、不裁剪），不就绪则退回。
func (b *boxManager) rollback(app string, req apigen.OrbBoxRollbackRequest) apigen.OrbBoxResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := req.ReleaseID
	cur := b.apps[app]
	if cur.release(id) == nil {
		return boxFailResult(boxFailf("release_not_found", "release %s of %s is not on the box", id, app), "")
	}
	if cur.Trial == nil && cur.Live == id {
		return boxOK(id, nil) // 上一次的应答丢了、控制面重试
	}
	if st, err := os.Stat(b.releaseDir(app, id)); err != nil || !st.IsDir() {
		return boxFailResult(boxFailf("release_not_found", "release %s of %s is missing on disk", id, app), "")
	}
	b.beginLocked(app, id)
	defer b.endLocked(app)
	prev := cur.clone()
	next := cur.clone()
	next.Live = id
	next.Trial = &boxTrial{Release: id, Prev: prev}
	return b.tryLocked(app, next, prev, time.Duration(req.ReadyTimeoutSec)*time.Second)
}

// tryLocked 是 activate 的第 6–9 步（rollback 共用）：带着 Trial 落盘、换链接、publish（一次 switchApp），等就绪；
// 就绪则清掉 Trial 并落盘，否则再用一次 switchApp 退回 prev。
func (b *boxManager) tryLocked(app string, next, prev *boxApp, timeout time.Duration) apigen.OrbBoxResult {
	id := next.Live
	b.apps[app] = next
	err := b.r.svc.switchApp(app, func() error {
		// 先落盘（带着 Trial）再换链接：两者之间崩溃时，重启后按 Trial 退回，current 不会被当成 live 拉起。
		if err := b.saveLocked(); err != nil {
			return err
		}
		if err := b.linkCurrent(app, id); err != nil {
			return err
		}
		b.publishLocked()
		return nil
	})
	var fail *boxFail
	if err != nil {
		fail = boxFailf("switch_failed", "switch %s to %s: %v", app, id, err)
	} else {
		if b.hookTrial != nil {
			b.hookTrial(app, id)
		}
		if !b.waitReady(app, next.Port, timeout) {
			fail = boxFailf("not_listening", "%s is not listening on port %d after %s", app, next.Port, timeout)
		}
	}
	if fail == nil {
		next.Trial = nil
		if err := b.saveLocked(); err != nil {
			b.r.log.Warn("write box state", "err", err)
		}
		b.publishLocked()
		return boxOK(id, nil)
	}
	log := b.logTail(app)
	b.revertLocked(app, prev, id)
	return boxFailResult(fail, log)
}

// revertLocked 是失败收尾：用一次 switchApp 把 app 整体换回 prev（prev 为 nil 则移除 app），再按删除规则清理。
func (b *boxManager) revertLocked(app string, prev *boxApp, trial string) {
	if err := b.r.svc.switchApp(app, func() error {
		if err := b.restoreLocked(app, prev); err != nil {
			b.r.log.Warn("restore box app", "app", app, "err", err)
		}
		return nil // 无论如何都把之前的服务拉回来
	}); err != nil {
		b.r.log.Warn("switch box app back", "app", app, "err", err)
	}
	b.cleanupTrial(app, trial, prev)
}

// restoreLocked 把 current 链回 prev.Live 并用 prev 整体替换内存里的 app（prev 为 nil 则移除 app 与 current），
// 落盘并 publish。先换链接后落盘：两者之间崩溃时状态里仍有 Trial，重启后再退一次（幂等）。
func (b *boxManager) restoreLocked(app string, prev *boxApp) error {
	var err error
	if prev != nil {
		err = b.linkCurrent(app, prev.Live)
		b.apps[app] = prev
	} else {
		if rerr := os.Remove(b.currentLink(app)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = rerr
		}
		delete(b.apps, app)
	}
	if serr := b.saveLocked(); err == nil {
		err = serr
	}
	b.publishLocked()
	return err
}

// cleanupTrial：试运行的 release 不在 prev.Releases 里（这次新解出来的）才删它的目录、残留的 .tmp 与包；
// prev 为 nil 且 data/ 为空时连 ~/apps/<app> 一起删。
func (b *boxManager) cleanupTrial(app, trial string, prev *boxApp) {
	if prev.release(trial) == nil {
		dir := b.releaseDir(app, trial)
		_ = os.RemoveAll(dir)
		_ = os.RemoveAll(dir + ".tmp")
		_ = os.Remove(filepath.Join(b.incomingDir(), trial+".tar.gz"))
	}
	b.removeEmptyApp(app, prev)
}

// removeEmptyApp：之前没有这个 app、data/ 也是空的，就删掉 ~/apps/<app>（首次激活失败不留目录）。
func (b *boxManager) removeEmptyApp(app string, prev *boxApp) {
	if prev != nil || b.apps[app] != nil {
		return
	}
	des, err := os.ReadDir(filepath.Join(b.appDir(app), "data"))
	if err == nil && len(des) > 0 {
		return
	}
	_ = os.RemoveAll(b.appDir(app))
}

// pruneLocked 删掉 keep 之外最旧的 release（不删 live），返回删掉的 id。
func (b *boxManager) pruneLocked(app string, a *boxApp, keep int) []string {
	keep = max(keep, 1)
	var kept []boxRelease
	var pruned []string
	for i, rel := range a.Releases {
		if i < keep || rel.ID == a.Live {
			kept = append(kept, rel)
			continue
		}
		if err := os.RemoveAll(b.releaseDir(app, rel.ID)); err != nil {
			b.r.log.Warn("prune box release", "app", app, "release", rel.ID, "err", err)
		}
		pruned = append(pruned, rel.ID)
	}
	a.Releases = kept
	return pruned
}

// restart 换上新的 env 并重启 app 的全部服务，等就绪；不回滚（新 env 保留）。
func (b *boxManager) restart(app string, req apigen.OrbBoxRestartRequest) apigen.OrbBoxResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := b.apps[app]
	if cur == nil || cur.release(cur.Live) == nil {
		return boxFailResult(boxFailf("app_not_found", "app %s is not deployed on this box", app), "")
	}
	var saveErr error
	if err := b.r.svc.switchApp(app, func() error {
		cur.Env = maps.Clone(req.Env)
		saveErr = b.saveLocked()
		b.publishLocked()
		return nil
	}); err != nil {
		return boxFailResult(boxFailf("switch_failed", "restart %s: %v", app, err), "")
	}
	if saveErr != nil {
		b.r.log.Warn("write box state", "err", saveErr)
	}
	timeout := time.Duration(req.ReadyTimeoutSec) * time.Second
	if !b.waitReady(app, cur.Port, timeout) {
		return boxFailResult(boxFailf("not_listening", "%s is not listening on port %d after %s", app, cur.Port, timeout), b.logTail(app))
	}
	return boxOK(cur.Live, nil)
}

// undeploy 从状态里移除 app（它的服务随之从监督器消失），再删 releases/ 与 current（purge 时连 data/）。
func (b *boxManager) undeploy(app string, purge bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var saveErr error
	if b.apps[app] != nil {
		if err := b.r.svc.switchApp(app, func() error {
			delete(b.apps, app)
			saveErr = b.saveLocked()
			b.publishLocked()
			return nil
		}); err != nil {
			return err
		}
	}
	dir := b.appDir(app)
	if purge {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		return saveErr
	}
	if err := os.RemoveAll(filepath.Join(dir, "releases")); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "current")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	b.removeEmptyApp(app, nil)
	return saveErr
}

// waitReady 每 readyPoll 试连 127.0.0.1:<port>、再 [::1]:<port>；该 app 的服务全部 failed 或超时即不就绪。
func (b *boxManager) waitReady(app string, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if c, err := dialLoopback(port); err == nil {
			_ = c.Close()
			return true
		}
		if b.allFailed(app) || time.Now().After(deadline) {
			return false
		}
		time.Sleep(b.readyPoll)
	}
}

// appServices 返回监督器里属于 app 的服务快照。
func (b *boxManager) appServices(app string) []apigen.OrbService {
	var out []apigen.OrbService
	for _, o := range b.r.svc.List() {
		if o.App != nil && *o.App == app {
			out = append(out, o)
		}
	}
	return out
}

func (b *boxManager) allFailed(app string) bool {
	list := b.appServices(app)
	if len(list) == 0 {
		return false
	}
	for _, o := range list {
		if o.Status != apigen.OrbServiceStatusFailed {
			return false
		}
	}
	return true
}

// logTail 返回 app 各服务日志末尾，合计至多 boxLogTail 字节。
func (b *boxManager) logTail(app string) string {
	list := b.appServices(app)
	if len(list) == 0 {
		return ""
	}
	per := int64(boxLogTail / len(list))
	var sb strings.Builder
	for _, o := range list {
		data, err := readLogTail(filepath.Join(b.r.svc.logDir, o.Name+".log"), per-int64(len(o.Name))-8)
		if err != nil || len(data) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "== %s ==\n%s", o.Name, data)
		if !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteByte('\n')
		}
	}
	s := sb.String()
	if len(s) > boxLogTail {
		s = s[len(s)-boxLogTail:]
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// git 在 dir 里跑 git（按可信 PATH 解析），返回 stdout；非零退出返回错误。
func (b *boxManager) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := b.r.bins.command(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = envList(b.r.baseEnv())
	cmd.Stdin = nil
	out, err := cmd.Output()
	return string(out), err
}

func nonEmptyLines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func openIncoming(dir, name string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(name)
}

// asBoxFail 把解包 / 文件系统错误映射为业务失败。
func asBoxFail(err error) *boxFail {
	var bf *boxFail
	switch {
	case errors.As(err, &bf):
		return bf
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return boxFailf("canceled", "activation canceled: %v", err)
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return boxFailf("no_space", "%v", err)
	default:
		return boxFailf("internal", "%v", err)
	}
}

func boxOK(live string, pruned []string) apigen.OrbBoxResult {
	return apigen.OrbBoxResult{Ok: true, LiveReleaseID: new(live), Pruned: pruned}
}

func boxFailResult(f *boxFail, log string) apigen.OrbBoxResult {
	res := apigen.OrbBoxResult{Ok: false, Code: new(f.code), Message: new(f.msg)}
	if log != "" {
		res.Log = new(log)
	}
	return res
}

// ---- 解包 ----

// extract 把 tar.gz 解到 dir（已存在且为空）。全部写入经 os.Root（拒绝解析到根之外，包括经由包里先放进来的
// 符号链接）。条目名去掉前导 `./` 与目录结尾的 `/`；绝对路径、`..` 与非规范路径 → bad_archive；
// 符号链接的目标原样保留（只在 app 运行时被跟随）；硬链接两端都在根内解析；设备、FIFO 等 → bad_archive。
func (b *boxManager) extract(ctx context.Context, r io.Reader, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return boxFailf("bad_archive", "not a gzip archive: %v", err)
	}
	tr := tar.NewReader(gz)
	var (
		entries int
		total   int64
	)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return boxFailf("bad_archive", "read archive: %v", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		entries++
		if entries > b.maxEntries {
			return boxFailf("too_large", "archive has more than %d entries", b.maxEntries)
		}
		name, ok := archiveName(hdr.Name)
		if !ok {
			return boxFailf("bad_archive", "entry %q: invalid path", hdr.Name)
		}
		if name == "" {
			continue // `.` 本身
		}
		if err := b.extractEntry(root, tr, hdr, name, &total); err != nil {
			return err
		}
	}
	// 读到 gzip 末尾才校验 CRC 与长度
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return boxFailf("bad_archive", "read archive: %v", err)
	}
	return nil
}

func (b *boxManager) extractEntry(root *os.Root, tr *tar.Reader, hdr *tar.Header, name string, total *int64) error {
	rootErr := func(err error) error {
		if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
			return err
		}
		return boxFailf("bad_archive", "entry %q: %v", hdr.Name, err)
	}
	mkParent := func() error {
		if d := path.Dir(name); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return rootErr(err)
			}
		}
		return nil
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(name, 0o755); err != nil {
			return rootErr(err)
		}
	case tar.TypeReg:
		if hdr.Size < 0 {
			return boxFailf("bad_archive", "entry %q: negative size", hdr.Name)
		}
		*total += hdr.Size
		if *total > b.maxUnpacked {
			return boxFailf("too_large", "archive unpacks to more than %d bytes", b.maxUnpacked)
		}
		if err := mkParent(); err != nil {
			return err
		}
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hdr.Mode&0o777))
		if err != nil {
			return rootErr(err)
		}
		_, err = io.CopyN(f, tr, hdr.Size)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return boxFailf("bad_archive", "entry %q: truncated", hdr.Name)
			}
			return rootErr(err)
		}
	case tar.TypeSymlink:
		if hdr.Linkname == "" || strings.ContainsRune(hdr.Linkname, 0) {
			return boxFailf("bad_archive", "entry %q: invalid link target", hdr.Name)
		}
		if err := mkParent(); err != nil {
			return err
		}
		if err := root.Symlink(hdr.Linkname, name); err != nil {
			return rootErr(err)
		}
	case tar.TypeLink:
		target, ok := archiveName(hdr.Linkname)
		if !ok || target == "" {
			return boxFailf("bad_archive", "entry %q: invalid link target %q", hdr.Name, hdr.Linkname)
		}
		if err := mkParent(); err != nil {
			return err
		}
		if err := root.Link(target, name); err != nil {
			return rootErr(err)
		}
	default:
		return boxFailf("bad_archive", "entry %q: type %q not allowed", hdr.Name, hdr.Typeflag)
	}
	return nil
}

// archiveName 规范化条目名：去掉前导 `./` 与结尾 `/`；`.` 本身返回 ("", true)。之后须非绝对、规范、不逃出根。
func archiveName(raw string) (string, bool) {
	name := raw
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return "", raw != ""
	}
	if strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || path.Clean(name) != name ||
		name == ".." || strings.HasPrefix(name, "../") {
		return "", false
	}
	return name, true
}

// ---- 本机 API ----

func (r *Runtime) boxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /box/incoming/{releaseId}", r.handleBoxIncoming)
	mux.HandleFunc("GET /box/apps", r.handleBoxApps)
	mux.HandleFunc("POST /box/apps/{app}/activate", r.handleBoxActivate)
	mux.HandleFunc("POST /box/apps/{app}/rollback", r.handleBoxRollback)
	mux.HandleFunc("POST /box/apps/{app}/restart", r.handleBoxRestart)
	mux.HandleFunc("DELETE /box/apps/{app}", r.handleBoxUndeploy)
	mux.HandleFunc("POST /box/exec", r.handleBoxExec)
}

// boxMgr 返回 box 管理器；没有（协调宿主）时写 404。
func (r *Runtime) boxMgr(w http.ResponseWriter) *boxManager {
	if r.box == nil {
		writeErr(w, errf(http.StatusNotFound, "not_found", "box apps are not available here"))
	}
	return r.box
}

// boxAppName 取路径里的 app 名并校验。
func boxAppName(req *http.Request) (string, error) {
	app := req.PathValue("app")
	if !portalhost.ValidAppName(app) {
		return "", badRequest("invalid app name %q", app)
	}
	return app, nil
}

// readBoxJSON 同 readJSON，但上限 maxBoxJSONBody（带 env 的请求）。
func readBoxJSON(w http.ResponseWriter, req *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBoxJSONBody))
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

func validBoxEnv(env map[string]string) error {
	for k := range env {
		if !envKeyRe.MatchString(k) {
			return badRequest("invalid env name %q", k)
		}
		if reservedServiceEnv[k] {
			return badRequest("env %s is managed by orbd", k)
		}
	}
	return nil
}

func validReadyTimeout(sec int) error {
	if sec < 1 || sec > maxBoxReadyTimeout {
		return badRequest("readyTimeoutSec must be 1-%d", maxBoxReadyTimeout)
	}
	return nil
}

// validateActivate 校验并规范化激活请求（服务 cwd 清洗成相对 release 根的规范形式）。
func (r *Runtime) validateActivate(req *apigen.OrbBoxActivateRequest) error {
	if !boxReleaseRe.MatchString(req.ReleaseID) {
		return badRequest("invalid releaseId %q", req.ReleaseID)
	}
	if req.Port < 1 || req.Port > 65535 || r.reservedPort(req.Port) {
		return badRequest("invalid port %d", req.Port)
	}
	if n := len(req.Services); n < 1 || n > maxBoxAppServices {
		return badRequest("an app needs 1-%d services", maxBoxAppServices)
	}
	seen := map[string]bool{}
	for i := range req.Services {
		sv := &req.Services[i]
		if !boxServiceRe.MatchString(sv.Name) || strings.Contains(sv.Name, "--") || seen[sv.Name] {
			return badRequest("invalid or duplicate service name %q", sv.Name)
		}
		seen[sv.Name] = true
		if strings.TrimSpace(sv.Command) == "" {
			return badRequest("service %s: command is required", sv.Name)
		}
		cwd, err := cleanCwd(sv.Cwd)
		if err != nil {
			return badRequest("service %s: %v", sv.Name, err)
		}
		sv.Cwd = cwd
	}
	if req.Keep < 1 {
		return badRequest("keep must be at least 1")
	}
	if err := validBoxEnv(req.Env); err != nil {
		return err
	}
	return validReadyTimeout(req.ReadyTimeoutSec)
}

// handleBoxIncoming 收下一个 release 压缩包：校验字节数与 SHA-256 后提交到 incoming/<releaseId>.tar.gz。
func (r *Runtime) handleBoxIncoming(w http.ResponseWriter, req *http.Request) {
	b := r.boxMgr(w)
	if b == nil {
		return
	}
	id := req.PathValue("releaseId")
	if !boxReleaseRe.MatchString(id) {
		writeErr(w, badRequest("invalid releaseId %q", id))
		return
	}
	q := req.URL.Query()
	size, err := strconv.ParseInt(q.Get("size"), 10, 64)
	if err != nil || size < 0 {
		writeErr(w, badRequest("invalid size=%q", q.Get("size")))
		return
	}
	want := q.Get("sha256")
	if !sha256Re.MatchString(want) {
		writeErr(w, badRequest("invalid sha256 %q", want))
		return
	}
	if size > b.maxArchive || req.ContentLength > b.maxArchive {
		writeErr(w, errf(http.StatusRequestEntityTooLarge, "too_large", "release archive exceeds %d bytes", b.maxArchive))
		return
	}
	b.sweepIncoming()
	h := sha256.New()
	tmp, err := r.receiveFile(http.MaxBytesReader(w, req.Body, b.maxArchive), "box-", h)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer func() { _ = os.Remove(tmp) }() // 提交成功后 tmp 已不存在
	st, err := os.Stat(tmp)
	if err != nil {
		writeErr(w, err)
		return
	}
	if st.Size() != size {
		writeErr(w, errf(http.StatusBadRequest, "size_mismatch", "received %d bytes, expected %d", st.Size(), size))
		return
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		writeErr(w, errf(http.StatusUnprocessableEntity, "checksum_mismatch", "sha256 %s does not match %s", got, want))
		return
	}
	if err := os.MkdirAll(b.incomingDir(), 0o700); err != nil {
		writeErr(w, err)
		return
	}
	if err := commitFile(tmp, filepath.Join(b.incomingDir(), id+".tar.gz"), false); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sweepIncoming 删掉 incoming/ 里修改时间超过 boxIncomingTTL 的遗留包。
func (b *boxManager) sweepIncoming() {
	des, err := os.ReadDir(b.incomingDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-boxIncomingTTL)
	for _, de := range des {
		if info, err := de.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(b.incomingDir(), de.Name()))
		}
	}
}

func (r *Runtime) handleBoxApps(w http.ResponseWriter, _ *http.Request) {
	b := r.boxMgr(w)
	if b == nil {
		return
	}
	writeJSON(w, http.StatusOK, b.status.Load())
}

func (r *Runtime) handleBoxActivate(w http.ResponseWriter, req *http.Request) {
	b := r.boxMgr(w)
	if b == nil {
		return
	}
	app, err := boxAppName(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body apigen.OrbBoxActivateRequest
	if err := readBoxJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := r.validateActivate(&body); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b.activate(req.Context(), app, body))
}

func (r *Runtime) handleBoxRollback(w http.ResponseWriter, req *http.Request) {
	b := r.boxMgr(w)
	if b == nil {
		return
	}
	app, err := boxAppName(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body apigen.OrbBoxRollbackRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	if !boxReleaseRe.MatchString(body.ReleaseID) {
		writeErr(w, badRequest("invalid releaseId %q", body.ReleaseID))
		return
	}
	if err := validReadyTimeout(body.ReadyTimeoutSec); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b.rollback(app, body))
}

func (r *Runtime) handleBoxRestart(w http.ResponseWriter, req *http.Request) {
	b := r.boxMgr(w)
	if b == nil {
		return
	}
	app, err := boxAppName(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body apigen.OrbBoxRestartRequest
	if err := readBoxJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := validBoxEnv(body.Env); err != nil {
		writeErr(w, err)
		return
	}
	if err := validReadyTimeout(body.ReadyTimeoutSec); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b.restart(app, body))
}

func (r *Runtime) handleBoxUndeploy(w http.ResponseWriter, req *http.Request) {
	b := r.boxMgr(w)
	if b == nil {
		return
	}
	app, err := boxAppName(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	purge, err := boolQuery(req, "purge", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := b.undeploy(app, purge); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
