package orbdrt

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// Runner 的项目层（M21）：一台 runner 同时服务多个项目，项目层随 agent.start 按线程下发，落在按内容寻址的
// `$HOME/.oryn/project-config/<projectId>-<内容 sha256 前 12 位>/.omp/`。同一版本只写一次（先写临时目录再 rename，
// 并发线程读不到写了一半的文件），不同项目、同项目不同版本互不覆盖；每次 agent.start 后删掉同一项目下没有运行中
// agent 引用的旧版本。orb 仍是 `$HOME/.oryn/project-config/.omp` + omphome.apply（projectomp.go）。

// runnerProjectIDRe 是可作目录名前缀的项目 id（控制面的 prj_…；不含路径分隔符与 `.`）。
var runnerProjectIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// runnerProjectHashLen 是目录名里内容摘要的十六进制位数。
const runnerProjectHashLen = 12

// runnerProjectDir 确保项目 pid 的这一版项目层已落盘并返回其根目录（`<root>/.omp/` 为项目层），同时把它记为
// 「正在启动」，清理不会删它；调用方在 agent 启动（或失败）之后调用 release。
func (r *Runtime) runnerProjectDir(pid string, cfg *apigen.LinkCtlProjectConfig) (dir string, release func(), err error) {
	if !runnerProjectIDRe.MatchString(pid) {
		return "", nil, ctlErr(link.CodeInvalidParams, "invalid projectId %q", pid)
	}
	parent := r.projectConfigRoot()
	// 先按占位根校验与解码（路径规则与 orb 写项目层相同），算出内容摘要才知道真正的目录名。
	items, err := projectItems(filepath.Join(parent, "_", projectOmpDir), cfg.Files)
	if err != nil {
		return "", nil, ctlErr(link.CodeInvalidParams, "projectConfig: %v", err)
	}
	slices.SortFunc(items, func(a, b projectItem) int { return strings.Compare(a.path, b.path) })
	h := sha256.New()
	for _, it := range items {
		rel, _ := filepath.Rel(filepath.Join(parent, "_", projectOmpDir), it.path)
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(it.data)))
		fmt.Fprintf(h, "%s\x00%o\x00", filepath.ToSlash(rel), it.mode)
		h.Write(n[:])
		h.Write(it.data)
	}
	name := pid + "-" + hex.EncodeToString(h.Sum(nil))[:runnerProjectHashLen]
	dir = filepath.Join(parent, name)

	r.projMu.Lock()
	if r.projPending == nil {
		r.projPending = map[string]int{}
	}
	r.projPending[dir]++
	r.projMu.Unlock()
	release = func() {
		r.projMu.Lock()
		if r.projPending[dir]--; r.projPending[dir] <= 0 {
			delete(r.projPending, dir)
		}
		r.projMu.Unlock()
	}
	if err := materializeProject(parent, dir, pid, items); err != nil {
		release()
		return "", nil, ctlErr("failed", "project config: %v", err)
	}
	return dir, release, nil
}

// materializeProject 把 items（以占位根 `<parent>/_/.omp` 解析的路径）写成 dir/.omp：dir 已是真实目录就复用；
// 否则在 parent 下的临时目录写好再整体 rename（并发写同一版本时，rename 失败的一方丢弃自己的临时目录）。
func materializeProject(parent, dir, pid string, items []projectItem) error {
	if st, err := os.Lstat(dir); err == nil {
		if st.IsDir() {
			return nil
		}
		if err := os.Remove(dir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := realDir(parent); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".tmp-"+pid+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // rename 成功后 tmp 已不存在
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	placeholder := filepath.Join(parent, "_")
	if err := os.MkdirAll(filepath.Join(tmp, projectOmpDir), 0o755); err != nil {
		return err
	}
	for _, it := range items {
		rel, err := filepath.Rel(placeholder, it.path)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(filepath.Join(tmp, rel), it.data, it.mode); err != nil {
			return fmt.Errorf("%s: %w", it.rel, err)
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		if st, serr := os.Lstat(dir); serr == nil && st.IsDir() {
			return nil // 另一个 agent.start 先写好了同一版本
		}
		return err
	}
	return nil
}

// projectDirsInUse 返回运行中（监督器未结束）的 agent 所用的项目层目录。
func (r *Runtime) projectDirsInUse() map[string]bool {
	out := map[string]bool{}
	for _, t := range r.threadList() {
		t.mu.Lock()
		if s := t.sup; s != nil && !s.finished() && s.spec.projectDir != "" {
			out[s.spec.projectDir] = true
		}
		t.mu.Unlock()
	}
	return out
}

// pruneRunnerProject 删除项目 pid 的、没有运行中 agent 引用也不在启动途中的旧版本目录；尽力而为。
func (r *Runtime) pruneRunnerProject(pid string) {
	parent := r.projectConfigRoot()
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	r.projMu.Lock()
	defer r.projMu.Unlock()
	inUse := r.projectDirsInUse()
	for _, e := range entries {
		name := e.Name()
		if !runnerProjectVersion(name, pid) {
			continue
		}
		dir := filepath.Join(parent, name)
		if inUse[dir] || r.projPending[dir] > 0 {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			r.log.Warn("remove old project config failed", "dir", dir, "err", err)
		}
	}
}

// runnerProjectVersion 报告目录名 name 是否恰是项目 pid 的一个版本（<pid>-<12 位十六进制>）；别的项目即使以
// 「pid-」开头也不匹配。
func runnerProjectVersion(name, pid string) bool {
	h, ok := strings.CutPrefix(name, pid+"-")
	if !ok || len(h) != runnerProjectHashLen {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && strings.ToLower(h) == h
}
