package orbdrt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 改动接口（M3 计划 4.1）：orbd 直接跑 git。omp 自带的 get_git_changes 只有 HEAD↔工作区，无暂存与 commit 列表。

const (
	gitTimeout = 20 * time.Second
	// emptyTree 是空树对象（HEAD 未出生、根提交的父提交）。
	emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	// maxGitFiles 是 status 文件数上限；maxPatchBytes 是单文件 patch 上限；maxGitCommits 是 commits 上限。
	maxGitFiles   = 2000
	maxPatchBytes = 1 << 20
	maxGitCommits = 200
	// untrackedCountBytes：不超过它的未跟踪文本文件给出行数；untrackedDiffBytes：超过它的未跟踪文件 diff 为 large。
	untrackedCountBytes = 1 << 20
	untrackedDiffBytes  = 4 << 20
	// maxGitOutput 是列举类 git 输出的读取上限（防止巨型仓库耗尽内存）。
	maxGitOutput = 64 << 20
	maxGitPaths  = 10000
)

var (
	commitRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	baseRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$`)
)

// gitResult 是一次 git 调用的结果；code 为退出码（被截断而杀掉时无意义）。
type gitResult struct {
	out       []byte
	truncated bool
	code      int
	stderr    string
}

// git 在 dir 中执行 git（20s 超时），stdout 至多读 limit 字节，超出即杀掉进程并置 truncated。
// 只有无法启动、超时或被信号杀死（且非截断）时返回错误；非零退出码由调用方按 code 判断。
func (r *Runtime) git(ctx context.Context, dir string, stdin []byte, limit int, args ...string) (gitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{
		"-C", dir,
		"-c", "core.quotepath=off", "-c", "color.ui=false",
		"-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
	}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	env := r.baseEnv()
	env["GIT_OPTIONAL_LOCKS"] = "0"
	env["GIT_TERMINAL_PROMPT"] = "0"
	env["LC_ALL"] = "C"
	cmd.Env = envList(env)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return gitResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return gitResult{}, err
	}
	out, rerr := io.ReadAll(io.LimitReader(pipe, int64(limit)+1))
	res := gitResult{out: out}
	if len(out) > limit {
		res.out, res.truncated = out[:limit], true
		_ = cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, pipe)
	}
	werr := cmd.Wait()
	res.stderr = stderr.String()
	if ctx.Err() != nil && !res.truncated {
		return res, errf(http.StatusInternalServerError, "git_failed", "git %s timed out", args[0])
	}
	if rerr != nil && !res.truncated {
		return res, rerr
	}
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		res.code = ee.ExitCode()
		if res.code < 0 && !res.truncated {
			return res, errf(http.StatusInternalServerError, "git_failed", "git %s: %v", args[0], werr)
		}
	case !res.truncated:
		return res, werr
	}
	return res, nil
}

// gitOK 执行 git 并要求退出码 0；失败映射为 git_busy（index.lock）或 git_failed（附 stderr 尾部）。
func (r *Runtime) gitOK(ctx context.Context, dir string, stdin []byte, limit int, args ...string) ([]byte, bool, error) {
	res, err := r.git(ctx, dir, stdin, limit, args...)
	if err != nil {
		return nil, false, err
	}
	if res.code != 0 && !res.truncated {
		return nil, false, gitFailure(args[0], res)
	}
	return res.out, res.truncated, nil
}

func gitFailure(op string, res gitResult) *apiError {
	msg := strings.TrimSpace(res.stderr)
	if strings.Contains(msg, "index.lock") {
		return errf(http.StatusConflict, "git_busy", "%s", msg)
	}
	return errf(http.StatusInternalServerError, "git_failed", "git %s exited %d: %s", op, res.code, msg)
}

// tailBuffer 只保留最后 4KiB（stderr 尾部）。
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4<<10 {
		t.b = append(t.b[:0:0], t.b[len(t.b)-4<<10:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.b) }

// revParse 解析一个修订为完整 sha；不存在时返回空串。
func (r *Runtime) revParse(ctx context.Context, top, rev string) (string, error) {
	res, err := r.git(ctx, top, nil, 4<<10, "rev-parse", "--verify", "--quiet", "--end-of-options", rev)
	if err != nil {
		return "", err
	}
	if res.code != 0 {
		return "", nil
	}
	return strings.TrimSpace(string(res.out)), nil
}

// ---- 参数校验 ----

// validRelPath：仓库内相对路径；拒绝空串、NUL、绝对路径与 `..` 段。
func validRelPath(p string) bool {
	if p == "" || strings.ContainsRune(p, 0) || filepath.IsAbs(p) {
		return false
	}
	if slices.Contains(strings.Split(p, "/"), "..") {
		return false
	}
	return filepath.Clean(p) != "."
}

func literal(p string) string { return ":(literal)" + p }

func validBase(b string) bool { return baseRe.MatchString(b) && !strings.Contains(b, "..") }

// gitQuery 是 status / diff 共用的查询参数。
type gitQuery struct {
	root, scope, commit, base string
}

func parseGitQuery(req *http.Request) (gitQuery, error) {
	q := req.URL.Query()
	g := gitQuery{root: q.Get("root"), scope: q.Get("scope"), commit: q.Get("commit"), base: q.Get("base")}
	if g.scope == "" {
		g.scope = string(apigen.All)
	}
	switch apigen.GitScope(g.scope) {
	case apigen.All, apigen.Uncommitted, apigen.Commit:
	default:
		return g, badRequest("invalid scope %q", g.scope)
	}
	if g.commit != "" && !commitRe.MatchString(g.commit) {
		return g, badRequest("invalid commit %q", g.commit)
	}
	if g.scope == string(apigen.Commit) && g.commit == "" {
		return g, badRequest("scope=commit requires commit")
	}
	if g.base != "" && !validBase(g.base) {
		return g, badRequest("invalid base %q", g.base)
	}
	return g, nil
}

// ---- 仓库定位 ----

// repoLoc 是一次请求定位到的仓库；top 为空表示 root 不在仓库内。
type repoLoc struct {
	rootAbs, rootReal string
	top               string
}

// locateRepo 解析 root（缺省当前工作目录）并找到仓库顶层；顶层不在 $HOME 之下时视为不在仓库内。
func (r *Runtime) locateRepo(ctx context.Context, root string) (repoLoc, error) {
	explicit := root != ""
	if !explicit {
		root = r.currentWorkdir()
	}
	abs, real, err := r.userPath(root, true)
	if err != nil {
		return repoLoc{}, err
	}
	loc := repoLoc{rootAbs: abs, rootReal: real}
	st, err := os.Stat(real)
	switch {
	case err != nil && !explicit:
		return loc, nil
	case err != nil:
		return repoLoc{}, fsError(err)
	case !st.IsDir():
		return repoLoc{}, errf(http.StatusConflict, "not_a_directory", "%s is not a directory", abs)
	}
	res, err := r.git(ctx, real, nil, 64<<10, "rev-parse", "--show-toplevel")
	if err != nil {
		return repoLoc{}, err
	}
	if res.code != 0 {
		return loc, nil
	}
	top := strings.TrimSpace(string(res.out))
	if realTop, err := filepath.EvalSymlinks(top); err != nil {
		return loc, nil
	} else if _, ok := r.relHome(realTop); !ok {
		return loc, nil
	}
	loc.top = top
	return loc, nil
}

func (r *Runtime) requireRepo(ctx context.Context, root string) (string, error) {
	loc, err := r.locateRepo(ctx, root)
	if err != nil {
		return "", err
	}
	if loc.top == "" {
		return "", errf(http.StatusConflict, "not_a_repo", "%s is not inside a git repository", loc.rootAbs)
	}
	return loc.top, nil
}

// candidates 返回 root 下一层含 `.git` 的子目录（绝对路径，≤20）。
func candidates(loc repoLoc) []string {
	des, err := os.ReadDir(loc.rootReal)
	if err != nil {
		return nil
	}
	var out []string
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		if _, err := os.Lstat(filepath.Join(loc.rootReal, de.Name(), ".git")); err == nil {
			out = append(out, filepath.Join(loc.rootAbs, de.Name()))
			if len(out) == 20 {
				break
			}
		}
	}
	return out
}

// ---- 基线与范围 ----

// resolveBase 按 base 参数 → origin/HEAD → main / master 的顺序确定基线名，再取
// refs/remotes/origin/<name>、refs/heads/<name> 中先能解析者；HEAD 未出生或都解析不到时返回 nil。
func (r *Runtime) resolveBase(ctx context.Context, top, name, head string) (*apigen.GitBase, error) {
	if head == "" {
		return nil, nil
	}
	refs := func(n string) []string { return []string{"refs/remotes/origin/" + n, "refs/heads/" + n} }
	if name == "" {
		res, err := r.git(ctx, top, nil, 4<<10, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
		if err != nil {
			return nil, err
		}
		if res.code == 0 {
			name = strings.TrimPrefix(strings.TrimSpace(string(res.out)), "origin/")
		}
	}
	if name == "" {
	pick:
		for _, n := range []string{"main", "master"} {
			for _, ref := range refs(n) {
				sha, err := r.revParse(ctx, top, ref+"^{commit}")
				if err != nil {
					return nil, err
				}
				if sha != "" {
					name = n
					break pick
				}
			}
		}
	}
	if name == "" {
		return nil, nil
	}
	for _, ref := range refs(name) {
		sha, err := r.revParse(ctx, top, ref+"^{commit}")
		if err != nil {
			return nil, err
		}
		if sha == "" {
			continue
		}
		b := &apigen.GitBase{Name: name, Ref: ref, MergeBase: sha}
		res, err := r.git(ctx, top, nil, 4<<10, "merge-base", "HEAD", ref)
		if err != nil {
			return nil, err
		}
		if mb := strings.TrimSpace(string(res.out)); res.code == 0 && mb != "" {
			b.MergeBase = mb
		} else {
			// 浅克隆 / 无共同祖先：用基线引用本身。
			b.Approximate = true
		}
		return b, nil
	}
	return nil, nil
}

// gitRange 是一次比较的两端；worktree 表示 to 为工作区（此时 to 为空）。
type gitRange struct {
	from, to string
	worktree bool
}

func (g gitRange) args() []string {
	if g.worktree {
		return []string{g.from}
	}
	return []string{g.from, g.to}
}

// repoState 汇集一次请求解析出的 HEAD、分支、基线与比较范围。
type repoState struct {
	head, branch string
	base         *apigen.GitBase
	rng          gitRange
}

func (r *Runtime) repoState(ctx context.Context, top string, q gitQuery) (repoState, error) {
	var s repoState
	var err error
	if s.head, err = r.revParse(ctx, top, "HEAD^{commit}"); err != nil {
		return s, err
	}
	res, err := r.git(ctx, top, nil, 4<<10, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return s, err
	}
	if res.code == 0 {
		s.branch = strings.TrimSpace(string(res.out))
	}
	if s.base, err = r.resolveBase(ctx, top, q.base, s.head); err != nil {
		return s, err
	}
	switch apigen.GitScope(q.scope) {
	case apigen.Commit:
		sha, err := r.revParse(ctx, top, q.commit+"^{commit}")
		if err != nil {
			return s, err
		}
		if sha == "" {
			return s, errf(http.StatusNotFound, "not_found", "commit %s not found", q.commit)
		}
		parent, err := r.revParse(ctx, top, sha+"^1")
		if err != nil {
			return s, err
		}
		if parent == "" {
			parent = emptyTree
		}
		s.rng = gitRange{from: parent, to: sha}
	case apigen.All:
		if s.base != nil {
			s.rng = gitRange{from: s.base.MergeBase, worktree: true}
			break
		}
		fallthrough
	default: // uncommitted
		from := s.head
		if from == "" {
			from = emptyTree
		}
		s.rng = gitRange{from: from, worktree: true}
	}
	return s, nil
}

// ---- status ----

func nulFields(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

var nameStatusMap = map[byte]apigen.GitFileChangeStatus{
	'A': apigen.GitFileChangeStatusAdded,
	'M': apigen.GitFileChangeStatusModified,
	'D': apigen.GitFileChangeStatusDeleted,
	'R': apigen.GitFileChangeStatusRenamed,
	'C': apigen.GitFileChangeStatusCopied,
	'T': apigen.GitFileChangeStatusTypechange,
	'U': apigen.GitFileChangeStatusConflicted,
}

func (r *Runtime) handleGitStatus(w http.ResponseWriter, req *http.Request) {
	q, err := parseGitQuery(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx := req.Context()
	loc, err := r.locateRepo(ctx, q.root)
	if err != nil {
		writeErr(w, err)
		return
	}
	if loc.top == "" {
		writeJSON(w, http.StatusOK, apigen.GitStatus{
			Repo: false, Scope: apigen.GitScope(q.scope), Files: []apigen.GitFileChange{}, Candidates: candidates(loc),
		})
		return
	}
	st, err := r.repoState(ctx, loc.top, q)
	if err != nil {
		writeErr(w, err)
		return
	}
	files, truncated, err := r.changedFiles(ctx, loc.top, st.rng)
	if err != nil {
		writeErr(w, err)
		return
	}
	res := apigen.GitStatus{
		Repo: true, Root: &loc.top, Base: st.base, Scope: apigen.GitScope(q.scope), Files: files, Truncated: truncated,
	}
	if st.branch != "" {
		res.Branch = &st.branch
	}
	if st.head != "" {
		res.Head = &st.head
	}
	writeJSON(w, http.StatusOK, res)
}

// changedFiles 计算范围内的文件改动；to 为工作区时并入未跟踪文件与暂存状态。
func (r *Runtime) changedFiles(ctx context.Context, top string, rng gitRange) ([]apigen.GitFileChange, bool, error) {
	byPath := map[string]*apigen.GitFileChange{}
	var order []string
	add := func(c *apigen.GitFileChange) {
		if _, ok := byPath[c.Path]; !ok {
			order = append(order, c.Path)
		}
		byPath[c.Path] = c
	}
	diffArgs := append([]string{"diff", "--no-ext-diff", "-M", "--name-status", "-z"}, rng.args()...)
	out, truncated, err := r.gitOK(ctx, top, nil, maxGitOutput, append(diffArgs, "--")...)
	if err != nil {
		return nil, false, err
	}
	toks := nulFields(out)
	for i := 0; i < len(toks); i++ {
		code := toks[i]
		if code == "" || i+1 >= len(toks) {
			break
		}
		c := &apigen.GitFileChange{Staged: apigen.GitFileChangeStagedNone, Status: apigen.GitFileChangeStatusModified}
		if s, ok := nameStatusMap[code[0]]; ok {
			c.Status = s
		}
		if code[0] == 'R' || code[0] == 'C' {
			if i+2 >= len(toks) {
				break
			}
			old := toks[i+1]
			c.OldPath, c.Path = &old, toks[i+2]
			i += 2
		} else {
			c.Path = toks[i+1]
			i++
		}
		add(c)
	}
	numArgs := append([]string{"diff", "--no-ext-diff", "-M", "--numstat", "-z"}, rng.args()...)
	out, ntrunc, err := r.gitOK(ctx, top, nil, maxGitOutput, append(numArgs, "--")...)
	if err != nil {
		return nil, false, err
	}
	truncated = truncated || ntrunc
	toks = nulFields(out)
	for i := 0; i < len(toks); i++ {
		parts := strings.SplitN(toks[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" { // 重命名 / 复制：其后两个字段是旧路径与新路径
			if i+2 >= len(toks) {
				break
			}
			path = toks[i+2]
			i += 2
		}
		c := byPath[path]
		if c == nil {
			continue
		}
		if parts[0] == "-" && parts[1] == "-" {
			c.Binary = true
			continue
		}
		a, aerr := strconv.Atoi(parts[0])
		d, derr := strconv.Atoi(parts[1])
		if aerr == nil && derr == nil {
			c.Additions, c.Deletions = &a, &d
		}
	}
	if rng.worktree {
		if err := r.mergeIndexState(ctx, top, byPath, add); err != nil {
			return nil, false, err
		}
		ut, err := r.untracked(ctx, top, maxGitFiles+1-len(order))
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || ut.truncated
		for _, c := range ut.files {
			if _, ok := byPath[c.Path]; !ok {
				add(c)
			}
		}
	}
	slices.Sort(order)
	files := make([]apigen.GitFileChange, 0, min(len(order), maxGitFiles))
	for _, p := range order {
		if len(files) == maxGitFiles {
			truncated = true
			break
		}
		files = append(files, *byPath[p])
	}
	return files, truncated, nil
}

// mergeIndexState 以 `git status --porcelain=v2` 补上暂存状态：X≠. 且 Y=. → full；二者都≠. → partial；
// `u` 记录 → conflicted（不在列表中的冲突文件也加入）。净差异里没有、但索引或工作区相对 HEAD 有改动的文件
// （如暂存了修改、又把工作区改回 HEAD 的内容）同样加入，行数省略：它们仍有可撤销的暂存内容。
func (r *Runtime) mergeIndexState(ctx context.Context, top string, byPath map[string]*apigen.GitFileChange, add func(*apigen.GitFileChange)) error {
	out, _, err := r.gitOK(ctx, top, nil, maxGitOutput, "status", "--porcelain=v2", "-z", "--untracked-files=no")
	if err != nil {
		return err
	}
	toks := nulFields(out)
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if len(tok) < 2 {
			continue
		}
		var path, xy, orig string
		conflicted := false
		switch tok[0] {
		case '1':
			f := strings.SplitN(tok, " ", 9)
			if len(f) != 9 {
				continue
			}
			xy, path = f[1], f[8]
		case '2':
			f := strings.SplitN(tok, " ", 10)
			i++ // 下一个字段是原路径
			if len(f) != 10 {
				continue
			}
			xy, path = f[1], f[9]
			if i < len(toks) {
				orig = toks[i]
			}
		case 'u':
			f := strings.SplitN(tok, " ", 11)
			if len(f) != 11 {
				continue
			}
			xy, path, conflicted = f[1], f[10], true
		default:
			continue
		}
		c := byPath[path]
		if conflicted {
			if c == nil {
				c = &apigen.GitFileChange{Path: path, Staged: apigen.GitFileChangeStagedNone}
				add(c)
			}
			c.Status = apigen.GitFileChangeStatusConflicted
			continue
		}
		if len(xy) != 2 {
			continue
		}
		x, y := xy[0], xy[1]
		if c == nil {
			code := x
			if code == '.' {
				code = y
			}
			c = &apigen.GitFileChange{Path: path, Staged: apigen.GitFileChangeStagedNone, Status: apigen.GitFileChangeStatusModified}
			if s, ok := nameStatusMap[code]; ok {
				c.Status = s
			}
			if orig != "" && orig != path {
				c.OldPath = &orig
			}
			add(c)
		}
		switch {
		case x != '.' && y == '.':
			c.Staged = apigen.GitFileChangeStagedFull
		case x != '.' && y != '.':
			c.Staged = apigen.GitFileChangeStagedPartial
		}
	}
	return nil
}

type untrackedList struct {
	files     []*apigen.GitFileChange
	truncated bool
}

// untracked 列出至多 max 个未跟踪文件（遵守 .gitignore）；≤1MiB 的文本文件给出行数。
func (r *Runtime) untracked(ctx context.Context, top string, max int) (untrackedList, error) {
	var res untrackedList
	out, truncated, err := r.gitOK(ctx, top, nil, maxGitOutput, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return res, err
	}
	res.truncated = truncated
	for _, p := range nulFields(out) {
		if len(res.files) >= max {
			res.truncated = true
			break
		}
		c := &apigen.GitFileChange{Path: p, Status: apigen.GitFileChangeStatusUntracked, Staged: apigen.GitFileChangeStagedNone}
		abs := filepath.Join(top, p)
		if fi, err := os.Lstat(abs); err == nil && fi.Mode().IsRegular() {
			if fi.Size() <= untrackedCountBytes {
				if b, err := os.ReadFile(abs); err == nil {
					if bytes.IndexByte(b, 0) >= 0 {
						c.Binary = true
					} else {
						n := bytes.Count(b, []byte{'\n'})
						if len(b) > 0 && b[len(b)-1] != '\n' {
							n++
						}
						c.Additions, c.Deletions = &n, new(0)
					}
				}
			} else if bin, err := hasNUL(abs); err == nil {
				c.Binary = bin
			}
		}
		res.files = append(res.files, c)
	}
	return res, nil
}

// hasNUL 报告文件前 8KiB 是否含 NUL（git 判断二进制的同一启发式）。
func hasNUL(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, textSniffBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

// ---- diff ----

func (r *Runtime) handleGitDiff(w http.ResponseWriter, req *http.Request) {
	q, err := parseGitQuery(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	path, oldPath := req.URL.Query().Get("path"), req.URL.Query().Get("oldPath")
	if !validRelPath(path) {
		writeErr(w, badPath("invalid path %q", path))
		return
	}
	if oldPath != "" && !validRelPath(oldPath) {
		writeErr(w, badPath("invalid oldPath %q", oldPath))
		return
	}
	ctx := req.Context()
	top, err := r.requireRepo(ctx, q.root)
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := r.repoState(ctx, top, q)
	if err != nil {
		writeErr(w, err)
		return
	}
	res := apigen.GitFileDiff{Path: path, Kind: apigen.Text}
	if oldPath != "" {
		res.OldPath = &oldPath
	}
	var out []byte
	var truncated bool
	untracked := false
	if st.rng.worktree {
		ls, _, err := r.gitOK(ctx, top, nil, 64<<10, "ls-files", "--others", "--exclude-standard", "-z", "--", literal(path))
		if err != nil {
			writeErr(w, err)
			return
		}
		untracked = slices.Contains(nulFields(ls), path)
	}
	if untracked {
		abs := filepath.Join(top, path)
		fi, err := os.Lstat(abs)
		if err != nil {
			writeErr(w, err)
			return
		}
		if fi.Size() > untrackedDiffBytes {
			res.Kind = apigen.Large
			writeJSON(w, http.StatusOK, res)
			return
		}
		gr, err := r.git(ctx, top, nil, maxPatchBytes, "diff", "--no-index", "--no-ext-diff", "-U3", "--", "/dev/null", path)
		if err != nil {
			writeErr(w, err)
			return
		}
		if gr.code > 1 && !gr.truncated {
			writeErr(w, gitFailure("diff", gr))
			return
		}
		out, truncated = gr.out, gr.truncated
	} else {
		args := append([]string{"diff", "--no-ext-diff", "-M", "-U3"}, st.rng.args()...)
		args = append(args, "--", literal(path))
		if oldPath != "" && oldPath != path {
			args = append(args, literal(oldPath))
		}
		out, truncated, err = r.gitOK(ctx, top, nil, maxPatchBytes, args...)
		if err != nil {
			writeErr(w, err)
			return
		}
		if st.rng.worktree && len(out) == 0 {
			// 净差异为空但索引与工作区各有改动（暂存后又把工作区改回）：依次给出暂存（HEAD→索引）与
			// 未暂存（索引→工作区）两段 patch，二者互相抵消；都为空时仍是空 patch。
			if out, truncated, err = r.indexPatches(ctx, top, st.head, path, oldPath); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	if truncated {
		if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
			out = out[:i+1]
		} else {
			out = nil
		}
		res.Truncated = true
	}
	if isBinaryPatch(out) {
		res.Kind, res.Truncated = apigen.Binary, false
		out = nil
	}
	res.Patch = string(out)
	writeJSON(w, http.StatusOK, res)
}

// indexPatches 返回 path 的暂存 patch（HEAD→索引，HEAD 未出生时相对空树）与未暂存 patch（索引→工作区）
// 的拼接，总长受 maxPatchBytes 限制。
func (r *Runtime) indexPatches(ctx context.Context, top, head, path, oldPath string) ([]byte, bool, error) {
	if head == "" {
		head = emptyTree
	}
	paths := []string{"--", literal(path)}
	if oldPath != "" && oldPath != path {
		paths = append(paths, literal(oldPath))
	}
	staged, truncated, err := r.gitOK(ctx, top, nil, maxPatchBytes, append([]string{"diff", "--no-ext-diff", "-M", "-U3", "--cached", head}, paths...)...)
	if err != nil || truncated {
		return staged, truncated, err
	}
	unstaged, truncated, err := r.gitOK(ctx, top, nil, maxPatchBytes-len(staged), append([]string{"diff", "--no-ext-diff", "-M", "-U3"}, paths...)...)
	if err != nil {
		return nil, false, err
	}
	return append(staged, unstaged...), truncated, nil
}

// isBinaryPatch 识别 git 对二进制文件输出的 `Binary files … differ` 行。
func isBinaryPatch(patch []byte) bool {
	for line := range bytes.SplitSeq(patch, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("Binary files ")) && bytes.HasSuffix(line, []byte(" differ")) {
			return true
		}
		if bytes.HasPrefix(line, []byte("@@")) {
			return false
		}
	}
	return false
}

// ---- commits ----

func (r *Runtime) handleGitCommits(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, badRequest("invalid limit %q", v))
			return
		}
		limit = min(n, maxGitCommits)
	}
	base := q.Get("base")
	if base != "" && !validBase(base) {
		writeErr(w, badRequest("invalid base %q", base))
		return
	}
	ctx := req.Context()
	top, err := r.requireRepo(ctx, q.Get("root"))
	if err != nil {
		writeErr(w, err)
		return
	}
	res := apigen.GitCommitList{Commits: []apigen.GitCommit{}}
	head, err := r.revParse(ctx, top, "HEAD^{commit}")
	if err != nil {
		writeErr(w, err)
		return
	}
	if head == "" {
		writeJSON(w, http.StatusOK, res)
		return
	}
	b, err := r.resolveBase(ctx, top, base, head)
	if err != nil {
		writeErr(w, err)
		return
	}
	rev := "HEAD"
	if b != nil {
		rev = b.MergeBase + "..HEAD"
	}
	out, _, err := r.gitOK(ctx, top, nil, maxGitOutput,
		"log", "--format=%H%x1f%h%x1f%an%x1f%ae%x1f%at%x1f%s%x1e", "-n", strconv.Itoa(limit+1), rev, "--")
	if err != nil {
		writeErr(w, err)
		return
	}
	for rec := range strings.SplitSeq(string(out), "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
		if len(f) != 6 {
			continue
		}
		if len(res.Commits) == limit {
			res.Truncated = true
			break
		}
		at, _ := strconv.ParseInt(f[4], 10, 64)
		res.Commits = append(res.Commits, apigen.GitCommit{
			Sha: f[0], ShortSha: f[1], AuthorName: f[2], AuthorEmail: f[3], TS: at * 1000, Subject: f[5],
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// ---- stage / unstage ----

func (r *Runtime) readGitPaths(w http.ResponseWriter, req *http.Request) (string, []byte, error) {
	var body apigen.GitPathsRequest
	if err := readJSON(w, req, &body); err != nil {
		return "", nil, err
	}
	if len(body.Paths) == 0 {
		return "", nil, badRequest("paths must not be empty")
	}
	if len(body.Paths) > maxGitPaths {
		return "", nil, badRequest("too many paths (max %d)", maxGitPaths)
	}
	var spec bytes.Buffer
	for _, p := range body.Paths {
		if !validRelPath(p) {
			return "", nil, badPath("invalid path %q", p)
		}
		spec.WriteString(literal(p))
		spec.WriteByte(0)
	}
	root := ""
	if body.Root != nil {
		root = *body.Root
	}
	top, err := r.requireRepo(req.Context(), root)
	if err != nil {
		return "", nil, err
	}
	return top, spec.Bytes(), nil
}

func (r *Runtime) handleGitStage(w http.ResponseWriter, req *http.Request) {
	top, spec, err := r.readGitPaths(w, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, _, err := r.gitOK(req.Context(), top, spec, 64<<10, "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Runtime) handleGitUnstage(w http.ResponseWriter, req *http.Request) {
	top, spec, err := r.readGitPaths(w, req)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx := req.Context()
	head, err := r.revParse(ctx, top, "HEAD^{commit}")
	if err != nil {
		writeErr(w, err)
		return
	}
	args := []string{"reset", "-q", "HEAD", "--pathspec-from-file=-", "--pathspec-file-nul"}
	if head == "" {
		args = []string{"rm", "-r", "-q", "--cached", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul"}
	}
	if _, _, err := r.gitOK(ctx, top, spec, 64<<10, args...); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
