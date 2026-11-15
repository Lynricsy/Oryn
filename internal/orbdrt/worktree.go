package orbdrt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// runner 的 worktree（M11b 计划 7.2）：worktree.add 在仓库旁新建 worktree 与同名分支并登记到
// $HOME/.oryn/orbd/worktrees.json；登记过的目录同样可作线程的工作目录。worktree.remove 不带 --force、
// 删分支不带 -D：有未提交改动或分支未合并时保留用户的东西。

var worktreeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

const (
	// worktreeFetchTimeout 是 worktree.add 之前 `git fetch` 的上限（失败忽略）。
	worktreeFetchTimeout = 30 * time.Second
	// worktreeGitTimeout 是 worktree add / remove / branch 各自的上限（大仓库检出可能较久）。
	worktreeGitTimeout = 5 * time.Minute
	// maxWorktreeSuffix 是目录或分支重名时尝试的最大后缀（-2 … -N）。
	maxWorktreeSuffix = 100
)

// worktreeEntry 是一条 worktree 登记。
type worktreeEntry struct {
	Repo   string `json:"repo"`
	Dir    string `json:"dir"`
	Branch string `json:"branch"`
}

// worktreeStore 是 worktrees.json 的内存副本。
type worktreeStore struct {
	path string
	mu   sync.Mutex
	list []worktreeEntry
}

func loadWorktrees(path string) *worktreeStore {
	s := &worktreeStore{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s.list); err != nil {
		slog.Warn("orbd: ignoring corrupt worktree registry", "path", path, "err", err)
		s.list = nil
	}
	return s
}

func (s *worktreeStore) dirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.list))
	for _, e := range s.list {
		out = append(out, e.Dir)
	}
	return out
}

// find 按目录查找登记（目录先按原样、再按解析符号链接后的路径比较）。
func (s *worktreeStore) find(dir string) (worktreeEntry, bool) {
	clean := filepath.Clean(dir)
	real, _ := evalExisting(clean)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.list {
		if e.Dir == clean || e.Dir == real {
			return e, true
		}
	}
	return worktreeEntry{}, false
}

func (s *worktreeStore) add(e worktreeEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, e)
	return s.saveLocked()
}

func (s *worktreeStore) remove(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = slices.DeleteFunc(s.list, func(e worktreeEntry) bool { return e.Dir == dir })
	return s.saveLocked()
}

func (s *worktreeStore) saveLocked() error {
	list := s.list
	if list == nil {
		list = []worktreeEntry{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, b, 0o600)
}

// runGit 在 dir 中执行 git（用户自己的 git 配置与凭据，不交互），返回 stderr；非零退出为错误。
func (r *Runtime) runGit(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := r.bins.command(ctx, "git", append([]string{"-C", dir}, args...)...)
	env := r.baseEnv()
	env["GIT_TERMINAL_PROMPT"] = "0"
	env["LC_ALL"] = "C"
	cmd.Env = envList(env)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	msg := strings.TrimSpace(stderr.String())
	if err != nil && msg == "" {
		msg = err.Error()
	}
	return msg, err
}

// branchExists 报告仓库里是否已有分支 refs/heads/<branch>。
func (r *Runtime) branchExists(ctx context.Context, repo, branch string) (bool, error) {
	msg, err := r.runGit(ctx, worktreeGitTimeout, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git show-ref: %s", msg)
}

// worktreeAdd 实现 ctl worktree.add。
func (r *Runtime) worktreeAdd(ctx context.Context, p apigen.LinkCtlWorktreeAddParams) (apigen.LinkCtlWorktreeAddResult, error) {
	var res apigen.LinkCtlWorktreeAddResult
	if !worktreeNameRe.MatchString(p.Name) {
		return res, ctlErr(link.CodeInvalidParams, "invalid worktree name %q", p.Name)
	}
	repo, ok := r.allowed(p.Repo)
	if !ok {
		return res, ctlErr(CodeDirNotServed, "%q is not inside a directory served by this runner", p.Repo)
	}
	if _, err := os.Lstat(filepath.Join(repo, ".git")); err != nil {
		return res, ctlErr(link.CodeInvalidParams, "%s is not a git repository (no .git)", repo)
	}
	r.runner.wtMu.Lock()
	defer r.runner.wtMu.Unlock()
	if msg, err := r.runGit(ctx, worktreeFetchTimeout, repo, "fetch", "--quiet"); err != nil {
		r.log.Info("worktree.add: git fetch failed; continuing", "repo", repo, "err", msg)
	}
	parent, base := filepath.Dir(repo), filepath.Base(repo)
	for i := 1; i <= maxWorktreeSuffix; i++ {
		suffix := ""
		if i > 1 {
			suffix = "-" + strconv.Itoa(i)
		}
		dir := filepath.Join(parent, base+"-"+p.Name+suffix)
		branch := p.Name + suffix
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		exists, err := r.branchExists(ctx, repo, branch)
		if err != nil {
			return res, ctlErr("failed", "%v", err)
		}
		if exists {
			continue
		}
		if msg, err := r.runGit(ctx, worktreeGitTimeout, repo, "worktree", "add", "-b", branch, dir); err != nil {
			return res, ctlErr("failed", "%s", msg)
		}
		if err := r.runner.wt.add(worktreeEntry{Repo: repo, Dir: dir, Branch: branch}); err != nil {
			r.log.Warn("persist worktree registry failed", "err", err)
		}
		r.log.Info("worktree added", "repo", repo, "dir", dir, "branch", branch)
		return apigen.LinkCtlWorktreeAddResult{Dir: dir, Branch: branch}, nil
	}
	return res, ctlErr("failed", "no free directory / branch name for %q next to %s", p.Name, repo)
}

// worktreeRemove 实现 ctl worktree.remove。
func (r *Runtime) worktreeRemove(ctx context.Context, p apigen.LinkCtlWorktreeRemoveParams) (apigen.LinkCtlWorktreeRemoveResult, error) {
	var res apigen.LinkCtlWorktreeRemoveResult
	e, ok := r.runner.wt.find(p.Dir)
	if !ok {
		return res, ctlErr("not_found", "%q is not a worktree created by this runner", p.Dir)
	}
	r.runner.wtMu.Lock()
	defer r.runner.wtMu.Unlock()
	if _, err := os.Lstat(e.Dir); errors.Is(err, fs.ErrNotExist) {
		// 目录已被手工删掉：清理 git 的 worktree 记录后照常删分支、注销。
		if msg, err := r.runGit(ctx, worktreeGitTimeout, e.Repo, "worktree", "prune"); err != nil {
			r.log.Info("git worktree prune failed", "repo", e.Repo, "err", msg)
		}
	} else if msg, err := r.runGit(ctx, worktreeGitTimeout, e.Repo, "worktree", "remove", e.Dir); err != nil {
		// 不带 --force：有未提交改动（含未跟踪文件）时 git 拒绝，目录原样保留。
		return res, ctlErr(CodeWorktreeDirty, "%s", msg)
	}
	res.Ok = true
	if msg, err := r.runGit(ctx, worktreeGitTimeout, e.Repo, "branch", "-d", e.Branch); err != nil {
		r.log.Info("worktree branch kept", "branch", e.Branch, "err", msg)
		kept := true
		res.BranchKept = &kept
	}
	if err := r.runner.wt.remove(e.Dir); err != nil {
		r.log.Warn("persist worktree registry failed", "err", err)
	}
	return res, nil
}
