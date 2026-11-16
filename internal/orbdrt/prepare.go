package orbdrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// github_prepare（M15 R20）：在线程工作目录里按固定顺序做结构检查、修复与检查命令，
// 全部通过才得到可上报控制面的通过记录（HEAD 提交）。

// 门禁级别。
const (
	PrepareLevelPush = "push"
	PrepareLevelPR   = "pr"
)

// prepareOutputMax 是每步保留的输出尾部上限。
const prepareOutputMax = 8 << 10

// 修复 / 检查 / 测试命令的时限；测试可调小。
var (
	prepareFixTimeout   = 600 * time.Second
	prepareCheckTimeout = 600 * time.Second
	prepareTestTimeout  = 3600 * time.Second
)

// PrepareRequest 是一次 github_prepare 调用。
type PrepareRequest struct {
	Dir        string              // 线程工作目录（git 仓库）
	Level      string              // "push" | "pr"
	SkipChecks bool                // 跳过修复 / 检查 / 测试命令（结构检查照做）
	Reason     string              // SkipChecks 时必填
	Release    bool                // 发布线程：允许在默认分支上
	Identity   *apigen.GitIdentity // 线程身份的提交身份；nil = 不校验作者
	Gates      apigen.GitHubGates  // 项目门禁配置（修复 / 检查 / 测试命令）
	Env        []string            // 执行命令的环境；nil = 继承 orbd 的环境
}

// PrepareResult 是 github_prepare 的结果（orbd.sock github.prepare 原样以 JSON 回给调用方）。
type PrepareResult struct {
	OK      bool          `json:"ok"`
	SHA     string        `json:"sha,omitempty"`     // 通过时的 HEAD
	Skipped bool          `json:"skipped,omitempty"` // 通过但跳过了命令（SkipChecks）
	Steps   []PrepareStep `json:"steps"`             // 已执行的步骤，按顺序
	Failure string        `json:"failure,omitempty"` // 首个失败步骤的说明
}

// PrepareStep 是一个已执行步骤：名称、是否通过、输出尾部（≤ 8KiB）。
type PrepareStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
}

// errStepFailed 让步骤函数提前结束；失败说明已记入结果。
var errStepFailed = errors.New("prepare step failed")

// prepareRun 是一次执行的上下文与累积结果。
type prepareRun struct {
	ctx context.Context
	req PrepareRequest
	env []string
	res PrepareResult
}

func (p *prepareRun) pass(name, output string) {
	p.res.Steps = append(p.res.Steps, PrepareStep{Name: name, OK: true, Output: tailString(output)})
}

// fail 记下失败步骤并返回 errStepFailed。
func (p *prepareRun) fail(name, output, format string, args ...any) error {
	p.res.Steps = append(p.res.Steps, PrepareStep{Name: name, Output: tailString(output)})
	p.res.Failure = name + ": " + fmt.Sprintf(format, args...)
	return errStepFailed
}

// RunPrepare 按 R20 的顺序执行 github_prepare：
//  1. HEAD 不是 detached；2. 当前分支不是默认分支（Release 例外）；3. origin/<默认分支>..HEAD 非空；
//  4. 把区间内提交信息里的字面量 `\n` 改写为真换行（只改信息）；5. 运行修复命令并把产生的改动并入 HEAD；
//  6. 工作树干净；7. 区间内每个提交的作者等于 Identity；8. 检查命令；9. level=pr 时测试命令。
//
// SkipChecks 只跳过 5、8、9 的命令。任一步失败返回 OK=false 与 Failure，不给 SHA；
// 参数无效或 ctx 结束时返回错误。
func RunPrepare(ctx context.Context, req PrepareRequest) (PrepareResult, error) {
	if req.Level != PrepareLevelPush && req.Level != PrepareLevelPR {
		return PrepareResult{}, fmt.Errorf("invalid level %q (want push or pr)", req.Level)
	}
	if req.SkipChecks && strings.TrimSpace(req.Reason) == "" {
		return PrepareResult{}, errors.New("skip_checks requires a non-empty reason")
	}
	if req.Dir == "" {
		return PrepareResult{}, errors.New("empty working directory")
	}
	env := req.Env
	if env == nil {
		env = os.Environ()
	}
	// git 不得交互（凭据提示、编辑器）。
	env = append(env[:len(env):len(env)], "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	p := &prepareRun{ctx: ctx, req: req, env: env}
	err := p.run()
	if ctx.Err() != nil {
		return PrepareResult{}, ctx.Err()
	}
	if err != nil {
		if !errors.Is(err, errStepFailed) {
			return PrepareResult{}, err
		}
		p.res.OK, p.res.SHA = false, ""
		return p.res, nil
	}
	p.res.OK, p.res.Skipped = true, req.SkipChecks
	return p.res, nil
}

func (p *prepareRun) run() error {
	// 1. HEAD 不是 detached。
	out, err := p.git(nil, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return p.fail("head", out, "HEAD is detached; check out a branch first")
	}
	branch := strings.TrimSpace(out)
	p.pass("head", branch)

	// 2. 当前分支不是默认分支（发布线程例外）。
	def, ok := p.defaultBranch()
	if !ok {
		return p.fail("branch", "", "cannot determine the default branch: origin/HEAD, origin/main and origin/master are all missing")
	}
	if branch == def && !p.req.Release {
		return p.fail("branch", "", "on the default branch %q; commit to a feature branch instead", def)
	}
	p.pass("branch", fmt.Sprintf("branch %s, default %s", branch, def))

	// 3. origin/<默认分支>..HEAD 非空。
	rangeSpec := "refs/remotes/origin/" + def + "..HEAD"
	commits, out, err := p.revList(rangeSpec)
	if err != nil {
		return p.fail("range", out, "git rev-list %s failed", rangeSpec)
	}
	if len(commits) == 0 {
		return p.fail("range", "", "no commits in origin/%s..HEAD", def)
	}
	p.pass("range", fmt.Sprintf("%d commit(s) in origin/%s..HEAD", len(commits), def))

	// 4. 提交信息里的字面量 `\n` 改写为真换行。
	if err := p.fixMessages(branch, commits); err != nil {
		return err
	}

	// 5. 修复命令：产生的改动并入 HEAD 提交。工作树原本就不干净时不并入（否则会把无关改动吞进提交），交给第 6 步报告。
	if fix := gateCmd(p.req.Gates.FixCmd); fix != "" && !p.req.SkipChecks {
		before, err := p.git(nil, "status", "--porcelain")
		if err != nil {
			return p.fail("fix", before, "git status failed")
		}
		out, err := p.shell(fix, prepareFixTimeout)
		if err != nil {
			return p.fail("fix", out, "fix command failed: %v", err)
		}
		if strings.TrimSpace(before) == "" {
			status, err := p.git(nil, "status", "--porcelain")
			if err != nil {
				return p.fail("fix", status, "git status failed")
			}
			if strings.TrimSpace(status) != "" {
				if o, err := p.git(nil, "add", "-A"); err != nil {
					return p.fail("fix", out+o, "git add failed")
				}
				if o, err := p.git(nil, "commit", "--amend", "--no-edit"); err != nil {
					return p.fail("fix", out+o, "amending HEAD with the fix command's changes failed")
				}
				out += "\n[oryn] amended HEAD with the fix command's changes"
			}
		}
		p.pass("fix", out)
	}

	// 6. 工作树干净（含未跟踪文件）。
	status, err := p.git(nil, "status", "--porcelain")
	if err != nil {
		return p.fail("clean", status, "git status failed")
	}
	if strings.TrimSpace(status) != "" {
		return p.fail("clean", status, "working tree is not clean; commit or remove these changes")
	}
	p.pass("clean", "")

	// 7. 区间内每个提交的作者等于线程身份（第 4、5 步可能改写了提交，重新列）。
	if id := p.req.Identity; id != nil {
		out, err := p.git(nil, "log", "--format=%H%x00%an%x00%ae", rangeSpec)
		if err != nil {
			return p.fail("author", out, "git log failed")
		}
		var bad []string
		for line := range strings.Lines(out) {
			f := strings.Split(strings.TrimRight(line, "\n"), "\x00")
			if len(f) != 3 {
				continue
			}
			if f[1] != id.Name || !strings.EqualFold(f[2], id.Email) {
				bad = append(bad, fmt.Sprintf("%s %s <%s>", shortRev(f[0]), f[1], f[2]))
			}
		}
		if len(bad) > 0 {
			return p.fail("author", strings.Join(bad, "\n"), "%d commit(s) not authored by %s <%s>", len(bad), id.Name, id.Email)
		}
		p.pass("author", "")
	}

	if !p.req.SkipChecks {
		// 8. 检查命令。
		if c := gateCmd(p.req.Gates.CheckCmd); c != "" {
			out, err := p.shell(c, prepareCheckTimeout)
			if err != nil {
				return p.fail("check", out, "check command failed: %v", err)
			}
			p.pass("check", out)
		}
		// 9. level=pr 时测试命令。
		if c := gateCmd(p.req.Gates.TestCmd); c != "" && p.req.Level == PrepareLevelPR {
			out, err := p.shell(c, prepareTestTimeout)
			if err != nil {
				return p.fail("test", out, "test command failed: %v", err)
			}
			p.pass("test", out)
		}
	}

	head, err := p.git(nil, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return p.fail("head", head, "git rev-parse HEAD failed")
	}
	p.res.SHA = strings.TrimSpace(head)
	return nil
}

// defaultBranch 是 origin 的默认分支短名：origin/HEAD 指向的分支，缺省依次取存在的 main、master。
func (p *prepareRun) defaultBranch() (string, bool) {
	if out, err := p.git(nil, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b, ok := strings.CutPrefix(strings.TrimSpace(out), "origin/"); ok && b != "" {
			return b, true
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, err := p.git(nil, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+b); err == nil {
			return b, true
		}
	}
	return "", false
}

// revList 返回区间内的提交（旧 → 新）。
func (p *prepareRun) revList(spec string) ([]string, string, error) {
	out, err := p.git(nil, "rev-list", "--reverse", spec)
	if err != nil {
		return nil, out, err
	}
	return strings.Fields(out), "", nil
}

// fixMessages 是第 4 步：从第一个信息需要改写的提交起，旧 → 新逐个用 commit-tree 重建（tree、作者、提交者与
// 两个日期原样保留，父提交换成重建后的），最后把分支指向新的 HEAD。没有需要改写的信息时什么都不做。
func (p *prepareRun) fixMessages(branch string, commits []string) error {
	rewritten := map[string]string{} // 旧提交 → 重建后的提交
	changed := 0
	for _, c := range commits {
		raw, err := p.git(nil, "cat-file", "commit", c)
		if err != nil {
			return p.fail("messages", raw, "git cat-file %s failed", shortRev(c))
		}
		obj, ok := parseCommitObject(raw)
		if !ok {
			return p.fail("messages", "", "cannot parse commit %s", shortRev(c))
		}
		msg := strings.ReplaceAll(obj.message, `\n`, "\n")
		parentMoved := false
		for i, par := range obj.parents {
			if n, ok := rewritten[par]; ok {
				obj.parents[i], parentMoved = n, true
			}
		}
		if msg == obj.message && !parentMoved {
			continue
		}
		if msg != obj.message {
			changed++
		}
		args := []string{"commit-tree", obj.tree}
		for _, par := range obj.parents {
			args = append(args, "-p", par)
		}
		env := []string{
			"GIT_AUTHOR_NAME=" + obj.author.name, "GIT_AUTHOR_EMAIL=" + obj.author.email, "GIT_AUTHOR_DATE=@" + obj.author.date,
			"GIT_COMMITTER_NAME=" + obj.committer.name, "GIT_COMMITTER_EMAIL=" + obj.committer.email, "GIT_COMMITTER_DATE=@" + obj.committer.date,
		}
		out, err := p.gitIn(env, strings.NewReader(msg), args...)
		if err != nil {
			return p.fail("messages", out, "git commit-tree for %s failed", shortRev(c))
		}
		rewritten[c] = strings.TrimSpace(out)
	}
	if changed == 0 {
		p.pass("messages", "")
		return nil
	}
	oldHead := commits[len(commits)-1]
	newHead := rewritten[oldHead]
	if out, err := p.git(nil, "update-ref", "-m", "oryn: github_prepare rewrote literal \\n in commit messages",
		"refs/heads/"+branch, newHead, oldHead); err != nil {
		return p.fail("messages", out, "git update-ref failed")
	}
	p.pass("messages", fmt.Sprintf("rewrote literal \\n in %d commit message(s); HEAD %s -> %s", changed, shortRev(oldHead), shortRev(newHead)))
	return nil
}

// commitIdent 是提交对象 author / committer 行的三个部分；date 为 git 内部格式 `<unix> <tz>`。
type commitIdent struct{ name, email, date string }

type commitObject struct {
	tree      string
	parents   []string
	author    commitIdent
	committer commitIdent
	message   string
}

// parseCommitObject 解析 `git cat-file commit` 的输出：头部到第一个空行为止，其后原样是提交信息。
// 签名等多行头（续行以空格开头）忽略：重建的提交本就不再带原签名。
func parseCommitObject(raw string) (commitObject, bool) {
	var obj commitObject
	head, msg, ok := strings.Cut(raw, "\n\n")
	if !ok {
		head, msg = strings.TrimSuffix(raw, "\n"), ""
	}
	obj.message = msg
	var haveAuthor, haveCommitter bool
	for line := range strings.SplitSeq(head, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "tree":
			obj.tree = val
		case "parent":
			obj.parents = append(obj.parents, val)
		case "author":
			obj.author, haveAuthor = parseIdent(val)
		case "committer":
			obj.committer, haveCommitter = parseIdent(val)
		}
	}
	return obj, obj.tree != "" && haveAuthor && haveCommitter
}

// parseIdent 解析 `Name <email> <unix> <tz>`。
func parseIdent(s string) (commitIdent, bool) {
	lt, gt := strings.IndexByte(s, '<'), strings.LastIndexByte(s, '>')
	if lt < 0 || gt < lt {
		return commitIdent{}, false
	}
	id := commitIdent{name: strings.TrimSpace(s[:lt]), email: s[lt+1 : gt], date: strings.TrimSpace(s[gt+1:])}
	return id, id.date != ""
}

// git 在工作目录里执行 git，返回合并输出。
func (p *prepareRun) git(extraEnv []string, args ...string) (string, error) {
	return p.gitIn(extraEnv, nil, args...)
}

func (p *prepareRun) gitIn(extraEnv []string, stdin *strings.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(p.ctx, "git", args...)
	cmd.Dir = p.req.Dir
	cmd.Env = append(p.env[:len(p.env):len(p.env)], extraEnv...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String() + stderr.String(), err
	}
	return stdout.String(), nil
}

// shell 在工作目录里用 `sh -c` 执行项目配置的命令（独立进程组，超时或 ctx 结束时杀掉整个组），返回输出尾部。
func (p *prepareRun) shell(script string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(p.ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = p.req.Dir
	cmd.Env = p.env
	out := &prepareTail{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if err != nil && ctx.Err() != nil && p.ctx.Err() == nil {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	return out.String(), err
}

// prepareTail 只保留最后 prepareOutputMax 字节的输出。
type prepareTail struct{ b []byte }

func (t *prepareTail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= prepareOutputMax {
		t.b = append(t.b[:0], p[len(p)-prepareOutputMax:]...)
		return n, nil
	}
	if over := len(t.b) + len(p) - prepareOutputMax; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	t.b = append(t.b, p...)
	return n, nil
}

func (t *prepareTail) String() string { return tailString(string(t.b)) }

// tailString 截取 s 的最后 prepareOutputMax 字节，并去掉截断处残缺的 UTF-8 前缀。
func tailString(s string) string {
	if len(s) > prepareOutputMax {
		s = s[len(s)-prepareOutputMax:]
	}
	for i := 0; i < len(s) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(s[i]) {
			return s[i:]
		}
	}
	return s
}

// gateCmd 是去掉首尾空白的门禁命令；未配置为空串。
func gateCmd(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
