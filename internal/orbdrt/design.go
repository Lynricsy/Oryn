package orbdrt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/designengine"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// 设计画布的 orb 工作副本（M29，docs/plan-m29.md C3 / C4）：`~/designs/<artifactId>/` 是 artifact 库里设计稿的缓存，
// 经 ctl artifact.call 的内部操作物化（fetch_design_files）与封存（seal_design）；dev 服务是受管服务 dsn-<hash>。

const (
	// designMetaFile 是工作副本根下的元数据（designMeta）。
	designMetaFile = ".oryn-design.json"
	// designShareRoot 是镜像预装引擎依赖的根：<根>/<引擎版本>/node_modules。
	designShareRoot = "/usr/local/share/oryn/design-engine"
	// designSealTimeout 是 design.seal 的上限（C4）。
	designSealTimeout = 5 * time.Minute
	// designOpenTimeout 是 design.open 的上限：可能要 npm ci 引擎、装设计依赖、物化大文件。
	designOpenTimeout = 10 * time.Minute
	// designDefaultTitle 是 design.open 没给标题时新画布的标题。
	designDefaultTitle = "未命名设计"
)

// designIDRe 限定工作副本目录名（artifact id），防止路径穿越。
var designIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// designMeta 是 .oryn-design.json（C3）：BaseVersion 是副本所基于的版本，SealedHash 是那时 src/** 的
// design.SourceHash（与磁盘上的不同即为脏），Service / Port 是 dev 服务。
type designMeta struct {
	ArtifactID  string `json:"artifactId"`
	ThreadID    string `json:"threadId"`
	BaseVersion int    `json:"baseVersion"`
	Engine      string `json:"engine"`
	SealedHash  string `json:"sealedHash"`
	Service     string `json:"service"`
	Port        int    `json:"port"`
}

// designTurn 是线程当前的 agent 回合（design.turn 记下）。
type designTurn struct{ id, title string }

// designState 是设计运行时的内存状态；零值可用。
type designState struct {
	mu     sync.Mutex
	turns  map[string]designTurn        // threadId → 当前回合
	locks  map[string]*sync.Mutex       // artifactId → 串行化该副本的 open / seal / check
	checks map[string]designCachedCheck // artifactId → 最近一次全帧 design.check
	// shareRoot 覆盖 designShareRoot（测试用）；空 = designShareRoot。
	shareRoot string
	// chromium 覆盖 Chromium 的查找（测试用）：非空时只认这一个路径。
	chromium string
	// engineMu 串行化引擎源的写出与依赖安装。
	engineMu sync.Mutex
}

// lock 锁住一个工作副本，返回解锁函数。
func (d *designState) lock(id string) func() {
	d.mu.Lock()
	if d.locks == nil {
		d.locks = map[string]*sync.Mutex{}
	}
	m := d.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		d.locks[id] = m
	}
	d.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (d *designState) turn(threadID string) (designTurn, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.turns[threadID]
	return t, ok
}

// setTurn 记下线程的当前回合，返回之前的回合。
func (d *designState) setTurn(threadID string, t designTurn) (designTurn, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.turns == nil {
		d.turns = map[string]designTurn{}
	}
	prev, ok := d.turns[threadID]
	d.turns[threadID] = t
	return prev, ok
}

func (d *designState) share() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shareRoot != "" {
		return d.shareRoot
	}
	return designShareRoot
}

// designProbeConfig 是探针的审美配置（取自 design_head；surface 只认 landing、app，与探针一致）。
type designProbeConfig struct {
	aesthetic bool
	surface   string
}

// designProbeConfigOf 取 head 的探针配置。
func designProbeConfigOf(head orbsock.DesignHead) designProbeConfig {
	cfg := designProbeConfig{aesthetic: head.Aesthetic}
	if head.Surface == "landing" || head.Surface == "app" {
		cfg.surface = head.Surface
	}
	return cfg
}

// query 是页面地址的探针参数（含 `__oryn_probe=1`），不带开头的 ?。
func (c designProbeConfig) query() string {
	q := design.ProbeQuery + "=1"
	if c.aesthetic {
		q += "&__oryn_aesthetic=1"
	}
	if c.surface != "" {
		q += "&__oryn_surface=" + c.surface
	}
	return q
}

// designCachedCheck 是缓存的一次全帧检查：Check.Fingerprint 是当时源码的 SourceHash，cfg 是当时的探针配置。
type designCachedCheck struct {
	check design.Check
	cfg   designProbeConfig
}

// setCheck 记下 id 最近一次全帧检查与它用的探针配置。
func (d *designState) setCheck(id string, c design.Check, cfg designProbeConfig) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checks == nil {
		d.checks = map[string]designCachedCheck{}
	}
	d.checks[id] = designCachedCheck{check: c, cfg: cfg}
}

// check 返回 id 缓存的检查，只在它的 Fingerprint 等于 hash（源码自检查以来没变）且探针配置等于 cfg（方向与 surface
// 自检查以来没变，cfg 取当前 head）时有效。
func (d *designState) check(id, hash string, cfg designProbeConfig) (design.Check, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.checks[id]
	return c.check, ok && hash != "" && c.check.Fingerprint == hash && c.cfg == cfg
}

func (d *designState) chromiumOverride() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.chromium
}

// ---- 路径 ----

func (r *Runtime) designsRoot() string { return filepath.Join(r.home, "designs") }

func (r *Runtime) designDir(id string) string { return filepath.Join(r.designsRoot(), id) }

func (r *Runtime) designEngineDir() string {
	return filepath.Join(r.home, ".oryn", "design", "engine", designengine.Version)
}

func (r *Runtime) designSandboxDir() string {
	return filepath.Join(r.home, ".oryn", "design", "sandbox")
}

// designServiceName 是工作副本的 dev 服务名 `dsn-<sha256(artifactId) 前 10 位 hex>`。
func designServiceName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "dsn-" + hex.EncodeToString(sum[:])[:10]
}

func designDevURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) + "/" }

// ---- orbd.sock ----

// handleDesignSock 处理 design.* 方法；第二个返回值为 false 表示不是设计方法。
func (r *Runtime) handleDesignSock(ctx context.Context, method string, params json.RawMessage, out *orbsock.Responder) (any, bool, error) {
	switch method {
	case orbsock.MethodDesignOpen:
		var p orbsock.DesignOpenParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		res, err := r.designOpen(ctx, p, out)
		return res, true, err
	case orbsock.MethodDesignTurn:
		var p orbsock.DesignTurnParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		return struct{}{}, true, r.designTurnStart(ctx, p, out)
	case orbsock.MethodDesignSeal:
		var p orbsock.DesignSealParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		res, err := r.designSeal(ctx, p, out)
		return res, true, err
	case orbsock.MethodDesignCheck:
		var p orbsock.DesignCheckParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		res, err := r.designCheck(ctx, p)
		return res, true, err
	case orbsock.MethodDesignStatus:
		var p orbsock.DesignStatusParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		res, err := r.designStatus(ctx, p)
		return res, true, err
	}
	return nil, false, nil
}

func needThread(threadID string) error {
	if threadID == "" {
		return orbsock.Errorf(orbsock.CodeInvalidParams, "threadId is required")
	}
	return nil
}

// ---- design.open ----

// designOpen 建立或接管本线程的工作副本：没有 id 时先建一个空设计画布；物化（或对账）head、写出引擎与 sandbox、
// 起 dev 服务。
func (r *Runtime) designOpen(ctx context.Context, p orbsock.DesignOpenParams, out *orbsock.Responder) (orbsock.DesignOpenResult, error) {
	if err := needThread(p.ThreadID); err != nil {
		return orbsock.DesignOpenResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, designOpenTimeout)
	defer cancel()
	turn, _ := r.design.turn(p.ThreadID)
	id := p.ID
	if id == "" {
		var err error
		if id, err = r.designCreate(ctx, p.ThreadID, &turn, p.Title); err != nil {
			return orbsock.DesignOpenResult{}, err
		}
	} else if !designIDRe.MatchString(id) {
		return orbsock.DesignOpenResult{}, orbsock.Errorf(orbsock.CodeInvalidParams, "invalid artifact id %q", id)
	}
	head, err := r.designHead(ctx, p.ThreadID, id)
	if err != nil {
		return orbsock.DesignOpenResult{}, err
	}
	unlock := r.design.lock(id)
	defer unlock()
	if err := r.designEnsureEngine(ctx, out); err != nil {
		return orbsock.DesignOpenResult{}, err
	}
	if err := r.designWriteSandbox(ctx, p.ThreadID); err != nil {
		return orbsock.DesignOpenResult{}, err
	}
	dir := r.designDir(id)
	meta, err := r.designReconcile(ctx, p.ThreadID, id, head)
	if err != nil {
		return orbsock.DesignOpenResult{}, err
	}
	meta.ThreadID = p.ThreadID
	if err := r.designDeps(ctx, dir, false, out); err != nil {
		return orbsock.DesignOpenResult{}, err
	}
	o, err := r.designStartService(ctx, p.ThreadID, id)
	if err != nil {
		return orbsock.DesignOpenResult{}, err
	}
	meta.Engine, meta.Service, meta.Port = designengine.Version, o.Name, *o.Port
	if err := writeDesignMeta(dir, meta); err != nil {
		return orbsock.DesignOpenResult{}, err
	}
	return orbsock.DesignOpenResult{ID: id, Dir: dir, Version: meta.BaseVersion, DevURL: designDevURL(meta.Port)}, nil
}

// designCreate 经 ctl 建一个带 design、没有画框的空设计画布，返回 id。
func (r *Runtime) designCreate(ctx context.Context, threadID string, turn *designTurn, title string) (string, error) {
	if strings.TrimSpace(title) == "" {
		title = designDefaultTitle
	}
	doc := canvas.Doc{Design: &canvas.Design{Engine: canvas.DesignEngine}, Canvases: []canvas.Canvas{{ID: canvas.DefaultCanvasID, Name: canvas.DefaultCanvasName}}}
	var res struct {
		ID string `json:"id"`
	}
	err := r.designCall(ctx, threadID, turn, "create_artifact", map[string]any{
		"title": title,
		"files": []any{map[string]any{"path": "canvas.json", "content": string(doc.JSON())}},
	}, &res)
	if err != nil {
		return "", err
	}
	if !designIDRe.MatchString(res.ID) {
		return "", orbsock.Errorf(orbsock.CodeInternal, "create_artifact returned an invalid id %q", res.ID)
	}
	return res.ID, nil
}

// designHead 取最新版本摘要（也校验它是设计画布或空画布）。
func (r *Runtime) designHead(ctx context.Context, threadID, id string) (orbsock.DesignHead, error) {
	var head orbsock.DesignHead
	err := r.designCall(ctx, threadID, nil, "design_head", map[string]any{"id": id}, &head)
	return head, err
}

// designReconcile 让副本与 head 对齐（C3 对账）：没有副本或副本属于别的 artifact 时物化；baseVersion 落后于 head
// 时干净的副本重新物化、脏的保留。调用方持有副本锁。
func (r *Runtime) designReconcile(ctx context.Context, threadID, id string, head orbsock.DesignHead) (designMeta, error) {
	dir := r.designDir(id)
	meta, err := readDesignMeta(dir)
	switch {
	case err == nil && meta.ArtifactID == id:
		if meta.BaseVersion >= head.Version {
			return meta, nil
		}
		cur, err := designSourceHash(dir)
		if err != nil {
			return designMeta{}, err
		}
		if cur != meta.SealedHash {
			r.log.Info("design copy is behind head but dirty; keeping it", "artifact", id, "base", meta.BaseVersion, "head", head.Version)
			return meta, nil
		}
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		r.log.Warn("design meta unreadable; materializing again", "artifact", id, "err", err)
	}
	hash, err := r.designMaterialize(ctx, threadID, id, head.Version)
	if err != nil {
		return designMeta{}, err
	}
	meta = designMeta{ArtifactID: id, ThreadID: threadID, BaseVersion: head.Version, Engine: designengine.Version, SealedHash: hash,
		Service: meta.Service, Port: meta.Port}
	return meta, writeDesignMeta(dir, meta)
}

// designFileState 是物化中一个文件的进度。
type designFileState struct {
	size, sha string
	written   int64
	want      int64
	h         hash.Hash
}

// designMaterialize 把版本 version 的 src/** 经 fetch_design_files 取回到 <dir>/src（保留已有的 src/node_modules），
// 返回它的 SourceHash。每片按 offset 追加到临时目录；全部取完后逐个核对 size 与 sha256，任何一处不符整次失败、
// 不动现有的 src/。
func (r *Runtime) designMaterialize(ctx context.Context, threadID, id string, version int) (string, error) {
	dir := r.designDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, ".oryn-src-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	newSrc := filepath.Join(tmp, "src")
	if err := os.MkdirAll(newSrc, 0o755); err != nil {
		return "", err
	}
	files := map[string]*designFileState{}
	var order []string
	cursor := ""
	for {
		params := map[string]any{"id": id, "version": version}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page orbsock.DesignFilesPage
		if err := r.designCall(ctx, threadID, nil, "fetch_design_files", params, &page); err != nil {
			return "", err
		}
		if page.Version != version {
			return "", orbsock.Errorf(orbsock.CodeInternal, "fetch_design_files returned version %d, want %d", page.Version, version)
		}
		for _, c := range page.Files {
			st, err := designWriteChunk(newSrc, files, c)
			if err != nil {
				return "", orbsock.Errorf(orbsock.CodeInternal, "materialize %s: %v", c.Path, err)
			}
			if st != "" {
				order = append(order, st)
			}
		}
		if page.Next == "" {
			break
		}
		if page.Next == cursor && len(page.Files) == 0 {
			return "", orbsock.Errorf(orbsock.CodeInternal, "fetch_design_files made no progress at cursor %q", cursor)
		}
		cursor = page.Next
	}
	for _, p := range order {
		st := files[p]
		if st.written != st.want || hex.EncodeToString(st.h.Sum(nil)) != st.sha {
			return "", orbsock.Errorf(orbsock.CodeInternal, "materialize %s: got %d bytes with a different digest, want %d bytes sha256 %s", p, st.written, st.want, st.sha)
		}
	}
	src := filepath.Join(dir, "src")
	if fi, err := os.Lstat(filepath.Join(src, "node_modules")); err == nil && fi.IsDir() {
		if err := os.Rename(filepath.Join(src, "node_modules"), filepath.Join(newSrc, "node_modules")); err != nil {
			return "", err
		}
	}
	old := filepath.Join(tmp, "old")
	if err := os.Rename(src, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(newSrc, src); err != nil {
		return "", err
	}
	return designSourceHash(dir)
}

// designWriteChunk 把一片追加到 root 下对应文件；返回首次见到的文件路径（用于最后核对），否则空串。
func designWriteChunk(root string, files map[string]*designFileState, c orbsock.DesignFileChunk) (string, error) {
	rel, ok := strings.CutPrefix(c.Path, "src/")
	if !ok || !filepath.IsLocal(rel) || slices.Contains(strings.Split(rel, "/"), "node_modules") {
		return "", errors.New("path is not under src/")
	}
	data, err := base64.StdEncoding.DecodeString(c.Data)
	if err != nil {
		return "", err
	}
	st, seen := files[c.Path]
	if !seen {
		if c.Offset != 0 {
			return "", fmt.Errorf("first chunk starts at offset %d", c.Offset)
		}
		st = &designFileState{sha: c.Sha256, want: c.Size, h: sha256.New()}
		files[c.Path] = st
	} else if c.Offset != st.written || c.Size != st.want || c.Sha256 != st.sha {
		return "", fmt.Errorf("chunk at offset %d does not continue %d written bytes", c.Offset, st.written)
	}
	if st.written+int64(len(data)) > st.want {
		return "", fmt.Errorf("chunks exceed the declared size %d", st.want)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if !seen {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	st.h.Write(data)
	st.written += int64(len(data))
	if seen {
		return "", nil
	}
	return c.Path, nil
}

// designEnsureEngine 写出引擎源并备好它的 node_modules：优先软链到镜像预装目录；预装目录不在时在引擎目录跑一次
// npm ci（进度经 out 流回）。
func (r *Runtime) designEnsureEngine(ctx context.Context, out *orbsock.Responder) error {
	r.design.engineMu.Lock()
	defer r.design.engineMu.Unlock()
	dir := r.designEngineDir()
	if err := designengine.WriteTo(dir); err != nil {
		return orbsock.Errorf(orbsock.CodeInternal, "%v", err)
	}
	link := filepath.Join(dir, "node_modules")
	share := filepath.Join(r.design.share(), designengine.Version, "node_modules")
	if fi, err := os.Stat(share); err == nil && fi.IsDir() {
		if cur, err := os.Readlink(link); err == nil && cur == share {
			return nil
		}
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			return nil // 之前 npm ci 装好的真目录，照用
		}
		_ = os.Remove(link)
		return os.Symlink(share, link)
	}
	if _, err := os.Stat(filepath.Join(link, "vite", "bin", "vite.js")); err == nil {
		return nil
	}
	_ = os.Remove(link) // 指向不存在目录的软链
	designLog(out, "[orbd] design engine %s is not preinstalled; running npm ci (about a minute)\n", designengine.Version)
	if tail, err := r.designExec(ctx, dir, nil, out, "npm", "ci", "--no-audit", "--no-fund"); err != nil {
		return &orbsock.Error{Code: orbsock.CodeInternal, Message: "npm ci for the design engine failed: " + err.Error() + "\n" + tail}
	}
	return nil
}

// designWriteSandbox 经 fetch_design_runtime 取控制面的页面运行时与探针，写到 ~/.oryn/design/sandbox/；控制面没有的
// 删掉旧的。
func (r *Runtime) designWriteSandbox(ctx context.Context, threadID string) error {
	var res orbsock.DesignRuntimeFiles
	if err := r.designCall(ctx, threadID, nil, "fetch_design_runtime", map[string]any{}, &res); err != nil {
		return err
	}
	dir := r.designSandboxDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	got := map[string]bool{}
	for _, f := range res.Files {
		if f.Name != "runtime.js" && f.Name != "probe.js" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return orbsock.Errorf(orbsock.CodeInternal, "fetch_design_runtime %s: %v", f.Name, err)
		}
		if err := writeFileAtomic(filepath.Join(dir, f.Name), data, 0o644); err != nil {
			return err
		}
		got[f.Name] = true
	}
	for _, name := range []string{"runtime.js", "probe.js"} {
		if !got[name] {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}

// designStartService 起（或保持）副本的 dev 服务并等它离开 starting。工作副本在工作区之外，受管服务的 cwd 只能是
// 工作区内的相对路径，所以在命令里 cd。
func (r *Runtime) designStartService(ctx context.Context, threadID, id string) (apigen.OrbService, error) {
	dir := r.designDir(id)
	engine := r.designEngineDir()
	cmd := "cd " + designQuote(dir) + " && exec node " + designQuote(filepath.Join(engine, "node_modules", "vite", "bin", "vite.js")) +
		" --config " + designQuote(filepath.Join(engine, "vite.config.mjs"))
	o, err := r.svc.Start(orbsock.ServiceStartParams{
		Name: designServiceName(id), Command: &cmd, Portal: true, ThreadID: threadID,
		Env: map[string]string{"ORYN_DESIGN_DIR": dir, "ORYN_DESIGN_SANDBOX": r.designSandboxDir()},
	})
	if err != nil {
		return apigen.OrbService{}, sockErr(err)
	}
	o = r.settle(ctx, o)
	if o.Status != apigen.OrbServiceStatusRunning || o.Port == nil {
		msg := ""
		if o.Error != nil {
			msg = *o.Error
		}
		if msg == "" {
			msg = string(o.Status)
		}
		return apigen.OrbService{}, orbsock.Errorf(orbsock.CodeInternal, "design dev server %s did not start (%s); see `oryn orb service logs %s`", o.Name, msg, o.Name)
	}
	return o, nil
}

// designDeps 在 src/package.json 存在且（force 或 src/node_modules 不在）时于 src/ 下跑 npm install（D3：不加限制）。
func (r *Runtime) designDeps(ctx context.Context, dir string, force bool, out *orbsock.Responder) error {
	src := filepath.Join(dir, "src")
	if _, err := os.Stat(filepath.Join(src, "package.json")); err != nil {
		return nil
	}
	if !force {
		if _, err := os.Stat(filepath.Join(src, "node_modules")); err == nil {
			return nil
		}
	}
	designLog(out, "[orbd] installing design dependencies (npm install in %s)\n", src)
	tail, err := r.designExec(ctx, src, nil, out, "npm", "install", "--no-audit", "--no-fund")
	if err != nil {
		return &orbsock.Error{Code: orbsock.CodeBuildFailed, Message: "npm install 失败（" + err.Error() + "），没有落版本：\n" + tail}
	}
	return nil
}

// ---- design.turn / design.seal ----

// designTurnStart 记下本回合；上一回合留下的脏副本（终态封存失败或没发出）先以旧回合封存，旧回合不明（orbd 重启过）
// 时不带回合。干净的副本照常跳过；封存失败只记日志，不挡新回合。随后把本线程的每个副本与 head 对账
// （designTurnReconcile），这样查看器里的「恢复」在下一回合就进了工作副本。
func (r *Runtime) designTurnStart(ctx context.Context, p orbsock.DesignTurnParams, out *orbsock.Responder) error {
	if err := needThread(p.ThreadID); err != nil {
		return err
	}
	if p.TurnID == "" {
		return orbsock.Errorf(orbsock.CodeInvalidParams, "turnId is required")
	}
	prev, _ := r.design.setTurn(p.ThreadID, designTurn{id: p.TurnID, title: p.Title})
	if prev.id == p.TurnID {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, designSealTimeout)
	defer cancel()
	if _, err := r.designSealThread(ctx, p.ThreadID, prev, out); err != nil {
		r.log.Warn("sealing the previous design turn failed", "thread", p.ThreadID, "turn", prev.id, "err", err)
	}
	for _, meta := range r.designCopies(p.ThreadID) {
		if err := r.designTurnReconcile(ctx, p.ThreadID, meta.ArtifactID, out); err != nil {
			r.log.Warn("reconciling the design copy at turn start failed", "thread", p.ThreadID, "artifact", meta.ArtifactID, "err", err)
		}
	}
	return nil
}

// designTurnReconcile 按 designReconcile 的规则让一个副本对齐 head（干净且落后 → 重新物化；脏的保留，留给封存报
// conflict）。重新物化了时：src/package.json 变了就重装设计依赖；dev 服务在跑就重启它（整个 src/ 被换掉，vite 的
// 监听跟不上）。失败由调用方记日志。
func (r *Runtime) designTurnReconcile(ctx context.Context, threadID, id string, out *orbsock.Responder) error {
	unlock := r.design.lock(id)
	defer unlock()
	dir := r.designDir(id)
	before, err := readDesignMeta(dir)
	if err != nil || before.ThreadID != threadID || before.ArtifactID != id {
		return nil // 刚被别的线程接管或删掉
	}
	oldPkg, _ := os.ReadFile(filepath.Join(dir, "src", "package.json"))
	head, err := r.designHead(ctx, threadID, id)
	if err != nil {
		return err
	}
	after, err := r.designReconcile(ctx, threadID, id, head)
	if err != nil || after.BaseVersion == before.BaseVersion {
		return err
	}
	designLog(out, "[orbd] %s: working copy re-materialized from v%d (was v%d)\n", id, after.BaseVersion, before.BaseVersion)
	newPkg, _ := os.ReadFile(filepath.Join(dir, "src", "package.json"))
	if err := r.designDeps(ctx, dir, !bytes.Equal(oldPkg, newPkg), out); err != nil {
		return err
	}
	if after.Service == "" {
		return nil
	}
	switch r.svc.snapshot(after.Service).Status {
	case apigen.OrbServiceStatusStopped, apigen.OrbServiceStatusFailed:
		return nil
	}
	_, err = r.svc.Restart(after.Service)
	return sockErr(err)
}

// designSeal 构建并封存本线程的每个脏副本。回合标题取 design.turn 记下的（turnId 相同时）。
func (r *Runtime) designSeal(ctx context.Context, p orbsock.DesignSealParams, out *orbsock.Responder) (orbsock.DesignSealResult, error) {
	if err := needThread(p.ThreadID); err != nil {
		return orbsock.DesignSealResult{}, err
	}
	turn := designTurn{id: p.TurnID}
	if t, ok := r.design.turn(p.ThreadID); ok && t.id == p.TurnID {
		turn = t
	}
	ctx, cancel := context.WithTimeout(ctx, designSealTimeout)
	defer cancel()
	return r.designSealThread(ctx, p.ThreadID, turn, out)
}

// designSealThread 依次封存本线程的副本（按 id 排序），遇到第一个错误即停并回它。
func (r *Runtime) designSealThread(ctx context.Context, threadID string, turn designTurn, out *orbsock.Responder) (orbsock.DesignSealResult, error) {
	res := orbsock.DesignSealResult{Sealed: []orbsock.DesignSealed{}, Skipped: []string{}}
	for _, meta := range r.designCopies(threadID) {
		v, err := r.designSealOne(ctx, threadID, meta.ArtifactID, turn, out)
		if err != nil {
			return res, err
		}
		if v == 0 {
			res.Skipped = append(res.Skipped, meta.ArtifactID)
		} else {
			res.Sealed = append(res.Sealed, orbsock.DesignSealed{ID: meta.ArtifactID, Version: v})
		}
	}
	return res, nil
}

// designSealOne 封存一个副本，返回新版本号；源码没变（hash == sealedHash）时返回 0。顺序：判脏（安装依赖之前的快照，
// agent 什么都没改时不触发安装）→ 装依赖 → 重读 src/ 并重算 hash（npm install 可能新建或改写 src/package-lock.json）
// → vite build → 工程锁（D2，写在 dist/_project/）→ 文件差集 → seal_design（带与新 hash 一致的缓存检查）→ 记 sealedHash。
func (r *Runtime) designSealOne(ctx context.Context, threadID, id string, turn designTurn, out *orbsock.Responder) (int, error) {
	unlock := r.design.lock(id)
	defer unlock()
	dir := r.designDir(id)
	meta, err := readDesignMeta(dir)
	if err != nil || meta.ThreadID != threadID {
		return 0, nil // 刚被别的线程接管或删掉
	}
	src, err := designSourceFiles(dir)
	if err != nil {
		return 0, err
	}
	if design.SourceHash(src) == meta.SealedHash {
		return 0, nil
	}
	head, err := r.designHead(ctx, threadID, id)
	if err != nil {
		return 0, err
	}
	headSha := make(map[string]string, len(head.Files))
	for _, f := range head.Files {
		headSha[f.Path] = f.Sha256
	}
	_, hasPkg := src["src/package.json"]
	if hasPkg {
		if err := r.designDeps(ctx, dir, designSha(src["src/package.json"]) != headSha["src/package.json"], out); err != nil {
			return 0, err
		}
		if src, err = designSourceFiles(dir); err != nil {
			return 0, err
		}
	}
	hash := design.SourceHash(src)
	if err := r.designEnsureEngine(ctx, out); err != nil {
		return 0, err
	}
	if err := r.designBuild(ctx, dir, id, out); err != nil {
		return 0, err
	}
	if hasPkg {
		if err := designengine.WriteProjectLock(ctx, dir); err != nil {
			return 0, &orbsock.Error{Code: orbsock.CodeBuildFailed, Message: id + " 的导出工程锁生成失败，没有落版本：\n" + designTail(err.Error())}
		}
	}
	files, err := designSealFiles(dir, src, headSha)
	if err != nil {
		return 0, err
	}
	params := map[string]any{"id": id, "baseVersion": meta.BaseVersion, "files": files}
	if chk, ok := r.design.check(id, hash, designProbeConfigOf(head)); ok {
		params["check"] = chk
	}
	var sealed struct {
		Version int `json:"version"`
	}
	if err := r.designCall(ctx, threadID, &turn, "seal_design", params, &sealed); err != nil {
		return 0, err
	}
	meta.BaseVersion, meta.SealedHash = sealed.Version, hash
	if err := writeDesignMeta(dir, meta); err != nil {
		return 0, err
	}
	return sealed.Version, nil
}

// designTail 是 s 末尾至多 4 KiB（错误消息里带的子进程输出尾）。
func designTail(s string) string {
	if len(s) > 4<<10 {
		return s[len(s)-4<<10:]
	}
	return s
}

// designBuild 跑 vite build；失败回 build_failed，消息带 stderr 末尾 4 KiB。
func (r *Runtime) designBuild(ctx context.Context, dir, id string, out *orbsock.Responder) error {
	engine := r.designEngineDir()
	env := map[string]string{"ORYN_DESIGN_DIR": dir, "ORYN_DESIGN_SANDBOX": r.designSandboxDir()}
	tail, err := r.designExec(ctx, dir, env, out, "node",
		filepath.Join(engine, "node_modules", "vite", "bin", "vite.js"), "build", "--config", filepath.Join(engine, "vite.config.mjs"))
	if err != nil {
		return &orbsock.Error{Code: orbsock.CodeBuildFailed, Message: id + " 的 vite build 失败（" + err.Error() + "），没有落版本：\n" + tail}
	}
	return nil
}

// designSealFiles 是 seal_design 的 files：src/**（不含 node_modules）与 dist/** 里与 head 不同的文件给 source（orb 内
// 绝对路径），head 有而磁盘上没有的给 remove。
func designSealFiles(dir string, src map[string][]byte, headSha map[string]string) ([]any, error) {
	dist, err := designDistFiles(dir)
	if err != nil {
		return nil, err
	}
	onDisk := make(map[string]string, len(src)+len(dist)) // 路径 → sha256
	for p, b := range src {
		onDisk[p] = designSha(b)
	}
	maps := []any{}
	for p, sum := range dist {
		onDisk[p] = sum
	}
	paths := make([]string, 0, len(onDisk))
	for p := range onDisk {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		if headSha[p] != onDisk[p] {
			maps = append(maps, map[string]any{"path": p, "source": filepath.Join(dir, filepath.FromSlash(p))})
		}
	}
	var removed []string
	for p := range headSha {
		if _, ok := onDisk[p]; !ok {
			removed = append(removed, p)
		}
	}
	slices.Sort(removed)
	for _, p := range removed {
		maps = append(maps, map[string]any{"path": p, "remove": true})
	}
	return maps, nil
}

// ---- design.status ----

// designStatus 列出本线程的工作副本。check：源码与探针配置（方向、surface）自最近一次全帧 design.check 以来都没变时
// 取那次结果；否则取最新版本的检查结果，按副本当前源码换算状态（控制面不可达时为 nil，这时也没法确认配置，不用缓存）。
func (r *Runtime) designStatus(ctx context.Context, p orbsock.DesignStatusParams) (orbsock.DesignStatusResult, error) {
	if err := needThread(p.ThreadID); err != nil {
		return orbsock.DesignStatusResult{}, err
	}
	res := orbsock.DesignStatusResult{Designs: []orbsock.DesignStatus{}}
	for _, meta := range r.designCopies(p.ThreadID) {
		dir := r.designDir(meta.ArtifactID)
		hash, err := designSourceHash(dir)
		if err != nil {
			return res, err
		}
		st := orbsock.DesignStatus{ID: meta.ArtifactID, Dir: dir, Dirty: hash != meta.SealedHash}
		if head, err := r.designHead(ctx, p.ThreadID, meta.ArtifactID); err == nil {
			st.Check = designAPICheck(head.Check, hash)
			if chk, ok := r.design.check(meta.ArtifactID, hash, designProbeConfigOf(head)); ok {
				if raw, err := json.Marshal(chk); err == nil {
					st.Check = designAPICheck(raw, hash)
				}
			}
		}
		res.Designs = append(res.Designs, st)
	}
	return res, nil
}

// designAPICheck 把 check_json 原文换成对外形状，状态取 Effective(当前源码指纹)。
func designAPICheck(raw json.RawMessage, hash string) *apigen.ArtifactCheck {
	if len(raw) == 0 {
		return nil
	}
	var c design.Check
	var out apigen.ArtifactCheck
	if json.Unmarshal(raw, &c) != nil || c.Status == "" || json.Unmarshal(raw, &out) != nil {
		return nil
	}
	out.Status = apigen.ArtifactCheckStatus(c.Effective(hash))
	if out.Issues == nil {
		out.Issues = []apigen.ArtifactCheckIssue{}
	}
	return &out
}

// ---- design.check ----

const (
	// designCheckTimeout 是 design.check 的上限（C4）。
	designCheckTimeout = 90 * time.Second
	// designCheckParallel 是同时跑的 Chromium 个数。
	designCheckParallel = 3
	// designDOMLimit 是 --dump-dom 输出的上限（与控制面 render.Job.DumpDOM 一致）。
	designDOMLimit = 4 << 20
	// 帧的缺省窗口与尺寸范围（与引擎 plugin-frames.mjs 的 DEFAULT_* / MIN_SIZE / MAX_SIZE 一致）。
	designFrameWidth, designFrameHeight    = 1280, 800
	designFrameMinSize, designFrameMaxSize = 64, 8000
)

var (
	// designFrameIDRe 是帧 id（引擎 FRAME_ID）。
	designFrameIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	// designFrameDeclRe 取 `export const frame = {…}` 的对象体；designFrameSizeRe 取其中的数字字面量宽高。
	designFrameDeclRe = regexp.MustCompile(`export\s+const\s+frame\b[^=]*=\s*\{([^}]*)\}`)
	designFrameSizeRe = regexp.MustCompile(`["']?\b(width|height)["']?\s*:\s*(\d+(?:\.\d+)?)`)
	// designChromiumPaths 是 orb 镜像里 Chromium 的位置（P5：/usr/sbin/chromium），都没有再查可信 PATH。
	designChromiumPaths = []string{"/usr/sbin/chromium", "/usr/bin/chromium"}
)

// designFrameRun 是一帧的检查结果：Err 非空表示探针没跑成；Image 是截图（base64 PNG），ShotErr 是截图失败的原因。
type designFrameRun struct {
	Issues  []design.Issue
	Err     string
	Image   string
	ShotErr string
}

// designCheck 用 orb 里的 headless Chromium 对工作副本的帧跑设计探针（plan-m29 C8），可附截图。页面来自 dev 服务
// （`/frames/<id>?__oryn_probe=1`，源码即时）；dev 服务没在跑时临时在 127.0.0.1 起静态服务提供 dist/ 与 probe.js
// （上一次构建的产物）。审美配置取自 design_head，拼进页面地址（designProbeConfig.query）；取不到 head 时按缺省（只跑
// 版面规则）照常检查并在 message 说明。没有 Chromium 或某帧没跑成时不报错，status 为 unverified 并在 message 说明。
// 全帧检查的结果连同探针配置按当时源码的 SourceHash 缓存，供 design.status 与封存使用；只缓存确实对应当前源码与当前
// 配置的结论：dev 服务上跑的，或者没有 Chromium（unverified 与内容无关）；静态 dist/ 可能是更早的构建（物化不带
// dist/），取不到 head 时配置不确定，这两种都不缓存。
func (r *Runtime) designCheck(ctx context.Context, p orbsock.DesignCheckParams) (orbsock.DesignCheckResult, error) {
	if err := needThread(p.ThreadID); err != nil {
		return orbsock.DesignCheckResult{}, err
	}
	if !designIDRe.MatchString(p.ID) {
		return orbsock.DesignCheckResult{}, orbsock.Errorf(orbsock.CodeInvalidParams, "invalid artifact id %q", p.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, designCheckTimeout)
	defer cancel()
	unlock := r.design.lock(p.ID)
	defer unlock()
	cp, err := r.designCopyFor(p.ThreadID, p.ID)
	if err != nil {
		return orbsock.DesignCheckResult{}, err
	}
	all, err := designFrameIDs(cp.Dir)
	if err != nil {
		return orbsock.DesignCheckResult{}, err
	}
	ids := all
	if len(p.Frames) > 0 {
		ids = nil
		var unknown []string
		for _, f := range p.Frames {
			switch {
			case !slices.Contains(all, f):
				unknown = append(unknown, f)
			case !slices.Contains(ids, f):
				ids = append(ids, f)
			}
		}
		if len(unknown) > 0 {
			return orbsock.DesignCheckResult{}, orbsock.Errorf(orbsock.CodeInvalidParams, "no such frame(s) in %s: %s (frames are src/frames/<id>.tsx; existing: %s)",
				p.ID, strings.Join(unknown, ", "), strings.Join(all, ", "))
		}
	}
	hash, err := designSourceHash(cp.Dir)
	if err != nil {
		return orbsock.DesignCheckResult{}, err
	}
	var cfg designProbeConfig
	head, headErr := r.designHead(ctx, p.ThreadID, p.ID)
	if headErr == nil {
		cfg = designProbeConfigOf(head)
	}

	runs := make([]designFrameRun, len(ids))
	live := false
	bin, down := r.designChromium()
	if down == "" && len(ids) > 0 {
		pageURL, isLive, stop, err := r.designCheckPages(cp, cfg)
		if err != nil {
			return orbsock.DesignCheckResult{}, err
		}
		defer stop()
		live = isLive
		tmp, err := os.MkdirTemp("", "oryn-design-check-")
		if err != nil {
			return orbsock.DesignCheckResult{}, err
		}
		defer os.RemoveAll(tmp)
		sem := make(chan struct{}, designCheckParallel)
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				runs[i] = r.designCheckFrame(ctx, bin, tmp, cp.Dir, id, pageURL, p.Screenshot)
			})
		}
		wg.Wait()
	} else if down != "" {
		for i := range runs {
			runs[i].Err = down
		}
	}

	res := orbsock.DesignCheckResult{Status: design.CheckClean, Issues: []orbsock.DesignCheckIssue{}, Images: []orbsock.DesignCheckImage{}}
	chk := design.Check{Status: design.CheckClean, Issues: []design.Issue{}, Fingerprint: hash}
	var notes []string
	unverified := 0
	for i, run := range runs {
		if run.Image != "" {
			res.Images = append(res.Images, orbsock.DesignCheckImage{Frame: ids[i], Data: run.Image})
		}
		if run.ShotErr != "" {
			notes = append(notes, "截图失败 "+ids[i]+"："+run.ShotErr)
		}
		if run.Err != "" {
			unverified++
			if down == "" {
				notes = append(notes, "未检查 "+ids[i]+"："+run.Err)
			}
			continue
		}
		for _, is := range run.Issues {
			is.Frame = ids[i]
			chk.Issues = append(chk.Issues, is)
			res.Issues = append(res.Issues, orbsock.DesignCheckIssue{Rule: is.Rule, Frame: is.Frame, Loc: is.Loc, Text: is.Text, Detail: is.Detail})
		}
	}
	switch {
	case len(chk.Issues) > 0:
		chk.Status = design.CheckIssues
	case unverified > 0 || len(ids) == 0:
		chk.Status = design.CheckUnverified
	}
	switch {
	case down != "":
		notes = append([]string{down + "，检查没有跑成。这和设计内容无关，不要重试 design_check。"}, notes...)
	case len(ids) == 0:
		notes = append(notes, "src/frames 下没有帧（每帧一个 src/frames/<id>.tsx）。")
	case !live:
		notes = append([]string{"dev 服务没在跑，检查的是上一次构建的 dist/ 产物。"}, notes...)
	}
	if headErr != nil {
		notes = append([]string{"取不到控制面的设计配置（" + headErr.Error() + "），这次没有跑审美规则，结果也不记作本回合的检查。"}, notes...)
	}
	res.Status, res.Message = chk.Status, strings.Join(notes, "\n")

	if len(p.Frames) == 0 && headErr == nil && (live || down != "") {
		if now, err := designSourceHash(cp.Dir); err == nil && now == hash {
			r.design.setCheck(p.ID, chk, cfg)
		}
	}
	return res, nil
}

// designFrameIDs 列出 src/frames/ 下的帧 id（`<id>.tsx`，_ 开头的共享外壳与不合规的文件名除外），按字节序排序。
func designFrameIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "src", "frames"))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".tsx")
		if ok && e.Type().IsRegular() && designFrameIDRe.MatchString(id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// designFrameSize 是帧的窗口尺寸：像引擎一样静态读 `export const frame = {width, height}` 的数字字面量（取当前源码，
// 不依赖上一次构建的 dist/frames.json），缺失或越界时取 1280×800。
func designFrameSize(dir, id string) (int, int) {
	w, h := designFrameWidth, designFrameHeight
	b, err := os.ReadFile(filepath.Join(dir, "src", "frames", id+".tsx"))
	if err != nil {
		return w, h
	}
	m := designFrameDeclRe.FindSubmatch(b)
	if m == nil {
		return w, h
	}
	for _, kv := range designFrameSizeRe.FindAllSubmatch(m[1], -1) {
		f, err := strconv.ParseFloat(string(kv[2]), 64)
		if err != nil || f < designFrameMinSize || f > designFrameMaxSize {
			continue
		}
		if string(kv[1]) == "width" {
			w = int(f + 0.5)
		} else {
			h = int(f + 0.5)
		}
	}
	return w, h
}

// designChromium 返回 Chromium 的绝对路径（/usr/sbin/chromium → /usr/bin/chromium → 可信 PATH 里的 chromium）；
// 找不到时第二个返回值是原因。
func (r *Runtime) designChromium() (string, string) {
	paths := designChromiumPaths
	if p := r.design.chromiumOverride(); p != "" {
		paths = []string{p}
	}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p, ""
		}
	}
	if r.design.chromiumOverride() == "" {
		if p, err := r.bins.look("chromium"); err == nil {
			return p, ""
		}
	}
	return "", "orb 里没有 Chromium（找过 " + strings.Join(paths, "、") + " 与 PATH）"
}

// designCheckPages 返回每帧的页面地址（查询串是 cfg.query()）：dev 服务在跑时用它（live=true）；否则在 127.0.0.1
// 随机端口临时起静态服务，提供 dist/ 与 sandbox 里的 probe.js（构建产物的入口以 ./probe.js 加载探针），没有
// dist/<id>.html 的帧 ok=false。stop 关掉临时服务。
func (r *Runtime) designCheckPages(cp designCopy, cfg designProbeConfig) (pageURL func(frame string) (string, bool), live bool, stop func(), err error) {
	query := "?" + cfg.query()
	if cp.Meta.Service != "" {
		if o := r.svc.snapshot(cp.Meta.Service); o.Status == apigen.OrbServiceStatusRunning && o.Port != nil {
			base := designDevURL(*o.Port)
			return func(id string) (string, bool) { return base + "frames/" + id + query, true }, true, func() {}, nil
		}
	}
	dist := filepath.Join(cp.Dir, "dist")
	probe := filepath.Join(r.designSandboxDir(), "probe.js")
	files := http.FileServer(http.Dir(dist))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, false, nil, err
	}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/probe.js" {
			http.ServeFile(w, q, probe)
			return
		}
		files.ServeHTTP(w, q)
	})}
	go func() { _ = srv.Serve(ln) }()
	base := "http://" + ln.Addr().String() + "/"
	return func(id string) (string, bool) {
		if st, err := os.Stat(filepath.Join(dist, id+".html")); err != nil || !st.Mode().IsRegular() {
			return "", false
		}
		return base + id + ".html" + query, true
	}, false, func() { _ = srv.Close() }, nil
}

// designCheckFrame 对一帧跑探针（--dump-dom），screenshot 时再截一张同尺寸的图。
func (r *Runtime) designCheckFrame(ctx context.Context, bin, tmp, dir, id string, pageURL func(string) (string, bool), screenshot bool) designFrameRun {
	var run designFrameRun
	u, ok := pageURL(id)
	if !ok {
		run.Err = "dev 服务没在跑，也没有构建产物 dist/" + id + ".html"
		return run
	}
	w, h := designFrameSize(dir, id)
	dom, err := r.designRunChromium(ctx, bin, tmp, w, h, u, "")
	if err == nil {
		run.Issues, err = design.ParseProbe(dom)
	}
	if err != nil {
		run.Err = err.Error()
	}
	if screenshot {
		shot := filepath.Join(tmp, id+".png")
		if _, err := r.designRunChromium(ctx, bin, tmp, w, h, u, shot); err != nil {
			run.ShotErr = err.Error()
		} else if b, err := os.ReadFile(shot); err != nil {
			run.ShotErr = "Chromium 没有写出截图"
		} else {
			run.Image = base64.StdEncoding.EncodeToString(b)
		}
	}
	return run
}

// designRunChromium 跑一次 headless Chromium：shot 为空时 --dump-dom 并返回 stdout（至多 designDOMLimit），否则
// --screenshot=shot。参数逐项固定（URL 由 orbd 拼出，帧 id 已校验）；不加 --no-sandbox（P5：orb 内 uid 1000 不需要）。
// 每次用自己的临时 profile（--user-data-dir、HOME），跑完删除，不碰桌面 Chromium 的 profile。
func (r *Runtime) designRunChromium(ctx context.Context, bin, tmp string, w, h int, pageURL, shot string) ([]byte, error) {
	profile, err := os.MkdirTemp(tmp, "profile-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(profile)
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--mute-audio",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-sync",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-dev-shm-usage",
		"--user-data-dir=" + profile,
		"--window-size=" + strconv.Itoa(w) + "," + strconv.Itoa(h),
		"--force-device-scale-factor=1",
		"--virtual-time-budget=5000",
		"--run-all-compositor-stages-before-draw",
	}
	if shot != "" {
		args = append(args, "--screenshot="+shot)
	} else {
		args = append(args, "--dump-dom")
	}
	args = append(args, pageURL)
	cmd := exec.CommandContext(ctx, bin, args...)
	env := r.baseEnv()
	env["HOME"], env["XDG_CONFIG_HOME"], env["XDG_CACHE_HOME"] = profile, profile, profile
	cmd.Env = envList(env)
	stdout := &designCappedBuffer{limit: designDOMLimit}
	stderr := &tailBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("chromium 没有在时限内完成（%v）", ctx.Err())
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 1<<10 {
			msg = "…" + msg[len(msg)-1<<10:]
		}
		return nil, fmt.Errorf("chromium 失败（%v）：%s", err, msg)
	}
	if stdout.over {
		return nil, fmt.Errorf("chromium 的 --dump-dom 输出超过 %d 字节", designDOMLimit)
	}
	return stdout.b.Bytes(), nil
}

// designCappedBuffer 只留前 limit 字节，多出的丢弃并记下 over。
type designCappedBuffer struct {
	b     bytes.Buffer
	limit int
	over  bool
}

func (c *designCappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.b.Len(); len(p) > room {
		c.over = true
		c.b.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return c.b.Write(p)
}

// ---- 工作副本 ----

// designCopy 是一个工作副本：Dir 是根目录，Meta 是它的 .oryn-design.json（Meta.Port 是 dev 服务端口，
// designDevURL(Meta.Port) 是本机地址）。
type designCopy struct {
	Dir  string
	Meta designMeta
}

// designCopyFor 返回本线程对 id 的工作副本；没有（或属于别的线程）时回 no_design。调用方需要串行时自己持
// r.design.lock(id)。
func (r *Runtime) designCopyFor(threadID, id string) (designCopy, error) {
	if !designIDRe.MatchString(id) {
		return designCopy{}, orbsock.Errorf(orbsock.CodeInvalidParams, "invalid artifact id %q", id)
	}
	dir := r.designDir(id)
	meta, err := readDesignMeta(dir)
	if err != nil || meta.ThreadID != threadID || meta.ArtifactID != id {
		return designCopy{}, orbsock.Errorf(orbsock.CodeNoDesign, "this thread has no working copy of %s; call design_open first", id)
	}
	return designCopy{Dir: dir, Meta: meta}, nil
}

// ---- 工具 ----

// designCopies 列出属于 threadID 的工作副本（按 artifact id 排序）。
func (r *Runtime) designCopies(threadID string) []designMeta {
	entries, err := os.ReadDir(r.designsRoot())
	if err != nil {
		return nil
	}
	var out []designMeta
	for _, e := range entries {
		if !e.IsDir() || !designIDRe.MatchString(e.Name()) {
			continue
		}
		meta, err := readDesignMeta(filepath.Join(r.designsRoot(), e.Name()))
		if err != nil || meta.ThreadID != threadID || meta.ArtifactID != e.Name() {
			continue
		}
		out = append(out, meta)
	}
	return out
}

// designCall 经 ctl artifact.call 调一个操作（files[*].source 期间登记为可经 library.fetch 取回），把结果的 text
// 按 JSON 解到 out（可为 nil）。turn 非空时带上回合。控制面的错误码与文案原样回。
func (r *Runtime) designCall(ctx context.Context, threadID string, turn *designTurn, op string, params map[string]any, out any) error {
	p := apigen.LinkCtlArtifactCallParams{ThreadID: threadID, Op: op, Params: params}
	if turn != nil && turn.id != "" {
		p.Turn = &struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}{ID: turn.id, Title: turn.title}
	}
	sources, err := artifactSources(p.Params)
	if err != nil {
		return err
	}
	defer r.stageLibraryPaths(sources)()
	raw, err := r.ctlForwardFor(ctx, apigen.MethodArtifactCall, p, artifactCallTimeout)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	var res apigen.LinkCtlArtifactCallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return orbsock.Errorf(orbsock.CodeInternal, "%s: %v", op, err)
	}
	if err := json.Unmarshal([]byte(res.Text), out); err != nil {
		return orbsock.Errorf(orbsock.CodeInternal, "%s: %v", op, err)
	}
	return nil
}

// designExec 在 dir 下跑一个子进程（环境 = baseEnv + extra），stdout / stderr 流回 out，返回 stderr 末尾 4 KiB。
func (r *Runtime) designExec(ctx context.Context, dir string, extra map[string]string, out *orbsock.Responder, name string, args ...string) (string, error) {
	env := r.baseEnv()
	for k, v := range extra {
		env[k] = v
	}
	cmd := r.bins.command(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = envList(env)
	tail := &tailBuffer{} // 只留 stderr 末尾 4 KiB
	cmd.Stdout = sockLogWriter{out: out, stream: orbsock.StreamStdout}
	cmd.Stderr = io.MultiWriter(tail, sockLogWriter{out: out, stream: orbsock.StreamStderr})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("%w (%v)", err, ctx.Err())
	}
	return tail.String(), err
}

// sockLogWriter 把子进程输出作为日志行流回控制口调用方（out 为 nil 时丢弃）。
type sockLogWriter struct {
	out    *orbsock.Responder
	stream string
}

func (w sockLogWriter) Write(p []byte) (int, error) {
	if w.out != nil {
		w.out.Log(w.stream, string(p))
	}
	return len(p), nil
}

func designLog(out *orbsock.Responder, format string, args ...any) {
	if out != nil {
		out.Log(orbsock.StreamSystem, fmt.Sprintf(format, args...))
	}
}

// designSourceFiles 读 <dir>/src 下的源码（不进任何一层 node_modules，只取普通文件），键是 `src/` 开头的相对路径。
func designSourceFiles(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := walkRegular(dir, "src", true, func(rel, abs string) error {
		b, err := os.ReadFile(abs)
		if err == nil {
			out[rel] = b
		}
		return err
	})
	return out, err
}

// designSourceHash 是副本当前源码的 design.SourceHash。
func designSourceHash(dir string) (string, error) {
	src, err := designSourceFiles(dir)
	if err != nil {
		return "", err
	}
	return design.SourceHash(src), nil
}

// designDistFiles 返回 <dir>/dist 下普通文件的 sha256（键是 `dist/` 开头的相对路径）。
func designDistFiles(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := walkRegular(dir, "dist", false, func(rel, abs string) error {
		f, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		out[rel] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	return out, err
}

// walkRegular 遍历 <dir>/<top> 下的普通文件（top 不存在时什么也不做），rel 是 `/` 分隔、以 top/ 开头的相对路径。
func walkRegular(dir, top string, skipNodeModules bool, fn func(rel, abs string) error) error {
	root := filepath.Join(dir, top)
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipNodeModules && d.Name() == "node_modules" && abs != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, abs)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), abs)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func designSha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readDesignMeta(dir string) (designMeta, error) {
	b, err := os.ReadFile(filepath.Join(dir, designMetaFile))
	if err != nil {
		return designMeta{}, err
	}
	var m designMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return designMeta{}, err
	}
	return m, nil
}

func writeDesignMeta(dir string, m designMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, designMetaFile), append(b, '\n'), 0o644)
}

// designQuote 把 s 引成单个 shell 词。
func designQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
