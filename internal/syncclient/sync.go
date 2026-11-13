// Package syncclient 是 `oryn sync` 的本地引擎（M5 计划 4.4）：经 orbd 的 /sync/prepare 取得「快照提交 + bundle」，
// 在本地干净检出上持续镜像（Mirror，退出还原）或一次性应用（Apply）。
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
	"path/filepath"
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

var (
	// ErrDirty：本地检出有改动或处于合并 / 变基等中间状态。
	ErrDirty = errors.New("本地检出不干净")
	// ErrStaleState：上一次镜像的状态文件还在（先 `oryn sync --restore`）。
	ErrStaleState = errors.New("存在未还原的 sync 状态")
	// ErrLocalEdits：镜像期间本地文件被改动；已停止且未覆盖（状态文件保留）。
	ErrLocalEdits = errors.New("镜像期间检测到本地改动")
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
	stateName  = "oryn-sync.json"
	indexName  = "oryn-sync.index"
	bundleName = "oryn-sync.bundle"
	maxHave    = 256
)

// state 是 <git-dir>/oryn-sync.json：镜像开始前的位置，Restore 据此还原。
type state struct {
	Version   int    `json:"version"`
	ThreadID  string `json:"threadId"`
	OrigRef   string `json:"origRef"`
	OrigSha   string `json:"origSha"`
	StartedAt int64  `json:"startedAt"`
	PID       int    `json:"pid"`
}

// repo 是一个本地检出。
type repo struct {
	top, gitDir string
}

// git 在检出里执行 git：不跑钩子、不提示游离 HEAD、不弹认证提示；返回去掉首尾空白的 stdout。
func (g *repo) git(env []string, args ...string) (string, error) {
	return runGit(g.top, env, args...)
}

func runGit(dir string, env []string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "advice.detachedHead=false", "-c", "core.quotepath=off"}, args...)
	// 本地 git 调用不随 ctx 取消：中途杀掉 checkout / reset 会留下半截工作区与 index.lock
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), append([]string{"GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}, env...)...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
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
	return &repo{top: top, gitDir: gitDir}, nil
}

func (g *repo) path(name string) string { return filepath.Join(g.gitDir, name) }

// precheck：没有残留状态文件、工作区干净、不在合并 / 拣选 / 还原 / 二分 / 变基中间状态。
func (g *repo) precheck() error {
	if _, err := os.Stat(g.path(stateName)); err == nil {
		return fmt.Errorf("%w（%s）：先运行 `oryn sync --restore`", ErrStaleState, g.path(stateName))
	}
	for _, n := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(g.path(n)); err == nil {
			return fmt.Errorf("%w：仓库处于未完成的操作中（%s）", ErrDirty, n)
		}
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

// fetch 下载 bundle 并取进 refs/oryn/sync/<key>/*，最后确认 head 与快照提交都在本地。
func (g *repo) fetch(ctx context.Context, r Remote, key string, st apigen.OrbSyncState) error {
	if st.Bundle != nil {
		file := g.path(bundleName)
		f, err := os.Create(file)
		if err != nil {
			return err
		}
		defer os.Remove(file)
		err = r.Bundle(ctx, st.Bundle.ID, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("下载 bundle: %w", err)
		}
		if _, err := g.git(nil, "fetch", "--no-tags", "--quiet", file, "+refs/oryn/sync/*:refs/oryn/sync/"+key+"/*"); err != nil {
			return err
		}
	}
	for _, c := range []string{st.Head, st.Snapshot} {
		if !g.ok("cat-file", "-e", c+"^{commit}") {
			return fmt.Errorf("本地缺少 orb 的提交 %s", short(c))
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

// worktreeTree 用临时 index（真实 index 的副本）把当前工作区写成树，不改真实 index。
func (g *repo) worktreeTree() (string, error) {
	idx := g.path(indexName)
	defer os.Remove(idx)
	env := []string{"GIT_INDEX_FILE=" + idx}
	if b, err := os.ReadFile(g.path("index")); err == nil {
		if err := os.WriteFile(idx, b, 0o600); err != nil {
			return "", err
		}
	} else if _, err := g.git(env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := g.git(env, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	return g.git(env, "write-tree")
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

// ---- 持续镜像 ----

// Mirror 持续把 orb 的提交与工作区镜像到本地检出（HEAD 游离在 orb 的 HEAD 上，工作区 = orb 的工作区），
// 直到 ctx 取消后还原并返回 nil。镜像期间本地被改动则返回 ErrLocalEdits，不还原、不覆盖。
func Mirror(ctx context.Context, r Remote, o Options) error {
	g, err := openRepo(o.Dir)
	if err != nil {
		return err
	}
	if err := g.precheck(); err != nil {
		return err
	}
	origSha, err := g.git(nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return errors.New("本地仓库还没有提交")
	}
	origRef, _ := g.git(nil, "symbolic-ref", "--quiet", "HEAD")
	b, _ := json.MarshalIndent(state{
		Version: 1, ThreadID: o.ThreadID, OrigRef: origRef, OrigSha: origSha, StartedAt: time.Now().UnixMilli(), PID: os.Getpid(),
	}, "", "  ")
	if err := os.WriteFile(g.path(stateName), append(b, '\n'), 0o600); err != nil {
		return err
	}
	m := &mirror{g: g, r: r, o: o, key: refKey(o.ThreadID), origSha: origSha}
	first, waiting, failing := true, false, false
	for {
		st, err := g.prepare(ctx, r, o, !first, m.head, m.snapshot)
		if ctx.Err() != nil {
			return Restore(g.top)
		}
		switch {
		case err != nil && first:
			// 尚未改动本地：撤掉状态文件即可
			_ = os.Remove(g.path(stateName))
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
			if st.Head != m.head || st.Snapshot != m.snapshot {
				applied, err := m.round(ctx, st, first)
				if ctx.Err() != nil {
					return Restore(g.top)
				}
				switch {
				case err != nil && (first || errors.Is(err, ErrLocalEdits) || applied):
					if first && !applied {
						_ = os.Remove(g.path(stateName))
					}
					return err
				case err != nil:
					logf(o, "同步失败（稍后重试）：%v", err)
				default:
					first = false
				}
			}
		}
		select {
		case <-ctx.Done():
			return Restore(g.top)
		case <-time.After(o.Interval):
		}
	}
}

// mirror 是一次持续镜像的进度：上一次应用的 orb HEAD、快照与应用后本地工作区的树。
type mirror struct {
	g                    *repo
	r                    Remote
	o                    Options
	key, origSha         string
	head, snapshot, tree string
}

// round 应用一个新状态。touched 为真表示本地检出已被改动（此后的错误不可重试）。
// 本地改动保护比较的是「上一轮应用后本地算出的树」而不是快照的树：本机的全局忽略规则与 orb 不同也不会误报。
func (m *mirror) round(ctx context.Context, st apigen.OrbSyncState, first bool) (touched bool, err error) {
	if !first {
		tree, err := m.g.worktreeTree()
		if err != nil {
			return false, err
		}
		if tree != m.tree {
			return false, fmt.Errorf("%w：已停止，本地文件未被覆盖；处理后运行 `oryn sync --restore` 还原", ErrLocalEdits)
		}
	}
	if err := m.g.fetch(ctx, m.r, m.key, st); err != nil {
		return false, err
	}
	if err := m.g.mirrorApply(st); err != nil {
		return true, err
	}
	tree, err := m.g.worktreeTree()
	if err != nil {
		return true, err
	}
	m.head, m.snapshot, m.tree = st.Head, st.Snapshot, tree
	m.g.logApplied(m.o, st, m.origSha)
	return false, nil
}

// mirrorApply：清掉上一轮的未跟踪文件 → 强制游离检出快照 → index 退回 orb 的 HEAD。
// 结果：HEAD = orb 的提交，未提交改动为未暂存、新文件为未跟踪；被忽略的本地文件不受影响。
func (g *repo) mirrorApply(st apigen.OrbSyncState) error {
	for _, args := range [][]string{
		{"clean", "-fdq"},
		{"checkout", "-q", "-f", "--detach", st.Snapshot},
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
func Restore(dir string) error {
	g, err := openRepo(dir)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(g.path(stateName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("没有需要还原的 sync 状态")
		}
		return err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil || s.OrigSha == "" {
		return fmt.Errorf("状态文件 %s 损坏", g.path(stateName))
	}
	steps := [][]string{{"clean", "-fdq"}}
	if br, ok := strings.CutPrefix(s.OrigRef, "refs/heads/"); ok {
		steps = append(steps, []string{"checkout", "-q", "-f", br})
	} else {
		steps = append(steps, []string{"checkout", "-q", "-f", "--detach", s.OrigSha})
	}
	steps = append(steps, []string{"reset", "-q", "--hard", s.OrigSha})
	for _, args := range steps {
		if _, err := g.git(nil, args...); err != nil {
			return err
		}
	}
	if err := g.dropRefs(refKey(s.ThreadID)); err != nil {
		return err
	}
	_ = os.Remove(g.path(indexName))
	_ = os.Remove(g.path(bundleName))
	return os.Remove(g.path(stateName))
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
	st, err := g.prepare(ctx, r, o, false)
	if err != nil {
		return err
	}
	key := refKey(o.ThreadID)
	if err := g.fetch(ctx, r, key, st); err != nil {
		return err
	}
	branch := ""
	if !o.Detach && st.Branch != nil {
		branch = *st.Branch
	}
	switch {
	case branch == "":
		_, err = g.git(nil, "checkout", "-q", "--detach", st.Head)
	case !g.ok("rev-parse", "--verify", "--quiet", "refs/heads/"+branch):
		_, err = g.git(nil, "checkout", "-q", "-b", branch, st.Head)
	case g.occupied(branch):
		logf(o, "警告：分支 %s 已被另一个 worktree 检出，改为游离检出", branch)
		branch = ""
		_, err = g.git(nil, "checkout", "-q", "--detach", st.Head)
	case !g.ok("merge-base", "--is-ancestor", "refs/heads/"+branch, st.Head):
		logf(o, "警告：本地分支 %s 与 orb 的提交已分叉，改为游离检出", branch)
		branch = ""
		_, err = g.git(nil, "checkout", "-q", "--detach", st.Head)
	default:
		if _, err = g.git(nil, "checkout", "-q", branch); err == nil {
			_, err = g.git(nil, "merge", "-q", "--ff-only", st.Head)
		}
	}
	if err != nil {
		return err
	}
	if st.Snapshot != st.Head {
		if _, err := g.git(nil, "restore", "-q", "--source="+st.Snapshot, "--staged", "--worktree", "--", ":/"); err != nil {
			return err
		}
		if _, err := g.git(nil, "reset", "-q"); err != nil {
			return err
		}
	}
	if err := g.dropRefs(key); err != nil {
		return err
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
	var path string
	for line := range strings.SplitSeq(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			path = p
			continue
		}
		if line == "branch refs/heads/"+branch {
			if real, _ := filepath.EvalSymlinks(path); real != self {
				return true
			}
		}
	}
	return false
}
