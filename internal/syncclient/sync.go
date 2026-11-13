// Package syncclient 是 `oryn sync` 的本地引擎（M5 计划 4.4）：经 orbd 的 /sync/prepare 取得「快照提交 + bundle」，
// 在本地干净检出上持续镜像（Mirror，退出还原）或一次性应用（Apply）。
//
// 安全原则：任何可能丢失本地数据的路径一律拒绝并报错，而不是尽力而为——
//   - 每次覆盖工作区之前（下载与导入对象之后）都按「开始时 / 上一轮应用后」记录的基线复核 HEAD、暂存区与工作区；
//   - 本地被忽略的文件与要写入的树有路径冲突（同名、或文件 / 目录互相占位）时拒绝，checkout / merge 另加
//     `--no-overwrite-ignore` 作第二道保护（`git restore` 与 `checkout -f` 没有这道保护，只能靠前者）；
//   - 镜像写出、但在本地被忽略的文件（git status 看不见）记入状态文件，内容纳入改动检查，还原时删除；
//   - 还原同样先复核，有本地改动就保留状态与文件并报错；只有显式 `Discard` 才丢弃本地改动。
package syncclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// Remote 是 orb 侧的两个 sync 接口。
type Remote interface {
	// Prepare 调 POST orbs/{id}/sync/prepare；background 为真时 orb 不可用即报 orb_not_ready（不唤醒）。
	Prepare(ctx context.Context, req apigen.OrbSyncPrepareRequest, background bool) (apigen.OrbSyncState, error)
	// Bundle 把 GET orbs/{id}/sync/bundle 的字节写入 w。
	Bundle(ctx context.Context, id string, w io.Writer) error
}

// Options 是 Mirror / Apply 的参数。
type Options struct {
	// Dir 是本地检出内的任意目录；ThreadID 决定本地 refs/oryn/sync/<key>/* 的 key；Root 是 orb 内的仓库路径（可空）。
	Dir, ThreadID, Root string
	// Interval 是镜像模式的轮询间隔。
	Interval time.Duration
	// Force：无共同提交时仍打包完整历史；Detach：Apply 总是游离检出。
	Force, Detach bool
	// Log 输出进度行（可为 nil）。
	Log func(format string, a ...any)
}

// RestoreOptions 是 Restore 的参数。
type RestoreOptions struct {
	// Discard：丢弃镜像期间的本地改动（改动的文件、新建的文件、游离 HEAD 上的新提交）后还原。
	// 本地被忽略的文件与原提交的路径冲突时仍然拒绝。
	Discard bool
}

var (
	// ErrDirty：本地检出有改动或处于合并 / 变基等中间状态。
	ErrDirty = errors.New("本地检出不干净")
	// ErrStaleState：上一次镜像的状态文件还在（先 `oryn sync --restore`）。
	ErrStaleState = errors.New("存在未还原的 sync 状态")
	// ErrLocalEdits：本地文件在同步 / 还原之前被改动；已停止且未覆盖。
	ErrLocalEdits = errors.New("检测到本地改动")
	// ErrIgnoredConflict：本地被忽略的文件与要写入的树路径冲突；已拒绝且未覆盖。
	ErrIgnoredConflict = errors.New("本地被忽略的文件与要写入的路径冲突")
	// ErrBadState：orb 返回的同步状态或 bundle 不合法。
	ErrBadState = errors.New("orb 返回的同步数据不合法")
	// ErrNotARepo：Dir 不在 git 仓库内。
	ErrNotARepo = errors.New("当前目录不在 git 仓库内")
)

// Coded 是带错误码的接口错误（HTTPRemote 的 *APIError 实现它）。
type Coded interface {
	error
	ErrorCode() string
}

// Code 返回 err 链上第一个 Coded 的错误码（没有则为空）。
func Code(err error) string {
	var c Coded
	if errors.As(err, &c) {
		return c.ErrorCode()
	}
	return ""
}

const (
	stateName    = "oryn-sync.json"
	indexName    = "oryn-sync.index"
	bundleName   = "oryn-sync.bundle"
	maxHave      = 256
	stateVersion = 2
	// maxBundle 与 orbd 侧上限一致；客户端另按声明的大小卡死。
	maxBundle = 1 << 30
	// maxListed 是错误信息里最多列出的路径数。
	maxListed = 20
)

var (
	fullShaRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	bundleIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// state 是 <git-dir>/oryn-sync.json：镜像开始前的位置（Restore 的目标）与本地基线（改动检查的依据）。
// Applied 为假时基线是开始前的本地状态；为真时是最近一次应用后的状态。Pending 非空表示正在应用该快照
// （进程在应用中途退出时留下，此时无法区分本地改动，只能显式 Discard）。
type state struct {
	Version   int    `json:"version"`
	ThreadID  string `json:"threadId"`
	OrigRef   string `json:"origRef"`
	OrigSha   string `json:"origSha"`
	StartedAt int64  `json:"startedAt"`
	PID       int    `json:"pid"`
	Applied   bool   `json:"applied"`
	// Head 是基线的 HEAD 提交；Snapshot 是最近一次应用的快照。
	Head     string `json:"head"`
	Snapshot string `json:"snapshot,omitempty"`
	// IndexTree / Tree 是基线的暂存区树与工作区树（工作区树含 Owned）。
	IndexTree string `json:"indexTree"`
	Tree      string `json:"tree"`
	// Owned 是镜像写出、但在本地被忽略的文件（相对仓库根，/ 分隔）。
	Owned   []string `json:"owned,omitempty"`
	Pending string   `json:"pending,omitempty"`
}

// repo 是一个本地检出。
type repo struct {
	top, gitDir string
	// icase 是 core.ignorecase：路径冲突按大小写不敏感比较。
	icase bool
}

// git 在检出里执行 git：不跑钩子、不提示游离 HEAD、不递归子模块、不弹认证提示；返回去掉首尾空白的 stdout。
func (g *repo) git(env []string, args ...string) (string, error) {
	return runGit(g.top, env, args...)
}

func runGit(dir string, env []string, args ...string) (string, error) {
	out, err := runGitRaw(dir, env, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// runGitRaw 同 runGit，但 stdout 原样返回（-z 输出不能去空白），stdin 非空时作为标准输入。
func runGitRaw(dir string, env []string, stdin []byte, args ...string) ([]byte, error) {
	full := append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "advice.detachedHead=false",
		"-c", "core.quotepath=off", "-c", "submodule.recurse=false"}, args...)
	// 本地 git 调用不随 ctx 取消：中途杀掉 checkout / reset 会留下半截工作区与 index.lock
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), append([]string{"GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}, env...)...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// gitZ 执行带 -z 的 git 并按 NUL 切分输出。
func (g *repo) gitZ(args ...string) ([]string, error) {
	out, err := runGitRaw(g.top, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	var list []string
	for p := range strings.SplitSeq(string(out), "\x00") {
		if p != "" {
			list = append(list, p)
		}
	}
	return list, nil
}

// ok 执行 git 并只关心是否成功（`rev-parse --verify`、`merge-base --is-ancestor` 等）。
func (g *repo) ok(args ...string) bool {
	_, err := g.git(nil, args...)
	return err == nil
}

func openRepo(dir string) (*repo, error) {
	top, err := runGit(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return nil, fmt.Errorf("%w: %s", ErrNotARepo, dir)
	}
	gitDir, err := runGit(top, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	ic, _ := runGit(top, nil, "config", "--type=bool", "--get", "core.ignorecase")
	return &repo{top: top, gitDir: gitDir, icase: ic == "true"}, nil
}

func (g *repo) path(name string) string { return filepath.Join(g.gitDir, name) }

// midOp 返回进行中的合并 / 拣选 / 还原 / 二分 / 变基的标记名（没有则为空）。
func (g *repo) midOp() string {
	for _, n := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(g.path(n)); err == nil {
			return n
		}
	}
	return ""
}

// precheck：没有残留状态文件、工作区干净、不在合并 / 拣选 / 还原 / 二分 / 变基中间状态。
func (g *repo) precheck() error {
	if _, err := os.Stat(g.path(stateName)); err == nil {
		return fmt.Errorf("%w（%s）：先运行 `oryn sync --restore`", ErrStaleState, g.path(stateName))
	}
	if op := g.midOp(); op != "" {
		return fmt.Errorf("%w：仓库处于未完成的操作中（%s）", ErrDirty, op)
	}
	st, err := g.git(nil, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if st != "" {
		return fmt.Errorf("%w：有未提交的改动或未跟踪文件\n%s", ErrDirty, st)
	}
	return nil
}

// baseline 记录当前本地状态（HEAD、分支、暂存区树、工作区树）作为改动检查的基线。
func (g *repo) baseline() (*state, error) {
	head, err := g.git(nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return nil, errors.New("本地仓库还没有提交")
	}
	ref, _ := g.git(nil, "symbolic-ref", "--quiet", "HEAD")
	t, err := g.localTrees(nil)
	if err != nil {
		return nil, err
	}
	return &state{Version: stateVersion, OrigRef: ref, OrigSha: head, Head: head, IndexTree: t.index, Tree: t.work}, nil
}

// refKey 是线程 id 的末 12 位（本地 refs/oryn/sync/<key>/*）。
func refKey(threadID string) string {
	if len(threadID) > 12 {
		return threadID[len(threadID)-12:]
	}
	return threadID
}

// have 是本地已有的提交：HEAD + 最近提交的 200 个分支 / 远端分支 + extra，去重，至多 256 个。
func (g *repo) have(extra ...string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) == 40 && !seen[s] && len(out) < maxHave {
			seen[s] = true
			out = append(out, s)
		}
	}
	if h, err := g.git(nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		add(h)
	}
	for _, e := range extra {
		add(e)
	}
	if refs, err := g.git(nil, "for-each-ref", "--sort=-committerdate", "--count=200", "--format=%(objectname)", "refs/heads", "refs/remotes"); err == nil {
		for l := range strings.SplitSeq(refs, "\n") {
			add(l)
		}
	}
	return out
}

// prepare 调用 Remote.Prepare，把 sync_no_common 包装成带提示的错误。
func (g *repo) prepare(ctx context.Context, r Remote, o Options, background bool, extra ...string) (apigen.OrbSyncState, error) {
	req := apigen.OrbSyncPrepareRequest{Have: g.have(extra...)}
	if o.Root != "" {
		req.Root = &o.Root
	}
	if o.Force {
		req.Force = &o.Force
	}
	st, err := r.Prepare(ctx, req, background)
	if err != nil && Code(err) == "sync_no_common" {
		return st, fmt.Errorf("%w\n提示：先 `git fetch origin`，或确认当前目录是这个项目的检出；`--force` 跳过", err)
	}
	return st, err
}

// validate 校验 orb 返回的状态形态：head / snapshot 是完整 sha、bundle 的 id 与大小合法、分支名是合法的分支。
func (g *repo) validate(st apigen.OrbSyncState) error {
	if !fullShaRe.MatchString(st.Head) || !fullShaRe.MatchString(st.Snapshot) {
		return fmt.Errorf("%w：head / snapshot 必须是 40 位十六进制提交 sha（%q / %q）", ErrBadState, st.Head, st.Snapshot)
	}
	if b := st.Bundle; b != nil {
		if !bundleIDRe.MatchString(b.ID) {
			return fmt.Errorf("%w：bundle id %q", ErrBadState, b.ID)
		}
		if b.Size <= 0 || b.Size > maxBundle {
			return fmt.Errorf("%w：bundle 大小 %d 超出范围（1–%d 字节）", ErrBadState, b.Size, maxBundle)
		}
	}
	if st.Branch != nil {
		b := *st.Branch
		// 不用 `check-ref-format --branch`：它会把 @{-1} 之类展开成别的分支
		if b == "" || b == "HEAD" || strings.HasPrefix(b, "-") || !g.ok("check-ref-format", "refs/heads/"+b) {
			return fmt.Errorf("%w：分支名 %q", ErrBadState, b)
		}
	}
	return nil
}

// limitWriter 最多写入 n 字节，超出即报错（bundle 不得超过 orb 声明的大小）。
type limitWriter struct {
	w    io.Writer
	n    int64
	size int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		return 0, fmt.Errorf("%w：bundle 超过声明的 %d 字节", ErrBadState, l.size)
	}
	n, err := l.w.Write(p)
	l.n -= int64(n)
	return n, err
}

// fetch 校验状态、下载 bundle（不超过声明的大小）并带 fsck 取进 refs/oryn/sync/<key>/*，
// 最后确认 head 与快照提交都在本地、快照是 head 的直接子提交。
func (g *repo) fetch(ctx context.Context, r Remote, key string, st apigen.OrbSyncState) error {
	if err := g.validate(st); err != nil {
		return err
	}
	if st.Bundle != nil {
		file := g.path(bundleName)
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer os.Remove(file)
		lw := &limitWriter{w: f, n: st.Bundle.Size, size: st.Bundle.Size}
		err = r.Bundle(ctx, st.Bundle.ID, lw)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil && lw.n != 0 {
			err = fmt.Errorf("%w：bundle 只收到 %d / %d 字节", ErrBadState, st.Bundle.Size-lw.n, st.Bundle.Size)
		}
		if err != nil {
			return fmt.Errorf("下载 bundle: %w", err)
		}
		if _, err := g.git(nil, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
			"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--quiet",
			file, "+refs/oryn/sync/*:refs/oryn/sync/"+key+"/*"); err != nil {
			return err
		}
	}
	for _, c := range []string{st.Head, st.Snapshot} {
		if !g.ok("cat-file", "-e", c+"^{commit}") {
			return fmt.Errorf("本地缺少 orb 的提交 %s", short(c))
		}
	}
	if st.Snapshot != st.Head {
		if parents, err := g.git(nil, "rev-parse", st.Snapshot+"^@"); err != nil || parents != st.Head {
			return fmt.Errorf("%w：快照 %s 不是 head %s 的直接子提交", ErrBadState, short(st.Snapshot), short(st.Head))
		}
	}
	return nil
}

// dropRefs 删除 refs/oryn/sync/<key>/*。
func (g *repo) dropRefs(key string) error {
	refs, err := g.git(nil, "for-each-ref", "--format=%(refname)", "refs/oryn/sync/"+key+"/")
	if err != nil {
		return err
	}
	for ref := range strings.SplitSeq(refs, "\n") {
		if ref == "" {
			continue
		}
		if _, err := g.git(nil, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}

// trees 是本地暂存区与工作区各自写成的树。
type trees struct{ index, work string }

// localTrees 用临时 index（真实 index 的副本）把暂存区与工作区写成树，不改真实 index；
// owned 中仍存在的路径（被本地忽略的镜像文件）强制计入工作区树。
func (g *repo) localTrees(owned []string) (trees, error) {
	var t trees
	idx := g.path(indexName)
	defer os.Remove(idx)
	env := []string{"GIT_INDEX_FILE=" + idx}
	if b, err := os.ReadFile(g.path("index")); err == nil {
		if err := os.WriteFile(idx, b, 0o600); err != nil {
			return t, err
		}
	} else if _, err := g.git(env, "read-tree", "HEAD"); err != nil {
		return t, err
	}
	var err error
	if t.index, err = g.git(env, "write-tree"); err != nil {
		return t, err
	}
	if _, err := g.git(env, "add", "-A", "--", "."); err != nil {
		return t, err
	}
	var present []byte
	for _, p := range owned {
		if _, err := os.Lstat(filepath.Join(g.top, filepath.FromSlash(p))); err == nil {
			present = append(append(present, p...), 0)
		}
	}
	if len(present) > 0 {
		if _, err := runGitRaw(g.top, append(env, "GIT_LITERAL_PATHSPECS=1"), present,
			"add", "-f", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return t, err
		}
	}
	t.work, err = g.git(env, "write-tree")
	return t, err
}

// verify 按基线复核本地状态：无进行中的操作、HEAD 与分支未变、暂存区树与工作区树未变；否则返回 ErrLocalEdits。
func (g *repo) verify(s *state) error {
	if op := g.midOp(); op != "" {
		return fmt.Errorf("%w：仓库处于未完成的操作中（%s）", ErrLocalEdits, op)
	}
	head, _ := g.git(nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	ref, _ := g.git(nil, "symbolic-ref", "--quiet", "HEAD")
	wantRef := s.OrigRef
	if s.Applied {
		wantRef = ""
	}
	if head != s.Head || ref != wantRef {
		return fmt.Errorf("%w：HEAD 已变动（%s → %s）", ErrLocalEdits, where(wantRef, s.Head), where(ref, head))
	}
	t, err := g.localTrees(s.Owned)
	if err != nil {
		return fmt.Errorf("%w：无法确认本地状态：%v", ErrLocalEdits, err)
	}
	if t.work != s.Tree {
		return fmt.Errorf("%w：%s", ErrLocalEdits, listPaths("工作区有改动", g.changed(s.Tree, t.work)))
	}
	if t.index != s.IndexTree {
		return fmt.Errorf("%w：%s", ErrLocalEdits, listPaths("暂存区有改动", g.changed(s.IndexTree, t.index)))
	}
	return nil
}

func where(ref, sha string) string {
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return b + "@" + short(sha)
	}
	return short(sha)
}

// changed 列出两棵树之间不同的路径。
func (g *repo) changed(a, b string) []string {
	paths, _ := g.gitZ("diff-tree", "-r", "-z", "--name-only", "--no-renames", a, b)
	return paths
}

func listPaths(title string, paths []string) string {
	var sb strings.Builder
	sb.WriteString(title)
	for i, p := range paths {
		if i == maxListed {
			fmt.Fprintf(&sb, "\n  …另有 %d 个", len(paths)-maxListed)
			break
		}
		sb.WriteString("\n  " + p)
	}
	return sb.String()
}

// ignored 列出本地未跟踪且被忽略的文件（相对仓库根）。
func (g *repo) ignored() ([]string, error) {
	list, err := g.gitZ("ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	for i, p := range list {
		// 未跟踪的嵌套仓库以 "dir/" 出现
		list[i] = strings.TrimSuffix(p, "/")
	}
	return list, nil
}

func (g *repo) key(p string) string {
	if g.icase {
		return strings.ToLower(p)
	}
	return p
}

// treeFiles 返回提交树里的全部文件路径（按 key 归一）。
func (g *repo) treeFiles(commit string) (map[string]bool, error) {
	list, err := g.gitZ("ls-tree", "-r", "-z", "--name-only", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	files := make(map[string]bool, len(list))
	for _, p := range list {
		files[g.key(p)] = true
	}
	return files, nil
}

// conflicts 返回 ignored（除去 skip）中与 targets 任一提交树冲突的路径：同名，或一方是另一方的上级目录。
func (g *repo) conflicts(ignored, skip []string, targets ...string) ([]string, error) {
	files, dirs := map[string]bool{}, map[string]bool{}
	for _, c := range targets {
		tf, err := g.treeFiles(c)
		if err != nil {
			return nil, err
		}
		for p := range tf {
			files[p] = true
			for d := path.Dir(p); d != "." && !dirs[d]; d = path.Dir(d) {
				dirs[d] = true
			}
		}
	}
	var out []string
	for _, p := range ignored {
		if slices.Contains(skip, p) {
			continue
		}
		k := g.key(p)
		hit := files[k] || dirs[k]
		for d := path.Dir(k); !hit && d != "."; d = path.Dir(d) {
			hit = files[d]
		}
		if hit {
			out = append(out, p)
		}
	}
	return out, nil
}

// checkIgnored：本地被忽略的文件（除去 skip）与 targets 冲突时返回 ErrIgnoredConflict。
func (g *repo) checkIgnored(skip []string, targets ...string) error {
	ign, err := g.ignored()
	if err != nil {
		return err
	}
	hits, err := g.conflicts(ign, skip, targets...)
	if err != nil {
		return err
	}
	if len(hits) > 0 {
		return fmt.Errorf("%w，已拒绝，本地文件未被覆盖；先移走或改名这些文件再重试：%s",
			ErrIgnoredConflict, listPaths("", hits))
	}
	return nil
}

// ownedIn 返回本地未跟踪、被忽略且出现在 commit 树里的文件：这些文件由镜像写出，git status 看不见。
func (g *repo) ownedIn(commit string) ([]string, error) {
	ign, err := g.ignored()
	if err != nil {
		return nil, err
	}
	files, err := g.treeFiles(commit)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range ign {
		if files[g.key(p)] {
			out = append(out, p)
		}
	}
	return out, nil
}

// removeOwned 删除镜像写出的被忽略文件；路径必须在仓库内且不穿过符号链接，目录不删。
func (g *repo) removeOwned(paths []string) error {
	for _, p := range paths {
		rel := filepath.FromSlash(p)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("状态文件里的路径不合法：%q", p)
		}
		missing := false
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			fi, err := os.Lstat(filepath.Join(g.top, filepath.FromSlash(d)))
			if errors.Is(err, os.ErrNotExist) {
				missing = true
				break
			}
			if err != nil {
				return err
			}
			if !fi.IsDir() {
				return fmt.Errorf("拒绝删除 %s：上级 %s 不是目录", p, d)
			}
		}
		if missing {
			continue
		}
		full := filepath.Join(g.top, rel)
		fi, err := os.Lstat(full)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("拒绝删除 %s：已变成目录", p)
		}
		if err := os.Remove(full); err != nil {
			return err
		}
	}
	return nil
}

func (g *repo) writeState(s *state) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := g.path(stateName + ".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, g.path(stateName))
}

func (g *repo) readState() (*state, error) {
	b, err := os.ReadFile(g.path(stateName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("没有需要还原的 sync 状态")
		}
		return nil, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil || s.OrigSha == "" {
		return nil, fmt.Errorf("状态文件 %s 损坏", g.path(stateName))
	}
	return &s, nil
}

// dropState 删除 refs/oryn/sync/<key>/*、临时文件与状态文件（本地检出从未被改动时使用）。
func (g *repo) dropState(key string) error {
	if err := g.dropRefs(key); err != nil {
		return err
	}
	_ = os.Remove(g.path(indexName))
	_ = os.Remove(g.path(bundleName))
	return os.Remove(g.path(stateName))
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func logf(o Options, format string, a ...any) {
	if o.Log != nil {
		o.Log(format, a...)
	}
}

// fatal 报告镜像循环里不可重试的错误。
func fatal(err error) bool {
	return errors.Is(err, ErrLocalEdits) || errors.Is(err, ErrIgnoredConflict) || errors.Is(err, ErrBadState)
}

// ---- 持续镜像 ----

// Mirror 持续把 orb 的提交与工作区镜像到本地检出（HEAD 游离在 orb 的 HEAD 上，工作区 = orb 的工作区），
// 直到 ctx 取消后还原并返回 nil。本地被改动则返回 ErrLocalEdits，不还原、不覆盖；还原前同样复核。
func Mirror(ctx context.Context, r Remote, o Options) error {
	g, err := openRepo(o.Dir)
	if err != nil {
		return err
	}
	if err := g.precheck(); err != nil {
		return err
	}
	s, err := g.baseline()
	if err != nil {
		return err
	}
	s.ThreadID, s.StartedAt, s.PID = o.ThreadID, time.Now().UnixMilli(), os.Getpid()
	if err := g.writeState(s); err != nil {
		return err
	}
	m := &mirror{g: g, r: r, o: o, key: refKey(o.ThreadID), s: s}
	stop := func() error { return Restore(g.top, RestoreOptions{}) }
	waiting, failing := false, false
	for {
		st, err := g.prepare(ctx, r, o, s.Applied, s.Head, s.Snapshot)
		if ctx.Err() != nil {
			return stop()
		}
		switch {
		case err != nil && !s.Applied:
			// 尚未改动本地：撤掉状态即可
			_ = g.dropState(m.key)
			return err
		case err != nil && Code(err) == "orb_not_ready":
			if !waiting {
				logf(o, "orb 未运行，等待中…")
				waiting = true
			}
		case err != nil:
			if !failing {
				logf(o, "同步失败（稍后重试）：%v", err)
				failing = true
			}
		default:
			waiting, failing = false, false
			if st.Head != s.Head || st.Snapshot != s.Snapshot {
				err := m.round(ctx, st)
				if ctx.Err() != nil {
					return stop()
				}
				switch {
				case err == nil:
				case s.Pending != "":
					return fmt.Errorf("应用中途失败，本地处于半应用状态：%w\n确认不需要保留本地改动后运行 `oryn sync --restore --discard` 还原", err)
				case !s.Applied:
					_ = g.dropState(m.key)
					return err
				case fatal(err):
					return err
				default:
					logf(o, "同步失败（稍后重试）：%v", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return stop()
		case <-time.After(o.Interval):
		}
	}
}

// mirror 是一次持续镜像的进度；s 与状态文件保持一致。
type mirror struct {
	g   *repo
	r   Remote
	o   Options
	key string
	s   *state
}

// round 应用一个新状态：下载与导入 → 按基线复核本地 → 检查被忽略文件冲突 → 标记 Pending → 覆盖 → 记录新基线。
// 本地改动保护比较的是「上一轮应用后本地算出的树」而不是快照的树：本机的全局忽略规则与 orb 不同也不会误报。
func (m *mirror) round(ctx context.Context, st apigen.OrbSyncState) error {
	g, s := m.g, m.s
	if err := g.fetch(ctx, m.r, m.key, st); err != nil {
		return err
	}
	if err := g.verify(s); err != nil {
		if !s.Applied {
			return fmt.Errorf("%w\n未应用任何内容，本地文件保持原样", err)
		}
		return fmt.Errorf("%w\n已停止，本地文件未被覆盖；保存好需要的改动后运行 `oryn sync --restore --discard` 还原（会丢弃这些改动）", err)
	}
	if err := g.checkIgnored(s.Owned, st.Snapshot); err != nil {
		return err
	}
	s.Pending = st.Snapshot
	if err := g.writeState(s); err != nil {
		s.Pending = ""
		return err
	}
	if err := g.mirrorApply(s.Owned, st); err != nil {
		return err
	}
	owned, err := g.ownedIn(st.Snapshot)
	if err != nil {
		return err
	}
	t, err := g.localTrees(owned)
	if err != nil {
		return err
	}
	s.Applied, s.Head, s.Snapshot, s.Owned, s.IndexTree, s.Tree, s.Pending = true, st.Head, st.Snapshot, owned, t.index, t.work, ""
	if err := g.writeState(s); err != nil {
		return err
	}
	g.logApplied(m.o, st, s.OrigSha)
	return nil
}

// mirrorApply：删掉上一轮写出的被忽略文件 → 清掉未跟踪文件并把已跟踪文件退回 HEAD（已复核，都是上一轮镜像的内容）→
// 非强制游离检出快照（--no-overwrite-ignore）→ index 退回 orb 的 HEAD。
// 结果：HEAD = orb 的提交，未提交改动为未暂存、新文件为未跟踪。
func (g *repo) mirrorApply(owned []string, st apigen.OrbSyncState) error {
	if err := g.removeOwned(owned); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"clean", "-fdq"},
		{"reset", "-q", "--hard"},
		{"checkout", "-q", "--no-overwrite-ignore", "--detach", st.Snapshot},
		{"reset", "-q", st.Head},
	} {
		if _, err := g.git(nil, args...); err != nil {
			return err
		}
	}
	return nil
}

// logApplied 打一行 `HH:MM:SS <短 sha> <分支> +<领先原提交数>[ +未提交改动]`。
func (g *repo) logApplied(o Options, st apigen.OrbSyncState, origSha string) {
	branch := "(detached)"
	if st.Branch != nil {
		branch = *st.Branch
	}
	line := fmt.Sprintf("%s %s %s", time.Now().Format("15:04:05"), short(st.Head), branch)
	if n, err := g.git(nil, "rev-list", "--count", origSha+".."+st.Head); err == nil {
		if c, _ := strconv.Atoi(n); c > 0 {
			line += " +" + n
		}
	}
	if st.Dirty {
		line += " +未提交改动"
	}
	logf(o, "%s", line)
}

// Restore 按状态文件把检出还原到镜像开始前的分支与提交，并删除 refs/oryn/sync/<key>/* 与状态文件。
// 本地从未被镜像改动时只删除状态；否则先按基线复核，有本地改动时返回 ErrLocalEdits 并保留状态与文件，
// 除非 o.Discard。本地被忽略的文件与原提交冲突时总是拒绝。
func Restore(dir string, o RestoreOptions) error {
	g, err := openRepo(dir)
	if err != nil {
		return err
	}
	s, err := g.readState()
	if err != nil {
		return err
	}
	key := refKey(s.ThreadID)
	legacy := s.Version < stateVersion
	if !legacy && !s.Applied && s.Pending == "" {
		return g.dropState(key)
	}
	const discardHint = "\n未还原，本地文件保持原样；保存好需要的改动后运行 `oryn sync --restore --discard`（会丢弃这些改动）"
	owned := slices.Clone(s.Owned)
	switch {
	case o.Discard && s.Pending != "":
		// 中断前已确认这些路径不与本地被忽略的文件冲突：此刻出现在那里的被忽略文件来自镜像
		more, err := g.ownedIn(s.Pending)
		if err != nil {
			return err
		}
		for _, p := range more {
			if !slices.Contains(owned, p) {
				owned = append(owned, p)
			}
		}
	case o.Discard:
	case legacy:
		return fmt.Errorf("%w：状态文件来自旧版本，无法确认本地改动%s", ErrLocalEdits, discardHint)
	case s.Pending != "":
		return fmt.Errorf("%w：上一次应用在中途中断，无法区分本地改动%s", ErrLocalEdits, discardHint)
	default:
		if err := g.verify(s); err != nil {
			return fmt.Errorf("%w%s", err, discardHint)
		}
	}
	target, checkout := s.OrigSha, []string{"checkout", "-q", "--no-overwrite-ignore", "--detach", s.OrigSha}
	if br, ok := strings.CutPrefix(s.OrigRef, "refs/heads/"); ok {
		tip, err := g.git(nil, "rev-parse", "--verify", "--quiet", s.OrigRef+"^{commit}")
		if err != nil {
			return fmt.Errorf("原分支 %s 已不存在，无法还原", br)
		}
		target, checkout = tip, []string{"checkout", "-q", "--no-overwrite-ignore", br, "--"}
	}
	// 先查冲突再删镜像文件：拒绝时本地保持原样，复核仍能通过
	if err := g.checkIgnored(owned, target); err != nil {
		return err
	}
	if err := g.removeOwned(owned); err != nil {
		return err
	}
	for _, args := range [][]string{{"clean", "-fdq"}, {"reset", "-q", "--hard"}, checkout} {
		if _, err := g.git(nil, args...); err != nil {
			return err
		}
	}
	return g.dropState(key)
}

// ---- 一次性应用 ----

// Apply 把 orb 的提交与未提交改动一次性落到本地：挂到同名分支（快进），不能快进或被别的 worktree 占用时游离检出；
// 未提交改动以未暂存 / 未跟踪的形式出现在工作区。不写状态文件。
func Apply(ctx context.Context, r Remote, o Options) error {
	g, err := openRepo(o.Dir)
	if err != nil {
		return err
	}
	if err := g.precheck(); err != nil {
		return err
	}
	base, err := g.baseline()
	if err != nil {
		return err
	}
	st, err := g.prepare(ctx, r, o, false)
	if err != nil {
		return err
	}
	key := refKey(o.ThreadID)
	defer func() { _ = g.dropRefs(key) }()
	if err := g.fetch(ctx, r, key, st); err != nil {
		return err
	}
	if err := g.verify(base); err != nil {
		return fmt.Errorf("%w\n未应用任何内容，本地文件保持原样", err)
	}
	targets := []string{st.Head}
	if st.Snapshot != st.Head {
		targets = append(targets, st.Snapshot)
	}
	if err := g.checkIgnored(nil, targets...); err != nil {
		return err
	}
	branch := ""
	if !o.Detach && st.Branch != nil {
		branch = *st.Branch
	}
	switch {
	case branch == "":
		_, err = g.git(nil, "checkout", "-q", "--no-overwrite-ignore", "--detach", st.Head)
	case !g.ok("rev-parse", "--verify", "--quiet", "refs/heads/"+branch):
		_, err = g.git(nil, "checkout", "-q", "--no-overwrite-ignore", "-b", branch, st.Head)
	case g.occupied(branch):
		logf(o, "警告：分支 %s 已被另一个 worktree 检出，改为游离检出", branch)
		branch = ""
		_, err = g.git(nil, "checkout", "-q", "--no-overwrite-ignore", "--detach", st.Head)
	case !g.ok("merge-base", "--is-ancestor", "refs/heads/"+branch, st.Head):
		logf(o, "警告：本地分支 %s 与 orb 的提交已分叉，改为游离检出", branch)
		branch = ""
		_, err = g.git(nil, "checkout", "-q", "--no-overwrite-ignore", "--detach", st.Head)
	default:
		if _, err = g.git(nil, "checkout", "-q", "--no-overwrite-ignore", branch, "--"); err == nil {
			_, err = g.git(nil, "merge", "-q", "--ff-only", "--no-overwrite-ignore", st.Head)
		}
	}
	if err != nil {
		return err
	}
	if st.Snapshot != st.Head {
		// restore 没有 --no-overwrite-ignore：靠上面的冲突检查兜底
		if _, err := g.git(nil, "restore", "-q", "--source="+st.Snapshot, "--staged", "--worktree", "--", ":/"); err != nil {
			return err
		}
		if _, err := g.git(nil, "reset", "-q"); err != nil {
			return err
		}
		if hidden, err := g.ownedIn(st.Snapshot); err == nil && len(hidden) > 0 {
			logf(o, "警告：以下 %d 个文件来自 orb，但被本地忽略规则忽略，git status 不会显示：%s", len(hidden), listPaths("", hidden))
		}
	}
	where := "游离 HEAD"
	if branch != "" {
		where = "分支 " + branch
	}
	extra := "，无未提交改动"
	if st.Dirty {
		extra = "，含未提交改动"
	}
	logf(o, "已应用到%s（%s）%s", where, short(st.Head), extra)
	return nil
}

// occupied 报告 branch 是否被当前检出以外的 worktree 检出。
func (g *repo) occupied(branch string) bool {
	out, err := g.git(nil, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	self, _ := filepath.EvalSymlinks(g.top)
	var wt string
	for line := range strings.SplitSeq(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			wt = p
			continue
		}
		if line == "branch refs/heads/"+branch {
			if real, _ := filepath.EvalSymlinks(wt); real != self {
				return true
			}
		}
	}
	return false
}
